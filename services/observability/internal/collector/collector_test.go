package collector

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kk-infra/lib/apitypes"
	platformauth "kk-infra/lib/auth"
)

func TestCollectorUsesRestrictedControlplaneIdentity(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := platformauth.AuthenticateService("secret", platformauth.BearerToken(r), "controlplane", "observability"); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`{"code":0,"data":{"nodes":[]}}`))
	}))
	defer api.Close()
	c := NewGPUCollector(api.URL, slog.Default(), time.Minute)
	c.SetServiceSecret("secret")
	called := false
	c.OnGPU = func(apitypes.GPUResourcesView) { called = true }
	c.collect()
	if !called {
		t.Fatal("collector failed authenticated fetch")
	}
}
