// Command pricing-facade answers the AWS Price List API for the catalog's EC2 types. It trusts the gateway on the
// internal network and is never published to the host.
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

	"github.com/tazzledazzle/meridian/internal/gateway"
	"github.com/tazzledazzle/meridian/internal/pricingfacade"
)

// shutdownGrace bounds how long in-flight requests get to finish after a signal.
const shutdownGrace = 5 * time.Second

func main() {
	// SIGINT/SIGTERM cancel the context, which starts a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, nil)
	stop()
	os.Exit(code)
}

// newServer sets every timeout: a slow or idle client must not hold a connection forever.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// run is the whole program without process globals; it returns the exit code.
// If ready is non-nil, the bound address is sent on it once the listener is open.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, ready chan<- string) int {
	_ = stdout
	logger := log.New(stderr, "pricing-facade: ", log.LstdFlags)
	fs := flag.NewFlagSet("pricing-facade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:4571", "address to listen on")
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
	h := pricingfacade.New()
	h.Logf = logger.Printf // refusals only: operation status and error code, never a body
	srv := newServer(*listen, h)
	logger.Printf("listening on %s", ln.Addr())

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	if ready != nil {
		ready <- ln.Addr().String()
	}

	code, serveDone := 0, false
	select {
	case <-ctx.Done(): // signal or parent cancel: graceful shutdown
	case err := <-serveErr: // the server died on its own
		logger.Printf("server stopped: %v", err)
		serveDone, code = true, 1
	}
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
	return code
}
