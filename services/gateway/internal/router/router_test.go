package router

import (
	"context"
	"testing"
)

type memoryStore struct{ routes map[string]*Route }

func (s *memoryStore) LoadRoutes(context.Context) ([]*Route, error) {
	result := make([]*Route, 0, len(s.routes))
	for _, route := range s.routes {
		result = append(result, route)
	}
	return result, nil
}

func (s *memoryStore) SaveRoute(_ context.Context, route *Route) error {
	s.routes[route.Model] = route
	return nil
}

func (s *memoryStore) DeleteRoute(_ context.Context, model string) error {
	delete(s.routes, model)
	return nil
}

func TestTableRestoresAndPersistsRoutes(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{routes: map[string]*Route{
		"existing": {Model: "existing", Endpoint: "http://existing", TenantID: "tenant-a", StableEndpoint: "http://stable", CanaryEndpoint: "http://canary", RolloutStatus: "Healthy"},
	}}
	table, err := NewTableWithStore(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if route, err := table.Resolve("existing"); err != nil || route.TenantID != "tenant-a" {
		t.Fatalf("恢复路由失败: route=%+v err=%v", route, err)
	}
	if route, _ := table.Resolve("existing"); route.RolloutStatus != "Healthy" || route.StableEndpoint == "" || route.CanaryEndpoint == "" {
		t.Fatalf("rollout metadata restore failed: %+v", route)
	}
	if err := table.Register(ctx, &Route{Model: "new", Endpoint: "http://new", TenantID: "tenant-b"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.routes["new"]; !ok {
		t.Fatal("注册路由未持久化")
	}
	if err := table.Unregister(ctx, "existing"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.routes["existing"]; ok {
		t.Fatal("撤销路由未持久化")
	}
}
