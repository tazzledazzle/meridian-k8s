// Package awscall builds signed AWS requests for seeding and debugging the
// simulated plane (a tiny stand-in for the AWS CLI, with no dependencies).
package awscall

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

// Services are the SigV4 service names the gateway accepts; anything else would only be rejected later.
var Services = []string{"ec2", "iam", "sts", "sqs", "ssm", "pricing", "eks"}

// ErrUnknownService is returned for a service the gateway does not handle.
var ErrUnknownService = errors.New("unknown service")

// check validates the inputs every builder shares. Errors never include the secret.
func check(endpoint, service, region, access, secret string) (string, error) {
	known := false
	for _, s := range Services {
		known = known || s == service
	}
	if !known {
		return "", fmt.Errorf("%w %q (want one of %s)", ErrUnknownService, service, strings.Join(Services, ", "))
	}
	if access == "" || secret == "" {
		return "", errors.New("access key and secret key are required")
	}
	if region == "" {
		return "", errors.New("region is required")
	}
	// Only a plain http(s) endpoint with a host and no embedded credentials is accepted.
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("endpoint must be an http(s) URL with a host, without credentials, query or fragment")
	}
	return strings.TrimRight(endpoint, "/"), nil
}

// BuildQuery builds a signed query-protocol POST (EC2, IAM, STS). params are "key=value" pairs.
func BuildQuery(endpoint, service, region, action, version string, params []string, access, secret string, now time.Time) (*http.Request, []byte, error) {
	base, err := check(endpoint, service, region, access, secret)
	if err != nil {
		return nil, nil, err
	}
	if action == "" {
		return nil, nil, errors.New("action is required")
	}
	form := url.Values{"Action": {action}, "Version": {version}}
	for _, p := range params {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, nil, fmt.Errorf("parameter %q must look like key=value", p)
		}
		// Action and Version come from their own flags; a positional param must not silently replace them.
		if k == "Action" || k == "Version" {
			return nil, nil, fmt.Errorf("parameter %q is reserved; use the -action and -version flags", k)
		}
		form.Set(k, v)
	}
	body := []byte(form.Encode())
	r, err := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	awsapi.Sign(r, body, access, secret, region, service, now)
	return r, body, nil
}

// BuildJSON builds a signed JSON-RPC POST (SSM, SQS, Pricing). jsonVersion is "1.0" or "1.1".
func BuildJSON(endpoint, service, region, target, jsonVersion, body, access, secret string, now time.Time) (*http.Request, []byte, error) {
	base, err := check(endpoint, service, region, access, secret)
	if err != nil {
		return nil, nil, err
	}
	if target == "" {
		return nil, nil, errors.New("target is required")
	}
	if jsonVersion != "1.0" && jsonVersion != "1.1" {
		return nil, nil, fmt.Errorf("json version %q must be 1.0 or 1.1", jsonVersion)
	}
	if !json.Valid([]byte(body)) {
		return nil, nil, errors.New("body is not valid JSON")
	}
	r, err := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	r.Header.Set("Content-Type", "application/x-amz-json-"+jsonVersion)
	// The target must be set before signing: Sign covers X-Amz-Target when present.
	r.Header.Set("X-Amz-Target", target)
	awsapi.Sign(r, []byte(body), access, secret, region, service, now)
	return r, []byte(body), nil
}

// BuildREST builds a signed REST-JSON request (EKS). path starts with "/".
func BuildREST(endpoint, service, region, method, path, body, access, secret string, now time.Time) (*http.Request, []byte, error) {
	base, err := check(endpoint, service, region, access, secret)
	if err != nil {
		return nil, nil, err
	}
	// "//host/x" would be read as a network-path reference and could change the target host.
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, nil, errors.New("path must start with a single \"/\"")
	}
	r, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	awsapi.Sign(r, []byte(body), access, secret, region, service, now)
	return r, []byte(body), nil
}
