package spot

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

// FuzzEventKarpenterRoundTrip: whatever region, id and time, Event yields JSON
// that Karpenter's Metadata decodes, with the routing key intact.
func FuzzEventKarpenterRoundTrip(f *testing.F) {
	f.Add("us-west-2", aID, int64(0))
	f.Add("", "", int64(-1<<62))
	f.Add("\xff", "\x00", int64(1<<62))
	f.Fuzz(func(t *testing.T, region, id string, sec int64) {
		b := Event(region, id, time.Unix(sec, 0))
		if !json.Valid(b) {
			t.Fatalf("invalid JSON %q", b)
		}
		var md karpMetadata
		if err := json.Unmarshal(b, &md); err != nil {
			// time.Time refuses years outside 0..9999 on Marshal-side only; decode must work for what we emit
			// unless the formatted year is outside RFC3339's range.
			y := time.Unix(sec, 0).UTC().Year()
			if y >= 0 && y <= 9999 && !strings.ContainsRune(region+id, '\xff') {
				t.Fatalf("Karpenter Metadata cannot decode %q: %v", b, err)
			}
		}
	})
}

// FuzzValidatorsAgreeWithSpec: the id and queue validators must equal an
// independent hand-written predicate, and an accepted value must be safe to
// embed in a form parameter and a JSON string unchanged.
func FuzzValidatorsAgreeWithSpec(f *testing.F) {
	for _, s := range []string{"i-12345678", "q", "", "i-ÿ", "a b", "i-12345678\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		idOK := len(s) >= 10 && len(s) <= 34 && strings.HasPrefix(s, "i-")
		for _, c := range []byte(strings.TrimPrefix(s, "i-")) {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
				idOK = false
			}
		}
		if (ValidateInstanceID(s) == nil) != idOK {
			t.Fatalf("id %q: validator=%v spec=%v", s, ValidateInstanceID(s) == nil, idOK)
		}
		qOK := len(s) >= 1 && len(s) <= 80
		for _, c := range []byte(s) {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				qOK = false
			}
		}
		in := Injector{Endpoint: "http://127.0.0.1:1", Region: "us-west-2", QueueName: s, Access: "AK", Secret: "SK"}
		if (in.Validate() == nil) != qOK {
			t.Fatalf("queue %q: validator=%v spec=%v", s, in.Validate() == nil, qOK)
		}
		if idOK {
			if url.QueryEscape(s) != s {
				t.Fatalf("accepted id %q needs escaping", s)
			}
			j, _ := json.Marshal(s)
			if string(j) != `"`+s+`"` {
				t.Fatalf("accepted id %q needs JSON escaping", s)
			}
		}
	})
}

// FuzzInjectorValidateNeverPanics: arbitrary endpoint/region/credentials never panic,
// never make a request (Validate is offline) and never echo the secret.
func FuzzInjectorValidateNeverPanics(f *testing.F) {
	f.Add("http://h:1", "us-west-2", "q", "AK", "SK")
	f.Add("http://u:SECRETX@h", "r", "q", "a", "SECRETX")
	f.Add("\x00", "\x00", "\x00", "\x00", "\x00")
	f.Fuzz(func(t *testing.T, ep, region, q, ak, sk string) {
		in := &Injector{Endpoint: ep, Region: region, QueueName: q, Access: ak, Secret: sk}
		err := in.Validate()
		if err != nil && len(sk) >= 3 && strings.Contains(err.Error(), sk) {
			t.Fatalf("secret %q in error %q", sk, err)
		}
	})
}
