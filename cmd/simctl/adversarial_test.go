package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/fakeec2"
)

func build(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "simctl")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

type fakeGW struct {
	srv   *httptest.Server
	store *fakeec2.Fake
	mu    sync.Mutex
	calls []string
}

func (g *fakeGW) log() string { g.mu.Lock(); defer g.mu.Unlock(); return strings.Join(g.calls, ",") }

func newGW(t *testing.T) *fakeGW {
	g := &fakeGW{store: fakeec2.New(time.Now, time.Millisecond)}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if _, err := awsapi.Verify(r, b, func(k string) (string, bool) { return "SK", k == "AK" }, time.Now(), 15*time.Minute); err != nil {
			http.Error(w, err.Error(), 403)
			return
		}
		rec := func(s string) { g.mu.Lock(); g.calls = append(g.calls, s); g.mu.Unlock() }
		if tg := r.Header.Get("X-Amz-Target"); tg != "" {
			rec(strings.TrimPrefix(tg, "AmazonSQS."))
			_, _ = w.Write([]byte(`{"QueueUrl":"http://floci:4566/000000000000/q","MessageId":"m"}`))
			return
		}
		q, _ := url.ParseQuery(string(b))
		if q.Get("Action") == "TerminateInstances" {
			rec("Terminate")
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		g.store.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func runBin(t *testing.T, bin string, env []string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, o.String(), e.String()
}

var goodEnv = []string{"AWS_ACCESS_KEY_ID=AK", "AWS_SECRET_ACCESS_KEY=SK", "PATH=" + os.Getenv("PATH")}

func TestBinaryExitCodes(t *testing.T) {
	bin := build(t)
	g := newGW(t)
	id := g.store.Launch("m5.large", "us-west-2a", "spot", nil)
	time.Sleep(5 * time.Millisecond)
	if c, _, _ := runBin(t, bin, goodEnv); c != 2 {
		t.Errorf("no args: %d", c)
	}
	if c, _, e := runBin(t, bin, []string{"PATH=" + os.Getenv("PATH")}, "interrupt", id); c != 2 || strings.Contains(e, "panic") {
		t.Errorf("no creds: %d %s", c, e)
	}
	if c, out, e := runBin(t, bin, goodEnv, "-gateway", g.srv.URL, "interrupt", "-grace", "1ms", id); c != 0 || !strings.Contains(out, "interrupted "+id) {
		t.Errorf("happy: %d %q %q", c, out, e)
	}
	if c, _, _ := runBin(t, bin, goodEnv, "-gateway", g.srv.URL, "interrupt", "-grace", "1ms", "i-0000000000000dead"); c != 1 {
		t.Errorf("unknown instance: %d", c)
	}
	if c, out, _ := runBin(t, bin, goodEnv, "probe", g.srv.URL); c != 0 || !strings.Contains(out, "reachable") {
		t.Errorf("probe: %d %q", c, out)
	}
	if c, _, _ := runBin(t, bin, goodEnv, "-timeout", "300ms", "probe", "http://127.0.0.1:1"); c != 1 {
		t.Errorf("probe closed: %d", c)
	}
}

func TestSignalsDuringGraceExitNonZeroAndLeaveTheInstanceRunning(t *testing.T) {
	bin := build(t)
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			g := newGW(t)
			id := g.store.Launch("m5.large", "us-west-2a", "spot", nil)
			time.Sleep(5 * time.Millisecond)
			cmd := exec.Command(bin, "-gateway", g.srv.URL, "interrupt", "-grace", "1h", id)
			cmd.Env = goodEnv
			var o, e bytes.Buffer
			cmd.Stdout, cmd.Stderr = &o, &e
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for !strings.Contains(g.log(), "SendMessage") {
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					t.Fatalf("warning never sent: %s %s", g.log(), e.String())
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				var ee *exec.ExitError
				if !errors.As(err, &ee) || ee.ExitCode() != 1 {
					t.Fatalf("want exit 1, got %v (stderr %q)", err, e.String())
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("process ignored the signal")
			}
			if !strings.Contains(e.String(), "NOT terminated") || strings.Contains(o.String(), "interrupted") {
				t.Errorf("out=%q err=%q", o.String(), e.String())
			}
			if st, _ := g.store.State(id); st != "running" || strings.Contains(g.log(), "Terminate") {
				t.Errorf("state %s calls %s: signal must not terminate", st, g.log())
			}
		})
	}
}

func TestSignalBeforeAnyCallStillExitsPromptly(t *testing.T) {
	bin := build(t)
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(20 * time.Second) }))
	defer hang.Close()
	cmd := exec.Command(bin, "-gateway", hang.URL, "interrupt", "-grace", "1s", "i-0123456789abcdef0")
	cmd.Env = goodEnv
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("exit 0 after SIGTERM mid-request")
		}
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("SIGTERM during an in-flight request was not honoured")
	}
}

func TestBinaryOutputNeverContainsCredentials(t *testing.T) {
	bin := build(t)
	env := []string{"AWS_ACCESS_KEY_ID=AKIASECRETKEYID1", "AWS_SECRET_ACCESS_KEY=VERYSECRETVALUE123", "PATH=" + os.Getenv("PATH")}
	for _, args := range [][]string{{"interrupt"}, {"-help"}, {"-gateway", "http://127.0.0.1:1", "interrupt", "-grace", "0s", "i-0123456789abcdef0"}, {"bogus"}} {
		_, o, e := runBin(t, bin, env, args...)
		if strings.Contains(o+e, "VERYSECRETVALUE123") {
			t.Errorf("%v leaked secret: %q %q", args, o, e)
		}
	}
}
