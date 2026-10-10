// Package versions models versions.yaml, the single source of truth for every
// pinned image, chart and tool the platform depends on.
package versions

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Entry is one pinned component. Images must carry a digest; charts and tools
// are pinned by version only.
type Entry struct {
	Name    string `yaml:"name"`
	Kind    string `yaml:"kind"`           // image | chart | tool
	Repo    string `yaml:"repo,omitempty"` // required for images, e.g. "rancher/k3s"
	Version string `yaml:"version"`
	Digest  string `yaml:"digest,omitempty"` // required for images, "sha256:<64 hex>"
}

// File is the parsed versions.yaml.
type File struct {
	Components []Entry `yaml:"components"`
}

// digestRE accepts only a full sha256 digest in lowercase hex.
var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// required lists the components G0 cannot run without, with the kind each must have.
var required = map[string]string{
	"karpenter": "chart",
	"k3s":       "image",
	"k3d":       "tool",
	"kwok":      "image",
	"floci":     "image", // the AWS emulator S0 evaluates (moto is optional: added only if S0 needs it)
	"golang":    "image", // builder image for every Go service
	"static":    "image", // minimal runtime image for every Go service
}

// Get returns the component with the given name.
func (f File) Get(name string) (Entry, bool) {
	for _, e := range f.Components {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Validate reports every problem at once, so one run shows everything to fix.
func (f File) Validate() error {
	var errs []error
	seen := map[string]bool{}
	for _, e := range f.Components {
		seen[e.Name] = true
		switch e.Kind {
		case "image", "chart", "tool":
		default:
			errs = append(errs, fmt.Errorf("%s: unknown kind %q", e.Name, e.Kind))
		}
		// "latest" and empty versions defeat the whole point of pinning.
		if v := strings.ToLower(strings.TrimSpace(e.Version)); v == "" || v == "latest" {
			errs = append(errs, fmt.Errorf("%s: version must be pinned, got %q", e.Name, e.Version))
		}
		// Images are pinned by digest so a re-pushed tag cannot change what runs.
		if e.Kind == "image" && !digestRE.MatchString(e.Digest) {
			errs = append(errs, fmt.Errorf("%s: image needs a sha256 digest, got %q", e.Name, e.Digest))
		}
		if e.Kind == "image" && e.Repo == "" {
			errs = append(errs, fmt.Errorf("%s: image needs a repo", e.Name))
		}
	}
	for name, kind := range required {
		if !seen[name] {
			errs = append(errs, fmt.Errorf("%s: required %s component is missing", name, kind))
		}
	}
	return errors.Join(errs...)
}
