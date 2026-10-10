package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPrintsEnvForTheRepoFile(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"../../versions.yaml"}, &out, &errb); code != 0 {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "FLOCI_IMAGE=floci/floci:2.2.0@sha256:") || !strings.Contains(out.String(), "KARPENTER_VERSION=") {
		t.Fatalf("out=%q", out.String())
	}
}

func TestRunRefusesAnUnpinnedFileAndPrintsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "versions.yaml")
	if err := os.WriteFile(path, []byte("components:\n  - {name: floci, kind: image, repo: floci/floci, version: latest}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{path}, &out, &errb); code == 0 || out.Len() != 0 {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
}

func TestRunRefusesAMissingFile(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{filepath.Join(t.TempDir(), "nope.yaml")}, &out, &errb); code == 0 || out.Len() != 0 {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
}
