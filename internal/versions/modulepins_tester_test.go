package versions

import (
	"strings"
	"testing"
)

// Tester (meridian-17s.61) adversarial cases for CheckModulePins. Cases marked DEFECT assert the
// behaviour a pin check must have; they currently fail, so each is guarded by testerKnownDefects and
// skipped with the reason. Flip the guard to false to see the failures.
const testerKnownDefects = true

const goodDigest = d64b

func good() string {
	return moduleFor("1.24.13", "gcr.io/distroless/static-debian12", goodDigest)
}

func TestTesterMalformedModuleNeverPassesAndNeverPanics(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"whitespace":      "  \n\t\n",
		"comment only":    "# go_sdk.download(version = \"1.24.13\")\n",
		"unicode digits":  strings.Replace(good(), "1.24.13", "１.24.13", 1),
		"truncated":       `go_sdk.download(version = "1.24.13"`,
		"empty version":   moduleFor("", "gcr.io/distroless/static-debian12", goodDigest),
		"digest tag only": strings.Replace(good(), goodDigest, "latest", 1),
		"nul bytes":       "\x00\x00go_sdk",
	}
	for name, m := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panic %v", name, r)
				}
			}()
			if err := CheckModulePins(m, pinsFile()); err == nil {
				t.Errorf("%s: malformed MODULE.bazel passed the pin check", name)
			}
		}()
	}
}

func TestTesterReorderedKwargsAreAccepted(t *testing.T) {
	if testerKnownDefects {
		t.Skip("ACCEPTED T-PIN-5: oci.pull must list name first; reordering fails closed (a false alarm, not a hole)")
	}
	m := "go_sdk.download(\n    version = \"1.24.13\",\n)\n" +
		"oci.pull(\n    image  =  \"gcr.io/distroless/static-debian12\",\n    name = \"distroless_static\",\n    digest=\"" + goodDigest + "\",\n)\n"
	// name must come first in the regexp; the order here is deliberate (Starlark kwargs are unordered).
	if err := CheckModulePins(m, pinsFile()); err != nil {
		t.Errorf("valid but reordered kwargs rejected (false alarm, not a hole): %v", err)
	}
}

func TestTesterDefectCommentedOutDecoyHidesDrift(t *testing.T) {
	if testerKnownDefects {
		t.Skip("DEFECT T-PIN-1: the regexps ignore Starlark comments; a commented-out stanza that matches versions.yaml shadows a real one that has drifted")
	}
	drifted := "# go_sdk.download(version = \"1.24.13\")\ngo_sdk.download(version = \"1.25.0\")\n" +
		"oci.pull(\n    name = \"distroless_static\",\n    digest = \"" + goodDigest + "\",\n    image = \"gcr.io/distroless/static-debian12\",\n)\n"
	if err := CheckModulePins(drifted, pinsFile()); err == nil {
		t.Error("Go SDK drift hidden behind a commented-out decoy")
	}
}

func TestTesterDefectOnlyFirstGoSDKStanzaChecked(t *testing.T) {
	if testerKnownDefects {
		t.Skip("DEFECT T-PIN-2: a second go_sdk.download with a different version is not examined")
	}
	m := good() + "go_sdk.download(name = \"other\", version = \"1.25.0\")\n"
	if err := CheckModulePins(m, pinsFile()); err == nil {
		t.Error("second go_sdk.download(version=1.25.0) not flagged")
	}
}

func TestTesterDefectDigestInCommentIsCounted(t *testing.T) {
	if testerKnownDefects {
		t.Skip("DEFECT T-PIN-1b: a '# digest = \"sha256:...\"' comment inside the stanza satisfies the check with no real digest set")
	}
	m := strings.Replace(good(), `digest = "`+goodDigest+`",`, `# digest = "`+goodDigest+`",`, 1)
	if err := CheckModulePins(m, pinsFile()); err == nil {
		t.Error("stanza without a digest passed because a comment carried one")
	}
}
