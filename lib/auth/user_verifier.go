package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// UserVerifier checks the current membership, not only the token signature.
type UserVerifier struct {
	url, secret, caller string
	http                *http.Client
}

func NewUserVerifier(baseURL, secret, caller string) *UserVerifier {
	return &UserVerifier{url: strings.TrimRight(baseURL, "/") + "/internal/auth/introspect", secret: secret, caller: caller,
		http: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (v *UserVerifier) Verify(ctx context.Context, token string) (*Claims, error) {
	if v == nil {
		return nil, fmt.Errorf("online user verification required")
	}
	claims, err := ParseAccessToken(v.secret, token, "controlplane")
	if err != nil {
		return nil, err
	}
	serviceToken, err := IssueServiceToken(v.secret, v.caller, "controlplane")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+serviceToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user verification status %d", resp.StatusCode)
	}
	var result struct {
		Active   bool   `json:"active"`
		Subject  string `json:"sub"`
		TenantID string `json:"tenantId"`
		Role     Role   `json:"role"`
		Exp      int64  `json:"exp"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return nil, err
	}
	if !result.Active || result.Subject != claims.Subject || result.TenantID != claims.TenantID || result.Role != claims.Role || result.Exp != claims.ExpiresAt.Unix() {
		return nil, fmt.Errorf("current user identity unavailable")
	}
	return claims, nil
}
