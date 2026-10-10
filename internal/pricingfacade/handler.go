// Package pricingfacade answers the AWS Price List API (GetProducts) for EC2 on-demand Linux prices from the catalog.
package pricingfacade

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/catalog"
)

const (
	jsonContentType = "application/x-amz-json-1.1"
	offerTermCode   = "JRTCKXETXF" // the real on-demand offer term code
	rateCodeSuffix  = "6YS6EN2CT7" // the real hourly rate code
	publicationDate = "2026-01-01T00:00:00Z"
	version         = "20260101000000"
)

// product is one pre-rendered catalog entry: the PriceList document plus the fields filters match against.
type product struct {
	doc   string            // the JSON document returned inside PriceList
	attrs map[string]string // filterable fields (product attributes, productFamily, marketoption)
}

// Handler serves AWSPriceListService.GetProducts.
type Handler struct {
	products []product // one per catalog type, sorted by instance type
	// Logf, if set, is told about each refused request (operation, status and error code only; never a body).
	Logf func(format string, args ...any)
}

const (
	maxBody    = 1 << 20 // the gateway never forwards more than this
	maxResults = 100     // AWS GetProducts limit; also the default page size
)

// New builds the product list once; the catalog is static so every answer is deterministic.
func New() *Handler {
	h := &Handler{}
	for _, t := range catalog.Types {
		h.products = append(h.products, newProduct(t, "Shared", "Compute Instance"))
	}
	// Karpenter 1.14.1 runs a standard and a bare-metal query and fails with "no on-demand pricing found" when
	// EITHER is empty. The catalog has no bare-metal type, so one product answers the bare-metal query; it is not
	// a catalog type, so ec2-sim never offers it and Karpenter never launches it.
	h.products = append(h.products, newProduct(bareMetal, "Dedicated", "Compute Instance (bare metal)"))
	return h
}

// bareMetal is the single bare-metal product (price and size follow the real m5.metal).
var bareMetal = catalog.Type{Name: "m5.metal", VCPU: 96, MemoryMiB: 393216, OnDemand: 4.608}

// newProduct renders the AWS price list document for one instance type, tenancy and product family.
func newProduct(t catalog.Type, tenancy, family string) product {
	sku := skuFor(t.Name)
	attrs := map[string]string{
		"servicecode": "AmazonEC2", "location": "US West (Oregon)", "locationType": "AWS Region",
		"instanceType": t.Name, "tenancy": tenancy, "operatingSystem": "Linux", "preInstalledSw": "NA",
		"capacitystatus": "Used", "regionCode": catalog.Region, "usagetype": "USW2-BoxUsage:" + t.Name,
		"vcpu": strconv.Itoa(t.VCPU), "memory": strconv.FormatFloat(float64(t.MemoryMiB)/1024, 'f', -1, 64) + " GiB",
		"operation": "RunInstances", "licenseModel": "No License required",
	}
	term := sku + "." + offerTermCode
	rate := term + "." + rateCodeSuffix
	doc := map[string]any{
		"product":     map[string]any{"productFamily": family, "attributes": attrs, "sku": sku},
		"serviceCode": "AmazonEC2",
		"terms": map[string]any{"OnDemand": map[string]any{term: map[string]any{
			"offerTermCode": offerTermCode, "sku": sku, "effectiveDate": publicationDate, "termAttributes": map[string]string{},
			"priceDimensions": map[string]any{rate: map[string]any{
				"unit": "Hrs", "endRange": "Inf", "rateCode": rate, "beginRange": "0",
				"description":  "$" + strconv.FormatFloat(t.OnDemand, 'f', -1, 64) + " per On Demand Linux " + t.Name + " Instance Hour",
				"appliesTo":    []string{},
				"pricePerUnit": map[string]string{"USD": strconv.FormatFloat(t.OnDemand, 'f', 10, 64)},
			}},
		}}},
		"version": version, "publicationDate": publicationDate,
	}
	b, _ := json.Marshal(doc) // maps marshal in sorted key order, so the bytes are stable
	// Filter field names are case-insensitive (Karpenter sends "serviceCode" for the attribute "servicecode"),
	// so the keys are stored lower-cased and matches() lower-cases the requested field.
	filterable := map[string]string{"productfamily": family, "marketoption": "OnDemand"}
	for k, v := range attrs {
		filterable[strings.ToLower(k)] = v
	}
	return product{doc: string(b), attrs: filterable}
}

// skuFor derives a stable 16-character SKU from the instance type (FNV-1a, then hex, upper-cased like AWS SKUs).
func skuFor(name string) string {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(name); i++ {
		h = (h ^ uint64(name[i])) * 1099511628211
	}
	const hex = "0123456789ABCDEF"
	b := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		b[i] = hex[h&0xf]
		h >>= 4
	}
	return string(b)
}

type filter struct {
	Type  string `json:"Type"`
	Field string `json:"Field"`
	Value string `json:"Value"`
}

type getProductsRequest struct {
	ServiceCode   string   `json:"ServiceCode"`
	FormatVersion string   `json:"FormatVersion"`
	Filters       []filter `json:"Filters"`
	MaxResults    *int     `json:"MaxResults"` // pointer: absent and null mean the default
	NextToken     *string  `json:"NextToken"`
}

// fail writes an AWS JSON error: __type and message in the body, the code again in X-Amzn-Errortype (SDKs read the header first).
func (h *Handler) fail(w http.ResponseWriter, status int, code, msg string) {
	if h.Logf != nil {
		h.Logf("pricing-facade: refused request: status=%d code=%s", status, code)
	}
	w.Header().Set("X-Amzn-Errortype", code)
	awsapi.WriteJSON(w, status, jsonContentType, map[string]string{"__type": code, "message": msg})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.fail(w, http.StatusMethodNotAllowed, "MethodNotAllowedException", "only POST is supported")
		return
	}
	// Only GetProducts is routed here; the gateway enforces that, this is the second line of defence.
	if tgt := r.Header.Get("X-Amz-Target"); tgt != "AWSPriceListService.GetProducts" {
		h.fail(w, http.StatusBadRequest, "UnknownOperationException", "unsupported operation")
		return
	}
	body, err := awsapi.ReadBody(r, maxBody)
	if err != nil {
		h.fail(w, http.StatusBadRequest, "SerializationException", "request body unreadable or larger than 1 MiB")
		return
	}
	var in getProductsRequest
	if err := json.Unmarshal(body, &in); err != nil {
		h.fail(w, http.StatusBadRequest, "SerializationException", "request is not a valid GetProducts JSON document")
		return
	}
	if msg := validate(in); msg != "" {
		h.fail(w, http.StatusBadRequest, "InvalidParameterException", msg)
		return
	}

	// Collect matches. Other service codes have no products, as on AWS for a code with no rows.
	var matched []string
	if in.ServiceCode == "AmazonEC2" {
		for _, p := range h.products {
			if matches(p, in.Filters) {
				matched = append(matched, p.doc)
			}
		}
	}

	fp := fingerprint(in)
	start := 0
	if in.NextToken != nil {
		start, err = parseToken(*in.NextToken, fp, len(matched))
		if err != nil {
			h.fail(w, http.StatusBadRequest, "InvalidNextTokenException", err.Error())
			return
		}
	}
	size := maxResults
	if in.MaxResults != nil {
		size = *in.MaxResults
	}
	end := min(start+size, len(matched))
	out := map[string]any{"FormatVersion": "aws_v1", "PriceList": append([]string{}, matched[start:end]...)}
	if end < len(matched) { // more remain: hand out an opaque cursor
		out["NextToken"] = makeToken(end, fp)
	}
	awsapi.WriteJSON(w, http.StatusOK, jsonContentType, out)
}

// validate returns a message for the first parameter AWS would refuse, or "".
func validate(in getProductsRequest) string {
	switch {
	case in.ServiceCode == "":
		return "ServiceCode is required"
	case in.FormatVersion != "" && in.FormatVersion != "aws_v1":
		return "FormatVersion must be aws_v1"
	case in.MaxResults != nil && (*in.MaxResults < 1 || *in.MaxResults > maxResults):
		return "MaxResults must be between 1 and 100"
	}
	for i, f := range in.Filters {
		if f.Type != "TERM_MATCH" { // the only filter type this facade implements
			return fmt.Sprintf("Filters[%d].Type must be TERM_MATCH", i)
		}
		if f.Field == "" {
			return fmt.Sprintf("Filters[%d].Field is required", i)
		}
	}
	return ""
}

// matches applies TERM_MATCH filters with AND semantics; a field the product does not have matches nothing.
func matches(p product, fs []filter) bool {
	for _, f := range fs {
		if v, ok := p.attrs[strings.ToLower(f.Field)]; !ok || v != f.Value {
			return false
		}
	}
	return true
}

// fingerprint identifies the query a token belongs to (service code and filters, not the page size).
func fingerprint(in getProductsRequest) string {
	b, _ := json.Marshal([]any{in.ServiceCode, in.Filters})
	var h uint64 = 14695981039346656037
	for _, c := range b {
		h = (h ^ uint64(c)) * 1099511628211
	}
	return strconv.FormatUint(h, 16)
}

// makeToken is opaque to clients: base64url("pf1:<offset>:<query fingerprint>").
func makeToken(offset int, fp string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("pf1:" + strconv.Itoa(offset) + ":" + fp))
}

// parseToken returns the offset a token points at, or an error if it is malformed, belongs to another query, or is out of range.
func parseToken(tok, fp string, total int) (int, error) {
	bad := fmt.Errorf("the NextToken is not valid for this request")
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return 0, bad
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 || parts[0] != "pf1" || parts[2] != fp {
		return 0, bad
	}
	off, err := strconv.Atoi(parts[1])
	if err != nil || strconv.Itoa(off) != parts[1] || off <= 0 || off >= total { // canonical form only; a cursor always points inside the set
		return 0, bad
	}
	return off, nil
}
