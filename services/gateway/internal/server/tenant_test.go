package server

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	platformauth "kk-infra/lib/auth"
	"kk-infra/services/gateway/internal/auth"
	"kk-infra/services/gateway/internal/router"
)

func TestTenantServingCheckFailClosedAndNoPositiveCache(t *testing.T) {
	body := `{"code":0,"data":{"tenantId":"tenant-a","active":true}}`
	status := http.StatusOK
	calls := 0
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := platformauth.AuthenticateService("secret", platformauth.BearerToken(r), "controlplane", "gateway"); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/internal/tenants/tenant-a/serving-status" {
			t.Error(r.URL.Path)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer cp.Close()
	keys := auth.NewManager()
	key, err := keys.Issue("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(keys, router.NewTable(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetAuthSecret("secret")
	s.ConfigureTenantCheck(cp.URL, "secret")
	call := func(want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", "Bearer "+key.Key)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
		}
	}
	call(200)
	body = `{"code":0,"data":{"tenantId":"tenant-a","active":false}}`
	call(403)
	for _, invalid := range []string{`{}`, `{"code":1,"data":{"tenantId":"tenant-a","active":true}}`, `{"code":0,"data":{"tenantId":"tenant-b","active":true}}`, `not-json`} {
		body = invalid
		call(503)
	}
	status = 500
	call(503)
	status = 302
	call(503)
	if calls != 8 {
		t.Fatalf("unexpected calls: %d", calls)
	}
	cp.Close()
	call(503)
	s.tenantChecker = nil
	call(503)
}
