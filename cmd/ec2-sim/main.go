// Command ec2-sim serves ec2:DescribeInstanceTypes from the catalog.
//
// It is reference data only: Floci keeps serving every other EC2 operation. It
// has no credentials because it trusts the aws-gateway on the internal network
// and is never published. It logs lifecycle events only, never request bodies.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tazzledazzle/meridian/internal/catalog"
	"github.com/tazzledazzle/meridian/internal/ec2sim"
	"github.com/tazzledazzle/meridian/internal/gateway"
)

// shutdownGrace bounds how long in-flight requests get to finish after a signal.
const shutdownGrace = 5 * time.Second

// newServer sets every timeout; requests are tiny, so the limits are tight.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

func main() {
	// SIGINT/SIGTERM cancel the context, which starts a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, nil)
	stop()
	os.Exit(code)
}

// run is the whole program without process globals; it returns the exit code.
// If ready is non-nil, the bound address is sent on it once the listener is up.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, ready chan<- string) int {
	logger := log.New(stderr, "ec2-sim: ", log.LstdFlags)
	fs := flag.NewFlagSet("ec2-sim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:4570", "address to listen on")
	allowAny := fs.Bool("allow-any-interface", false, "allow a non-loopback listen address (containers only; never publish the port)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		logger.Printf("unexpected arguments: %v", fs.Args())
		return 1
	}
	if err := gateway.CheckBind(*listen, *allowAny); err != nil {
		logger.Printf("%v", err)
		return 1
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		logger.Printf("listen: %v", err)
		return 1
	}
	srv := newServer(*listen, ec2sim.New(catalog.Types))
	logger.Printf("listening on %s, serving %d instance types", ln.Addr(), len(catalog.Types))
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	if ready != nil {
		ready <- ln.Addr().String()
	}

	code, drained := 0, false
	select {
	case <-ctx.Done(): // signal or parent cancel: graceful shutdown
	case err := <-serveErr: // the server died on its own
		logger.Printf("server stopped: %v", err)
		code, drained = 1, true
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil { // drains in-flight requests
		logger.Printf("shutdown: %v", err)
		_ = srv.Close()
		code = 1
	}
	if !drained { // a second read would block forever if the server had already died
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			code = 1
		}
	}
	_ = stdout
	return code
}
