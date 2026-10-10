package awscall

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

const (
	advAK     = "AKIDEXAMPLE"
	advSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
)

var advNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func advLookup(k string) (string, bool) { return advSecret, k == advAK }

func advVerify(r *http.Request, body []byte) error {
	_, err := awsapi.Verify(r, body, advLookup, advNow, time.Minute)
	return err
}

// readBodyOf returns the bytes that would go on the wire.
func wireBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	if r.Body == nil {
		return nil
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAdvEveryServiceBuildsAVerifiableRequest(t *testing.T) {
	for _, svc := range Services {
		r, body, err := BuildQuery("http://127.0.0.1:4566", svc, "us-west-2", "DescribeThings", "2016-11-15", []string{"A=1"}, advAK, advSecret, advNow)
		if err != nil {
			t.Fatalf("%s query: %v", svc, err)
		}
		if err := advVerify(r, body); err != nil {
			t.Errorf("%s query: %v", svc, err)
		}
		r, body, err = BuildJSON("http://127.0.0.1:4566", svc, "us-west-2", "X.Y", "1.1", `{"a":1}`, advAK, advSecret, advNow)
		if err != nil {
			t.Fatalf("%s json: %v", svc, err)
		}
		if err := advVerify(r, body); err != nil {
			t.Errorf("%s json: %v", svc, err)
		}
		r, body, err = BuildREST("http://127.0.0.1:4566", svc, "us-west-2", "GET", "/clusters", "", advAK, advSecret, advNow)
		if err != nil {
			t.Fatalf("%s rest: %v", svc, err)
		}
		if err := advVerify(r, body); err != nil {
			t.Errorf("%s rest: %v", svc, err)
		}
	}
}

func TestAdvQueryParamEdgeCases(t *testing.T) {
	cases := map[string][]string{
		"equals in value":  {"K=a=b=c"},
		"ampersand":        {"K=a&b=c"},
		"plus":             {"K=a+b"},
		"space":            {"K=a b"},
		"unicode":          {"K=日本語é"},
		"empty value":      {"K="},
		"repeated key":     {"K=1", "K=2"},
		"percent":          {"K=%41%zz"},
		"newline":          {"K=a\nb"},
		"unicode key":      {"Ключ=1"},
		"key with special": {"A.B.1=x", "Filter.1.Name=tag:Name"},
	}
	for name, params := range cases {
		r, body, err := BuildQuery("http://127.0.0.1:4566", "ec2", "us-west-2", "DescribeX", "2016-11-15", params, advAK, advSecret, advNow)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if err := advVerify(r, body); err != nil {
			t.Errorf("%s: verify: %v", name, err)
		}
		vals, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("%s: body not parseable: %v", name, err)
			continue
		}
		if vals.Get("Action") != "DescribeX" {
			t.Errorf("%s: action lost: %v", name, vals)
		}
	}
	// the value must round trip exactly
	_, body, _ := BuildQuery("http://h:1", "ec2", "r", "A", "v", []string{"K=a=b&c+d é"}, advAK, advSecret, advNow)
	if v, _ := url.ParseQuery(string(body)); v.Get("K") != "a=b&c+d é" {
		t.Errorf("value did not round trip: %q", v.Get("K"))
	}
	// repeated key: last wins (documented behaviour via Set); assert only a single value is sent
	_, body, _ = BuildQuery("http://h:1", "ec2", "r", "A", "v", []string{"K=1", "K=2"}, advAK, advSecret, advNow)
	if v, _ := url.ParseQuery(string(body)); len(v["K"]) != 1 {
		t.Errorf("repeated key sends %v", v["K"])
	}
	// key without '='
	if _, _, err := BuildQuery("http://h:1", "ec2", "r", "A", "v", []string{"novalue"}, advAK, advSecret, advNow); err == nil {
		t.Error("key without = must fail")
	}
	if _, _, err := BuildQuery("http://h:1", "ec2", "r", "A", "v", []string{"=v"}, advAK, advSecret, advNow); err == nil {
		t.Error("empty key must fail")
	}
}

type mutation struct {
	name string
	do   func(r *http.Request, body []byte) (*http.Request, []byte)
}

func TestAdvTamperingAfterBuildingFailsVerify(t *testing.T) {
	muts := []mutation{
		{"method", func(r *http.Request, b []byte) (*http.Request, []byte) { r.Method = "PUT"; return r, b }},
		{"path", func(r *http.Request, b []byte) (*http.Request, []byte) { r.URL.Path = "/other"; return r, b }},
		{"query", func(r *http.Request, b []byte) (*http.Request, []byte) { r.URL.RawQuery = "x=1"; return r, b }},
		{"body", func(r *http.Request, b []byte) (*http.Request, []byte) { return r, append(append([]byte{}, b...), ' ') }},
		{"host", func(r *http.Request, b []byte) (*http.Request, []byte) { r.Host = "evil.example:80"; return r, b }},
		{"content-type", func(r *http.Request, b []byte) (*http.Request, []byte) {
			r.Header.Set("Content-Type", "text/plain")
			return r, b
		}},
		{"date", func(r *http.Request, b []byte) (*http.Request, []byte) {
			r.Header.Set("X-Amz-Date", "20261009T120001Z")
			return r, b
		}},
	}
	builders := map[string]func() (*http.Request, []byte){
		"query": func() (*http.Request, []byte) {
			r, b, _ := BuildQuery("http://127.0.0.1:4566", "ec2", "us-west-2", "A", "v", nil, advAK, advSecret, advNow)
			return r, b
		},
		"json": func() (*http.Request, []byte) {
			r, b, _ := BuildJSON("http://127.0.0.1:4566", "ssm", "us-west-2", "AmazonSSM.Get", "1.1", `{}`, advAK, advSecret, advNow)
			return r, b
		},
		"rest": func() (*http.Request, []byte) {
			r, b, _ := BuildREST("http://127.0.0.1:4566", "eks", "us-west-2", "POST", "/clusters", `{"name":"x"}`, advAK, advSecret, advNow)
			return r, b
		},
	}
	for bn, mk := range builders {
		for _, m := range muts {
			r, b := mk()
			if err := advVerify(r, b); err != nil {
				t.Fatalf("%s baseline: %v", bn, err)
			}
			r, b = m.do(r, b)
			if err := advVerify(r, b); err == nil {
				t.Errorf("%s: tampering with %s still verifies", bn, m.name)
			}
		}
	}
	r, b, _ := BuildJSON("http://127.0.0.1:4566", "ssm", "us-west-2", "AmazonSSM.Get", "1.1", `{}`, advAK, advSecret, advNow)
	r.Header.Set("X-Amz-Target", "AmazonSSM.Delete")
	if advVerify(r, b) == nil {
		t.Error("target tamper verifies")
	}
}

// Every successfully built request must actually carry a signature. Sign fails closed by
// silently removing Authorization, which would make the builders return an UNSIGNED request with a nil error.
func TestBugBuildersNeverReturnUnsignedRequestWithoutError(t *testing.T) {
	paths := []string{"/ok", "/a%zz", "/a b", "/\x7f", "/clusters/%e0%a4%a", "/x?y=%zz", "/日本", "/a/../b", "/%2e%2e/x", "/a/", "/a;b", "/a#frag"}
	for _, p := range paths {
		r, b, err := BuildREST("http://127.0.0.1:4566", "eks", "us-west-2", "GET", p, "", advAK, advSecret, advNow)
		if err != nil {
			continue
		}
		// BUG (if it fails): no error, but no Authorization header -> would be sent unsigned.
		if r.Header.Get("Authorization") == "" {
			t.Errorf("BuildREST(%q) returned nil error and an unsigned request", p)
			continue
		}
		if e := advVerify(r, b); e != nil {
			t.Errorf("BuildREST(%q) does not verify: %v", p, e)
		}
	}
}

func TestAdvRESTPathEdgeCases(t *testing.T) {
	bad := []string{"", "x", "//evil.com/x", "//", "http://evil/x"}
	for _, p := range bad {
		if _, _, err := BuildREST("http://127.0.0.1:4566", "eks", "r", "GET", p, "", advAK, advSecret, advNow); err == nil {
			t.Errorf("path %q must be refused", p)
		}
	}
	// a path that carries "@" or backslash must not move the target host
	for _, p := range []string{"/@evil.com/x", "/\\evil.com", "/..;/x"} {
		r, _, err := BuildREST("http://127.0.0.1:4566", "eks", "r", "GET", p, "", advAK, advSecret, advNow)
		if err != nil {
			continue
		}
		if r.URL.Host != "127.0.0.1:4566" {
			t.Errorf("path %q changed the host to %q", p, r.URL.Host)
		}
	}
	// trailing slash, dot segments, encoded dots verify
	for _, p := range []string{"/", "/a/", "/a/../b", "/%2e%2e/x", "/a?b=c", "/a%2Fb"} {
		r, b, err := BuildREST("http://127.0.0.1:4566", "eks", "r", "GET", p, "", advAK, advSecret, advNow)
		if err != nil {
			t.Logf("path %q refused: %v", p, err)
			continue
		}
		if err := advVerify(r, b); err != nil {
			t.Errorf("path %q: %v", p, err)
		}
	}
}

func TestAdvEndpointForms(t *testing.T) {
	ok := []string{"http://127.0.0.1:4566", "http://127.0.0.1:4566/", "http://[::1]:4566", "https://example.com", "http://localhost", "HTTP://LOCALHOST:1", "http://127.0.0.1:4566///"}
	for _, e := range ok {
		r, body, err := BuildQuery(e, "ec2", "r", "A", "v", nil, advAK, advSecret, advNow)
		if err != nil {
			t.Errorf("%s: %v", e, err)
			continue
		}
		if err := advVerify(r, body); err != nil {
			t.Errorf("%s: verify %v", e, err)
		}
	}
	// "http://h:99999999" is accepted (fails at dial time); acceptable, not asserted.
	bad := []string{"", "127.0.0.1:4566", "ftp://h", "http://", "http://u:p@h", "http://h?x=1", "http://h#f", "://", "http:///x", "file:///etc/passwd", "http://h:abc"}
	for _, e := range bad {
		if _, _, err := BuildQuery(e, "ec2", "r", "A", "v", nil, advAK, advSecret, advNow); err == nil {
			t.Errorf("endpoint %q must be refused", e)
		}
	}
}

func TestAdvNonUTCClock(t *testing.T) {
	for _, loc := range []string{"Asia/Kolkata", "America/Los_Angeles", "Pacific/Kiritimati"} {
		l, err := time.LoadLocation(loc)
		if err != nil {
			t.Skip(err)
		}
		// near a date boundary in the local zone
		now := time.Date(2026, 10, 10, 1, 30, 0, 0, l)
		r, body, _ := BuildQuery("http://h:1", "ec2", "r", "A", "v", nil, advAK, advSecret, now)
		if _, err := awsapi.Verify(r, body, advLookup, now, time.Minute); err != nil {
			t.Errorf("%s: %v", loc, err)
		}
		if d := r.Header.Get("X-Amz-Date"); !strings.HasSuffix(d, "Z") || d[:8] != now.UTC().Format("20060102") {
			t.Errorf("%s: X-Amz-Date %q not UTC", loc, d)
		}
	}
}

func TestAdvSecretNeverAppearsInRequestOrErrors(t *testing.T) {
	secret := "SuperSecretValue/xyz+123"
	check := func(what string, r *http.Request, err error) {
		var sb strings.Builder
		if r != nil {
			for k, v := range r.Header {
				sb.WriteString(k + strings.Join(v, ",") + "\n")
			}
			sb.WriteString(r.URL.String() + r.Host)
			sb.Write(wireBody(t, r))
		}
		if err != nil {
			sb.WriteString(err.Error())
		}
		if strings.Contains(sb.String(), secret) || strings.Contains(sb.String(), url.QueryEscape(secret)) {
			t.Errorf("%s leaks the secret: %s", what, sb.String())
		}
	}
	r, _, err := BuildQuery("http://h:1", "ec2", "r", "A", "v", []string{"x=1"}, advAK, secret, advNow)
	check("query ok", r, err)
	r, _, err = BuildQuery("http://h:1", "nope", "r", "A", "v", nil, advAK, secret, advNow)
	check("query unknown svc", r, err)
	r, _, err = BuildQuery("http://u:"+secret+"@h:1", "ec2", "r", "A", "v", nil, advAK, secret, advNow)
	check("endpoint creds", r, err)
	r, _, err = BuildJSON("http://h:1", "ssm", "r", "T", "9.9", "{", advAK, secret, advNow)
	check("json bad", r, err)
	r, _, err = BuildREST("http://h:1", "eks", "r", "GET", "x", "", advAK, secret, advNow)
	check("rest bad", r, err)
	_ = bytes.MinRead
}

func TestAdvJSONBodyForms(t *testing.T) {
	for _, b := range []string{`{}`, `[]`, `null`, `"s"`, `1`, ` {"a":"日本"} `, `{"a":"\u0000"}`} {
		r, body, err := BuildJSON("http://h:1", "ssm", "r", "T.X", "1.0", b, advAK, advSecret, advNow)
		if err != nil {
			t.Errorf("%q: %v", b, err)
			continue
		}
		if err := advVerify(r, body); err != nil {
			t.Errorf("%q: %v", b, err)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-amz-json-1.0" {
			t.Errorf("content type %q", got)
		}
	}
	for _, b := range []string{``, `{`, `{"a":}`, "\xff"} {
		if _, _, err := BuildJSON("http://h:1", "ssm", "r", "T.X", "1.1", b, advAK, advSecret, advNow); err == nil {
			t.Errorf("%q must be refused", b)
		}
	}
	// header injection through target
	r, _, err := BuildJSON("http://h:1", "ssm", "r", "T.X\r\nEvil: 1", "1.1", `{}`, advAK, advSecret, advNow)
	if err == nil && r.Header.Get("Evil") != "" {
		t.Error("header injection through target")
	}
}

// BUG: a region containing "/" (or other credential-scope delimiters) is accepted and signed,
// yielding an Authorization header the gateway will reject as malformed. Builders should refuse it.
func TestBugRegionWithScopeDelimiterIsAcceptedButUnverifiable(t *testing.T) {
	for _, reg := range []string{"a/b", "us-west-2,x", "a b", strings.Repeat("0", 65)} {
		r, body, err := BuildQuery("http://h:1", "ec2", reg, "A", "v", nil, advAK, advSecret, advNow)
		if err != nil {
			continue
		}
		if e := advVerify(r, body); e != nil {
			t.Errorf("region %q: builder returned nil error but request does not verify: %v", reg, e)
		}
	}
}
