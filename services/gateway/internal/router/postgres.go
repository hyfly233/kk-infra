package router

import (
	"context"
	"database/sql"
)

// PostgresStore 将路由保存到共享 PostgreSQL。
type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) LoadRoutes(ctx context.Context) ([]*Route, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model, endpoint, tenant_id, deployment_id FROM gateway_routes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var routes []*Route
	for rows.Next() {
		route := &Route{}
		if err := rows.Scan(&route.Model, &route.Endpoint, &route.TenantID, &route.DeploymentID); err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

func (s *PostgresStore) SaveRoute(ctx context.Context, route *Route) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gateway_routes (model, endpoint, tenant_id, deployment_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (model) DO UPDATE SET endpoint = EXCLUDED.endpoint, tenant_id = EXCLUDED.tenant_id,
		deployment_id = EXCLUDED.deployment_id, updated_at = now()`,
		route.Model, route.Endpoint, route.TenantID, route.DeploymentID)
	return err
}

func (s *PostgresStore) DeleteRoute(ctx context.Context, model string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM gateway_routes WHERE model = $1`, model)
	return err
}
