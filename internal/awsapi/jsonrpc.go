package awsapi

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strings"
)

// TargetOp extracts the operation name from the X-Amz-Target header
// ("AWSPriceListService.GetProducts" -> "GetProducts").
func TargetOp(r *http.Request) (string, bool) {
	t := r.Header.Get("X-Amz-Target")
	if t == "" {
		return "", false
	}
	if i := strings.LastIndex(t, "."); i >= 0 {
		return t[i+1:], true
	}
	return t, true
}

// WriteJSON writes a JSON body with the given content type (JSON-RPC services use
// application/x-amz-json-1.0 or 1.1; REST-JSON uses application/json).
func WriteJSON(w http.ResponseWriter, status int, contentType string, v any) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Amzn-Requestid", NewRequestID())
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteXML writes an XML body, as query-protocol services do.
func WriteXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "text/xml;charset=UTF-8")
	w.Header().Set("X-Amzn-Requestid", NewRequestID())
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(v)
}
