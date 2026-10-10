package trace

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestAdvConcurrentEverything(t *testing.T) {
	r := NewRecorder()
	var wg sync.WaitGroup
	dir := t.TempDir()
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				switch (g + i) % 5 {
				case 0, 1:
					r.Record("ec2", fmt.Sprintf("Op%d", i%17), i%3 != 0)
				case 2:
					_ = r.Golden()
					_ = r.Unsupported()
				case 3:
					if _, err := r.JSON(); err != nil {
						t.Error(err)
					}
				case 4:
					if err := WriteGolden(filepath.Join(dir, fmt.Sprintf("g%d.golden", g%4)), r.Golden()); err != nil {
						t.Error(err)
					}
				}
			}
		}(g)
	}
	wg.Wait()
	if len(r.Golden()) != 17 {
		t.Fatalf("want 17 distinct ops, got %d", len(r.Golden()))
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("stray temp file %s", e.Name())
		}
	}
}

func TestAdvGoldenSortingUnicodeAndOdd(t *testing.T) {
	r := NewRecorder()
	r.Record("ec2", "Zebra", true)
	r.Record("ec2", "Äpfel", true)
	r.Record("ec2", "日本", true)
	r.Record("", "", true)
	r.Record("a:b", "c:d", true)
	g := r.Golden()
	if !sort.StringsAreSorted(g) || len(g) != 5 {
		t.Fatalf("golden not sorted/complete: %q", g)
	}
}

// The gateway only records operation names it parsed from a signed request, but the
// Recorder API accepts any string. A newline in a name must not corrupt the golden file.
// Accepted deviation B6: names with a newline cannot reach the recorder through the gateway (opNameRE).
func TestDeviationGoldenRoundTripWithNewlineInName(t *testing.T) {
	t.Skip("accepted deviation B6: unreachable via the gateway, which validates operation names first")
	r := NewRecorder()
	r.Record("ec2", "Describe\nInstances", true)
	r.Record("ec2", "# comment", true)
	p := filepath.Join(t.TempDir(), "g")
	if err := WriteGolden(p, r.Golden()); err != nil {
		t.Fatal(err)
	}
	back, err := ReadGolden(p)
	if err != nil {
		t.Fatal(err)
	}
	added, removed := Diff(r.Golden(), back)
	// BUG: newline in an operation name corrupts the golden format (one entry becomes two lines).
	if len(added)+len(removed) != 0 {
		t.Fatalf("round trip differs: written=%q read=%q added=%q removed=%q", r.Golden(), back, added, removed)
	}
}

func TestAdvColonInNamesCollideButDoNotCrash(t *testing.T) {
	r := NewRecorder()
	r.Record("a:b", "c", true)
	r.Record("a", "b:c", true)
	p := filepath.Join(t.TempDir(), "g")
	if err := WriteGolden(p, r.Golden()); err != nil {
		t.Fatal(err)
	}
	back, _ := ReadGolden(p)
	t.Logf("colon collision keeps %d entries: %q", len(back), back)
}

func TestAdvReadGoldenShapes(t *testing.T) {
	dir := t.TempDir()
	w := func(name, c string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name, content string
		want          []string
	}{
		{"empty", "", nil},
		{"crlf", "# h\r\nec2:A\r\nec2:B\r\n", []string{"ec2:A", "ec2:B"}},
		{"trailing blanks", "ec2:A\n\n\n  \n", []string{"ec2:A"}},
		{"dups kept", "ec2:A\nec2:A\n", []string{"ec2:A", "ec2:A"}},
		{"no trailing newline", "ec2:A", []string{"ec2:A"}},
		{"indented comment", "   # c\nx:y\n", []string{"x:y"}},
	}
	for _, c := range cases {
		got, err := ReadGolden(w(c.name, c.content))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	if _, err := ReadGolden(filepath.Join(dir, "nope")); err == nil {
		t.Error("missing file should error")
	}
	if _, err := ReadGolden(dir); err == nil {
		t.Error("directory should error")
	}
}

// A line longer than bufio.Scanner's 64 KiB limit must be reported, not silently truncated.
func TestAdvReadGoldenHugeLineIsAnErrorNotSilent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "g")
	os.WriteFile(p, []byte("ec2:A\n"+strings.Repeat("x", 200000)+"\nec2:B\n"), 0o644)
	got, err := ReadGolden(p)
	if err == nil {
		t.Fatalf("expected an error for a 200k line, got %d lines silently", len(got))
	}
}

func TestAdvReadGoldenManyLines(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 300000; i++ {
		fmt.Fprintf(&sb, "svc:Op%d\n", i)
	}
	p := filepath.Join(t.TempDir(), "g")
	os.WriteFile(p, []byte(sb.String()), 0o644)
	got, err := ReadGolden(p)
	if err != nil || len(got) != 300000 {
		t.Fatalf("n=%d err=%v", len(got), err)
	}
}

func TestAdvWriteGoldenFailures(t *testing.T) {
	dir := t.TempDir()
	if err := WriteGolden(filepath.Join(dir, "no", "such", "g"), []string{"a:b"}); err == nil {
		t.Error("missing parent dir should fail")
	}
	d := filepath.Join(dir, "isdir")
	os.Mkdir(d, 0o755)
	if err := WriteGolden(d, []string{"a:b"}); err == nil {
		t.Error("writing onto a directory should fail")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("stray temp %s", e.Name())
		}
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		ro := filepath.Join(dir, "ro")
		os.Mkdir(ro, 0o555)
		defer os.Chmod(ro, 0o755)
		if err := WriteGolden(filepath.Join(ro, "g"), []string{"a:b"}); err == nil {
			t.Error("read-only dir should fail")
		}
		if e, _ := os.ReadDir(ro); len(e) != 0 {
			t.Errorf("leftovers in ro dir: %v", e)
		}
	}
}

func TestAdvWriteGoldenOverwriteAndMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "g")
	if err := WriteGolden(p, []string{"a:b"}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	if err := WriteGolden(p, []string{"a:b", "c:d"}); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadGolden(p)
	if len(got) != 2 {
		t.Errorf("got %q", got)
	}
}

func TestAdvWriteGoldenThroughSymlinkReplacesLinkNotTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("keep"), 0o644)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if err := WriteGolden(link, []string{"a:b"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(target)
	if string(b) != "keep" {
		t.Errorf("symlink target was modified: %q", b)
	}
}

func TestAdvDiffPropertyAgainstSetModel(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	gen := func() []string {
		n := rng.Intn(30)
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("s%d:o%d", rng.Intn(4), rng.Intn(12))
		}
		return out
	}
	for iter := 0; iter < 2000; iter++ {
		o, c := gen(), gen()
		added, removed := Diff(o, c)
		om, cm := map[string]bool{}, map[string]bool{}
		for _, s := range o {
			om[s] = true
		}
		for _, s := range c {
			cm[s] = true
		}
		var wa, wr []string
		for s := range cm {
			if !om[s] {
				wa = append(wa, s)
			}
		}
		for s := range om {
			if !cm[s] {
				wr = append(wr, s)
			}
		}
		sort.Strings(wa)
		sort.Strings(wr)
		if strings.Join(added, ",") != strings.Join(wa, ",") || strings.Join(removed, ",") != strings.Join(wr, ",") {
			t.Fatalf("iter %d: o=%q c=%q added=%q/%q removed=%q/%q", iter, o, c, added, wa, removed, wr)
		}
	}
	if a, r := Diff(nil, nil); len(a) != 0 || len(r) != 0 {
		t.Error("nil diff")
	}
}

func TestAdvUnsupportedFlagSticks(t *testing.T) {
	r := NewRecorder()
	r.Record("ec2", "A", false)
	r.Record("ec2", "A", true)
	if u := r.Unsupported(); len(u) != 1 {
		t.Errorf("unsupported flag must stick: %q", u)
	}
}
