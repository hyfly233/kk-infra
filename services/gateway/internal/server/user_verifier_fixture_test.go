package server

import (
	"encoding/json"
	platformauth "kk-infra/lib/auth"
	"net/http"
	"net/http/httptest"
	"testing"
)

// This stub isolates local role/ownership tests; binary E2E checks live membership.
func userVerifierFixture(t *testing.T, secret, caller string) *platformauth.UserVerifier {
	t.Helper()
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := platformauth.AuthenticateService(secret, platformauth.BearerToken(r), "controlplane", caller); err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		c, err := platformauth.ParseAccessToken(secret, body.Token, "controlplane")
		if err != nil {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": c.Subject, "tenantId": c.TenantID, "role": c.Role, "exp": c.ExpiresAt.Unix()})
	}))
	t.Cleanup(cp.Close)
	return platformauth.NewUserVerifier(cp.URL, secret, caller)
}
