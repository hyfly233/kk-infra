// gateway AI 网关服务入口。
// 提供 OpenAI 兼容 API：/v1/models、/v1/chat/completions（含流式）。
// API Key 存储支持内存（默认）或 Postgres。
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/store"
	"kk-infra/services/gateway/internal/auth"
	"kk-infra/services/gateway/internal/proxy"
	"kk-infra/services/gateway/internal/router"
	"kk-infra/services/gateway/internal/server"
	"kk-infra/services/gateway/internal/sink"
)

func main() {
	addr := flag.String("addr", ":8083", "监听地址")
	ratePerMinute := flag.Int("rate-per-minute", 0, "每租户每分钟限流（0=不限）")
	storage := flag.String("storage", "memory", "存储后端: memory | postgres")
	authSecret := flag.String("auth-secret", os.Getenv("CARROT_AUTH_SECRET"), "管理接口 JWT 签名密钥，空值仅供不安全开发模式")
	controlplaneURL := flag.String("controlplane-url", "http://127.0.0.1:8080", "在线租户状态查询的 controlplane 地址")
	observabilityURL := flag.String("observability-url", "", "observability 服务地址（指标上报，空则跳过）")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("gateway 启动", "addr", *addr, "storage", *storage)

	var (
		keys *auth.Manager
		db   *sql.DB
	)
	if *storage == "postgres" {
		var err error
		db, err = store.Open(store.DefaultConfig())
		if err != nil {
			logger.Error("连接 Postgres 失败", "err", err)
			os.Exit(1)
		}
		if err := store.MigrateAll(db); err != nil {
			logger.Error("执行 migration 失败", "err", err)
			os.Exit(1)
		}
		keys = auth.NewManagerWithStore(auth.NewPostgresStore(db))
		logger.Info("使用 Postgres API Key 存储")
	} else {
		keys = auth.NewManager()
		logger.Info("使用内存 API Key 存储")
	}

	routes := router.NewTable()
	if *storage == "postgres" {
		var err error
		routes, err = router.NewTableWithStore(context.Background(), router.NewPostgresStore(db))
		if err != nil {
			logger.Error("恢复网关路由失败", "err", err)
			os.Exit(1)
		}
		logger.Info("已恢复网关路由", "count", len(routes.List()))
	}
	var metricsSink proxy.MetricsSink
	if *observabilityURL != "" {
		httpSink := sink.NewHTTPSink(*observabilityURL, logger)
		httpSink.SetServiceSecret(*authSecret)
		metricsSink = httpSink
		logger.Info("指标上报到 observability", "url", *observabilityURL)
	}
	p := proxy.NewProxy(routes, logger, metricsSink)
	// R2-4：模型授权（API Key 白名单）
	p.SetAuthorize(func(ctx context.Context, apiKey, model string) error {
		if !keys.CanAccess(apiKey, model) {
			return fmt.Errorf("API Key 无权访问模型 %s", model)
		}
		return nil
	})
	if *ratePerMinute > 0 {
		p.RatePerMinute = *ratePerMinute
	}
	srv := server.NewServer(keys, routes, p, logger)
	srv.SetAuthSecret(*authSecret)
	srv.SetUserVerifier(platformauth.NewUserVerifier(*controlplaneURL, *authSecret, "gateway"))
	if *authSecret != "" {
		srv.ConfigureTenantCheck(*controlplaneURL, *authSecret)
	}
	if *authSecret == "" {
		logger.Warn("gateway 管理接口鉴权未启用，仅限可信本地开发环境")
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("HTTP 服务监听", "addr", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP 服务异常退出", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("收到退出信号，开始优雅关闭")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	logger.Info("gateway 已退出")
}
