package client

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

type gpuWatch struct {
	nodes, pods           cache.SharedIndexInformer
	nodeFailed, podFailed atomic.Bool
}

// StartGPUWatch must run before serving requests. Reads reject unsynchronized or
// failed watches rather than silently advertising stale available capacity.
func (c *RealKubeClient) StartGPUWatch(ctx context.Context) error {
	dc, err := dynamic.NewForConfig(&rest.Config{Host: c.baseURL, BearerToken: c.token, Transport: c.httpClient.Transport})
	if err != nil {
		return err
	}
	c.gpuWatch = newGPUWatch(dc)
	go c.gpuWatch.nodes.Run(ctx.Done())
	go c.gpuWatch.pods.Run(ctx.Done())
	return nil
}

func newGPUWatch(dc dynamic.Interface) *gpuWatch {
	g := &gpuWatch{}
	g.nodes = gpuInformer(dc, "nodes", &g.nodeFailed)
	g.pods = gpuInformer(dc, "pods", &g.podFailed)
	return g
}

func gpuInformer(dc dynamic.Interface, name string, failed *atomic.Bool) cache.SharedIndexInformer {
	resource := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: name})
	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
			result, err := resource.List(ctx, opts)
			failed.Store(err != nil)
			return result, err
		},
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			result, err := resource.Watch(ctx, opts)
			failed.Store(err != nil)
			return result, err
		},
	}
	i := cache.NewSharedIndexInformer(cache.ToListWatcherWithWatchListSemantics(lw, dc), &unstructured.Unstructured{}, 0, cache.Indexers{})
	_ = i.SetWatchErrorHandler(func(_ *cache.Reflector, _ error) { failed.Store(true) })
	return i
}

func (g *gpuWatch) decode(nodes bool, out any) error {
	if !g.nodes.HasSynced() || !g.pods.HasSynced() || g.nodes.IsStopped() || g.pods.IsStopped() || g.nodeFailed.Load() || g.podFailed.Load() {
		return fmt.Errorf("GPU informer cache is not synchronized or its watch is unhealthy")
	}
	i := g.pods
	if nodes {
		i = g.nodes
	}
	items := i.GetStore().List()
	data, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
