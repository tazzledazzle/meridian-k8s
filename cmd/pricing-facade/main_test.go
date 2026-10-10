package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// runArgs runs the command to completion and returns exit code and stderr.
func runArgs(t *testing.T, args ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	code := run(ctx, args, &out, &errb, nil)
	return code, errb.String()
}

func TestRefusesNonLoopbackWithoutOptIn(t *testing.T) {
	code, stderr := runArgs(t, "-listen=0.0.0.0:0")
	if code != 1 || !strings.Contains(stderr, "-allow-any-interface") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestRejectsGarbageAddressEvenWithOptIn(t *testing.T) {
	if code, _ := runArgs(t, "-listen=nonsense", "-allow-any-interface"); code != 1 {
		t.Fatalf("code=%d", code)
	}
}

func TestBadFlagsAndStrayArgumentsFail(t *testing.T) {
	if code, _ := runArgs(t, "-nope"); code != 2 {
		t.Fatalf("unknown flag: code=%d", code)
	}
	if code, stderr := runArgs(t, "-listen=127.0.0.1:0", "extra"); code != 1 || !strings.Contains(stderr, "unexpected arguments") {
		t.Fatalf("stray arg: code=%d stderr=%q", code, stderr)
	}
}

func TestPortInUseFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if code, stderr := runArgs(t, "-listen="+ln.Addr().String()); code != 1 || !strings.Contains(stderr, "listen") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestServesGetProductsAndShutsDownGracefully(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan int, 1)
	var errb bytes.Buffer
	go func() { done <- run(ctx, []string{"-listen=127.0.0.1:0"}, io.Discard, &errb, ready) }()
	var addr string
	select {
	case addr = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("server never became ready")
	}
	body := `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"TERM_MATCH","Field":"instanceType","Value":"m5.large"}]}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/", strings.NewReader(body))
	req.Header.Set("X-Amz-Target", "AWSPriceListService.GetProducts")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "m5.large") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	cancel() // SIGTERM equivalent
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d, stderr %s", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not shut down")
	}
	if strings.Contains(errb.String(), "m5.large") || strings.Contains(errb.String(), "ServiceCode") {
		t.Fatalf("request content leaked into the log: %s", errb.String())
	}
}

func TestServerHasEveryTimeoutSet(t *testing.T) {
	s := newServer("127.0.0.1:0", http.NewServeMux())
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.WriteTimeout <= 0 || s.IdleTimeout <= 0 || s.MaxHeaderBytes <= 0 {
		t.Fatalf("timeouts not all set: %+v", s)
	}
}
