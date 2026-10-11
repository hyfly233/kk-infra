// Package store 提供 Postgres 连接池与轻量 migration 执行器。
// 使用 database/sql + lib/pq（零外部依赖，本机已缓存）。
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"time"

	_ "github.com/lib/pq" // Postgres 驱动
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrateAll 执行全部内置 migration（幂等可重入）
func MigrateAll(db *sql.DB) error {
	m := NewMigrator(db)
	return m.Migrate(migrationsFS, []string{
		"migrations/001_init.sql",
		"migrations/002_api_key_models.sql",
		"migrations/003_gateway_routes.sql",
		"migrations/004_identity.sql",
		"migrations/005_tenants.sql",
		"migrations/007_usage_billing.sql",
		"migrations/008_gateway_route_model.sql",
		"migrations/009_artifact_metadata.sql",
		"migrations/010_release_records.sql",
		"migrations/011_deployment_revisions.sql",
	})
}

// Config 数据库连接配置
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string // disable / require
}

// DSN 生成连接串
func (c Config) DSN() string {
	sslmode := c.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.DBName, sslmode,
	)
}

// DefaultConfig 本机 docker Postgres 默认配置（/Users/flyhy/workspace/docker/postgresql）
func DefaultConfig() Config {
	return Config{
		Host:     "127.0.0.1",
		Port:     5432,
		User:     "postgres",
		Password: "password",
		DBName:   "carrot",
		SSLMode:  "disable",
	}
}

// Open 打开连接池并验证连通性
func Open(cfg Config) (*sql.DB, error) {
	db, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	// 验证连通（带重试，容器可能刚启动）
	var pingErr error
	for i := 0; i < 5; i++ {
		if pingErr = db.Ping(); pingErr == nil {
			return db, nil
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("连接数据库失败: %w", pingErr)
}
