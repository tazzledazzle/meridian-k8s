package versions

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads and parses a versions file. It does not validate; callers decide
// whether to call Validate (the repo's own test always does).
func Load(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("read %s: %w", path, err)
	}
	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return f, nil
}
