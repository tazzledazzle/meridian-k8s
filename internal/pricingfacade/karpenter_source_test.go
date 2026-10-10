package pricingfacade

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

// karpenterSourceFilters is the filter set in aws/karpenter-provider-aws v1.14.1,
// pkg/providers/pricing fetchOnDemandPricing: six base filters, then tenancy and productFamily for
// standard (non bare-metal) instances. Note the camel-case "serviceCode" field: the product attribute
// is stored as "servicecode", and the live stack showed an exact-case match returns nothing (found in Task 8).
// totalProducts is every product the facade serves: the catalog types plus one bare-metal product that only
// Karpenter's bare-metal query matches.
var totalProducts = len(catalog.Types) + 1

const karpenterSourceFilters = `[
 {"Type":"TERM_MATCH","Field":"regionCode","Value":"us-west-2"},
 {"Type":"TERM_MATCH","Field":"serviceCode","Value":"AmazonEC2"},
 {"Type":"TERM_MATCH","Field":"preInstalledSw","Value":"NA"},
 {"Type":"TERM_MATCH","Field":"operatingSystem","Value":"Linux"},
 {"Type":"TERM_MATCH","Field":"capacitystatus","Value":"Used"},
 {"Type":"TERM_MATCH","Field":"marketoption","Value":"OnDemand"},
 {"Type":"TERM_MATCH","Field":"tenancy","Value":"Shared"},
 {"Type":"TERM_MATCH","Field":"productFamily","Value":"Compute Instance"}]`

func TestKarpenterSourceFilterSetReturnsEveryCatalogType(t *testing.T) {
	code, body, _ := post(t, New(), target, `{"ServiceCode":"AmazonEC2","Filters":`+karpenterSourceFilters+`,"MaxResults":100}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.PriceList) != len(catalog.Types) {
		t.Fatalf("got %d priced entries, want %d (one per catalog type); Karpenter would log 'no on-demand pricing found'",
			len(r.PriceList), len(catalog.Types))
	}
}

// Filter field names are case-insensitive, as they must be for Karpenter's "serviceCode" to match "servicecode".
func TestFilterFieldNamesAreCaseInsensitive(t *testing.T) {
	for _, field := range []string{"REGIONCODE", "regioncode", "RegionCode", "OperatingSystem", "operatingsystem"} {
		value := map[string]string{"regioncode": "us-west-2", "operatingsystem": "Linux"}[strings.ToLower(field)]
		body := `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"TERM_MATCH","Field":"` + field + `","Value":"` + value + `"}],"MaxResults":100}`
		code, out, _ := post(t, New(), target, body)
		var r response
		if code != 200 || json.Unmarshal(out, &r) != nil || len(r.PriceList) != totalProducts {
			t.Errorf("field %q: status %d, %d entries (want %d)", field, code, len(r.PriceList), totalProducts)
		}
	}
}

// A field the product really lacks still matches nothing, and values stay case-sensitive like TERM_MATCH.
func TestUnknownFieldAndWrongCaseValueStillMatchNothing(t *testing.T) {
	for name, f := range map[string]string{
		"unknown field":    `{"Type":"TERM_MATCH","Field":"nosuchfield","Value":"x"}`,
		"wrong-case value": `{"Type":"TERM_MATCH","Field":"operatingSystem","Value":"linux"}`,
	} {
		code, out, _ := post(t, New(), target, `{"ServiceCode":"AmazonEC2","Filters":[`+f+`],"MaxResults":100}`)
		var r response
		if code != 200 || json.Unmarshal(out, &r) != nil || len(r.PriceList) != 0 {
			t.Errorf("%s: status %d, %d entries, want 0", name, code, len(r.PriceList))
		}
	}
}

// Karpenter 1.14.1 UpdateOnDemandPricing runs two queries (standard and bare metal) and fails with "no on-demand
// pricing found" if EITHER result set is empty (live stack, Task 8). The catalog has no bare-metal type, so the
// facade carries one bare-metal product that only the bare-metal query matches.
func TestBareMetalQueryReturnsAPricedEntry(t *testing.T) {
	filters := strings.Replace(karpenterSourceFilters,
		`{"Type":"TERM_MATCH","Field":"tenancy","Value":"Shared"}`, `{"Type":"TERM_MATCH","Field":"tenancy","Value":"Dedicated"}`, 1)
	filters = strings.Replace(filters, `"Compute Instance"`, `"Compute Instance (bare metal)"`, 1)
	code, out, _ := post(t, New(), target, `{"ServiceCode":"AmazonEC2","Filters":`+filters+`,"MaxResults":100}`)
	var r response
	if code != 200 || json.Unmarshal(out, &r) != nil || len(r.PriceList) == 0 {
		t.Fatalf("status %d, %d entries, want 200 and at least one: Karpenter rejects an empty bare-metal set", code, len(r.PriceList))
	}
	for _, e := range r.PriceList {
		name, usd := parseLikeKarpenter(t, e)
		if name == "" || usd <= 0 {
			t.Errorf("bare-metal entry has instanceType %q price %v", name, usd)
		}
		if _, inCatalog := catalog.Lookup(name); inCatalog {
			t.Errorf("%s is a catalog type; the bare-metal product must not be one", name)
		}
	}
}

// The standard query must keep returning exactly the catalog types, so the bare-metal product never leaks into it.
func TestStandardQueryExcludesBareMetal(t *testing.T) {
	_, out, _ := post(t, New(), target, `{"ServiceCode":"AmazonEC2","Filters":`+karpenterSourceFilters+`,"MaxResults":100}`)
	var r response
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	for _, e := range r.PriceList {
		if name, _ := parseLikeKarpenter(t, e); strings.HasSuffix(name, ".metal") {
			t.Errorf("bare-metal type %s leaked into the standard query", name)
		}
	}
}

// parseLikeKarpenter mirrors priceItem in Karpenter's onDemandPage: instance type and the USD price of its on-demand term.
func parseLikeKarpenter(t *testing.T, entry string) (string, float64) {
	t.Helper()
	var it struct {
		Product struct {
			Attributes struct{ InstanceType string }
		}
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct{ PricePerUnit map[string]string }
			}
		}
	}
	if err := json.Unmarshal([]byte(entry), &it); err != nil {
		t.Fatal(err)
	}
	var usd float64
	for _, term := range it.Terms.OnDemand {
		for _, d := range term.PriceDimensions {
			if v, err := strconv.ParseFloat(d.PricePerUnit["USD"], 64); err == nil {
				usd = v
			}
		}
	}
	return it.Product.Attributes.InstanceType, usd
}
