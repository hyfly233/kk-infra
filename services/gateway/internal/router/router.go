// Package router 模型路由：model 字段 → 后端服务 endpoint。
package router

import (
	"context"
	"errors"
	"sync"
)

// ErrRouteNotFound 模型无路由
var ErrRouteNotFound = errors.New("model route not found")

// Route 路由条目
type Route struct {
	Model        string `json:"model"`    // 模型名（= 部署服务名）
	Endpoint     string `json:"endpoint"` // 后端 base URL（如 http://qwen-demo:80）
	TenantID     string `json:"tenantId"` // 归属租户
	DeploymentID string `json:"deploymentId"`
}

// Store 保存路由，使 gateway 重启后能够恢复。
type Store interface {
	LoadRoutes(ctx context.Context) ([]*Route, error)
	SaveRoute(ctx context.Context, route *Route) error
	DeleteRoute(ctx context.Context, model string) error
}

// Table 路由表（线程安全）
type Table struct {
	mu     sync.RWMutex
	routes map[string]*Route // model → route
	store  Store
}

// NewTable 创建路由表
func NewTable() *Table {
	return &Table{routes: make(map[string]*Route)}
}

// NewTableWithStore 创建并恢复持久化路由表。
func NewTableWithStore(ctx context.Context, store Store) (*Table, error) {
	t := NewTable()
	t.store = store
	routes, err := store.LoadRoutes(ctx)
	if err != nil {
		return nil, err
	}
	for _, route := range routes {
		t.routes[route.Model] = route
	}
	return t, nil
}

// Register 注册/更新路由
func (t *Table) Register(ctx context.Context, r *Route) error {
	if t.store != nil {
		if err := t.store.SaveRoute(ctx, r); err != nil {
			return err
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.routes[r.Model] = r
	return nil
}

// Unregister 注销路由
func (t *Table) Unregister(ctx context.Context, model string) error {
	if t.store != nil {
		if err := t.store.DeleteRoute(ctx, model); err != nil {
			return err
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.routes, model)
	return nil
}

// Resolve 解析模型路由
func (t *Table) Resolve(model string) (*Route, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	r, ok := t.routes[model]
	if !ok {
		return nil, ErrRouteNotFound
	}
	return r, nil
}

// List 列出全部路由
func (t *Table) List() []*Route {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]*Route, 0, len(t.routes))
	for _, r := range t.routes {
		out = append(out, r)
	}
	return out
}
