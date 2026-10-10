// Package s0probe is the S0 spike's contract probe. It sends the AWS calls
// Karpenter makes to an endpoint (the aws-gateway in front of Floci, or in
// front of our own simulators) and checks that the answers contain the fields
// Karpenter reads. The pass/fail table it produces says, operation by
// operation, whether the backend is eligible to serve it. A pass is not
// a guarantee: real Karpenter is the final oracle (handoff amendment A4).
package s0probe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tazzledazzle/meridian/internal/awscall"
)

const (
	maxBody        = 16 << 20 // largest response body the probe reads; more is a failure, not a silent truncation
	defaultRetries = 5        // retries after the first attempt; ~30 s of backoff covers a cold-start race (EC2 lags STS)
	defaultBackoff = 500 * time.Millisecond
	maxBackoff     = 8 * time.Second
)

// Config says where to probe and what the seeded environment is called.
type Config struct {
	Endpoint  string // gateway URL
	Region    string
	Access    string // from the environment only; never printed or written to a report
	Secret    string
	Cluster   string // cluster name; subnets and security groups are tagged karpenter.sh/discovery=<Cluster>
	AMIParam  string // SSM parameter holding the AMI ID, the way Karpenter's alias resolves it (read only)
	QueueName string // SQS interruption queue name
	EKSCreate bool   // call eks:CreateCluster before DescribeCluster (false when the cluster already exists)
	Groups    []string
	Wait      time.Duration // how long to poll for asynchronous state (cluster ACTIVE, instance running, message arrival)
	Client    *http.Client
	Now       func() time.Time

	Retries      int           // retries of a transient failure; 0 = default (5), negative = none
	Backoff      time.Duration // first retry delay, doubled each time up to 8 s; 0 = default (500 ms)
	PollInterval time.Duration // delay between polls; 0 = 500 ms
}

func (c *Config) wantGroup(g string) bool {
	if len(c.Groups) == 0 {
		return true
	}
	for _, x := range c.Groups {
		if x == g {
			return true
		}
	}
	return false
}

func (c *Config) retries() int {
	switch {
	case c.Retries < 0:
		return 0
	case c.Retries == 0:
		return defaultRetries
	}
	return c.Retries
}

func (c *Config) backoff() time.Duration {
	if c.Backoff <= 0 {
		return defaultBackoff
	}
	return c.Backoff
}

func (c *Config) pollEvery() time.Duration {
	if c.PollInterval <= 0 {
		return 500 * time.Millisecond
	}
	return c.PollInterval
}

// Result is the outcome of one check.
type Result struct {
	Name     string        `json:"name"`   // service:Operation
	Group    string        `json:"group"`  // ec2, iam, sqs, ssm, sts, pricing, eks
	Status   string        `json:"status"` // pass | fail | skip
	Detail   string        `json:"detail"`
	Duration time.Duration `json:"-"`
}

// Env is what a check can use: the client and values earlier checks produced.
type Env struct {
	Cfg    *Config
	State  map[string]string
	Lists  map[string][]string
	Suffix string
}

// Check is one probe step.
type Check struct {
	Name  string
	Group string
	Needs []string // State keys that earlier checks must have produced; otherwise the check is skipped
	// GatedBy names an earlier check. If that check did not pass, this one is skipped. EC2 uses it so that a
	// failing ec2:CreateFleet skips the rest of the coupled instance-state matrix (amendment A2).
	GatedBy string
	// Cleanup lifts the gate when the gating check launched an instance (State["instance"] is set) even though it
	// failed a later field check: the instance must not be left running and the template must not be left behind.
	Cleanup bool
	Run     func(ctx context.Context, e *Env) (detail string, err error)
}

// Resp is a raw HTTP answer.
type Resp struct {
	Status   int
	Body     []byte
	Attempts int // how many times the request was sent
}

// errNotProbed marks a check that chose not to exercise its operation; Run reports it as a skip, never a pass.
var errNotProbed = errors.New("not probed")

func notProbed(msg string) error { return fmt.Errorf("%w: %s", errNotProbed, msg) }

// readOnly reports whether repeating an operation is harmless. Only these are retried after a plain 500 or a
// connection that died mid-request, because a state-changing call may already have taken effect.
func readOnly(op string) bool {
	for _, p := range []string{"Describe", "Get", "List", "Receive"} {
		if strings.HasPrefix(op, p) {
			return true
		}
	}
	return false
}

// isDialError reports a failure to even connect, which means the request was never processed.
func isDialError(err error) bool {
	var oe *net.OpError
	return errors.As(err, &oe) && oe.Op == "dial"
}

// retryable decides whether an answer (or transport error) is worth another attempt.
func retryable(status int, err error, safe bool) bool {
	if err != nil {
		return safe || isDialError(err)
	}
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true // the backend or the gateway was not ready; the request was not processed
	case http.StatusInternalServerError:
		return safe
	}
	return false
}

// send runs build()+Do with a bounded exponential backoff on transient failures. build is called again for each
// attempt so every attempt carries a fresh signature.
func (e *Env) send(ctx context.Context, op string, build func() (*http.Request, error)) (Resp, error) {
	c := e.Cfg.Client
	if c == nil {
		c = &http.Client{
			Timeout: 30 * time.Second, // one hung call must not stall the whole probe
			// A redirect would resend a signed request somewhere else; treat it as the answer instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	safe := readOnly(op)
	delay := e.Cfg.backoff()
	maxAttempts := e.Cfg.retries() + 1
	var r Resp
	var err error
	for attempt := 1; ; attempt++ {
		var req *http.Request
		if req, err = build(); err != nil {
			return Resp{}, err
		}
		r, err = e.do(ctx, c, req)
		r.Attempts = attempt
		if err == nil && r.Status == http.StatusOK || ctx.Err() != nil || attempt >= maxAttempts || !retryable(r.Status, err, safe) {
			break
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2 // exponential, capped at 8 s (or the configured first delay if that is larger)
		if limit := max(maxBackoff, e.Cfg.backoff()); delay > limit {
			delay = limit
		}
	}
	if err != nil && r.Attempts > 1 {
		err = fmt.Errorf("%w (after %d attempts)", err, r.Attempts)
	}
	return r, err
}

func (e *Env) do(ctx context.Context, c *http.Client, req *http.Request) (Resp, error) {
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return Resp{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return Resp{Status: resp.StatusCode}, fmt.Errorf("reading response: %w", err)
	}
	if len(b) > maxBody {
		return Resp{Status: resp.StatusCode}, fmt.Errorf("response exceeds %d MiB", maxBody>>20)
	}
	return Resp{Status: resp.StatusCode, Body: b}, nil
}

func (e *Env) now() time.Time {
	if e.Cfg.Now != nil {
		return e.Cfg.Now()
	}
	return time.Now()
}

// failure turns a non-200 answer into an error that shows what the backend said.
// The body is redacted before it is cut to 240 bytes, so a secret straddling the cut cannot leak its first characters.
func (e *Env) failure(r Resp) error {
	body := redactor(e.Cfg).Replace(strings.TrimSpace(string(r.Body)))
	if len(body) > 240 {
		cut := 240
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut]
	}
	msg := fmt.Sprintf("HTTP %d: %s", r.Status, body)
	if r.Attempts > 1 {
		msg += fmt.Sprintf(" (after %d attempts)", r.Attempts)
	}
	return errors.New(msg)
}

// Query sends a query-protocol call and returns the parsed XML on HTTP 200.
func (e *Env) Query(ctx context.Context, service, version, action string, params ...string) (*Node, error) {
	r, err := e.send(ctx, action, func() (*http.Request, error) {
		req, _, err := awscall.BuildQuery(e.Cfg.Endpoint, service, e.Cfg.Region, action, version, params, e.Cfg.Access, e.Cfg.Secret, e.now())
		return req, err
	})
	if err != nil {
		return nil, err
	}
	if r.Status != http.StatusOK {
		return nil, e.failure(r)
	}
	n, err := ParseXML(r.Body)
	if err != nil {
		return nil, fmt.Errorf("response is not XML: %w (%.120s)", err, r.Body)
	}
	return n, nil
}

// JSONCall sends a JSON-RPC call and decodes the JSON object on HTTP 200.
func (e *Env) JSONCall(ctx context.Context, service, target, jsonVersion string, body any) (map[string]any, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	_, op, _ := strings.Cut(target, ".")
	r, err := e.send(ctx, op, func() (*http.Request, error) {
		req, _, err := awscall.BuildJSON(e.Cfg.Endpoint, service, e.Cfg.Region, target, jsonVersion, string(b), e.Cfg.Access, e.Cfg.Secret, e.now())
		return req, err
	})
	if err != nil {
		return nil, err
	}
	if r.Status != http.StatusOK {
		return nil, e.failure(r)
	}
	return decodeObject(r.Body)
}

// REST sends a REST-JSON call and decodes the JSON object on HTTP 200 or 201.
func (e *Env) REST(ctx context.Context, service, method, path string, body any) (map[string]any, error) {
	payload := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = string(b)
	}
	op := "Post"
	if method == http.MethodGet {
		op = "Get" // GET is read-only, so it may be retried after a 500
	}
	r, err := e.send(ctx, op, func() (*http.Request, error) {
		req, _, err := awscall.BuildREST(e.Cfg.Endpoint, service, e.Cfg.Region, method, path, payload, e.Cfg.Access, e.Cfg.Secret, e.now())
		return req, err
	})
	if err != nil {
		return nil, err
	}
	if r.Status != http.StatusOK && r.Status != http.StatusCreated {
		return nil, e.failure(r)
	}
	return decodeObject(r.Body)
}

func decodeObject(b []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("response is not a JSON object: %w (%.120s)", err, b)
	}
	return m, nil
}

// Get walks a dotted path through decoded JSON ("cluster.kubernetesNetworkConfig.serviceIpv4Cidr").
func Get(m map[string]any, path string) any {
	var cur any = m
	for _, seg := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[seg]
	}
	return cur
}

// GetString is Get for string values; non-strings and missing paths give "".
func GetString(m map[string]any, path string) string {
	s, _ := Get(m, path).(string)
	return s
}

// poll calls fn every PollInterval until it returns true or Wait passes.
func (e *Env) poll(ctx context.Context, fn func() (bool, error)) error {
	deadline := time.Now().Add(e.Cfg.Wait)
	for {
		ok, err := fn()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s", e.Cfg.Wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(e.Cfg.pollEvery()):
		}
	}
}

// redactor replaces the credentials (raw and URL-escaped) so no backend echo can reach a report.
func redactor(cfg *Config) *strings.Replacer {
	var pairs []string
	for _, s := range []string{cfg.Secret, cfg.Access} { // secret first: it may contain the access key as a substring
		if s != "" {
			pairs = append(pairs, s, "***", url.QueryEscape(s), "***", url.PathEscape(s), "***")
		}
	}
	return strings.NewReplacer(pairs...)
}

// runOne executes a check and turns a panic into a failure, so one bad response shape cannot stop the whole probe.
func runOne(ctx context.Context, c Check, e *Env) (detail string, err error) {
	defer func() {
		if r := recover(); r != nil {
			detail, err = "", fmt.Errorf("panic in check: %v", r)
		}
	}()
	return c.Run(ctx, e)
}

// Run executes the checks in order and returns one result per check.
func Run(ctx context.Context, cfg Config, checks []Check) []Result {
	var sfx [4]byte
	_, _ = rand.Read(sfx[:])
	e := &Env{Cfg: &cfg, State: map[string]string{}, Lists: map[string][]string{}, Suffix: hex.EncodeToString(sfx[:])}
	redact := redactor(&cfg)
	byName := map[string]Result{}
	var out []Result
	for _, c := range checks {
		if !cfg.wantGroup(c.Group) {
			continue
		}
		r := Result{Name: c.Name, Group: c.Group}
		switch {
		case ctx.Err() != nil:
			r.Status, r.Detail = "skip", "interrupted: "+ctx.Err().Error()
		case gateBlocks(c, byName, &r, e):
		default:
			var missing []string
			for _, k := range c.Needs {
				if e.State[k] == "" {
					missing = append(missing, k)
				}
			}
			if len(missing) > 0 {
				r.Status, r.Detail = "skip", "needs "+strings.Join(missing, ", ")+" from an earlier check"
				break
			}
			start := time.Now()
			detail, err := runOne(ctx, c, e)
			r.Duration = time.Since(start)
			if errors.Is(err, errNotProbed) {
				r.Status, r.Detail = "skip", err.Error()
			} else if err != nil {
				r.Status, r.Detail = "fail", err.Error()
			} else {
				r.Status, r.Detail = "pass", detail
			}
		}
		r.Detail = redact.Replace(r.Detail)
		byName[c.Name] = r
		out = append(out, r)
	}
	return out
}

// gateBlocks fills r with a skip result when the check's gate did not pass.
func gateBlocks(c Check, done map[string]Result, r *Result, e *Env) bool {
	if c.GatedBy == "" || (c.Cleanup && e.State["instance"] != "") {
		return false
	}
	g, ran := done[c.GatedBy]
	if !ran || g.Status == "pass" {
		return false
	}
	r.Status = "skip"
	if g.Status == "fail" {
		r.Detail = c.GatedBy + " failed, so this coupled check was not run: the backend is not eligible for the coupled group and a fallback is needed"
	} else {
		r.Detail = c.GatedBy + " did not run (a prerequisite failed), so this coupled check was not run"
	}
	return true
}
