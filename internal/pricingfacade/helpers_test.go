package pricingfacade

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const target = "AWSPriceListService.GetProducts"

// post sends one JSON-RPC 1.1 request straight to the handler.
func post(t testing.TB, h http.Handler, tgt, body string) (int, []byte, http.Header) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if tgt != "" {
		req.Header.Set("X-Amz-Target", tgt)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), rec.Header()
}

// karpenterFilters is the filter set Karpenter (and the S0 probe) sends for Linux on-demand prices.
const karpenterFilters = `[
 {"Type":"TERM_MATCH","Field":"regionCode","Value":"us-west-2"},
 {"Type":"TERM_MATCH","Field":"tenancy","Value":"Shared"},
 {"Type":"TERM_MATCH","Field":"preInstalledSw","Value":"NA"},
 {"Type":"TERM_MATCH","Field":"operatingSystem","Value":"Linux"},
 {"Type":"TERM_MATCH","Field":"capacitystatus","Value":"Used"},
 {"Type":"TERM_MATCH","Field":"marketoption","Value":"OnDemand"}]`

type response struct {
	FormatVersion string   `json:"FormatVersion"`
	PriceList     []string `json:"PriceList"`
	NextToken     string   `json:"NextToken"`
}

func decode(t testing.TB, body []byte) response {
	t.Helper()
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, body)
	}
	return r
}

// priceOf mirrors Karpenter's parsing: product.attributes.instanceType and terms.OnDemand.*.priceDimensions.*.pricePerUnit.USD.
func priceOf(t testing.TB, entry string) (string, string) {
	t.Helper()
	var doc struct {
		Product struct {
			Attributes map[string]string `json:"attributes"`
		} `json:"product"`
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct {
					PricePerUnit map[string]string `json:"pricePerUnit"`
				} `json:"priceDimensions"`
			} `json:"OnDemand"`
		} `json:"terms"`
	}
	if err := json.Unmarshal([]byte(entry), &doc); err != nil {
		t.Fatalf("entry is not JSON: %v", err)
	}
	for _, term := range doc.Terms.OnDemand {
		for _, dim := range term.PriceDimensions {
			return doc.Product.Attributes["instanceType"], dim.PricePerUnit["USD"]
		}
	}
	return doc.Product.Attributes["instanceType"], ""
}
