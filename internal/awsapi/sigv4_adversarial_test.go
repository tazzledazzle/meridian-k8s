package awsapi

import (
	"bytes"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var advLookup = lookupFor("AK", "SECRET")

// mkReq builds a request for rawURL and signs it (service ec2, us-west-2).
func mkReq(t *testing.T, method, rawURL string, body []byte, hdr map[string]string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	Sign(r, body, "AK", "SECRET", "us-west-2", "ec2", t0)
	return r
}

func verifyOK(t *testing.T, name string, r *http.Request, body []byte) {
	t.Helper()
	if _, err := Verify(r, body, advLookup, t0, time.Minute); err != nil {
		t.Errorf("%s: want ok, got %v", name, err)
	}
}

// ---- 1. mismatching signatures, equal/different lengths, never panic ----
func TestAdvSignatureMismatchLengths(t *testing.T) {
	r := mkReq(t, "GET", "http://h.example/?a=b", nil, nil)
	good := r.Header.Get("Authorization")
	sig := good[len(good)-64:]
	prefix := strings.TrimSuffix(good, sig)
	last := "0"
	if sig[63] == '0' {
		last = "1"
	}
	cases := map[string]struct {
		sig  string
		want error
	}{
		"same length, last nibble flipped": {sig[:63] + last, ErrSignatureMismatch},
		"all zero same length":             {strings.Repeat("0", 64), ErrSignatureMismatch},
		"shorter":                          {sig[:63], ErrInvalidAuth},
		"longer":                           {sig + "0", ErrInvalidAuth},
		"empty":                            {"", ErrInvalidAuth},
		"uppercase hex":                    {strings.ToUpper(sig), ErrInvalidAuth},
	}
	for n, c := range cases {
		r2 := r.Clone(r.Context())
		r2.Header.Set("Authorization", prefix+c.sig)
		wantErr(t, n, c.want, r2, nil, advLookup, t0, time.Minute)
	}
}

// ---- 2. path canonicalisation ----
func canonPath(t *testing.T, service, rawURL string) string {
	t.Helper()
	r, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := canonicalRequest(r, nil, []string{"host"}, service)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(c, "\n")[1]
}

func TestAdvPathCanonicalisation(t *testing.T) {
	cases := []struct{ name, svc, url, want string }{
		{"get-vanilla root", "ec2", "http://h/", "/"},
		{"empty path -> /", "ec2", "http://h", "/"},
		{"trailing slash kept", "ec2", "http://h/a/", "/a/"},
		{"get-space (wire-encoded %20, double-encoded)", "ec2", "http://h/example%20space/", "/example%2520space/"},
		{"get-space raw space (Go encodes then double)", "ec2", "http://h/example space/", "/example%2520space/"},
		{"get-utf8 non-s3 double", "ec2", "http://h/%E1%88%B4", "/%25E1%2588%25B4"},
		{"s3 single encoded", "s3", "http://h/example%20space/", "/example%20space/"},
		{"s3 utf8 single", "s3", "http://h/%E1%88%B4", "/%E1%88%B4"},
		{"%2F in segment non-s3", "ec2", "http://h/a%2Fb", "/a%252Fb"},
		{"%2F in segment s3", "s3", "http://h/a%2Fb", "/a%2Fb"},
		{"double-encoded input non-s3", "ec2", "http://h/a%2520b", "/a%252520b"},
		{"tilde and unreserved", "ec2", "http://h/a-b_c.d~e", "/a-b_c.d~e"},
		{"reserved sub-delims", "ec2", "http://h/a:b@c", "/a%3Ab%40c"},
	}
	for _, c := range cases {
		if got := canonPath(t, c.svc, c.url); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// BUG: AWS says non-S3 services normalise the path (resolve "." / ".." and
// collapse "//") before double-encoding (AWS suite get-relative, get-relative-relative,
// get-slash, get-slashes, get-slash-dot-slash). sigv4.go signs the path verbatim.
// Judgement: Go SDKs (aws-sdk-go v1/v2) do not normalise either, so this only
// affects botocore-style clients; low severity.
func TestBugPathNotNormalisedForNonS3(t *testing.T) {
	cases := []struct{ name, url, want string }{
		{"get-relative", "http://h/example/..", "/"},
		{"get-relative-relative", "http://h/example1/example2/../..", "/"},
		{"get-slash-dot-slash", "http://h/./", "/"},
		{"get-slashes", "http://h/example//", "/example/"},
		{"get-slash", "http://h//", "/"},
	}
	for _, c := range cases {
		if got := canonPath(t, "ec2", c.url); got != c.want {
			t.Errorf("%s: got %q want %q (AWS normalises non-S3 paths)", c.name, got, c.want)
		}
	}
}

func TestAdvPathRoundTrips(t *testing.T) {
	for _, p := range []string{"/", "", "/a b/", "/ሴ", "/a%2Fb", "/a%2520b", "/../x", "//", "/.", "/a/", "/%00", "/a;b", "/a%3Fx"} {
		r := mkReq(t, "GET", "http://127.0.0.1:4566"+p, nil, nil)
		verifyOK(t, "path "+p, r, nil)
	}
}

// ---- 3. query ----
func canonQ(t *testing.T, raw string) string {
	t.Helper()
	r, _ := http.NewRequest("GET", "http://h/", nil)
	r.URL.RawQuery = raw
	q, err := canonicalQuery(r)
	if err != nil {
		t.Fatalf("%q: %v", raw, err)
	}
	return q
}

func TestAdvQueryCanonicalisation(t *testing.T) {
	cases := []struct{ name, raw, want string }{
		{"plus is space -> %20", "a=b+c", "a=b%20c"},
		{"%20", "a=b%20c", "a=b%20c"},
		{"literal plus %2B", "a=b%2Bc", "a=b%2Bc"},
		{"empty value", "a=", "a="},
		{"no equals", "a", "a="},
		{"empty query", "", ""},
		{"repeated keys sorted by value", "a=2&a=1&a=10", "a=1&a=10&a=2"},
		{"sorted by key", "b=1&a=2&B=3", "B=3&a=2&b=1"},
		{"lowercase hex escape normalised", "a=%e1%88%b4", "a=%E1%88%B4"},
		{"reserved chars encoded", "a=!*'()", "a=%21%2A%27%28%29"},
		{"slash in value encoded", "a=/x", "a=%2Fx"},
		{"tilde kept", "a=~", "a=~"},
		{"key encoded", "a b=1", "a%20b=1"},
		{"empty pair ignored", "a=1&&b=2", "a=1&b=2"},
		{"equals in value", "a=b=c", "a=b%3Dc"},
		{"key with empty value and others", "z&a=1", "a=1&z="},
	}
	for _, c := range cases {
		if got := canonQ(t, c.raw); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// Sorting must be by encoded key: "a b" -> a%20b sorts before "a-b" ('%' < '-').
func TestAdvQuerySortsByEncodedKey(t *testing.T) {
	if got, want := canonQ(t, "a-b=1&a+b=2"), "a%20b=2&a-b=1"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAdvQueryEquivalentFormsVerify(t *testing.T) {
	r := mkReq(t, "GET", "http://127.0.0.1:4566/?a=b%20c&x=1", nil, nil)
	r.URL.RawQuery = "x=1&a=b+c"
	verifyOK(t, "reordered + vs %20", r, nil)
	r.URL.RawQuery = "x=1&a=b%20c&"
	verifyOK(t, "trailing &", r, nil)
}

// ---- 4. headers ----
func TestAdvHeaderValueNormalisation(t *testing.T) {
	r := mkReq(t, "POST", "http://127.0.0.1:4566/", []byte("x"), map[string]string{"Content-Type": "  text/plain;   charset=utf-8  "})
	verifyOK(t, "signed with messy ws", r, []byte("x"))
	r.Header.Set("Content-Type", "text/plain; charset=utf-8")
	verifyOK(t, "normalised ws verifies same sig", r, []byte("x"))
	r.Header.Set("Content-Type", "text/plain;\t charset=utf-8")
	verifyOK(t, "tab collapses", r, []byte("x"))
	r.Header.Set("Content-Type", "text/plain;charset=utf-8")
	wantErr(t, "removed space is a real change", ErrSignatureMismatch, r, []byte("x"), advLookup, t0, time.Minute)
}

// BUG: strings.Fields collapses any Unicode whitespace (NBSP, U+2003, U+0085),
// not just ASCII space/tab. Two distinct header values then share a signature.
// Judgement: botocore also uses str.split(); low severity, header malleability only.
func TestBugHeaderUnicodeWhitespaceCollapsed(t *testing.T) {
	r := mkReq(t, "POST", "http://127.0.0.1:4566/", []byte("x"), map[string]string{"Content-Type": "a b"})
	r.Header.Set("Content-Type", "a\u00a0b")
	if _, err := Verify(r, []byte("x"), advLookup, t0, time.Minute); err == nil {
		t.Errorf("NBSP-separated header value verified against a signature over an ASCII-space value")
	}
}

func TestAdvMultiValueSignedHeader(t *testing.T) {
	r := mkReq(t, "POST", "http://127.0.0.1:4566/", []byte("x"), map[string]string{"Content-Type": "a,b"})
	r.Header["Content-Type"] = []string{"a", "b"}
	verifyOK(t, "two lines == comma-joined", r, []byte("x"))
	r.Header["Content-Type"] = []string{"a", "c"}
	wantErr(t, "different second value", ErrSignatureMismatch, r, []byte("x"), advLookup, t0, time.Minute)
}

func TestAdvHost(t *testing.T) {
	r := mkReq(t, "GET", "http://127.0.0.1:4566/", nil, nil)
	verifyOK(t, "host with port", r, nil)
	r.Host = "127.0.0.1"
	wantErr(t, "port dropped", ErrSignatureMismatch, r, nil, advLookup, t0, time.Minute)
	r.Host = "127.0.0.1:4566"
	verifyOK(t, "restored", r, nil)
	r.Host = "LOCALHOST:4566"
	wantErr(t, "different host", ErrSignatureMismatch, r, nil, advLookup, t0, time.Minute)

	// Host value is not lowercased (matches Go SDK); changing case must mismatch.
	r = mkReq(t, "GET", "http://Example.COM/", nil, nil)
	verifyOK(t, "mixed-case host roundtrip", r, nil)
	r.Host = "example.com"
	wantErr(t, "host case change", ErrSignatureMismatch, r, nil, advLookup, t0, time.Minute)

	// Empty r.Host falls back to URL.Host.
	r = mkReq(t, "GET", "http://svc.local:9/", nil, nil)
	r.Host = ""
	verifyOK(t, "empty Host falls back to URL.Host", r, nil)
}

// ---- 5. methods and bodies ----
func TestAdvMethodsAndBodies(t *testing.T) {
	for _, m := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
		body := []byte("payload-" + m)
		r := mkReq(t, m, "http://127.0.0.1:4566/p?q=1", body, nil)
		verifyOK(t, m, r, body)
		if m != "GET" {
			r.Method = "GET"
			wantErr(t, m+" -> GET", ErrSignatureMismatch, r, body, advLookup, t0, time.Minute)
		}
	}
	r := mkReq(t, "POST", "http://127.0.0.1:4566/", nil, nil)
	verifyOK(t, "nil body", r, nil)
	verifyOK(t, "empty body == nil body", r, []byte{})
	wantErr(t, "body injected", ErrSignatureMismatch, r, []byte("x"), advLookup, t0, time.Minute)
}

func TestAdvSingleByteTamper(t *testing.T) {
	body := bytes.Repeat([]byte("abcdefgh"), 8)
	r := mkReq(t, "POST", "http://127.0.0.1:4566/", body, nil)
	for i := range body {
		b := append([]byte(nil), body...)
		b[i] ^= 0x01
		if _, err := Verify(r, b, advLookup, t0, time.Minute); err != ErrSignatureMismatch {
			t.Fatalf("byte %d: err = %v", i, err)
		}
	}
	for _, b := range [][]byte{body[:len(body)-1], append(append([]byte(nil), body...), 0)} {
		if _, err := Verify(r, b, advLookup, t0, time.Minute); err != ErrSignatureMismatch {
			t.Fatalf("len %d: err = %v", len(b), err)
		}
	}
}

func TestAdvLargeBody(t *testing.T) {
	body := make([]byte, 1<<20)
	rand.New(rand.NewSource(1)).Read(body)
	r := mkReq(t, "PUT", "http://127.0.0.1:4566/big", body, nil)
	verifyOK(t, "1MiB", r, body)
	body[len(body)-1] ^= 1
	wantErr(t, "1MiB tamper last byte", ErrSignatureMismatch, r, body, advLookup, t0, time.Minute)
}

// ---- 6. X-Amz-Date shapes ----
func TestAdvAmzDateShapes(t *testing.T) {
	good := t0.Format(amzDateFormat)
	cases := map[string]string{
		"lowercase t":      strings.Replace(good, "T", "t", 1),
		"lowercase z":      strings.Replace(good, "Z", "z", 1),
		"non-UTC offset":   "20261009T140000+0200",
		"missing Z":        strings.TrimSuffix(good, "Z"),
		"date only":        "20261009",
		"RFC3339":          "2026-10-09T12:00:00Z",
		"empty":            "",
		"leading space":    " " + good,
		"trailing newline": good + "\n",
		"month 13":         "20261309T120000Z",
		"seconds 60":       "20261009T120060Z",
		"hour 24":          "20261009T240000Z",
		"huge":             strings.Repeat("9", 10000),
	}
	for n, d := range cases {
		r := mkReq(t, "GET", "http://127.0.0.1:4566/", nil, nil)
		r.Header.Set("X-Amz-Date", d)
		auth, err := Verify(r, nil, advLookup, t0, time.Hour)
		if err == nil {
			t.Errorf("%s: accepted %q (auth=%+v)", n, d, auth)
		}
	}
}

// BUG: time.Parse accepts a fractional-second suffix even though the layout has none,
// and the string-to-sign is rebuilt from the parsed time, so the timestamp in the
// string-to-sign differs from the raw header text. A request whose X-Amz-Date has
// fractional seconds (signed via forge) is accepted; AWS requires the strict basic
// ISO8601 form. Judgement: low; leniency only, no forgery without the secret.
func TestBugAmzDateFractionalSecondsAccepted(t *testing.T) {
	r := newPost("Action=X")
	frac := "20261009T120000.123Z"
	r.Header.Set("X-Amz-Date", frac)
	forge(r, []byte("Action=X"), Auth{AccessKey: "AK", Date: "20261009", Region: "us-west-2", Service: "ec2",
		SignedHeaders: []string{"host", "x-amz-date"}}, "SECRET", t0)
	if _, err := Verify(r, []byte("Action=X"), advLookup, t0, time.Hour); err == nil {
		t.Errorf("fractional-second X-Amz-Date %q accepted", frac)
	}
}

// ---- 7. Authorization variants ----
func TestAdvAuthorizationVariants(t *testing.T) {
	r := mkReq(t, "GET", "http://127.0.0.1:4566/", nil, nil)
	good := r.Header.Get("Authorization")
	sig := good[len(good)-64:]
	ok := map[string]string{
		"tab after algorithm":    strings.Replace(good, "SHA256 ", "SHA256\t", 1),
		"multiple spaces":        strings.Replace(good, "SHA256 ", "SHA256   ", 1),
		"no space after comma":   strings.ReplaceAll(good, ", ", ","),
		"leading/trailing space": "  " + good + "  ",
	}
	for n, h := range ok {
		r.Header.Set("Authorization", h)
		verifyOK(t, n, r, nil)
	}
	cred := strings.Split(good, ", ")[0][len("AWS4-HMAC-SHA256 "):]
	bad := map[string]string{
		"trailing comma":        good + ",",
		"uppercase signature":   good[:len(good)-64] + strings.ToUpper(sig),
		"unknown extra field":   good + ", Foo=bar",
		"extra scope segment":   strings.Replace(good, "/aws4_request", "/extra/aws4_request", 1),
		"wrong terminator":      strings.Replace(good, "/aws4_request", "/aws4_requesu", 1),
		"missing terminator":    strings.Replace(good, "/aws4_request", "", 1),
		"lowercase algorithm":   strings.Replace(good, "AWS4-HMAC-SHA256", "aws4-hmac-sha256", 1),
		"other algorithm":       strings.Replace(good, "AWS4-HMAC-SHA256", "AWS4-HMAC-SHA512", 1),
		"no space after algo":   strings.Replace(good, "SHA256 ", "SHA256", 1),
		"signed headers empty":  strings.Replace(good, "SignedHeaders=host;x-amz-date", "SignedHeaders=", 1),
		"signed headers ;;":     strings.Replace(good, "host;x-amz-date", "host;;x-amz-date", 1),
		"signed headers trail;": strings.Replace(good, "host;x-amz-date", "host;x-amz-date;", 1),
		"credential missing":    "AWS4-HMAC-SHA256 SignedHeaders=host;x-amz-date, Signature=" + sig,
		"fields reordered":      "AWS4-HMAC-SHA256 Signature=" + sig + ", " + cred + ", SignedHeaders=host;x-amz-date",
		"date 7 digits":         strings.Replace(good, "/20261009/", "/2026100/", 1),
		"date non-ascii digit":  strings.Replace(good, "/20261009/", "/２０２６１００９/", 1),
		"empty region":          strings.Replace(good, "/us-west-2/", "//", 1),
		"NUL byte":              good + "\x00",
		"just whitespace":       "   ",
	}
	for n, h := range bad {
		r.Header.Set("Authorization", h)
		auth, err := Verify(r, nil, advLookup, t0, time.Minute)
		if err == nil {
			t.Errorf("%s: accepted (auth=%+v)", n, auth)
		}
	}
	r.Header.Set("Authorization", strings.Repeat("A", 1<<16))
	wantErr(t, "65k header", ErrInvalidAuth, r, nil, advLookup, t0, time.Minute)
}

// ---- 8. property + determinism + no mutation ----
func randStr(rng *rand.Rand, alpha []rune, min, max int) string {
	n := min + rng.Intn(max-min+1)
	b := make([]rune, n)
	for i := range b {
		b[i] = alpha[rng.Intn(len(alpha))]
	}
	return string(b)
}

func TestAdvPropertySignThenVerify(t *testing.T) {
	rng := rand.New(rand.NewSource(20261009))
	methods := []string{"GET", "POST", "PUT", "DELETE"}
	idc := []rune("abcdefghijklmnopqrstuvwxyz0123456789-")
	akc := []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	secc := []rune("abcXYZ/+=0189!@#$ é")
	pc := []rune("abc /%._~-ሴ:@")
	kc := []rune("ab c+&=%/é")
	vc := []rune("xy z+&=%/é")
	for i := 0; i < 500; i++ {
		region := randStr(rng, idc, 1, 20)
		service := randStr(rng, idc, 1, 20)
		ak := randStr(rng, akc, 1, 30)
		secret := randStr(rng, secc, 1, 40)
		body := make([]byte, rng.Intn(2000))
		rng.Read(body)
		q := url.Values{}
		for j := 0; j < rng.Intn(4); j++ {
			q.Add(randStr(rng, kc, 1, 4), randStr(rng, vc, 0, 4))
		}
		at := t0.Add(time.Duration(rng.Intn(86400*365)) * time.Second)
		u := &url.URL{Scheme: "http", Host: "svc" + randStr(rng, []rune("ab"), 0, 3) + ":4566", Path: "/" + randStr(rng, pc, 0, 12), RawQuery: q.Encode()}
		r, _ := http.NewRequest(methods[rng.Intn(4)], u.String(), nil)
		if rng.Intn(2) == 0 {
			r.Header.Set("Content-Type", randStr(rng, []rune("abc/ ;="), 1, 10))
		}
		Sign(r, body, ak, secret, region, service, at)
		desc := fmt.Sprintf("i=%d region=%q service=%q url=%s", i, region, service, u)
		auth, err := Verify(r, body, lookupFor(ak, secret), at, time.Minute)
		if err != nil {
			t.Fatalf("%s: %v", desc, err)
		}
		if auth.Region != region || auth.Service != service || auth.AccessKey != ak {
			t.Fatalf("%s: auth=%+v", desc, auth)
		}
		if _, err := Verify(r, body, lookupFor(ak, secret+"x"), at, time.Minute); err != ErrSignatureMismatch {
			t.Fatalf("%s: wrong secret err=%v", desc, err)
		}
	}
}

func TestAdvVerifyDeterministicAndNonMutating(t *testing.T) {
	body := []byte("Action=X&Version=1")
	r := mkReq(t, "POST", "http://127.0.0.1:4566/a%20b?z=1&a=2", body, map[string]string{"Content-Type": "x/y"})
	r.Header.Add("X-Other", "1")
	hdrBefore := r.Header.Clone()
	hostBefore, methodBefore := r.Host, r.Method
	bodyCopy := append([]byte(nil), body...)
	urlBefore := *r.URL
	var first Auth
	for i := 0; i < 5; i++ {
		a, err := Verify(r, body, advLookup, t0, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = a
		} else if !reflect.DeepEqual(a, first) {
			t.Fatalf("nondeterministic: %+v vs %+v", a, first)
		}
	}
	if !reflect.DeepEqual(r.Header, hdrBefore) || r.Host != hostBefore || r.Method != methodBefore {
		t.Errorf("request headers/host/method mutated")
	}
	if *r.URL != urlBefore {
		t.Errorf("URL mutated: %+v vs %+v", *r.URL, urlBefore)
	}
	if !bytes.Equal(body, bodyCopy) {
		t.Errorf("body mutated")
	}
	r.Header.Set("X-Amz-Date", "bogus")
	h := r.Header.Clone()
	Verify(r, body, advLookup, t0, time.Minute)
	if !reflect.DeepEqual(r.Header, h) {
		t.Errorf("failed Verify mutated headers")
	}
}

func TestAdvVerifyDoesNotConsumeBody(t *testing.T) {
	body := []byte("hello")
	r := mkReq(t, "POST", "http://127.0.0.1:4566/", body, nil)
	if _, err := Verify(r, body, advLookup, t0, time.Minute); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.ReadFrom(r.Body)
	if buf.String() != "hello" {
		t.Errorf("r.Body consumed: %q", buf.String())
	}
}

// ---- 9. concurrency (run with -race) ----
func TestAdvConcurrentSignVerify(t *testing.T) {
	shared := mkReq(t, "POST", "http://127.0.0.1:4566/?a=1", []byte("b"), map[string]string{"Content-Type": "x"})
	var wg sync.WaitGroup
	errs := make(chan error, 64*20*2)
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				body := []byte(fmt.Sprintf("body-%d-%d", g, i))
				r, _ := http.NewRequest("POST", "http://127.0.0.1:4566/p", bytes.NewReader(body))
				at := t0.Add(time.Duration(g) * time.Second)
				Sign(r, body, "AK", "SECRET", "us-west-2", "ec2", at)
				if _, err := Verify(r, body, advLookup, at, time.Minute); err != nil {
					errs <- err
				}
				if _, err := Verify(shared, []byte("b"), advLookup, t0, time.Minute); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent: %v", e)
	}
}

// ---- 11. security properties ----
func TestAdvScopeEditsRejected(t *testing.T) {
	r := mkReq(t, "GET", "http://127.0.0.1:4566/", nil, nil)
	good := r.Header.Get("Authorization")
	edit := func(name, from, to string, want error) {
		r2 := r.Clone(r.Context())
		r2.Header.Set("Authorization", strings.Replace(good, from, to, 1))
		wantErr(t, name, want, r2, nil, advLookup, t0, time.Minute)
	}
	edit("service edited", "/ec2/", "/s3/", ErrSignatureMismatch)
	edit("service edited 2", "/ec2/", "/iam/", ErrSignatureMismatch)
	edit("region edited", "/us-west-2/", "/us-east-1/", ErrSignatureMismatch)
	edit("date edited (scope vs X-Amz-Date)", "/20261009/", "/20261008/", ErrInvalidAuth)
	edit("access key edited", "Credential=AK/", "Credential=AL/", ErrUnknownKey)

	r2 := r.Clone(r.Context())
	r2.Header.Set("X-Amz-Date", "20261008T120000Z")
	r2.Header.Set("Authorization", strings.Replace(good, "/20261009/", "/20261008/", 1))
	wantErr(t, "date edited in both places", ErrSignatureMismatch, r2, nil, advLookup, t0.Add(-24*time.Hour), time.Minute)
}

func TestAdvSignatureNotTransferable(t *testing.T) {
	base := mkReq(t, "POST", "http://127.0.0.1:4566/a/b?x=1&y=2", []byte("b"), nil)
	mut := map[string]func(r *http.Request){
		"method":         func(r *http.Request) { r.Method = "PUT" },
		"path":           func(r *http.Request) { r.URL.Path = "/a/c"; r.URL.RawPath = "" },
		"path case":      func(r *http.Request) { r.URL.Path = "/A/b"; r.URL.RawPath = "" },
		"query value":    func(r *http.Request) { r.URL.RawQuery = "x=1&y=3" },
		"query extra":    func(r *http.Request) { r.URL.RawQuery += "&z=1" },
		"query drop":     func(r *http.Request) { r.URL.RawQuery = "x=1" },
		"query empty":    func(r *http.Request) { r.URL.RawQuery = "" },
		"host":           func(r *http.Request) { r.Host = "evil.example:4566" },
		"trailing slash": func(r *http.Request) { r.URL.Path = "/a/b/" },
	}
	for n, f := range mut {
		r := base.Clone(base.Context())
		f(r)
		wantErr(t, n, ErrSignatureMismatch, r, []byte("b"), advLookup, t0, time.Minute)
	}
}

func TestAdvQueryKeyValueSwap(t *testing.T) {
	r := mkReq(t, "GET", "http://127.0.0.1:4566/?a=1&b=2", nil, nil)
	r.URL.RawQuery = "a=2&b=1"
	wantErr(t, "swap", ErrSignatureMismatch, r, nil, advLookup, t0, time.Minute)
	r.URL.RawQuery = "a=1%262&b="
	wantErr(t, "smuggle & in value", ErrSignatureMismatch, r, nil, advLookup, t0, time.Minute)
}

func TestAdvSkewBoundsAndLookupMisbehaviour(t *testing.T) {
	r := mkReq(t, "GET", "http://127.0.0.1:4566/", nil, nil)
	if _, err := Verify(r, nil, advLookup, t0, 0); err != nil {
		t.Errorf("zero skew, exact time: %v", err)
	}
	wantErr(t, "zero skew +1s", ErrRequestExpired, r, nil, advLookup, t0.Add(time.Second), 0)
	wantErr(t, "now far future", ErrRequestExpired, r, nil, advLookup, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), time.Minute)
	wantErr(t, "now zero time", ErrRequestExpired, r, nil, advLookup, time.Time{}, time.Minute)
	wantErr(t, "ok=false", ErrUnknownKey, r, nil, func(string) (string, bool) { return "SECRET", false }, t0, time.Minute)
}
