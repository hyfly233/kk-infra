package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUserVerifierValidatesCurrentIdentityAndFailsClosed(t *testing.T) {
	token, _ := IssueAccessToken("secret", "u", "tenant-a", RoleDeveloper, "controlplane", time.Now(), time.Minute)
	c, _ := ParseAccessToken("secret", token, "controlplane")
	response := map[string]any{"active": true, "sub": "u", "tenantId": "tenant-a", "role": RoleDeveloper, "exp": c.ExpiresAt.Unix()}
	status := 200
	calls := 0
	redirectCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectCalls++; w.WriteHeader(200) }))
	defer target.Close()
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/auth/introspect" || r.Method != "POST" {
			t.Error("incorrect endpoint")
		}
		if err := AuthenticateService("secret", BearerToken(r), "controlplane", "gateway"); err != nil {
			t.Error(err)
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token != token {
			t.Error("user credential missing from body")
		}
		w.Header().Set("Location", target.URL)
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(response)
	}))
	defer cp.Close()
	v := NewUserVerifier(cp.URL, "secret", "gateway")
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"active", "sub", "tenantId", "role", "exp"} {
		old := response[field]
		response[field] = nil
		if _, err := v.Verify(context.Background(), token); err == nil {
			t.Fatalf("accepted invalid %s", field)
		}
		response[field] = old
	}
	response["active"] = false
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("cached active identity")
	}
	for _, code := range []int{401, 500, 302} {
		status = code
		if _, err := v.Verify(context.Background(), token); err == nil {
			t.Fatalf("accepted status %d", code)
		}
	}
	if redirectCalls != 0 || calls != 10 {
		t.Fatalf("calls=%d redirects=%d", calls, redirectCalls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Verify(ctx, token); err == nil {
		t.Fatal("ignored cancellation")
	}
	cp.Close()
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("unreachable controlplane accepted")
	}
	var missing *UserVerifier
	if _, err := missing.Verify(context.Background(), token); err == nil {
		t.Fatal("missing verifier accepted")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "not-json") }))
	defer bad.Close()
	if _, err := NewUserVerifier(bad.URL, "secret", "gateway").Verify(context.Background(), token); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
