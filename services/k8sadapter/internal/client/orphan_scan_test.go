package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFakeScanKeepsSameNameAcrossNamespaces(t *testing.T) {
	f := NewFakeKubeClient(DefaultFakeNodes())
	for _, ns := range []string{"tenant-a", "tenant-b"} {
		f.deploys[ns+"/shared"] = &FakeDeployment{Spec: DeploymentSpec{DeploymentID: ns, Name: "shared", Namespace: ns, Replicas: 1}}
	}
	all, err := f.ListDeployments(context.Background(), "*")
	if err != nil || len(all) != 2 || all[0].Namespace == all[1].Namespace {
		t.Fatalf("all namespaces: %+v %v", all, err)
	}
	one, err := f.ListDeployments(context.Background(), "tenant-a")
	if err != nil || len(one) != 1 || one[0].Namespace != "tenant-a" {
		t.Fatalf("namespace filter: %+v %v", one, err)
	}
}

func TestRealScanUsesAllNamespaceEndpointAndItemNamespace(t *testing.T) {
	for _, progressive := range []bool{false, true} {
		t.Run(fmt.Sprint(progressive), func(t *testing.T) {
			root := "/apis/apps/v1/deployments"
			if progressive {
				root = "/apis/argoproj.io/v1alpha1/rollouts"
			}
			seen := map[string]bool{}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == root {
					if r.URL.Query().Get("labelSelector") != "carrot.ai/managed-by=carrot" {
						t.Error("scan omitted managed label filter")
					}
					fmt.Fprint(w, `{"items":[{"metadata":{"name":"shared","namespace":"tenant-a"}},{"metadata":{"name":"shared","namespace":"tenant-b"}}]}`)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/shared") {
					seen[r.URL.Path] = true
					fmt.Fprint(w, `{"metadata":{"labels":{"carrot.ai/deployment-id":"shared"}},"spec":{"replicas":1},"status":{"phase":"Healthy","readyReplicas":1,"availableReplicas":1}}`)
					return
				}
				fmt.Fprint(w, `{"items":[]}`)
			}))
			defer api.Close()
			c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client(), progressiveEnabled: progressive}
			all, err := c.ListDeployments(context.Background(), "*")
			if err != nil || len(all) != 2 || len(seen) != 2 || all[0].Namespace != "tenant-a" || all[1].Namespace != "tenant-b" {
				t.Fatalf("scan: %+v paths=%v err=%v", all, seen, err)
			}
		})
	}
}
