package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const d64 = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// base returns a valid matrix, with extra lines appended.
func baseYAML(extra string) string {
	return `components:
  - {name: karpenter, kind: chart, version: "1.0"}
  - {name: k3s, kind: image, repo: rancher/k3s, version: v1, digest: "` + d64 + `"}
  - {name: k3d, kind: tool, version: v5}
  - {name: kwok, kind: image, repo: registry.k8s.io/kwok/kwok, version: v0.8.0, digest: "` + d64 + `"}
  - {name: floci, kind: image, repo: floci/floci, version: "2.2.0", digest: "` + d64 + `"}
  - {name: golang, kind: image, repo: golang, version: "1.24", digest: "` + d64 + `"}
  - {name: static, kind: image, repo: gcr.io/distroless/static-debian12, version: nonroot, digest: "` + d64 + `"}
` + extra
}

func runYAML(t *testing.T, content string) (int, string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "v.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := run([]string{p}, &out, &errb)
	return code, out.String(), errb.String()
}

func TestAdvBaselineIsAccepted(t *testing.T) {
	if code, out, errs := runYAML(t, baseYAML("")); code != 0 {
		t.Fatalf("baseline refused: %d %s %s", code, out, errs)
	}
}

func TestAdvHostileYAMLFieldsPrintNothing(t *testing.T) {
	hostile := map[string]string{
		"newline version":  `  - {name: extra, kind: tool, version: "1\nEVIL=1"}`,
		"subshell":         `  - {name: extra, kind: tool, version: "$(touch /tmp/pwn)"}`,
		"backtick":         "  - {name: extra, kind: tool, version: \"`id`\"}",
		"quotes":           `  - {name: extra, kind: tool, version: "a'b\"c"}`,
		"spaces":           `  - {name: extra, kind: tool, version: "a b"}`,
		"equals":           `  - {name: extra, kind: tool, version: "a=b"}`,
		"unicode":          `  - {name: extra, kind: tool, version: "日本"}`,
		"very long":        `  - {name: extra, kind: tool, version: "` + strings.Repeat("9", 200000) + `\n"}`,
		"name collision":   `  - {name: k3-s, kind: tool, version: "1"}` + "\n" + `  - {name: k3_s, kind: tool, version: "1"}`,
		"duplicate":        `  - {name: k3d, kind: tool, version: "v6"}`,
		"unicode name":     `  - {name: "日本", kind: tool, version: "1"}`,
		"dotted name":      `  - {name: "a.b", kind: tool, version: "1"}`,
		"newline in name":  `  - {name: "a\nB", kind: tool, version: "1"}`,
		"repo with dollar": `  - {name: extra, kind: image, repo: "a/$HOME", version: "1", digest: "` + d64 + `"}`,
		"repo newline":     `  - {name: extra, kind: image, repo: "a\nB=1", version: "1", digest: "` + d64 + `"}`,
		"digest non hex":   `  - {name: extra, kind: image, repo: a/b, version: "1", digest: "sha256:` + strings.Repeat("$", 64) + `"}`,
		"digest newline":   `  - {name: extra, kind: image, repo: a/b, version: "1", digest: "sha256:` + strings.Repeat("a", 63) + `\n"}`,
		"yaml number ver":  `  - {name: extra, kind: tool, version: 1e3}`,
	}
	for name, extra := range hostile {
		code, out, errs := runYAML(t, baseYAML(extra))
		if name == "duplicate" || name == "name collision" || strings.HasPrefix(name, "digest") || name == "repo with dollar" || name == "repo newline" || name == "newline version" ||
			name == "subshell" || name == "backtick" || name == "quotes" || name == "spaces" || name == "equals" || name == "unicode" || name == "very long" || name == "unicode name" || name == "dotted name" || name == "newline in name" {
			if code == 0 || out != "" {
				t.Errorf("%s: code=%d out=%.200q", name, code, out)
			}
			continue
		}
		_ = errs
	}
}

// Digest is only checked for prefix and length by Validate; a non-hex digest must still not reach the output.
func TestBugNonHexDigestIsEmittedAsPinnedRef(t *testing.T) {
	code, out, _ := runYAML(t, baseYAML(`  - {name: extra, kind: image, repo: a/b, version: "1", digest: "sha256:`+strings.Repeat("Z", 64)+`"}`))
	// Z is inside CheckEnv's allowed alphabet: an obviously invalid digest (non-hex) is emitted as a pinned ref.
	// BUG: a digest with non-hex characters passes Validate and CheckEnv, so Compose gets an unpullable "pin".
	if code == 0 && strings.Contains(out, "EXTRA_IMAGE=a/b:1@sha256:"+strings.Repeat("Z", 64)) {
		t.Fatalf("non-hex digest was emitted as a valid image ref")
	}
}

func TestAdvOutputDeterministic(t *testing.T) {
	_, a, _ := runYAML(t, baseYAML(""))
	for i := 0; i < 10; i++ {
		_, b, _ := runYAML(t, baseYAML(""))
		if a != b {
			t.Fatal("non deterministic")
		}
	}
	lines := strings.Split(strings.TrimSpace(a), "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i-1] > lines[i] {
			t.Fatalf("unsorted: %q > %q", lines[i-1], lines[i])
		}
	}
}

func TestAdvBadInputsExitNonZero(t *testing.T) {
	for name, c := range map[string]string{"empty": "", "garbage": "{{{{", "not a mapping": "- a\n- b\n", "no components": "x: 1\n", "tab": "components:\n\t- x\n"} {
		code, out, _ := runYAML(t, c)
		if code == 0 || out != "" {
			t.Errorf("%s: code=%d out=%q", name, code, out)
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{t.TempDir()}, &out, &errb); code == 0 || out.Len() != 0 {
		t.Errorf("directory path: code=%d", code)
	}
}
