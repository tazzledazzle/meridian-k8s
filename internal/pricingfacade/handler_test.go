package pricingfacade

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

func req(filters string) string {
	return `{"ServiceCode":"AmazonEC2","FormatVersion":"aws_v1","Filters":` + filters + `}`
}

func TestKarpenterQueryReturnsOneDocumentPerCatalogTypeWithCatalogPrices(t *testing.T) {
	code, body, hdr := post(t, New(), target, req(karpenterFilters))
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "application/x-amz-json-1.1") {
		t.Fatalf("code=%d type=%s body=%s", code, hdr.Get("Content-Type"), body)
	}
	out := decode(t, body)
	if out.FormatVersion != "aws_v1" || len(out.PriceList) != len(catalog.Types) || out.NextToken != "" {
		t.Fatalf("format=%q entries=%d token=%q", out.FormatVersion, len(out.PriceList), out.NextToken)
	}
	got := map[string]float64{}
	for _, e := range out.PriceList {
		typ, usd := priceOf(t, e)
		p, err := strconv.ParseFloat(usd, 64)
		if err != nil {
			t.Fatalf("%s: price %q: %v", typ, usd, err)
		}
		got[typ] = p
	}
	for _, ty := range catalog.Types {
		if got[ty.Name] != ty.OnDemand {
			t.Errorf("%s = %v, want %v", ty.Name, got[ty.Name], ty.OnDemand)
		}
	}
}

func TestDocumentHasTheRealAWSShape(t *testing.T) {
	_, body, _ := post(t, New(), target, req(`[{"Type":"TERM_MATCH","Field":"instanceType","Value":"m5.large"}]`))
	out := decode(t, body)
	if len(out.PriceList) != 1 {
		t.Fatalf("entries = %d", len(out.PriceList))
	}
	var doc struct {
		Product struct {
			ProductFamily string            `json:"productFamily"`
			Attributes    map[string]string `json:"attributes"`
			SKU           string            `json:"sku"`
		} `json:"product"`
		ServiceCode     string `json:"serviceCode"`
		Version         string `json:"version"`
		PublicationDate string `json:"publicationDate"`
		Terms           struct {
			OnDemand map[string]struct {
				OfferTermCode   string `json:"offerTermCode"`
				SKU             string `json:"sku"`
				PriceDimensions map[string]struct {
					Unit         string            `json:"unit"`
					RateCode     string            `json:"rateCode"`
					PricePerUnit map[string]string `json:"pricePerUnit"`
				} `json:"priceDimensions"`
			} `json:"OnDemand"`
		} `json:"terms"`
	}
	if err := json.Unmarshal([]byte(out.PriceList[0]), &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"servicecode": "AmazonEC2", "location": "US West (Oregon)", "locationType": "AWS Region", "instanceType": "m5.large",
		"tenancy": "Shared", "operatingSystem": "Linux", "preInstalledSw": "NA", "capacitystatus": "Used",
		"regionCode": "us-west-2", "usagetype": "USW2-BoxUsage:m5.large",
	}
	for k, v := range want {
		if doc.Product.Attributes[k] != v {
			t.Errorf("attributes[%s] = %q, want %q", k, doc.Product.Attributes[k], v)
		}
	}
	if doc.Product.ProductFamily != "Compute Instance" || doc.ServiceCode != "AmazonEC2" || doc.Product.SKU == "" || doc.Version == "" || doc.PublicationDate == "" {
		t.Fatalf("bad envelope: %+v", doc)
	}
	if len(doc.Terms.OnDemand) != 1 {
		t.Fatalf("on-demand terms = %d", len(doc.Terms.OnDemand))
	}
	for key, term := range doc.Terms.OnDemand {
		if key != doc.Product.SKU+".JRTCKXETXF" || term.OfferTermCode != "JRTCKXETXF" || term.SKU != doc.Product.SKU {
			t.Errorf("term key %q offer %q sku %q", key, term.OfferTermCode, term.SKU)
		}
		if len(term.PriceDimensions) != 1 {
			t.Errorf("dimensions = %d", len(term.PriceDimensions))
		}
		for rk, d := range term.PriceDimensions {
			if !strings.HasPrefix(rk, key+".") || d.RateCode != rk || d.Unit != "Hrs" || d.PricePerUnit["USD"] != "0.0960000000" {
				t.Errorf("dimension %q: %+v", rk, d)
			}
		}
	}
}

func TestResponsesAreDeterministicAndPricesAreSane(t *testing.T) {
	_, a, _ := post(t, New(), target, req(karpenterFilters))
	_, b, _ := post(t, New(), target, req(karpenterFilters))
	// The request id lives in a header, so bodies must be byte-identical across handler instances.
	if string(a) != string(b) {
		t.Fatal("two handlers answered the same query differently")
	}
	seenSKU := map[string]bool{}
	for _, e := range decode(t, a).PriceList {
		_, usd := priceOf(t, e)
		p, err := strconv.ParseFloat(usd, 64)
		if err != nil || !(p > 0) || p > 1e6 {
			t.Errorf("bad price %q", usd)
		}
		var d struct{ Product struct{ SKU string } }
		_ = json.Unmarshal([]byte(e), &d)
		if seenSKU[d.Product.SKU] {
			t.Errorf("duplicate sku %s", d.Product.SKU)
		}
		seenSKU[d.Product.SKU] = true
	}
}
