package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	platformauth "kk-infra/lib/auth"
)

func (s *Server) ConfigureTenantCheck(controlplaneURL, secret string) {
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	s.tenantChecker = func(ctx context.Context, tenant string) (bool, error) {
		token, err := platformauth.IssueServiceToken(secret, "gateway", "controlplane")
		if err != nil {
			return false, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(controlplaneURL, "/")+"/internal/tenants/"+url.PathEscape(tenant)+"/serving-status", nil)
		if err != nil {
			return false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		var result struct {
			Code int `json:"code"`
			Data struct {
				TenantID string `json:"tenantId"`
				Active   *bool  `json:"active"`
			} `json:"data"`
		}
		if resp.StatusCode != http.StatusOK {
			return false, fmt.Errorf("tenant query status %d", resp.StatusCode)
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
			return false, err
		}
		if result.Code != 0 || result.Data.TenantID != tenant || result.Data.Active == nil {
			return false, fmt.Errorf("invalid tenant status response")
		}
		return *result.Data.Active, nil
	}
}
