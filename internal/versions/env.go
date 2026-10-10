package versions

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Env renders the matrix as environment lines for Compose and the shell scripts:
// NAME_VERSION for every component and NAME_IMAGE (repo:version@digest) for images.
// Run the result through CheckEnv before handing it to a shell or Compose.
func (f File) Env() []string {
	var out []string
	for _, e := range f.Components {
		name := strings.ToUpper(strings.ReplaceAll(e.Name, "-", "_"))
		out = append(out, fmt.Sprintf("%s_VERSION=%s", name, e.Version))
		if e.Kind == "image" {
			out = append(out, fmt.Sprintf("%s_IMAGE=%s:%s@%s", name, e.Repo, e.Version, e.Digest))
		}
	}
	sort.Strings(out)
	return out
}

// envLine allows only characters that are inert in a shell and in Compose .env files.
var envLine = regexp.MustCompile(`^[A-Z][A-Z0-9_]*=[A-Za-z0-9._:@/+-]+$`)

// CheckEnv refuses lines that could inject extra lines, expand, or quote, and
// duplicate keys (for example "a-b" and "a_b" both become A_B_*).
func CheckEnv(lines []string) error {
	seen := map[string]bool{}
	for _, l := range lines {
		if !envLine.MatchString(l) {
			return fmt.Errorf("unsafe or malformed env line %q", l)
		}
		key, _, _ := strings.Cut(l, "=")
		if seen[key] {
			return fmt.Errorf("duplicate env key %s", key)
		}
		seen[key] = true
	}
	return nil
}
