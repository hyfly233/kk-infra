package clusters

import (
	"strings"
	"testing"
	"time"

	"kk-infra/lib/domain"
)

func TestAlertsTriggerWithoutDeploymentsAndResolveAfterHeartbeat(t *testing.T) {
	repo := NewMemoryRepository()
	s, _ := NewService(repo, []byte("0123456789abcdef0123456789abcdef"))
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	if _, err := s.Register("gpu-west", "west", "https://k8s.example.test", "http://adapter:8082", "secret", nil, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	alerts, err := s.Alerts()
	if err != nil || len(alerts) != 0 {
		t.Fatalf("registration grace not respected: %+v %v", alerts, err)
	}
	now = now.Add(91 * time.Second)
	alerts, err = s.Alerts()
	if err != nil || len(alerts) != 1 || alerts[0].Kind != "offline" || alerts[0].Fingerprint != "gpu-west/offline" {
		t.Fatalf("missing offline alert: %+v %v", alerts, err)
	}
	again, _ := s.Alerts()
	if len(again) != 1 || again[0] != alerts[0] {
		t.Fatal("repeated reads duplicated or changed offline condition")
	}
	// A new service instance derives the same state from persisted heartbeats.
	restarted, _ := NewService(repo, []byte("0123456789abcdef0123456789abcdef"))
	restarted.now = s.now
	resumed, _ := restarted.Alerts()
	if len(resumed) != 1 || resumed[0] != alerts[0] {
		t.Fatal("restart lost offline alert")
	}
	if err := s.Report("gpu-west", "healthy", nil); err != nil {
		t.Fatal(err)
	}
	alerts, _ = s.Alerts()
	if len(alerts) != 0 {
		t.Fatalf("heartbeat did not resolve alert: %+v", alerts)
	}
	metrics, err := s.AlertMetrics()
	if err != nil || !strings.Contains(metrics, `carrot_cluster_alert{cluster_id="gpu-west",kind="offline"} 0`) {
		t.Fatalf("metrics did not resolve offline condition: %s %v", metrics, err)
	}
	if err := s.ReportSnapshot("gpu-west", "unhealthy", nil, nil, &domain.ClusterTelemetry{Status: "unhealthy", CollectedAt: now}); err != nil {
		t.Fatal(err)
	}
	alerts, _ = s.Alerts()
	if len(alerts) != 2 {
		t.Fatalf("health and telemetry alerts not independent: %+v", alerts)
	}
	now = now.Add(91 * time.Second)
	alerts, _ = s.Alerts()
	if len(alerts) != 1 || alerts[0].Kind != "offline" {
		t.Fatalf("stale snapshots generated duplicate downstream alerts: %+v", alerts)
	}
}
