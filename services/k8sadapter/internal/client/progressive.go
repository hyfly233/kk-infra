package client

import (
	"fmt"

	"kk-infra/services/k8sadapter/internal/k8s"
)

type progressiveManifests struct {
	Rollout          map[string]any
	StableService    *k8s.Service
	CanaryService    *k8s.Service
	VirtualService   map[string]any
	AnalysisTemplate map[string]any
}

func renderProgressiveManifests(spec *DeploymentSpec, base *deployManifests, prometheusURL string) (*progressiveManifests, error) {
	if spec == nil || base == nil || base.Deployment == nil || base.Service == nil {
		return nil, fmt.Errorf("rollout 渲染参数不完整")
	}
	if prometheusURL == "" {
		return nil, fmt.Errorf("rollout 要求 prometheus-url")
	}
	name, namespace := spec.Name, base.Deployment.Metadata.Namespace
	labels := base.Deployment.Metadata.Labels
	stableName, canaryName := name+"-stable", name+"-canary"
	stable := *base.Service
	stable.Metadata = k8s.ObjectMeta{Name: stableName, Namespace: namespace, Labels: labels}
	canary := *base.Service
	canary.Metadata = k8s.ObjectMeta{Name: canaryName, Namespace: namespace, Labels: labels}
	analysisName := name + "-analysis"
	virtualName := name + "-traffic"
	routeName := "primary"

	analysis := map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "AnalysisTemplate",
		"metadata": map[string]any{"name": analysisName, "namespace": namespace, "labels": labels},
		"spec": map[string]any{
			"args": []map[string]any{{"name": "deployment-id"}, {"name": "tenant-id"}},
			"metrics": []map[string]any{
				analysisMetric("error-rate", `result[0] < 0.05`, prometheusURL, `sum(rate(carrot_inference_errors_total{deployment_id="{{args.deployment-id}}",tenant_id="{{args.tenant-id}}"}[2m])) / clamp_min(sum(rate(carrot_inference_requests_total{deployment_id="{{args.deployment-id}}",tenant_id="{{args.tenant-id}}"}[2m])), 1)`),
				analysisMetric("ttft", `result[0] < 2000`, prometheusURL, `max(carrot_inference_ttft_ms{deployment_id="{{args.deployment-id}}",tenant_id="{{args.tenant-id}}"})`),
				analysisMetric("throughput", `result[0] > 1`, prometheusURL, `sum(rate(carrot_inference_output_tokens_total{deployment_id="{{args.deployment-id}}",tenant_id="{{args.tenant-id}}"}[2m]))`),
			},
		},
	}
	virtual := map[string]any{
		"apiVersion": "networking.istio.io/v1beta1", "kind": "VirtualService",
		"metadata": map[string]any{"name": virtualName, "namespace": namespace, "labels": labels},
		"spec": map[string]any{"hosts": []string{name}, "http": []map[string]any{{"name": routeName, "route": []map[string]any{
			{"destination": map[string]any{"host": stableName}, "weight": 100}, {"destination": map[string]any{"host": canaryName}, "weight": 0},
		}}}},
	}
	analysisStep := map[string]any{"analysis": map[string]any{"templates": []map[string]any{{"templateName": analysisName}}, "args": []map[string]any{{"name": "deployment-id", "value": spec.DeploymentID}, {"name": "tenant-id", "value": spec.Labels["carrot.ai/tenant-id"]}}}}
	rollout := map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
		"metadata": map[string]any{"name": name, "namespace": namespace, "labels": labels},
		"spec": map[string]any{
			"replicas": spec.Replicas, "selector": base.Deployment.Spec.Selector, "template": base.Deployment.Spec.Template,
			"strategy": map[string]any{"canary": map[string]any{
				"stableService": stableName, "canaryService": canaryName,
				"trafficRouting": map[string]any{"istio": map[string]any{"virtualService": map[string]any{"name": virtualName, "routes": []string{routeName}}}},
				"steps":          []map[string]any{{"setWeight": 10}, {"pause": map[string]any{"duration": "30s"}}, analysisStep, {"setWeight": 25}, analysisStep, {"setWeight": 50}, analysisStep, {"setWeight": 100}},
			}},
		},
	}
	return &progressiveManifests{Rollout: rollout, StableService: &stable, CanaryService: &canary, VirtualService: virtual, AnalysisTemplate: analysis}, nil
}

func analysisMetric(name, success, address, query string) map[string]any {
	return map[string]any{"name": name, "interval": "1m", "count": 3, "failureLimit": 1, "successCondition": success, "provider": map[string]any{"prometheus": map[string]any{"address": address, "query": query}}}
}
