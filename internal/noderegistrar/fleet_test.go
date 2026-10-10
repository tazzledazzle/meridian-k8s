package noderegistrar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/fakeec2"
)

// TestEC2FleetReadsInstancesFromDescribeInstances runs the real EC2 client
// against a fake that answers in the shape AWS documents, so a wrong element
// name fails here. The S0 probe checks the same shape against the real backend.
func TestEC2FleetReadsInstancesFromDescribeInstances(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store := fakeec2.New(func() time.Time { return now }, 3*time.Second)
	tags := map[string]string{"kubernetes.io/cluster/meridian-g0": "owned", BackendKey: "kwok"}
	own := store.Launch("m5.large", "us-west-2b", "spot", tags)
	store.Launch("c5.large", "us-west-2a", "", nil) // not ours
	now = now.Add(5 * time.Second)

	srv := httptest.NewServer(store)
	defer srv.Close()
	f := &EC2Fleet{Endpoint: srv.URL, Region: "us-west-2", Access: "AK", Secret: "SK", ClusterTag: "kubernetes.io/cluster/meridian-g0"}
	got, err := f.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d instances, want only the one tagged for this cluster: %+v", len(got), got)
	}
	g := got[0]
	if g.ID != own || g.State != "running" || g.Type != "m5.large" || g.AZ != "us-west-2b" ||
		g.PrivateDNS == "" || g.Tags[BackendKey] != "kwok" {
		t.Fatalf("instance = %+v", g)
	}
	// And the node it produces is well formed.
	if _, err := (KWOK{}).Node(g); err != nil {
		t.Fatal(err)
	}
}

func TestEC2FleetReportsHTTPFailures(t *testing.T) {
	srv := httptest.NewServer(fakeec2.New(time.Now, 0))
	srv.Close() // nothing is listening any more
	f := &EC2Fleet{Endpoint: srv.URL, Region: "us-west-2", Access: "AK", Secret: "SK", ClusterTag: "x"}
	if _, err := f.Instances(context.Background()); err == nil {
		t.Fatal("expected a connection error")
	}
}

// Extra: a non-200 answer (for example a signature error) must surface as an error.
func TestEC2FleetReportsNon200Answers(t *testing.T) {
	bad := httptest.NewServer(httpErr{})
	defer bad.Close()
	f := &EC2Fleet{Endpoint: bad.URL, Region: "us-west-2", Access: "AK", Secret: "SK", ClusterTag: "x"}
	if _, err := f.Instances(context.Background()); err == nil {
		t.Fatal("expected an HTTP error")
	}
}

type httpErr struct{}

func (httpErr) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "<Response><Errors><Error><Code>AuthFailure</Code></Error></Errors></Response>", http.StatusUnauthorized)
}
