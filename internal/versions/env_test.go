package versions

import (
	"reflect"
	"strings"
	"testing"
)

func TestEnvEmitsPinnedImageRefsAndVersions(t *testing.T) {
	f := File{Components: []Entry{
		{Name: "moto", Kind: "image", Repo: "motoserver/moto", Version: "5.0.0", Digest: digest},
		{Name: "karpenter", Kind: "chart", Version: "1.2.3"},
	}}
	want := []string{
		"KARPENTER_VERSION=1.2.3",
		"MOTO_IMAGE=motoserver/moto:5.0.0@" + digest,
		"MOTO_VERSION=5.0.0",
	}
	if got := f.Env(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Env() = %v, want %v", got, want)
	}
}

func TestEnvReplacesDashesInNames(t *testing.T) {
	f := File{Components: []Entry{{Name: "my-tool", Kind: "tool", Version: "1"}}}
	if got := f.Env(); !reflect.DeepEqual(got, []string{"MY_TOOL_VERSION=1"}) {
		t.Fatalf("Env() = %v", got)
	}
}

// The output is sourced by shells and read by Compose, so a value must never be able to add lines or expand.
func TestCheckEnvRejectsUnsafeValuesAndDuplicateKeys(t *testing.T) {
	bad := map[string][]string{
		"newline":   {"A_VERSION=1\nB_VERSION=2"},
		"space":     {"A_VERSION=1 2"},
		"dollar":    {"A_VERSION=$(id)"},
		"quote":     {`A_VERSION="x"`},
		"backtick":  {"A_VERSION=`id`"},
		"semicolon": {"A_VERSION=1;rm"},
		"bad key":   {"a-b_VERSION=1"},
		"empty":     {"A_VERSION="},
		"duplicate": {"A_VERSION=1", "A_VERSION=2"},
	}
	for name, lines := range bad {
		if err := CheckEnv(lines); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	ok := []string{"MOTO_IMAGE=motoserver/moto:5.0.0@" + digest, "K3S_VERSION=v1.35.9-k3s1", "STATIC_VERSION=nonroot"}
	if err := CheckEnv(ok); err != nil {
		t.Fatalf("CheckEnv(good) = %v", err)
	}
}

func TestCheckEnvCatchesDashUnderscoreNameCollision(t *testing.T) {
	f := File{Components: []Entry{
		{Name: "a-b", Kind: "tool", Version: "1"},
		{Name: "a_b", Kind: "tool", Version: "2"},
	}}
	if err := CheckEnv(f.Env()); err == nil {
		t.Fatal("a-b and a_b both map to A_B_VERSION; expected an error")
	}
}

func TestEnvOfTheRepoFileIsSafe(t *testing.T) {
	f, err := Load("../../versions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	env := f.Env()
	if err := CheckEnv(env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(env, "\n"), "FLOCI_IMAGE=floci/floci:") {
		t.Fatalf("env = %v", env)
	}
}
