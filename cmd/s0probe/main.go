// Command s0probe runs the S0 contract probe against an AWS endpoint (the
// aws-gateway in front of Floci, Moto or our own simulators) and writes a report.
// Exit status is 0 only when every selected check passed; a pass means "eligible",
// not "accepted by Karpenter" (see the report text).
//
// Credentials come only from AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, never from flags
// (flags show up in process listings), and are never printed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tazzledazzle/meridian/internal/s0probe"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// nameRE keeps -name a plain file stem, so a report can never be written outside -out.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// run is main without process globals, so tests can drive it. Exit codes: 0 all checks passed, 1 some did not,
// 2 bad usage or a report that could not be written.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("s0probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		endpoint = fs.String("endpoint", "http://localhost:4566", "gateway URL")
		region   = fs.String("region", "us-west-2", "AWS region")
		cluster  = fs.String("cluster", "meridian-g0", "cluster name used for the karpenter.sh/discovery tag")
		ami      = fs.String("ami-param", "/aws/service/eks/optimized-ami/1.35/amazon-linux-2023/x86_64/standard/recommended/image_id", "SSM parameter holding the AMI id (read only)")
		queue    = fs.String("queue", "meridian-g0-interruption", "SQS interruption queue name")
		create   = fs.Bool("eks-create", false, "call eks:CreateCluster before DescribeCluster (an existing cluster, 409, is fine)")
		groups   = fs.String("groups", "", "comma-separated groups to run (default all)")
		wait     = fs.Duration("wait", 60*time.Second, "how long to wait for asynchronous state (cluster ACTIVE, instance running)")
		timeout  = fs.Duration("timeout", 10*time.Minute, "overall limit for the whole run")
		out      = fs.String("out", "", "directory for the report files (default: print only)")
		name     = fs.String("name", "report", "report file stem: <name>.md and <name>.json")
		title    = fs.String("title", "S0 contract probe", "report title")
		prices   = fs.String("prices", "", "comma-separated instance types: print their on-demand prices as JSON and exit (used by the gate)")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "s0probe: "+format+"\n", a...)
		return 2
	}

	// Fail closed on anything that is not usable before a single request is sent.
	access, secret := getenv("AWS_ACCESS_KEY_ID"), getenv("AWS_SECRET_ACCESS_KEY")
	if access == "" || secret == "" {
		return usage("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set in the environment (run `make init`, then `set -a; . ./.env; set +a`); there are no credential flags")
	}
	if u, err := url.Parse(*endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return usage("-endpoint must be an http(s) URL, got %q", *endpoint)
	}
	if *cluster == "" || *region == "" {
		return usage("-cluster and -region must not be empty")
	}
	if *wait < 0 || *timeout <= 0 {
		return usage("-wait must not be negative and -timeout must be positive")
	}
	if !nameRE.MatchString(*name) || strings.Contains(*name, "..") {
		return usage("-name must be a plain file stem such as report or report-moto, got %q", *name)
	}
	known := map[string]bool{}
	for _, c := range s0probe.Checks() {
		known[c.Group] = true
	}
	var selected []string
	if *groups != "" {
		for _, g := range strings.Split(*groups, ",") {
			if !known[g] {
				return usage("unknown group %q", g)
			}
			selected = append(selected, g)
		}
	}
	var types []string
	if *prices != "" {
		for _, t := range strings.Split(*prices, ",") {
			if strings.TrimSpace(t) == "" {
				return usage("-prices has an empty instance type in %q", *prices)
			}
			types = append(types, strings.TrimSpace(t))
		}
	}

	cfg := s0probe.Config{
		Endpoint: *endpoint, Region: *region, Cluster: *cluster, AMIParam: *ami,
		QueueName: *queue, EKSCreate: *create, Wait: *wait, Groups: selected,
		Access: access, Secret: secret,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	if len(types) > 0 {
		got, err := s0probe.OnDemandPrices(ctx, cfg, types)
		if err != nil {
			fmt.Fprintln(stderr, "s0probe:", redact(err.Error(), access, secret))
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(got)
		return 0
	}

	results := s0probe.Run(ctx, cfg, s0probe.Checks())
	if len(results) == 0 {
		return usage("no checks selected")
	}
	md := s0probe.Markdown(*title, results)
	fmt.Fprint(stdout, md)

	if *out != "" {
		if err := writeReports(*out, *name, md, results); err != nil {
			fmt.Fprintln(stderr, "s0probe: write report:", err)
			return 2
		}
	}
	if s := s0probe.Summarize(results); s.Fail+s.Skip > 0 {
		return 1
	}
	return 0
}

// redact removes the credentials from text that came from a backend before it is printed.
func redact(s string, secrets ...string) string {
	for _, x := range secrets {
		if x != "" {
			s = strings.ReplaceAll(s, x, "***")
		}
	}
	return s
}

// writeReports writes <name>.md and <name>.json into dir, each through a temporary file so a crash never
// leaves half a report that the decision script could read.
func writeReports(dir, name, md string, results []s0probe.Result) error {
	js, err := s0probe.JSON(results)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for ext, data := range map[string][]byte{".md": []byte(md), ".json": js} {
		dst := filepath.Join(dir, name+ext)
		tmp, err := os.CreateTemp(dir, "."+name+ext+".tmp-*")
		if err != nil {
			return err
		}
		_, werr := tmp.Write(data)
		cerr := tmp.Close()
		if werr != nil || cerr != nil {
			os.Remove(tmp.Name())
			return fmt.Errorf("%s: %v %v", dst, werr, cerr)
		}
		if err := os.Chmod(tmp.Name(), 0o644); err != nil {
			os.Remove(tmp.Name())
			return err
		}
		if err := os.Rename(tmp.Name(), dst); err != nil {
			os.Remove(tmp.Name())
			return err
		}
	}
	return nil
}
