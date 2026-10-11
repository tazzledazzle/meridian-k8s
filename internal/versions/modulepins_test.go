package versions

import (
	"os"
	"strings"
	"testing"
)

// moduleFor renders the two MODULE.bazel stanzas CheckModulePins reads.
func moduleFor(goVersion, repo, digest string) string {
	return `go_sdk.download(version = "` + goVersion + `")
oci.pull(
    name = "distroless_static",
    digest = "` + digest + `",
    image = "` + repo + `",
)
`
}

func pinsFile() File {
	return File{Components: []Entry{
		{Name: "golang", Kind: "image", Repo: "golang", Version: "1.24.13", Digest: d64a},
		{Name: "static", Kind: "image", Repo: "gcr.io/distroless/static-debian12", Version: "nonroot", Digest: d64b},
	}}
}

const (
	d64a = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	d64b = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestModulePinsAgreeWithVersionsYaml(t *testing.T) {
	m := moduleFor("1.24.13", "gcr.io/distroless/static-debian12", d64b)
	if err := CheckModulePins(m, pinsFile()); err != nil {
		t.Fatalf("matching pins rejected: %v", err)
	}
}

func TestModulePinsRejectDivergence(t *testing.T) {
	cases := map[string]string{
		"go version":   moduleFor("1.25.0", "gcr.io/distroless/static-debian12", d64b),
		"image digest": moduleFor("1.24.13", "gcr.io/distroless/static-debian12", d64a),
		"image repo":   moduleFor("1.24.13", "gcr.io/distroless/base-debian12", d64b),
		"no go sdk":    strings.Replace(moduleFor("1.24.13", "gcr.io/distroless/static-debian12", d64b), "go_sdk.download", "go_sdk.other", 1),
		"no oci pull":  "go_sdk.download(version = \"1.24.13\")\n",
	}
	for name, m := range cases {
		if err := CheckModulePins(m, pinsFile()); err == nil {
			t.Errorf("%s: divergence not detected", name)
		}
	}
}

// The repo's own MODULE.bazel must agree with its versions.yaml (the check `bazel test //:lint-versions` runs).
func TestRepoModuleBazelPinsMatchVersionsYaml(t *testing.T) {
	f, err := Load("../../versions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := os.ReadFile("../../MODULE.bazel")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckModulePins(string(m), f); err != nil {
		t.Fatal(err)
	}
}
