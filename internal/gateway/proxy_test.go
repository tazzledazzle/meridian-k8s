package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Only absolute http(s) URLs with a host are acceptable backends.
func TestProxyRejectsBadBackendURLs(t *testing.T) {
	for _, u := range []string{"", "floci:4566", "ftp://floci:4566", "file:///etc/passwd", "http://", "/relative", "http://user:pw@floci:4566", "http://floci:4566?x=1"} {
		if _, err := Proxy(u); err == nil {
			t.Errorf("Proxy(%q) accepted", u)
		}
	}
	if _, err := Proxy("http://floci:4566"); err != nil {
		t.Errorf("valid URL rejected: %v", err)
	}
}

// The signed Host and Authorization reach the backend unchanged; client-supplied
// forwarding headers do not, so a caller cannot spoof its origin to the emulator.
func TestProxyForwardsSignedHeadersButNotSpoofableOnes(t *testing.T) {
	var got http.Header
	var gotHost, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotHost, gotPath = r.Header.Clone(), r.Host, r.URL.RequestURI()
	}))
	defer upstream.Close()
	p, err := Proxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://gateway.example:4566/clusters/c?x=1", strings.NewReader("b"))
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AK/x")
	r.Header.Set("X-Forwarded-For", "10.9.9.9")
	r.Header.Set("X-Forwarded-Host", "evil")
	r.Header.Set("Forwarded", "for=10.9.9.9")
	r.Header.Set("X-Real-Ip", "10.9.9.9")
	r.Header.Set("Connection", "X-Hop")
	r.Header.Set("X-Hop", "secret-hop")
	do(p, r)
	if gotHost != "gateway.example:4566" {
		t.Errorf("Host = %q, want the signed host", gotHost)
	}
	if got.Get("Authorization") == "" {
		t.Error("Authorization was not forwarded")
	}
	// The gateway sets its own X-Forwarded-Host; only the spoofed value is forbidden.
	if got.Get("X-Forwarded-Host") == "evil" {
		t.Error("client-supplied X-Forwarded-Host reached the backend")
	}
	for _, h := range []string{"Forwarded", "X-Real-Ip", "X-Hop"} {
		if got.Get(h) != "" {
			t.Errorf("%s reached the backend: %q", h, got.Get(h))
		}
	}
	if strings.Contains(got.Get("X-Forwarded-For"), "10.9.9.9") {
		t.Errorf("client-supplied X-Forwarded-For reached the backend: %q", got.Get("X-Forwarded-For"))
	}
	if gotPath != "/clusters/c?x=1" {
		t.Errorf("path = %q", gotPath)
	}
}

// A backend that is down yields a 502 without the backend's address in the body.
func TestProxyBackendDownIs502WithoutLeakingAddress(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	p, err := Proxy(url)
	if err != nil {
		t.Fatal(err)
	}
	rec := do(p, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566/", nil))
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), strings.TrimPrefix(url, "http://")) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

// A backend that accepts but never answers is cut off with a 504.
func TestProxyHangingBackendTimesOut(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer upstream.Close()
	defer close(release)
	p, err := newProxy(upstream.URL, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	rec := do(p, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566/", nil))
	if rec.Code != http.StatusGatewayTimeout || time.Since(start) > 2*time.Second {
		t.Fatalf("code=%d after %v", rec.Code, time.Since(start))
	}
}
