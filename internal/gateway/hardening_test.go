package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

// logSink collects Logf output so tests can assert on what gets logged.
type logSink struct {
	mu   sync.Mutex
	logs []string
}

func (s *logSink) Logf(f string, a ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, fmt.Sprintf(f, a...))
}

func (s *logSink) all() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.logs, "\n")
}

// hardened builds a gateway over the standard test ops with a log sink.
func hardened(called map[string]*bool, trace bool, sink *logSink) http.Handler {
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
		Logf:        sink.Logf,
	})
}

func anyCalled(called map[string]*bool) bool {
	for _, c := range called {
		if *c {
			return true
		}
	}
	return false
}

// A body over the limit is refused with 413 and never reaches a backend.
func TestOversizedBodyIsRejected(t *testing.T) {
	called := map[string]*bool{}
	g := hardened(called, false, &logSink{})
	big := "Action=DescribeInstances&Pad=" + strings.Repeat("a", maxBody)
	rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", big, nil))
	if rec.Code != http.StatusRequestEntityTooLarge || anyCalled(called) {
		t.Fatalf("code=%d called=%v", rec.Code, anyCalled(called))
	}
}

// A request whose Body is nil (possible when a handler is called directly) must not panic.
func TestNilBodyDoesNotPanic(t *testing.T) {
	called := map[string]*bool{}
	g := hardened(called, false, &logSink{})
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566/?Action=DescribeInstances", nil)
	awsapi.Sign(r, nil, "AK", "SECRET", "us-west-2", "ec2", testNow)
	r.Body = nil
	if rec := do(g, r); rec.Body.String() != "ec2-ok" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// The backend must receive exactly the body the signature covered.
func TestBackendReceivesFullBody(t *testing.T) {
	var got string
	g := New(Config{
		Ops:    testOps(),
		Lookup: func(k string) (string, bool) { return "SECRET", k == "AK" },
		Now:    func() time.Time { return testNow },
		Backends: map[string]http.Handler{"ec2": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
		}), "pricing": nil, "eks": nil},
		Logf: func(string, ...any) {},
	})
	do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances&Version=2016-11-15", nil))
	if got != "Action=DescribeInstances&Version=2016-11-15" {
		t.Fatalf("backend saw %q", got)
	}
}

// The operation the gateway checks must be the operation the backend executes.
// Every way of presenting two different operations is refused.
func TestAmbiguousOperationsAreRefused(t *testing.T) {
	cases := []struct {
		name string
		req  func() *http.Request
	}{
		{"query service with X-Amz-Target pointing at an allowed op", func() *http.Request {
			return signedPost("ec2", "application/x-www-form-urlencoded", "Action=TerminateInstances",
				map[string]string{"X-Amz-Target": "Foo.DescribeInstances"})
		}},
		{"Action differs between URL and body", func() *http.Request {
			// The body names an allowed op; a backend that prefers the URL would run the other one.
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/?Action=TerminateInstances", strings.NewReader("Action=DescribeInstances"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			awsapi.Sign(r, []byte("Action=DescribeInstances"), "AK", "SECRET", "us-west-2", "ec2", testNow)
			return r
		}},
		{"repeated Action in the body", func() *http.Request {
			return signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances&Action=TerminateInstances", nil)
		}},
		{"repeated X-Amz-Target", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("{}"))
			r.Header.Set("Content-Type", "application/x-amz-json-1.1")
			r.Header.Add("X-Amz-Target", "AWSPriceListService.GetProducts")
			r.Header.Add("X-Amz-Target", "AWSPriceListService.DeleteEverything")
			awsapi.Sign(r, []byte("{}"), "AK", "SECRET", "us-west-2", "pricing", testNow)
			return r
		}},
		{"JSON service with Action instead of X-Amz-Target", func() *http.Request {
			return signedPost("pricing", "application/x-www-form-urlencoded", "Action=GetProducts", nil)
		}},
		{"JSON service called with GET", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566/", nil)
			r.Header.Set("X-Amz-Target", "AWSPriceListService.GetProducts")
			awsapi.Sign(r, nil, "AK", "SECRET", "us-west-2", "pricing", testNow)
			return r
		}},
		{"eks with X-Amz-Target on another path", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566/other", nil)
			r.Header.Set("X-Amz-Target", "X.DescribeCluster")
			awsapi.Sign(r, nil, "AK", "SECRET", "us-west-2", "eks", testNow)
			return r
		}},
		{"target with more than one dot", func() *http.Request {
			return signedPost("pricing", "application/x-amz-json-1.1", "{}", map[string]string{"X-Amz-Target": "A.B.GetProducts"})
		}},
		{"eks trailing slash", func() *http.Request { return signedGet("eks", "/clusters/c/") }},
		{"eks dot-dot cluster name", func() *http.Request { return signedGet("eks", "/clusters/..") }},
		{"eks extra path segment", func() *http.Request { return signedGet("eks", "/clusters/c/node-groups") }},
		{"eks wrong method", func() *http.Request {
			r := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1:4566/clusters/c", nil)
			awsapi.Sign(r, nil, "AK", "SECRET", "us-west-2", "eks", testNow)
			return r
		}},
		{"service not in the gateway's service table", func() *http.Request {
			return signedPost("s3", "application/x-www-form-urlencoded", "Action=DescribeInstances", nil)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called := map[string]*bool{}
			rec := do(hardened(called, false, &logSink{}), c.req())
			if rec.Code != http.StatusBadRequest || anyCalled(called) {
				t.Fatalf("code=%d called=%v body=%s", rec.Code, anyCalled(called), rec.Body)
			}
		})
	}
}

func signedGet(service, path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566"+path, nil)
	awsapi.Sign(r, nil, "AK", "SECRET", "us-west-2", service, testNow)
	return r
}

// Even in trace mode the ambiguity rules hold, otherwise trace mode is a bypass.
func TestTraceModeStillRefusesAmbiguousOperations(t *testing.T) {
	called := map[string]*bool{}
	rec := do(hardened(called, true, &logSink{}), signedPost("ec2", "application/x-www-form-urlencoded",
		"Action=TerminateInstances", map[string]string{"X-Amz-Target": "Foo.DescribeInstances"}))
	if rec.Code != http.StatusBadRequest || anyCalled(called) {
		t.Fatalf("code=%d called=%v", rec.Code, anyCalled(called))
	}
}

// An operation name is attacker-chosen (post-authentication). It must never be
// able to forge log lines or inject into the error message.
func TestHostileOperationNamesAreRefusedAndNeverLogged(t *testing.T) {
	called := map[string]*bool{}
	sink := &logSink{}
	g := hardened(called, false, sink)
	rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=Evil%0A2026%2F01%2F01+FAKE+LOG+LINE%3Cscript%3E", nil))
	if rec.Code != http.StatusBadRequest || anyCalled(called) {
		t.Fatalf("code=%d called=%v", rec.Code, anyCalled(called))
	}
	if strings.Contains(sink.all(), "\n2026") || strings.Contains(rec.Body.String(), "<script>") {
		t.Fatalf("hostile name leaked: log=%q body=%q", sink.all(), rec.Body)
	}
}

// Over-long operation names are refused too.
func TestOverlongOperationNameIsRefused(t *testing.T) {
	called := map[string]*bool{}
	rec := do(hardened(called, false, &logSink{}), signedPost("ec2", "application/x-www-form-urlencoded", "Action="+strings.Repeat("A", 200), nil))
	if rec.Code != http.StatusBadRequest || anyCalled(called) {
		t.Fatalf("code=%d", rec.Code)
	}
}

// A nil handler registered under a backend name is "not available", not a nil-pointer panic.
func TestNilBackendHandlerIsUnavailableNotAPanic(t *testing.T) {
	g := New(Config{
		Ops:      testOps(),
		Lookup:   func(k string) (string, bool) { return "SECRET", k == "AK" },
		Now:      func() time.Time { return testNow },
		Backends: map[string]http.Handler{"ec2": nil},
		Logf:     func(string, ...any) {},
	})
	rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

// Trace mode with no trace route for the service fails closed with 503.
func TestTraceModeWithoutRouteIsUnavailable(t *testing.T) {
	called := map[string]*bool{}
	g := hardened(called, true, &logSink{})
	rec := do(g, signedPost("pricing", "application/x-amz-json-1.1", "{}", map[string]string{"X-Amz-Target": "AWSPriceListService.Unlisted"}))
	if rec.Code != http.StatusServiceUnavailable || anyCalled(called) {
		t.Fatalf("code=%d called=%v", rec.Code, anyCalled(called))
	}
}

// A panicking backend yields an AWS-shaped 500, with no panic text in the response.
func TestBackendPanicBecomesInternalFailure(t *testing.T) {
	g := New(Config{
		Ops:    testOps(),
		Lookup: func(k string) (string, bool) { return "SECRET", k == "AK" },
		Now:    func() time.Time { return testNow },
		Backends: map[string]http.Handler{"ec2": http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom-secret-detail")
		})},
		Logf: func(string, ...any) {},
	})
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("panic escaped the gateway: %v", p)
		}
	}()
	rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances", nil))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

// http.ErrAbortHandler is net/http's deliberate abort signal and must keep propagating.
func TestErrAbortHandlerStillPropagates(t *testing.T) {
	g := New(Config{
		Ops:    testOps(),
		Lookup: func(k string) (string, bool) { return "SECRET", k == "AK" },
		Now:    func() time.Time { return testNow },
		Backends: map[string]http.Handler{"ec2": http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		})},
		Logf: func(string, ...any) {},
	})
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Fatal("ErrAbortHandler was swallowed")
		}
	}()
	do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances", nil))
}

// No Lookup at all means no key is valid: fail closed.
func TestNoLookupRejectsEverything(t *testing.T) {
	var c bool
	g := New(Config{Ops: testOps(), Now: func() time.Time { return testNow },
		Backends: map[string]http.Handler{"ec2": backend("x", &c)}, Logf: func(string, ...any) {}})
	rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances", nil))
	if rec.Code != 403 || c {
		t.Fatalf("code=%d called=%v", rec.Code, c)
	}
}

// Unauthenticated callers do not reach OnCall, so they cannot pollute the trace.
func TestOnCallOnlySeesAuthenticatedCalls(t *testing.T) {
	called := map[string]*bool{}
	n := 0
	g := newGateway(called, true, func(string, string, bool) { n++ })
	do(g, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("Action=ImportKeyPair")))
	if n != 0 {
		t.Fatalf("OnCall invoked %d times for an unauthenticated request", n)
	}
}

// Expired, unknown-key and wrong-secret requests map to their AWS codes.
func TestAuthErrorCodes(t *testing.T) {
	called := map[string]*bool{}
	g := newGateway(called, false, nil)

	old := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("Action=DescribeInstances"))
	old.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	awsapi.Sign(old, []byte("Action=DescribeInstances"), "AK", "SECRET", "us-west-2", "ec2", testNow.Add(-time.Hour))
	if rec := do(g, old); rec.Code != 403 || rec.Header().Get("X-Amzn-Errortype") != "RequestExpired" {
		t.Errorf("expired: %d %v", rec.Code, rec.Header())
	}

	unk := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("Action=DescribeInstances"))
	unk.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	awsapi.Sign(unk, []byte("Action=DescribeInstances"), "NOPE", "SECRET", "us-west-2", "ec2", testNow)
	if rec := do(g, unk); rec.Code != 403 || rec.Header().Get("X-Amzn-Errortype") != "InvalidClientTokenId" {
		t.Errorf("unknown key: %d %v", rec.Code, rec.Header())
	}

	bad := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("Action=DescribeInstances"))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	awsapi.Sign(bad, []byte("Action=DescribeInstances"), "AK", "WRONG", "us-west-2", "ec2", testNow)
	if rec := do(g, bad); rec.Code != 403 || rec.Header().Get("X-Amzn-Errortype") != "SignatureDoesNotMatch" {
		t.Errorf("wrong secret: %d %v", rec.Code, rec.Header())
	}
	if anyCalled(called) {
		t.Fatal("a rejected request reached a backend")
	}
}

// Neither the secret nor the signature may show up in logs or error bodies.
func TestSecretAndSignatureAreNeverLoggedOrReturned(t *testing.T) {
	called := map[string]*bool{}
	sink := &logSink{}
	g := hardened(called, false, sink)
	r := signedPost("ec2", "application/x-www-form-urlencoded", "Action=ImportKeyPair", nil)
	sig := r.Header.Get("Authorization")[strings.Index(r.Header.Get("Authorization"), "Signature=")+len("Signature="):]
	rec := do(g, r)
	bad := signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances", nil)
	bad.Header.Set("Authorization", strings.Replace(bad.Header.Get("Authorization"), "Signature=", "Signature=00", 1))
	rec2 := do(g, bad)
	for _, s := range []string{sink.all(), rec.Body.String(), rec2.Body.String()} {
		if strings.Contains(s, "SECRET") || strings.Contains(s, sig) {
			t.Fatalf("credential material leaked: %q", s)
		}
	}
}
