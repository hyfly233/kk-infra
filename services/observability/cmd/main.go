// observability 可观测性服务入口。
// 提供请求指标与 GPU 利用率的内存聚合与查询 API。
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kk-infra/lib/apitypes"
	platformstore "kk-infra/lib/store"
	"kk-infra/services/observability/internal/collector"
	"kk-infra/services/observability/internal/metrics"
	"kk-infra/services/observability/internal/prometheus"
	"kk-infra/services/observability/internal/server"
	"kk-infra/services/observability/internal/usage"
)

func main() {
	addr := flag.String("addr", ":8084", "监听地址")
	retention := flag.Duration("retention", 2*time.Hour, "指标保留窗口")
	controlplaneURL := flag.String("controlplane-url", "", "controlplane 地址（如 http://localhost:8080），配置后启用 GPU 指标采集")
	prometheusURL := flag.String("prometheus-url", "", "Prometheus 地址（如 http://localhost:9090），配置后 GPU 查询走 DCGM 指标")
	storage := flag.String("storage", "memory", "存储后端: memory | postgres")
	authSecret := flag.String("auth-secret", os.Getenv("CARROT_AUTH_SECRET"), "内部管理接口服务 JWT 密钥，空值仅供不安全开发模式")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	store := metrics.NewStore(*retention)
	srv := server.NewServer(store, logger)
	srv.SetAuthSecret(*authSecret)
	if *authSecret == "" {
		logger.Warn("observability 服务鉴权未启用，仅限可信本地开发环境")
	}
	var usageStore usage.Store = usage.NewMemoryStore()
	if *storage == "postgres" {
		db, err := platformstore.Open(platformstore.DefaultConfig())
		if err != nil {
			logger.Error("连接 Postgres 失败", "err", err)
			os.Exit(1)
		}
		if err := platformstore.MigrateAll(db); err != nil {
			logger.Error("执行 migration 失败", "err", err)
			os.Exit(1)
		}
		usageStore = usage.NewPostgresStore(db)
		srv.SetUsageStore(usageStore)
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				if err := usageStore.AccrueGPU(context.Background(), time.Now(), time.Minute); err != nil {
					logger.Warn("累计 GPU 副本时长失败", "err", err)
				}
			}
		}()
	}
	if *storage != "postgres" {
		srv.SetUsageStore(usageStore)
	}

	// R2-3：Prometheus adapter（DCGM 指标查询）
	if *prometheusURL != "" {
		srv.SetPrometheus(prometheus.NewClient(*prometheusURL))
		logger.Info("Prometheus 查询已启用", "url", *prometheusURL)
	}

	// GPU 指标采集：定期从 controlplane 拉取 GPU 资源状态
	if *controlplaneURL != "" {
		sourcePath := "/api/v1/resources/gpus"
		if *authSecret != "" {
			sourcePath = "/internal/resources/gpus"
		}
		col := collector.NewGPUCollector(*controlplaneURL+sourcePath, logger, 15*time.Second)
		col.SetServiceSecret(*authSecret)
		col.OnGPU = func(view apitypes.GPUResourcesView) {
			for _, n := range view.Nodes {
				store.RecordGPU(metrics.GPUSample{
					Ts:          time.Now(),
					NodeName:    n.NodeName,
					GPUType:     n.GPUType,
					Utilization: n.Utilization,
					Used:        n.Used,
					Total:       n.Total,
				})
			}
		}
		collectorCtx, collectorStop := context.WithCancel(context.Background())
		go col.Run(collectorCtx)
		defer collectorStop()
		logger.Info("GPU 指标采集已启用", "controlplane", *controlplaneURL)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("HTTP 服务监听", "addr", *addr, "retention", retention.String())
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
	logger.Info("observability 已退出")
}
