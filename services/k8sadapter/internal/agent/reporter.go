// Package agent reports the local adapter's capacity to the control plane.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"kk-infra/lib/domain"
)

type gpuSource interface {
	ListGPUNodes(context.Context) ([]domain.GPUResource, error)
}

type capacity struct {
	GPUType     string `json:"gpuType"`
	Total       int32  `json:"total"`
	Allocatable int32  `json:"allocatable"`
	Used        int32  `json:"used"`
}

type snapshot struct {
	HealthStatus string     `json:"healthStatus"`
	GPUCapacity  []capacity `json:"gpuCapacity"`
}

type Reporter struct {
	source              gpuSource
	endpoint, tokenFile string
	client              *http.Client
}

func New(source gpuSource, controlplaneURL, clusterID, tokenFile string) (*Reporter, error) {
	u, err := url.Parse(controlplaneURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("controlplane URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`).MatchString(clusterID) || tokenFile == "" || source == nil {
		return nil, fmt.Errorf("valid cluster ID, token file and GPU source are required")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/clusters/" + clusterID + "/heartbeat"
	return &Reporter{source: source, endpoint: u.String(), tokenFile: tokenFile,
		client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Report reads the token on every request so Secret volume rotation needs no restart.
// Collection failure explicitly replaces previously healthy capacity with unhealthy.
func (r *Reporter) Report(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := os.ReadFile(r.tokenFile)
	if err != nil {
		return fmt.Errorf("read cluster agent token: %w", err)
	}
	if strings.TrimSpace(string(token)) == "" {
		return fmt.Errorf("cluster agent token file is empty")
	}
	collectCtx, cancelCollection := context.WithTimeout(ctx, 10*time.Second)
	nodes, collectErr := r.source.ListGPUNodes(collectCtx)
	cancelCollection()
	payload := snapshot{HealthStatus: "healthy", GPUCapacity: []capacity{}}
	byType := map[string]capacity{}
	if collectErr != nil {
		payload.HealthStatus = "unhealthy"
	} else {
		for _, node := range nodes {
			if node.Health != domain.GPUHealthHealthy || node.GPUType == "" || node.Total < 0 || node.Allocatable < 0 || node.Used < 0 || node.Used > node.Allocatable || node.Allocatable > node.Total {
				payload.HealthStatus = "unhealthy"
				break
			}
			c := byType[node.GPUType]
			c.GPUType = node.GPUType
			c.Total += node.Total
			c.Allocatable += node.Allocatable
			c.Used += node.Used
			byType[node.GPUType] = c
		}
	}
	if payload.HealthStatus == "healthy" {
		for _, c := range byType {
			payload.GPUCapacity = append(payload.GPUCapacity, c)
		}
		sort.Slice(payload.GPUCapacity, func(i, j int) bool { return payload.GPUCapacity[i].GPUType < payload.GPUCapacity[j].GPUType })
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	res, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("send cluster heartbeat: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("cluster heartbeat rejected: HTTP %d", res.StatusCode)
	}
	var result struct {
		Code int `json:"code"`
		Data struct {
			Accepted bool `json:"accepted"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&result); err != nil || result.Code != 0 || !result.Data.Accepted {
		return fmt.Errorf("invalid cluster heartbeat acknowledgement")
	}
	if collectErr != nil {
		return fmt.Errorf("reported unhealthy cluster: %w", collectErr)
	}
	return nil
}

// Run reports immediately, then every 30 seconds (below the 90-second stale cutoff).
func (r *Reporter) Run(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := r.Report(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("cluster heartbeat failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
