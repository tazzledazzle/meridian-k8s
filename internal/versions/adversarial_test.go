package versions

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const advDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func img(name, repo, ver, dig string) Entry {
	return Entry{Name: name, Kind: "image", Repo: repo, Version: ver, Digest: dig}
}

func TestAdvCheckEnvRejectsInjection(t *testing.T) {
	bad := []string{
		"A=b\nB=c", "A=b\n", "A=$(touch /tmp/x)", "A=`id`", "A=\"q\"", "A='q'", "A=a b", "A=a;b", "A=a|b", "A=a&b",
		"A=${X}", "A=$X", "A=a\\b", "A=a#b", "A=a*b", "A=日本", "A=", "=v", "a=b", "1A=b", "A-B=c", "A.B=c", "A B=c",
		"A=a=b", "A=a\rb", "A=a\x00b", " A=b", "A=b ", "A=<x>", "A=a(b)", "A=~", "A=a!b", "A=a{b}", "A=a,b", "A=a%b", "A=a?b",
		"A=a\tb",
	}
	for _, l := range bad {
		if err := CheckEnv([]string{l}); err == nil {
			t.Errorf("CheckEnv accepted %q", l)
		}
	}
	good := []string{"A=b", "A_1=1.2.3", "X_IMAGE=repo/x:v1@" + advDigest, "A=a+b-c_d"}
	for _, l := range good {
		if err := CheckEnv([]string{l}); err != nil {
			t.Errorf("CheckEnv rejected %q: %v", l, err)
		}
	}
}

func TestAdvCheckEnvDuplicatesAndCollisions(t *testing.T) {
	if err := CheckEnv([]string{"A=1", "A=2"}); err == nil {
		t.Error("duplicate key accepted")
	}
	f := File{Components: []Entry{{Name: "a-b", Kind: "tool", Version: "1"}, {Name: "a_b", Kind: "tool", Version: "1"}}}
	if err := CheckEnv(f.Env()); err == nil {
		t.Error("a-b / a_b collision accepted")
	}
	f = File{Components: []Entry{{Name: "x", Kind: "tool", Version: "1"}, {Name: "x", Kind: "tool", Version: "2"}}}
	if err := CheckEnv(f.Env()); err == nil {
		t.Error("duplicate component accepted")
	}
	// mixed case collision
	f = File{Components: []Entry{{Name: "Ab", Kind: "tool", Version: "1"}, {Name: "aB", Kind: "tool", Version: "1"}}}
	if err := CheckEnv(f.Env()); err == nil {
		t.Error("case collision accepted")
	}
	// kind collisions: component "x" image yields X_VERSION and X_IMAGE; component "x-image"
	f = File{Components: []Entry{img("x", "r/x", "1", advDigest), {Name: "x-image", Kind: "tool", Version: "1"}}}
	if err := CheckEnv(f.Env()); err != nil {
		t.Errorf("legit distinct keys refused: %v", err)
	}
}

func TestAdvEnvHostileFieldsAreRefusedByCheckEnv(t *testing.T) {
	hostile := []string{"1\nEVIL=1", "$(id)", "`id`", "a b", "'x'", "\"x\"", "日本", strings.Repeat("a", 100000) + "\n", "a;b", "a=b", ""}
	for _, h := range hostile {
		for name, f := range map[string]File{
			"version": {Components: []Entry{{Name: "t", Kind: "tool", Version: h}}},
			"repo":    {Components: []Entry{img("t", h, "1", advDigest)}},
			"digest":  {Components: []Entry{img("t", "r", "1", h)}},
			"name":    {Components: []Entry{{Name: h, Kind: "tool", Version: "1"}}},
		} {
			if err := CheckEnv(f.Env()); err == nil && h != "a=b" && h != "" {
				t.Errorf("%s=%q passed CheckEnv", name, h)
			}
		}
	}
	// "a=b" in a version/repo is inert (the shell only splits on the first '='), but CheckEnv's regex forbids it: confirm refused
	f := File{Components: []Entry{{Name: "t", Kind: "tool", Version: "a=b"}}}
	if CheckEnv(f.Env()) == nil {
		t.Error("= in value accepted")
	}
	// empty version: NAME_VERSION= must be refused (regex needs 1+ chars)
	f = File{Components: []Entry{{Name: "t", Kind: "tool", Version: ""}}}
	if CheckEnv(f.Env()) == nil {
		t.Error("empty version accepted")
	}
}

func TestAdvEnvIsDeterministicAndSortedRegardlessOfInputOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	comps := []Entry{}
	for i := 0; i < 20; i++ {
		comps = append(comps, img(fmt.Sprintf("c-%d", i), fmt.Sprintf("r/c%d", i), "v1", advDigest))
	}
	base := File{Components: comps}.Env()
	if !sort.StringsAreSorted(base) {
		t.Fatal("not sorted")
	}
	for i := 0; i < 50; i++ {
		sh := append([]Entry{}, comps...)
		rng.Shuffle(len(sh), func(a, b int) { sh[a], sh[b] = sh[b], sh[a] })
		got := File{Components: sh}.Env()
		if strings.Join(got, "\n") != strings.Join(base, "\n") {
			t.Fatal("order depends on input order")
		}
	}
}

func TestAdvEmptyFileEnv(t *testing.T) {
	if got := (File{}).Env(); len(got) != 0 {
		t.Errorf("%v", got)
	}
	if err := CheckEnv(nil); err != nil {
		t.Error(err)
	}
}

func TestAdvRepoVersionsFileEveryImageIsDigestPinned(t *testing.T) {
	f, err := Load("../../versions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	env := f.Env()
	if err := CheckEnv(env); err != nil {
		t.Fatal(err)
	}
	ref := regexp.MustCompile(`^[A-Z0-9_]+_IMAGE=[a-z0-9./_-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$`)
	n := 0
	for _, e := range f.Components {
		if e.Kind != "image" {
			continue
		}
		n++
		key := strings.ToUpper(strings.ReplaceAll(e.Name, "-", "_")) + "_IMAGE="
		found := false
		for _, l := range env {
			if strings.HasPrefix(l, key) {
				found = true
				if !ref.MatchString(l) {
					t.Errorf("image ref not digest pinned: %s", l)
				}
			}
		}
		if !found {
			t.Errorf("no env line for image %s", e.Name)
		}
	}
	if n == 0 {
		t.Fatal("no images in versions.yaml")
	}
}

// Sourcing the rendered file in bash must execute nothing and yield exactly the values.
func TestAdvSourcedInBashYieldsExactValuesWithoutExecution(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	f, err := Load("../../versions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	env := f.Env()
	if err := CheckEnv(env); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env.images")
	os.WriteFile(envFile, []byte(strings.Join(env, "\n")+"\n"), 0o644)
	cmd := exec.Command("bash", "-c", `set -a; source "$1"; env`, "bash", envFile)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			got[k] = v
		}
	}
	for _, l := range env {
		k, v, _ := strings.Cut(l, "=")
		if got[k] != v {
			t.Errorf("%s: bash saw %q want %q", k, got[k], v)
		}
	}
}

// Even a deliberately hostile (but CheckEnv-passing) line must be inert; and refused lines must never reach bash.
func TestAdvBashDoesNotExecuteAnythingFromAcceptedLines(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	// worst case within the allowed alphabet
	lines := []string{"A=" + marker, "B=a+b-c@d:e/f.g_h", "C=--"}
	if err := CheckEnv(lines); err != nil {
		t.Fatal(err)
	}
	ef := filepath.Join(dir, "e")
	os.WriteFile(ef, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	out, err := exec.Command("bash", "-c", `set -a; source "$1"; printf '%s|%s|%s' "$A" "$B" "$C"`, "bash", ef).CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	if string(out) != marker+"|a+b-c@d:e/f.g_h|--" {
		t.Errorf("got %q", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("marker file exists")
	}
}

func FuzzCheckEnv(f *testing.F) {
	for _, s := range []string{"A=b", "A=$(id)", "A=b\nC=d", "", "=", "A=日本", "A=a b"} {
		f.Add(s)
	}
	safe := regexp.MustCompile(`^[A-Z][A-Z0-9_]*=[A-Za-z0-9._:@/+-]+$`)
	f.Fuzz(func(t *testing.T, line string) {
		err := CheckEnv([]string{line})
		if err == nil {
			if !safe.MatchString(line) || strings.ContainsAny(line, "\n\r\x00 \t$`'\"\\;&|<>(){}*?!#~") {
				t.Fatalf("CheckEnv accepted %q", line)
			}
		}
		// Env on arbitrary field content must never panic.
		_ = File{Components: []Entry{{Name: line, Kind: "image", Repo: line, Version: line, Digest: line}}}.Env()
	})
}
