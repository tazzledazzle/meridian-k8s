package spot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// clock is a mutex-guarded fake clock shared by the injector, the fake EC2 and the servers.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeAWS serves EC2 (from fakeec2) and SQS (a recorder) behind one URL and
// rejects any request whose SigV4 signature does not verify.
type fakeAWS struct {
	ec2   http.Handler
	clk   *clock
	mu    sync.Mutex
	log   []string // ordered "service:Operation" and "sleep" entries
	sends []map[string]string
	// knobs
	failGetQueueURL bool
	failSend        bool
	badSigs         []string
	targets         []string
}

func (f *fakeAWS) record(s string) { f.mu.Lock(); f.log = append(f.log, s); f.mu.Unlock() }
func (f *fakeAWS) entries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}
func (f *fakeAWS) count(prefix string) int {
	n := 0
	for _, e := range f.entries() {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	// Prove the request is signable: the same Verify the gateway uses, including that X-Amz-Target is signed.
	if _, err := awsapi.Verify(r, b, func(k string) (string, bool) { return "SK", k == "AK" }, f.clk.Now(), 15*time.Minute); err != nil {
		f.mu.Lock()
		f.badSigs = append(f.badSigs, err.Error())
		f.mu.Unlock()
		http.Error(w, "bad signature", http.StatusForbidden)
		return
	}
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		f.mu.Lock()
		f.targets = append(f.targets, target+"|"+r.Header.Get("Content-Type"))
		f.mu.Unlock()
		op := strings.TrimPrefix(target, "AmazonSQS.")
		f.record("sqs:" + op)
		switch op {
		case "GetQueueUrl":
			if f.failGetQueueURL {
				http.Error(w, `{"message":"down"}`, http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"QueueUrl":"http://aws-gateway:4566/123456789012/meridian-g0-interruption"}`))
		case "SendMessage":
			if f.failSend {
				http.Error(w, `{"message":"down"}`, http.StatusServiceUnavailable)
				return
			}
			var in map[string]string
			_ = json.Unmarshal(b, &in)
			f.mu.Lock()
			f.sends = append(f.sends, in)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"MessageId":"m1"}`))
		}
		return
	}
	q, _ := url.ParseQuery(string(b))
	f.record("ec2:" + q.Get("Action"))
	r.Body = io.NopCloser(bytes.NewReader(b))
	f.ec2.ServeHTTP(w, r)
}

type rig struct {
	in    *Injector
	store *fakeec2.Fake
	aws   *fakeAWS
	clk   *clock
	srv   *httptest.Server
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{clk: &clock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}}
	r.store = fakeec2.New(r.clk.Now, 3*time.Second)
	r.aws = &fakeAWS{ec2: r.store, clk: r.clk}
	r.srv = httptest.NewServer(r.aws)
	t.Cleanup(r.srv.Close)
	r.in = &Injector{Endpoint: r.srv.URL, Region: "us-west-2", QueueName: "meridian-g0-interruption", Access: "AK", Secret: "SK",
		Sleep: func(_ context.Context, d time.Duration) error {
			r.aws.record("sleep:" + d.String())
			r.clk.Add(d)
			return nil
		},
		Now: r.clk.Now}
	return r
}

func (r *rig) launch(market string) string {
	id := r.store.Launch("m5.large", "us-west-2a", market, nil)
	r.clk.Add(5 * time.Second) // past pending
	return id
}

func (r *rig) assertNothingSent(t *testing.T) {
	t.Helper()
	if e := r.aws.entries(); strings.Contains(strings.Join(e, ","), "sqs:SendMessage") || strings.Contains(strings.Join(e, ","), "TerminateInstances") || r.aws.count("sleep") != 0 {
		t.Fatalf("nothing should have been sent or terminated, log = %v", e)
	}
}

func TestInterruptWarnsWaitsThenTerminatesInThatOrder(t *testing.T) {
	r := newRig(t)
	id := r.launch("spot")
	if err := r.in.Interrupt(context.Background(), id, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	want := "ec2:DescribeInstances,sqs:GetQueueUrl,sqs:SendMessage,sleep:2m0s,ec2:TerminateInstances"
	if got := strings.Join(r.aws.entries(), ","); got != want {
		t.Fatalf("order = %s\nwant    %s", got, want)
	}
	if state, _ := r.store.State(id); state != "shutting-down" {
		t.Fatalf("instance state = %s, want shutting-down", state)
	}
	if len(r.aws.badSigs) != 0 {
		t.Fatalf("requests failed SigV4 verification: %v", r.aws.badSigs)
	}
	// SQS calls use the JSON 1.0 protocol with the target header signed (Verify enforces that when the header is present).
	for _, tg := range r.aws.targets {
		if !strings.HasSuffix(tg, "|application/x-amz-json-1.0") {
			t.Errorf("sqs call %q is not JSON 1.0", tg)
		}
	}
}

func TestWarningBodyIsTheEventForThatInstanceOnTheResolvedQueue(t *testing.T) {
	r := newRig(t)
	id := r.launch("spot")
	if err := r.in.Interrupt(context.Background(), id, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(r.aws.sends) != 1 {
		t.Fatalf("sends = %v", r.aws.sends)
	}
	s := r.aws.sends[0]
	if s["QueueUrl"] != "http://aws-gateway:4566/123456789012/meridian-g0-interruption" {
		t.Errorf("QueueUrl = %q: SendMessage must use the URL GetQueueUrl returned", s["QueueUrl"])
	}
	m := decode(t, []byte(s["MessageBody"]))
	if k := (karpParserKey{m.Version, m.Source, m.DetailType}); k != spotRegistryKey || m.Detail.InstanceID != id || m.Detail.InstanceAction != "terminate" || m.Region != "us-west-2" {
		t.Fatalf("message body = %+v", m)
	}
	if !m.Time.Equal(r.clk.Now().Add(-time.Second)) { // Now() at warn time, before the 1s grace
		t.Errorf("time = %v", m.Time)
	}
}

func TestRejectedInstancesSendNothing(t *testing.T) {
	r := newRig(t)
	onDemand := r.launch("")
	terminated := r.launch("spot")
	if _, err := r.in.query(context.Background(), "TerminateInstances", "InstanceId.1="+terminated); err != nil {
		t.Fatal(err)
	}
	pending := r.store.Launch("m5.large", "us-west-2a", "spot", nil) // launched last so the clock has not passed its pending window
	r.aws.mu.Lock()
	r.aws.log = nil // forget the setup call
	r.aws.mu.Unlock()
	for name, id := range map[string]string{"on-demand": onDemand, "pending": pending, "terminated": terminated, "unknown": "i-0000000000000dead"} {
		if err := r.in.Interrupt(context.Background(), id, time.Minute); err == nil {
			t.Errorf("%s instance must be rejected", name)
		}
	}
	r.assertNothingSent(t)
}

func TestAnyMarketSkipsOnlyTheSpotCheck(t *testing.T) {
	r := newRig(t)
	od := r.launch("")
	if err := r.in.Check(context.Background(), od); err == nil {
		t.Fatal("on-demand must fail without AnyMarket")
	}
	r.in.AnyMarket = true
	if err := r.in.Check(context.Background(), od); err != nil {
		t.Fatalf("AnyMarket should accept a running instance without instanceLifecycle: %v", err)
	}
	pending := r.store.Launch("m5.large", "us-west-2a", "", nil)
	if err := r.in.Check(context.Background(), pending); err == nil {
		t.Fatal("AnyMarket must still require running")
	}
	if err := r.in.Check(context.Background(), "i-0000000000000dead"); err == nil {
		t.Fatal("AnyMarket must still require existence")
	}
	if err := r.in.Interrupt(context.Background(), od, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestFailedWarningNeverTerminates(t *testing.T) {
	for name, set := range map[string]func(*fakeAWS){
		"GetQueueUrl fails": func(a *fakeAWS) { a.failGetQueueURL = true },
		"SendMessage fails": func(a *fakeAWS) { a.failSend = true },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			id := r.launch("spot")
			set(r.aws)
			if err := r.in.Interrupt(context.Background(), id, time.Minute); err == nil {
				t.Fatal("expected an error")
			}
			if state, _ := r.store.State(id); state != "running" || r.aws.count("sleep") != 0 || r.aws.count("ec2:TerminateInstances") != 0 {
				t.Fatalf("state=%s log=%v: a failed warning must not terminate anything", state, r.aws.entries())
			}
		})
	}
}

func TestCancelDuringGraceDoesNotTerminate(t *testing.T) {
	r := newRig(t)
	id := r.launch("spot")
	ctx, cancel := context.WithCancel(context.Background())
	r.in.Sleep = nil // exercise the real context-aware wait
	done := make(chan error, 1)
	go func() { done <- r.in.Interrupt(ctx, id, time.Hour) }()
	for r.aws.count("sqs:SendMessage") == 0 { // wait until the warning went out
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Interrupt did not return after cancel")
	}
	// Documented decision: aborting must never silently reclaim the instance.
	if state, _ := r.store.State(id); state != "running" || r.aws.count("ec2:TerminateInstances") != 0 {
		t.Fatalf("state=%s: cancellation must not terminate", state)
	}
}

func TestFailedWaitNeverTerminates(t *testing.T) {
	// The wait fails while ctx is still live, so only the explicit error check (not a dead ctx) can stop the terminate.
	r := newRig(t)
	id := r.launch("spot")
	r.in.Sleep = func(context.Context, time.Duration) error { return errors.New("wait failed") }
	if err := r.in.Interrupt(context.Background(), id, time.Hour); err == nil || !strings.Contains(err.Error(), "NOT terminated") {
		t.Fatalf("err = %v", err)
	}
	if r.aws.count("ec2:TerminateInstances") != 0 {
		t.Fatal("terminated after a failed wait")
	}
}

func TestCanceledBeforeStartSendsNothing(t *testing.T) {
	r := newRig(t)
	id := r.launch("spot")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.in.Interrupt(ctx, id, time.Second); err == nil {
		t.Fatal("expected an error")
	}
	r.assertNothingSent(t)
}

func TestTerminateFailureIsReported(t *testing.T) {
	r := newRig(t)
	id := r.launch("spot")
	r.in.Sleep = func(context.Context, time.Duration) error { r.srv.Close(); return nil } // EC2 vanishes during the grace
	if err := r.in.Interrupt(context.Background(), id, time.Second); err == nil {
		t.Fatal("a failed TerminateInstances must be an error")
	}
}

func TestInputValidationHappensBeforeAnyRequest(t *testing.T) {
	r := newRig(t)
	id := r.launch("spot")
	bad := map[string]func(*Injector){
		"file scheme":      func(i *Injector) { i.Endpoint = "file:///etc/passwd" },
		"ftp scheme":       func(i *Injector) { i.Endpoint = "ftp://x" },
		"no host":          func(i *Injector) { i.Endpoint = "http://" },
		"userinfo":         func(i *Injector) { i.Endpoint = "http://u:p@" + strings.TrimPrefix(r.srv.URL, "http://") },
		"query":            func(i *Injector) { i.Endpoint = r.srv.URL + "?x=1" },
		"fragment":         func(i *Injector) { i.Endpoint = r.srv.URL + "#f" },
		"empty access":     func(i *Injector) { i.Access = "" },
		"empty secret":     func(i *Injector) { i.Secret = "" },
		"bad region":       func(i *Injector) { i.Region = "us/west" },
		"empty queue":      func(i *Injector) { i.QueueName = "" },
		"queue with slash": func(i *Injector) { i.QueueName = "a/b" },
		"queue with quote": func(i *Injector) { i.QueueName = `a","x":"y` },
	}
	for name, mut := range bad {
		in := *r.in
		mut(&in)
		if err := in.Interrupt(context.Background(), id, time.Second); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	for _, badID := range []string{"", "x", "i-", "i-abc&Action=TerminateInstances", "i-abc def", "i-abc\n", strings.Repeat("a", 100)} {
		if err := r.in.Interrupt(context.Background(), badID, time.Second); err == nil {
			t.Errorf("instance id %q accepted", badID)
		}
	}
	if err := r.in.Interrupt(context.Background(), id, -time.Second); err == nil {
		t.Error("negative grace accepted")
	}
	if len(r.aws.entries()) != 0 {
		t.Fatalf("validation failures must not reach the network: %v", r.aws.entries())
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var hits int
	var mu sync.Mutex
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mu.Lock(); hits++; mu.Unlock() }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	in := &Injector{Endpoint: redir.URL, Region: "us-west-2", QueueName: "q", Access: "AK", Secret: "SK"}
	if err := in.Check(context.Background(), "i-0123456789abcdef0"); err == nil {
		t.Fatal("a redirect must be an error")
	}
	// Even a caller-supplied client must not be allowed to follow redirects.
	in.Client = &http.Client{Timeout: time.Second}
	if err := in.Check(context.Background(), "i-0123456789abcdef0"); err == nil {
		t.Fatal("a redirect must be an error with a custom client")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("the redirect target was contacted %d times", hits)
	}
}

func TestResponseReadsAreBounded(t *testing.T) {
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("a"), 1<<20)
		for i := 0; i < 64; i++ { // 64 MiB, far beyond the cap
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer huge.Close()
	in := &Injector{Endpoint: huge.URL, Region: "us-west-2", QueueName: "q", Access: "AK", Secret: "SK"}
	err := in.Check(context.Background(), "i-0123456789abcdef0")
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want a 'too large' error", err)
	}
}

func TestDefaultClientHasATimeoutAndCustomTimeoutsApply(t *testing.T) {
	if (&Injector{}).client().Timeout <= 0 {
		t.Fatal("the default client must have a timeout")
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer slow.Close()
	in := &Injector{Endpoint: slow.URL, Region: "us-west-2", QueueName: "q", Access: "AK", Secret: "SK", Client: &http.Client{Timeout: 30 * time.Millisecond}}
	start := time.Now()
	if err := in.Check(context.Background(), "i-0123456789abcdef0"); err == nil || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("err=%v after %v: timeout not applied", err, time.Since(start))
	}
}

func TestSecretNeverAppearsInErrors(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A hostile or buggy backend that echoes the Authorization header and the secret itself.
		http.Error(w, "rejected "+r.Header.Get("Authorization")+" secret=SUPERSECRET", http.StatusBadRequest)
	}))
	defer echo.Close()
	in := &Injector{Endpoint: echo.URL, Region: "us-west-2", QueueName: "q", Access: "AK", Secret: "SUPERSECRET"}
	for name, err := range map[string]error{
		"check":     in.Check(context.Background(), "i-0123456789abcdef0"),
		"warn":      in.Warn(context.Background(), "i-0123456789abcdef0"),
		"reclaim":   in.Reclaim(context.Background(), "i-0123456789abcdef0"),
		"interrupt": in.Interrupt(context.Background(), "i-0123456789abcdef0", 0),
	} {
		if err == nil || strings.Contains(err.Error(), "SUPERSECRET") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestTwoInterruptsOnDifferentInstancesRunConcurrently(t *testing.T) {
	r := newRig(t)
	a, b := r.launch("spot"), r.launch("spot")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = r.in.Interrupt(context.Background(), id, time.Second)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs = %v", errs)
	}
	for _, id := range []string{a, b} {
		if s, _ := r.store.State(id); s != "shutting-down" {
			t.Errorf("%s = %s", id, s)
		}
	}
	seen := map[string]bool{}
	for _, s := range r.aws.sends {
		seen[decode(t, []byte(s["MessageBody"])).Detail.InstanceID] = true
	}
	if len(seen) != 2 || !seen[a] || !seen[b] {
		t.Fatalf("each instance needs its own warning, got %v", seen)
	}
}

func TestMillisecondGraceUsesTheRealSleep(t *testing.T) {
	r := newRig(t)
	r.in.Sleep = nil
	id := r.launch("spot")
	if err := r.in.Interrupt(context.Background(), id, 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.store.State(id); s != "shutting-down" {
		t.Fatalf("state = %s", s)
	}
}
