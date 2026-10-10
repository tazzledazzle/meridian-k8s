package main

// Adversarial tests for the s0probe command: exit codes, flag misuse, output files and secrets.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	advAccess = "AKIAADVERSARIALKEY"
	advSecret = "adv/Secret+Value0123456789abcdefghijkl"
)

func advEnv() func(string) string {
	return env(map[string]string{"AWS_ACCESS_KEY_ID": advAccess, "AWS_SECRET_ACCESS_KEY": advSecret})
}

// stsOK answers STS correctly and anything else with an echo of the secret and the Authorization header.
func stsOK(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "/sts/aws4_request") {
			_, _ = w.Write([]byte(`<R><GetCallerIdentityResult><Account>1</Account></GetCallerIdentityResult></R>`))
			return
		}
		http.Error(w, "echo "+advSecret+" "+advAccess+" "+r.Header.Get("Authorization"), 400)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAdvExitCodes(t *testing.T) {
	srv := stsOK(t)
	cases := []struct {
		name string
		args []string
		env  func(string) string
		code int
	}{
		{"sts only passes", []string{"-endpoint", srv.URL, "-groups", "sts"}, advEnv(), 0},
		{"iam fails against the echo backend", []string{"-endpoint", srv.URL, "-groups", "iam", "-wait", "10ms"}, advEnv(), 1},
		{"skip counts as not passing (eks without -eks-create)", []string{"-endpoint", srv.URL, "-groups", "eks", "-wait", "10ms"}, advEnv(), 1},
		{"unknown group", []string{"-endpoint", srv.URL, "-groups", "nope"}, advEnv(), 2},
		{"unknown group among good ones", []string{"-endpoint", srv.URL, "-groups", "sts,nope"}, advEnv(), 2},
		{"empty element in groups", []string{"-endpoint", srv.URL, "-groups", "sts,"}, advEnv(), 2},
		{"group names are case sensitive", []string{"-endpoint", srv.URL, "-groups", "STS"}, advEnv(), 2},
		{"group with a space", []string{"-endpoint", srv.URL, "-groups", "sts, iam"}, advEnv(), 2},
		{"missing access key", []string{"-endpoint", srv.URL}, env(map[string]string{"AWS_ACCESS_KEY_ID": ""}), 2},
		{"missing secret", []string{"-endpoint", srv.URL}, env(map[string]string{"AWS_SECRET_ACCESS_KEY": ""}), 2},
		{"unknown flag", []string{"-bogus"}, advEnv(), 2},
		{"bad duration", []string{"-wait", "soon"}, advEnv(), 2},
		{"help", []string{"-h"}, advEnv(), 2},
		{"endpoint without scheme", []string{"-endpoint", "localhost:4566"}, advEnv(), 2},
		{"endpoint ftp", []string{"-endpoint", "ftp://x"}, advEnv(), 2},
		{"endpoint empty", []string{"-endpoint", ""}, advEnv(), 2},
		{"endpoint without host", []string{"-endpoint", "http://"}, advEnv(), 2},
		{"timeout zero", []string{"-endpoint", srv.URL, "-timeout", "0"}, advEnv(), 2},
		{"timeout negative", []string{"-endpoint", srv.URL, "-timeout", "-1s"}, advEnv(), 2},
		{"wait negative", []string{"-endpoint", srv.URL, "-wait", "-1s"}, advEnv(), 2},
		{"empty cluster", []string{"-endpoint", srv.URL, "-cluster", ""}, advEnv(), 2},
		{"empty region", []string{"-endpoint", srv.URL, "-region", ""}, advEnv(), 2},
		{"prices with an empty element", []string{"-endpoint", srv.URL, "-prices", "m5.large,,c5.large"}, advEnv(), 2},
		{"prices whitespace only", []string{"-endpoint", srv.URL, "-prices", " "}, advEnv(), 2},
		{"prices against a backend that has none", []string{"-endpoint", srv.URL, "-prices", "m5.large"}, advEnv(), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errs := do(t, c.env, c.args...)
			if code != c.code {
				t.Errorf("exit %d, want %d\nstdout: %.300s\nstderr: %.300s", code, c.code, out, errs)
			}
			for _, s := range []string{advSecret, advAccess} {
				if strings.Contains(out, s) || strings.Contains(errs, s) {
					t.Errorf("output contains %q:\n%s\n%s", s, out, errs)
				}
			}
		})
	}
}

// BUG candidate: arguments after the flags are ignored, so `-groups sts iam` (a space instead of a comma) silently
// runs only sts and exits 0.
func TestBugPositionalArgumentsAreSilentlyIgnored(t *testing.T) {
	srv := stsOK(t)
	code, out, errs := do(t, advEnv(), "-endpoint", srv.URL, "-groups", "sts", "iam")
	// BUG: main never looks at fs.Args(); the operator asked for two groups, got one, and sees exit status 0.
	if code != 2 {
		t.Errorf("exit %d (stdout has %d lines), want 2 for a stray positional argument; stderr=%q", code, strings.Count(out, "\n"), errs)
	}
}

func TestAdvNameValidation(t *testing.T) {
	srv := stsOK(t)
	bad := []string{"", "../x", "..", "a/b", `a\b`, "/etc/passwd", ".hidden", "-x", "a b", "a\nb", "a..b", "x\x00y", "a:b", "report.md/../../x", "é", "a*", "$(id)", "a;b"}
	for _, n := range bad {
		dir := t.TempDir()
		code, _, _ := do(t, advEnv(), "-endpoint", srv.URL, "-groups", "sts", "-out", dir, "-name", n)
		if code != 2 {
			t.Errorf("-name %q accepted (exit %d)", n, code)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Errorf("-name %q left files behind: %v", n, ents)
		}
	}
	for _, n := range []string{"report", "report-moto", "r.1", "R_2", "0", "a-b.c_d"} {
		dir := t.TempDir()
		if code, _, errs := do(t, advEnv(), "-endpoint", srv.URL, "-groups", "sts", "-out", dir, "-name", n); code != 0 {
			t.Errorf("-name %q rejected: %d %s", n, code, errs)
		}
		for _, ext := range []string{".md", ".json"} {
			if _, err := os.Stat(filepath.Join(dir, n+ext)); err != nil {
				t.Errorf("-name %q: %v", n, err)
			}
		}
	}
}

func TestAdvOutputFiles(t *testing.T) {
	srv := stsOK(t)
	t.Run("nested directory is created, nothing leaks outside, no temp files remain, mode 0644", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "a", "b", "c")
		if code, _, errs := do(t, advEnv(), "-endpoint", srv.URL, "-groups", "sts", "-out", dir); code != 0 {
			t.Fatalf("%d %s", code, errs)
		}
		ents, _ := os.ReadDir(dir)
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
			info, _ := e.Info()
			if info.Mode().Perm() != 0o644 {
				t.Errorf("%s mode %v", e.Name(), info.Mode().Perm())
			}
		}
		if strings.Join(names, ",") != "report.json,report.md" {
			t.Errorf("files = %v", names)
		}
		top, _ := os.ReadDir(root)
		if len(top) != 1 {
			t.Errorf("root = %v", top)
		}
	})
	t.Run("the JSON file is valid, self-describing and free of secrets", func(t *testing.T) {
		dir := t.TempDir()
		do(t, advEnv(), "-endpoint", srv.URL, "-groups", "iam,sts", "-out", dir, "-wait", "10ms")
		for _, f := range []string{"report.md", "report.json"} {
			b, err := os.ReadFile(filepath.Join(dir, f))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), advSecret) || strings.Contains(string(b), advAccess) {
				t.Errorf("%s contains a credential", f)
			}
			if f == "report.json" {
				var v map[string]any
				if err := json.Unmarshal(b, &v); err != nil {
					t.Fatal(err)
				}
				if _, ok := v["results"].([]any); !ok {
					t.Errorf("no results list: %.200s", b)
				}
			}
		}
	})
	t.Run("-out is an existing file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "file")
		_ = os.WriteFile(f, []byte("x"), 0o600)
		code, out, errs := do(t, advEnv(), "-endpoint", srv.URL, "-groups", "sts", "-out", f)
		if code != 2 || out == "" || !strings.Contains(errs, "write report") {
			t.Errorf("code=%d errs=%q", code, errs)
		}
		if b, _ := os.ReadFile(f); string(b) != "x" {
			t.Error("the file was modified")
		}
	})
	t.Run("-out parent is a file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "file")
		_ = os.WriteFile(f, []byte("x"), 0o600)
		if code, _, _ := do(t, advEnv(), "-endpoint", srv.URL, "-groups", "sts", "-out", filepath.Join(f, "sub")); code != 2 {
			t.Errorf("code=%d", code)
		}
	})
	t.Run("unwritable directory", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root ignores permissions")
		}
		dir := t.TempDir()
		_ = os.Chmod(dir, 0o555)
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		code, out, errs := do(t, advEnv(), "-endpoint", srv.URL, "-groups", "sts", "-out", dir)
		if code != 2 || !strings.Contains(out, "sts:GetCallerIdentity") || !strings.Contains(errs, "write report") {
			t.Errorf("code=%d errs=%q", code, errs)
		}
	})
	t.Run("a failed write leaves no temp file", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "report.md"), []byte("OLD"), 0o644)
		_ = os.Mkdir(filepath.Join(dir, "report.json"), 0o755) // rename onto a directory fails
		code, _, _ := do(t, advEnv(), "-endpoint", srv.URL, "-groups", "sts", "-out", dir)
		if code != 2 {
			t.Errorf("code=%d", code)
		}
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if strings.Contains(e.Name(), ".tmp-") {
				t.Errorf("temp file left: %s", e.Name())
			}
		}
	})
	t.Run("an existing report is replaced, not appended to", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < 2; i++ {
			do(t, advEnv(), "-endpoint", srv.URL, "-groups", "sts", "-out", dir)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "report.md"))
		if strings.Count(string(b), "# S0 contract probe") != 1 {
			t.Error("report duplicated")
		}
	})
	t.Run("-out traversal is the caller's choice and stays contained to that directory", func(t *testing.T) {
		root := t.TempDir()
		_ = os.MkdirAll(filepath.Join(root, "x", "y"), 0o755)
		dir := filepath.Join(root, "x", "y", "..", "z")
		if code, _, errs := do(t, advEnv(), "-endpoint", srv.URL, "-groups", "sts", "-out", dir); code != 0 {
			t.Fatalf("%d %s", code, errs)
		}
		if _, err := os.Stat(filepath.Join(root, "x", "z", "report.md")); err != nil {
			t.Error(err)
		}
		if ents, _ := os.ReadDir(filepath.Join(root, "x", "y")); len(ents) != 0 {
			t.Errorf("wrote into y: %v", ents)
		}
	})
}

func TestAdvPricesMode(t *testing.T) {
	price := func(usd string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := json.Marshal(map[string]any{"PriceList": []string{
				`{"product":{"attributes":{"instanceType":"c5.large"}},"terms":{"OnDemand":{"T":{"priceDimensions":{"D":{"pricePerUnit":{"USD":"` + usd + `"}}}}}}}`}})
			_, _ = w.Write(b)
		}))
	}
	t.Run("prints JSON and exits 0, trimming spaces", func(t *testing.T) {
		srv := price("0.085")
		defer srv.Close()
		code, out, errs := do(t, advEnv(), "-endpoint", srv.URL, "-prices", " c5.large ")
		var got map[string]float64
		if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got["c5.large"] != 0.085 {
			t.Fatalf("code=%d out=%q errs=%q", code, out, errs)
		}
	})
	t.Run("prices mode writes no report files", func(t *testing.T) {
		srv := price("0.085")
		defer srv.Close()
		dir := t.TempDir()
		do(t, advEnv(), "-endpoint", srv.URL, "-prices", "c5.large", "-out", dir)
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Errorf("files: %v", ents)
		}
	})
	t.Run("a wrong type is an error, stdout stays empty", func(t *testing.T) {
		srv := price("0.085")
		defer srv.Close()
		code, out, errs := do(t, advEnv(), "-endpoint", srv.URL, "-prices", "m5.large")
		if code != 1 || out != "" || errs == "" {
			t.Errorf("code=%d out=%q errs=%q", code, out, errs)
		}
	})
	t.Run("backend error text with the secret is redacted on stderr", func(t *testing.T) {
		srv := stsOK(t)
		_, out, errs := do(t, advEnv(), "-endpoint", srv.URL, "-prices", "m5.large")
		if strings.Contains(out+errs, advSecret) || strings.Contains(out+errs, advAccess) {
			t.Errorf("leak: %s %s", out, errs)
		}
	})
}

func TestBugPricesModeExitsZeroWithEmptyOutputForANaNPrice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(map[string]any{"PriceList": []string{
			`{"product":{"attributes":{"instanceType":"c5.large"}},"terms":{"OnDemand":{"T":{"priceDimensions":{"D":{"pricePerUnit":{"USD":"NaN"}}}}}}}`}})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	code, out, errs := do(t, advEnv(), "-endpoint", srv.URL, "-prices", "c5.large")
	// BUG: OnDemandPrices accepts NaN, json.Encoder then fails, the error is dropped (`_ =`), and the gate sees
	// exit status 0 with an empty stdout.
	if code == 0 && out == "" {
		t.Fatalf("exit 0 with no output (stderr %q)", errs)
	}
}

func TestAdvTimeoutBoundsASlowBackend(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	code, out, _ := do(t, advEnv(), "-endpoint", srv.URL, "-timeout", "300ms", "-wait", "10ms")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("took %s with -timeout 300ms", time.Since(start))
	}
	if strings.Contains(out, "| pass |") {
		t.Errorf("a timed-out run reports passes:\n%s", out)
	}
}

func TestAdvConnectionRefusedExitsOneWithAReportNotAPanic(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	code, out, errs := do(t, advEnv(), "-endpoint", url, "-groups", "sts")
	if code != 1 || !strings.Contains(out, "fail") || strings.Contains(errs, "panic") {
		t.Errorf("code=%d out=%.200s errs=%q", code, out, errs)
	}
}

func TestAdvCredentialsAreNotEchoedByUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"-secret=" + advSecret}, {"-access-key", advAccess}, {"-endpoint", "http://u:" + advSecret + "@h", "-bogus"}, {"-groups", advSecret},
		{"-name", advSecret}, {"-prices", advSecret + ",,"},
	} {
		_, out, errs := do(t, advEnv(), args...)
		// The offending *argument* may be echoed by design; the environment credentials must not be.
		if strings.Contains(out+errs, advAccess) && !strings.Contains(strings.Join(args, " "), advAccess) {
			t.Errorf("%v: stderr leaks the access key id: %s", args, errs)
		}
	}
}
