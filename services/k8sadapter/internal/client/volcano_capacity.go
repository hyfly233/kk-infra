package client

import (
	"context"
	"fmt"
	"sort"

	"kk-infra/lib/domain"
)

// ListVolcanoQueues is optional when Volcano is disabled. If enabled, a missing
// CRD or denied API request is an error, not an empty successful capacity report.
func (c *RealKubeClient) ListVolcanoQueues(ctx context.Context) ([]domain.VolcanoQueueCapacity, error) {
	if !c.volcanoEnabled {
		return nil, nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Capability map[string]string `json:"capability"`
				Deserved   map[string]string `json:"deserved"`
			} `json:"spec"`
			Status struct {
				State     string            `json:"state"`
				Allocated map[string]string `json:"allocated"`
				Pending   int32             `json:"pending"`
				Running   int32             `json:"running"`
				Inqueue   int32             `json:"inqueue"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := c.do(ctx, "GET", "/apis/scheduling.volcano.sh/v1beta1/queues", nil, &list); err != nil {
		return nil, fmt.Errorf("query Volcano queue capacity: %w", err)
	}
	result := make([]domain.VolcanoQueueCapacity, 0, len(list.Items))
	for _, q := range list.Items {
		state := q.Status.State
		if state == "" {
			state = "Unknown"
		}
		result = append(result, domain.VolcanoQueueCapacity{Name: q.Metadata.Name, State: state, Capability: q.Spec.Capability, Deserved: q.Spec.Deserved, Allocated: q.Status.Allocated, Pending: q.Status.Pending, Running: q.Status.Running, Inqueue: q.Status.Inqueue})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
