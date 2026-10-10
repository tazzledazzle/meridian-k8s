package awsapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTargetOpExtractsOperationName(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Amz-Target", "AWSPriceListService.GetProducts")
	if op, ok := TargetOp(r); !ok || op != "GetProducts" {
		t.Fatalf("op=%q ok=%v", op, ok)
	}
	if _, ok := TargetOp(httptest.NewRequest(http.MethodPost, "/", nil)); ok {
		t.Fatal("no target header should report ok=false")
	}
}

func TestWriteJSONAndWriteXMLSetContentTypes(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, 200, "application/x-amz-json-1.1", map[string]string{"a": "b"})
	if rec.Header().Get("Content-Type") != "application/x-amz-json-1.1" || rec.Body.String() != "{\"a\":\"b\"}\n" {
		t.Fatalf("json: ct=%q body=%q", rec.Header().Get("Content-Type"), rec.Body)
	}
	type resp struct {
		XMLName struct{} `xml:"Ping"`
		V       string   `xml:"v"`
	}
	rec = httptest.NewRecorder()
	WriteXML(rec, 200, resp{V: "x"})
	if rec.Header().Get("Content-Type") != "text/xml;charset=UTF-8" || rec.Body.String() != "<Ping><v>x</v></Ping>" {
		t.Fatalf("xml: ct=%q body=%q", rec.Header().Get("Content-Type"), rec.Body)
	}
}
