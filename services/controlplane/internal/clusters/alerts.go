package clusters

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type Alert struct {
	Fingerprint string    `json:"fingerprint"`
	ClusterID   string    `json:"clusterId"`
	Kind        string    `json:"kind"`
	Severity    string    `json:"severity"`
	Reason      string    `json:"reason"`
	ObservedAt  time.Time `json:"observedAt"`
}

// Alerts are active snapshots derived from persisted heartbeats, not historical
// events. Stable fingerprints allow Prometheus/Alertmanager to deduplicate them.
func (s *Service) Alerts() ([]Alert, error) {
	items, err := s.repo.List()
	if err != nil {
		return nil, err
	}
	result := []Alert{}
	now := s.now()
	for _, c := range items {
		result = append(result, clusterAlerts(c, now)...)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Fingerprint < result[j].Fingerprint })
	return result, nil
}

func clusterAlerts(c Cluster, now time.Time) []Alert {
	last := c.CreatedAt
	if c.LastHeartbeat != nil {
		last = *c.LastHeartbeat
	}
	alert := func(kind, severity, reason string, at time.Time) Alert {
		return Alert{Fingerprint: c.ID + "/" + kind, ClusterID: c.ID, Kind: kind, Severity: severity, Reason: reason, ObservedAt: at}
	}
	if now.Sub(last) > 90*time.Second {
		return []Alert{alert("offline", "critical", "集群超过 90 秒未收到心跳", last.Add(90*time.Second))}
	}
	result := []Alert{}
	if c.HealthStatus == "unhealthy" {
		result = append(result, alert("unhealthy", "critical", "集群 agent 报告容量采集或节点健康异常", last))
	}
	if c.Telemetry != nil && (c.Telemetry.Status != "healthy" || now.Sub(c.Telemetry.CollectedAt) > 90*time.Second) {
		result = append(result, alert("telemetry", "warning", "集群 Prometheus/DCGM 遥测不可用或已过期", c.Telemetry.CollectedAt))
	}
	return result
}

func (s *Service) AlertMetrics() (string, error) {
	items, err := s.repo.List()
	if err != nil {
		return "", err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	var out strings.Builder
	out.WriteString("# HELP carrot_cluster_alert Active cluster condition (1 active, 0 inactive).\n# TYPE carrot_cluster_alert gauge\n")
	now := s.now()
	for _, c := range items {
		active := map[string]bool{}
		for _, a := range clusterAlerts(c, now) {
			active[a.Kind] = true
		}
		for _, kind := range []string{"offline", "unhealthy", "telemetry"} {
			value := 0
			if active[kind] {
				value = 1
			}
			fmt.Fprintf(&out, "carrot_cluster_alert{cluster_id=%q,kind=%q} %d\n", c.ID, kind, value)
		}
	}
	return out.String(), nil
}
