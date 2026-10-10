package awsapi

import (
	"net/http"
	"testing"
	"time"
)

// FuzzVerify feeds arbitrary Authorization / X-Amz-Date strings to Verify against a
// request genuinely signed by Sign. Verify must never panic. If it returns nil, the
// parsed credential scope, signature and date must be those Sign produced (syntactic
// whitespace variants are legitimately accepted, anything else is a forgery).
func FuzzVerify(f *testing.F) {
	mk := func() (*http.Request, []byte) {
		body := []byte("Action=X")
		r, _ := http.NewRequest("POST", "http://127.0.0.1:4566/p?a=1", nil)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		Sign(r, body, "AK", "SECRET", "us-west-2", "ec2", t0)
		return r, body
	}
	g, _ := mk()
	goodAuth, goodDate := g.Header.Get("Authorization"), g.Header.Get("X-Amz-Date")
	genuine, err := parseAuth(goodAuth)
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range []string{goodAuth, "", "Bearer x", goodAuth + ",", "  " + goodAuth, goodAuth[:len(goodAuth)-1], "AWS4-HMAC-SHA256 Credential=//", "\x00"} {
		f.Add(s, goodDate)
	}
	for _, d := range []string{"", "20261009T120000.5Z", "20261009", "20261009t120000z", "garbage", "20261009T120000Z\n"} {
		f.Add(goodAuth, d)
	}
	f.Fuzz(func(t *testing.T, auth, date string) {
		r, body := mk()
		r.Header.Set("Authorization", auth)
		r.Header.Set("X-Amz-Date", date)
		a, err := Verify(r, body, lookupFor("AK", "SECRET"), t0, time.Minute)
		if err != nil {
			if a.AccessKey != "" || a.Signature != "" {
				t.Fatalf("non-zero Auth with error %v: %+v", err, a)
			}
			return
		}
		if date != goodDate {
			t.Fatalf("accepted altered X-Amz-Date %q", date)
		}
		if a.AccessKey != genuine.AccessKey || a.Date != genuine.Date || a.Region != genuine.Region ||
			a.Service != genuine.Service || a.Signature != genuine.Signature ||
			len(a.SignedHeaders) != len(genuine.SignedHeaders) {
			t.Fatalf("accepted forged Authorization %q -> %+v", auth, a)
		}
	})
}
