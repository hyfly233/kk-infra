package client

import (
	"context"
	"fmt"
	"strconv"
)

type gpuContainer struct {
	RestartPolicy string `json:"restartPolicy"`
	Resources     struct {
		Requests map[string]string `json:"requests"`
		Limits   map[string]string `json:"limits"`
	} `json:"resources"`
}

func (c gpuContainer) request() (int32, error) {
	value, ok := c.Resources.Requests["nvidia.com/gpu"]
	if !ok {
		value = c.Resources.Limits["nvidia.com/gpu"]
	}
	if value == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid GPU request quantity %q", value)
	}
	return int32(n), nil
}

// Count all assigned, non-terminal Pods, including other tenants and terminating
// Pods. An init container's peak overlaps previously started native sidecars.
func (c *RealKubeClient) gpuRequestsByNode(ctx context.Context) (map[string]int32, error) {
	var pods struct {
		Items []struct {
			Spec struct {
				NodeName       string         `json:"nodeName"`
				Containers     []gpuContainer `json:"containers"`
				InitContainers []gpuContainer `json:"initContainers"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := c.do(ctx, "GET", "/api/v1/pods", nil, &pods); err != nil {
		return nil, fmt.Errorf("query GPU Pod requests: %w", err)
	}
	used := map[string]int32{}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == "" || pod.Status.Phase == "Succeeded" || pod.Status.Phase == "Failed" {
			continue
		}
		var app, sidecars, peak int32
		for _, container := range pod.Spec.Containers {
			n, err := container.request()
			if err != nil {
				return nil, err
			}
			app += n
		}
		for _, container := range pod.Spec.InitContainers {
			n, err := container.request()
			if err != nil {
				return nil, err
			}
			peak = max(peak, sidecars+n)
			if container.RestartPolicy == "Always" {
				sidecars += n
			}
		}
		used[pod.Spec.NodeName] += max(app+sidecars, peak)
	}
	return used, nil
}
