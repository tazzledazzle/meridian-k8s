package pricingfacade

import (
	"encoding/json"
	"strconv"
	"testing"
)

// FuzzGetProducts feeds arbitrary request JSON and an arbitrary NextToken. The handler must never panic, must answer
// 200 or 400 only, must always answer valid JSON, and a 200 must carry only parseable, positive-priced documents.
func FuzzGetProducts(f *testing.F) {
	f.Add(req(karpenterFilters), "")
	f.Add(`{"ServiceCode":"AmazonEC2","MaxResults":2}`, "")
	f.Add(`{"ServiceCode":"AmazonEC2","MaxResults":2}`, makeToken(2, fingerprint(getProductsRequest{ServiceCode: "AmazonEC2"})))
	f.Add(`{`, "!!!")
	f.Add(`{"ServiceCode":"AmazonEC2","Filters":[{"Type":"EQUALS"}]}`, "cGYxOjE6MA")
	f.Add(`{"ServiceCode":null,"MaxResults":1e400}`, "")
	h := New()
	f.Fuzz(func(t *testing.T, body, token string) {
		// Inject the token as a JSON string next to the fuzzed document when the document is an object.
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(body), &m) == nil && m != nil {
			m["NextToken"], _ = json.Marshal(token)
			body2, _ := json.Marshal(m)
			check(t, h, string(body2))
		}
		check(t, h, body)
	})
}

func check(t *testing.T, h *Handler, body string) {
	t.Helper()
	code, out, _ := post(t, h, target, body)
	if code != 200 && code != 400 {
		t.Fatalf("status %d for %q", code, body)
	}
	if !json.Valid(out) {
		t.Fatalf("invalid JSON response for %q: %s", body, out)
	}
	if code != 200 {
		return
	}
	var r response
	if err := json.Unmarshal(out, &r); err != nil || r.PriceList == nil || len(r.PriceList) > 100 {
		t.Fatalf("bad 200 body for %q: %s", body, out)
	}
	for _, e := range r.PriceList {
		_, usd := priceOf(t, e)
		if p, err := strconv.ParseFloat(usd, 64); err != nil || !(p > 0) {
			t.Fatalf("bad price %q", usd)
		}
	}
}
