package versions

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	goSDKRE   = regexp.MustCompile(`go_sdk\.download\([^)]*version\s*=\s*"([^"]+)"`)
	ociPullRE = regexp.MustCompile(`(?s)oci\.pull\(\s*name\s*=\s*"distroless_static"(.*?)\n\)`)
	digestKV  = regexp.MustCompile(`digest\s*=\s*"([^"]+)"`)
	imageKV   = regexp.MustCompile(`image\s*=\s*"([^"]+)"`)
)

// stripComments drops Starlark "#" comments (outside string literals) so a commented-out stanza cannot satisfy a check.
func stripComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		inStr := false
		for j := 0; j < len(l); j++ {
			switch {
			case l[j] == '\\' && inStr:
				j++
			case l[j] == '"':
				inStr = !inStr
			case l[j] == '#' && !inStr:
				lines[i] = l[:j]
				j = len(l)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// CheckModulePins verifies that the few literals MODULE.bazel must repeat (bzlmod cannot read versions.yaml)
// still equal versions.yaml: the Go SDK version (component "golang") and the distroless base image repo and
// digest (component "static"). versions.yaml stays the source of truth; a divergence is an error.
func CheckModulePins(moduleBazel string, f File) error {
	var errs []error
	moduleBazel = stripComments(moduleBazel)

	golang, ok := f.Get("golang")
	all := goSDKRE.FindAllStringSubmatch(moduleBazel, -1)
	var m []string
	if len(all) > 0 {
		m = all[0]
	}
	for _, o := range all[min(1, len(all)):] {
		if golang.Version != o[1] {
			errs = append(errs, fmt.Errorf("Go SDK: a further go_sdk.download pins %s, versions.yaml golang is %s", o[1], golang.Version))
		}
	}
	switch {
	case !ok:
		errs = append(errs, errors.New("versions.yaml has no golang component"))
	case m == nil:
		errs = append(errs, errors.New("MODULE.bazel has no go_sdk.download(version = ...)"))
	case m[1] != golang.Version:
		errs = append(errs, fmt.Errorf("Go SDK: MODULE.bazel pins %s, versions.yaml golang is %s", m[1], golang.Version))
	}

	static, ok := f.Get("static")
	p := ociPullRE.FindStringSubmatch(moduleBazel)
	switch {
	case !ok:
		errs = append(errs, errors.New("versions.yaml has no static component"))
	case p == nil:
		errs = append(errs, errors.New(`MODULE.bazel has no oci.pull(name = "distroless_static", ...)`))
	default:
		if d := digestKV.FindStringSubmatch(p[1]); d == nil || d[1] != static.Digest {
			errs = append(errs, fmt.Errorf("base image digest: MODULE.bazel differs from versions.yaml static (%s)", static.Digest))
		}
		if i := imageKV.FindStringSubmatch(p[1]); i == nil || i[1] != static.Repo {
			errs = append(errs, fmt.Errorf("base image repo: MODULE.bazel differs from versions.yaml static (%s)", static.Repo))
		}
	}
	return errors.Join(errs...)
}
