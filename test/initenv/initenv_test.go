package initenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run executes scripts/init-env.sh against a target path and returns the file contents.
func run(t *testing.T, target string) string {
	t.Helper()
	cmd := exec.Command("bash", "../../scripts/init-env.sh", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init-env.sh failed: %v\n%s", err, out)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInitEnvGeneratesDistinctKeysWithRestrictedMode(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.env"), filepath.Join(dir, "b.env")
	ca, cb := run(t, a), run(t, b)
	if ca == cb {
		t.Fatal("two runs produced identical credentials")
	}
	if !strings.Contains(ca, "AWS_ACCESS_KEY_ID=") || !strings.Contains(ca, "AWS_SECRET_ACCESS_KEY=") {
		t.Fatalf("missing keys in:\n%s", ca)
	}
	info, _ := os.Stat(a)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestInitEnvNeverOverwritesExistingFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "x.env")
	first := run(t, target)
	second := run(t, target)
	if first != second {
		t.Fatal("existing .env was overwritten")
	}
}

func TestGitignoreExcludesEnvFile(t *testing.T) {
	b, err := os.ReadFile("../../.gitignore")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\n.env\n") && !strings.HasPrefix(string(b), ".env\n") {
		t.Fatalf(".gitignore must list .env, got:\n%s", b)
	}
}
