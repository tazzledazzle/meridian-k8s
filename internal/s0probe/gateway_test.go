package s0probe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tazzledazzle/meridian/internal/gateway"
	"github.com/tazzledazzle/meridian/internal/ops"
)

// probeThroughGateway puts the real aws-gateway handler (SigV4 verification, ops.yaml routing, X-Amz-Target
// prefix rules, the two EKS routes) between the probe and the fake backend, as in the S0 stack. It returns the
// results and the gateway's log lines (it logs every refused operation).
func probeThroughGateway(t *testing.T, f *fakeAWS, list ops.List) ([]Result, []string) {
	t.Helper()
	cfg := f.cfg("")
	var mu sync.Mutex
	var logs []string
	backends := map[string]http.Handler{}
	for _, name := range list.Backends() {
		backends[name] = f // every backend name points at the fake; routing by name is the gateway's own concern
	}
	gw := gateway.New(gateway.Config{
		Ops:      list,
		Lookup:   func(k string) (string, bool) { return cfg.Secret, k == cfg.Access },
		Backends: backends,
		Logf: func(format string, a ...any) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, format)
		},
	})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	cfg.Endpoint = srv.URL
	return Run(t.Context(), cfg, Checks()), logs
}

// Live findings 4 and 5: every probed request is accepted by the real gateway (signed, in ops.yaml, with the
// X-Amz-Target prefix the gateway requires, and only the two EKS routes it knows).
func TestEveryProbeRequestIsRoutableByTheRealGateway(t *testing.T) {
	list, err := ops.Load("../../ops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newFake(t)
	rs, logs := probeThroughGateway(t, f, list)
	for _, r := range rs {
		if r.Status != "pass" {
			t.Errorf("%s: %s: %s", r.Name, r.Status, r.Detail)
		}
	}
	if len(logs) != 0 {
		t.Errorf("the gateway logged refusals: %v", logs)
	}
}

// The same test must be able to fail: remove one operation from ops.yaml and the probe says so.
func TestGatewayRefusalOfAnOperationShowsUpAsAFailureNamingIt(t *testing.T) {
	list, err := ops.Load("../../ops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var kept []ops.Op
	for _, o := range list.Ops {
		if !(o.Service == "ec2" && o.Name == "CreateFleet") {
			kept = append(kept, o)
		}
	}
	f, _ := newFake(t)
	rs, logs := probeThroughGateway(t, f, ops.List{Ops: kept})
	var fleet Result
	for _, r := range rs {
		if r.Name == "ec2:CreateFleet" {
			fleet = r
		}
	}
	if fleet.Status != "fail" || !strings.Contains(fleet.Detail, "ec2:CreateFleet is not in ops.yaml") {
		t.Fatalf("CreateFleet = %+v", fleet)
	}
	if len(logs) == 0 {
		t.Error("expected the gateway to log the refusal")
	}
}
