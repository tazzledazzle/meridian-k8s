package s0probe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func priceDoc(typ, usd string) string {
	return `{"product":{"attributes":{"instanceType":"` + typ + `"}},"terms":{"OnDemand":{"T":{"priceDimensions":{"D":{"pricePerUnit":{"USD":"` + usd + `"}}}}}}}`
}

func TestParsePrice(t *testing.T) {
	typ, usd, err := parsePrice(priceDoc("c5.large", "0.085"))
	if err != nil || typ != "c5.large" || usd != 0.085 {
		t.Fatalf("parsePrice = %q %v %v", typ, usd, err)
	}
	if _, _, err := parsePrice("not json"); err == nil {
		t.Fatal("a non-JSON entry must be an error")
	}
}

func TestOnDemandPricesAsksPerTypeAndRejectsMismatches(t *testing.T) {
	prices := map[string]string{"c5.large": "0.085", "m5.large": "0.096"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct{ Filters []map[string]string }
		_ = json.Unmarshal(b, &req)
		var list []string
		for _, f := range req.Filters {
			if f["Field"] == "instanceType" {
				if usd, ok := prices[f["Value"]]; ok {
					list = append(list, priceDoc(f["Value"], usd))
				}
			}
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(map[string]any{"PriceList": list})
	}))
	defer srv.Close()
	cfg := Config{Endpoint: srv.URL, Region: "us-west-2", Access: "AK", Secret: "SK"}
	got, err := OnDemandPrices(context.Background(), cfg, []string{"c5.large", "m5.large"})
	if err != nil || got["c5.large"] != 0.085 || got["m5.large"] != 0.096 {
		t.Fatalf("prices = %v, %v", got, err)
	}
	if _, err := OnDemandPrices(context.Background(), cfg, []string{"t3.nano"}); err == nil || !strings.Contains(err.Error(), "no on-demand price") {
		t.Fatalf("missing type should fail, got %v", err)
	}
}

// A price document with no USD term must not turn into a silent zero price: the
// gate would then compare against zero.
func TestOnDemandPricesRejectsDocumentWithoutUSD(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc := `{"product":{"attributes":{"instanceType":"c5.large"}},"terms":{"OnDemand":{}}}`
		_ = json.NewEncoder(w).Encode(map[string]any{"PriceList": []string{doc}})
	}))
	defer srv.Close()
	cfg := Config{Endpoint: srv.URL, Region: "us-west-2", Access: "AK", Secret: "SK"}
	if _, err := OnDemandPrices(context.Background(), cfg, []string{"c5.large"}); err == nil {
		t.Fatal("a document without pricePerUnit.USD must be an error")
	}
}
