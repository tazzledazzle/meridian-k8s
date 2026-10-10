package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// FuzzGateway feeds arbitrary methods, paths, queries, headers and bodies.
// Invariants: the gateway never panics, and a backend (or OnCall) is only ever
// reached for a request that was produced by Sign and left untouched afterwards.
func FuzzGateway(f *testing.F) {
	f.Add("POST", "/", "", "Action=DescribeInstances", "", "", "application/x-www-form-urlencoded", uint8(1), "ec2")
	f.Add("POST", "/", "", "{}", "", "AmazonSQS.SendMessage", "application/x-amz-json-1.1", uint8(1), "sqs")
	f.Add("GET", "/clusters/c1", "", "", "", "", "", uint8(1), "eks")
	f.Add("POST", "/", "Action=RunInstances", "Action=DescribeInstances", "", "", "", uint8(1), "ec2")
	f.Add("POST", "/", "", "Action=DescribeInstances", "", "", "", uint8(0), "ec2")
	f.Add("POST", "/", "", "Action=DescribeInstances", "AWS4-HMAC-SHA256 Credential=a/20261009/us-west-2/ec2/aws4_request, SignedHeaders=host;x-amz-date, Signature=00", "", "", uint8(3), "ec2")
	f.Add("POST", "/", "", "Action=DescribeInstances", "", "Foo.Bar", "", uint8(2), "ec2")
	f.Add("GET", "//clusters/%2e%2e", "x=1", "", "", "", "", uint8(2), "eks")
	f.Add("POST", "/", "", "Action=ImportKeyPair", "", "", "", uint8(5), "ec2")

	l := advOps(f)
	f.Fuzz(func(t *testing.T, method, path, rawQuery, body, auth, target, ct string, mode uint8, service string) {
		if len(body) > 1<<16 {
			t.Skip()
		}
		var calls atomic.Int64
		be := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			_, _ = w.Write([]byte("ok"))
		})
		oc := &advOnCalls{}
		backends := map[string]http.Handler{}
		for _, n := range l.Backends() {
			backends[n] = be
		}
		routes := map[string]string{}
		for _, s := range advServices {
			routes[s] = "floci"
		}
		g := New(Config{Ops: l, Lookup: advLookup, Now: func() time.Time { return testNow },
			Backends: backends, TraceRoutes: routes, TraceMode: mode&4 != 0,
			Logf: func(string, ...any) {}, OnCall: oc.fn})

		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		r, err := http.NewRequest(method, "http://127.0.0.1:4566"+path, bytes.NewReader([]byte(body)))
		if err != nil {
			t.Skip()
		}
		r.URL.RawQuery = rawQuery
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		if target != "" {
			r.Header.Set("X-Amz-Target", target)
		}
		r.RemoteAddr = "192.0.2.1:1234"
		legit := false
		switch mode & 3 {
		case 0: // unsigned, optionally with a fuzzed Authorization
			if auth != "" {
				r.Header.Set("Authorization", auth)
			}
		case 1: // properly signed and untouched
			advSignAs(r, []byte(body), advAK, advSecret, service, testNow)
			legit = r.Header.Get("Authorization") != ""
		case 2: // signed, then tampered
			advSignAs(r, []byte(body), advAK, advSecret, service, testNow)
			switch len(body) % 3 {
			case 0:
				r.Body = io.NopCloser(strings.NewReader(body + "x"))
			case 1:
				r.URL.Path += "x"
			default:
				r.URL.RawQuery += "&t=1"
			}
		case 3: // signed, then Authorization replaced
			advSignAs(r, []byte(body), advAK, advSecret, service, testNow)
			r.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, r)
		if calls.Load() > 0 && !legit {
			t.Fatalf("backend called for a request not produced by Sign (mode %d): %s %s", mode, method, path)
		}
		if oc.n() > 0 && !legit {
			t.Fatalf("OnCall invoked for unauthenticated request (mode %d)", mode)
		}
		if rec.Code == 0 {
			t.Fatal("no status")
		}
	})
}
