// controlplane 控制面服务入口。
// 编排模型注册、GPU 资源、部署生命周期，提供 REST API。
// 存储支持内存（--storage=memory，默认）或 Postgres（--storage=postgres）。
package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kk-infra/lib/store"
	"kk-infra/services/controlplane/internal/biz"
	"kk-infra/services/controlplane/internal/clients"
	"kk-infra/services/controlplane/internal/clusters"
	"kk-infra/services/controlplane/internal/data"
	"kk-infra/services/controlplane/internal/identity"
	"kk-infra/services/controlplane/internal/server"
	"kk-infra/services/controlplane/internal/worker"
)

func main() {
	addr := flag.String("addr", ":8080", "监听地址")
	modelRegistry := flag.String("model-registry", "http://127.0.0.1:8081", "modelregistry 地址")
	k8sAdapter := flag.String("k8s-adapter", "http://127.0.0.1:8082", "k8sadapter 地址")
	gatewayURL := flag.String("gateway-url", "http://127.0.0.1:8083", "gateway 服务地址（模型路由同步）")
	deploymentImage := flag.String("deployment-image", "", "部署使用的模型镜像（默认 k8sadapter 决定）")
	observabilityURL := flag.String("observability-url", "http://127.0.0.1:8084", "observability 服务地址（GPU 指标转发）")
	authSecret := flag.String("auth-secret", os.Getenv("CARROT_AUTH_SECRET"), "JWT 签名密钥（从 CARROT_AUTH_SECRET 注入）")
	storage := flag.String("storage", "memory", "存储后端: memory | postgres")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("controlplane 启动", "addr", *addr, "storage", *storage,
		"observability", *observabilityURL)

	var repo data.DeploymentRepository
	var quotaStore data.QuotaStore
	var auditStore data.AuditStore
	var clusterRepo clusters.Repository
	var db *sql.DB
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
		repo = data.NewPostgresDeploymentRepository(db)
		quotaStore = data.NewPostgresQuotaStore(db)
		auditStore = data.NewPostgresAuditStore(db)
		clusterRepo = clusters.NewPostgresRepository(db)
		logger.Info("使用 Postgres 存储", "db", store.DefaultConfig().DBName)
	} else {
		repo = data.NewMemoryDeploymentRepository()
		quotaStore = data.NewMemoryQuotaStore()
		auditStore = data.NewMemoryAuditStore()
		clusterRepo = clusters.NewMemoryRepository()
		logger.Info("使用内存存储")
	}

	modelClient := clients.NewModelRegistryClient(*modelRegistry)
	kubeClient := clients.NewK8sAdapterClient(*k8sAdapter)
	var clusterService *clusters.Service
	if encryptionKey := os.Getenv("CLUSTER_ENCRYPTION_KEY"); encryptionKey != "" {
		key, err := base64.StdEncoding.DecodeString(encryptionKey)
		if err != nil || len(key) != 32 {
			logger.Error("CLUSTER_ENCRYPTION_KEY 必须是 32 字节密钥的标准 Base64 编码")
			os.Exit(1)
		}
		clusterService, err = clusters.NewService(clusterRepo, key)
		if err != nil {
			logger.Error("初始化集群注册服务失败", "err", err)
			os.Exit(1)
		}
	} else {
		logger.Info("集群注册服务未启用", "reason", "CLUSTER_ENCRYPTION_KEY 未设置")
	}

	deployUse := biz.NewDeploymentUseCase(repo, modelClient, kubeClient)
	if clusterService != nil {
		deployUse.SetClusterPlacement(clusterService, clients.NewClusterAdapterPool(clusterService, kubeClient))
	}
	if *gatewayURL != "" {
		deployUse.SetGateway(clients.NewGatewayClient(*gatewayURL))
	}
	deployUse.SetDeploymentImage(*deploymentImage)
	// R2-4：启用租户配额 + 审计
	quotaUse := biz.NewQuotaUseCase(quotaStore, logger)
	deployUse.SetQuota(quotaUse)
	auditUse := biz.NewAuditUseCase(auditStore, logger)
	deployUse.SetAudit(auditUse)
	resUse := biz.NewResourceUseCase(kubeClient)
	srv := server.NewServer(deployUse, resUse, quotaUse, auditUse, repo, logger)
	if clusterService != nil {
		srv.SetClusterService(clusterService)
	}
	srv.SetTenantProvisioner(kubeClient)
	if hubURL := os.Getenv("NOTEBOOK_HUB_URL"); hubURL != "" {
		if *authSecret == "" {
			logger.Error("Notebook 必须启用真实认证")
			os.Exit(1)
		}
		hub, err := clients.NewNotebookHubClient(hubURL, os.Getenv("NOTEBOOK_PUBLIC_URL"), os.Getenv("NOTEBOOK_HUB_API_TOKEN"))
		if err != nil {
			logger.Error("初始化 Notebook Hub 失败", "err", err)
			os.Exit(1)
		}
		srv.SetNotebookHub(hub)
	}
	if *authSecret != "" {
		identityService := identity.NewService(*authSecret)
		if db != nil {
			postgresIdentity, err := identity.NewPostgresService(db, *authSecret)
			if err != nil {
				logger.Error("恢复身份服务失败", "err", err)
				os.Exit(1)
			}
			identityService = postgresIdentity
		}
		srv.SetIdentityService(identityService)
		deployUse.SetTenantState(identityService)
	}
	if *observabilityURL != "" {
		srv.SetObservabilityClient(clients.NewObservabilityClient(*observabilityURL))
	}

	// Reconciler：每 3 秒对账一次
	reconciler := worker.NewReconciler(repo, deployUse, logger, 3*time.Second)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 启动 Reconciler
	go reconciler.Run(ctx)

	go func() {
		logger.Info("HTTP 服务监听", "addr", *addr,
			"modelRegistry", *modelRegistry, "k8sAdapter", *k8sAdapter)
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
	logger.Info("controlplane 已退出")
}
