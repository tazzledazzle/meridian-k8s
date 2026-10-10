package pricingfacade

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func fuzz2Assert(t *testing.T, code int, out []byte, hdr http.Header) {
	t.Helper()
	if code != 200 && code != 400 && code != 405 {
		t.Fatalf("status %d", code)
	}
	if !json.Valid(out) {
		t.Fatalf("invalid JSON: %q", out)
	}
	if !strings.HasPrefix(hdr.Get("Content-Type"), "application/x-amz-json-1.1") {
		t.Fatalf("content-type %q", hdr.Get("Content-Type"))
	}
	if code != 200 && (hdr.Get("X-Amzn-Errortype") == "" || advSDKErrorCode(hdr, out) == "") {
		t.Fatalf("error without readable code: %q", out)
	}
}

// FuzzRawRequest fuzzes the body, the target and a free header.
func FuzzRawRequest(f *testing.F) {
	f.Add(`{"ServiceCode":"AmazonEC2"}`, target, "gzip")
	f.Add(`{"ServiceCode":"AmazonEC2","MaxResults":3}`, "x", "")
	f.Add("\x00\xff", "", "100-continue")
	h := New()
	f.Fuzz(func(t *testing.T, body, tgt, hv string) {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("X-Amz-Target", tgt)
		r.Header.Set("Content-Encoding", hv)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		fuzz2Assert(t, w.Code, w.Body.Bytes(), w.Header())
	})
}

// FuzzTokenMutation mutates a genuine token with byte edits and replays it against the same and other queries.
func FuzzTokenMutation(f *testing.F) {
	h := New()
	_, out, _ := post(f, h, target, `{"ServiceCode":"AmazonEC2","MaxResults":3}`)
	tok := decode(f, out).NextToken
	f.Add(tok, 0, byte(1), 3)
	f.Add(tok, 5, byte(0xff), 1)
	f.Fuzz(func(t *testing.T, tk string, pos int, x byte, size int) {
		b := []byte(tk)
		if len(b) > 0 {
			if pos < 0 {
				pos = -pos
			}
			if pos < 0 {
				pos = 0
			}
			b[pos%len(b)] ^= x
		}
		if size < 1 || size > 100 {
			size = 3
		}
		body := `{"ServiceCode":"AmazonEC2","MaxResults":` + strconv.Itoa(size) + `,"NextToken":` + strconv.Quote(string(b)) + `}`
		code, out, hdr := post(t, h, target, body)
		fuzz2Assert(t, code, out, hdr)
		if code == 200 {
			// an accepted token must decode to a canonical in-range offset for this exact query
			raw, err := base64.RawURLEncoding.DecodeString(string(b))
			if err != nil || !strings.HasPrefix(string(raw), "pf1:") {
				t.Fatalf("accepted a non-canonical token %q", b)
			}
			if n := len(decode(t, out).PriceList); n < 1 || n > size {
				t.Fatalf("page size %d (max %d)", n, size)
			}
		}
	})
}

// FuzzFilterValues feeds arbitrary filter fields and values; the page must contain only matching products.
func FuzzFilterValues(f *testing.F) {
	f.Add("instanceType", "m5.large")
	f.Add("", "")
	f.Add("tenancy", "Shared")
	f.Add("\x00", "\xff")
	h := New()
	f.Fuzz(func(t *testing.T, field, value string) {
		fb, _ := json.Marshal(field)
		vb, _ := json.Marshal(value)
		body := `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"TERM_MATCH","Field":` + string(fb) + `,"Value":` + string(vb) + `}]}`
		code, out, hdr := post(t, h, target, body)
		fuzz2Assert(t, code, out, hdr)
		if code != 200 {
			return
		}
		for _, e := range decode(t, out).PriceList {
			var d struct {
				Product struct {
					Attributes map[string]string `json:"attributes"`
				} `json:"product"`
			}
			if err := json.Unmarshal([]byte(e), &d); err != nil {
				t.Fatal(err)
			}
			v, ok := d.Product.Attributes[field]
			if !ok && field != "productFamily" && field != "marketoption" {
				t.Fatalf("field %q matched a product lacking it", field)
			}
			if ok && v != value {
				t.Fatalf("returned %q for %q=%q", v, field, value)
			}
		}
	})
}
