package client

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/cache"
)

func TestGPUInformerSynchronizesAndTracksPodChanges(t *testing.T) {
	nodes := schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{nodes: "NodeList", pods: "PodList"})
	if _, err := dc.Resource(nodes).Create(context.Background(), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": "gpu-node", "labels": map[string]any{"carrot.ai/gpu-type": "A100"}},
		"status": map[string]any{"allocatable": map[string]any{"nvidia.com/gpu": "8"}},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	g := newGPUWatch(dc)
	var list nodeList
	if err := g.decode(true, &list); err == nil {
		t.Fatal("unsynchronized cache accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go g.nodes.Run(ctx.Done())
	go g.pods.Run(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), g.nodes.HasSynced, g.pods.HasSynced) {
		t.Fatal("cache sync failed")
	}
	if err := g.decode(true, &list); err != nil || len(list.Items) != 1 || list.Items[0].Metadata.Name != "gpu-node" {
		t.Fatalf("initial nodes not cached: %+v %v", list, err)
	}
	node, err := dc.Resource(nodes).Get(ctx, "gpu-node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(node.Object, "4", "status", "allocatable", "nvidia.com/gpu"); err != nil {
		t.Fatal(err)
	}
	if _, err := dc.Resource(nodes).Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		if err := g.decode(true, &list); err == nil && list.Items[0].Status.Allocatable["nvidia.com/gpu"] == "4" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("node update was not observed")
	}
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "gpu-pod", "namespace": "tenant-a", "labels": map[string]any{"carrot.ai/managed-by": "carrot", "carrot.ai/deployment-id": "deploy-a", "carrot.ai/tenant-id": "tenant-a"}},
		"spec":   map[string]any{"nodeName": "gpu-node", "containers": []any{map[string]any{"name": "model", "resources": map[string]any{"requests": map[string]any{"nvidia.com/gpu": "2"}}}}},
		"status": map[string]any{"phase": "Running"},
	}}
	if _, err := dc.Resource(pods).Namespace("tenant-a").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c := &RealKubeClient{gpuWatch: g}
	waitUsage := func(want int32) {
		t.Helper()
		for ctx.Err() == nil {
			used, err := c.gpuRequestsByNode(ctx)
			if err == nil && used["gpu-node"] == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("GPU usage did not become %d", want)
	}
	waitUsage(2)
	_, assigned, err := c.gpuRequestSnapshot(ctx)
	if err != nil || len(assigned) != 1 || assigned[0].GPUCount != 2 || assigned[0].DeploymentID != "deploy-a" {
		t.Fatalf("cached attribution: %+v %v", assigned, err)
	}
	if err := dc.Resource(pods).Namespace("tenant-a").Delete(ctx, "gpu-pod", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUsage(0)
	_, assigned, err = c.gpuRequestSnapshot(ctx)
	if err != nil || len(assigned) != 0 {
		t.Fatalf("deleted Pod attribution retained: %+v %v", assigned, err)
	}
	g.podFailed.Store(true)
	if _, err := c.gpuRequestsByNode(ctx); err == nil {
		t.Fatal("failed watch advertised capacity")
	}
}
