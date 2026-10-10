package trace

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzReadGolden(f *testing.F) {
	for _, s := range []string{"", "a:b\n", "# c\r\nx:y\r\n", "\n\n\n", "a\x00b\n", "\xff\xfe\n"} {
		f.Add([]byte(s))
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(dir, "g")
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Skip()
		}
		lines, err := ReadGolden(p)
		if err != nil {
			return
		}
		for _, l := range lines {
			if l == "" || l[0] == '#' {
				t.Fatalf("comment/blank leaked: %q", l)
			}
		}
	})
}
