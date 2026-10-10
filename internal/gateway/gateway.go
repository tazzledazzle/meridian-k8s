package gateway

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/ops"
)

// Config wires the gateway. Each operation in ops.yaml names its backend, so one
// service can be served by several backends (for example most EC2 operations by
// Floci and a few by ec2-sim).
type Config struct {
	Ops      ops.List
	Lookup   awsapi.SecretLookup
	Now      func() time.Time        // nil means time.Now
	Skew     time.Duration           // zero means 15 minutes
	Backends map[string]http.Handler // keyed by backend name (the ops.yaml "backend" value)
	// TraceRoutes maps a SigV4 service name to the backend that receives
	// operations missing from ops.yaml, in trace mode only.
	TraceRoutes map[string]string
	// TraceMode forwards operations missing from ops.yaml instead of rejecting
	// them, so the Karpenter trace can discover the real call surface.
	// Authentication is still enforced in this mode.
	TraceMode bool
	// OnCall is told about every authenticated call (the trace recorder uses it).
	OnCall func(service, op string, supported bool)
	Logf   func(format string, args ...any)
}

const maxBody = 1 << 20 // 1 MiB is far more than any call Karpenter makes

// styleFor picks the error envelope a service's SDK expects.
func styleFor(service string) awsapi.Style {
	switch service {
	case "ec2":
		return awsapi.StyleEC2
	case "iam", "sts":
		return awsapi.StyleQuery
	case "eks":
		return awsapi.StyleRESTJSON
	default: // pricing, ssm, sqs
		return awsapi.StyleJSON
	}
}

// authError maps a verification failure to the AWS error code the real service returns.
func authError(err error) (code string) {
	switch {
	case errors.Is(err, awsapi.ErrMissingAuth):
		return "MissingAuthenticationToken"
	case errors.Is(err, awsapi.ErrInvalidAuth):
		return "IncompleteSignature"
	case errors.Is(err, awsapi.ErrUnknownKey):
		return "InvalidClientTokenId"
	case errors.Is(err, awsapi.ErrRequestExpired):
		return "RequestExpired"
	default:
		return "SignatureDoesNotMatch"
	}
}

// protocolOf is the wire protocol the gateway accepts for each SigV4 service.
// The operation is read from exactly one place per protocol, so the operation
// the gateway checks is the one the backend executes.
var protocolOf = map[string]string{
	"ec2": "query", "iam": "query", "sts": "query",
	"sqs": "json", "ssm": "json", "pricing": "json",
	"eks": "rest",
}

// targetPrefix is the X-Amz-Target prefix each JSON service uses.
var targetPrefix = map[string]string{"sqs": "AmazonSQS", "ssm": "AmazonSSM", "pricing": "AWSPriceListService"}

var (
	opNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)                      // bounded, log- and XML-safe
	targetRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}\.[A-Za-z][A-Za-z0-9_]{0,127}$`) // exactly Prefix.Operation
	eksPath  = regexp.MustCompile(`^/clusters/([A-Za-z0-9][A-Za-z0-9_-]{0,99})$`)       // GET /clusters/{name}
)

// operation identifies the AWS operation of a request, or reports why it cannot.
// Anything ambiguous is refused: repeated or conflicting selectors could make
// the gateway and the backend disagree about which operation is being run.
func operation(r *http.Request, body []byte, service string) (string, error) {
	switch protocolOf[service] {
	case "json": // JSON-RPC: only X-Amz-Target, only POST
		targets := r.Header.Values("X-Amz-Target")
		if r.Method != http.MethodPost || len(targets) != 1 || !targetRE.MatchString(targets[0]) {
			return "", errors.New("expected a POST with exactly one X-Amz-Target of the form Service.Operation")
		}
		// The target prefix must match the credential-scope service, so an sqs-scoped request cannot name an ssm operation.
		if prefix, _, _ := strings.Cut(targets[0], "."); prefix != targetPrefix[service] {
			return "", errors.New("X-Amz-Target prefix does not match the signed service")
		}
		op, _ := awsapi.TargetOp(r)
		return op, nil
	case "rest": // REST-JSON (eks): the operation is implied by method and path
		if m := eksPath.FindStringSubmatch(r.URL.Path); r.Method == http.MethodGet && m != nil {
			return "DescribeCluster", nil
		}
		if r.Method == http.MethodPost && r.URL.Path == "/clusters" { // seeding creates the cluster
			return "CreateCluster", nil
		}
		return "", errors.New("unrecognised request path")
	case "query": // query protocol: only the Action parameter
		if len(r.Header.Values("X-Amz-Target")) > 0 {
			return "", errors.New("X-Amz-Target is not valid for this service")
		}
		fromBody, err := url.ParseQuery(string(body))
		if err != nil {
			return "", errors.New("malformed request body")
		}
		actions := append(fromBody["Action"], r.URL.Query()["Action"]...)
		if len(actions) != 1 { // none, repeated, or present in both URL and body
			return "", errors.New("expected exactly one Action parameter")
		}
		return actions[0], nil
	}
	return "", errors.New("service not handled by this gateway")
}

// MissingBackends lists backend names that ops.yaml routes to but that are not
// configured, so a typo or a forgotten flag stops startup instead of causing 503s later.
func (c Config) MissingBackends() []string {
	var missing []string
	for _, name := range c.Ops.Backends() {
		if _, ok := c.Backends[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// New builds the gateway handler.
func New(cfg Config) http.Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Skew == 0 {
		cfg.Skew = 15 * time.Minute
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil { // handlers called directly may have no body
			r.Body = http.NoBody
		}
		body, err := awsapi.ReadBody(r, maxBody)
		if err != nil {
			// Fixed messages only: a raw read error can carry internal addresses, and this runs before authentication.
			if strings.Contains(err.Error(), "exceeds") {
				awsapi.WriteError(w, awsapi.StyleJSON, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", "request body is too large")
				return
			}
			awsapi.WriteError(w, awsapi.StyleJSON, http.StatusBadRequest, "IncompleteBody", "the request body could not be read")
			return
		}
		// Authenticate first: nothing about operations or backends is revealed to unauthenticated callers.
		auth, err := awsapi.Verify(r, body, cfg.Lookup, cfg.Now(), cfg.Skew)
		if err != nil {
			// The service is unknown until the credential scope is trusted, so auth errors use the JSON envelope.
			awsapi.WriteError(w, awsapi.StyleJSON, http.StatusForbidden, authError(err), err.Error())
			return
		}
		// ReverseProxy removes every header named in Connection, so a client could make the backend lose the signed
		// Authorization. Drop connection-control headers after verification so the backend sees what was verified.
		r.Header.Del("Connection")
		r.Header.Del("Proxy-Connection")
		r.Header.Del("Keep-Alive")
		style := styleFor(auth.Service)
		if _, known := protocolOf[auth.Service]; !known {
			cfg.Logf("request for unsupported service %q", auth.Service)
			awsapi.WriteError(w, style, http.StatusBadRequest, "UnsupportedOperation", "service is not supported by this gateway")
			return
		}
		op, err := operation(r, body, auth.Service)
		if err != nil || !opNameRE.MatchString(op) {
			// The attacker-chosen name is never echoed or logged.
			awsapi.WriteError(w, style, http.StatusBadRequest, "InvalidAction", "could not determine a single valid operation")
			return
		}
		entry, supported := cfg.Ops.Find(auth.Service, op)
		if cfg.OnCall != nil {
			cfg.OnCall(auth.Service, op, supported)
		}
		if !supported && !cfg.TraceMode {
			cfg.Logf("unsupported operation %s:%s", auth.Service, op)
			awsapi.WriteError(w, style, http.StatusBadRequest, "UnsupportedOperation", auth.Service+":"+op+" is not in ops.yaml")
			return
		}
		name := entry.Backend
		if !supported { // trace mode: forward to the service's trace route
			name = cfg.TraceRoutes[auth.Service]
		}
		backend, ok := cfg.Backends[name]
		if !ok || backend == nil { // a nil handler counts as not configured
			awsapi.WriteError(w, style, http.StatusServiceUnavailable, "ServiceUnavailable",
				"no backend "+strconv.Quote(name)+" configured for "+auth.Service+":"+op)
			return
		}
		serveBackend(w, r, backend, style, auth.Service+":"+op, cfg.Logf)
	})
}

// serveBackend calls the backend and turns a panic into an AWS-shaped 500 that
// carries no panic detail. http.ErrAbortHandler is net/http's deliberate abort
// signal and keeps propagating.
func serveBackend(w http.ResponseWriter, r *http.Request, h http.Handler, style awsapi.Style, what string, logf func(string, ...any)) {
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			logf("backend panic serving %s", what) // the panic value itself may hold request data, so it is not logged
			awsapi.WriteError(w, style, http.StatusInternalServerError, "InternalFailure", "the backend failed")
		}
	}()
	h.ServeHTTP(w, r)
}
