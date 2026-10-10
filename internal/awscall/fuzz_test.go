package awscall

import (
	"strings"
	"testing"
)

func FuzzBuildQuery(f *testing.F) {
	f.Add("K=v", "DescribeX", "ec2", "us-west-2")
	f.Add("K=a=b&c", "A", "iam", "r")
	f.Add("=", "", "sts", "")
	f.Add("日本=é", "A\nB", "ec2", "a/b")
	f.Fuzz(func(t *testing.T, param, action, service, region string) {
		// Odd regions are covered by TestBugRegionWithScopeDelimiterIsAcceptedButUnverifiable.
		if len(region) > 32 {
			region = "us-west-2"
		}
		for _, c := range region {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				region = "us-west-2"
				break
			}
		}
		r, body, err := BuildQuery("http://127.0.0.1:4566", service, region, action, "2016-11-15", []string{param}, advAK, advSecret, advNow)
		if err != nil {
			return
		}
		if r.Header.Get("Authorization") == "" {
			t.Fatalf("unsigned request without error: param=%q action=%q svc=%q region=%q", param, action, service, region)
		}
		if err := advVerify(r, body); err != nil {
			t.Fatalf("built request does not verify: %v (param=%q action=%q svc=%q region=%q)", err, param, action, service, region)
		}
	})
}

func FuzzBuildREST(f *testing.F) {
	f.Add("/clusters", "GET", "")
	f.Add("/a%zz", "POST", "{}")
	f.Add("/a b/../c?x=y", "DELETE", "x")
	f.Fuzz(func(t *testing.T, path, method, body string) {
		if strings.Contains(path, "?") { // known: TestBugBuildersNeverReturnUnsignedRequestWithoutError
			t.Skip()
		}
		r, b, err := BuildREST("http://127.0.0.1:4566", "eks", "us-west-2", method, path, body, advAK, advSecret, advNow)
		if err != nil {
			return
		}
		if r.Header.Get("Authorization") == "" {
			t.Fatalf("unsigned request without error: path=%q method=%q", path, method)
		}
		if err := advVerify(r, b); err != nil {
			t.Fatalf("does not verify: %v (path=%q method=%q)", err, path, method)
		}
	})
}
