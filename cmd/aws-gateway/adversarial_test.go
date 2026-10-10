package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awscall"
	"github.com/tazzledazzle/meridian/internal/trace"
)

// startWith is start() with a custom environment and a separate stdout buffer.
func startWith(t *testing.T, env map[string]string, args ...string) (*started, *syncBuffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	st := &started{cancel: cancel, done: make(chan int, 1), errb: &syncBuffer{}}
	stdout := &syncBuffer{}
	go func() { st.done <- run(ctx, args, getenv(env), stdout, st.errb, ready) }()
	select {
	case st.addr = <-ready:
	case code := <-st.done:
		t.Fatalf("exited early with %d: %s", code, st.errb.String())
	case <-time.After(5 * time.Second):
		t.Fatal("gateway did not start")
	}
	t.Cleanup(cancel)
	return st, stdout
}

func okBackend(t *testing.T) (*httptest.Server, *atomic.Int64) {
	var n atomic.Int64
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		io.WriteString(w, "ok")
	}))
	t.Cleanup(b.Close)
	return b, &n
}

func TestAdvFlagMisuse(t *testing.T) {
	o := writeOps(t, "")
	b, _ := okBackend(t)
	be := "floci=" + b.URL
	cases := map[string][]string{
		"unknown flag":          {"-nope"},
		"secret flag":           {"-secret", "x", "-ops", o, "-backend", be},
		"access key flag":       {"-access-key", "x", "-ops", o, "-backend", be},
		"positional":            {"-listen", "127.0.0.1:0", "-ops", o, "-backend", be, "extra"},
		"trace-route no be":     {"-listen", "127.0.0.1:0", "-ops", o, "-backend", be, "-trace", "-trace-route", "ec2=ghost", "-trace-out", t.TempDir()},
		"trace-route bad svc":   {"-listen", "127.0.0.1:0", "-ops", o, "-backend", be, "-trace", "-trace-route", "s3=floci", "-trace-out", t.TempDir()},
		"trace-route no value":  {"-listen", "127.0.0.1:0", "-ops", o, "-backend", be, "-trace-route", "ec2="},
		"backend empty name":    {"-listen", "127.0.0.1:0", "-ops", o, "-backend", "=http://x"},
		"bad listen":            {"-listen", "nonsense", "-ops", o, "-backend", be},
		"bad listen with allow": {"-listen", "nonsense", "-allow-any-interface", "-ops", o, "-backend", be},
		"listen port too big":   {"-listen", "127.0.0.1:99999", "-ops", o, "-backend", be, "-trace-out", t.TempDir()},
		"ops is a dir":          {"-listen", "127.0.0.1:0", "-ops", t.TempDir(), "-backend", be},
		"-h":                    {"-h"},
		"ipv4 mapped public":    {"-listen", "8.8.8.8:0", "-ops", o, "-backend", be},
		"empty host":            {"-listen", ":0", "-ops", o, "-backend", be},
	}
	for name, args := range cases {
		code, errs := runArgs(t, goodEnv, args...)
		if code == 0 {
			t.Errorf("%s: exit 0, want non-zero (err=%q)", name, errs)
		}
	}
}

func TestAdvRecordAndTraceTogether(t *testing.T) {
	b, _ := okBackend(t)
	dir := t.TempDir()
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL,
		"-trace", "-record", "-trace-route", "ec2=floci", "-trace-out", dir)
	// both set: trace mode wins for forwarding (unlisted op forwarded)
	code, _ := call(t, st.addr, "DescribeCapacityReservations")
	t.Logf("-trace and -record together: unlisted op => HTTP %d (trace wins when 200)", code)
	if c := st.stop(t); c != 0 {
		t.Fatalf("exit %d", c)
	}
}

func TestAdvSecretsNeverOnStdoutOrStderrWithUnusualCredentials(t *testing.T) {
	creds := []struct{ ak, sk string }{
		{"AKIA0EXAMPLE", "s3cr3t/with+chars=and spaces"},
		{"AKIA1EXAMPLE", "秘密の鍵-ünï"},
		{"A", strings.Repeat("S", 5000)},
		{"AKIA3EXAMPLE", "`id`;rm -rf $(x)"},
	}
	for i, c := range creds {
		b, _ := okBackend(t)
		env := map[string]string{"AWS_ACCESS_KEY_ID": c.ak, "AWS_SECRET_ACCESS_KEY": c.sk}
		st, stdout := startWith(t, env, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL)
		req, _, err := awscall.BuildQuery("http://"+st.addr, "ec2", "us-west-2", "DescribeInstances", "2016-11-15", nil, c.ak, c.sk, time.Now())
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		bd, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%d: unusual-but-valid credentials rejected: %d %s", i, resp.StatusCode, bd)
		}
		// a wrong secret with the right key id
		req, _, _ = awscall.BuildQuery("http://"+st.addr, "ec2", "us-west-2", "DescribeInstances", "2016-11-15", nil, c.ak, "wrong", time.Now())
		resp, _ = http.DefaultClient.Do(req)
		wb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Errorf("%d: wrong secret got %d", i, resp.StatusCode)
		}
		if st.stop(t) != 0 {
			t.Errorf("%d: nonzero exit", i)
		}
		all := stdout.String() + st.errb.String() + string(wb)
		if strings.Contains(all, c.sk) {
			t.Errorf("%d: secret leaked", i)
		}
		if strings.Contains(stdout.String()+st.errb.String(), c.ak) {
			t.Errorf("%d: access key id logged", i)
		}
	}
}

func TestAdvUnsignedAndBadlySignedNeverReachBackend(t *testing.T) {
	b, hits := okBackend(t)
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL, "-record", "-trace-out", t.TempDir())
	body := "Action=DescribeInstances&Version=2016-11-15"
	send := func(mut func(*http.Request)) int {
		req, _ := http.NewRequest("POST", "http://"+st.addr+"/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		mut(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return -1
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	signed := func(r *http.Request) {
		s, _, _ := awscall.BuildQuery("http://"+st.addr, "ec2", "us-west-2", "DescribeInstances", "2016-11-15", nil, "AK", secretValue, time.Now())
		r.Header.Set("Authorization", s.Header.Get("Authorization"))
		r.Header.Set("X-Amz-Date", s.Header.Get("X-Amz-Date"))
	}
	for name, mut := range map[string]func(*http.Request){
		"no auth":   func(r *http.Request) {},
		"junk auth": func(r *http.Request) { r.Header.Set("Authorization", "Bearer x") },
		"empty sig": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AK/20260101/us-west-2/ec2/aws4_request, SignedHeaders=host, Signature=")
		},
		"auth other req": signed, // signature of a request with the same body: valid! counted separately below
	} {
		if name == "auth other req" {
			continue
		}
		before := hits.Load()
		code := send(mut)
		if code == 200 || hits.Load() != before {
			t.Errorf("%s: code=%d backend reached", name, code)
		}
	}
	// A signature copied onto a request with a different body must fail
	{
		before := hits.Load()
		req, _ := http.NewRequest("POST", "http://"+st.addr+"/", strings.NewReader("Action=TerminateInstances&Version=2016-11-15"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		signed(req)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 || hits.Load() != before {
				t.Errorf("replayed signature on different body reached backend (code %d)", resp.StatusCode)
			}
		}
	}
	st.stop(t)
}

func TestAdvLargeBodyAndHeaders(t *testing.T) {
	b, hits := okBackend(t)
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL)
	// > 1 MiB body, unsigned
	big := strings.NewReader(strings.Repeat("A", 2<<20))
	resp, err := http.Post("http://"+st.addr+"/", "application/x-www-form-urlencoded", big)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("2 MiB body: got %d want 413", resp.StatusCode)
		}
	} // a reset connection is also an acceptable refusal
	// > 64 KiB header
	req, _ := http.NewRequest("GET", "http://"+st.addr+"/", nil)
	req.Header.Set("X-Big", strings.Repeat("h", 100<<10))
	resp, err = http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
			t.Errorf("100 KiB header: got %d want 431", resp.StatusCode)
		}
	}
	if hits.Load() != 0 {
		t.Error("backend reached")
	}
	st.stop(t)
}

func TestAdvSlowlorisHeaderTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("takes ~10s")
	}
	b, _ := okBackend(t)
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL)
	c, err := net.Dial("tcp", st.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	c.Write([]byte("POST / HTTP/1.1\r\nHost: x\r\n"))
	// drip a header byte every second; never finish the header block
	closed := make(chan time.Duration, 1)
	go func() {
		buf := make([]byte, 1)
		c.SetReadDeadline(time.Now().Add(20 * time.Second))
		c.Read(buf) // returns on server close or response
		closed <- time.Since(start)
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
loop:
	for {
		select {
		case d := <-closed:
			if d > 14*time.Second {
				t.Errorf("slow-header connection survived %v (ReadHeaderTimeout is 10s)", d)
			}
			break loop
		case <-tick.C:
			c.Write([]byte("X: y\r\n"))
		}
	}
	st.stop(t)
}

func TestAdvShutdownGraceExpiryWithHungRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("takes ~5s")
	}
	hold := make(chan struct{})
	entered := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-hold
	}))
	defer backend.Close()
	defer close(hold)
	dir := t.TempDir()
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+backend.URL, "-record", "-trace-out", dir)
	go func() {
		req, _, _ := awscall.BuildQuery("http://"+st.addr, "ec2", "us-west-2", "DescribeInstances", "2016-11-15", nil, "AK", secretValue, time.Now())
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	begin := time.Now()
	st.cancel()
	select {
	case code := <-st.done:
		el := time.Since(begin)
		if el > shutdownGrace+3*time.Second {
			t.Errorf("shutdown took %v", el)
		}
		if code == 0 {
			t.Errorf("hung request forced a close; exit code should be non-zero, got 0")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run never returned with a hung request")
	}
	// the trace is still flushed
	if g, _ := trace.ReadGolden(filepath.Join(dir, "calls.golden")); len(g) != 1 {
		t.Errorf("trace not flushed after forced close: %v", g)
	}
}

func TestAdvAllowAnyInterfaceBindsAndWarns(t *testing.T) {
	b, _ := okBackend(t)
	st, _ := startWith(t, goodEnv, "-listen", "0.0.0.0:0", "-allow-any-interface", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL)
	host, _, _ := net.SplitHostPort(st.addr)
	if host != "0.0.0.0" && host != "::" && host != "[::]" {
		t.Errorf("bound to %q, want wildcard", st.addr)
	}
	if !strings.Contains(st.errb.String(), "WARNING") {
		t.Errorf("no warning logged: %q", st.errb.String())
	}
	_, port, _ := net.SplitHostPort(st.addr)
	if code, _ := call(t, "127.0.0.1:"+port, "DescribeInstances"); code != 200 {
		t.Errorf("call through loopback: %d", code)
	}
	if st.stop(t) != 0 {
		t.Error("nonzero exit")
	}
}

func TestAdvNoWarningOnLoopback(t *testing.T) {
	b, _ := okBackend(t)
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL)
	st.stop(t)
	if strings.Contains(st.errb.String(), "WARNING") {
		t.Errorf("unexpected warning: %q", st.errb.String())
	}
}

func TestAdvConcurrentRecordThenShutdownFlush(t *testing.T) {
	b, _ := okBackend(t)
	dir := t.TempDir()
	old := flushInterval
	flushInterval = 5 * time.Millisecond
	defer func() { flushInterval = old }()
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL, "-record", "-trace-out", dir)
	var wg sync.WaitGroup
	var sent atomic.Int64
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				action := "DescribeInstances"
				if (g+i)%2 == 0 {
					action = fmt.Sprintf("Unlisted%d", i%5)
				}
				req, _, _ := awscall.BuildQuery("http://"+st.addr, "ec2", "us-west-2", action, "2016-11-15", nil, "AK", secretValue, time.Now())
				if resp, err := http.DefaultClient.Do(req); err == nil {
					resp.Body.Close()
					sent.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()
	if c := st.stop(t); c != 0 {
		t.Fatalf("exit %d: %s", c, st.errb.String())
	}
	g, err := trace.ReadGolden(filepath.Join(dir, "calls.golden"))
	if err != nil || len(g) != 6 {
		t.Fatalf("golden=%v err=%v sent=%d", g, err, sent.Load())
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("stray temp %s", e.Name())
		}
	}
}

func TestAdvTraceOutExistingFilesAreReplaced(t *testing.T) {
	b, _ := okBackend(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "calls.golden"), []byte("stale:Old\n"), 0o444)
	os.WriteFile(filepath.Join(dir, "calls.json"), []byte("garbage"), 0o400)
	os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("keep"), 0o644)
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL, "-record", "-trace-out", dir)
	call(t, st.addr, "DescribeInstances")
	if st.stop(t) != 0 {
		t.Fatal(st.errb.String())
	}
	g, _ := trace.ReadGolden(filepath.Join(dir, "calls.golden"))
	if len(g) != 1 || g[0] != "ec2:DescribeInstances" {
		t.Errorf("golden not replaced: %v", g)
	}
	if u, _ := os.ReadFile(filepath.Join(dir, "unrelated.txt")); string(u) != "keep" {
		t.Error("unrelated file touched")
	}
}

func TestAdvTraceOutIsAFileOrDirectoryLikeThings(t *testing.T) {
	b, _ := okBackend(t)
	o := writeOps(t, "")
	f := filepath.Join(t.TempDir(), "afile")
	os.WriteFile(f, []byte("x"), 0o644)
	code, _ := runArgs(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", o, "-backend", "floci="+b.URL, "-record", "-trace-out", f)
	if code == 0 {
		t.Error("trace-out is a regular file: expected refusal")
	}
	// calls.golden is a directory
	d := t.TempDir()
	os.Mkdir(filepath.Join(d, "calls.golden"), 0o755)
	code, _ = runArgs(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", o, "-backend", "floci="+b.URL, "-record", "-trace-out", d)
	if code == 0 {
		t.Error("calls.golden is a directory: expected refusal")
	}
	// nested non-existent directory is created
	n := filepath.Join(t.TempDir(), "a", "b", "c")
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", o, "-backend", "floci="+b.URL, "-record", "-trace-out", n)
	st.stop(t)
	if _, err := os.Stat(filepath.Join(n, "calls.golden")); err != nil {
		t.Errorf("nested dir not created: %v", err)
	}
}

func TestAdvTraceOutSymlinkedGoldenDoesNotWriteThrough(t *testing.T) {
	b, _ := okBackend(t)
	d := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("precious"), 0o644)
	if err := os.Symlink(victim, filepath.Join(d, "calls.golden")); err != nil {
		t.Skip(err)
	}
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL, "-record", "-trace-out", d)
	st.stop(t)
	if v, _ := os.ReadFile(victim); string(v) != "precious" {
		t.Errorf("symlink target overwritten: %q", v)
	}
}

// After a successful startup the directory becomes read-only: the final flush fails and the exit code must say so.
func TestAdvTraceOutBecomesReadOnlyAfterStartup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	b, _ := okBackend(t)
	d := t.TempDir()
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL, "-record", "-trace-out", d)
	call(t, st.addr, "DescribeInstances")
	os.Chmod(d, 0o555)
	defer os.Chmod(d, 0o755)
	if c := st.stop(t); c == 0 {
		t.Errorf("final trace write failed but exit code is 0 (log: %s)", st.errb.String())
	}
	ents, _ := os.ReadDir(d)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("stray temp %s", e.Name())
		}
	}
}

func TestAdvCleanShutdownIsZeroAndStdoutQuiet(t *testing.T) {
	b, _ := okBackend(t)
	st, stdout := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL)
	if c := st.stop(t); c != 0 {
		t.Fatalf("exit %d", c)
	}
	if stdout.String() != "" {
		t.Errorf("stdout not empty: %q", stdout.String())
	}
}

func TestAdvServerDyingOnItsOwnIsNotTestable(t *testing.T) {
	t.Skip("run() owns the listener and exposes no hook to make Serve fail while ctx is live; covered by code review only")
}

func TestAdvNoTraceFlagsMeansNoFilesAndNoRecording(t *testing.T) {
	b, _ := okBackend(t)
	d := filepath.Join(t.TempDir(), "should-not-exist")
	st, _ := startWith(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL, "-trace-out", d)
	call(t, st.addr, "DescribeInstances")
	st.stop(t)
	if _, err := os.Stat(d); err == nil {
		t.Error("trace dir created without -trace/-record")
	}
}

func TestAdvIPv6LoopbackListen(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no ipv6")
	}
	ln.Close()
	b, _ := okBackend(t)
	st, _ := startWith(t, goodEnv, "-listen", "[::1]:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL)
	if code, _ := call(t, st.addr, "DescribeInstances"); code != 200 {
		t.Errorf("code %d", code)
	}
	st.stop(t)
}

// BUG: the source comment says "the URL itself is not echoed", but url.Parse errors embed the whole URL, userinfo included.
func TestBugBackendURLWithCredentialsEchoedOnParseFailure(t *testing.T) {
	_, errs := runArgs(t, goodEnv, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci=http://user:hunter2@host:99999999/\x7f")
	if strings.Contains(errs, "hunter2") {
		t.Errorf("backend credentials echoed: %q", errs)
	}
}

// An access key id that the gateway can never authenticate (it breaks the SigV4 credential scope) is accepted at startup.
func TestBugStartsWithAnAccessKeyThatCanNeverAuthenticate(t *testing.T) {
	b, _ := okBackend(t)
	env := map[string]string{"AWS_ACCESS_KEY_ID": "AK/WITH/SLASH", "AWS_SECRET_ACCESS_KEY": "s"}
	code, _ := runArgs(t, env, "-listen", "127.0.0.1:0", "-ops", writeOps(t, ""), "-backend", "floci="+b.URL)
	// runArgs cancels after 5s and a healthy server then exits 0; an early refusal would be non-zero.
	if code == 0 {
		t.Errorf("gateway started with an unusable access key id (exit 0)")
	}
}

var _ = bytes.MinRead
