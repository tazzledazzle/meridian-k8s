package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testAccess = "AKIATESTONLY"
	testSecret = "test/secret+value"
)

func env(extra map[string]string) func(string) string {
	m := map[string]string{"AWS_ACCESS_KEY_ID": testAccess, "AWS_SECRET_ACCESS_KEY": testSecret}
	for k, v := range extra {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

func do(t *testing.T, getenv func(string) string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, getenv, &out, &errb)
	return code, out.String(), errb.String()
}

// backend answers STS correctly and every IAM call with HTTP 500.
func backend(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.Header.Get("Authorization"), "/sts/aws4_request"):
			w.Write([]byte(`<R><GetCallerIdentityResult><Account>000000000000</Account></GetCallerIdentityResult></R>`))
		case strings.Contains(r.Header.Get("X-Amz-Target"), "GetProducts"):
			w.Write([]byte(`{"PriceList":["{\"product\":{\"attributes\":{\"instanceType\":\"c5.large\"}},\"terms\":{\"OnDemand\":{\"T\":{\"priceDimensions\":{\"D\":{\"pricePerUnit\":{\"USD\":\"0.085\"}}}}}}}"]}`))
		default:
			http.Error(w, "boom "+testSecret, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCredentialsMustComeFromTheEnvironment(t *testing.T) {
	srv := backend(t)
	for _, missing := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		code, out, errs := do(t, env(map[string]string{missing: ""}), "-endpoint", srv.URL, "-groups", "sts")
		if code != 2 || !strings.Contains(errs, missing) || out != "" {
			t.Errorf("without %s: code=%d stdout=%q stderr=%q", missing, code, out, errs)
		}
	}
}

func TestThereAreNoCredentialFlags(t *testing.T) {
	code, _, errs := do(t, env(nil), "-secret", "x")
	if code != 2 || !strings.Contains(errs, "flag provided but not defined") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestRunPassingGroupExitsZeroAndWritesReports(t *testing.T) {
	srv := backend(t)
	dir := t.TempDir()
	code, out, errs := do(t, env(nil), "-endpoint", srv.URL, "-groups", "sts", "-out", dir, "-name", "report-moto", "-title", "S0 test", "-wait", "10ms")
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out, errs)
	}
	if !strings.Contains(out, "# S0 test") || !strings.Contains(out, "sts:GetCallerIdentity") {
		t.Fatalf("stdout:\n%s", out)
	}
	for _, f := range []string{"report-moto.md", "report-moto.json"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), testSecret) || strings.Contains(string(b), testAccess) {
			t.Fatalf("%s contains a credential", f)
		}
	}
	var rep struct {
		Results []struct{ Name, Status string }
	}
	b, _ := os.ReadFile(filepath.Join(dir, "report-moto.json"))
	if err := json.Unmarshal(b, &rep); err != nil || len(rep.Results) != 1 || rep.Results[0].Status != "pass" {
		t.Fatalf("report.json: %v %s", err, b)
	}
}

func TestRunWithAFailureExitsOneStillWritesReportsAndNeverEchoesSecrets(t *testing.T) {
	srv := backend(t)
	dir := t.TempDir()
	code, out, errs := do(t, env(nil), "-endpoint", srv.URL, "-groups", "sts,iam", "-out", dir)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "iam:CreateRole") || !strings.Contains(out, "HTTP 500") {
		t.Fatalf("stdout:\n%s", out)
	}
	if strings.Contains(out, testSecret) || strings.Contains(errs, testSecret) {
		t.Fatal("a backend echoed the secret into the output")
	}
	if _, err := os.Stat(filepath.Join(dir, "report.json")); err != nil {
		t.Fatalf("default report name: %v", err)
	}
}

func TestBadFlagValuesAreRejectedBeforeAnyRequest(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	cases := map[string][]string{
		"unknown group":         {"-endpoint", srv.URL, "-groups", "sts,nope"},
		"endpoint not a URL":    {"-endpoint", "not a url"},
		"endpoint wrong scheme": {"-endpoint", "ftp://x"},
		"name with a slash":     {"-endpoint", srv.URL, "-name", "../evil"},
		"empty name":            {"-endpoint", srv.URL, "-name", ""},
		"negative wait":         {"-endpoint", srv.URL, "-wait", "-1s"},
		"empty cluster":         {"-endpoint", srv.URL, "-cluster", ""},
		"empty price type":      {"-endpoint", srv.URL, "-prices", "c5.large,,"},
	}
	for name, args := range cases {
		code, _, errs := do(t, env(nil), args...)
		if code != 2 || errs == "" {
			t.Errorf("%s: code=%d stderr=%q", name, code, errs)
		}
		if strings.Contains(errs, testSecret) {
			t.Errorf("%s: stderr contains the secret", name)
		}
	}
	if hits != 0 {
		t.Fatalf("%d requests were sent", hits)
	}
}

func TestUnwritableOutputDirectoryIsExitTwo(t *testing.T) {
	srv := backend(t)
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errs := do(t, env(nil), "-endpoint", srv.URL, "-groups", "sts", "-out", filepath.Join(f, "sub"))
	if code != 2 || !strings.Contains(errs, "write report") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestPricesFlagPrintsJSON(t *testing.T) {
	srv := backend(t)
	code, out, errs := do(t, env(nil), "-endpoint", srv.URL, "-prices", "c5.large")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errs)
	}
	var got map[string]float64
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["c5.large"] != 0.085 {
		t.Fatalf("stdout %q: %v", out, err)
	}
	// The backend answers a c5.large document for t3.nano: a mismatch the gate must not accept.
	code, _, errs = do(t, env(nil), "-endpoint", srv.URL, "-prices", "t3.nano", "-wait", "10ms")
	if code != 1 || errs == "" {
		t.Fatalf("bad type list: code=%d stderr=%q", code, errs)
	}
}

func TestTimeoutStopsALongRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // the server only notices a closed connection after the body is read
		select {
		case <-r.Context().Done():
		case <-t.Context().Done():
		}
	}))
	defer srv.Close()
	code, out, _ := do(t, env(nil), "-endpoint", srv.URL, "-groups", "sts,iam", "-timeout", "200ms")
	if code != 1 || !strings.Contains(out, "sts:GetCallerIdentity") {
		t.Fatalf("code=%d stdout=%s", code, out)
	}
}
