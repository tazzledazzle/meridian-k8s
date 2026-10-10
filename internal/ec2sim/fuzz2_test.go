package ec2sim

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

// FuzzGlobMatchesRegexp: glob must agree with a regexp translation of the same pattern.
func FuzzGlobMatchesRegexp(f *testing.F) {
	for _, s := range [][2]string{{"m*", "m5.large"}, {"?5.large", "m5.large"}, {"*a*a*b", "aaaa"}, {"", ""}, {"**", ""}} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, pat, s string) {
		if len(pat) > 64 || len(s) > 64 {
			t.Skip()
		}
		if strings.Contains(s, "*") {
			t.Skip() // known defect, pinned by TestBugGlobStarWildcardConsumedByLiteralStar
		}
		for _, r := range pat + s {
			if r >= 0x80 {
				// glob's ? is byte-wise, EC2's is per character. Harmless here: every attribute value
				// the service filters on is ASCII, so a multi-byte ? can never match a real value.
				t.Skip()
			}
		}
		if glob(pat, s) != refGlob(pat, s) {
			t.Fatalf("glob(%q,%q)=%v regexp=%v", pat, s, glob(pat, s), refGlob(pat, s))
		}
	})
}

// FuzzPagedWalkEqualsUnpaged: for any page size and filter pattern, the paged walk equals the unpaged result.
func FuzzPagedWalkEqualsUnpaged(f *testing.F) {
	f.Add(5, "m*")
	f.Add(100, "*")
	f.Add(7, "?5.large")
	h := New(catalog.Types)
	f.Fuzz(func(t *testing.T, size int, pat string) {
		if len(pat) > 64 {
			t.Skip()
		}
		post := func(kv ...string) *httptest.ResponseRecorder {
			v := url.Values{"Action": {"DescribeInstanceTypes"}}
			for i := 0; i+1 < len(kv); i += 2 {
				v.Add(kv[i], kv[i+1])
			}
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(v.Encode()))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec
		}
		flt := []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", pat}
		un := post(flt...)
		if un.Code != 200 {
			t.Fatalf("unpaged %d", un.Code)
		}
		want := strings.Join(names(parse(t, un.Body.String())), ",")
		if size < 5 || size > 100 {
			if rec := post(append([]string{"MaxResults", strconv.Itoa(size)}, flt...)...); rec.Code != 400 {
				t.Fatalf("MaxResults=%d: %d", size, rec.Code)
			}
			return
		}
		var got []string
		token := ""
		for i := 0; i < 50; i++ {
			kv := append([]string{"MaxResults", strconv.Itoa(size)}, flt...)
			if token != "" {
				kv = append(kv, "NextToken", token)
			}
			rec := post(kv...)
			if rec.Code != 200 {
				t.Fatalf("page %d: %d", i, rec.Code)
			}
			r := parse(t, rec.Body.String())
			if len(r.Items) == 0 && r.Next != "" {
				t.Fatal("empty page with a token")
			}
			got = append(got, names(r)...)
			if r.Next == "" {
				break
			}
			token = r.Next
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("paged %v != unpaged %s", got, want)
		}
	})
}

// FuzzNextToken: arbitrary tokens (raw and base64-wrapped) never panic and yield 400 or a well-formed catalog subset.
func FuzzNextToken(f *testing.F) {
	f.Add("")
	f.Add("ZWMyc2ltOnYxOm01Lmxhcmdl")
	f.Add("!!!")
	h := New(catalog.Types)
	f.Fuzz(func(t *testing.T, tok string) {
		for _, tk := range []string{tok, base64.RawURLEncoding.EncodeToString([]byte("ec2sim:v1:" + tok))} {
			v := url.Values{"Action": {"DescribeInstanceTypes"}, "MaxResults": {"5"}, "NextToken": {tk}}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(v.Encode())))
			if rec.Code != 200 && rec.Code != 400 {
				t.Fatalf("status %d for %q", rec.Code, tk)
			}
			wellFormed(t, rec.Body.Bytes())
		}
	})
}
