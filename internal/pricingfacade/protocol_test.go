package pricingfacade

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

func f(field, value string) string {
	return `{"Type":"TERM_MATCH","Field":"` + field + `","Value":"` + value + `"}`
}

func count(t *testing.T, body string) int {
	t.Helper()
	code, out, _ := post(t, New(), target, body)
	if code != 200 {
		t.Fatalf("code %d: %s", code, out)
	}
	return len(decode(t, out).PriceList)
}

func TestFiltersAreANDedExactAndUnknownThingsMatchNothing(t *testing.T) {
	cases := []struct {
		name, filters string
		want          int
	}{
		{"one type", `[` + f("instanceType", "c5.large") + `]`, 1},
		{"type and family", `[` + f("productFamily", "Compute Instance") + `,` + f("instanceType", "c5.large") + `]`, 1},
		{"type and wrong os", `[` + f("instanceType", "c5.large") + `,` + f("operatingSystem", "Windows") + `]`, 0},
		{"exact not prefix", `[` + f("instanceType", "c5") + `]`, 0},
		{"case sensitive", `[` + f("operatingSystem", "linux") + `]`, 0},
		{"unknown field", `[` + f("nonsense", "x") + `]`, 0},
		{"unknown region", `[` + f("regionCode", "us-east-1") + `]`, 0},
		{"location name", `[` + f("location", "US West (Oregon)") + `]`, totalProducts},
		{"shared tenancy", `[` + f("tenancy", "Shared") + `]`, len(catalog.Types)},
		{"dedicated tenancy", `[` + f("tenancy", "Dedicated") + `]`, 1}, // the one bare-metal product
		{"no filters", `[]`, totalProducts},
		{"null filters", `null`, totalProducts},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := count(t, req(c.filters)); got != c.want {
				t.Fatalf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestOtherServiceCodesReturnAnEmptyListNotAnError(t *testing.T) {
	code, body, _ := post(t, New(), target, `{"ServiceCode":"AmazonS3"}`)
	if code != 200 || strings.Contains(string(body), `"PriceList":null`) || len(decode(t, body).PriceList) != 0 || !strings.Contains(string(body), `"PriceList":[]`) {
		t.Fatalf("code=%d body=%s", code, body)
	}
}

// walk pages through the whole result set and returns the entries in order and the page sizes.
func walk(t *testing.T, h http.Handler, max int) ([]string, []int) {
	t.Helper()
	var all []string
	var sizes []int
	token := ""
	for page := 0; page < 100; page++ {
		body := `{"ServiceCode":"AmazonEC2","Filters":` + karpenterFilters
		if max != 0 {
			body += `,"MaxResults":` + strconv.Itoa(max)
		}
		if token != "" {
			body += `,"NextToken":` + strconv.Quote(token)
		}
		code, out, _ := post(t, h, target, body+`}`)
		if code != 200 {
			t.Fatalf("page %d: %d %s", page, code, out)
		}
		r := decode(t, out)
		all = append(all, r.PriceList...)
		sizes = append(sizes, len(r.PriceList))
		if r.NextToken == "" {
			return all, sizes
		}
		token = r.NextToken
	}
	t.Fatal("pagination never ended")
	return nil, nil
}

func TestPaginationWalksEverythingExactlyOnceForEveryPageSize(t *testing.T) {
	h := New()
	_, want, _ := post(t, h, target, req(karpenterFilters))
	whole := decode(t, want).PriceList
	for _, max := range []int{0, 1, 2, 5, len(catalog.Types) - 1, len(catalog.Types), len(catalog.Types) + 1, 100} {
		all, sizes := walk(t, h, max)
		if strings.Join(all, "\n") != strings.Join(whole, "\n") {
			t.Errorf("max=%d: walk differs from the unpaged answer (%d vs %d entries)", max, len(all), len(whole))
		}
		limit := max
		if limit == 0 {
			limit = 100
		}
		for i, n := range sizes {
			if n == 0 || n > limit {
				t.Errorf("max=%d page %d has %d entries", max, i, n)
			}
		}
	}
}

func TestPageBoundaryHasNoTokenWhenNothingRemains(t *testing.T) {
	n := totalProducts
	_, body, _ := post(t, New(), target, `{"ServiceCode":"AmazonEC2","MaxResults":`+strconv.Itoa(n)+`}`)
	if r := decode(t, body); len(r.PriceList) != n || r.NextToken != "" {
		t.Fatalf("entries=%d token=%q", len(r.PriceList), r.NextToken)
	}
}

// errorOf returns the status, __type and message of an error response and checks the header agrees.
func errorOf(t *testing.T, code int, body []byte, hdr http.Header) (string, string) {
	t.Helper()
	var e struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body is not JSON: %v: %s", err, body)
	}
	if e.Type == "" || e.Message == "" || hdr.Get("X-Amzn-Errortype") != e.Type || hdr.Get("X-Amzn-Requestid") == "" {
		t.Fatalf("incomplete error: %s hdr=%v", body, hdr)
	}
	if !strings.HasPrefix(hdr.Get("Content-Type"), "application/x-amz-json-1.1") {
		t.Fatalf("content type %q", hdr.Get("Content-Type"))
	}
	return e.Type, e.Message
}

func TestBadRequestsGetProperJSONErrors(t *testing.T) {
	token := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	cases := []struct {
		name, target, body, wantType string
	}{
		{"other operation", "AWSPriceListService.DescribeServices", `{}`, "UnknownOperationException"},
		{"other service prefix", "AmazonSSM.GetParameter", `{}`, "UnknownOperationException"},
		{"missing target", "", `{}`, "UnknownOperationException"},
		{"bad json", target, `{`, "SerializationException"},
		{"empty body", target, ``, "SerializationException"},
		{"json array", target, `[]`, "SerializationException"},
		{"wrong field type", target, `{"ServiceCode":5}`, "SerializationException"},
		{"max results is string", target, `{"ServiceCode":"AmazonEC2","MaxResults":"5"}`, "SerializationException"},
		{"missing service code", target, `{}`, "InvalidParameterException"},
		{"empty service code", target, `{"ServiceCode":""}`, "InvalidParameterException"},
		{"max results zero", target, `{"ServiceCode":"AmazonEC2","MaxResults":0}`, "InvalidParameterException"},
		{"max results negative", target, `{"ServiceCode":"AmazonEC2","MaxResults":-1}`, "InvalidParameterException"},
		{"max results 101", target, `{"ServiceCode":"AmazonEC2","MaxResults":101}`, "InvalidParameterException"},
		{"format version", target, `{"ServiceCode":"AmazonEC2","FormatVersion":"aws_v2"}`, "InvalidParameterException"},
		{"filter type EQUALS", target, `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"EQUALS","Field":"instanceType","Value":"x"}]}`, "InvalidParameterException"},
		{"filter type ANY_OF", target, `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"ANY_OF","Field":"instanceType","Value":"x"}]}`, "InvalidParameterException"},
		{"filter type missing", target, `{"ServiceCode":"AmazonEC2","Filters":[{"Field":"instanceType","Value":"x"}]}`, "InvalidParameterException"},
		{"filter field missing", target, `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"TERM_MATCH","Value":"x"}]}`, "InvalidParameterException"},
		{"token not base64", target, `{"ServiceCode":"AmazonEC2","NextToken":"!!!"}`, "InvalidNextTokenException"},
		{"token garbage", target, `{"ServiceCode":"AmazonEC2","NextToken":"` + token("hello") + `"}`, "InvalidNextTokenException"},
		{"token offset out of range", target, `{"ServiceCode":"AmazonEC2","NextToken":"` + token("pf1:9999:0") + `"}`, "InvalidNextTokenException"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body, hdr := post(t, New(), c.target, c.body)
			if code != 400 {
				t.Fatalf("status %d: %s", code, body)
			}
			if typ, _ := errorOf(t, code, body, hdr); typ != c.wantType {
				t.Fatalf("__type %q, want %q", typ, c.wantType)
			}
		})
	}
}

func TestTokenFromADifferentQueryIsRejected(t *testing.T) {
	h := New()
	_, out, _ := post(t, h, target, `{"ServiceCode":"AmazonEC2","MaxResults":2}`)
	tok := decode(t, out).NextToken
	if tok == "" {
		t.Fatal("expected a token")
	}
	code, body, hdr := post(t, h, target, `{"ServiceCode":"AmazonEC2","MaxResults":2,"NextToken":`+strconv.Quote(tok)+`,"Filters":[`+f("instanceType", "c5.large")+`]}`)
	if code != 400 {
		t.Fatalf("status %d: %s", code, body)
	}
	if typ, _ := errorOf(t, code, body, hdr); typ != "InvalidNextTokenException" {
		t.Fatalf("__type %q", typ)
	}
}

func TestMethodContentTypeAndBodySize(t *testing.T) {
	h := New()
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/", nil))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
			t.Errorf("%s: %d allow=%q", m, rec.Code, rec.Header().Get("Allow"))
		}
		errorOf(t, rec.Code, rec.Body.Bytes(), rec.Header())
	}
	// Any Content-Type is tolerated.
	for _, ct := range []string{"", "application/json", "text/plain", "application/x-amz-json-1.0"} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(req(karpenterFilters)))
		r.Header.Set("X-Amz-Target", target)
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 200 {
			t.Errorf("content-type %q: %d", ct, rec.Code)
		}
	}
	// A body over 1 MiB is refused, not buffered without bound.
	big := `{"ServiceCode":"AmazonEC2","Filters":[],"Pad":"` + strings.Repeat("a", 1<<20) + `"}`
	code, body, hdr := post(t, h, target, big)
	if code != http.StatusBadRequest {
		t.Fatalf("oversize: %d", code)
	}
	errorOf(t, code, body, hdr)
}

func TestUnknownRequestFieldsAreIgnored(t *testing.T) {
	if n := count(t, `{"ServiceCode":"AmazonEC2","Extra":{"a":1},"MaxResults":null,"NextToken":null}`); n != totalProducts {
		t.Fatalf("got %d", n)
	}
}
