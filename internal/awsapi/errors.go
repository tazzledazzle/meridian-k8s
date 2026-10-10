// Package awsapi holds the wire-level helpers shared by every facade: error
// envelopes, query-parameter parsing, JSON-RPC helpers and SigV4 verification.
package awsapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

// Style selects which AWS error envelope to emit. Real SDKs parse each style
// differently, so using the wrong one makes errors unreadable to them.
type Style string

const (
	StyleQuery    Style = "query"    // iam, sts: <ErrorResponse><Error>
	StyleEC2      Style = "ec2"      // ec2: <Response><Errors><Error>
	StyleJSON     Style = "json"     // pricing, ssm, sqs: {"message": ...} + X-Amzn-Errortype
	StyleRESTJSON Style = "restjson" // eks: same body, application/json
)

// NewRequestID returns a random UUID-shaped request id.
func NewRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand does not fail on supported platforms
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// esc XML-escapes a string for embedding in a hand-built XML body.
func esc(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	return sb.String()
}

// WriteError writes an AWS-shaped error response.
func WriteError(w http.ResponseWriter, s Style, status int, code, message string) {
	id := NewRequestID()
	w.Header().Set("X-Amzn-Requestid", id)
	switch s {
	case StyleEC2:
		w.Header().Set("Content-Type", "text/xml;charset=UTF-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, "<Response><Errors><Error><Code>%s</Code><Message>%s</Message></Error></Errors><RequestID>%s</RequestID></Response>",
			esc(code), esc(message), id)
	case StyleQuery:
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(status)
		fmt.Fprintf(w, "<ErrorResponse><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>",
			esc(code), esc(message), id)
	default: // StyleJSON, StyleRESTJSON
		ct := "application/json"
		if s == StyleJSON {
			ct = "application/x-amz-json-1.1"
		}
		w.Header().Set("Content-Type", ct)
		// The SDKs read the error code from this header before looking at the body.
		w.Header().Set("X-Amzn-Errortype", code)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
	}
}
