package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVolcanoCapacityIsOptionalAndPreservesQuantities(t *testing.T) {
	calls := 0
	status := http.StatusOK
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/apis/scheduling.volcano.sh/v1beta1/queues" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, `{"items":[{"metadata":{"name":"tenant-a"},"spec":{"capability":{"nvidia.com/gpu":"8","cpu":"16000m"},"deserved":{"nvidia.com/gpu":"4"}},"status":{"state":"Open","allocated":{"nvidia.com/gpu":"2"},"pending":3,"running":1,"inqueue":2}}]}`)
	}))
	defer api.Close()
	c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client()}
	if queues, err := c.ListVolcanoQueues(context.Background()); err != nil || len(queues) != 0 || calls != 0 {
		t.Fatalf("disabled Volcano queried API: %+v %v", queues, err)
	}
	c.ConfigureVolcano(true, "tenant-")
	queues, err := c.ListVolcanoQueues(context.Background())
	if err != nil || len(queues) != 1 {
		t.Fatalf("queue report: %+v %v", queues, err)
	}
	q := queues[0]
	if q.State != "Open" || q.Pending != 3 || q.Running != 1 || q.Inqueue != 2 || q.Capability["cpu"] != "16000m" || q.Deserved["nvidia.com/gpu"] != "4" || q.Allocated["nvidia.com/gpu"] != "2" {
		t.Fatalf("queue quantities lost: %+v", q)
	}
	status = http.StatusForbidden
	if _, err := c.ListVolcanoQueues(context.Background()); err == nil {
		t.Fatal("forbidden API treated as empty capacity")
	}
}
