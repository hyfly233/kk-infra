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

	"kk-infra/lib/store"
	"kk-infra/services/pipeline/internal/data"
	"kk-infra/services/pipeline/internal/server"
	"kk-infra/services/pipeline/internal/service"
)

func main() {
	addr := flag.String("addr", ":8086", "监听地址")
	storageMode := flag.String("storage", "memory", "memory | postgres")
	registry := flag.String("model-registry", "http://127.0.0.1:8081", "modelregistry URL")
	probe := flag.String("probe-url", "http://127.0.0.1:8085", "临时探针/基准服务 URL")
	token := flag.String("pipeline-token", "", "modelregistry 发布令牌")
	authSecret := flag.String("auth-secret", os.Getenv("CARROT_AUTH_SECRET"), "JWT 签名密钥（为空时使用开发模式）")
	authAudience := flag.String("auth-audience", "controlplane", "JWT audience")
	maxTTFT := flag.Float64("benchmark-max-ttft-ms", 5000, "发布门禁允许的最大 TTFT（毫秒，0 表示禁用）")
	minThroughput := flag.Float64("benchmark-min-tokens-per-sec", 0.1, "发布门禁要求的最小输出吞吐（0 表示禁用）")
	maxErrorRate := flag.Float64("benchmark-max-error-rate", 0, "发布门禁允许的最大错误率百分比")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *maxTTFT < 0 || *minThroughput < 0 || *maxErrorRate < 0 || *maxErrorRate > 100 {
		logger.Error("benchmark 门限无效", "maxTTFTMs", *maxTTFT, "minTokensPerSec", *minThroughput, "maxErrorRate", *maxErrorRate)
		os.Exit(2)
	}
	var releaseStore data.Store = data.NewMemoryStore()
	var auditStore data.AuditStore = &data.MemoryAuditStore{}
	if *storageMode == "postgres" {
		db, err := store.Open(store.DefaultConfig())
		if err != nil {
			logger.Error("连接数据库失败", "err", err)
			os.Exit(1)
		}
		if err = store.MigrateAll(db); err != nil {
			logger.Error("migration 失败", "err", err)
			os.Exit(1)
		}
		releaseStore = data.NewPostgresStore(db)
		auditStore = data.NewPostgresAuditStore(db)
	}
	svc := service.New(releaseStore, *registry, *probe, *token, auditStore)
	svc.SetServiceSecret(*authSecret)
	svc.SetBenchmarkPolicy(service.BenchmarkPolicy{MaxTTFTMs: *maxTTFT, MinTokensPerSec: *minThroughput, MaxErrorRate: *maxErrorRate})
	handler := server.New(svc, logger)
	if *authSecret != "" {
		handler.SetAuth(*authSecret, *authAudience)
	}
	httpSrv := &http.Server{Addr: *addr, Handler: handler.Handler(), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		logger.Info("pipeline 启动", "addr", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP 服务退出", "err", err)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdown)
}
