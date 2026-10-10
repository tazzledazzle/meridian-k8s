package noderegistrar

import (
	"context"
	"errors"
	"sort"
	"testing"
)

type fakeFleet struct {
	instances []Instance
	err       error
}

func (f *fakeFleet) Instances(context.Context) ([]Instance, error) { return f.instances, f.err }

type fakeCluster struct {
	nodes map[string]Node // by name
	err   error
}

func newFakeCluster() *fakeCluster { return &fakeCluster{nodes: map[string]Node{}} }

func (c *fakeCluster) ManagedNodes(context.Context) ([]Node, error) {
	var out []Node
	for _, n := range c.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, c.err
}
func (c *fakeCluster) Create(_ context.Context, n Node) error { c.nodes[n.Name] = n; return nil }
func (c *fakeCluster) Delete(_ context.Context, name string) error {
	delete(c.nodes, name)
	return nil
}

func inst(id, state string, tags map[string]string) Instance {
	return Instance{ID: id, State: state, Type: "m5.large", AZ: "us-west-2a",
		PrivateDNS: "ip-" + id + ".internal", Tags: tags}
}

func newRec(f *fakeFleet, c *fakeCluster) *Reconciler {
	return &Reconciler{Fleet: f, Cluster: c, Backends: map[string]Backend{"kwok": KWOK{}}, Default: "kwok"}
}

func TestRunningInstanceGetsExactlyOneNode(t *testing.T) {
	f := &fakeFleet{instances: []Instance{inst("i-1", "running", nil), inst("i-2", "pending", nil)}}
	c := newFakeCluster()
	r := newRec(f, c)
	res, err := r.Reconcile(context.Background())
	if err != nil || len(res.Created) != 1 || len(c.nodes) != 1 {
		t.Fatalf("res=%+v err=%v nodes=%d (a pending instance must not get a node)", res, err, len(c.nodes))
	}
	res, _ = r.Reconcile(context.Background())
	if len(res.Created) != 0 || len(c.nodes) != 1 {
		t.Fatalf("second pass must be a no-op: %+v", res)
	}
	f.instances[1].State = "running"
	res, _ = r.Reconcile(context.Background())
	if len(res.Created) != 1 || len(c.nodes) != 2 {
		t.Fatalf("pending -> running should add a node: %+v", res)
	}
}

func TestNodeIsDeletedWhenTheInstanceTerminatesOrDisappears(t *testing.T) {
	f := &fakeFleet{instances: []Instance{inst("i-1", "running", nil), inst("i-2", "running", nil), inst("i-3", "running", nil)}}
	c := newFakeCluster()
	r := newRec(f, c)
	if _, err := r.Reconcile(context.Background()); err != nil || len(c.nodes) != 3 {
		t.Fatalf("setup: %v %d", err, len(c.nodes))
	}
	f.instances[0].State = "shutting-down"
	f.instances[1].State = "terminated"
	f.instances = f.instances[:2] // i-3 vanished from the listing (for example ec2-sim restarted)
	res, err := r.Reconcile(context.Background())
	if err != nil || len(res.Deleted) != 3 || len(c.nodes) != 0 {
		t.Fatalf("res=%+v err=%v nodes=%d", res, err, len(c.nodes))
	}
}

func TestAFailedFleetListingNeverDeletesNodes(t *testing.T) {
	f := &fakeFleet{instances: []Instance{inst("i-1", "running", nil)}}
	c := newFakeCluster()
	r := newRec(f, c)
	_, _ = r.Reconcile(context.Background())
	f.instances, f.err = nil, errors.New("ec2 unreachable")
	if _, err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if len(c.nodes) != 1 {
		t.Fatal("an EC2 outage must not delete Nodes")
	}
}

func TestBackendTagSelectsTheBackendAndNeverFallsBack(t *testing.T) {
	f := &fakeFleet{instances: []Instance{
		inst("i-kwok", "running", map[string]string{BackendKey: "kwok"}),
		inst("i-real", "running", map[string]string{BackendKey: "real"}),
		inst("i-none", "running", nil),
	}}
	c := newFakeCluster()
	res, err := newRec(f, c).Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 2 {
		t.Fatalf("created = %v (the untagged instance uses the default backend)", res.Created)
	}
	if _, ok := res.Skipped["i-real"]; !ok || len(c.nodes) != 2 {
		t.Fatalf("a request for the unavailable 'real' backend must be reported, not turned into a KWOK node: %+v", res.Skipped)
	}
}

func TestUnmanagedNodesAreNeverTouched(t *testing.T) {
	// The cluster interface only ever lists managed nodes, so the control-plane node is invisible here.
	c := newFakeCluster()
	f := &fakeFleet{}
	res, err := newRec(f, c).Reconcile(context.Background())
	if err != nil || len(res.Deleted) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// Extra tests beyond the plan: failure paths of the cluster and of the backend.
func TestAFailedNodeListingIsAnErrorAndChangesNothing(t *testing.T) {
	f := &fakeFleet{instances: []Instance{inst("i-1", "running", nil)}}
	c := newFakeCluster()
	c.err = errors.New("apiserver down")
	if _, err := newRec(f, c).Reconcile(context.Background()); err == nil || len(c.nodes) != 0 {
		t.Fatalf("err=%v nodes=%d", err, len(c.nodes))
	}
}

func TestAnInstanceTheBackendCannotBuildIsSkippedNotFatal(t *testing.T) {
	bad := inst("i-bad", "running", nil)
	bad.Type = "x9.huge"
	f := &fakeFleet{instances: []Instance{bad, inst("i-ok", "running", nil)}}
	c := newFakeCluster()
	var logged int
	r := newRec(f, c)
	r.Logf = func(string, ...any) { logged++ }
	res, err := r.Reconcile(context.Background())
	if err != nil || len(res.Created) != 1 || res.Skipped["i-bad"] == "" || logged == 0 {
		t.Fatalf("res=%+v err=%v logged=%d", res, err, logged)
	}
}
