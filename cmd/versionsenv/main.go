// Command versionsenv prints versions.yaml as KEY=VALUE lines (see `make env-images`).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/tazzledazzle/meridian/internal/versions"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run prints nothing unless every check passes, so a failed run cannot leave a half-written .env.images behind with content.
func run(args []string, stdout, stderr io.Writer) int {
	path := "versions.yaml"
	if len(args) > 0 {
		path = args[0]
	}
	f, err := versions.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Refuse to emit anything from an unpinned file: Compose would silently pull "latest".
	if err := f.Validate(); err != nil {
		fmt.Fprintf(stderr, "versions.yaml is not fully pinned:\n%v\n", err)
		return 1
	}
	env := f.Env()
	if err := versions.CheckEnv(env); err != nil { // no shell-active characters, no duplicate keys
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, line := range env {
		fmt.Fprintln(stdout, line)
	}
	return 0
}
