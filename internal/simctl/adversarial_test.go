package simctl

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFlagCombinationsExitCodes(t *testing.T) {
	s := newStack(t, "SK")
	id := s.spot(t)
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want int
	}{
		{"no args", creds, nil, 2},
		{"only global flags", creds, []string{"-gateway", s.srv.URL}, 2},
		{"empty gateway", creds, []string{"-gateway", "", "interrupt", id}, 2},
		{"empty queue", creds, []string{"-gateway", s.srv.URL, "-queue", "", "interrupt", id}, 2},
		{"queue with dot", creds, []string{"-gateway", s.srv.URL, "-queue", "q.fifo", "interrupt", id}, 2},
		{"double dash then command", creds, []string{"-gateway", s.srv.URL, "--", "interrupt", "-grace", "1ms", id}, 0},
		{"unknown flag after command", creds, []string{"-gateway", s.srv.URL, "interrupt", "-bogus", id}, 2},
		{"empty id", creds, []string{"-gateway", s.srv.URL, "interrupt", ""}, 2},
		{"id with newline", creds, []string{"-gateway", s.srv.URL, "interrupt", id + "\n"}, 2},
		{"unicode id", creds, []string{"-gateway", s.srv.URL, "interrupt", "i-１２３４５６７８"}, 2},
		{"two ids", creds, []string{"-gateway", s.srv.URL, "interrupt", id, id}, 2},
		{"grace overflow", creds, []string{"-gateway", s.srv.URL, "interrupt", "-grace", "99999999999999h", id}, 2},
		{"timeout flag irrelevant to interrupt", creds, []string{"-gateway", s.srv.URL, "-timeout", "-5s", "interrupt", "-grace", "1ms", id}, 0},
		{"whitespace-only creds are not empty", map[string]string{"AWS_ACCESS_KEY_ID": " ", "AWS_SECRET_ACCESS_KEY": " "}, []string{"-gateway", s.srv.URL, "interrupt", "-grace", "1ms", "i-0000000000000dead"}, 1},
		{"newline in access key", map[string]string{"AWS_ACCESS_KEY_ID": "A\nK", "AWS_SECRET_ACCESS_KEY": "SK"}, []string{"-gateway", s.srv.URL, "interrupt", "-grace", "1ms", "i-0000000000000dead"}, 1},
		{"-help", creds, []string{"-help"}, 0},
		{"interrupt -h", creds, []string{"-gateway", s.srv.URL, "interrupt", "-h"}, 2},
		{"dead gateway", creds, []string{"-gateway", "http://127.0.0.1:1", "interrupt", "-grace", "1ms", id}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string(nil), c.args...)
			for i, a := range args {
				if a == id && c.want == 0 {
					args[i] = s.spot(t) // each success case consumes its own instance
				}
			}
			code, out, e := run(t, c.env, args...)
			if code != c.want {
				t.Fatalf("exit %d want %d out=%q err=%q", code, c.want, out, e)
			}
			if code != 0 && strings.Contains(out, "interrupted") {
				t.Fatalf("failure printed success: %q", out)
			}
		})
	}
}

func TestNeitherKeyNorSecretAppearsInAnyOutput(t *testing.T) {
	const ak, sk = "AKIAADVERSARIAL0001", "SECRETADVERSARIAL/zzz+9"
	hostile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "echo "+r.Header.Get("Authorization")+sk, 500)
	}))
	defer hostile.Close()
	ok200 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{\"x\":\"" + sk + "\"}")) }))
	defer ok200.Close()
	env := map[string]string{"AWS_ACCESS_KEY_ID": ak, "AWS_SECRET_ACCESS_KEY": sk}
	for _, gw := range []string{hostile.URL, ok200.URL, "http://127.0.0.1:1", "http://" + ak + ":" + sk + "@h"} {
		for _, args := range [][]string{{"interrupt", "-grace", "1ms", aid}, {"interrupt", "-any-market", "-grace", "0s", aid}} {
			_, out, e := run(t, env, append([]string{"-gateway", gw}, args...)...)
			if strings.Contains(out+e, sk) {
				t.Errorf("%s %v leaked the secret: %q %q", gw, args, out, e)
			}
		}
	}
}

// BUG (low): a backend that echoes the Authorization header makes simctl print the access key
// id and a valid-for-15-minutes signature; only the secret is scrubbed.
func TestBugAccessKeyIdEchoedByBackendIsPrinted(t *testing.T) {
	const ak = "AKIAADVERSARIAL0001"
	hostile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "echo "+r.Header.Get("Authorization"), 500)
	}))
	defer hostile.Close()
	env := map[string]string{"AWS_ACCESS_KEY_ID": ak, "AWS_SECRET_ACCESS_KEY": "zzqq"}
	_, out, e := run(t, env, "-gateway", hostile.URL, "interrupt", "-grace", "1ms", aid)
	if strings.Contains(out+e, ak) {
		t.Errorf("printed the access key id: %q", e)
	}
}

const aid = "i-0123456789abcdef0"

func TestProbeSemantics(t *testing.T) {
	// Exit 1 for refused, closed-after-accept, TLS-to-plain, hang; 0 for any status.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close() // accept then slam
		}
	}()
	defer ln.Close()
	if code, out, _ := run(t, nil, "-timeout", "1s", "probe", "http://"+ln.Addr().String()); code != 1 || !strings.Contains(out, "unreachable") {
		t.Errorf("slammed connection: %d %q", code, out)
	}
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tls.Close()
	if code, _, _ := run(t, nil, "-timeout", "1s", "probe", tls.URL); code != 1 {
		t.Errorf("untrusted TLS cert counts as reachable? exit %d", code)
	}
	if code, _, _ := run(t, nil, "-timeout", "1s", "probe", "http://"+strings.TrimPrefix(tls.URL, "https://")); code != 0 {
		t.Errorf("plain HTTP to a TLS port gets a 400 answer: exit %d", code)
	}
	for _, st := range []int{200, 204, 301, 401, 403, 404, 429, 500, 503} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(st) }))
		if code, _, _ := run(t, nil, "probe", srv.URL); code != 0 {
			t.Errorf("status %d: exit %d", st, code)
		}
		srv.Close()
	}
	for _, to := range []string{"-1s", "11m", "0", "abc"} {
		if code, _, _ := run(t, nil, "-timeout", to, "probe", "http://127.0.0.1:1"); code != 2 {
			t.Errorf("timeout %s: exit %d want 2", to, code)
		}
	}
	// A canceled context is "did not answer", not a panic.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if code, _, _ := runCtx(t, ctx, nil, "probe", srv.URL); code != 1 {
		t.Errorf("canceled ctx probe exit %d", code)
	}
}

func TestProbeSendsOnlyOneGETWithNoCredentials(t *testing.T) {
	var mu sync.Mutex
	var reqs []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mu.Lock(); reqs = append(reqs, r); mu.Unlock() }))
	defer srv.Close()
	run(t, creds, "probe", srv.URL+"/x/y")
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 1 || reqs[0].Method != "GET" || reqs[0].Header.Get("Authorization") != "" || reqs[0].URL.Path != "/x/y" {
		t.Fatalf("%+v", reqs)
	}
}

// BUG: probe's http.Client uses the default transport, which honours HTTP_PROXY/HTTPS_PROXY. The
// gate runs `probe` to prove a host has NO route out; with a (dead) proxy configured the probe
// reports "unreachable" (exit 1) although a direct route exists, a false pass of the isolation
// gate. The probe should bypass proxies (Transport{Proxy: nil}).
func TestBugProbeIgnoresProxyEnvironment(t *testing.T) {
	addrs, _ := net.InterfaceAddrs()
	var ip string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			ip = n.IP.String()
			break
		}
	}
	if ip == "" {
		t.Skip("no non-loopback IPv4 address (Go never proxies loopback)")
	}
	ln, err := net.Listen("tcp", ip+":0")
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	// Go reads the proxy environment once per process, so the check runs in a child test process
	// started with the proxy variables already set (otherwise the result depends on test order).
	if os.Getenv("SIMCTL_PROXY_CHILD") == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBugProbeIgnoresProxyEnvironment$", "-test.v")
		cmd.Env = append(os.Environ(), "SIMCTL_PROXY_CHILD="+srv.URL, "HTTP_PROXY=http://127.0.0.1:1", "http_proxy=http://127.0.0.1:1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child: %v\n%s", err, out)
		}
		return
	}
	if code, out, _ := run(t, nil, "-timeout", "2s", "probe", os.Getenv("SIMCTL_PROXY_CHILD")); code != 0 {
		t.Fatalf("direct route exists but probe says exit %d %q (proxy env was used)", code, out)
	}
}

func TestConcurrentRunsAreIndependent(t *testing.T) {
	s := newStack(t, "SK")
	const n = 12
	ids := make([]string, n)
	for i := range ids {
		ids[i] = s.spot(t)
	}
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _, _ = run(t, creds, "-gateway", s.srv.URL, "interrupt", "-grace", "1ms", ids[i])
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 0 {
			t.Errorf("%s exit %d", ids[i], c)
		}
	}
}

func TestSecondInterruptOfTheSameInstanceExitsOne(t *testing.T) {
	s := newStack(t, "SK")
	id := s.spot(t)
	if c, _, e := run(t, creds, "-gateway", s.srv.URL, "interrupt", "-grace", "1ms", id); c != 0 {
		t.Fatal(c, e)
	}
	time.Sleep(10 * time.Millisecond)
	if c, _, _ := run(t, creds, "-gateway", s.srv.URL, "interrupt", "-grace", "1ms", id); c != 1 {
		t.Fatalf("re-interrupt of a terminating instance exit %d, want 1", c)
	}
}

// FuzzRunArgs: random argument vectors never panic, exit with 0/1/2 only, never print the secret,
// and never print success unless exit 0. Network is pinned to a closed local port; inputs that
// could redirect to another host are skipped.
func FuzzRunArgs(f *testing.F) {
	f.Add("interrupt\x00-grace\x001ms\x00" + aid)
	f.Add("-queue\x00x\x00interrupt\x00" + aid)
	f.Add("probe")
	f.Add("")
	f.Fuzz(func(t *testing.T, joined string) {
		low := strings.ToLower(joined)
		if strings.Contains(low, "gateway") || strings.Contains(low, "probe") || strings.Contains(low, "http") || strings.Contains(low, "//") {
			t.Skip()
		}
		args := append([]string{"-gateway", "http://127.0.0.1:1"}, strings.Split(joined, "\x00")...)
		env := map[string]string{"AWS_ACCESS_KEY_ID": "AKFUZZ", "AWS_SECRET_ACCESS_KEY": "SECRETFUZZ"}
		code, out, e := run(t, env, args...)
		if code < 0 || code > 2 {
			t.Fatalf("exit %d", code)
		}
		if strings.Contains(out+e, "SECRETFUZZ") {
			t.Fatalf("leak: %q %q", out, e)
		}
		if code != 0 && strings.Contains(out, "interrupted ") {
			t.Fatalf("success text on failure")
		}
	})
}

func parseHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}

// FuzzProbeURL: the probe never contacts a host other than loopback port 1 in this fuzz.
func FuzzProbeURL(f *testing.F) {
	f.Add("/path")
	f.Add("@evil")
	f.Add("?q")
	f.Fuzz(func(t *testing.T, suffix string) {
		raw := "http://127.0.0.1:1" + suffix
		if u, err := parseHost(raw); err != nil || u != "127.0.0.1" {
			t.Skip()
		}
		code, _, _ := run(t, nil, "-timeout", "200ms", "probe", raw)
		if code < 1 && code != 0 || code > 2 {
			t.Fatalf("exit %d", code)
		}
	})
}
