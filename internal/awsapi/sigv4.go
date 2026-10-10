package awsapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Sentinel errors let the gateway map each failure to the AWS error code the
// real service would return. None of them embed request data or secrets.
var (
	ErrMissingAuth       = errors.New("missing Authorization header")             // MissingAuthenticationToken
	ErrInvalidAuth       = errors.New("malformed Authorization header")           // IncompleteSignature
	ErrUnknownKey        = errors.New("unknown access key")                       // InvalidClientTokenId
	ErrSignatureMismatch = errors.New("signature does not match")                 // SignatureDoesNotMatch
	ErrRequestExpired    = errors.New("request timestamp outside allowed window") // RequestExpired
)

// Auth is the parsed credential scope of a verified request.
type Auth struct {
	AccessKey     string
	Date          string // yyyymmdd
	Region        string
	Service       string // the sigv4 signing name, e.g. "ec2"; the gateway routes on this
	SignedHeaders []string
	Signature     string
}

// SecretLookup returns the secret for an access key.
type SecretLookup func(accessKey string) (secret string, ok bool)

// Credential fields are restricted to the characters real AWS names use, with
// length bounds, so hostile values never reach the string-to-sign.
var authRe = regexp.MustCompile(`^AWS4-HMAC-SHA256\s+Credential=([A-Za-z0-9_.@+=-]{1,128})/(\d{8})/([a-z0-9-]{1,64})/([a-z0-9-]{1,64})/aws4_request,\s*SignedHeaders=([^,\s]+),\s*Signature=([0-9a-f]{64})$`)

const amzDateFormat = "20060102T150405Z"

// maxAuthHeader bounds the Authorization header we are willing to parse.
const maxAuthHeader = 4096

// parseAuth parses an Authorization header value.
func parseAuth(h string) (Auth, error) {
	if len(h) > maxAuthHeader {
		return Auth{}, ErrInvalidAuth
	}
	m := authRe.FindStringSubmatch(strings.TrimSpace(h))
	if m == nil {
		return Auth{}, ErrInvalidAuth
	}
	return Auth{AccessKey: m[1], Date: m[2], Region: m[3], Service: m[4],
		SignedHeaders: strings.Split(m[5], ";"), Signature: m[6]}, nil
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// uriEncode percent-encodes per the SigV4 rules: unreserved characters stay,
// everything else becomes %XX (uppercase); '/' is kept only when encodeSlash is false.
func uriEncode(s string, encodeSlash bool) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			sb.WriteByte(c)
		case c == '/' && !encodeSlash:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

// canonicalQuery sorts query parameters by key then value and encodes them.
// It fails on a query Go cannot fully parse (e.g. a ';' or bad %-escape),
// because url.Query() would silently drop such pairs from the signature.
func canonicalQuery(r *http.Request) (string, error) {
	type kv struct{ k, v string }
	var pairs []kv
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", ErrInvalidAuth
	}
	for k, vals := range q {
		for _, v := range vals {
			pairs = append(pairs, kv{uriEncode(k, true), uriEncode(v, true)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&"), nil
}

// canonicalRequest builds the string that is hashed and signed.
func canonicalRequest(r *http.Request, body []byte, signedHeaders []string, service string) (string, error) {
	query, err := canonicalQuery(r)
	if err != nil {
		return "", err
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	// Every service except S3 double-encodes the path.
	if service != "s3" {
		path = uriEncode(path, false)
	}
	var hdr strings.Builder
	for _, name := range signedHeaders {
		var val string
		if name == "host" {
			val = r.Host
			if val == "" {
				val = r.URL.Host
			}
		} else {
			val = strings.Join(r.Header.Values(name), ",")
		}
		// Header values are trimmed and runs of whitespace collapsed to one space.
		hdr.WriteString(name + ":" + strings.Join(strings.Fields(val), " ") + "\n")
	}
	return strings.Join([]string{
		r.Method, path, query, hdr.String(), strings.Join(signedHeaders, ";"), hexSHA256(body),
	}, "\n"), nil
}

// signature computes the hex signature for a request and credential scope.
func signature(r *http.Request, body []byte, a Auth, secret string, t time.Time) (string, error) {
	scope := fmt.Sprintf("%s/%s/%s/aws4_request", a.Date, a.Region, a.Service)
	canon, err := canonicalRequest(r, body, a.SignedHeaders, a.Service)
	if err != nil {
		return "", err
	}
	toSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", t.UTC().Format(amzDateFormat), scope, hexSHA256([]byte(canon)),
	}, "\n")
	k := hmacSHA256([]byte("AWS4"+secret), a.Date)
	k = hmacSHA256(k, a.Region)
	k = hmacSHA256(k, a.Service)
	k = hmacSHA256(k, "aws4_request")
	return hex.EncodeToString(hmacSHA256(k, toSign)), nil
}

// maxSignedHeaders bounds the work an attacker-chosen SignedHeaders list can cause.
const maxSignedHeaders = 64

// checkSignedHeaders enforces SigV4's rules on the SignedHeaders list: lowercase,
// strictly sorted (so no duplicates), host and x-amz-date present, and every
// named header actually on the request. Without this a client could sign too
// little (leaving host or the timestamp unauthenticated) or sign phantom headers.
func checkSignedHeaders(r *http.Request, names []string) error {
	if len(names) == 0 || len(names) > maxSignedHeaders {
		return ErrInvalidAuth
	}
	var hasHost, hasDate bool
	for i, n := range names {
		if n == "" || n != strings.ToLower(n) || (i > 0 && names[i-1] >= n) {
			return ErrInvalidAuth
		}
		switch n {
		case "host":
			hasHost = true
		case "x-amz-date":
			hasDate = true
		}
		if n != "host" && len(r.Header.Values(n)) == 0 {
			return ErrInvalidAuth
		}
	}
	if !hasHost || !hasDate {
		return ErrInvalidAuth
	}
	return nil
}

// Verify checks a request's SigV4 signature against the secret for its access
// key. It never reads r.Body; the caller passes the already-read body.
func Verify(r *http.Request, body []byte, lookup SecretLookup, now time.Time, skew time.Duration) (Auth, error) {
	// A repeated Authorization or X-Amz-Date is ambiguous; refuse rather than pick one.
	if len(r.Header.Values("Authorization")) > 1 || len(r.Header.Values("X-Amz-Date")) > 1 {
		return Auth{}, ErrInvalidAuth
	}
	h := r.Header.Get("Authorization")
	if h == "" {
		return Auth{}, ErrMissingAuth
	}
	a, err := parseAuth(h)
	if err != nil {
		return Auth{}, err
	}
	if err := checkSignedHeaders(r, a.SignedHeaders); err != nil {
		return Auth{}, err
	}
	t, err := time.Parse(amzDateFormat, r.Header.Get("X-Amz-Date"))
	if err != nil {
		return Auth{}, ErrInvalidAuth
	}
	// The credential scope date must be the day of the signing timestamp.
	if t.UTC().Format("20060102") != a.Date {
		return Auth{}, ErrInvalidAuth
	}
	// Reject anything outside the allowed clock-skew window (past or future).
	if d := now.Sub(t); d > skew || d < -skew {
		return Auth{}, ErrRequestExpired
	}
	// Fail closed: no lookup function, an unknown key, or an empty secret all mean "no such key".
	if lookup == nil {
		return Auth{}, ErrUnknownKey
	}
	secret, ok := lookup(a.AccessKey)
	if !ok || secret == "" {
		return Auth{}, ErrUnknownKey
	}
	// hmac.Equal compares in constant time, so timing leaks nothing about the signature.
	want, err := signature(r, body, a, secret, t)
	if err != nil {
		return Auth{}, err
	}
	if !hmac.Equal([]byte(want), []byte(a.Signature)) {
		return Auth{}, ErrSignatureMismatch
	}
	return a, nil
}

// Sign signs a request in place (sets X-Amz-Date and Authorization). It exists
// for tests and test clients; production traffic is signed by the AWS SDKs.
func Sign(r *http.Request, body []byte, accessKey, secret, region, service string, t time.Time) {
	t = t.UTC()
	r.Header.Set("X-Amz-Date", t.Format(amzDateFormat))
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	r.Host = host
	signedNames := []string{"host", "x-amz-date"}
	if r.Header.Get("Content-Type") != "" {
		signedNames = []string{"content-type", "host", "x-amz-date"}
	}
	a := Auth{AccessKey: accessKey, Date: t.Format("20060102"), Region: region, Service: service, SignedHeaders: signedNames}
	sig, err := signature(r, body, a, secret, t)
	if err != nil {
		// Fail closed: a request we cannot canonicalise is left unsigned.
		r.Header.Del("Authorization")
		return
	}
	a.Signature = sig
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s/%s/%s/aws4_request, SignedHeaders=%s, Signature=%s",
		accessKey, a.Date, region, service, strings.Join(signedNames, ";"), a.Signature))
}
