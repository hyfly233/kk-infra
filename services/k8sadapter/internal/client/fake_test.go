package client

import (
	"context"
	"testing"
	"time"

	"kk-infra/lib/domain"
)

// Fake 集群基础信息
func TestFakeListGPUNodes(t *testing.T) {
	f := NewFakeKubeClient(DefaultFakeNodes())
	nodes, err := f.ListGPUNodes(context.Background())
	if err != nil {
		t.Fatalf("查询 GPU 失败: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("节点数 = %d, want 2", len(nodes))
	}
	total := nodes[0].Total + nodes[1].Total
	if total != 16 {
		t.Errorf("GPU 总数 = %d, want 16", total)
	}
	for _, n := range nodes {
		if n.GPUType != "A100" {
			t.Errorf("GPU 型号错误: %s", n.GPUType)
		}
		if n.Available() != n.Total {
			t.Errorf("初始可用应等于总量: %+v", n)
		}
	}
}

func TestFakeGPUNodeOccupancyDoesNotDuplicateAcrossNodes(t *testing.T) {
	f := NewFakeKubeClient([]FakeNodeConfig{
		{NodeName: "a1", GPUType: "A100", GPUCount: 8},
		{NodeName: "a2", GPUType: "A100", GPUCount: 8},
		{NodeName: "h1", GPUType: "H100", GPUCount: 4},
	})
	f.deploys["a"] = &FakeDeployment{Stage: 1, Spec: DeploymentSpec{Replicas: 1, Resource: domain.Resource{GPUType: "A100", GPUCount: 12}}}
	f.deploys["h"] = &FakeDeployment{Stage: 1, Spec: DeploymentSpec{Replicas: 1, Resource: domain.Resource{GPUType: "H100", GPUCount: 2}}}
	nodes, err := f.ListGPUNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if nodes[0].Used != 8 || nodes[1].Used != 4 || nodes[2].Used != 2 {
		t.Fatalf("duplicated or mixed GPU occupancy: %+v", nodes)
	}
	f.deploys["a"].Spec.Resource.GPUCount = 17
	nodes, err = f.ListGPUNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if nodes[1].Used != 9 {
		t.Fatalf("overcommit hidden from heartbeat: %+v", nodes)
	}
}

// 创建部署 → 生命周期推进 → Running
func TestFakeDeploymentLifecycle(t *testing.T) {
	f := NewFakeKubeClient(DefaultFakeNodes())
	f.startLatency = 50 * time.Millisecond

	spec := &DeploymentSpec{
		DeploymentID: "d1",
		Name:         "qwen-demo",
		Namespace:    "tenant-default",
		Replicas:     1,
		Resource:     fakeResource(),
	}
	res, err := f.CreateDeployment(context.Background(), spec)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if res.DeploymentID != "d1" {
		t.Errorf("DeploymentID 错误: %s", res.DeploymentID)
	}
	if res.Endpoint != "qwen-demo.tenant-default.svc.cluster.local" {
		t.Errorf("Endpoint 错误: %s", res.Endpoint)
	}

	// 幂等创建
	res2, err := f.CreateDeployment(context.Background(), spec)
	if err != nil || res2.DeploymentID != "d1" {
		t.Fatalf("幂等创建失败: %v %+v", err, res2)
	}

	// 等待 Running
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, _ = f.GetDeployment(context.Background(), "qwen-demo", "tenant-default")
		if res.Status != nil && res.Status.Condition == "Available" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("部署未进入 Running: %+v", res.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if res.Status.ReadyReplicas != 1 {
		t.Errorf("Ready 副本 = %d, want 1", res.Status.ReadyReplicas)
	}

	// 更新必须替换既有规格，而不是命中幂等创建后直接返回旧对象。
	updated := *spec
	updated.Replicas = 2
	updated.Resource.GPUCount = 2
	updated.Args = []string{"--max-model-len", "4096"}
	res, err = f.UpdateDeployment(context.Background(), &updated)
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		res, _ = f.GetDeployment(context.Background(), "qwen-demo", "tenant-default")
		if res.Status != nil && res.Status.Condition == "Available" && res.Status.ReadyReplicas == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("更新后未重新就绪: %+v", res.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, _, usedAfterUpdate, _ := f.NodeGPUCapacity(context.Background(), "A100")
	if usedAfterUpdate != 4 {
		t.Errorf("更新后 GPU used = %d, want 4", usedAfterUpdate)
	}

	// 重启必须使副本重新经历就绪过程。
	res, err = f.RestartDeployment(context.Background(), "qwen-demo", "tenant-default")
	if err != nil {
		t.Fatalf("重启失败: %v", err)
	}
	if res.Status.ReadyReplicas != 0 {
		t.Fatalf("重启应先清空 ready 副本: %+v", res.Status)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		res, _ = f.GetDeployment(context.Background(), "qwen-demo", "tenant-default")
		if res.Status != nil && res.Status.Condition == "Available" && res.Status.ReadyReplicas == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("重启后未重新就绪: %+v", res.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// GPU 占用
	_, _, used, _ := f.NodeGPUCapacity(context.Background(), "A100")
	if used != 4 {
		t.Errorf("更新后 GPU used = %d, want 4", used)
	}

	// 扩容
	res, err = f.ScaleDeployment(context.Background(), "qwen-demo", "tenant-default", 3)
	if err != nil {
		t.Fatalf("扩容失败: %v", err)
	}
	if res.Status.Replicas != 3 || res.Status.ReadyReplicas != 3 {
		t.Errorf("扩容状态错误: %+v", res.Status)
	}

	// 删除 + 幂等删除
	if err := f.DeleteDeployment(context.Background(), "qwen-demo", "tenant-default"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if err := f.DeleteDeployment(context.Background(), "qwen-demo", "tenant-default"); err != nil {
		t.Fatalf("幂等删除应成功: %v", err)
	}
	_, err = f.GetDeployment(context.Background(), "qwen-demo", "tenant-default")
	if err != ErrNotFound {
		t.Fatalf("删除后查询应返回 ErrNotFound: %v", err)
	}
	// 资源释放
	_, _, used, _ = f.NodeGPUCapacity(context.Background(), "A100")
	if used != 0 {
		t.Errorf("删除后 GPU used = %d, want 0", used)
	}
}

// 注入失败：第二阶段失败 → ReplicaFailure
func TestFakeDeploymentFailure(t *testing.T) {
	f := NewFakeKubeClient(DefaultFakeNodes())
	f.startLatency = 30 * time.Millisecond

	spec := &DeploymentSpec{
		DeploymentID: "d2",
		Name:         "bad-model",
		Namespace:    "tenant-default",
		Replicas:     1,
		Resource:     fakeResource(),
	}
	f.mu.Lock()
	f.deploys["tenant-default/bad-model"] = &FakeDeployment{Spec: *spec, FailAfter: 1}
	f.mu.Unlock()
	go f.advance("tenant-default/bad-model")

	deadline := time.Now().Add(3 * time.Second)
	for {
		res, _ := f.GetDeployment(context.Background(), "bad-model", "tenant-default")
		if res.Status != nil && res.Status.Condition == "ReplicaFailure" {
			if res.Message == "" {
				t.Error("失败部署应有诊断信息")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("部署未进入 Failed")
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func fakeResource() domain.Resource {
	return domain.Resource{GPUType: "A100", GPUCount: 1, MemoryMB: 32768}
}
