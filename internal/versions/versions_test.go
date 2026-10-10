package versions

import (
	"strings"
	"testing"
)

// digest is a syntactically valid sha256 digest used only in test fixtures.
const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// valid returns a File that satisfies every rule, so each test can break one rule.
func valid() File {
	return File{Components: []Entry{
		{Name: "karpenter", Kind: "chart", Version: "1.0.0"},
		{Name: "k3s", Kind: "image", Repo: "rancher/k3s", Version: "v1.0.0-k3s1", Digest: digest},
		{Name: "k3d", Kind: "tool", Version: "5.0.0"},
		{Name: "kwok", Kind: "image", Repo: "registry.k8s.io/kwok/kwok", Version: "v0.0.1", Digest: digest},
		{Name: "floci", Kind: "image", Repo: "floci/floci", Version: "1.0.0", Digest: digest},
		{Name: "golang", Kind: "image", Repo: "golang", Version: "1.24", Digest: digest},
		{Name: "static", Kind: "image", Repo: "gcr.io/distroless/static-debian12", Version: "nonroot", Digest: digest},
	}}
}

func TestValidateAcceptsFullyPinnedFile(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("expected valid file, got %v", err)
	}
}

func TestValidateRejectsLatestAndEmptyVersions(t *testing.T) {
	for _, bad := range []string{"", "latest", "LATEST"} {
		f := valid()
		f.Components[0].Version = bad
		err := f.Validate()
		if err == nil || !strings.Contains(err.Error(), "karpenter") {
			t.Fatalf("version %q should be rejected for karpenter, got %v", bad, err)
		}
	}
}

func TestValidateRejectsImageWithoutDigest(t *testing.T) {
	f := valid()
	f.Components[1].Digest = ""
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), "k3s") {
		t.Fatalf("image without digest should be rejected, got %v", err)
	}
}

func TestValidateRejectsMissingRequiredComponent(t *testing.T) {
	f := valid()
	f.Components = f.Components[:4] // drops floci, golang and static
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), "floci") {
		t.Fatalf("missing floci should be rejected, got %v", err)
	}
}

func TestValidateRejectsImageWithoutRepo(t *testing.T) {
	f := valid()
	f.Components[1].Repo = ""
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), "k3s") {
		t.Fatalf("image without repo should be rejected, got %v", err)
	}
}

func TestValidateRejectsUnknownKind(t *testing.T) {
	f := valid()
	f.Components[2].Kind = "mystery"
	if err := f.Validate(); err == nil {
		t.Fatal("unknown kind should be rejected")
	}
}

func TestGetFindsComponentByName(t *testing.T) {
	e, ok := valid().Get("kwok")
	if !ok || e.Name != "kwok" {
		t.Fatalf("Get(kwok) = %+v, %v", e, ok)
	}
	if _, ok := valid().Get("nope"); ok {
		t.Fatal("Get(nope) should report not found")
	}
}

func TestRepoVersionsFileIsValid(t *testing.T) {
	f, err := Load("../../versions.yaml") // relative to internal/versions
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("versions.yaml is not fully pinned:\n%v", err)
	}
}
