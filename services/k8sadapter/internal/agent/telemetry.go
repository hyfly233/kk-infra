package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"kk-infra/lib/domain"
)

const dcgmSelector = `{job="dcgm",__name__=~"DCGM_FI_DEV_GPU_UTIL|DCGM_FI_DEV_FB_USED|DCGM_FI_DEV_FB_FREE|DCGM_FI_DEV_XID_ERRORS"}`

type telemetryCollector struct {
	endpoint string
	http     *http.Client
}

func (r *Reporter) ConfigureTelemetry(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("agent Prometheus URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	r.telemetry = &telemetryCollector{endpoint: strings.TrimRight(endpoint, "/"), http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return nil
}

func (c *telemetryCollector) collect(ctx context.Context) *domain.ClusterTelemetry {
	result := &domain.ClusterTelemetry{Status: "unhealthy", CollectedAt: time.Now().UTC()}
	up, err := c.query(ctx, `up{job="dcgm"}`)
	if err != nil || len(up) == 0 {
		result.Reason = "Prometheus/DCGM targets unavailable"
		return result
	}
	targets := map[string]bool{}
	for _, s := range up {
		if s.Value != 1 {
			result.Reason = "DCGM scrape target is down"
			return result
		}
		if s.Labels["instance"] == "" {
			result.Reason = "DCGM target identity missing"
			return result
		}
		targets[s.Labels["instance"]] = false
	}
	metrics, err := c.query(ctx, dcgmSelector)
	if err != nil || len(metrics) == 0 {
		result.Reason = "DCGM metrics unavailable"
		return result
	}
	age, err := c.query(ctx, `max(time() - timestamp({job="dcgm",__name__=~"up|DCGM_FI_DEV_GPU_UTIL|DCGM_FI_DEV_FB_USED|DCGM_FI_DEV_FB_FREE|DCGM_FI_DEV_XID_ERRORS"}))`)
	if err != nil || len(age) != 1 || age[0].Value < 0 || age[0].Value > 90 {
		result.Reason = "DCGM samples stale or freshness unavailable"
		return result
	}
	// Require all four metrics for every exporter/GPU identity, rather than
	// treating a missing framebuffer measurement as zero.
	devices := map[string]map[string]bool{}
	for _, s := range metrics {
		if s.Value < 0 || s.Value > 1e15 {
			result.Reason = "DCGM metric invalid or unsupported"
			return result
		}
		if s.Name == "DCGM_FI_DEV_GPU_UTIL" && s.Value > 100 {
			result.Reason = "DCGM utilization out of range"
			return result
		}
		if s.Labels["instance"] == "" || (s.Labels["UUID"] == "" && s.Labels["gpu"] == "") {
			result.Reason = "DCGM device identity missing"
			return result
		}
		key := s.Labels["instance"] + "/" + s.Labels["UUID"] + "/" + s.Labels["gpu"]
		if _, ok := targets[s.Labels["instance"]]; !ok {
			result.Reason = "DCGM metric target missing from scrape status"
			return result
		}
		targets[s.Labels["instance"]] = true
		if devices[key] == nil {
			devices[key] = map[string]bool{}
		}
		devices[key][s.Name] = true
	}
	for _, names := range devices {
		for _, name := range []string{"DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_FB_USED", "DCGM_FI_DEV_FB_FREE", "DCGM_FI_DEV_XID_ERRORS"} {
			if !names[name] {
				result.Reason = "DCGM device metrics incomplete"
				return result
			}
		}
	}
	for _, observed := range targets {
		if !observed {
			result.Reason = "DCGM target metrics missing"
			return result
		}
	}
	result.Status, result.OldestSampleAgeSeconds = "healthy", &age[0].Value
	result.Samples = append(up, metrics...)
	return result
}

func (c *telemetryCollector) query(ctx context.Context, expression string) ([]domain.ClusterMetricSample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/v1/query?query="+url.QueryEscape(expression), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prometheus HTTP %d", res.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" || body.Data.ResultType != "vector" {
		return nil, fmt.Errorf("invalid Prometheus vector response")
	}
	result := make([]domain.ClusterMetricSample, 0, len(body.Data.Result))
	for _, item := range body.Data.Result {
		if len(item.Value) != 2 {
			return nil, fmt.Errorf("invalid Prometheus sample")
		}
		var timestamp float64
		var value string
		if json.Unmarshal(item.Value[0], &timestamp) != nil || json.Unmarshal(item.Value[1], &value) != nil {
			return nil, fmt.Errorf("invalid Prometheus sample encoding")
		}
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || timestamp <= 0 {
			return nil, fmt.Errorf("invalid Prometheus sample value")
		}
		result = append(result, domain.ClusterMetricSample{Name: item.Metric["__name__"], Labels: item.Metric, Value: n, EvaluatedAt: timestamp})
	}
	return result, nil
}
