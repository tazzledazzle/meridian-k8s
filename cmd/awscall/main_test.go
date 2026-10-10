package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

const secretValue = "TOPSECRET-VALUE-123"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var goodEnv = map[string]string{"AWS_ACCESS_KEY_ID": "AK", "AWS_SECRET_ACCESS_KEY": secretValue}

// fakeGateway verifies the signature like the real gateway and answers with status.
func fakeGateway(t *testing.T, status int, reply string) (*httptest.Server, *string) {
	t.Helper()
	var verifiedService string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lookup := func(k string) (string, bool) { return secretValue, k == "AK" }
		auth, err := awsapi.Verify(r, body, lookup, time.Now(), time.Minute)
		if err != nil {
			w.WriteHeader(403)
			return
		}
		verifiedService = auth.Service
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &verifiedService
}

func TestRunSendsASignedQueryRequestAndPrintsTheResponse(t *testing.T) {
	srv, svc := fakeGateway(t, 200, "<ok/>")
	var out, errb bytes.Buffer
	code := run([]string{"-endpoint", srv.URL, "-service", "ec2", "-action", "DescribeInstances", "Foo=bar"}, env(goodEnv), &out, &errb)
	if code != 0 || *svc != "ec2" || !strings.Contains(out.String(), "HTTP 200") || !strings.Contains(out.String(), "<ok/>") {
		t.Fatalf("code=%d svc=%s out=%q err=%q", code, *svc, out.String(), errb.String())
	}
}

func TestRunSendsASignedJSONRequest(t *testing.T) {
	srv, svc := fakeGateway(t, 200, "{}")
	var out, errb bytes.Buffer
	code := run([]string{"-endpoint", srv.URL, "-service", "ssm", "-target", "AmazonSSM.PutParameter", "-body", `{"Name":"/a"}`}, env(goodEnv), &out, &errb)
	if code != 0 || *svc != "ssm" {
		t.Fatalf("code=%d svc=%s out=%q err=%q", code, *svc, out.String(), errb.String())
	}
}

func TestRunExitsNonZeroOnHTTPError(t *testing.T) {
	srv, _ := fakeGateway(t, 400, "bad")
	var out, errb bytes.Buffer
	if code := run([]string{"-endpoint", srv.URL, "-service", "ec2", "-action", "X"}, env(goodEnv), &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRunRefusesWithoutCredentialsAndNeverPrintsTheSecret(t *testing.T) {
	for name, e := range map[string]map[string]string{
		"none":       {},
		"no secret":  {"AWS_ACCESS_KEY_ID": "AK"},
		"no key id":  {"AWS_SECRET_ACCESS_KEY": secretValue},
		"empty both": {"AWS_ACCESS_KEY_ID": "", "AWS_SECRET_ACCESS_KEY": ""},
	} {
		var out, errb bytes.Buffer
		code := run([]string{"-service", "ec2", "-action", "X"}, env(e), &out, &errb)
		if code == 0 || strings.Contains(out.String()+errb.String(), secretValue) {
			t.Fatalf("%s: code=%d out=%q err=%q", name, code, out.String(), errb.String())
		}
	}
}

func TestRunHasNoSecretFlag(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"-service", "ec2", "-action", "X", "-secret", secretValue}, env(goodEnv), &out, &errb)
	if code == 0 {
		t.Fatal("a -secret flag must not exist")
	}
	if strings.Contains(out.String()+errb.String(), secretValue) {
		t.Fatalf("the secret leaked: out=%q err=%q", out.String(), errb.String())
	}
}

func TestRunRefusesUnknownServiceWithAClearError(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"-service", "s3", "-action", "X"}, env(goodEnv), &out, &errb)
	if code == 0 || !strings.Contains(errb.String(), "ec2") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRunRefusesMissingService(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"-action", "X"}, env(goodEnv), &out, &errb); code == 0 {
		t.Fatal("expected failure without -service")
	}
}

func TestRunBoundsTheResponseItPrints(t *testing.T) {
	srv, _ := fakeGateway(t, 200, strings.Repeat("A", maxResponse*2))
	var out, errb bytes.Buffer
	if code := run([]string{"-endpoint", srv.URL, "-service", "ec2", "-action", "X"}, env(goodEnv), &out, &errb); code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if out.Len() > maxResponse+200 {
		t.Fatalf("printed %d bytes", out.Len())
	}
}

func TestRunReportsConnectionErrorsWithoutTheSecret(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"-endpoint", "http://127.0.0.1:1", "-service", "ec2", "-action", "X"}, env(goodEnv), &out, &errb)
	if code == 0 || strings.Contains(out.String()+errb.String(), secretValue) {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
}
