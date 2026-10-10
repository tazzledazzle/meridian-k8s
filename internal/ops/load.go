package ops

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads and parses an ops file; callers should call Validate.
func Load(path string) (List, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return List{}, fmt.Errorf("read %s: %w", path, err)
	}
	var l List
	if err := yaml.Unmarshal(raw, &l); err != nil {
		return List{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return l, nil
}
