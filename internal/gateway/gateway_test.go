package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/ops"
)

var testNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// backend returns a handler that records that it was called and echoes a marker.
func backend(marker string, called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		_, _ = w.Write([]byte(marker))
	})
}

func testOps() ops.List {
	mk := func(svc, name, proto string) ops.Op {
		return ops.Op{Service: svc, Name: name, Protocol: proto, Backend: svc, Fidelity: "behavior-inferred"}
	}
	return ops.List{Ops: []ops.Op{
		mk("ec2", "DescribeInstances", "query"),
		mk("pricing", "GetProducts", "json"),
		mk("eks", "DescribeCluster", "restjson"),
	}}
}

func newGateway(called map[string]*bool, trace bool, onCall func(string, string, bool)) http.Handler {
	backends := map[string]http.Handler{}
	for _, svc := range []string{"ec2", "pricing", "eks"} {
		called[svc] = new(bool)
		backends[svc] = backend(svc+"-ok", called[svc])
	}
	return New(Config{
		Ops:         testOps(),
		Lookup:      func(k string) (string, bool) { return "SECRET", k == "AK" },
		Now:         func() time.Time { return testNow },
		Backends:    backends,
		TraceRoutes: map[string]string{"ec2": "ec2"},
		TraceMode:   trace,
		OnCall:      onCall,
		Logf:        func(string, ...any) {},
	})
}

// signedPost builds a POST signed for the given service.
func signedPost(service, contentType, body string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	awsapi.Sign(r, []byte(body), "AK", "SECRET", "us-west-2", service, testNow)
	return r
}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestRoutesQueryJSONAndRESTRequestsToTheirBackends(t *testing.T) {
	called := map[string]*bool{}
	g := newGateway(called, false, nil)

	ec2 := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances&Version=2016-11-15", nil))
	if ec2.Body.String() != "ec2-ok" {
		t.Fatalf("ec2: %d %s", ec2.Code, ec2.Body)
	}

	price := do(g, signedPost("pricing", "application/x-amz-json-1.1", `{}`, map[string]string{"X-Amz-Target": "AWSPriceListService.GetProducts"}))
	if price.Body.String() != "pricing-ok" {
		t.Fatalf("pricing: %d %s", price.Code, price.Body)
	}

	get := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566/clusters/meridian", nil)
	awsapi.Sign(get, nil, "AK", "SECRET", "us-west-2", "eks", testNow)
	if eks := do(g, get); eks.Body.String() != "eks-ok" {
		t.Fatalf("eks: %d %s", eks.Code, eks.Body)
	}
}

func TestAuthFailuresAreRejectedBeforeAnyBackend(t *testing.T) {
	called := map[string]*bool{}
	g := newGateway(called, false, nil)

	unsigned := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("Action=DescribeInstances"))
	// Auth errors use the JSON envelope, where the AWS error code is the X-Amzn-Errortype header.
	if rec := do(g, unsigned); rec.Code != 403 || rec.Header().Get("X-Amzn-Errortype") != "MissingAuthenticationToken" {
		t.Errorf("unsigned: %d %s %v", rec.Code, rec.Body, rec.Header())
	}

	// Reuse the headers of a validly signed request on a different body: the
	// signature was computed over "DescribeInstances", so it must not verify.
	signedA := signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances", nil)
	tampered := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("Action=TerminateInstances"))
	tampered.Header = signedA.Header.Clone()
	if rec := do(g, tampered); rec.Code != 403 || rec.Header().Get("X-Amzn-Errortype") != "SignatureDoesNotMatch" {
		t.Errorf("tampered: %d %s %v", rec.Code, rec.Body, rec.Header())
	}

	if *called["ec2"] {
		t.Fatal("a rejected request reached the backend")
	}
}

func TestUnsupportedOperationIsRejectedAndReported(t *testing.T) {
	called := map[string]*bool{}
	type call struct {
		svc, op   string
		supported bool
	}
	var seen []call
	g := newGateway(called, false, func(s, o string, ok bool) { seen = append(seen, call{s, o, ok}) })

	rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=ImportKeyPair", nil))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "UnsupportedOperation") || *called["ec2"] {
		t.Fatalf("code=%d body=%s called=%v", rec.Code, rec.Body, *called["ec2"])
	}
	if len(seen) != 1 || seen[0] != (call{"ec2", "ImportKeyPair", false}) {
		t.Fatalf("OnCall saw %+v", seen)
	}
}

func TestTraceModeForwardsUnsupportedOperationsButStillAuthenticates(t *testing.T) {
	called := map[string]*bool{}
	g := newGateway(called, true, nil)

	if rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=ImportKeyPair", nil)); rec.Body.String() != "ec2-ok" {
		t.Fatalf("trace mode should forward: %d %s", rec.Code, rec.Body)
	}
	unsigned := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("Action=ImportKeyPair"))
	if rec := do(g, unsigned); rec.Code != 403 {
		t.Fatalf("trace mode must not bypass authentication, got %d", rec.Code)
	}
}

func TestEachOperationGoesToItsOwnBackend(t *testing.T) {
	var fromFloci, fromSim bool
	l := ops.List{Ops: []ops.Op{
		{Service: "ec2", Name: "CreateFleet", Protocol: "query", Backend: "floci", Fidelity: "behavior-inferred"},
		{Service: "ec2", Name: "DescribeSpotPriceHistory", Protocol: "query", Backend: "ec2-sim", Fidelity: "behavior-inferred"},
	}}
	g := New(Config{
		Ops: l, Lookup: func(k string) (string, bool) { return "SECRET", k == "AK" },
		Now:      func() time.Time { return testNow },
		Backends: map[string]http.Handler{"floci": backend("floci", &fromFloci), "ec2-sim": backend("sim", &fromSim)},
		Logf:     func(string, ...any) {},
	})
	if rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=CreateFleet", nil)); rec.Body.String() != "floci" {
		t.Fatalf("CreateFleet: %s", rec.Body)
	}
	if rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeSpotPriceHistory", nil)); rec.Body.String() != "sim" {
		t.Fatalf("DescribeSpotPriceHistory: %s", rec.Body)
	}
	if !fromFloci || !fromSim {
		t.Fatal("both backends should have been used")
	}
	if missing := (Config{Ops: l, Backends: map[string]http.Handler{"floci": nil}}).MissingBackends(); len(missing) != 1 || missing[0] != "ec2-sim" {
		t.Fatalf("MissingBackends = %v", missing)
	}
}

func TestServiceWithoutBackendIsUnavailable(t *testing.T) {
	g := New(Config{
		Ops:    ops.List{Ops: []ops.Op{{Service: "ec2", Name: "DescribeInstances", Protocol: "query", Backend: "ec2", Fidelity: "behavior-inferred"}}},
		Lookup: func(k string) (string, bool) { return "SECRET", k == "AK" },
		Now:    func() time.Time { return testNow },
		Logf:   func(string, ...any) {},
	})
	rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "ServiceUnavailable") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestProxyForwardsMethodBodyAndHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		_, _ = w.Write([]byte(r.Method + ":" + string(b[:n]) + ":" + r.Header.Get("X-Test")))
	}))
	defer upstream.Close()
	p, err := Proxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("payload"))
	r.Header.Set("X-Test", "yes")
	if rec := do(p, r); rec.Body.String() != "POST:payload:yes" {
		t.Fatalf("got %q", rec.Body)
	}
}
