package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// start runs the program on an ephemeral loopback port and returns its address and a stop function.
func start(t *testing.T, extra ...string) (addr string, stop func() int, stderr *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	stderr = &bytes.Buffer{}
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, append([]string{"-listen=127.0.0.1:0"}, extra...), io.Discard, stderr, ready)
	}()
	select {
	case addr = <-ready:
	case code := <-done:
		cancel()
		t.Fatalf("exited early with %d: %s", code, stderr)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("never became ready")
	}
	return addr, func() int {
		cancel()
		select {
		case c := <-done:
			return c
		case <-time.After(10 * time.Second):
			t.Fatal("did not shut down")
			return -1
		}
	}, stderr
}

func TestServesDescribeInstanceTypesAndShutsDownCleanly(t *testing.T) {
	addr, stop, stderr := start(t)
	resp, err := http.Post("http://"+addr+"/", "application/x-www-form-urlencoded", strings.NewReader("Action=DescribeInstanceTypes&InstanceType.1=m5.large"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "<instanceType>m5.large</instanceType>") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if strings.Contains(stderr.String(), "m5.large") || strings.Contains(stderr.String(), "Action=") {
		t.Errorf("log leaks request content: %s", stderr)
	}
	if !strings.Contains(stderr.String(), "listening on") {
		t.Errorf("no startup line: %s", stderr)
	}
}

func TestRefusesNonLoopbackWithoutOptIn(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"-listen=0.0.0.0:0"}, io.Discard, &stderr, nil); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "allow-any-interface") {
		t.Errorf("stderr should explain the opt-in: %s", &stderr)
	}
}

func TestAllowAnyInterfaceIsHonoured(t *testing.T) {
	addr, stop, stderr := start(t, "-allow-any-interface")
	_ = addr
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	// 0.0.0.0 only starts with the opt-in
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"-listen=0.0.0.0:0", "-allow-any-interface"}, io.Discard, io.Discard, ready)
	}()
	select {
	case <-ready:
	case c := <-done:
		t.Fatalf("exit %d", c)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	cancel()
	if c := <-done; c != 0 {
		t.Errorf("exit %d", c)
	}
}

func TestBadFlagsAndArguments(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want int
	}{
		"unknown flag":   {[]string{"-nope"}, 2},
		"positional":     {[]string{"extra"}, 1},
		"garbage listen": {[]string{"-listen=nonsense"}, 1},
		"port in use":    {nil, 1}, // filled below
	} {
		if name == "port in use" {
			continue
		}
		var stderr bytes.Buffer
		if code := run(context.Background(), tc.args, io.Discard, &stderr, nil); code != tc.want {
			t.Errorf("%s: exit %d, want %d (%s)", name, code, tc.want, &stderr)
		}
	}
}

func TestListenFailureIsReported(t *testing.T) {
	addr, stop, _ := start(t)
	defer stop()
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"-listen=" + addr}, io.Discard, &stderr, nil); code != 1 {
		t.Errorf("exit %d, want 1 (%s)", code, &stderr)
	}
}

func TestServerHasEveryTimeout(t *testing.T) {
	s := newServer("127.0.0.1:0", http.NotFoundHandler())
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.WriteTimeout <= 0 || s.IdleTimeout <= 0 || s.MaxHeaderBytes <= 0 {
		t.Errorf("missing timeout: %+v", s)
	}
}
