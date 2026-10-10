package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runCLI(env map[string]string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, func(k string) string { return env[k] }, &out, &errb)
	return code, out.String(), errb.String()
}

func noSecret(t *testing.T, what string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(p, secretValue) {
			t.Errorf("%s: secret leaked: %q", what, p)
		}
	}
}

func TestAdvExitCodesAndStreams(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "<ok/>")
	cases := []struct {
		name string
		env  map[string]string
		args []string
		code int
	}{
		{"ok", goodEnv, []string{"-endpoint", srv.URL, "-service", "ec2", "-action", "DescribeVpcs"}, 0},
		{"no args", goodEnv, nil, 2},
		{"unknown flag", goodEnv, []string{"-nope"}, 2},
		{"-secret flag absent", goodEnv, []string{"-secret", "x", "-service", "ec2"}, 2},
		{"-access-key flag absent", goodEnv, []string{"-access-key", "x", "-service", "ec2"}, 2},
		{"-h", goodEnv, []string{"-h"}, 2},
		{"missing secret", map[string]string{"AWS_ACCESS_KEY_ID": "AK"}, []string{"-service", "ec2", "-action", "X"}, 2},
		{"missing access", map[string]string{"AWS_SECRET_ACCESS_KEY": secretValue}, []string{"-service", "ec2", "-action", "X"}, 2},
		{"missing action", goodEnv, []string{"-endpoint", srv.URL, "-service", "ec2"}, 2},
		{"bad json version", goodEnv, []string{"-endpoint", srv.URL, "-service", "ssm", "-target", "T.X", "-json-version", "2"}, 2},
		{"bad body", goodEnv, []string{"-endpoint", srv.URL, "-service", "ssm", "-target", "T.X", "-body", "{"}, 2},
		{"bad param", goodEnv, []string{"-endpoint", srv.URL, "-service", "ec2", "-action", "X", "nokv"}, 2},
		{"bad endpoint", goodEnv, []string{"-endpoint", "gopher://x", "-service", "ec2", "-action", "X"}, 2},
		{"empty region flag", goodEnv, []string{"-endpoint", srv.URL, "-region", "", "-service", "ec2", "-action", "X"}, 2},
	}
	for _, c := range cases {
		code, out, errs := runCLI(c.env, c.args...)
		if code != c.code {
			t.Errorf("%s: code %d want %d (out=%q err=%q)", c.name, code, c.code, out, errs)
		}
		noSecret(t, c.name, out, errs)
		if c.code == 2 && out != "" {
			t.Errorf("%s: failures must not write stdout: %q", c.name, out)
		}
		if c.code == 0 && (!strings.HasPrefix(out, "HTTP 200\n") || errs != "") {
			t.Errorf("%s: out=%q err=%q", c.name, out, errs)
		}
	}
}

func TestAdvRegionFromEnv(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotAuth = r.Header.Get("Authorization") }))
	defer srv.Close()
	env := map[string]string{"AWS_ACCESS_KEY_ID": "AK", "AWS_SECRET_ACCESS_KEY": secretValue, "AWS_REGION": "eu-central-1"}
	if code, _, e := runCLI(env, "-endpoint", srv.URL, "-service", "ec2", "-action", "X"); code != 0 {
		t.Fatal(code, e)
	}
	if !strings.Contains(gotAuth, "/eu-central-1/ec2/") {
		t.Errorf("region from env not used: %q", gotAuth)
	}
	if strings.Contains(gotAuth, secretValue) {
		t.Error("secret in Authorization header")
	}
}

func TestAdvServerStatuses(t *testing.T) {
	for _, st := range []int{200, 204, 400, 403, 500, 503} {
		st := st
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(st)
			io.WriteString(w, "body-"+http.StatusText(st))
		}))
		code, out, errs := runCLI(goodEnv, "-endpoint", srv.URL, "-service", "ec2", "-action", "X")
		srv.Close()
		want := 0
		if st >= 400 {
			want = 1
		}
		if code != want {
			t.Errorf("status %d: code %d want %d out=%q err=%q", st, code, want, out, errs)
		}
		noSecret(t, "status", out, errs)
	}
}

// A redirect must not forward the signed request (and its Authorization header) to another host.
func TestAdvRedirectDoesNotLeakCredentialsToOtherHost(t *testing.T) {
	var leaked string
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
	}))
	defer evil.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(evil.URL, "127.0.0.1", "localhost", 1)+"/x", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	runCLI(goodEnv, "-endpoint", srv.URL, "-service", "ec2", "-action", "X")
	// BUG (if it fails): the signed Authorization header followed a 307 to a different origin.
	if leaked != "" {
		t.Errorf("Authorization header followed a cross-origin redirect: %q", leaked)
	}
}

func TestAdvHugeBodyIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("A"), 1<<20)
		for i := 0; i < 20; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	code, out, _ := runCLI(goodEnv, "-endpoint", srv.URL, "-service", "ec2", "-action", "X")
	if code != 0 || len(out) > maxResponse+64 {
		t.Fatalf("code=%d len=%d", code, len(out))
	}
}

func TestAdvConnectionRefusedAndGarbage(t *testing.T) {
	code, out, errs := runCLI(goodEnv, "-endpoint", "http://127.0.0.1:1", "-service", "ec2", "-action", "X")
	if code != 1 || out != "" {
		t.Errorf("refused: code=%d out=%q", code, out)
	}
	noSecret(t, "refused", out, errs)
}

func TestAdvParamsReachTheServerEncoded(t *testing.T) {
	var form string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form = string(b)
	}))
	defer srv.Close()
	runCLI(goodEnv, "-endpoint", srv.URL, "-service", "ec2", "-action", "X", "K=a=b&c d", "Empty=")
	if !strings.Contains(form, "K=a%3Db%26c+d") || !strings.Contains(form, "Empty=") {
		t.Errorf("form %q", form)
	}
}

func TestAdvSlowBodyTimeoutSkipped(t *testing.T) {
	t.Skip("client timeout is a hard-coded 30s inside run(); no injection point, and a 30s test is not worth it")
}

func FuzzCLIArgs(f *testing.F) {
	f.Add("-service ec2 -action X K=v")
	f.Add("-service ssm -target A.B -body {}")
	f.Add("-h")
	f.Add("-endpoint")
	f.Add("--service=ec2 --")
	f.Fuzz(func(t *testing.T, line string) {
		args := strings.Fields(line)
		for i, a := range args { // never contact a real host: force the endpoint to a closed local port
			if strings.HasPrefix(strings.TrimLeft(a, "-"), "endpoint") {
				args[i] = "-endpoint=http://127.0.0.1:1"
			}
		}
		args = append([]string{"-endpoint", "http://127.0.0.1:1"}, args...)
		var out, errb bytes.Buffer
		code := run(args, func(k string) string { return goodEnv[k] }, &out, &errb)
		if code < 0 || code > 2 {
			t.Fatalf("unexpected exit code %d", code)
		}
		if strings.Contains(out.String()+errb.String(), secretValue) {
			t.Fatal("secret leaked")
		}
	})
}
