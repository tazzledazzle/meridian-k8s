// Command awscall sends one signed request to the simulated AWS endpoint.
//
//	awscall -service ec2 -action CreateVpc CidrBlock=10.0.0.0/16
//	awscall -service ssm -target AmazonSSM.PutParameter -body '{"Name":"/a","Value":"b","Type":"String"}'
//
// Credentials come only from AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY; there
// is deliberately no flag for them, because flags show up in process listings.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/tazzledazzle/meridian/internal/awscall"
)

const maxResponse = 4 << 20 // never print more than 4 MiB

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run is main without process globals, so tests can drive it. It returns the exit code.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("awscall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	region := getenv("AWS_REGION")
	if region == "" {
		region = "us-west-2"
	}
	endpoint := fs.String("endpoint", "http://127.0.0.1:4566", "AWS endpoint (the aws-gateway)")
	service := fs.String("service", "", "SigV4 service name, one of ec2, iam, sts, sqs, ssm, pricing, eks")
	regionFlag := fs.String("region", region, "signing region")
	action := fs.String("action", "", "query-protocol action name")
	version := fs.String("version", "2016-11-15", "query-protocol API version")
	target := fs.String("target", "", "JSON-RPC target, e.g. AmazonSSM.PutParameter")
	jsonVersion := fs.String("json-version", "1.1", "JSON-RPC version: 1.0 or 1.1")
	body := fs.String("body", "{}", "JSON-RPC request body")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	access, secret := getenv("AWS_ACCESS_KEY_ID"), getenv("AWS_SECRET_ACCESS_KEY")
	if access == "" || secret == "" || *service == "" {
		fmt.Fprintln(stderr, "need -service plus AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY (run `make init`, then `set -a; . ./.env; set +a`)")
		return 2
	}

	var req *http.Request
	var err error
	if *target != "" {
		req, _, err = awscall.BuildJSON(*endpoint, *service, *regionFlag, *target, *jsonVersion, *body, access, secret, time.Now())
	} else {
		req, _, err = awscall.BuildQuery(*endpoint, *service, *regionFlag, *action, *version, fs.Args(), access, secret, time.Now())
	}
	if err != nil {
		fmt.Fprintln(stderr, "awscall:", err)
		return 2
	}
	// A bounded client: a stuck endpoint must not hang a seeding script forever.
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, "awscall: request failed:", err)
		return 1
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	fmt.Fprintf(stdout, "HTTP %d\n%s\n", resp.StatusCode, out)
	if resp.StatusCode >= 400 {
		return 1
	}
	return 0
}
