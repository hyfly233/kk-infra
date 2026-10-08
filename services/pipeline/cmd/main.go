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
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	var releaseStore data.Store = data.NewMemoryStore()
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
	}
	svc := service.New(releaseStore, *registry, *probe, *token)
	httpSrv := &http.Server{Addr: *addr, Handler: server.New(svc, logger).Handler(), ReadHeaderTimeout: 5 * time.Second}
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
