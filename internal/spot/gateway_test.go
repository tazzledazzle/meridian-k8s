package spot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/gateway"
	"github.com/tazzledazzle/meridian/internal/ops"
)

// TestInterruptThroughTheRealGatewayAndOpsYaml sends the whole sequence through the
// real gateway configured from the repository's ops.yaml, so a call the gateway
// would reject as unsupported (or a signature it would refuse) fails here.
func TestInterruptThroughTheRealGatewayAndOpsYaml(t *testing.T) {
	list, err := ops.Load("../../ops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t)
	backends := map[string]http.Handler{}
	for _, op := range list.Ops {
		backends[op.Backend] = r.aws // every backend name resolves to the fake EC2+SQS
	}
	var mu sync.Mutex
	var seen []string
	gw := httptest.NewServer(gateway.New(gateway.Config{
		Ops:      list,
		Lookup:   func(k string) (string, bool) { return "SK", k == "AK" },
		Now:      r.clk.Now,
		Backends: backends,
		OnCall: func(svc, op string, supported bool) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, svc+":"+op)
			if !supported {
				t.Errorf("gateway does not support %s:%s per ops.yaml", svc, op)
			}
		},
		Logf: func(string, ...any) {},
	}))
	defer gw.Close()
	r.in.Endpoint = gw.URL

	id := r.launch("spot")
	if err := r.in.Interrupt(context.Background(), id, time.Minute); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "ec2:DescribeInstances,sqs:GetQueueUrl,sqs:SendMessage,ec2:TerminateInstances"
	if got := strings.Join(seen, ","); got != want {
		t.Fatalf("gateway saw %s, want %s", got, want)
	}
	if s, _ := r.store.State(id); s != "shutting-down" {
		t.Fatalf("state = %s", s)
	}
}
