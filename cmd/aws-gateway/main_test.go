package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awscall"
	"github.com/tazzledazzle/meridian/internal/gateway"
	"github.com/tazzledazzle/meridian/internal/ops"
	"github.com/tazzledazzle/meridian/internal/trace"
)

const secretValue = "TOPSECRET-VALUE-123"

var goodEnv = map[string]string{"AWS_ACCESS_KEY_ID": "AK", "AWS_SECRET_ACCESS_KEY": secretValue}

func getenv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// writeOps writes a small valid ops.yaml routing ec2:DescribeInstances to backend "floci".
func writeOps(t *testing.T, body string) string {
	t.Helper()
	if body == "" {
		body = "ops:\n  - {service: ec2, name: DescribeInstances, protocol: query, backend: floci, fidelity: behavior-inferred}\n"
	}
	p := filepath.Join(t.TempDir(), "ops.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// runArgs runs the command to completion (startup failures return immediately) and returns code and stderr.
func runArgs(t *testing.T, env map[string]string, args ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	code := run(ctx, args, getenv(env), &out, &errb, nil)
	if strings.Contains(out.String()+errb.String(), secretValue) {
		t.Fatalf("the secret leaked into the output: %q %q", out.String(), errb.String())
	}
	return code, errb.String()
}

func TestRefusesWithoutCredentials(t *testing.T) {
	ops := writeOps(t, "")
	for name, env := range map[string]map[string]string{
		"none":      {},
		"no secret": {"AWS_ACCESS_KEY_ID": "AK"},
		"no key id": {"AWS_SECRET_ACCESS_KEY": secretValue},
	} {
		code, errs := runArgs(t, env, "-listen", "127.0.0.1:0", "-ops", ops, "-backend", "floci=http://127.0.0.1:1")
		if code == 0 || !strings.Contains(errs, "AWS_ACCESS_KEY_ID") {
			t.Errorf("%s: code=%d err=%q", name, code, errs)
		}
	}
}

func TestRefusesNonLoopbackWithoutOptIn(t *testing.T) {
	ops := writeOps(t, "")
	code, errs := runArgs(t, goodEnv, "-listen", "0.0.0.0:0", "-ops", ops, "-backend", "floci=http://127.0.0.1:1")
	if code == 0 || !strings.Contains(errs, "loopback") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestOptInPassesTheBindCheck(t *testing.T) {
	// With the opt-in the bind check passes; the missing ops file is the next failure.
	code, errs := runArgs(t, goodEnv, "-listen", "0.0.0.0:0", "-allow-any-interface", "-ops", filepath.Join(t.TempDir(), "missing.yaml"))
	if code == 0 || strings.Contains(errs, "loopback") || !strings.Contains(errs, "ops") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestRefusesOpsBackendWithoutBackendFlag(t *testing.T) {
	ops := writeOps(t, "")
	code, errs := runArgs(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", ops, "-backend", "other=http://127.0.0.1:1")
	if code == 0 || !strings.Contains(errs, "floci") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	code, errs = runArgs(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", ops) // no backends at all
	if code == 0 || !strings.Contains(errs, "floci") {
		t.Fatalf("no flags: code=%d err=%q", code, errs)
	}
}

func TestRefusesInvalidBackendURL(t *testing.T) {
	ops := writeOps(t, "")
	for _, u := range []string{"file:///etc/passwd", "http://", "ftp://x", "http://user:pw@127.0.0.1:1", "not a url", "http://127.0.0.1:1?x=1"} {
		code, errs := runArgs(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", ops, "-backend", "floci="+u)
		if code == 0 || !strings.Contains(errs, "backend") {
			t.Errorf("%q: code=%d err=%q", u, code, errs)
		}
	}
}

func TestRefusesMalformedAndDuplicateBackendFlags(t *testing.T) {
	ops := writeOps(t, "")
	for _, arg := range []string{"floci", "=http://x", "floci="} {
		if code, _ := runArgs(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", ops, "-backend", arg); code == 0 {
			t.Errorf("%q should be refused", arg)
		}
	}
	code, errs := runArgs(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", ops, "-backend", "floci=http://127.0.0.1:1", "-backend", "floci=http://127.0.0.1:2")
	if code == 0 || !strings.Contains(errs, "twice") {
		t.Fatalf("duplicate backend: code=%d err=%q", code, errs)
	}
}

func TestValidateRefusesNilHandlerAndBadTraceRoutes(t *testing.T) {
	list := mustOps(t, writeOps(t, ""))
	cfg := gateway.Config{Ops: list, Backends: map[string]http.Handler{"floci": nil}}
	if err := validateWiring(cfg); err == nil || !strings.Contains(err.Error(), "floci") {
		t.Fatalf("nil handler: err = %v", err)
	}
	cfg.Backends["floci"] = http.NotFoundHandler()
	if err := validateWiring(cfg); err != nil {
		t.Fatalf("good wiring: %v", err)
	}
	cfg.TraceRoutes = map[string]string{"ec2": "ghost"}
	if err := validateWiring(cfg); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("trace route to unknown backend: err = %v", err)
	}
	cfg.TraceRoutes = map[string]string{"ec2": "floci"}
	cfg.Backends["floci"] = nil
	if err := validateWiring(cfg); err == nil {
		t.Fatal("trace route to a nil handler must be refused")
	}
	cfg.Backends["floci"] = http.NotFoundHandler()
	cfg.TraceRoutes = map[string]string{"s3": "floci"}
	if err := validateWiring(cfg); err == nil || !strings.Contains(err.Error(), "s3") {
		t.Fatalf("trace route for an unsupported service: err = %v", err)
	}
}

func TestRefusesEmptyOpsList(t *testing.T) {
	ops := writeOps(t, "ops: []\n")
	code, errs := runArgs(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", ops)
	if code == 0 || !strings.Contains(errs, "no operations") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestRefusesInvalidOpsFile(t *testing.T) {
	ops := writeOps(t, "ops:\n  - {service: ec2, name: X, protocol: bogus, backend: floci, fidelity: behavior-inferred}\n")
	if code, _ := runArgs(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", ops, "-backend", "floci=http://127.0.0.1:1"); code == 0 {
		t.Fatal("an invalid ops.yaml must stop startup")
	}
}

func TestRefusesToStartWhenTraceDirIsNotWritable(t *testing.T) {
	ops := writeOps(t, "")
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, errs := runArgs(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", ops, "-backend", "floci=http://127.0.0.1:1",
		"-record", "-trace-out", filepath.Join(blocker, "sub"))
	if code == 0 || !strings.Contains(errs, "trace") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestRefusesWhenListenAddressIsTaken(t *testing.T) {
	taken := httptest.NewServer(http.NotFoundHandler())
	defer taken.Close()
	addr := strings.TrimPrefix(taken.URL, "http://")
	ops := writeOps(t, "")
	code, _ := runArgs(t, goodEnv, "-listen", addr, "-ops", ops, "-backend", "floci=http://127.0.0.1:1")
	if code == 0 {
		t.Fatal("binding a taken port must fail")
	}
}

func TestServerHasAllTimeouts(t *testing.T) {
	s := newServer("127.0.0.1:0", http.NotFoundHandler())
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.WriteTimeout <= 0 || s.IdleTimeout <= 0 || s.MaxHeaderBytes <= 0 {
		t.Fatalf("timeouts not all set: %+v", s)
	}
	// The write timeout must outlast the proxy's 60s backend wait, or slow-but-valid calls would be cut off.
	if s.WriteTimeout <= 60*time.Second {
		t.Fatalf("WriteTimeout %v must exceed the backend timeout", s.WriteTimeout)
	}
}

// started is a running gateway under test.
type started struct {
	addr   string
	cancel context.CancelFunc
	done   chan int
	errb   *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func start(t *testing.T, args ...string) *started {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	st := &started{cancel: cancel, done: make(chan int, 1), errb: &syncBuffer{}}
	go func() { st.done <- run(ctx, args, getenv(goodEnv), io.Discard, st.errb, ready) }()
	select {
	case st.addr = <-ready:
	case code := <-st.done:
		t.Fatalf("exited early with %d: %s", code, st.errb.String())
	case <-time.After(5 * time.Second):
		t.Fatal("gateway did not start")
	}
	t.Cleanup(cancel)
	return st
}

func (s *started) stop(t *testing.T) int {
	t.Helper()
	s.cancel()
	select {
	case code := <-s.done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after the context was cancelled")
		return -1
	}
}

func call(t *testing.T, addr, action string) (int, string) {
	t.Helper()
	req, _, err := awscall.BuildQuery("http://"+addr, "ec2", "us-west-2", action, "2016-11-15", nil, "AK", secretValue, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestRecordModeWritesTraceOnShutdownWithoutForwardingUnlistedOps(t *testing.T) {
	var hits sync.Map
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		hits.Store(string(body), true)
		w.WriteHeader(200)
	}))
	defer backend.Close()
	dir := filepath.Join(t.TempDir(), "out")
	st := start(t, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+backend.URL, "-record", "-trace-out", dir)

	if code, _ := call(t, st.addr, "DescribeInstances"); code != 200 {
		t.Fatalf("supported call: %d", code)
	}
	if code, _ := call(t, st.addr, "DescribeCapacityReservations"); code != 400 {
		t.Fatalf("unlisted op in -record mode must be rejected, got %d", code)
	}
	if code := st.stop(t); code != 0 {
		t.Fatalf("exit code %d: %s", code, st.errb.String())
	}

	golden, err := trace.ReadGolden(filepath.Join(dir, "calls.golden"))
	if err != nil || strings.Join(golden, ",") != "ec2:DescribeCapacityReservations,ec2:DescribeInstances" {
		t.Fatalf("golden=%v err=%v", golden, err)
	}
	js, err := os.ReadFile(filepath.Join(dir, "calls.json"))
	if err != nil || !strings.Contains(string(js), `"ec2:DescribeCapacityReservations"`) || !strings.Contains(string(js), `"unsupported"`) {
		t.Fatalf("calls.json=%s err=%v", js, err)
	}
	n := 0
	hits.Range(func(_, _ any) bool { n++; return true })
	if n != 1 {
		t.Fatalf("backend saw %d requests, want only the supported one", n)
	}
}

func TestTraceModeForwardsUnlistedOpsViaTraceRouteAndWritesTrace(t *testing.T) {
	var got sync.Map
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.Store(string(body), true)
		io.WriteString(w, "ok")
	}))
	defer backend.Close()
	dir := t.TempDir()
	st := start(t, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+backend.URL,
		"-trace", "-trace-route", "ec2=floci", "-trace-out", dir)
	if code, body := call(t, st.addr, "DescribeCapacityReservations"); code != 200 || body != "ok" {
		t.Fatalf("trace mode should forward: %d %q", code, body)
	}
	if code := st.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "calls.golden")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "calls.json")); err != nil {
		t.Fatal(err)
	}
}

func TestWithoutTraceFlagsNothingIsWrittenAndUnlistedOpsAreRejected(t *testing.T) {
	backend := httptest.NewServer(http.NotFoundHandler())
	defer backend.Close()
	dir := filepath.Join(t.TempDir(), "never")
	st := start(t, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+backend.URL, "-trace-out", dir)
	if code, _ := call(t, st.addr, "Nope"); code != 400 {
		t.Fatalf("got %d", code)
	}
	if st.stop(t) != 0 {
		t.Fatal("non-zero exit")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("trace output must not be created when neither -trace nor -record is set")
	}
}

func TestUnauthenticatedCallsAreRejected(t *testing.T) {
	backend := httptest.NewServer(http.NotFoundHandler())
	defer backend.Close()
	st := start(t, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+backend.URL)
	resp, err := http.Post("http://"+st.addr+"/", "application/x-www-form-urlencoded", strings.NewReader("Action=DescribeInstances"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	st.stop(t)
}

func TestWrongSecretIsRejected(t *testing.T) {
	backend := httptest.NewServer(http.NotFoundHandler())
	defer backend.Close()
	st := start(t, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+backend.URL)
	req, _, _ := awscall.BuildQuery("http://"+st.addr, "ec2", "us-west-2", "DescribeInstances", "v", nil, "AK", "another-secret", time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	st.stop(t)
}

// Graceful shutdown: cancelling the context lets an in-flight request finish, then run returns 0.
func TestGracefulShutdownLetsInFlightRequestFinish(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		io.WriteString(w, "done")
	}))
	defer backend.Close()
	dir := t.TempDir()
	st := start(t, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+backend.URL, "-record", "-trace-out", dir)

	type result struct {
		code int
		body string
	}
	res := make(chan result, 1)
	go func() { c, b := call(t, st.addr, "DescribeInstances"); res <- result{c, b} }()
	<-entered
	st.cancel() // shutdown begins while the request is in flight
	time.Sleep(100 * time.Millisecond)
	select {
	case <-st.done:
		t.Fatal("run returned before the in-flight request finished")
	default:
	}
	close(release)
	r := <-res
	if r.code != 200 || r.body != "done" {
		t.Fatalf("in-flight request: %d %q", r.code, r.body)
	}
	select {
	case code := <-st.done:
		if code != 0 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return")
	}
	golden, _ := trace.ReadGolden(filepath.Join(dir, "calls.golden"))
	if len(golden) != 1 || golden[0] != "ec2:DescribeInstances" {
		t.Fatalf("trace flushed after the drain should include the call: %v", golden)
	}
}

func TestPeriodicFlushWritesTraceBeforeShutdown(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	dir := t.TempDir()
	old := flushInterval
	flushInterval = 50 * time.Millisecond
	defer func() { flushInterval = old }()
	st := start(t, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+backend.URL, "-record", "-trace-out", dir)
	call(t, st.addr, "DescribeInstances")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if g, _ := trace.ReadGolden(filepath.Join(dir, "calls.golden")); len(g) == 1 {
			st.stop(t)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	st.stop(t)
	t.Fatal("periodic flush never wrote the call")
}

func TestStartupLogDoesNotContainTheSecret(t *testing.T) {
	backend := httptest.NewServer(http.NotFoundHandler())
	defer backend.Close()
	st := start(t, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+backend.URL)
	call(t, st.addr, "Nope")
	st.stop(t)
	if strings.Contains(st.errb.String(), secretValue) || !strings.Contains(st.errb.String(), "listening") {
		t.Fatalf("log: %q", st.errb.String())
	}
}

func mustOps(t *testing.T, path string) ops.List {
	t.Helper()
	l, err := ops.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
