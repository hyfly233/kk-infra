package k8s

import (
	"bytes"
	"io"
	"os"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestManagementIngressManifestAccessMatrix(t *testing.T) {
	data, err := os.ReadFile("../../../../deployments/k8s/15-management-access.yaml")
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var policies []networkingv1.NetworkPolicy
	for {
		var p networkingv1.NetworkPolicy
		if err := decoder.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if p.Kind != "NetworkPolicy" || p.Namespace != "carrot-ai" || p.APIVersion != "networking.k8s.io/v1" {
			t.Fatalf("invalid policy: %+v", p.TypeMeta)
		}
		if len(p.Spec.PolicyTypes) != 1 || p.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
			t.Fatal("unexpected policy direction")
		}
		policies = append(policies, p)
	}
	if len(policies) != 5 {
		t.Fatalf("policies=%d", len(policies))
	}
	allowed := func(source, namespace, target string, port int32) bool {
		isolated := false
		permit := false
		for _, p := range policies {
			dst, err := metav1.LabelSelectorAsSelector(&p.Spec.PodSelector)
			if err != nil {
				t.Fatal(err)
			}
			if !dst.Matches(labels.Set{"app": target}) {
				continue
			}
			isolated = true
			for _, rule := range p.Spec.Ingress {
				portAllowed := false
				for _, entry := range rule.Ports {
					if entry.Port != nil && entry.Port.IntVal == port && entry.Protocol != nil && *entry.Protocol == "TCP" {
						portAllowed = true
					}
				}
				for _, peer := range rule.From {
					if peer.IPBlock != nil || peer.NamespaceSelector != nil || peer.PodSelector == nil {
						t.Fatal("unexpected broad source")
					}
					src, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
					if err != nil {
						t.Fatal(err)
					}
					permit = permit || (portAllowed && namespace == p.Namespace && src.Matches(labels.Set{"app": source}))
				}
			}
		}
		return !isolated || permit
	}
	ports := map[string]int32{"controlplane": 8080, "modelregistry": 8081, "k8sadapter": 8082, "pipeline": 8086}
	expected := map[string]map[string]bool{
		"controlplane":  {"console": true, "gateway": true, "modelregistry": true, "pipeline": true, "k8sadapter": true, "observability": true},
		"modelregistry": {"console": true, "controlplane": true, "pipeline": true},
		"k8sadapter":    {"controlplane": true}, "pipeline": {"console": true},
	}
	for target, port := range ports {
		for _, source := range []string{"console", "gateway", "controlplane", "modelregistry", "pipeline", "k8sadapter", "observability", "unknown"} {
			if got := allowed(source, "carrot-ai", target, port); got != expected[target][source] {
				t.Fatalf("%s -> %s: %v", source, target, got)
			}
			if allowed(source, "tenant-a", target, port) || allowed(source, "carrot-ai", target, 9999) {
				t.Fatalf("unexpected namespace/port access: %s -> %s", source, target)
			}
		}
	}
}
