// modelregistry 模型注册中心服务入口。
// 存储支持内存（--storage=memory，默认）或 Postgres（--storage=postgres）。
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
	"kk-infra/services/modelregistry/internal/artifact"
	"kk-infra/services/modelregistry/internal/biz"
	"kk-infra/services/modelregistry/internal/data"
	"kk-infra/services/modelregistry/internal/server"
)

func main() {
	addr := flag.String("addr", ":8081", "监听地址")
	storage := flag.String("storage", "memory", "存储后端: memory | postgres")
	s3Endpoint := flag.String("s3-endpoint", "", "S3/MinIO endpoint；为空时使用开发校验器")
	s3Secure := flag.Bool("s3-secure", false, "S3 endpoint 使用 TLS")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("modelregistry 启动", "addr", *addr, "storage", *storage)

	var repo data.Repository
	if *storage == "postgres" {
		db, err := store.Open(store.DefaultConfig())
		if err != nil {
			logger.Error("连接 Postgres 失败", "err", err)
			os.Exit(1)
		}
		if err := store.MigrateAll(db); err != nil {
			logger.Error("执行 migration 失败", "err", err)
			os.Exit(1)
		}
		repo = data.NewPostgresRepository(db)
		logger.Info("使用 Postgres 存储", "db", store.DefaultConfig().DBName)
	} else {
		repo = data.NewMemoryRepository()
		logger.Info("使用内存存储")
	}

	var verifier artifact.Verifier = artifact.DevelopmentVerifier{}
	if *s3Endpoint != "" {
		accessKey, secretKey := os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY")
		var err error
		verifier, err = artifact.NewS3Verifier(*s3Endpoint, accessKey, secretKey, *s3Secure)
		if err != nil {
			logger.Error("初始化 S3 artifact 校验器失败", "err", err)
			os.Exit(1)
		}
	} else {
		logger.Warn("未配置 S3 endpoint，artifact 校验使用开发模式")
	}
	registry := biz.NewRegistry(repo, verifier)
	srv := server.NewServer(registry, logger)

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
	logger.Info("modelregistry 已退出")
}
