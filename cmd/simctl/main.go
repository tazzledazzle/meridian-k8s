// Command simctl injects spot interruptions through the gateway and probes network reachability.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/tazzledazzle/meridian/internal/simctl"
)

func main() {
	// SIGINT/SIGTERM cancel the run; an interrupt during the grace period leaves the instance running.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := simctl.Run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
