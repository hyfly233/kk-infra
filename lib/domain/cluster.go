package domain

import "time"

// DeploymentGPUObservation counts assigned, non-terminal Pod requests, not
// utilization. Old and canary Pods are included; unassigned Pods are not.
type DeploymentGPUObservation struct {
	DeploymentID string `json:"deploymentId"`
	TenantID     string `json:"tenantId"`
	Namespace    string `json:"namespace"`
	NodeName     string `json:"nodeName"`
	GPUType      string `json:"gpuType"`
	GPUCount     int32  `json:"gpuCount"`
	// Nil identifies legacy Pods without a template generation label.
	TemplateGeneration *int64 `json:"templateGeneration,omitempty"`
}

type ClusterTelemetry struct {
	Status                 string                `json:"status"`
	Reason                 string                `json:"reason,omitempty"`
	CollectedAt            time.Time             `json:"collectedAt"`
	OldestSampleAgeSeconds *float64              `json:"oldestSampleAgeSeconds,omitempty"`
	Samples                []ClusterMetricSample `json:"samples,omitempty"`
}

type ClusterMetricSample struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	Value       float64           `json:"value"`
	EvaluatedAt float64           `json:"evaluatedAt"`
}

// VolcanoQueueCapacity preserves Kubernetes resource quantities. Queue capacity
// is not equivalent to instantly schedulable GPU capacity or a reservation.
type VolcanoQueueCapacity struct {
	Name       string            `json:"name"`
	State      string            `json:"state"`
	Capability map[string]string `json:"capability,omitempty"`
	Deserved   map[string]string `json:"deserved,omitempty"`
	Allocated  map[string]string `json:"allocated,omitempty"`
	Pending    int32             `json:"pending"`
	Running    int32             `json:"running"`
	Inqueue    int32             `json:"inqueue"`
}
