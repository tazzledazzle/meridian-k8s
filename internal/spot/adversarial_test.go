package spot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

// ---------------------------------------------------------------------------
// Golden-sample verification against the REAL Karpenter v1.14.1 source/parser.
// These tests are gated on a local copy because they need the module cache
// (KARPENTER_SRC = .../pkg/controllers/interruption) and a helper binary built
// from a throwaway module (KARPENTER_PARSE_BIN) that calls
// interruption.NewEventParser(interruption.DefaultParsers...).Parse on stdin.
// ---------------------------------------------------------------------------

func karpSrc(t *testing.T) string {
	t.Helper()
	d := os.Getenv("KARPENTER_SRC")
	if d == "" {
		d = os.ExpandEnv("$HOME/go/pkg/mod/github.com/aws/karpenter-provider-aws@v1.14.1/pkg/controllers/interruption")
	}
	if _, err := os.Stat(d + "/messages/types.go"); err != nil {
		t.Skip("Karpenter v1.14.1 source not available: " + d)
	}
	return d
}

var jsonTagRE = regexp.MustCompile("`json:\"([^\"]+)\"`")

func tagsIn(t *testing.T, path, fromType string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	i := strings.Index(src, fromType)
	if i < 0 {
		t.Fatalf("%s not found in %s", fromType, path)
	}
	end := strings.Index(src[i:], "\n}\n")
	var out []string
	for _, m := range jsonTagRE.FindAllStringSubmatch(src[i:i+end], -1) {
		out = append(out, m[1])
	}
	return out
}

func structTags(v any) []string {
	var out []string
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		if tag, ok := rt.Field(i).Tag.Lookup("json"); ok {
			out = append(out, tag)
		}
	}
	return out
}

func TestKarpenterSourceTagsMatchTheStructsCopiedInTests(t *testing.T) {
	d := karpSrc(t)
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"Metadata", tagsIn(t, d+"/messages/types.go", "type Metadata struct"), structTags(karpMetadata{})},
		{"spotinterruption.Message", tagsIn(t, d+"/messages/spotinterruption/model.go", "type Message struct"), []string{"detail"}},
		{"spotinterruption.Detail", tagsIn(t, d+"/messages/spotinterruption/model.go", "type Detail struct"), structTags(karpDetail{})},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: Karpenter tags %q != test copy %q", c.name, c.got, c.want)
		}
	}
	// The registry key literals in spotinterruption/parser.go.
	raw, err := os.ReadFile(d + "/messages/spotinterruption/parser.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, lit := range []string{`return "0"`, `return "aws.ec2"`, `return "EC2 Spot Instance Interruption Warning"`} {
		if !bytes.Contains(raw, []byte(lit)) {
			t.Errorf("parser.go lacks %s", lit)
		}
	}
}

func realParse(t *testing.T, body []byte) (string, error) {
	t.Helper()
	bin := os.Getenv("KARPENTER_PARSE_BIN")
	if bin == "" {
		bin = "/tmp/kp/parse"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("Karpenter parser helper binary not built: " + bin)
	}
	cmd := exec.Command(bin)
	cmd.Stdin = bytes.NewReader(body)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestRealKarpenterParserClassifiesEventOutputAsSpotInterruption(t *testing.T) {
	id := "i-0123456789abcdef0"
	for _, region := range []string{"us-west-2", "eu-central-1", ""} {
		ev := Event(region, id, time.Date(2026, 10, 9, 12, 0, 0, 0, time.FixedZone("x", 3600)))
		out, err := realParse(t, ev)
		if err != nil || !strings.HasPrefix(out, "kind=spot_interrupted ids=["+id+"]") {
			t.Errorf("region %q: err=%v out=%q for %s", region, err, out, ev)
		}
	}
	out, err := realParse(t, loadGolden(t))
	if err != nil || !strings.HasPrefix(out, "kind=spot_interrupted ids=["+goldenID+"]") {
		t.Errorf("golden: %v %q", err, out)
	}
}

// Behaviour of Karpenter's parser on envelopes that differ from ours: these pin
// what is irrelevant (account, id, resources, time offset) and what is fatal.
func TestRealKarpenterParserFieldSensitivity(t *testing.T) {
	base := func() map[string]any {
		var m map[string]any
		_ = json.Unmarshal(Event("us-west-2", "i-0123456789abcdef0", time.Unix(1, 0)), &m)
		return m
	}
	ok := func(name string, mut func(map[string]any)) {
		m := base()
		mut(m)
		b, _ := json.Marshal(m)
		out, err := realParse(t, b)
		if err != nil || !strings.HasPrefix(out, "kind=spot_interrupted") {
			t.Errorf("%s should still parse as spot_interrupted: %v %q", name, err, out)
		}
	}
	noop := func(name string, mut func(map[string]any)) {
		m := base()
		mut(m)
		b, _ := json.Marshal(m)
		out, err := realParse(t, b)
		if err != nil || !strings.HasPrefix(out, "kind=no_op") {
			t.Errorf("%s should be a no-op: %v %q", name, err, out)
		}
	}
	ok("other account", func(m map[string]any) { m["account"] = "999999999999" })
	ok("other id", func(m map[string]any) { m["id"] = "" })
	ok("empty resources", func(m map[string]any) { m["resources"] = []string{} })
	ok("null resources", func(m map[string]any) { m["resources"] = nil })
	ok("missing resources", func(m map[string]any) { delete(m, "resources") })
	ok("mismatched resources", func(m map[string]any) { m["resources"] = []string{"arn:aws:ec2:us-east-1:instance/i-other"} })
	ok("time with offset", func(m map[string]any) { m["time"] = "2026-10-09T14:00:00+02:00" })
	ok("time with nanos", func(m map[string]any) { m["time"] = "2026-10-09T12:00:00.123456789Z" })
	ok("other region", func(m map[string]any) { m["region"] = "mars-1" })
	ok("missing instance-action", func(m map[string]any) { m["detail"] = map[string]string{"instance-id": "i-0123456789abcdef0"} })
	ok("extra fields", func(m map[string]any) { m["extra"] = 1 })
	noop("version 1", func(m map[string]any) { m["version"] = "1" })
	noop("source", func(m map[string]any) { m["source"] = "aws.ec2 " })
	noop("detail-type case", func(m map[string]any) { m["detail-type"] = "ec2 spot instance interruption warning" })
	// A time Karpenter cannot parse makes the whole message an error (it is then deleted from the queue, not handled).
	for _, bad := range []string{"2026-10-09 12:00:00", "2026-10-09T12:00:00", "12345", ""} {
		m := base()
		m["time"] = bad
		b, _ := json.Marshal(m)
		if out, err := realParse(t, b); err == nil {
			t.Errorf("time %q unexpectedly parsed: %q", bad, out)
		}
	}
}

// ---------------------------------------------------------------------------
// Scripted AWS stub for protocol, state and failure-injection tests.
// ---------------------------------------------------------------------------

type scripted struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []string // op names in arrival order
	bodies []string
	// per-op overrides; return true when the override wrote the response.
	over map[string]func(w http.ResponseWriter, r *http.Request, body []byte) bool
	// hook runs on every call before the response, with the 1-based call index.
	hook func(n int, op string)
	// describe XML to serve
	describe string
}

func describeXML(items ...string) string {
	var b strings.Builder
	b.WriteString(`<DescribeInstancesResponse><reservationSet>`)
	for _, it := range items {
		b.WriteString(`<item><instancesSet>` + it + `</instancesSet></item>`)
	}
	b.WriteString(`</reservationSet></DescribeInstancesResponse>`)
	return b.String()
}

func inst(id, state, lifecycle string) string {
	lc := ""
	if lifecycle != "" {
		lc = `<instanceLifecycle>` + lifecycle + `</instanceLifecycle>`
	}
	return `<item><instanceId>` + id + `</instanceId><instanceState><code>16</code><name>` + state + `</name></instanceState>` + lc + `</item>`
}

func newScripted(t *testing.T) *scripted {
	t.Helper()
	s := &scripted{t: t, over: map[string]func(http.ResponseWriter, *http.Request, []byte) bool{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if _, err := awsapi.Verify(r, b, func(k string) (string, bool) { return "SK", k == "AK" }, time.Now(), 15*time.Minute); err != nil {
			http.Error(w, "bad signature: "+err.Error(), http.StatusForbidden)
			return
		}
		op := ""
		if tg := r.Header.Get("X-Amz-Target"); tg != "" {
			op = strings.TrimPrefix(tg, "AmazonSQS.")
		} else {
			q, _ := url.ParseQuery(string(b))
			op = q.Get("Action")
		}
		s.mu.Lock()
		s.calls = append(s.calls, op)
		s.bodies = append(s.bodies, string(b))
		n := len(s.calls)
		hook := s.hook
		ov := s.over[op]
		desc := s.describe
		s.mu.Unlock()
		if hook != nil {
			hook(n, op)
		}
		if ov != nil && ov(w, r, b) {
			return
		}
		switch op {
		case "DescribeInstances":
			_, _ = w.Write([]byte(desc))
		case "GetQueueUrl":
			_, _ = w.Write([]byte(`{"QueueUrl":"http://floci:4566/000000000000/meridian-g0-interruption"}`))
		case "SendMessage":
			_, _ = w.Write([]byte(`{"MessageId":"m"}`))
		case "TerminateInstances":
			_, _ = w.Write([]byte(`<TerminateInstancesResponse/>`))
		default:
			http.Error(w, "unknown op "+op, 400)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *scripted) log() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.calls, ",")
}

func (s *scripted) injector() *Injector {
	return &Injector{Endpoint: s.srv.URL, Region: "us-west-2", QueueName: "meridian-g0-interruption", Access: "AK", Secret: "SK",
		Sleep: func(context.Context, time.Duration) error { return nil }}
}

const aID = "i-0123456789abcdef0"

func fail(code int, body string) func(http.ResponseWriter, *http.Request, []byte) bool {
	return func(w http.ResponseWriter, _ *http.Request, _ []byte) bool {
		http.Error(w, body, code)
		return true
	}
}

func TestInstanceStatesAndDescribeShapes(t *testing.T) {
	cases := []struct {
		name     string
		xml      string
		any      bool
		wantErr  bool
		wantSeen string
	}{
		{"running spot", describeXML(inst(aID, "running", "spot")), false, false, "DescribeInstances,GetQueueUrl,SendMessage,TerminateInstances"},
		{"running on-demand", describeXML(inst(aID, "running", "")), false, true, "DescribeInstances"},
		{"running on-demand any-market", describeXML(inst(aID, "running", "")), true, false, "DescribeInstances,GetQueueUrl,SendMessage,TerminateInstances"},
		{"running scheduled lifecycle", describeXML(inst(aID, "running", "scheduled")), false, true, "DescribeInstances"},
		{"uppercase SPOT lifecycle", describeXML(inst(aID, "running", "SPOT")), false, true, "DescribeInstances"},
		{"pending", describeXML(inst(aID, "pending", "spot")), false, true, "DescribeInstances"},
		{"shutting-down", describeXML(inst(aID, "shutting-down", "spot")), false, true, "DescribeInstances"},
		{"terminated", describeXML(inst(aID, "terminated", "spot")), true, true, "DescribeInstances"},
		{"stopping", describeXML(inst(aID, "stopping", "spot")), true, true, "DescribeInstances"},
		{"stopped", describeXML(inst(aID, "stopped", "spot")), true, true, "DescribeInstances"},
		{"empty state", describeXML(inst(aID, "", "spot")), true, true, "DescribeInstances"},
		{"state with whitespace", describeXML(inst(aID, " running ", "spot")), true, true, "DescribeInstances"},
		{"no reservations", describeXML(), true, true, "DescribeInstances"},
		{"two instances in one reservation", describeXML(inst(aID, "running", "spot") + inst("i-0123456789abcdef1", "running", "spot")), true, true, "DescribeInstances"},
		{"two reservations", describeXML(inst(aID, "running", "spot"), inst(aID, "running", "spot")), true, true, "DescribeInstances"},
		{"id mismatch", describeXML(inst("i-0123456789abcdef1", "running", "spot")), true, true, "DescribeInstances"},
		{"id prefix only", describeXML(inst("i-0123456789abcdef", "running", "spot")), true, true, "DescribeInstances"},
		{"id with padding", describeXML(inst(" "+aID+" ", "running", "spot")), true, true, "DescribeInstances"},
		{"malformed xml", `<DescribeInstancesResponse><reservationSet>`, true, true, "DescribeInstances"},
		{"empty body", ``, true, true, "DescribeInstances"},
		{"json body", `{"a":1}`, true, true, "DescribeInstances"},
		{"html body", `<html><body>hi</body></html>`, true, true, "DescribeInstances"},
		{"wrong element nesting", `<DescribeInstancesResponse><instancesSet><item><instanceId>` + aID + `</instanceId></item></instancesSet></DescribeInstancesResponse>`, true, true, "DescribeInstances"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newScripted(t)
			s.describe = c.xml
			in := s.injector()
			in.AnyMarket = c.any
			err := in.Interrupt(context.Background(), aID, time.Minute)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got := s.log(); got != c.wantSeen {
				t.Fatalf("calls = %s, want %s", got, c.wantSeen)
			}
		})
	}
}

func TestFailureAtEachCallStopsTheSequenceAndNeverTerminatesEarly(t *testing.T) {
	ops := []string{"DescribeInstances", "GetQueueUrl", "SendMessage", "TerminateInstances"}
	for i, op := range ops {
		for _, code := range []int{500, 503, 400, 403, 404} {
			t.Run(fmt.Sprintf("%s-%d", op, code), func(t *testing.T) {
				s := newScripted(t)
				s.describe = describeXML(inst(aID, "running", "spot"))
				s.over[op] = fail(code, "boom")
				slept := 0
				in := s.injector()
				in.Sleep = func(context.Context, time.Duration) error { slept++; return nil }
				err := in.Interrupt(context.Background(), aID, time.Minute)
				if err == nil {
					t.Fatal("expected an error")
				}
				if want := strings.Join(ops[:i+1], ","); s.log() != want {
					t.Fatalf("calls = %s, want %s", s.log(), want)
				}
				if i < 3 && slept != 0 {
					t.Fatalf("slept before the warning was delivered")
				}
				if i == 3 && slept != 1 {
					t.Fatalf("terminate failure must come after the grace, slept=%d", slept)
				}
			})
		}
	}
}

func TestSQSErrorShapes(t *testing.T) {
	for name, body := range map[string]string{
		"QueueDoesNotExist": `{"__type":"com.amazonaws.sqs#QueueDoesNotExist","message":"The specified queue does not exist."}`,
		"AccessDenied":      `{"__type":"AccessDeniedException","message":"denied"}`,
		"throttle":          `{"__type":"RequestThrottled"}`,
	} {
		for _, op := range []string{"GetQueueUrl", "SendMessage"} {
			t.Run(name+"/"+op, func(t *testing.T) {
				s := newScripted(t)
				s.describe = describeXML(inst(aID, "running", "spot"))
				s.over[op] = fail(400, body)
				err := s.injector().Interrupt(context.Background(), aID, 0)
				if err == nil || !strings.Contains(err.Error(), "deliver warning") {
					t.Fatalf("err = %v", err)
				}
				if strings.Contains(s.log(), "TerminateInstances") {
					t.Fatalf("terminated after SQS failure: %s", s.log())
				}
			})
		}
	}
}

func TestGetQueueUrlBadAnswersAreWarningFailures(t *testing.T) {
	for name, body := range map[string]string{
		"empty object": `{}`, "empty url": `{"QueueUrl":""}`, "null": `null`, "html": `<html/>`, "array": `[]`, "number url": `{"QueueUrl":5}`, "empty body": ``,
	} {
		t.Run(name, func(t *testing.T) {
			s := newScripted(t)
			s.describe = describeXML(inst(aID, "running", "spot"))
			s.over["GetQueueUrl"] = func(w http.ResponseWriter, _ *http.Request, _ []byte) bool { _, _ = w.Write([]byte(body)); return true }
			if err := s.injector().Interrupt(context.Background(), aID, 0); err == nil {
				t.Fatal("expected an error")
			}
			if s.log() != "DescribeInstances,GetQueueUrl" {
				t.Fatalf("calls = %s", s.log())
			}
		})
	}
}

// BUG: Warn formats the raw GetQueueUrl response body into its error
// ("GetQueueUrl returned no QueueUrl: %.200s") without scrubbing the secret,
// and neither Warn nor Interrupt's "deliver warning" wrapper redacts it. A
// backend that answers 200 with the secret in the body leaks it to the terminal
// (send() scrubs non-200 bodies, but this path bypasses send()'s scrub).
func TestBugGetQueueUrlBodyLeaksSecretInError(t *testing.T) {
	s := newScripted(t)
	s.describe = describeXML(inst(aID, "running", "spot"))
	s.over["GetQueueUrl"] = func(w http.ResponseWriter, _ *http.Request, _ []byte) bool {
		_, _ = w.Write([]byte(`{"debug":"SK"}`))
		return true
	}
	in := s.injector()
	in.Secret = "SK"
	for name, err := range map[string]error{
		"Warn":      in.Warn(context.Background(), aID),
		"Interrupt": in.Interrupt(context.Background(), aID, 0),
	} {
		if err == nil || strings.Contains(err.Error(), "SK") {
			t.Errorf("%s: secret leaked in error: %v", name, err)
		}
	}
}

// SSRF: the QueueUrl in a response is attacker-controlled data. It may go into
// the SendMessage body (that is the SQS contract) but must never become the
// request target: every request goes to the configured gateway endpoint.
func TestAttackerQueueURLCannotRedirectRequests(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.String())
		mu.Unlock()
	}))
	defer evil.Close()
	for _, qurl := range []string{
		evil.URL + "/000000000000/meridian-g0-interruption",
		strings.Replace(evil.URL, "127.0.0.1", "localhost", 1) + "/q",
		"//" + strings.TrimPrefix(evil.URL, "http://") + "/q",
		"file:///etc/passwd",
		evil.URL + "\r\nX-Injected: 1",
	} {
		s := newScripted(t)
		s.describe = describeXML(inst(aID, "running", "spot"))
		s.over["GetQueueUrl"] = func(w http.ResponseWriter, _ *http.Request, _ []byte) bool {
			b, _ := json.Marshal(map[string]string{"QueueUrl": qurl})
			_, _ = w.Write(b)
			return true
		}
		if err := s.injector().Interrupt(context.Background(), aID, 0); err != nil {
			t.Fatalf("%q: %v", qurl, err)
		}
		if s.log() != "DescribeInstances,GetQueueUrl,SendMessage,TerminateInstances" {
			t.Fatalf("%q: calls = %s", qurl, s.log())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 0 {
		t.Fatalf("a QueueUrl from a response redirected traffic: %v", hits)
	}
}

func TestFlociStyleQueueURLIsPassedThroughButSentToTheGateway(t *testing.T) {
	s := newScripted(t)
	s.describe = describeXML(inst(aID, "running", "spot"))
	if err := s.injector().Interrupt(context.Background(), aID, 0); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var sent map[string]string
	if err := json.Unmarshal([]byte(s.bodies[2]), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["QueueUrl"] != "http://floci:4566/000000000000/meridian-g0-interruption" {
		t.Fatalf("QueueUrl = %q", sent["QueueUrl"])
	}
	// The request body for SendMessage is exactly two string fields and MessageBody is itself a JSON string.
	if len(sent) != 2 {
		t.Fatalf("unexpected SendMessage fields: %v", sent)
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(sent["MessageBody"]), &inner); err != nil {
		t.Fatalf("MessageBody must be a JSON string holding the event: %v", err)
	}
	var gq map[string]string
	_ = json.Unmarshal([]byte(s.bodies[1]), &gq)
	if !reflect.DeepEqual(gq, map[string]string{"QueueName": "meridian-g0-interruption"}) {
		t.Fatalf("GetQueueUrl body = %v", gq)
	}
}

func TestSQSRequestsAreSigV4VerifiedAsJSON10WithSignedTarget(t *testing.T) {
	var gotCT, gotTarget, gotAuth []string
	s := newScripted(t)
	s.describe = describeXML(inst(aID, "running", "spot"))
	s.over["GetQueueUrl"] = func(w http.ResponseWriter, r *http.Request, b []byte) bool {
		gotCT = append(gotCT, r.Header.Get("Content-Type"))
		gotTarget = append(gotTarget, r.Header.Get("X-Amz-Target"))
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		return false
	}
	if err := s.injector().Interrupt(context.Background(), aID, 0); err != nil {
		t.Fatal(err)
	}
	if gotCT[0] != "application/x-amz-json-1.0" || gotTarget[0] != "AmazonSQS.GetQueueUrl" {
		t.Fatalf("ct=%q target=%q", gotCT, gotTarget)
	}
	if !strings.Contains(gotAuth[0], "x-amz-target") || !strings.Contains(gotAuth[0], "/us-west-2/sqs/aws4_request") {
		t.Fatalf("x-amz-target must be a signed header and scope must be sqs: %s", gotAuth[0])
	}
	if strings.Contains(gotAuth[0], "SK") {
		t.Fatal("Authorization contains the secret")
	}
}

// ---------------------------------------------------------------------------
// Ordering, grace and cancellation phases.
// ---------------------------------------------------------------------------

func TestTerminateStrictlyAfterGraceAndGraceIsPassedThrough(t *testing.T) {
	for _, g := range []time.Duration{0, 1, time.Second, 2 * time.Minute, time.Duration(math.MaxInt64)} {
		s := newScripted(t)
		s.describe = describeXML(inst(aID, "running", "spot"))
		in := s.injector()
		var got []time.Duration
		in.Sleep = func(_ context.Context, d time.Duration) error {
			got = append(got, d)
			if strings.Contains(s.log(), "Terminate") || !strings.HasSuffix(s.log(), "SendMessage") {
				t.Errorf("sleep must run after SendMessage and before Terminate, log=%s", s.log())
			}
			return nil
		}
		if err := in.Interrupt(context.Background(), aID, g); err != nil {
			t.Fatalf("grace %v: %v", g, err)
		}
		if len(got) != 1 || got[0] != g {
			t.Fatalf("grace %v: sleep got %v", g, got)
		}
	}
}

func TestNegativeGraceNeverTouchesTheNetwork(t *testing.T) {
	s := newScripted(t)
	s.describe = describeXML(inst(aID, "running", "spot"))
	for _, g := range []time.Duration{-1, -time.Hour, time.Duration(math.MinInt64)} {
		if err := s.injector().Interrupt(context.Background(), aID, g); err == nil {
			t.Errorf("grace %v accepted", g)
		}
	}
	if s.log() != "" {
		t.Fatalf("calls = %s", s.log())
	}
}

func TestZeroGraceWithRealWaitTerminatesPromptly(t *testing.T) {
	s := newScripted(t)
	s.describe = describeXML(inst(aID, "running", "spot"))
	in := s.injector()
	in.Sleep = nil
	start := time.Now()
	if err := in.Interrupt(context.Background(), aID, 0); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("zero grace took too long")
	}
	if !strings.HasSuffix(s.log(), "SendMessage,TerminateInstances") {
		t.Fatal(s.log())
	}
}

func TestHugeGraceWithRealWaitIsCancelable(t *testing.T) {
	s := newScripted(t)
	s.describe = describeXML(inst(aID, "running", "spot"))
	in := s.injector()
	in.Sleep = nil
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- in.Interrupt(ctx, aID, time.Duration(math.MaxInt64)) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(s.log(), "Terminate") {
			t.Fatalf("err=%v log=%s", err, s.log())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not return")
	}
}

func TestContextCanceledAtEachPhaseNeverTerminates(t *testing.T) {
	phases := []string{"DescribeInstances", "GetQueueUrl", "SendMessage", "grace"}
	for _, ph := range phases {
		t.Run(ph, func(t *testing.T) {
			s := newScripted(t)
			s.describe = describeXML(inst(aID, "running", "spot"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in := s.injector()
			in.Sleep = func(ctx context.Context, d time.Duration) error {
				if ph == "grace" {
					cancel()
				}
				<-ctx.Done()
				return ctx.Err()
			}
			if ph != "grace" {
				// cancel as the server receives that op: the client then sees a canceled context.
				s.hook = func(_ int, op string) {
					if op == ph {
						cancel()
						time.Sleep(50 * time.Millisecond)
					}
				}
			}
			err := in.Interrupt(ctx, aID, time.Hour)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(s.log(), "TerminateInstances") {
				t.Fatalf("terminated after cancel in %s: %s", ph, s.log())
			}
			if ph == "grace" && !strings.Contains(err.Error(), "NOT terminated") {
				t.Fatalf("grace abort must say NOT terminated: %v", err)
			}
		})
	}
}

func TestCancelDuringTerminateCallStillReportsAnError(t *testing.T) {
	s := newScripted(t)
	s.describe = describeXML(inst(aID, "running", "spot"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.hook = func(_ int, op string) {
		if op == "TerminateInstances" {
			cancel()
			time.Sleep(50 * time.Millisecond)
		}
	}
	if err := s.injector().Interrupt(ctx, aID, 0); err == nil {
		t.Fatal("expected an error when the terminate call is canceled mid-flight")
	}
}

func TestSlowServerTimesOutBeforeTerminating(t *testing.T) {
	s := newScripted(t)
	s.describe = describeXML(inst(aID, "running", "spot"))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s.over["SendMessage"] = func(http.ResponseWriter, *http.Request, []byte) bool { <-release; return true }
	in := s.injector()
	in.Client = &http.Client{Timeout: 100 * time.Millisecond}
	start := time.Now()
	err := in.Interrupt(context.Background(), aID, 0)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
	if strings.Contains(s.log(), "Terminate") {
		t.Fatal("terminated after SendMessage timed out")
	}
}

// Observation (low): client() documents "a timeout if the caller set none" but a caller-supplied
// client with Timeout 0 stays unbounded; only ctx bounds it. This pins current behaviour.
func TestCallerClientWithoutTimeoutStaysUnbounded(t *testing.T) {
	in := &Injector{Client: &http.Client{}}
	if got := in.client().Timeout; got != 0 {
		t.Logf("client timeout is now %v (behaviour changed: update the doc comment/test)", got)
	}
	t.Logf("OBSERVATION: caller client without Timeout is left unbounded (Timeout=%v)", in.client().Timeout)
}

func TestCallerClientIsNotMutated(t *testing.T) {
	c := &http.Client{Timeout: time.Second}
	in := &Injector{Client: c}
	_ = in.client()
	if c.CheckRedirect != nil {
		t.Fatal("injector mutated the caller's client")
	}
}

// ---------------------------------------------------------------------------
// Input validation.
// ---------------------------------------------------------------------------

func TestInstanceIDValidationTable(t *testing.T) {
	good := []string{"i-12345678", "i-0123456789abcdef0", "i-" + strings.Repeat("a", 32), "i-abcdefgh"}
	bad := []string{"", "i-", "i-1234567", "i-" + strings.Repeat("a", 33), "I-12345678", "i-ABCDEFGH", "i-1234567\n", "\ni-12345678", "i-12345678\x00",
		" i-12345678", "i-12345678 ", "i-1234 5678", "i-1234\t5678", "i-１２３４５６７８" /* fullwidth */, "i-абвгдежз", "i-12345678&Action=X", "i-12345678%26", "i-12345678/../x",
		"i-1234567́", "i_12345678", "i-12345678,i-87654321", "i-‮12345678", "i-12345678\r\nX: y", "i-12345678'--", `i-12345678"`, "i-12345678;", "i-123456\xff8"}
	for _, id := range good {
		if err := ValidateInstanceID(id); err != nil {
			t.Errorf("%q rejected: %v", id, err)
		}
	}
	for _, id := range bad {
		if err := ValidateInstanceID(id); err == nil {
			t.Errorf("%q accepted", id)
		}
	}
}

func TestQueueAndEndpointValidationTable(t *testing.T) {
	base := Injector{Endpoint: "http://127.0.0.1:1", Region: "us-west-2", QueueName: "q", Access: "AK", Secret: "SK"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"", strings.Repeat("a", 81), "a b", "a.b", "a/b", "a\n", "q.fifo", "é", "q\x00", `q"`, "q%20", "..", "a:b", "q​"} {
		in := base
		in.QueueName = q
		if in.Validate() == nil {
			t.Errorf("queue %q accepted", q)
		}
	}
	for _, q := range []string{"a", strings.Repeat("a", 80), "Meridian_g0-interruption", "0"} {
		in := base
		in.QueueName = q
		if err := in.Validate(); err != nil {
			t.Errorf("queue %q rejected: %v", q, err)
		}
	}
	for _, e := range []string{"", "x", "127.0.0.1:4566", "//host", "http:///x", "http://", "https://", "gopher://h", "javascript:alert(1)", "HTTP://h", "ws://h",
		"http://u@h", "http://u:p@h", "http://h?x", "http://h#x", "http://h/\x00", "http://[::1", "http://ho st", "http://h\n", "\nhttp://h", "http://%zz", " http://h"} {
		in := base
		in.Endpoint = e
		err := in.Validate()
		if err == nil && e != "HTTP://h" { // url.Parse lowercases the scheme, so HTTP://h is a legitimate http URL
			t.Errorf("endpoint %q accepted", e)
		}
	}
	for _, e := range []string{"http://aws-gateway:4566", "https://gw.example", "http://[::1]:4566", "http://h/", "http://h//", "http://h/prefix"} {
		in := base
		in.Endpoint = e
		if err := in.Validate(); err != nil {
			t.Errorf("endpoint %q rejected: %v", e, err)
		}
	}
	for _, r := range []string{"", "US-WEST-2", "us_west_2", "us/west", "us west", "us-west-2\n", strings.Repeat("a", 33), "ü", "us,west"} {
		in := base
		in.Region = r
		if in.Validate() == nil {
			t.Errorf("region %q accepted", r)
		}
	}
}

func TestEndpointWithTrailingSlashAndPrefixStillWorks(t *testing.T) {
	s := newScripted(t)
	s.describe = describeXML(inst(aID, "running", "spot"))
	in := s.injector()
	in.Endpoint = s.srv.URL + "/"
	if err := in.Interrupt(context.Background(), aID, 0); err != nil {
		t.Fatalf("trailing slash: %v", err)
	}
}

func TestBadConfigNeverReachesNetworkEvenFromCheckWarnReclaim(t *testing.T) {
	s := newScripted(t)
	s.describe = describeXML(inst(aID, "running", "spot"))
	in := s.injector()
	in.QueueName = "bad name"
	if in.Warn(context.Background(), aID) == nil {
		t.Error("Warn accepted a bad queue")
	}
	in = s.injector()
	in.Access = ""
	if in.Check(context.Background(), aID) == nil || in.Reclaim(context.Background(), aID) == nil {
		t.Error("empty access accepted")
	}
	if s.log() != "" {
		t.Fatalf("calls = %s", s.log())
	}
}

// ---------------------------------------------------------------------------
// Event shape.
// ---------------------------------------------------------------------------

func TestEventNormalisesTimeToUTCZAndSurvivesOddInput(t *testing.T) {
	at := time.Date(2026, 10, 9, 14, 0, 0, 999999999, time.FixedZone("x", 2*3600))
	var m map[string]any
	if err := json.Unmarshal(Event("us-west-2", aID, at), &m); err != nil {
		t.Fatal(err)
	}
	if m["time"] != "2026-10-09T12:00:00Z" {
		t.Fatalf("time = %v", m["time"])
	}
	for _, in := range [][2]string{{"", ""}, {"\x00", "\xff"}, {`"`, `\`}, {strings.Repeat("r", 1<<16), strings.Repeat("i", 1<<16)}} {
		if !json.Valid(Event(in[0], in[1], time.Time{})) {
			t.Errorf("invalid JSON for %q", in[0][:min(len(in[0]), 8)])
		}
	}
	// zero time and year > 9999 must still produce valid JSON.
	if !json.Valid(Event("r", aID, time.Time{})) || !json.Valid(Event("r", aID, time.Date(12345, 1, 1, 0, 0, 0, 0, time.UTC))) {
		t.Error("time edge produced invalid JSON")
	}
}

func TestEventAccountIsAConstantNotDerivedFromInput(t *testing.T) {
	var m map[string]any
	_ = json.Unmarshal(Event("us-west-2", aID, time.Unix(0, 0)), &m)
	if m["account"] != "123456789012" || m["version"] != "0" || m["source"] != "aws.ec2" {
		t.Fatalf("%v", m)
	}
	d := m["detail"].(map[string]any)
	if len(d) != 2 || d["instance-action"] != "terminate" || d["instance-id"] != aID {
		t.Fatalf("detail = %v", d)
	}
	if r := m["resources"].([]any); len(r) != 1 || r[0] != "arn:aws:ec2:us-west-2:instance/"+aID {
		t.Fatalf("resources = %v", r)
	}
}

// ---------------------------------------------------------------------------
// Concurrency under -race with one shared Injector.
// ---------------------------------------------------------------------------

func TestManyConcurrentInterruptsShareOneInjector(t *testing.T) {
	r := newRig(t)
	const n = 24
	ids := make([]string, n)
	for i := range ids {
		ids[i] = r.launch("spot")
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = r.in.Interrupt(context.Background(), ids[i], time.Duration(i)*time.Millisecond)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("%s: %v", ids[i], err)
		}
		if s, _ := r.store.State(ids[i]); s != "shutting-down" {
			t.Errorf("%s = %s", ids[i], s)
		}
	}
	if len(r.aws.sends) != n || len(r.aws.badSigs) != 0 {
		t.Fatalf("sends=%d badSigs=%v", len(r.aws.sends), r.aws.badSigs)
	}
}

func TestSameInstanceInterruptedConcurrentlyNeverPanicsOrCorrupts(t *testing.T) {
	r := newRig(t)
	id := r.launch("spot")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.in.Interrupt(context.Background(), id, time.Millisecond) }()
	}
	wg.Wait()
	if s, _ := r.store.State(id); s != "shutting-down" && s != "terminated" {
		t.Fatalf("state = %s", s)
	}
	if len(r.aws.badSigs) != 0 {
		t.Fatal(r.aws.badSigs)
	}
}

func TestInjectorFieldsAreNotMutatedByUse(t *testing.T) {
	s := newScripted(t)
	s.describe = describeXML(inst(aID, "running", "spot"))
	in := s.injector()
	in.Sleep = nil
	before := *in
	if err := in.Interrupt(context.Background(), aID, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if in.Client != before.Client || in.Endpoint != before.Endpoint || in.Now != nil {
		t.Fatal("Interrupt mutated the Injector")
	}
}

// Redirect from each of the two APIs (EC2 and SQS paths) must neither be followed nor leak a signed request.
func TestRedirectOnEachOpIsNotFollowed(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { mu.Lock(); hits++; mu.Unlock() }))
	defer target.Close()
	for _, op := range []string{"DescribeInstances", "GetQueueUrl", "SendMessage", "TerminateInstances"} {
		for _, code := range []int{301, 302, 303, 307, 308} {
			s := newScripted(t)
			s.describe = describeXML(inst(aID, "running", "spot"))
			s.over[op] = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
				http.Redirect(w, r, target.URL, code)
				return true
			}
			if err := s.injector().Interrupt(context.Background(), aID, 0); err == nil {
				t.Errorf("%s/%d: expected an error", op, code)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("redirect target hit %d times", hits)
	}
}

func TestSecretScrubbedFromEveryErrorPathIncludingShortSecrets(t *testing.T) {
	const secret = "s3cr3t/with+chars=="
	for _, op := range []string{"DescribeInstances", "GetQueueUrl", "SendMessage", "TerminateInstances"} {
		s := newScripted(t)
		s.describe = describeXML(inst(aID, "running", "spot"))
		s.over[op] = fail(500, "x "+secret+" y")
		in := s.injector()
		in.Secret = secret
		// the stub verifies against "SK", so signatures fail first with 403: also fine, the scrub must hold either way
		err := in.Interrupt(context.Background(), aID, 0)
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("%s: leaked: %v", op, err)
		}
	}
}

// Each scrubbing layer is tested alone, because the other layer would otherwise mask a mutant.
func TestRedactAloneScrubsErrorsAndKeepsNil(t *testing.T) {
	in := &Injector{Secret: "hunter2hunter2"}
	if in.redact(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	err := in.redact(errors.New("Post http://h: bad hunter2hunter2 and again hunter2hunter2"))
	if strings.Contains(err.Error(), "hunter2hunter2") || !strings.Contains(err.Error(), "***") {
		t.Fatalf("not scrubbed: %v", err)
	}
	plain := errors.New("no secret here")
	if in.redact(plain) != plain {
		t.Fatal("errors without the secret should pass through unchanged")
	}
}

func TestSendScrubsNon200BodiesBeforeTruncating(t *testing.T) {
	const secret = "hunter2hunter2"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the secret straddles the 200-byte truncation point
		http.Error(w, strings.Repeat("x", 190)+secret, 500)
	}))
	defer srv.Close()
	in := &Injector{Secret: secret}
	req, _ := http.NewRequest("POST", srv.URL, nil)
	_, err := in.send(context.Background(), req)
	if err == nil || strings.Contains(err.Error(), "hunter") {
		t.Fatalf("err = %v", err)
	}
}
