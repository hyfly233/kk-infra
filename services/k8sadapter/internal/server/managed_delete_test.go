package server

import (
	"context"
	"kk-infra/services/k8sadapter/internal/client"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type managedDeleteStub struct {
	client.KubeClient
	calls int
}

func (s *managedDeleteStub) DeleteManagedDeployment(_ context.Context, name, namespace, id string) error {
	s.calls++
	if name != "model" || namespace != "tenant-a" || id != "d1" {
		panic("wrong managed identity")
	}
	return nil
}

func TestManagedDeleteHTTPRequiresIdentityAndCapability(t *testing.T) {
	kube := &managedDeleteStub{}
	s := NewServer(kube, slog.Default())
	call := func(handler http.Handler, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	if res := call(s.Handler(), "/v1/deployments/model/managed?namespace=tenant-a"); res.Code != http.StatusBadRequest || kube.calls != 0 {
		t.Fatal("missing identity accepted")
	}
	path := "/v1/deployments/model/managed?namespace=tenant-a&deploymentId=d1"
	if res := call(s.Handler(), path); res.Code != http.StatusOK || kube.calls != 1 {
		t.Fatal("managed contract failed")
	}
	unsupported := NewServer(struct{ client.KubeClient }{}, slog.Default())
	if res := call(unsupported.Handler(), path); res.Code == http.StatusOK {
		t.Fatal("unsupported client fell back to unsafe deletion")
	}
}
