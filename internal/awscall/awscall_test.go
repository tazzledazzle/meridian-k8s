package awscall

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

var now = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func lookup(k string) (string, bool) { return "SECRET", k == "AK" }

func TestBuildQuerySignsAVerifiableRequest(t *testing.T) {
	r, body, err := BuildQuery("http://127.0.0.1:4566", "ec2", "us-west-2", "CreateVpc", "2016-11-15",
		[]string{"CidrBlock=10.0.0.0/16"}, "AK", "SECRET", now)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := awsapi.Verify(r, body, lookup, now, time.Minute)
	if err != nil || auth.Service != "ec2" {
		t.Fatalf("auth=%+v err=%v", auth, err)
	}
	form, _ := awsapi.Form(r, body)
	if form.Get("Action") != "CreateVpc" || form.Get("Version") != "2016-11-15" || form.Get("CidrBlock") != "10.0.0.0/16" {
		t.Fatalf("form=%v", form)
	}
	if r.Method != http.MethodPost {
		t.Fatalf("method = %s", r.Method)
	}
}

func TestBuildQueryRejectsMalformedParam(t *testing.T) {
	if _, _, err := BuildQuery("http://x", "ec2", "r", "A", "v", []string{"novalue"}, "AK", "S", now); err == nil {
		t.Fatal("expected an error for a param without '='")
	}
}

// A positional param must not be able to override the action the caller asked for.
func TestBuildQueryRefusesParamsThatOverrideActionOrVersion(t *testing.T) {
	for _, p := range []string{"Action=TerminateInstances", "Version=1999-01-01"} {
		if _, _, err := BuildQuery("http://x", "ec2", "r", "DescribeInstances", "2016-11-15", []string{p}, "AK", "S", now); err == nil {
			t.Fatalf("param %q should be refused", p)
		}
	}
}

func TestBuildQueryEncodesSpecialCharactersInValues(t *testing.T) {
	r, body, err := BuildQuery("http://x:1", "ec2", "r", "A", "v", []string{"Name=a&b=c d"}, "AK", "SECRET", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := awsapi.Verify(r, body, lookup, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	form, _ := awsapi.Form(r, body)
	if form.Get("Name") != "a&b=c d" || form.Get("b") != "" {
		t.Fatalf("form=%v", form)
	}
}

func TestBuildJSONSetsTargetAndContentType(t *testing.T) {
	r, body, err := BuildJSON("http://127.0.0.1:4566", "ssm", "us-west-2", "AmazonSSM.PutParameter", "1.1",
		`{"Name":"/a","Value":"b","Type":"String"}`, "AK", "SECRET", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := awsapi.Verify(r, body, lookup, now, time.Minute); err != nil {
		t.Fatalf("verify: %v", err)
	}
	b, _ := io.ReadAll(r.Body)
	if r.Header.Get("X-Amz-Target") != "AmazonSSM.PutParameter" ||
		r.Header.Get("Content-Type") != "application/x-amz-json-1.1" ||
		!strings.Contains(string(b), `"Name":"/a"`) {
		t.Fatalf("headers=%v body=%s", r.Header, b)
	}
}

// The target is set before signing, so the signature covers it; swapping it afterwards must fail.
func TestBuildJSONSignsTheTargetSoSwappingItFailsVerify(t *testing.T) {
	r, body, err := BuildJSON("http://127.0.0.1:4566", "ssm", "us-west-2", "AmazonSSM.GetParameter", "1.1", `{}`, "AK", "SECRET", now)
	if err != nil {
		t.Fatal(err)
	}
	if signed := r.Header.Get("Authorization"); !strings.Contains(signed, "x-amz-target") {
		t.Fatalf("x-amz-target is not in SignedHeaders: %s", signed)
	}
	r.Header.Set("X-Amz-Target", "AmazonSSM.DeleteParameter")
	if _, err := awsapi.Verify(r, body, lookup, now, time.Minute); err == nil {
		t.Fatal("swapped target must fail verification")
	}
}

// Verify hashes the real body, so a body changed after signing must fail.
func TestBuildJSONBodyTamperingFailsVerify(t *testing.T) {
	r, _, err := BuildJSON("http://127.0.0.1:4566", "ssm", "us-west-2", "AmazonSSM.PutParameter", "1.1", `{"Name":"/a"}`, "AK", "SECRET", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := awsapi.Verify(r, []byte(`{"Name":"/evil"}`), lookup, now, time.Minute); err == nil {
		t.Fatal("tampered body must fail verification")
	}
}

func TestBuildRESTSignsAGetAndAPost(t *testing.T) {
	get, body, err := BuildREST("http://127.0.0.1:4566", "eks", "us-west-2", http.MethodGet, "/clusters/meridian-g0", "", "AK", "SECRET", now)
	if err != nil {
		t.Fatal(err)
	}
	if auth, err := awsapi.Verify(get, body, lookup, now, time.Minute); err != nil || auth.Service != "eks" {
		t.Fatalf("GET: auth=%+v err=%v", auth, err)
	}
	post, body, err := BuildREST("http://127.0.0.1:4566", "eks", "us-west-2", http.MethodPost, "/clusters", `{"name":"x"}`, "AK", "SECRET", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := awsapi.Verify(post, body, lookup, now, time.Minute); err != nil {
		t.Fatalf("POST: %v", err)
	}
	if post.Header.Get("Content-Type") != "application/json" || get.Header.Get("Content-Type") != "" {
		t.Fatalf("content types: %q / %q", post.Header.Get("Content-Type"), get.Header.Get("Content-Type"))
	}
}

func TestBuildersRefuseUnknownService(t *testing.T) {
	_, _, err1 := BuildQuery("http://x", "s3", "r", "A", "v", nil, "AK", "S", now)
	_, _, err2 := BuildJSON("http://x", "dynamodb", "r", "T.Op", "1.1", "{}", "AK", "S", now)
	_, _, err3 := BuildREST("http://x", "lambda", "r", http.MethodGet, "/x", "", "AK", "S", now)
	for i, err := range []error{err1, err2, err3} {
		if !errors.Is(err, ErrUnknownService) || !strings.Contains(err.Error(), "ec2") {
			t.Fatalf("builder %d: err = %v, want ErrUnknownService listing the allowed services", i, err)
		}
	}
}

func TestBuildersRefuseBadInputs(t *testing.T) {
	cases := map[string]func() error{
		"empty access": func() error { _, _, e := BuildQuery("http://x", "ec2", "r", "A", "v", nil, "", "S", now); return e },
		"empty secret": func() error { _, _, e := BuildQuery("http://x", "ec2", "r", "A", "v", nil, "AK", "", now); return e },
		"empty action": func() error { _, _, e := BuildQuery("http://x", "ec2", "r", "", "v", nil, "AK", "S", now); return e },
		"empty region": func() error { _, _, e := BuildQuery("http://x", "ec2", "", "A", "v", nil, "AK", "S", now); return e },
		"file endpoint": func() error {
			_, _, e := BuildQuery("file:///etc", "ec2", "r", "A", "v", nil, "AK", "S", now)
			return e
		},
		"no host": func() error { _, _, e := BuildQuery("http://", "ec2", "r", "A", "v", nil, "AK", "S", now); return e },
		"endpoint creds": func() error {
			_, _, e := BuildQuery("http://u:p@x", "ec2", "r", "A", "v", nil, "AK", "S", now)
			return e
		},
		"empty target": func() error { _, _, e := BuildJSON("http://x", "ssm", "r", "", "1.1", "{}", "AK", "S", now); return e },
		"bad json version": func() error {
			_, _, e := BuildJSON("http://x", "ssm", "r", "A.B", "2.0", "{}", "AK", "S", now)
			return e
		},
		"invalid json body": func() error {
			_, _, e := BuildJSON("http://x", "ssm", "r", "A.B", "1.1", "{nope", "AK", "S", now)
			return e
		},
		"rest path no slash": func() error {
			_, _, e := BuildREST("http://x", "eks", "r", "GET", "clusters", "", "AK", "S", now)
			return e
		},
		"rest path with host": func() error {
			_, _, e := BuildREST("http://x", "eks", "r", "GET", "//evil.test/x", "", "AK", "S", now)
			return e
		},
	}
	for name, f := range cases {
		if err := f(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// Errors must never echo the secret.
func TestErrorsDoNotContainTheSecret(t *testing.T) {
	_, _, err := BuildQuery("file:///x", "ec2", "r", "A", "v", nil, "AK", "TOPSECRETVALUE", now)
	if err == nil || strings.Contains(err.Error(), "TOPSECRETVALUE") {
		t.Fatalf("err = %v", err)
	}
}
