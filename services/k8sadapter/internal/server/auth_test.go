package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	platformauth "kk-infra/lib/auth"
	"kk-infra/services/k8sadapter/internal/client"
)

func TestAdapterRejectsUnauthorizedIdentityBeforeDispatch(t *testing.T) {
	const secret = "adapter-test-secret"
	// A nil backend makes accidental dispatch of rejected requests fail the test.
	s := NewServer(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetAuthSecret(secret)
	user, _ := platformauth.IssueAccessToken(secret, "admin", "tenant-a", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Minute)
	wrongAudience, _ := platformauth.IssueServiceToken(secret, "controlplane", "gateway")
	wrongCaller, _ := platformauth.IssueServiceToken(secret, "pipeline", "k8sadapter")
	wrongSignature, _ := platformauth.IssueServiceToken("other", "controlplane", "k8sadapter")
	expired, _ := platformauth.IssueAccessToken(secret, "controlplane", "", platformauth.RoleViewer, "service:k8sadapter", time.Now().Add(-time.Hour), time.Minute)
	for _, token := range []string{"", "forged", user, wrongAudience, wrongCaller, wrongSignature, expired} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			r := httptest.NewRequest(method, "/v1/deployments", nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("method=%s returned %d", method, w.Code)
			}
		}
	}
}

func TestAdapterAcceptsControlplaneServiceIdentity(t *testing.T) {
	s := NewServer(client.NewFakeKubeClient(client.DefaultFakeNodes()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetAuthSecret("secret")
	token, err := platformauth.IssueServiceToken("secret", "controlplane", "k8sadapter")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/resources/gpus", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
