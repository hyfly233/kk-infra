package sink

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	platformauth "kk-infra/lib/auth"
)

func TestMetricsSinkSignsGatewayServiceIdentity(t *testing.T) {
	called := make(chan error, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called <- platformauth.AuthenticateService("secret", platformauth.BearerToken(r), "observability", "gateway")
		w.WriteHeader(200)
	}))
	defer api.Close()
	s := NewHTTPSink(api.URL, slog.Default())
	s.SetServiceSecret("secret")
	s.Record("tenant-a", "d1", "m", 10, 1, 5, 2, false)
	select {
	case err := <-called:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("metrics were not delivered")
	}
}
