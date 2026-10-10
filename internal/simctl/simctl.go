// Package simctl is the command-line client for spot interruptions and the
// egress probe. It ships as a container image so tests can run it on an
// isolated Docker network that publishes no ports to the host.
package simctl

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/tazzledazzle/meridian/internal/catalog"
	"github.com/tazzledazzle/meridian/internal/spot"
)

const usage = "usage: simctl [-gateway URL] [-queue NAME] [-timeout D] interrupt [-grace 2m] [-any-market] INSTANCE_ID | probe URL"

// Exit codes: 0 success, 1 the operation failed, 2 bad usage or configuration.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// Run executes one simctl invocation and returns the process exit code.
// getenv supplies AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY; credentials are
// never accepted as flags (they would show up in process listings) and never printed.
func Run(ctx context.Context, args []string, getenv func(string) string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("simctl", flag.ContinueOnError)
	fs.SetOutput(errOut)
	gateway := fs.String("gateway", "http://aws-gateway:4566", "aws-gateway URL")
	queue := fs.String("queue", "meridian-g0-interruption", "interruption queue name")
	timeout := fs.Duration("timeout", 5*time.Second, "probe timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(errOut, usage)
		return exitUsage
	}
	switch rest[0] {
	case "interrupt":
		return interrupt(ctx, rest[1:], *gateway, *queue, getenv, out, errOut)
	case "probe":
		// Exit 0 means the URL answered, 1 means it did not. The gate runs this
		// against a public AWS endpoint and requires 1.
		if len(rest) != 2 {
			fmt.Fprintln(errOut, usage)
			return exitUsage
		}
		if *timeout <= 0 || *timeout > 10*time.Minute {
			fmt.Fprintln(errOut, "-timeout must be between 1ns and 10m")
			return exitUsage
		}
		return probe(ctx, rest[1], *timeout, out, errOut)
	default:
		fmt.Fprintf(errOut, "unknown command %q\n%s\n", rest[0], usage)
		return exitUsage
	}
}

func interrupt(ctx context.Context, args []string, gateway, queue string, getenv func(string) string, out, errOut io.Writer) int {
	sub := flag.NewFlagSet("interrupt", flag.ContinueOnError)
	sub.SetOutput(errOut)
	grace := sub.Duration("grace", 2*time.Minute, "time between the warning and termination")
	anyMarket := sub.Bool("any-market", false, "skip the spot-lifecycle check (for backends that omit instanceLifecycle)")
	if err := sub.Parse(args); err != nil || sub.NArg() != 1 {
		fmt.Fprintln(errOut, usage)
		return exitUsage
	}
	access, secret := getenv("AWS_ACCESS_KEY_ID"), getenv("AWS_SECRET_ACCESS_KEY")
	if access == "" || secret == "" {
		fmt.Fprintln(errOut, "AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required")
		return exitUsage
	}
	id := sub.Arg(0)
	in := &spot.Injector{Endpoint: gateway, Region: catalog.Region, QueueName: queue, Access: access, Secret: secret, AnyMarket: *anyMarket}
	// Everything checkable without the network is checked first, so a typo exits 2 and touches nothing.
	if err := in.Validate(); err != nil {
		fmt.Fprintln(errOut, err)
		return exitUsage
	}
	if err := spot.ValidateInstanceID(id); err != nil {
		fmt.Fprintln(errOut, err)
		return exitUsage
	}
	if *grace < 0 {
		fmt.Fprintln(errOut, "-grace must not be negative")
		return exitUsage
	}
	if err := in.Interrupt(ctx, id, *grace); err != nil {
		fmt.Fprintln(errOut, err) // errors are scrubbed of the secret by the injector
		return exitFail
	}
	fmt.Fprintf(out, "interrupted %s\n", id)
	return exitOK
}

// probe reports whether anything answers HTTP at rawURL. Any status counts as an
// answer (a 403 still proves a route exists); redirects are not followed so the
// probe never reaches a host other than the one named.
func probe(ctx context.Context, rawURL string, timeout time.Duration, out, errOut io.Writer) int {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		fmt.Fprintln(errOut, "probe URL must be an http(s) URL with a host, without credentials, query or fragment")
		return exitUsage
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		fmt.Fprintln(errOut, "probe URL is not valid")
		return exitUsage
	}
	// No proxy: the probe asks whether THIS host has a route, and a proxy variable would answer for another path.
	direct := http.DefaultTransport.(*http.Transport).Clone()
	direct.Proxy = nil
	client := &http.Client{
		Timeout:       timeout,
		Transport:     direct,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(out, "unreachable: %v\n", err)
		return exitFail
	}
	resp.Body.Close() // the body is never read: only the fact of an answer matters
	fmt.Fprintf(out, "reachable: HTTP %d\n", resp.StatusCode)
	return exitOK
}
