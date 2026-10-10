package noderegistrar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI records what the registrar sends, like a tiny kube-apiserver.
type fakeAPI struct {
	auth    string
	created []kNode
	deleted []string
	status  map[string]int // method -> forced status
	query   string
}

func (a *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.auth = r.Header.Get("Authorization")
	if code, ok := a.status[r.Method]; ok {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"message":"forced"}`))
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
		a.query = r.URL.Query().Get("labelSelector")
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"n1","labels":{"meridian.io/instance-id":"i-1","meridian.io/managed-by":"node-registrar"}},"spec":{"providerID":"aws:///us-west-2a/i-1"}}]}`))
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/nodes":
		var n kNode
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &n)
		a.created = append(a.created, n)
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/nodes/"):
		a.deleted = append(a.deleted, strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestKubeClientSendsTheNodeAsKubernetesJSON(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	k := &Kube{Base: srv.URL, Token: "tok"}
	n, _ := KWOK{}.Node(runningInstance())
	if err := k.Create(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if api.auth != "Bearer tok" {
		t.Fatalf("auth = %q", api.auth)
	}
	got := api.created[0]
	if got.Kind != "Node" || got.APIVersion != "v1" || got.Spec.ProviderID != n.ProviderID ||
		len(got.Spec.Taints) != 2 || got.Status.Allocatable["pods"] != "29" ||
		got.Metadata.Annotations["kwok.x-k8s.io/node"] != "fake" {
		t.Fatalf("sent %+v", got)
	}
}

func TestKubeListsOnlyManagedNodesByLabelSelector(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	nodes, err := (&Kube{Base: srv.URL, Token: "t"}).ManagedNodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].Labels[InstanceIDKey] != "i-1" {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	if api.query != "meridian.io/managed-by=node-registrar" {
		t.Fatalf("selector = %q; without it the registrar could delete nodes it does not own", api.query)
	}
}

func TestKubeTreatsConflictAndNotFoundAsSuccessButSurfacesOtherErrors(t *testing.T) {
	api := &fakeAPI{status: map[string]int{http.MethodPost: 409, http.MethodDelete: 404}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	k := &Kube{Base: srv.URL, Token: "t"}
	n, _ := KWOK{}.Node(runningInstance())
	if err := k.Create(context.Background(), n); err != nil {
		t.Errorf("409 on create should be success: %v", err)
	}
	if err := k.Delete(context.Background(), "n1"); err != nil {
		t.Errorf("404 on delete should be success: %v", err)
	}
	api.status = map[string]int{http.MethodPost: 403, http.MethodDelete: 500, http.MethodGet: 401}
	if k.Create(context.Background(), n) == nil || k.Delete(context.Background(), "n1") == nil {
		t.Error("403/500 must be errors")
	}
	if _, err := k.ManagedNodes(context.Background()); err == nil {
		t.Error("401 on list must be an error")
	}
}

func TestInClusterFailsOutsideACluster(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if _, err := InCluster(); err == nil {
		t.Fatal("expected an error outside a cluster")
	}
}

// Extra: a successful delete reaches the right path, with the name escaped.
func TestKubeDeleteTargetsTheNamedNode(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	if err := (&Kube{Base: srv.URL, Token: "t"}).Delete(context.Background(), "ip-10-0-1-10.x"); err != nil {
		t.Fatal(err)
	}
	if len(api.deleted) != 1 || api.deleted[0] != "ip-10-0-1-10.x" {
		t.Fatalf("deleted = %v", api.deleted)
	}
}
