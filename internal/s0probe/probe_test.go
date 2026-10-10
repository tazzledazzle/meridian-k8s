package s0probe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/ops"
)

func TestRunPassFailSkipAndGroups(t *testing.T) {
	checks := []Check{
		{Name: "a:One", Group: "a", Run: func(ctx context.Context, e *Env) (string, error) {
			e.State["x"] = "1"
			return "ok", nil
		}},
		{Name: "a:Two", Group: "a", Needs: []string{"x"}, Run: func(ctx context.Context, e *Env) (string, error) {
			return "", errors.New("missing field")
		}},
		{Name: "b:Three", Group: "b", Needs: []string{"never"}, Run: func(ctx context.Context, e *Env) (string, error) {
			return "unreachable", nil
		}},
		{Name: "c:Four", Group: "c", Run: func(ctx context.Context, e *Env) (string, error) { return "", nil }},
	}
	rs := Run(context.Background(), Config{}, checks)
	got := []string{rs[0].Status, rs[1].Status, rs[2].Status, rs[3].Status}
	want := []string{"pass", "fail", "skip", "pass"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("statuses = %v, want %v", got, want)
		}
	}
	if s := Summarize(rs); s.Pass != 2 || s.Fail != 1 || s.Skip != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if fg := FailedGroups(rs); strings.Join(fg, ",") != "a,b" {
		t.Fatalf("failed groups = %v", fg)
	}
	only := Run(context.Background(), Config{Groups: []string{"c"}}, checks)
	if len(only) != 1 || only[0].Name != "c:Four" {
		t.Fatalf("group filter = %+v", only)
	}
}

func TestMarkdownAndJSON(t *testing.T) {
	rs := []Result{{Name: "x:Y", Group: "x", Status: "fail", Detail: "a|b\nc"}}
	md := Markdown("T", rs)
	if !strings.Contains(md, "| x:Y | x | fail | a/b c |") || !strings.Contains(md, "custom backend") {
		t.Fatalf("markdown:\n%s", md)
	}
	js, err := JSON(rs)
	if err != nil || !strings.Contains(string(js), `"failed_groups"`) {
		t.Fatalf("json: %v %s", err, js)
	}
	if !strings.Contains(Markdown("T", []Result{{Name: "p", Group: "p", Status: "pass"}}), "Every group passed") {
		t.Fatal("all-pass markdown should say so")
	}
}

// TestSTSCheckAgainstFake runs a real check end to end over HTTP against a tiny fake.
func TestSTSCheckAgainstFake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "/sts/aws4_request") {
			http.Error(w, "not signed for sts", http.StatusForbidden)
			return
		}
		w.Write([]byte(`<GetCallerIdentityResponse><GetCallerIdentityResult><Account>000000000000</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`))
	}))
	defer srv.Close()
	rs := Run(context.Background(), Config{Endpoint: srv.URL, Region: "us-east-1", Access: "a", Secret: "s", Groups: []string{"sts"}}, Checks())
	if len(rs) != 1 || rs[0].Status != "pass" || !strings.Contains(rs[0].Detail, "000000000000") {
		t.Fatalf("results = %+v", rs)
	}
}

func TestChecksHaveUniqueNamesAndKnownGroups(t *testing.T) {
	seen := map[string]bool{}
	groups := map[string]bool{"sts": true, "ssm": true, "iam": true, "sqs": true, "eks": true, "ec2": true, "pricing": true}
	for _, c := range Checks() {
		if seen[c.Name] {
			t.Errorf("duplicate check %q", c.Name)
		}
		seen[c.Name] = true
		if !groups[c.Group] {
			t.Errorf("check %q has unknown group %q", c.Name, c.Group)
		}
	}
}

// TestEveryProbedOperationIsInOpsYaml keeps the probe honest: it may only
// exercise operations the gateway would let through.
func TestEveryProbedOperationIsInOpsYaml(t *testing.T) {
	l, err := ops.Load("../../ops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range Checks() {
		name := strings.Fields(c.Name)[0] // "ec2:DescribeSubnets (discovery tag)" -> "ec2:DescribeSubnets"
		svc, op, _ := strings.Cut(name, ":")
		if !l.Has(svc, op) {
			t.Errorf("check %q uses %s, which is not in ops.yaml", c.Name, name)
		}
	}
}

// --- framework behaviour beyond the plan (amendment A2 and the live findings) ---

// A gated check is skipped when its gate failed, with a message that says why.
func TestGatedChecksAreSkippedWhenTheGateFails(t *testing.T) {
	ran := map[string]bool{}
	mk := func(name, gate string, err error) Check {
		return Check{Name: name, Group: "g", GatedBy: gate, Run: func(ctx context.Context, e *Env) (string, error) {
			ran[name] = true
			return "ok", err
		}}
	}
	checks := []Check{mk("g:Gate", "", errors.New("boom")), mk("g:Coupled", "g:Gate", nil), mk("g:Reference", "", nil)}
	rs := Run(context.Background(), Config{}, checks)
	if rs[0].Status != "fail" || rs[1].Status != "skip" || rs[2].Status != "pass" {
		t.Fatalf("statuses = %+v", rs)
	}
	if ran["g:Coupled"] {
		t.Fatal("a gated check must not run after its gate failed")
	}
	if !strings.Contains(rs[1].Detail, "g:Gate failed") {
		t.Fatalf("skip detail should name the gate: %q", rs[1].Detail)
	}
	// A gate that was itself skipped also blocks.
	skippedGate := []Check{{Name: "g:Gate", Group: "g", Needs: []string{"nope"}, Run: checks[0].Run}, mk("g:Coupled", "g:Gate", nil)}
	rs = Run(context.Background(), Config{}, skippedGate)
	if rs[1].Status != "skip" || !strings.Contains(rs[1].Detail, "g:Gate did not run") {
		t.Fatalf("skipped gate: %+v", rs)
	}
}

func TestPanickingCheckIsAFailureNotACrash(t *testing.T) {
	rs := Run(context.Background(), Config{}, []Check{
		{Name: "a:Boom", Group: "a", Run: func(ctx context.Context, e *Env) (string, error) {
			var m []string
			return m[3], nil
		}},
		{Name: "a:After", Group: "a", Run: func(ctx context.Context, e *Env) (string, error) { return "", nil }},
	})
	if rs[0].Status != "fail" || !strings.Contains(rs[0].Detail, "panic") || rs[1].Status != "pass" {
		t.Fatalf("results = %+v", rs)
	}
}

// The report must never carry the credentials, even if a backend echoes them in an error.
func TestDetailsNeverContainCredentials(t *testing.T) {
	cfg := Config{Access: "AKIAEXAMPLEKEY", Secret: "s3cr3t/value+with=chars"}
	rs := Run(context.Background(), cfg, []Check{
		{Name: "a:Echo", Group: "a", Run: func(ctx context.Context, e *Env) (string, error) {
			return "", errors.New("denied for " + cfg.Access + " using " + cfg.Secret)
		}},
		{Name: "a:EchoOK", Group: "a", Run: func(ctx context.Context, e *Env) (string, error) {
			return "key " + cfg.Access, nil
		}},
	})
	for _, r := range rs {
		if strings.Contains(r.Detail, cfg.Access) || strings.Contains(r.Detail, cfg.Secret) {
			t.Fatalf("detail leaks a credential: %q", r.Detail)
		}
	}
}

// An interrupt must not leave checks unreported or counted as passes.
func TestRunMarksChecksNotRunAfterCancellationAsSkipped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ran := 0
	rs := Run(ctx, Config{}, []Check{
		{Name: "a:One", Group: "a", Run: func(ctx context.Context, e *Env) (string, error) { ran++; cancel(); return "ok", nil }},
		{Name: "a:Two", Group: "a", Run: func(ctx context.Context, e *Env) (string, error) { ran++; return "ok", nil }},
	})
	if ran != 1 || len(rs) != 2 || rs[0].Status != "pass" || rs[1].Status != "skip" || !strings.Contains(rs[1].Detail, "interrupted") {
		t.Fatalf("ran=%d results = %+v", ran, rs)
	}
}

// retryServer answers the first n requests with status, then 200 with an XML body.
func retryServer(n int32, status int) (*httptest.Server, *int32) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) <= n {
			http.Error(w, "warming up", status)
			return
		}
		w.Write([]byte(`<R><Account>1</Account></R>`))
	}))
	return srv, &hits
}

func fastCfg(url string) *Config {
	return &Config{Endpoint: url, Region: "us-west-2", Access: "AK", Secret: "SK", Retries: 3, Backoff: time.Millisecond, PollInterval: time.Millisecond, Wait: 200 * time.Millisecond}
}

func newEnv(cfg *Config) *Env {
	return &Env{Cfg: cfg, State: map[string]string{}, Lists: map[string][]string{}}
}

func TestQueryRetriesTransientServerErrors(t *testing.T) {
	srv, hits := retryServer(2, http.StatusServiceUnavailable)
	defer srv.Close()
	n, err := newEnv(fastCfg(srv.URL)).Query(context.Background(), "ec2", ec2Version, "DescribeAvailabilityZones")
	if err != nil || n.Text("Account") != "1" || atomic.LoadInt32(hits) != 3 {
		t.Fatalf("err=%v hits=%d", err, atomic.LoadInt32(hits))
	}
}

func TestQueryGivesUpAfterBoundedRetriesAndSaysSo(t *testing.T) {
	srv, hits := retryServer(1000, http.StatusBadGateway)
	defer srv.Close()
	_, err := newEnv(fastCfg(srv.URL)).Query(context.Background(), "ec2", ec2Version, "DescribeAvailabilityZones")
	if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "4 attempts") {
		t.Fatalf("err = %v", err)
	}
	if atomic.LoadInt32(hits) != 4 {
		t.Fatalf("hits = %d, want 1 try + 3 retries", atomic.LoadInt32(hits))
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	srv, hits := retryServer(1000, http.StatusBadRequest)
	defer srv.Close()
	if _, err := newEnv(fastCfg(srv.URL)).Query(context.Background(), "ec2", ec2Version, "DescribeAvailabilityZones"); err == nil || atomic.LoadInt32(hits) != 1 {
		t.Fatalf("err=%v hits=%d", err, atomic.LoadInt32(hits))
	}
}

// A 500 from a state-changing call is not retried (the backend may have acted); a 503 is.
func TestMutatingCallsAreNotRetriedOn500ButAreOn503(t *testing.T) {
	srv, hits := retryServer(1000, http.StatusInternalServerError)
	defer srv.Close()
	if _, err := newEnv(fastCfg(srv.URL)).Query(context.Background(), "ec2", ec2Version, "CreateFleet"); err == nil || atomic.LoadInt32(hits) != 1 {
		t.Fatalf("CreateFleet 500: err=%v hits=%d", err, atomic.LoadInt32(hits))
	}
	srv2, hits2 := retryServer(2, http.StatusServiceUnavailable)
	defer srv2.Close()
	if _, err := newEnv(fastCfg(srv2.URL)).Query(context.Background(), "ec2", ec2Version, "CreateFleet"); err != nil || atomic.LoadInt32(hits2) != 3 {
		t.Fatalf("CreateFleet 503: err=%v hits=%d", err, atomic.LoadInt32(hits2))
	}
	// A read-only call does retry a plain 500 (Floci answers 500 while a service warms up).
	srv3, hits3 := retryServer(2, http.StatusInternalServerError)
	defer srv3.Close()
	if _, err := newEnv(fastCfg(srv3.URL)).Query(context.Background(), "ec2", ec2Version, "DescribeImages"); err != nil || atomic.LoadInt32(hits3) != 3 {
		t.Fatalf("DescribeImages 500: err=%v hits=%d", err, atomic.LoadInt32(hits3))
	}
}

// Connection refused (the backend is still starting) is retried; the final error says how often.
func TestConnectionRefusedIsRetriedThenReported(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	_, err := newEnv(fastCfg(url)).Query(context.Background(), "ec2", ec2Version, "CreateFleet")
	if err == nil || !strings.Contains(err.Error(), "4 attempts") {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryBackoffStopsOnContextCancel(t *testing.T) {
	srv, _ := retryServer(1000, http.StatusServiceUnavailable)
	defer srv.Close()
	cfg := fastCfg(srv.URL)
	cfg.Retries, cfg.Backoff = 50, time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := newEnv(cfg).Query(ctx, "ec2", ec2Version, "DescribeAvailabilityZones"); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v after %s", err, time.Since(start))
	}
}

func TestOversizedResponseIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<R>"))
		chunk := []byte(strings.Repeat("a", 1<<20))
		for i := 0; i < 18; i++ {
			w.Write(chunk)
		}
		w.Write([]byte("</R>"))
	}))
	defer srv.Close()
	_, err := newEnv(fastCfg(srv.URL)).Query(context.Background(), "ec2", ec2Version, "DescribeInstanceTypes")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v", err)
	}
}
