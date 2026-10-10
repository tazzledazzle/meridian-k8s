package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func copyRepo(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	root, _ := filepath.Abs("../..")
	for _, n := range []string{"go.mod", "go.sum", "Makefile", "versions.yaml", "ops.yaml"} {
		b, err := os.ReadFile(filepath.Join(root, n))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dst, n), b, 0o644)
	}
	if out, err := exec.Command("cp", "-R", filepath.Join(root, "cmd"), filepath.Join(root, "internal"), dst).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	return dst
}

func TestAdvMakeEnvImagesWritesAtomicallyAndLeavesNoTemp(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("no make")
	}
	d := copyRepo(t)
	cmd := exec.Command("make", "env-images")
	cmd.Dir = d
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make env-images: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(d, ".env.images"))
	if err != nil || !strings.Contains(string(b), "_IMAGE=") || !strings.Contains(string(b), "@sha256:") {
		t.Fatalf("err=%v content=%q", err, b)
	}
	if _, err := os.Stat(filepath.Join(d, ".env.images.tmp")); err == nil {
		t.Error("temp file left behind after success")
	}
}

// A failing run must neither clobber a previous good .env.images nor leave a half file in its place.
func TestAdvMakeEnvImagesFailureKeepsPreviousFile(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("no make")
	}
	d := copyRepo(t)
	good := filepath.Join(d, ".env.images")
	os.WriteFile(good, []byte("PREVIOUS=1\n"), 0o644)
	os.WriteFile(filepath.Join(d, "versions.yaml"), []byte("components:\n  - {name: floci, kind: image, repo: floci/floci, version: latest}\n"), 0o644)
	cmd := exec.Command("make", "env-images")
	cmd.Dir = d
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("make should fail on an unpinned file:\n%s", out)
	}
	b, _ := os.ReadFile(good)
	if string(b) != "PREVIOUS=1\n" {
		t.Errorf(".env.images was clobbered: %q", b)
	}
	// A leftover empty temp is acceptable (gitignored) but is reported here for visibility.
	if st, err := os.Stat(filepath.Join(d, ".env.images.tmp")); err == nil {
		t.Logf("note: failed run leaves .env.images.tmp (%d bytes); gitignored", st.Size())
	}
}

func TestAdvMakeBuildProducesEveryBinary(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("no make")
	}
	d := copyRepo(t)
	cmd := exec.Command("make", "build")
	cmd.Dir = d
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make build: %v\n%s", err, out)
	}
	for _, n := range []string{"aws-gateway", "awscall", "s0probe", "versionsenv", "node-registrar"} {
		st, err := os.Stat(filepath.Join(d, "bin", n))
		if err != nil || st.Mode()&0o111 == 0 {
			t.Errorf("bin/%s: %v", n, err)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(d, "bin"))
	if len(ents) != 5 { // every cmd/ directory builds one binary; s0probe joined in Task 8, node-registrar in Task 10
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("bin has %v, want exactly the five binaries", names)
	}
}
