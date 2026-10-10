// Command aws-gateway is the single AWS endpoint for the simulated plane.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tazzledazzle/meridian/internal/gateway"
	"github.com/tazzledazzle/meridian/internal/ops"
	"github.com/tazzledazzle/meridian/internal/trace"
)

// services are the SigV4 service names the gateway handles; a trace route for anything else could never match.
var services = map[string]bool{"ec2": true, "iam": true, "sts": true, "sqs": true, "ssm": true, "pricing": true, "eks": true}

// flushInterval is how often the trace is written while running (a variable so tests can shorten it).
var flushInterval = 10 * time.Second

// shutdownGrace bounds how long in-flight requests get to finish after a signal.
const shutdownGrace = 5 * time.Second

// pairFlag collects repeated name=value flags and refuses duplicates and empty halves.
type pairFlag map[string]string

func (b pairFlag) String() string { return fmt.Sprint(map[string]string(b)) }

func (b pairFlag) Set(v string) error {
	name, val, ok := strings.Cut(v, "=")
	if !ok || name == "" || val == "" {
		return fmt.Errorf("want name=value, got %q", v)
	}
	if _, dup := b[name]; dup { // a silent overwrite would hide a typo
		return fmt.Errorf("%q given twice", name)
	}
	b[name] = val
	return nil
}

// writeTrace persists the recorded call surface; both files are replaced atomically.
func writeTrace(rec *trace.Recorder, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := trace.WriteGolden(filepath.Join(dir, "calls.golden"), rec.Golden()); err != nil {
		return err
	}
	b, err := rec.JSON()
	if err != nil {
		return err
	}
	return trace.WriteFileAtomic(filepath.Join(dir, "calls.json"), b)
}

// newServer builds the HTTP server with every timeout set: the gateway reads up
// to 1 MiB before authenticating, so slow or idle clients must not hold connections.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second, // longer than the proxy's 60s wait for a backend
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// validateWiring refuses configurations that would turn into runtime 503s:
// ops.yaml backends with no handler, nil handlers, and trace routes to nowhere.
func validateWiring(cfg gateway.Config) error {
	if missing := cfg.MissingBackends(); len(missing) > 0 {
		return fmt.Errorf("ops.yaml routes to backends that have no -backend flag: %v", missing)
	}
	for name, h := range cfg.Backends { // MissingBackends only checks key presence
		if h == nil {
			return fmt.Errorf("backend %q has no handler", name)
		}
	}
	routes := make([]string, 0, len(cfg.TraceRoutes))
	for svc := range cfg.TraceRoutes {
		routes = append(routes, svc)
	}
	sort.Strings(routes)
	for _, svc := range routes {
		name := cfg.TraceRoutes[svc]
		if !services[svc] {
			return fmt.Errorf("-trace-route %s=%s: %s is not a service this gateway handles", svc, name, svc)
		}
		if h, ok := cfg.Backends[name]; !ok || h == nil {
			return fmt.Errorf("-trace-route %s=%s names a backend that has no -backend flag", svc, name)
		}
	}
	return nil
}

func main() {
	// SIGINT/SIGTERM cancel the context, which starts a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr, nil)
	stop()
	os.Exit(code)
}

// run is the whole program without process globals. It returns the exit code.
// If ready is non-nil, the bound listen address is sent on it once the server accepts connections.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, ready chan<- string) int {
	logger := log.New(stderr, "aws-gateway: ", log.LstdFlags)
	fail := func(format string, a ...any) int {
		logger.Printf(format, a...)
		return 1
	}

	fs := flag.NewFlagSet("aws-gateway", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:4566", "address to listen on")
	opsPath := fs.String("ops", "ops.yaml", "path to ops.yaml")
	allowAny := fs.Bool("allow-any-interface", false, "allow a non-loopback listen address (containers only; published ports must stay on 127.0.0.1)")
	traceMode := fs.Bool("trace", false, "forward operations missing from ops.yaml and record every call")
	record := fs.Bool("record", false, "record every call without forwarding unsupported operations (the gate uses this)")
	traceOut := fs.String("trace-out", "trace/raw", "directory for calls.golden and calls.json (trace and record modes)")
	backends := pairFlag{}
	fs.Var(backends, "backend", "name=url, repeatable; names are the \"backend\" values in ops.yaml (e.g. -backend floci=http://floci:4566)")
	traceRoutes := pairFlag{}
	fs.Var(traceRoutes, "trace-route", "service=backend, repeatable; trace mode sends operations missing from ops.yaml for that service to the named backend")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return fail("unexpected arguments: %v", fs.Args())
	}

	// No default credentials, ever: the keys come from `make init` through the environment. They are never logged.
	access, secret := getenv("AWS_ACCESS_KEY_ID"), getenv("AWS_SECRET_ACCESS_KEY")
	if access == "" || secret == "" {
		return fail("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required; run `make init`")
	}
	if err := gateway.CheckBind(*listen, *allowAny); err != nil {
		return fail("%v", err)
	}
	if *allowAny {
		logger.Printf("WARNING: listening on %s; make sure the published port is bound to 127.0.0.1", *listen)
	}
	list, err := ops.Load(*opsPath)
	if err != nil {
		return fail("ops: %v", err)
	}
	if err := list.Validate(); err != nil {
		return fail("ops: %v", err)
	}
	if len(list.Ops) == 0 {
		return fail("ops: %s lists no operations", *opsPath)
	}

	handlers := map[string]http.Handler{}
	for name, url := range backends {
		h, err := gateway.Proxy(url)
		if err != nil {
			return fail("backend %s: %v", name, err) // the URL itself is not echoed
		}
		handlers[name] = h
	}
	cfg := gateway.Config{
		Ops: list,
		// Constant-time comparison so response timing reveals nothing about the key.
		Lookup: func(k string) (string, bool) {
			if subtle.ConstantTimeCompare([]byte(k), []byte(access)) == 1 {
				return secret, true
			}
			return "", false
		},
		Backends:    handlers,
		TraceRoutes: traceRoutes,
		TraceMode:   *traceMode,
		Logf:        logger.Printf,
	}
	if err := validateWiring(cfg); err != nil {
		return fail("%v", err)
	}

	rec := trace.NewRecorder()
	recording := *traceMode || *record
	if recording {
		cfg.OnCall = rec.Record // called concurrently; the recorder is thread-safe
		// Fail closed: prove the output directory is writable now, not at shutdown when the evidence would be lost.
		if err := writeTrace(rec, *traceOut); err != nil {
			return fail("trace: cannot write to %s: %v", *traceOut, err)
		}
	}

	srv := newServer(*listen, gateway.New(cfg))
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return fail("listen: %v", err)
	}
	logger.Printf("listening on %s (trace=%v record=%v)", ln.Addr(), *traceMode, *record)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	var flusher sync.WaitGroup
	flushCtx, stopFlush := context.WithCancel(ctx)
	if recording { // flush periodically so a killed container still leaves a trace behind
		interval := flushInterval
		flusher.Add(1)
		go func() {
			defer flusher.Done()
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-flushCtx.Done():
					return
				case <-t.C:
					if err := writeTrace(rec, *traceOut); err != nil {
						logger.Printf("trace: %v", err)
					}
				}
			}
		}()
	}
	if ready != nil {
		ready <- ln.Addr().String()
	}

	code := 0
	serveDone := false // true once serveErr has been drained
	select {
	case <-ctx.Done(): // signal or parent cancel: graceful shutdown
	case err := <-serveErr: // the server died on its own
		logger.Printf("server stopped: %v", err)
		serveDone, code = true, 1
	}
	stopFlush()
	flusher.Wait() // no periodic write can overlap the final one

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil { // drains in-flight requests
		logger.Printf("shutdown: %v", err)
		_ = srv.Close()
		code = 1
	}
	if !serveDone { // a second read would block forever if the server had already died
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			code = 1
		}
	}
	if recording {
		if err := writeTrace(rec, *traceOut); err != nil { // after the drain, so late calls are included
			logger.Printf("trace: %v", err)
			code = 1
		}
	}
	_ = stdout
	return code
}
