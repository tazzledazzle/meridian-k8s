// Package spot injects spot interruptions into the simulated AWS plane. On real
// AWS, EC2 emits an EventBridge event two minutes before it reclaims a spot
// instance, and a rule forwards it to the SQS queue Karpenter watches. No
// emulator models that, so this package does it through the public API only:
// it posts the same event to the queue, waits out the grace period, then calls
// TerminateInstances. Because it uses only AWS calls through the gateway it
// works with any EC2 backend.
package spot

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/awscall"
)

const accountID = "123456789012"

// maxResponse caps every response read; a DescribeInstances answer for one instance is a few KiB.
const maxResponse = 1 << 20

var (
	instanceIDRE = regexp.MustCompile(`^i-[a-z0-9]{8,32}$`)    // EC2 ids; keeps "&" and spaces out of query parameters
	queueNameRE  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`) // SQS queue-name rules
)

// Event returns the EventBridge body for "EC2 Spot Instance Interruption
// Warning". Karpenter v1.14.1 routes on (version, source, detail-type) and then
// reads detail.instance-id; the shape is pinned by testdata/spot_interruption_warning.json.
func Event(region, instanceID string, at time.Time) []byte {
	ev := map[string]any{
		"version":     "0",
		"id":          awsapi.NewRequestID(),
		"detail-type": "EC2 Spot Instance Interruption Warning",
		"source":      "aws.ec2",
		"account":     accountID,
		"time":        at.UTC().Format(time.RFC3339),
		"region":      region,
		// Karpenter's own fixture (and AWS's spot event) use arn:aws:ec2:<zone-or-region>:instance/<id>, with no account part.
		"resources": []string{fmt.Sprintf("arn:aws:ec2:%s:instance/%s", region, instanceID)},
		"detail":    map[string]string{"instance-id": instanceID, "instance-action": "terminate"},
	}
	b, _ := json.Marshal(ev) // a map of strings and slices cannot fail to marshal
	return b
}

// Injector talks to the aws-gateway like any AWS client.
type Injector struct {
	Endpoint  string // gateway URL
	Region    string
	QueueName string
	Access    string
	Secret    string
	// AnyMarket skips the check that the instance is a spot instance. It is for
	// backends whose DescribeInstances does not report instanceLifecycle.
	AnyMarket bool
	Client    *http.Client
	// Sleep waits out the grace period; nil means a context-aware timer.
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
}

// client returns the HTTP client with redirects disabled (a gateway never redirects, and a
// redirect could carry a signed request to another host) and a timeout if the caller set none.
func (in *Injector) client() *http.Client {
	c := http.Client{Timeout: 15 * time.Second}
	if in.Client != nil {
		c = *in.Client // copy so the caller's client is not modified
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func (in *Injector) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

// validate rejects bad configuration before any request is made.
func (in *Injector) validate() error {
	// BuildQuery applies the endpoint, region and credential rules every awscall builder shares.
	if _, _, err := awscall.BuildQuery(in.Endpoint, "ec2", in.Region, "DescribeInstances", "2016-11-15", nil, in.Access, in.Secret, time.Unix(0, 0)); err != nil {
		return err
	}
	if !queueNameRE.MatchString(in.QueueName) {
		return errors.New("queue name must be 1-80 characters of a-z, A-Z, 0-9, - and _")
	}
	return nil
}

// Validate reports a configuration error (endpoint, region, credentials, queue name) without making a request.
func (in *Injector) Validate() error { return in.redact(in.validate()) }

// redact removes the secret from an error so it can never reach a log or terminal.
func (in *Injector) redact(err error) error {
	if err == nil || in.Secret == "" || !strings.Contains(err.Error(), in.Secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), in.Secret, "***"))
}

func (in *Injector) send(ctx context.Context, req *http.Request) ([]byte, error) {
	resp, err := in.client().Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxResponse {
		return nil, fmt.Errorf("response too large (over %d bytes)", maxResponse)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := string(b)
		if in.Secret != "" {
			snippet = strings.ReplaceAll(snippet, in.Secret, "***") // scrub before truncating
		}
		return nil, fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, snippet)
	}
	return b, nil
}

func (in *Injector) query(ctx context.Context, action string, params ...string) ([]byte, error) {
	if err := in.validate(); err != nil {
		return nil, in.redact(err)
	}
	req, _, err := awscall.BuildQuery(in.Endpoint, "ec2", in.Region, action, "2016-11-15", params, in.Access, in.Secret, in.now())
	if err != nil {
		return nil, in.redact(err)
	}
	b, err := in.send(ctx, req)
	if err != nil {
		return nil, in.redact(fmt.Errorf("%s: %w", action, err))
	}
	return b, nil
}

func (in *Injector) json(ctx context.Context, target string, body any) ([]byte, error) {
	if err := in.validate(); err != nil {
		return nil, in.redact(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	// SQS is a JSON 1.0 service; the X-Amz-Target header is signed by BuildJSON.
	req, _, err := awscall.BuildJSON(in.Endpoint, "sqs", in.Region, target, "1.0", string(raw), in.Access, in.Secret, in.now())
	if err != nil {
		return nil, in.redact(err)
	}
	b, err := in.send(ctx, req)
	if err != nil {
		return nil, in.redact(fmt.Errorf("%s: %w", target, err))
	}
	return b, nil
}

// ValidateInstanceID reports whether id looks like an EC2 instance id.
func ValidateInstanceID(id string) error { return checkID(id) }

func checkID(instanceID string) error {
	if !instanceIDRE.MatchString(instanceID) {
		return fmt.Errorf("%q is not an EC2 instance id (want i- followed by 8-32 lowercase letters or digits)", instanceID)
	}
	return nil
}

// Check confirms the instance exists, is running, and (unless AnyMarket) is a spot instance.
func (in *Injector) Check(ctx context.Context, instanceID string) error {
	if err := checkID(instanceID); err != nil {
		return err
	}
	b, err := in.query(ctx, "DescribeInstances", "InstanceId.1="+instanceID)
	if err != nil {
		return err
	}
	var out struct {
		Items []struct {
			ID        string `xml:"instanceId"`
			State     string `xml:"instanceState>name"`
			Lifecycle string `xml:"instanceLifecycle"`
		} `xml:"reservationSet>item>instancesSet>item"`
	}
	// The id must match too: a backend that ignores InstanceId.1 must not make us act on another instance.
	if err := xml.Unmarshal(b, &out); err != nil || len(out.Items) != 1 || out.Items[0].ID != instanceID {
		return fmt.Errorf("DescribeInstances did not return exactly instance %s", instanceID)
	}
	it := out.Items[0]
	if it.State != "running" {
		return fmt.Errorf("instance %s is %s; only running instances can be interrupted", instanceID, it.State)
	}
	if !in.AnyMarket && it.Lifecycle != "spot" {
		return fmt.Errorf("instance %s is not a spot instance (lifecycle %q); use AnyMarket if this backend omits instanceLifecycle", instanceID, it.Lifecycle)
	}
	return nil
}

// Warn posts the interruption warning to the queue.
func (in *Injector) Warn(ctx context.Context, instanceID string) error {
	if err := checkID(instanceID); err != nil {
		return err
	}
	b, err := in.json(ctx, "AmazonSQS.GetQueueUrl", map[string]string{"QueueName": in.QueueName})
	if err != nil {
		return err
	}
	var q struct {
		QueueURL string `json:"QueueUrl"`
	}
	if err := json.Unmarshal(b, &q); err != nil || q.QueueURL == "" {
		return fmt.Errorf("GetQueueUrl returned no QueueUrl: %.200s", b)
	}
	_, err = in.json(ctx, "AmazonSQS.SendMessage", map[string]string{
		"QueueUrl": q.QueueURL, "MessageBody": string(Event(in.Region, instanceID, in.now())),
	})
	return err
}

// Reclaim terminates the instance, as AWS does when the grace period ends.
func (in *Injector) Reclaim(ctx context.Context, instanceID string) error {
	if err := checkID(instanceID); err != nil {
		return err
	}
	_, err := in.query(ctx, "TerminateInstances", "InstanceId.1="+instanceID)
	return err
}

// wait is the default grace wait: a timer that stops early when ctx is done.
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Interrupt runs the whole sequence: check, warn, wait grace, reclaim. If the
// warning cannot be delivered, the instance is left alone. If ctx is canceled
// during the grace period the instance is also left alone: aborting must never
// silently reclaim capacity (the warning is already out, so Karpenter may
// drain the node, but the operator decides whether to terminate).
func (in *Injector) Interrupt(ctx context.Context, instanceID string, grace time.Duration) error {
	if grace < 0 {
		return errors.New("grace period must not be negative")
	}
	if err := checkID(instanceID); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return in.redact(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := in.Check(ctx, instanceID); err != nil {
		return err
	}
	if err := in.Warn(ctx, instanceID); err != nil {
		return fmt.Errorf("deliver warning: %w", err)
	}
	sleep := in.Sleep
	if sleep == nil {
		sleep = wait
	}
	if err := sleep(ctx, grace); err != nil {
		return fmt.Errorf("aborted during the grace period; %s was NOT terminated (the warning was already sent): %w", instanceID, err)
	}
	return in.Reclaim(ctx, instanceID)
}
