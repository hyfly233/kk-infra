package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"kk-infra/lib/domain"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type NotebookWorkspace struct {
	TenantID string `json:"tenantId"`
	OwnerID  string `json:"ownerId"`
	Status   string `json:"status"`
	URL      string `json:"url,omitempty"`
}

type NotebookHubClient struct {
	baseURL, publicURL, token string
	http                      *http.Client
}

func NewNotebookHubClient(baseURL, publicURL, token string) (*NotebookHubClient, error) {
	for _, raw := range []string{baseURL, publicURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("invalid notebook hub URL")
		}
	}
	if token == "" {
		return nil, fmt.Errorf("notebook hub service token required")
	}
	return &NotebookHubClient{baseURL: strings.TrimRight(baseURL, "/") + "/hub/api", publicURL: strings.TrimRight(publicURL, "/"), token: token, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *NotebookHubClient) request(ctx context.Context, method, path string, body any, out any) (int, error) {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return 0, err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	r.Header.Set("Authorization", "token "+c.token)
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(r)
	if err != nil {
		return 0, fmt.Errorf("notebook hub unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("notebook hub returned HTTP %d", resp.StatusCode)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("invalid notebook hub response")
		}
	}
	return resp.StatusCode, nil
}

func (c *NotebookHubClient) Get(ctx context.Context, tenant, user string) (*NotebookWorkspace, error) {
	w := &NotebookWorkspace{TenantID: tenant, OwnerID: user, Status: "ABSENT"}
	var record struct {
		Name    string `json:"name"`
		Servers map[string]struct {
			Ready   bool   `json:"ready"`
			Pending string `json:"pending"`
		} `json:"servers"`
	}
	name := domain.NotebookUsername(tenant, user)
	status, err := c.request(ctx, http.MethodGet, "/users/"+name, nil, &record)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return w, nil
	}
	if record.Name != name {
		return nil, fmt.Errorf("notebook owner mismatch")
	}
	server, ok := record.Servers["workspace"]
	if !ok {
		return w, nil
	}
	w.Status = "STOPPED"
	switch server.Pending {
	case "spawn":
		w.Status = "STARTING"
	case "stop":
		w.Status = "STOPPING"
	default:
		if server.Ready {
			w.Status = "RUNNING"
		}
	}
	if w.Status == "RUNNING" {
		w.URL = c.publicURL + "/user/" + name + "/workspace/lab"
	}
	return w, nil
}

func (c *NotebookHubClient) Start(ctx context.Context, tenant, user, grant string) (*NotebookWorkspace, error) {
	w, err := c.Get(ctx, tenant, user)
	if err != nil {
		return nil, err
	}
	if w.Status == "RUNNING" || w.Status == "STARTING" {
		return w, nil
	}
	if w.Status == "STOPPING" {
		return nil, fmt.Errorf("notebook is stopping")
	}
	name := domain.NotebookUsername(tenant, user)
	// Create the Hub user if absent; a concurrent create may return Conflict.
	var record struct {
		Name string `json:"name"`
	}
	status, err := c.request(ctx, http.MethodGet, "/users/"+name, nil, &record)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		status, err = c.request(ctx, http.MethodPost, "/users/"+name, map[string]bool{"admin": false}, nil)
		if err != nil && status != http.StatusConflict {
			return nil, err
		}
	}
	status, err = c.request(ctx, http.MethodPost, "/users/"+name+"/servers/workspace", map[string]string{"platform_spawn_token": grant}, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusCreated && status != http.StatusAccepted {
		return nil, fmt.Errorf("unexpected notebook spawn status")
	}
	if status == http.StatusCreated {
		return c.Get(ctx, tenant, user)
	}
	w.Status = "STARTING"
	return w, nil
}

func (c *NotebookHubClient) Delete(ctx context.Context, tenant, user string) (*NotebookWorkspace, error) {
	w, err := c.Get(ctx, tenant, user)
	if err != nil {
		return nil, err
	}
	if w.Status == "ABSENT" || w.Status == "STOPPING" {
		return w, nil
	}
	status, err := c.request(ctx, http.MethodDelete, "/users/"+domain.NotebookUsername(tenant, user)+"/servers/workspace", map[string]bool{"remove": true}, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusAccepted && status != http.StatusNoContent && status != http.StatusNotFound {
		return nil, fmt.Errorf("unexpected notebook delete status")
	}
	w.URL = ""
	w.Status = "ABSENT"
	if status == http.StatusAccepted {
		w.Status = "STOPPING"
	}
	return w, nil
}
