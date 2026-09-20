// k8sadapter Kubernetes 适配器服务入口。
// --fake 使用模拟集群（本地无集群时）；--kubeconfig 使用真实集群。
// 无 GPU 集群可用 --virtual-gpus 注入虚拟 GPU 池（Docker Desktop 验证用）。
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

	"kk-infra/services/k8sadapter/internal/client"
	"kk-infra/services/k8sadapter/internal/server"
)

func main() {
	addr := flag.String("addr", ":8082", "监听地址")
	fake := flag.Bool("fake", true, "使用 Fake Kubernetes 集群（默认 true）")
	kubeconfig := flag.String("kubeconfig", "", "真实集群 kubeconfig 路径（默认 ~/.kube/config）")
	virtualGPUs := flag.String("virtual-gpus", "", "虚拟 GPU 池配置（无 GPU 集群用），格式: node:gpuType:count:memMB:util:health;...")
	deployImage := flag.String("deploy-image", "", "部署使用的模型镜像（默认 vllm/vllm-openai:latest）")
	kedaEnabled := flag.Bool("keda-enabled", false, "为每个真实部署创建 KEDA ScaledObject（要求集群已安装 KEDA）")
	prometheusURL := flag.String("prometheus-url", "http://prometheus.monitoring.svc.cluster.local:9090", "KEDA 查询的 Prometheus 地址")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	var kube client.KubeClient
	if *fake {
		kube = client.NewFakeKubeClient(client.DefaultFakeNodes())
		logger.Info("k8sadapter 使用 Fake 集群: 2 节点 × 8 卡 A100")
	} else {
		real, err := client.NewRealKubeClient(*kubeconfig, *virtualGPUs, *deployImage)
		if err != nil {
			logger.Error("创建真实 K8s 客户端失败", "err", err)
			os.Exit(1)
		}
		real.ConfigureKEDA(*kedaEnabled, *prometheusURL)
		kube = real
		if *virtualGPUs != "" {
			logger.Info("k8sadapter 使用真实集群 + 虚拟 GPU 池", "virtualGPUs", *virtualGPUs)
		} else {
			logger.Info("k8sadapter 使用真实集群")
		}
	}

	srv := server.NewServer(kube, logger)
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
	logger.Info("k8sadapter 已退出")
}
