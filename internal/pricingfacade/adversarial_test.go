package pricingfacade

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

// advRaw sends a hand-built SDK-shaped request over a real HTTP connection.
func advRaw(t testing.TB, url, body string, mut func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSPriceListService.GetProducts")
	req.Header.Set("User-Agent", "aws-sdk-go-v2/1.30.0 os/linux lang/go#1.22 md/GOOS#linux api/pricing#1.28.0")
	req.Header.Set("X-Amz-Date", "20260101T000000Z")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260101/us-east-1/pricing/aws4_request, SignedHeaders=host;x-amz-date, Signature=deadbeef")
	req.Header.Set("Accept-Encoding", "gzip")
	if mut != nil {
		mut(req)
	}
	resp, err := http.DefaultTransport.RoundTrip(req) // no transparent gunzip, no redirects
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// karpenterProduct mirrors the structures Karpenter's pricing provider unmarshals.
type karpenterProduct struct {
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

// karpenterParse re-implements Karpenter's per-entry parse: instanceType from attributes, the first
// term/dimension, USD parsed as a float.
func karpenterParse(entry string) (string, float64, error) {
	var p karpenterProduct
	if err := json.Unmarshal([]byte(entry), &p); err != nil {
		return "", 0, err
	}
	it := p.Product.Attributes["instanceType"]
	if it == "" {
		return "", 0, fmt.Errorf("no instanceType")
	}
	for _, term := range p.Terms.OnDemand {
		for _, dim := range term.PriceDimensions {
			f, err := strconv.ParseFloat(dim.PricePerUnit["USD"], 64)
			if err != nil {
				return "", 0, err
			}
			return it, f, nil
		}
	}
	return "", 0, fmt.Errorf("no price")
}

// karpenterWalk runs the GetProducts + NextToken loop over the wire and returns type->price and order.
func karpenterWalk(t *testing.T, url string, filters string, maxResults string) (map[string]float64, []string) {
	t.Helper()
	out := map[string]float64{}
	var order []string
	token := ""
	for page := 0; page < 500; page++ {
		body := `{"ServiceCode":"AmazonEC2","Filters":` + filters
		if maxResults != "" {
			body += `,"MaxResults":` + maxResults
		}
		if token != "" {
			body += `,"NextToken":` + strconv.Quote(token)
		}
		body += `}`
		resp, b := advRaw(t, url, body, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, b)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-amz-json-1.1") {
			t.Fatalf("content-type %q", ct)
		}
		r := decode(t, b)
		for _, e := range r.PriceList {
			it, p, err := karpenterParse(e)
			if err != nil {
				t.Fatalf("karpenter parse: %v", err)
			}
			if _, dup := out[it]; dup {
				t.Fatalf("duplicate %s", it)
			}
			out[it] = p
			order = append(order, it)
		}
		if r.NextToken == "" {
			return out, order
		}
		token = r.NextToken
	}
	t.Fatal("pagination did not terminate")
	return nil, nil
}

func TestKarpenterWalkOverRealHTTPMatchesCatalog(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()
	base := []string{
		f("regionCode", "us-west-2"), f("tenancy", "Shared"), f("preInstalledSw", "NA"),
		f("operatingSystem", "Linux"), f("capacitystatus", "Used"),
	}
	orders := map[string][]string{
		"as-is":       base,
		"reversed":    {base[4], base[3], base[2], base[1], base[0]},
		"duplicates":  append(append([]string{}, base...), base[0], base[3], base[3]),
		"with-family": append(append([]string{}, base...), f("productFamily", "Compute Instance")),
		"with-type":   append(append([]string{}, base...), f("instanceType", "m5.large")),
		"with-market": append(append([]string{}, base...), f("marketoption", "OnDemand")),
		"with-loc":    append(append([]string{}, base...), f("location", "US West (Oregon)")),
	}
	for name, fs := range orders {
		for _, mr := range []string{"", "1", "5", "100"} {
			t.Run(name+"/max="+mr, func(t *testing.T) {
				got, order := karpenterWalk(t, srv.URL, "["+strings.Join(fs, ",")+"]", mr)
				want := len(catalog.Types)
				if name == "with-type" {
					want = 1
				}
				if len(got) != want || len(order) != want {
					t.Fatalf("got %d types want %d", len(got), want)
				}
				for tn, p := range got {
					ct, ok := catalog.Lookup(tn)
					if !ok || math.Abs(ct.OnDemand-p) > 1e-12 {
						t.Fatalf("%s: price %v catalog %v ok=%v", tn, p, ct.OnDemand, ok)
					}
				}
			})
		}
	}
}

func TestRequestVariantsOverTheWire(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()
	n := totalProducts // unfiltered listings include the bare-metal product
	cases := []struct {
		name, body string
		status     int
		list       int // -1: do not check
	}{
		{"empty filters array", `{"ServiceCode":"AmazonEC2","Filters":[]}`, 200, n},
		{"no filters key", `{"ServiceCode":"AmazonEC2"}`, 200, n},
		{"maxresults 1", `{"ServiceCode":"AmazonEC2","MaxResults":1}`, 200, 1},
		{"maxresults 100", `{"ServiceCode":"AmazonEC2","MaxResults":100}`, 200, n},
		{"maxresults 101", `{"ServiceCode":"AmazonEC2","MaxResults":101}`, 400, -1},
		{"maxresults 0", `{"ServiceCode":"AmazonEC2","MaxResults":0}`, 400, -1},
		{"maxresults -1", `{"ServiceCode":"AmazonEC2","MaxResults":-1}`, 400, -1},
		{"maxresults huge", `{"ServiceCode":"AmazonEC2","MaxResults":99999999999999999999}`, 400, -1},
		{"maxresults int64 max", `{"ServiceCode":"AmazonEC2","MaxResults":9223372036854775807}`, 400, -1},
		{"maxresults float", `{"ServiceCode":"AmazonEC2","MaxResults":1.5}`, 400, -1},
		{"maxresults string", `{"ServiceCode":"AmazonEC2","MaxResults":"10"}`, 400, -1},
		{"maxresults null", `{"ServiceCode":"AmazonEC2","MaxResults":null}`, 200, n},
		{"service amazonec2 lower", `{"ServiceCode":"amazonec2"}`, 200, 0},
		{"service empty", `{"ServiceCode":""}`, 400, -1},
		{"service missing", `{}`, 400, -1},
		{"service S3", `{"ServiceCode":"AmazonS3"}`, 200, 0},
		{"formatversion aws_v1", `{"ServiceCode":"AmazonEC2","FormatVersion":"aws_v1"}`, 200, n},
		{"formatversion bad", `{"ServiceCode":"AmazonEC2","FormatVersion":"aws_v2"}`, 400, -1},
		{"formatversion case", `{"ServiceCode":"AmazonEC2","FormatVersion":"AWS_V1"}`, 400, -1},
		{"filter type EQUALS", `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"EQUALS","Field":"a","Value":"b"}]}`, 400, -1},
		{"filter type lower", `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"term_match","Field":"a","Value":"b"}]}`, 400, -1},
		{"filter value case", `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"TERM_MATCH","Field":"operatingSystem","Value":"LINUX"}]}`, 200, 0},
		// Field names are case-insensitive (Karpenter sends "serviceCode" for the stored "servicecode"); values are not.
		{"filter field case", `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"TERM_MATCH","Field":"OperatingSystem","Value":"Linux"}]}`, 200, n},
		{"filters null elem", `{"ServiceCode":"AmazonEC2","Filters":[null]}`, 400, -1},
		{"filters wrong type", `{"ServiceCode":"AmazonEC2","Filters":"x"}`, 400, -1},
		{"unknown field ignored", `{"ServiceCode":"AmazonEC2","Bogus":1}`, 200, n},
		{"duplicate keys last wins", `{"ServiceCode":"AmazonS3","ServiceCode":"AmazonEC2"}`, 200, n},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, b := advRaw(t, srv.URL, c.body, nil)
			if resp.StatusCode != c.status {
				t.Fatalf("status %d want %d: %s", resp.StatusCode, c.status, b)
			}
			if c.status == 200 && c.list >= 0 && len(decode(t, b).PriceList) != c.list {
				t.Fatalf("list %d want %d", len(decode(t, b).PriceList), c.list)
			}
			if c.status == 400 {
				advAssertErrorShape(t, resp, b)
			}
		})
	}
}

// ---- pagination ----

func advFullList(t testing.TB, h http.Handler, size int, filters string) []string {
	t.Helper()
	var all []string
	token := ""
	for i := 0; i < 200; i++ {
		body := `{"ServiceCode":"AmazonEC2","MaxResults":` + strconv.Itoa(size) + `,"Filters":` + filters
		if token != "" {
			body += `,"NextToken":"` + token + `"`
		}
		body += `}`
		code, out, _ := post(t, h, target, body)
		if code != 200 {
			t.Fatalf("size %d: %d %s", size, code, out)
		}
		r := decode(t, out)
		if len(r.PriceList) == 0 || len(r.PriceList) > size {
			t.Fatalf("size %d: page of %d", size, len(r.PriceList))
		}
		for _, e := range r.PriceList {
			it, _, _ := karpenterParse(e)
			all = append(all, it)
		}
		if r.NextToken == "" {
			return all
		}
		token = r.NextToken
	}
	t.Fatal("no termination")
	return nil
}

func TestEveryPageSizeYieldsEachTypeOnceInStableOrder(t *testing.T) {
	h := New()
	for size := 1; size <= 100; size++ {
		got := advFullList(t, h, size, "[]")
		if len(got) != totalProducts {
			t.Fatalf("size %d: %d items", size, len(got))
		}
		for i, ct := range catalog.Types {
			if got[i] != ct.Name {
				t.Fatalf("size %d: position %d is %s want %s", size, i, got[i], ct.Name)
			}
		}
	}
}

func advFirstToken(t *testing.T, h http.Handler, body string) string {
	t.Helper()
	code, out, _ := post(t, h, target, body)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	tok := decode(t, out).NextToken
	if tok == "" {
		t.Fatalf("expected a NextToken: %s", out)
	}
	return tok
}

func TestTokenTamperingTruncationGarbageAndForeignQueries(t *testing.T) {
	h := New()
	q := func(tok string, filters string) string {
		return `{"ServiceCode":"AmazonEC2","MaxResults":3,"Filters":` + filters + `,"NextToken":` + strconv.Quote(tok) + `}`
	}
	tok := advFirstToken(t, h, `{"ServiceCode":"AmazonEC2","MaxResults":3,"Filters":[]}`)
	raw, _ := base64.RawURLEncoding.DecodeString(tok)
	for i := range raw {
		m := append([]byte{}, raw...)
		m[i] ^= 0x01
		// The offset digits are not secret: a forged offset only selects another slice of the same query.
		if i >= 4 && i < 4+len(strconv.Itoa(3)) {
			continue
		}
		code, out, _ := post(t, h, target, q(base64.RawURLEncoding.EncodeToString(m), "[]"))
		if code == 200 {
			t.Errorf("flip at %d accepted: %s", i, out)
		}
	}
	bad := []string{"!!!", "====", strings.Repeat("A", 1<<16), tok[:len(tok)-1], tok[:2], tok + "A", tok + "=", "cGYxOjM6",
		base64.StdEncoding.EncodeToString(raw) + "=="}
	for _, b := range bad {
		code, out, hdr := post(t, h, target, q(b, "[]"))
		if code != 400 {
			t.Errorf("token %.20q: status %d %s", b, code, out)
			continue
		}
		if hdr.Get("X-Amzn-Errortype") != "InvalidNextTokenException" {
			t.Errorf("token %.20q: error type %q", b, hdr.Get("X-Amzn-Errortype"))
		}
	}
	if code, _, _ := post(t, h, target, q("", "[]")); code != 200 && code != 400 {
		t.Errorf("empty token: %d", code)
	}
	for _, other := range []string{`[` + f("instanceType", "m5.large") + `]`, `[` + f("tenancy", "Shared") + `]`, `[` + f("regionCode", "us-west-2") + `]`} {
		if code, _, _ := post(t, h, target, q(tok, other)); code != 400 {
			t.Errorf("foreign filter token accepted for %s: %d", other, code)
		}
	}
	if code, _, _ := post(t, h, target, `{"ServiceCode":"AmazonS3","NextToken":"`+tok+`"}`); code != 400 {
		t.Errorf("foreign service token accepted: %d", code)
	}
}

func TestTokenReplayIsIdempotentAndWorksAcrossInstances(t *testing.T) {
	a, b := New(), New()
	tok := advFirstToken(t, a, `{"ServiceCode":"AmazonEC2","MaxResults":4}`)
	next := `{"ServiceCode":"AmazonEC2","MaxResults":4,"NextToken":"` + tok + `"}`
	_, o1, _ := post(t, a, target, next)
	_, o2, _ := post(t, a, target, next)
	_, o3, _ := post(t, b, target, next)
	if !bytes.Equal(o1, o2) {
		t.Fatal("replay differs")
	}
	if !bytes.Equal(o1, o3) {
		t.Fatal("instances disagree")
	}
	_, o4, _ := post(t, a, target, `{"ServiceCode":"AmazonEC2","MaxResults":1,"NextToken":"`+tok+`"}`)
	if len(decode(t, o4).PriceList) != 1 {
		t.Fatalf("page size change: %s", o4)
	}
}

func TestTokenOffsetEdgesAreRejected(t *testing.T) {
	h := New()
	fp := fingerprint(getProductsRequest{ServiceCode: "AmazonEC2"})
	for _, off := range []string{"0", "-1", "00", "01", "+1", "13", "14", "99999999999999999999", " 1", "1.0", "0x1"} {
		tok := base64.RawURLEncoding.EncodeToString([]byte("pf1:" + off + ":" + fp))
		code, out, _ := post(t, h, target, `{"ServiceCode":"AmazonEC2","NextToken":"`+tok+`"}`)
		if code != 400 {
			t.Errorf("offset %q accepted: %d %s", off, code, out)
		}
	}
}

// ---- data quality ----

func TestDocumentsMatchAWSShape(t *testing.T) {
	h := New()
	_, out, _ := post(t, h, target, `{"ServiceCode":"AmazonEC2"}`)
	list := decode(t, out).PriceList
	skus := map[string]bool{}
	priceRe := regexp.MustCompile(`^\d+\.\d{10}$`)
	if len(list) != totalProducts {
		t.Fatalf("%d docs", len(list))
	}
	// The first len(catalog.Types) documents are the catalog types in order; the last is the bare-metal product.
	if name, _ := parseLikeKarpenter(t, list[len(list)-1]); name != bareMetal.Name {
		t.Errorf("last document is %q, want the bare-metal product %q", name, bareMetal.Name)
	}
	for i, e := range list[:len(catalog.Types)] {
		ct := catalog.Types[i]
		var d map[string]any
		if err := json.Unmarshal([]byte(e), &d); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"product", "serviceCode", "terms", "version", "publicationDate"} {
			if _, ok := d[k]; !ok {
				t.Errorf("%s: missing top-level %q", ct.Name, k)
			}
		}
		prod := d["product"].(map[string]any)
		sku := prod["sku"].(string)
		if skus[sku] || len(sku) != 16 {
			t.Errorf("%s: sku %q dup or bad length", ct.Name, sku)
		}
		skus[sku] = true
		if prod["productFamily"] != "Compute Instance" {
			t.Errorf("productFamily %v", prod["productFamily"])
		}
		at := prod["attributes"].(map[string]any)
		want := map[string]string{
			"location": "US West (Oregon)", "regionCode": "us-west-2", "usagetype": "USW2-BoxUsage:" + ct.Name,
			"instanceType": ct.Name, "operatingSystem": "Linux", "tenancy": "Shared", "preInstalledSw": "NA",
			"capacitystatus": "Used", "vcpu": strconv.Itoa(ct.VCPU), "servicecode": "AmazonEC2", "operation": "RunInstances",
		}
		for k, v := range want {
			if at[k] != v {
				t.Errorf("%s: attr %s = %v want %s", ct.Name, k, at[k], v)
			}
		}
		if ct.Arch == "arm64" {
			// arm64 types must not claim an Intel processor.
			for _, k := range []string{"physicalProcessor", "processorArchitecture", "processorFeatures"} {
				if v, ok := at[k]; ok && strings.Contains(fmt.Sprint(v), "Intel") {
					t.Errorf("%s (arm64) claims %s=%v", ct.Name, k, v)
				}
			}
		}
		if at["memory"] != strconv.FormatFloat(float64(ct.MemoryMiB)/1024, 'f', -1, 64)+" GiB" {
			t.Errorf("%s memory %v", ct.Name, at["memory"])
		}
		od := d["terms"].(map[string]any)["OnDemand"].(map[string]any)
		if len(od) != 1 {
			t.Fatalf("terms: %d", len(od))
		}
		for tk, tv := range od {
			if !strings.HasPrefix(tk, sku+".") || !strings.HasSuffix(tk, ".JRTCKXETXF") {
				t.Errorf("term key %s", tk)
			}
			term := tv.(map[string]any)
			if term["offerTermCode"] != "JRTCKXETXF" || term["sku"] != sku {
				t.Errorf("term %v", term)
			}
			if _, err := time.Parse(time.RFC3339, term["effectiveDate"].(string)); err != nil {
				t.Errorf("effectiveDate: %v", err)
			}
			for rk, rv := range term["priceDimensions"].(map[string]any) {
				if !strings.HasPrefix(rk, tk+".") {
					t.Errorf("rate key %s", rk)
				}
				dim := rv.(map[string]any)
				usd := dim["pricePerUnit"].(map[string]any)["USD"].(string)
				if !priceRe.MatchString(usd) {
					t.Errorf("%s: price %q not 10dp", ct.Name, usd)
				}
				p, _ := strconv.ParseFloat(usd, 64)
				if !(p > 0) || math.IsInf(p, 0) || math.Abs(p-ct.OnDemand) > 1e-9 {
					t.Errorf("%s: price %v catalog %v", ct.Name, p, ct.OnDemand)
				}
				if dim["unit"] != "Hrs" || dim["rateCode"] != rk {
					t.Errorf("dimension %v", dim)
				}
			}
		}
		if _, err := time.Parse(time.RFC3339, d["publicationDate"].(string)); err != nil {
			t.Errorf("publicationDate: %v", err)
		}
		if !regexp.MustCompile(`^\d{14}$`).MatchString(d["version"].(string)) {
			t.Errorf("version %v", d["version"])
		}
	}
	if len(skus) != len(catalog.Types) { // SKUs of the catalog documents; the bare-metal one is checked above
		t.Fatalf("%d skus", len(skus))
	}
}

func TestBytesAndSKUsAreDeterministicAcrossInstances(t *testing.T) {
	_, a, _ := post(t, New(), target, `{"ServiceCode":"AmazonEC2"}`)
	_, b, _ := post(t, New(), target, `{"ServiceCode":"AmazonEC2"}`)
	if !bytes.Equal(a, b) {
		t.Fatal("bodies differ between instances")
	}
}

// ---- protocol ----

func advAssertErrorShape(t testing.TB, resp *http.Response, body []byte) {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-amz-json-1.1") {
		t.Errorf("error content-type %q", ct)
	}
	if advSDKErrorCode(resp.Header, body) == "" {
		t.Errorf("SDK could not derive an error code: hdr=%v body=%s", resp.Header, body)
	}
	var b map[string]string
	if err := json.Unmarshal(body, &b); err != nil || b["message"] == "" || b["__type"] == "" {
		t.Errorf("error body %s (%v)", body, err)
	}
}

// advSDKErrorCode mirrors aws-sdk-go-v2 awsjson1_1 error handling: X-Amzn-ErrorType first, else __type/code from
// the body; SanitizeErrorCode strips anything after ':' and everything up to and including '#'.
func advSDKErrorCode(h http.Header, body []byte) string {
	code := h.Get("X-Amzn-Errortype")
	if code == "" {
		var b struct {
			Type string `json:"__type"`
			Code string `json:"code"`
		}
		_ = json.Unmarshal(body, &b)
		code = b.Type
		if code == "" {
			code = b.Code
		}
	}
	if i := strings.Index(code, ":"); i >= 0 {
		code = code[:i]
	}
	if i := strings.LastIndex(code, "#"); i >= 0 {
		code = code[i+1:]
	}
	return strings.TrimSpace(code)
}

func TestSDKCanReadEveryErrorCode(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()
	cases := map[string]struct {
		mut  func(*http.Request)
		body string
		want string
	}{
		"bad json":       {nil, `{`, "SerializationException"},
		"bad param":      {nil, `{}`, "InvalidParameterException"},
		"bad token":      {nil, `{"ServiceCode":"AmazonEC2","NextToken":"zzz"}`, "InvalidNextTokenException"},
		"bad target":     {func(r *http.Request) { r.Header.Set("X-Amz-Target", "AWSPriceListService.DescribeServices") }, `{}`, "UnknownOperationException"},
		"missing target": {func(r *http.Request) { r.Header.Del("X-Amz-Target") }, `{}`, "UnknownOperationException"},
		"GET":            {func(r *http.Request) { r.Method = http.MethodGet }, ``, "MethodNotAllowedException"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			resp, b := advRaw(t, srv.URL, c.body, c.mut)
			if resp.StatusCode/100 != 4 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if got := advSDKErrorCode(resp.Header, b); got != c.want {
				t.Fatalf("code %q want %q", got, c.want)
			}
			advAssertErrorShape(t, resp, b)
			if got := advSDKErrorCode(http.Header{}, b); got != c.want {
				t.Fatalf("body-only code %q", got)
			}
		})
	}
	for in, want := range map[string]string{"com.amazon#Foo": "Foo", "Foo:http://x": "Foo", "a.b#Foo:x": "Foo"} {
		if got := advSDKErrorCode(http.Header{"X-Amzn-Errortype": {in}}, nil); got != want {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

func TestProtocolEdges(t *testing.T) {
	h := New()
	do := func(method, path, tgt, body string, mut func(*http.Request)) (int, []byte, http.Header) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if tgt != "" {
			r.Header.Set("X-Amz-Target", tgt)
		}
		if mut != nil {
			mut(r)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes(), w.Header()
	}
	ok := `{"ServiceCode":"AmazonEC2"}`
	for _, m := range []string{"GET", "PUT", "DELETE", "HEAD", "OPTIONS", "PATCH"} {
		code, _, hdr := do(m, "/", target, ok, nil)
		if code != 405 || hdr.Get("Allow") != "POST" {
			t.Errorf("%s: %d allow=%q", m, code, hdr.Get("Allow"))
		}
	}
	for _, tgt := range []string{"", "GetProducts", "AWSPriceListService.getproducts", "awspricelistservice.GetProducts", "AWSPriceListService.GetProducts ", " AWSPriceListService.GetProducts",
		"AWSPriceListService_V20170101.GetProducts", "Other.GetProducts", "AWSPriceListService.GetProducts.x", "AWSPriceListService.DescribeServices"} {
		if c, _, _ := do("POST", "/", tgt, ok, nil); c != 400 {
			t.Errorf("target %q accepted: %d", tgt, c)
		}
	}
	if c, _, _ := do("POST", "/", "", ok, func(r *http.Request) {
		r.Header["X-Amz-Target"] = []string{"AWSPriceListService.DescribeServices", target}
	}); c == 200 {
		t.Errorf("first bad target then good one was accepted")
	}
	for _, p := range []string{"/", "/x", "//", "/../", "/?a=b", "/%00"} {
		if code, _, _ := do("POST", p, target, ok, nil); code >= 500 {
			t.Errorf("path %q -> %d", p, code)
		}
	}
	pad := func(n int) string {
		return `{"ServiceCode":"AmazonEC2","X":"` + strings.Repeat("a", n-len(`{"ServiceCode":"AmazonEC2","X":""}`)) + `"}`
	}
	if c, _, _ := do("POST", "/", target, pad(1<<20), nil); c != 200 {
		t.Errorf("exactly 1MiB: %d", c)
	}
	if c, b, _ := do("POST", "/", target, pad(1<<20+1), nil); c != 400 {
		t.Errorf("1MiB+1: %d %s", c, b)
	}
	cases := map[string]string{
		"empty":            ``,
		"whitespace":       "   ",
		"null":             `null`,
		"array":            `[]`,
		"string":           `"x"`,
		"number":           `1`,
		"invalid utf8":     "{\"ServiceCode\":\"\xff\xfe\"}",
		"nul":              "{\"ServiceCode\":\"AmazonEC2\x00\"}",
		"trailing garbage": `{"ServiceCode":"AmazonEC2"} xyz`,
		"two docs":         `{"ServiceCode":"AmazonEC2"}{"ServiceCode":"AmazonEC2"}`,
		"deep nesting":     `{"ServiceCode":"AmazonEC2","X":` + strings.Repeat("[", 100000) + strings.Repeat("]", 100000) + `}`,
		"deep objects":     `{"ServiceCode":"AmazonEC2","X":` + strings.Repeat(`{"a":`, 50000) + `1` + strings.Repeat("}", 50000) + `}`,
		"bom":              "\xef\xbb\xbf" + ok,
	}
	for name, body := range cases {
		t.Run("body/"+name, func(t *testing.T) {
			c, b, _ := do("POST", "/", target, body, nil)
			if c >= 500 || !json.Valid(b) {
				t.Fatalf("%d %s", c, b)
			}
			switch name {
			case "empty", "whitespace", "array", "string", "number", "trailing garbage", "two docs", "deep nesting", "deep objects", "bom":
				if c != 400 {
					t.Errorf("want 400 got %d", c)
				}
			}
		})
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(ok))
	_ = zw.Close()
	if c, _, _ := do("POST", "/", target, gz.String(), func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }); c != 400 {
		t.Errorf("gzip request: %d", c)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, b := advRaw(t, srv.URL, ok, func(r *http.Request) { r.Header.Set("Expect", "100-continue") })
	if resp.StatusCode != 200 {
		t.Errorf("expect-continue: %d %s", resp.StatusCode, b)
	}
	resp, b = advRaw(t, srv.URL, ok, func(r *http.Request) { r.Proto, r.ProtoMajor, r.ProtoMinor = "HTTP/1.0", 1, 0; r.Close = true })
	if resp.StatusCode != 200 {
		t.Errorf("http/1.0: %d %s", resp.StatusCode, b)
	}
	resp, _ = advRaw(t, srv.URL, ok, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") })
	if resp.StatusCode >= 500 {
		t.Errorf("content-type: %d", resp.StatusCode)
	}
}

func TestHugeFilterArrayIsFastAndBounded(t *testing.T) {
	h := New()
	one := `{"Type":"TERM_MATCH","Field":"instanceType","Value":"m5.large"},`
	body := `{"ServiceCode":"AmazonEC2","Filters":[` + strings.Repeat(one, (1<<20-100)/len(one)) + `{"Type":"TERM_MATCH","Field":"tenancy","Value":"Shared"}]}`
	if len(body) > 1<<20 {
		t.Fatalf("test body too large %d", len(body))
	}
	start := time.Now()
	c, out, _ := post(t, h, target, body)
	if c != 200 || len(decode(t, out).PriceList) != 1 {
		t.Fatalf("%d", c)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	bad := strings.Replace(body, `"Type":"TERM_MATCH","Field":"tenancy"`, `"Type":"X","Field":"tenancy"`, 1)
	if c, _, _ := post(t, h, target, bad); c != 400 {
		t.Fatalf("bad last filter: %d", c)
	}
	big := `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"TERM_MATCH","Field":"a","Value":"` + strings.Repeat("v", 1000000) + `"}]}`
	start = time.Now()
	if c, _, _ := post(t, h, target, big); c != 200 || time.Since(start) > time.Second {
		t.Fatalf("big value: %d %v", c, time.Since(start))
	}
}

// ---- concurrency, logging ----

func TestConcurrentMixedPagesAreDeterministic(t *testing.T) {
	h := New()
	want := map[int][]string{}
	for size := 1; size <= 12; size++ {
		want[size] = advFullList(t, h, size, "[]")
	}
	var wg sync.WaitGroup
	errs := make(chan string, 200)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			size := i%12 + 1
			got := advFullList(t, h, size, "[]")
			if strings.Join(got, ",") != strings.Join(want[size], ",") {
				errs <- fmt.Sprintf("size %d diverged", size)
			}
			if i%7 == 0 {
				post(t, h, target, `{`)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestLogsNeverContainBodiesTokensOrCredentials(t *testing.T) {
	h := New()
	var mu sync.Mutex
	var logs []string
	h.Logf = func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	secret := "SUPERSECRETVALUE"
	post(t, h, target, `{"ServiceCode":"AmazonEC2","NextToken":"`+secret+`"}`)
	post(t, h, target, `{"ServiceCode":"`+secret+`","MaxResults":-1}`)
	post(t, h, target, `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"`+secret+`","Field":"f"}]}`)
	post(t, h, target, `{`+secret)
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+secret)
	r.Header.Set("X-Amz-Target", "Wrong."+secret)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if len(logs) < 4 {
		t.Fatalf("expected refusals logged, got %d", len(logs))
	}
	for _, l := range logs {
		if strings.Contains(l, secret) {
			t.Errorf("log leaks: %s", l)
		}
	}
}

func TestResponsesDoNotEchoRequestContent(t *testing.T) {
	h := New()
	secret := "SUPERSECRETVALUE"
	_, out, _ := post(t, h, target, `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"TERM_MATCH","Field":"x","Value":"`+secret+`"}]}`)
	if strings.Contains(string(out), secret) {
		t.Fatalf("echoed: %s", out)
	}
	_, out, _ = post(t, h, target, `{"ServiceCode":"`+secret+`","FormatVersion":"`+secret+`"}`)
	if strings.Contains(string(out), secret) {
		t.Fatalf("echoed in error: %s", out)
	}
}

func TestMutatingAResponseDoesNotCorruptHandlerState(t *testing.T) {
	h := New()
	_, a, _ := post(t, h, target, `{"ServiceCode":"AmazonEC2"}`)
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"ServiceCode":"AmazonEC2"}`))
	r.Header.Set("X-Amz-Target", target)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	bs := w.Body.Bytes()
	for i := range bs {
		bs[i] = 'x'
	}
	_, b, _ := post(t, h, target, `{"ServiceCode":"AmazonEC2"}`)
	if !bytes.Equal(a, b) {
		t.Fatal("state corrupted")
	}
}
