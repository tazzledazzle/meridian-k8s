package simctl

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/fakeec2"
)

func run(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	return runCtx(t, context.Background(), env, args...)
}

func runCtx(t *testing.T, ctx context.Context, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(ctx, args, func(k string) string { return env[k] }, &out, &errOut)
	return code, out.String(), errOut.String()
}

var creds = map[string]string{"AWS_ACCESS_KEY_ID": "AK", "AWS_SECRET_ACCESS_KEY": "SK"}

// stack is a fake gateway: it verifies SigV4 like the real one, serves EC2 from
// fakeec2 and records the SQS and terminate calls.
type stack struct {
	srv   *httptest.Server
	store *fakeec2.Fake
	mu    sync.Mutex
	calls []string
}

func (s *stack) record(c string) { s.mu.Lock(); s.calls = append(s.calls, c); s.mu.Unlock() }
func (s *stack) log() string     { s.mu.Lock(); defer s.mu.Unlock(); return strings.Join(s.calls, ",") }

func newStack(t *testing.T, secret string) *stack {
	t.Helper()
	s := &stack{store: fakeec2.New(time.Now, time.Millisecond)}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if _, err := awsapi.Verify(r, b, func(k string) (string, bool) { return secret, k == "AK" }, time.Now(), 15*time.Minute); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if target := r.Header.Get("X-Amz-Target"); strings.HasPrefix(target, "AmazonSQS.") {
			s.record(strings.TrimPrefix(target, "AmazonSQS."))
			_, _ = w.Write([]byte(`{"QueueUrl":"http://q/1"}`))
			return
		}
		q, _ := url.ParseQuery(string(b))
		if q.Get("Action") == "TerminateInstances" {
			s.record("Terminate")
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		s.store.ServeHTTP(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stack) spot(t *testing.T) string {
	id := s.store.Launch("m5.large", "us-west-2a", "spot", nil)
	time.Sleep(5 * time.Millisecond) // past the 1ms pending window
	return id
}

func TestBadUsageAndMissingCredentialsExitTwo(t *testing.T) {
	for _, args := range [][]string{{}, {"interrupt"}, {"probe"}, {"nonsense"}, {"-nope"}, {"interrupt", "a", "b"}, {"probe", "http://x", "y"}} {
		if code, _, _ := run(t, creds, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if code, _, e := run(t, nil, "interrupt", "i-abcdef012"); code != 2 || !strings.Contains(e, "AWS_ACCESS_KEY_ID") || !strings.Contains(e, "AWS_SECRET_ACCESS_KEY") {
		t.Errorf("no credentials: %d %s", code, e)
	}
	for _, env := range []map[string]string{{"AWS_ACCESS_KEY_ID": "AK"}, {"AWS_SECRET_ACCESS_KEY": "SK"}} {
		if code, _, _ := run(t, env, "interrupt", "i-abcdef012"); code != 2 {
			t.Errorf("half credentials %v: exit %d, want 2", env, code)
		}
	}
}

func TestInvalidInputsExitTwoBeforeAnyRequest(t *testing.T) {
	s := newStack(t, "SK")
	id := s.spot(t)
	cases := map[string][]string{
		"bad gateway scheme":   {"-gateway", "file:///x", "interrupt", id},
		"gateway userinfo":     {"-gateway", "http://u:p@127.0.0.1:1", "interrupt", id},
		"gateway query":        {"-gateway", s.srv.URL + "?a=b", "interrupt", id},
		"gateway fragment":     {"-gateway", s.srv.URL + "#a", "interrupt", id},
		"bad queue":            {"-gateway", s.srv.URL, "-queue", "a/b", "interrupt", id},
		"bad instance id":      {"-gateway", s.srv.URL, "interrupt", "i-x&Action=Terminate"},
		"negative grace":       {"-gateway", s.srv.URL, "interrupt", "-grace", "-1s", id},
		"unparsable grace":     {"-gateway", s.srv.URL, "interrupt", "-grace", "soon", id},
		"flag after the id":    {"-gateway", s.srv.URL, "interrupt", id, "-grace", "1ms"},
		"credentials via flag": {"-gateway", s.srv.URL, "interrupt", "-secret", "SK", id},
	}
	for name, args := range cases {
		if code, _, _ := run(t, creds, args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	if s.log() != "" {
		t.Fatalf("invalid input reached the network: %s", s.log())
	}
}

func TestInterruptCommandRunsTheSequenceAgainstTheGateway(t *testing.T) {
	s := newStack(t, "SK")
	id := s.spot(t)
	code, out, e := run(t, creds, "-gateway", s.srv.URL, "interrupt", "-grace", "1ms", id)
	if code != 0 || !strings.Contains(out, "interrupted "+id) {
		t.Fatalf("exit %d out=%q err=%q", code, out, e)
	}
	if got := s.log(); got != "GetQueueUrl,SendMessage,Terminate" {
		t.Fatalf("calls = %s", got)
	}
	if state, _ := s.store.State(id); state == "running" {
		t.Fatalf("instance should be terminating, is %s", state)
	}
}

func TestFailuresExitOne(t *testing.T) {
	s := newStack(t, "SK")
	if code, _, e := run(t, creds, "-gateway", s.srv.URL, "interrupt", "-grace", "1ms", "i-0000000000000dead"); code != 1 || e == "" {
		t.Fatalf("unknown instance: exit %d err %q, want 1", code, e)
	}
	od := s.store.Launch("m5.large", "us-west-2a", "", nil)
	time.Sleep(5 * time.Millisecond)
	if code, _, _ := run(t, creds, "-gateway", s.srv.URL, "interrupt", "-grace", "1ms", od); code != 1 {
		t.Fatalf("on-demand: exit %d, want 1", code)
	}
	if code, out, _ := run(t, creds, "-gateway", s.srv.URL, "interrupt", "-grace", "1ms", "-any-market", od); code != 0 || !strings.Contains(out, od) {
		t.Fatalf("-any-market: exit %d out %q, want 0", code, out)
	}
	wrong := map[string]string{"AWS_ACCESS_KEY_ID": "AK", "AWS_SECRET_ACCESS_KEY": "WRONG"}
	id := s.spot(t)
	if code, _, _ := run(t, wrong, "-gateway", s.srv.URL, "interrupt", "-grace", "1ms", id); code != 1 {
		t.Fatalf("bad signature: exit %d, want 1", code)
	}
	if state, _ := s.store.State(id); state != "running" {
		t.Fatalf("state = %s: a failed run must leave the instance alone", state)
	}
}

func TestCancelDuringGraceExitsNonZeroAndKeepsTheInstance(t *testing.T) {
	s := newStack(t, "SK")
	id := s.spot(t)
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		code     int
		out, err string
	}
	done := make(chan result, 1)
	go func() {
		c, o, e := runCtx(t, ctx, creds, "-gateway", s.srv.URL, "interrupt", "-grace", "1h", id)
		done <- result{c, o, e}
	}()
	for !strings.Contains(s.log(), "SendMessage") {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case r := <-done:
		// The cancel can land while SendMessage is still in flight ("deliver warning") or during the grace wait
		// ("NOT terminated"); either way the exit is 1 and nothing is terminated (checked below).
		if r.code != 1 || !(strings.Contains(r.err, "NOT terminated") || strings.Contains(r.err, "deliver warning")) || strings.Contains(r.out, "interrupted") {
			t.Fatalf("exit %d out=%q err=%q", r.code, r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("simctl did not return after cancel")
	}
	if state, _ := s.store.State(id); state != "running" || strings.Contains(s.log(), "Terminate") {
		t.Fatalf("state=%s calls=%s: cancel must not terminate", state, s.log())
	}
}

func TestSecretNeverAppearsInOutput(t *testing.T) {
	const secret = "TOPSECRETVALUE"
	hostile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "echo "+secret+" "+r.Header.Get("Authorization"), http.StatusBadRequest)
	}))
	defer hostile.Close()
	env := map[string]string{"AWS_ACCESS_KEY_ID": "AK", "AWS_SECRET_ACCESS_KEY": secret}
	for _, args := range [][]string{
		{"-gateway", hostile.URL, "interrupt", "-grace", "1ms", "i-0123456789abcdef0"},
		{"-gateway", hostile.URL, "interrupt", "-grace", "1ms", "-any-market", "i-0123456789abcdef0"},
		{"-gateway", "file:///x", "interrupt", "i-0123456789abcdef0"},
		{"interrupt"},
		{"-help"},
	} {
		_, out, e := run(t, env, args...)
		if strings.Contains(out+e, secret) {
			t.Errorf("%v leaked the secret: %q %q", args, out, e)
		}
	}
	if _, _, e := run(t, map[string]string{"AWS_SECRET_ACCESS_KEY": secret}, "interrupt", "i-0123456789abcdef0"); strings.Contains(e, secret) {
		t.Errorf("missing-credentials message leaked the secret: %q", e)
	}
}

func TestProbeDistinguishesReachableFromUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	code, out, _ := run(t, creds, "probe", srv.URL)
	if code != 0 || !strings.Contains(out, "reachable") || strings.Contains(out, "unreachable") {
		t.Fatalf("reachable: %d %q", code, out)
	}
	if code, _, _ := run(t, nil, "probe", srv.URL); code != 0 {
		t.Fatalf("probe must not need credentials, exit %d", code)
	}
	srv.Close()
	code, out, _ = run(t, creds, "-timeout", "500ms", "probe", srv.URL)
	if code != 1 || !strings.Contains(out, "unreachable") {
		t.Fatalf("unreachable: %d %q", code, out)
	}
}

func TestProbeCountsAnyHTTPAnswerAndDoesNotFollowRedirects(t *testing.T) {
	var hits int
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mu.Lock(); hits++; mu.Unlock() }))
	defer other.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
	defer redir.Close()
	if code, out, _ := run(t, nil, "probe", redir.URL); code != 0 || !strings.Contains(out, "302") {
		t.Fatalf("redirect: %d %q (a 302 is an answer)", code, out)
	}
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 503) }))
	defer fail.Close()
	if code, _, _ := run(t, nil, "probe", fail.URL); code != 0 {
		t.Fatalf("a 503 still proves a route exists, exit %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("the probe followed a redirect (%d hits)", hits)
	}
}

func TestProbeRejectsBadURLsWithExitTwo(t *testing.T) {
	for _, u := range []string{"", "file:///etc/passwd", "ftp://x", "http://", "http://u:p@127.0.0.1:1", "http://127.0.0.1:1/?a=b", "http://127.0.0.1:1/#f", "://bad", "no-scheme"} {
		if code, _, _ := run(t, nil, "probe", u); code != 2 {
			t.Errorf("probe %q: exit %d, want 2", u, code)
		}
	}
}

func TestProbeBoundsItsWaitByTimeoutAndContext(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) }))
	defer slow.Close()
	start := time.Now()
	if code, out, _ := run(t, nil, "-timeout", "50ms", "probe", slow.URL); code != 1 || !strings.Contains(out, "unreachable") || time.Since(start) > time.Second {
		t.Fatalf("exit %d out %q after %v", code, out, time.Since(start))
	}
	if code, _, _ := run(t, nil, "-timeout", "0s", "probe", slow.URL); code != 2 {
		t.Fatalf("zero timeout must be a usage error, got %d", code)
	}
}
