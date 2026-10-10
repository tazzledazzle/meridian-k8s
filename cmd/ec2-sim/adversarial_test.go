package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func bufioReader(r io.Reader) *bufio.Reader { return bufio.NewReader(r) }

func runCode(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(context.Background(), args, &out, &errb, nil)
	return code, out.String(), errb.String()
}

func TestAdvExitCodesForBadInvocations(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"unknown flag", []string{"-bogus"}, 2},
		{"flag missing value", []string{"-listen"}, 2},
		{"bool flag with junk", []string{"-allow-any-interface=maybe"}, 2},
		{"empty listen", []string{"-listen="}, 1},
		{"no port", []string{"-listen=127.0.0.1"}, 1},
		{"bare ipv6 no brackets", []string{"-listen=::1"}, 1},
		{"garbage with opt-in", []string{"-listen=%%%", "-allow-any-interface"}, 1},
		{"hostname not loopback", []string{"-listen=example.com:0"}, 1},
		{"bad port", []string{"-listen=127.0.0.1:99999"}, 1},
		{"0.0.0.0 refused", []string{"-listen=0.0.0.0:0"}, 1},
		{"all interfaces shorthand refused", []string{"-listen=:0"}, 1},
		{"ipv6 any refused", []string{"-listen=[::]:0"}, 1},
		{"LAN address refused", []string{"-listen=192.168.1.10:0"}, 1},
		{"positional arg", []string{"-listen=127.0.0.1:0", "x"}, 1},
		{"help", []string{"-h"}, 2},
	}
	for _, c := range cases {
		code, out, errs := runCode(c.args...)
		if code != c.want {
			t.Errorf("%s: exit %d, want %d (%s)", c.name, code, c.want, errs)
		}
		if out != "" {
			t.Errorf("%s: stdout not empty: %q", c.name, out)
		}
	}
}

func TestAdvHelpShowsLoopbackDefaultAndOptIn(t *testing.T) {
	_, _, errs := runCode("-h")
	if !strings.Contains(errs, "127.0.0.1:4570") || !strings.Contains(errs, "allow-any-interface") {
		t.Errorf("usage: %s", errs)
	}
}

func TestAdvLoopbackVariantsBindAndOthersDoNot(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		ctx, cancel := context.WithCancel(context.Background())
		ready := make(chan string, 1)
		done := make(chan int, 1)
		go func() { done <- run(ctx, []string{"-listen=" + addr}, io.Discard, io.Discard, ready) }()
		select {
		case got := <-ready:
			host, _, _ := net.SplitHostPort(got)
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				t.Errorf("%s bound to non-loopback %s", addr, got)
			}
		case c := <-done:
			if addr == "[::1]:0" {
				t.Skipf("no IPv6 loopback here (exit %d)", c)
			}
			t.Errorf("%s: exit %d", addr, c)
		case <-time.After(5 * time.Second):
			t.Errorf("%s: not ready", addr)
		}
		cancel()
		if c := <-done; c != 0 {
			t.Errorf("%s: exit %d after cancel", addr, c)
		}
	}
}

// rawConn dials and returns the conn with a deadline.
func rawConn(t *testing.T, addr string, d time.Duration) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(d))
	t.Cleanup(func() { c.Close() })
	return c
}

func TestAdvGracefulShutdownLetsInFlightRequestFinish(t *testing.T) {
	addr, stop, stderr := start(t)
	c := rawConn(t, addr, 10*time.Second)
	body := "Action=DescribeInstanceTypes&InstanceType.1=m5.large"
	// headers and half the body: the request is in flight
	fmt.Fprintf(c, "POST / HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: %d\r\n\r\n%s", len(body), body[:20])
	time.Sleep(200 * time.Millisecond)
	done := make(chan int, 1)
	go func() { done <- stop() }()
	time.Sleep(300 * time.Millisecond)
	select {
	case code := <-done:
		t.Fatalf("exited with %d while a request was in flight", code)
	default:
	}
	// new connections are refused once shutdown has begun
	if nc, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		nc.Close()
		t.Error("listener still accepting during shutdown")
	}
	_, _ = io.WriteString(c, body[20:])
	resp, err := http.ReadResponse(bufioReader(c), nil)
	if err != nil {
		t.Fatalf("in-flight request was dropped: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "m5.large") {
		t.Errorf("in-flight response %d %s", resp.StatusCode, b)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d: %s", code, stderr)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("did not exit after in-flight request finished")
	}
}

func TestAdvStuckRequestIsForceClosedAfterGraceAndExitsNonZero(t *testing.T) {
	if testing.Short() {
		t.Skip("takes the full shutdown grace")
	}
	addr, stop, stderr := start(t)
	c := rawConn(t, addr, 20*time.Second)
	fmt.Fprint(c, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\nAction=")
	time.Sleep(200 * time.Millisecond)
	began := time.Now()
	code := stop()
	took := time.Since(began)
	if took < shutdownGrace-time.Second || took > shutdownGrace+3*time.Second {
		t.Errorf("shutdown took %v, want about %v", took, shutdownGrace)
	}
	if code != 1 || !strings.Contains(stderr.String(), "shutdown:") {
		t.Errorf("exit %d, stderr %s", code, stderr)
	}
}

func TestAdvSlowlorisHeadersAreCutOffByReadHeaderTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for ReadHeaderTimeout")
	}
	addr, stop, _ := start(t)
	defer stop()
	c := rawConn(t, addr, 30*time.Second)
	began := time.Now()
	fmt.Fprint(c, "POST / HTTP/1.1\r\nHost: x\r\n")
	// drip one header byte-line every second, never finishing the header block
	closed := make(chan time.Duration, 1)
	go func() {
		_, _ = io.Copy(io.Discard, c) // returns when the server closes
		closed <- time.Since(began)
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case d := <-closed:
			if d < 8*time.Second || d > 14*time.Second {
				t.Errorf("connection cut after %v, want about the 10s ReadHeaderTimeout", d)
			}
			return
		case <-tick.C:
			_, _ = fmt.Fprint(c, "X-Drip: a\r\n")
		}
	}
}

func TestAdvHugeHeadersGet431(t *testing.T) {
	addr, stop, _ := start(t)
	defer stop()
	for name, req := range map[string]string{
		"one huge header":   "GET / HTTP/1.1\r\nHost: x\r\nX-Big: " + strings.Repeat("a", 200<<10) + "\r\n\r\n",
		"many small header": "GET / HTTP/1.1\r\nHost: x\r\n" + strings.Repeat("X-A: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\r\n", 3000) + "\r\n",
		"huge request line": "GET /" + strings.Repeat("a", 200<<10) + " HTTP/1.1\r\nHost: x\r\n\r\n",
	} {
		c := rawConn(t, addr, 5*time.Second)
		_, _ = io.WriteString(c, req)
		resp, err := http.ReadResponse(bufioReader(c), nil)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if resp.StatusCode != 431 && resp.StatusCode != 414 {
			t.Errorf("%s: status %d, want 431 (or 414 for the request line)", name, resp.StatusCode)
		}
	}
}

func TestAdvNothingSecretIsPrintedAndOnlyLifecycleLines(t *testing.T) {
	addr, stop, stderr := start(t)
	req, _ := http.NewRequest("POST", "http://"+addr+"/", strings.NewReader("Action=DescribeInstanceTypes&InstanceType.1=SECRETTYPE"))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIASECRETKEYID/20260101/us-west-2/ec2/aws4_request, Signature=deadbeefSECRETSIG")
	req.Header.Set("X-Amz-Security-Token", "SECRETTOKEN")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if code := stop(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, s := range []string{"SECRET", "AKIA", "Signature", "Authorization", "Action="} {
		if strings.Contains(stderr.String(), s) {
			t.Errorf("stderr contains %q: %s", s, stderr)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if !strings.Contains(line, "ec2-sim: ") {
			t.Errorf("unprefixed log line %q", line)
		}
	}
}

func TestAdvServerHeaderLimitsAreTight(t *testing.T) {
	s := newServer("127.0.0.1:0", http.NotFoundHandler())
	if s.ReadHeaderTimeout > 15*time.Second || s.MaxHeaderBytes > 128<<10 || s.ReadTimeout > time.Minute || s.WriteTimeout > time.Minute {
		t.Errorf("limits too loose: %+v", s)
	}
}

func TestAdvSecondRunAfterShutdownCanRebindSamePort(t *testing.T) {
	addr, stop, _ := start(t)
	if c := stop(); c != 0 {
		t.Fatal(c)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"-listen=" + addr}, io.Discard, io.Discard, ready) }()
	select {
	case <-ready:
	case c := <-done:
		t.Fatalf("rebind failed with exit %d", c)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	cancel()
	if c := <-done; c != 0 {
		t.Errorf("exit %d", c)
	}
}
