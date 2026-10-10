package awsapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// lookupFor returns a SecretLookup that knows one key pair.
func lookupFor(access, secret string) SecretLookup {
	return func(k string) (string, bool) { return secret, k == access }
}

// awsExample builds the worked example from AWS's "Signature Version 4 signing
// process" documentation (GET iam ListUsers), an oracle independent of Sign.
func awsExample() *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	r.Header.Set("X-Amz-Date", "20150830T123600Z")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, "+
		"SignedHeaders=content-type;host;x-amz-date, "+
		"Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7")
	return r
}

const awsExampleSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"

var awsExampleNow = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

func TestVerifyAcceptsAWSPublishedExample(t *testing.T) {
	auth, err := Verify(awsExample(), nil, lookupFor("AKIDEXAMPLE", awsExampleSecret), awsExampleNow, 15*time.Minute)
	if err != nil {
		t.Fatalf("AWS example rejected: %v", err)
	}
	if auth.Service != "iam" || auth.Region != "us-east-1" || auth.AccessKey != "AKIDEXAMPLE" {
		t.Fatalf("auth = %+v", auth)
	}
}

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// signed builds a POST signed with Sign, the shape every SDK sends to a query-protocol service.
func signed(t *testing.T, body string, at time.Time) (*http.Request, []byte) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	Sign(r, []byte(body), "AK", "SECRET", "us-west-2", "ec2", at)
	return r, []byte(body)
}

func TestVerifyAcceptsRequestSignedBySign(t *testing.T) {
	r, body := signed(t, "Action=DescribeInstances&Version=2016-11-15", t0)
	auth, err := Verify(r, body, lookupFor("AK", "SECRET"), t0, 15*time.Minute)
	if err != nil || auth.Service != "ec2" || auth.Region != "us-west-2" {
		t.Fatalf("auth=%+v err=%v", auth, err)
	}
}

// wantErr asserts Verify fails with exactly the sentinel and returns a zero Auth.
func wantErr(t *testing.T, name string, want error, r *http.Request, body []byte, lookup SecretLookup, now time.Time, skew time.Duration) {
	t.Helper()
	auth, err := Verify(r, body, lookup, now, skew)
	if !errors.Is(err, want) {
		t.Errorf("%s: err = %v, want %v", name, err, want)
	}
	if err != nil && auth.AccessKey != "" {
		t.Errorf("%s: non-zero Auth returned with error: %+v", name, auth)
	}
}

func TestVerifyRejectsTamperedBody(t *testing.T) {
	r, _ := signed(t, "Action=DescribeInstances", t0)
	wantErr(t, "tampered body", ErrSignatureMismatch, r, []byte("Action=TerminateInstances"), lookupFor("AK", "SECRET"), t0, time.Hour)
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	r, body := signed(t, "Action=DescribeInstances", t0)
	wantErr(t, "wrong secret", ErrSignatureMismatch, r, body, lookupFor("AK", "WRONG"), t0, time.Hour)
}

func TestVerifyRejectsUnknownKey(t *testing.T) {
	r, body := signed(t, "Action=DescribeInstances", t0)
	wantErr(t, "unknown key", ErrUnknownKey, r, body, lookupFor("OTHER", "SECRET"), t0, time.Hour)
}

func TestVerifyRejectsMissingAuthorization(t *testing.T) {
	r, body := signed(t, "Action=DescribeInstances", t0)
	r.Header.Del("Authorization")
	wantErr(t, "missing header", ErrMissingAuth, r, body, lookupFor("AK", "SECRET"), t0, time.Hour)
}

func TestVerifyRejectsMalformedAuthorization(t *testing.T) {
	r, body := signed(t, "Action=DescribeInstances", t0)
	r.Header.Set("Authorization", "Bearer abc")
	wantErr(t, "malformed header", ErrInvalidAuth, r, body, lookupFor("AK", "SECRET"), t0, time.Hour)
}

func TestVerifyRejectsStaleAndFutureTimestamps(t *testing.T) {
	r, body := signed(t, "Action=DescribeInstances", t0)
	good := lookupFor("AK", "SECRET")
	wantErr(t, "stale", ErrRequestExpired, r, body, good, t0.Add(2*time.Hour), 15*time.Minute)
	wantErr(t, "future", ErrRequestExpired, r, body, good, t0.Add(-2*time.Hour), 15*time.Minute)
	// The window edge is inclusive; one second beyond is rejected.
	if _, err := Verify(r, body, good, t0.Add(15*time.Minute), 15*time.Minute); err != nil {
		t.Errorf("edge of window rejected: %v", err)
	}
	wantErr(t, "just past window", ErrRequestExpired, r, body, good, t0.Add(15*time.Minute+time.Second), 15*time.Minute)
}

// forge writes an Authorization header with a chosen scope and signed-header
// list, signed with the real secret, to probe Verify's structural checks.
func forge(r *http.Request, body []byte, a Auth, secret string, at time.Time) {
	a.Signature, _ = signature(r, body, a, secret, at)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+a.AccessKey+"/"+a.Date+"/"+a.Region+"/"+a.Service+
		"/aws4_request, SignedHeaders="+strings.Join(a.SignedHeaders, ";")+", Signature="+a.Signature)
}

func newPost(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestVerifyRejectsCredentialDateNotMatchingAmzDate(t *testing.T) {
	r := newPost("Action=X")
	r.Header.Set("X-Amz-Date", t0.Format(amzDateFormat))
	// The scope claims yesterday although X-Amz-Date is today.
	forge(r, []byte("Action=X"), Auth{AccessKey: "AK", Date: "20261008", Region: "us-west-2", Service: "ec2",
		SignedHeaders: []string{"host", "x-amz-date"}}, "SECRET", t0)
	wantErr(t, "scope date", ErrInvalidAuth, r, []byte("Action=X"), lookupFor("AK", "SECRET"), t0, time.Hour)
}

func TestVerifyFailsClosedOnBadConfiguration(t *testing.T) {
	r, body := signed(t, "Action=DescribeInstances", t0)
	wantErr(t, "nil lookup", ErrUnknownKey, r, body, nil, t0, time.Hour)
	wantErr(t, "empty secret", ErrUnknownKey, r, body, lookupFor("AK", ""), t0, time.Hour)
	wantErr(t, "negative skew", ErrRequestExpired, r, body, lookupFor("AK", "SECRET"), t0, -time.Hour)
}

func TestVerifyRejectsEmptySecretEvenWhenSignedWithIt(t *testing.T) {
	r := newPost("Action=X")
	Sign(r, []byte("Action=X"), "AK", "", "us-west-2", "ec2", t0)
	wantErr(t, "signed with empty secret", ErrUnknownKey, r, []byte("Action=X"), lookupFor("AK", ""), t0, time.Hour)
}

func TestVerifyBindsBodyEvenWhenContentSha256HeaderPresent(t *testing.T) {
	orig := "Action=DescribeInstances"
	r := newPost(orig)
	r.Header.Set("X-Amz-Content-Sha256", hexSHA256([]byte(orig)))
	Sign(r, []byte(orig), "AK", "SECRET", "us-west-2", "ec2", t0)
	// Honest body verifies.
	if _, err := Verify(r, []byte(orig), lookupFor("AK", "SECRET"), t0, time.Hour); err != nil {
		t.Fatalf("honest request rejected: %v", err)
	}
	// A different body must not ride on the signed digest header.
	wantErr(t, "swapped body", ErrSignatureMismatch, r, []byte("Action=TerminateInstances"), lookupFor("AK", "SECRET"), t0, time.Hour)
}

func TestVerifyEnforcesSignedHeaderRules(t *testing.T) {
	good := lookupFor("AK", "SECRET")
	base := Auth{AccessKey: "AK", Date: t0.Format("20060102"), Region: "us-west-2", Service: "ec2"}
	cases := []struct {
		name    string
		headers []string
	}{
		{"host not signed", []string{"x-amz-date"}},
		{"x-amz-date not signed", []string{"host"}},
		{"unsorted", []string{"x-amz-date", "host"}},
		{"duplicate", []string{"host", "host", "x-amz-date"}},
		{"uppercase", []string{"Host", "x-amz-date"}},
		{"signed header absent from request", []string{"host", "x-amz-date", "x-amz-nope"}},
		{"too many", append(append([]string{"host"}, manyHeaders()...), "x-amz-date")},
	}
	for _, tc := range cases {
		r := newPost("Action=X")
		r.Header.Set("X-Amz-Date", t0.Format(amzDateFormat))
		for _, h := range manyHeaders() {
			r.Header.Set(h, "v")
		}
		a := base
		a.SignedHeaders = tc.headers
		forge(r, []byte("Action=X"), a, "SECRET", t0)
		wantErr(t, tc.name, ErrInvalidAuth, r, []byte("Action=X"), good, t0, time.Hour)
	}
}

// manyHeaders returns 70 sorted header names between "host" and "x-amz-date".
func manyHeaders() []string {
	var hs []string
	for i := 0; i < 70; i++ {
		hs = append(hs, "x-a"+string(rune('a'+i/26))+string(rune('a'+i%26)))
	}
	return hs
}

// TestVerifyAcceptsAWSSuiteVectors checks more vectors from AWS's published
// sigv4 test suite (service "service", host example.amazonaws.com).
func TestVerifyAcceptsAWSSuiteVectors(t *testing.T) {
	cases := []struct{ name, uri, sig string }{
		{"get-vanilla", "/", "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"get-vanilla-query", "/?Param1=value1&Param2=value2", "b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500"},
		{"get-vanilla-query-order", "/?Param2=value2&Param1=value1", "b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500"},
	}
	for _, tc := range cases {
		r, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com"+tc.uri, nil)
		r.Header.Set("X-Amz-Date", "20150830T123600Z")
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature="+tc.sig)
		if _, err := Verify(r, nil, lookupFor("AKIDEXAMPLE", awsExampleSecret), awsExampleNow, time.Minute); err != nil {
			t.Errorf("%s rejected: %v", tc.name, err)
		}
	}
}

func TestVerifyRejectsDuplicatedSecurityHeaders(t *testing.T) {
	r, body := signed(t, "Action=DescribeInstances", t0)
	r.Header.Add("Authorization", r.Header.Get("Authorization"))
	wantErr(t, "two Authorization headers", ErrInvalidAuth, r, body, lookupFor("AK", "SECRET"), t0, time.Hour)

	r, body = signed(t, "Action=DescribeInstances", t0)
	r.Header.Add("X-Amz-Date", r.Header.Get("X-Amz-Date"))
	wantErr(t, "two X-Amz-Date headers", ErrInvalidAuth, r, body, lookupFor("AK", "SECRET"), t0, time.Hour)
}

func TestVerifyRejectsOversizeOrOddCredentialFields(t *testing.T) {
	sig := strings.Repeat("a", 64)
	mk := func(cred string) string {
		return "AWS4-HMAC-SHA256 Credential=" + cred + "/20261009/us-west-2/ec2/aws4_request, SignedHeaders=host;x-amz-date, Signature=" + sig
	}
	cases := map[string]string{
		"huge access key":     mk(strings.Repeat("A", 5000)),
		"space in access key": mk("A K"),
		"control in key":      mk("AK\x01"),
	}
	for name, h := range cases {
		r, body := signed(t, "Action=X", t0)
		r.Header.Set("Authorization", h)
		wantErr(t, name, ErrInvalidAuth, r, body, lookupFor("AK", "SECRET"), t0, time.Hour)
	}
	// Odd region and service names are rejected the same way.
	for _, bad := range []string{"EC2", "ec 2", "ec2!"} {
		r, body := signed(t, "Action=X", t0)
		r.Header.Set("Authorization", strings.Replace(mk("AK"), "/ec2/", "/"+bad+"/", 1))
		wantErr(t, "service "+bad, ErrInvalidAuth, r, body, lookupFor("AK", "SECRET"), t0, time.Hour)
	}
}

func TestVerifyRejectsQueryPairsGoSilentlyDrops(t *testing.T) {
	// Go's url.Query() drops pairs containing ';', so a smuggled pair would
	// be invisible to the signature but visible to other parsers.
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566/?Action=X", nil)
	Sign(r, nil, "AK", "SECRET", "us-west-2", "ec2", t0)
	r.URL.RawQuery = "Action=X&evil=1;2"
	wantErr(t, "semicolon pair", ErrInvalidAuth, r, nil, lookupFor("AK", "SECRET"), t0, time.Hour)

	r = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566/?Action=X", nil)
	Sign(r, nil, "AK", "SECRET", "us-west-2", "ec2", t0)
	r.URL.RawQuery = "Action=X&bad=%zz"
	wantErr(t, "bad escape", ErrInvalidAuth, r, nil, lookupFor("AK", "SECRET"), t0, time.Hour)
}

func TestVerifyRejectsMissingOrBadAmzDate(t *testing.T) {
	r, body := signed(t, "Action=DescribeInstances", t0)
	r.Header.Del("X-Amz-Date")
	wantErr(t, "no date", ErrInvalidAuth, r, body, lookupFor("AK", "SECRET"), t0, time.Hour)

	r, body = signed(t, "Action=DescribeInstances", t0)
	r.Header.Set("X-Amz-Date", "garbage")
	wantErr(t, "bad date", ErrInvalidAuth, r, body, lookupFor("AK", "SECRET"), t0, time.Hour)
}
