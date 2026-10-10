# Meridian G0 (Floci-first): Karpenter Proof Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** Prove that real, unmodified Karpenter runs against a simulated AWS plane on a local k3d cluster with no route to the internet, passing all eight G0 criteria from a clean checkout with `make gate-karpenter`, while writing custom AWS services only for the operations the Floci emulator cannot serve.

**Architecture:** An `aws-gateway` verifies SigV4, checks each operation against `ops.yaml`, and routes it to the backend that entry names. Backends are Floci (an open-source AWS emulator) wherever it passes a contract probe, and small custom Go services (`ec2-sim`, `pricing-facade`, `eks-facade`) or Moto where it does not. A spike (S0) measures which, per operation. Two pieces no emulator provides are always built: a spot-interruption injector that uses only public AWS calls, and `node-registrar`, which turns running instances into KWOK-backed Nodes.

**Tech Stack:** Go 1.24 (standard library first), `gopkg.in/yaml.v3`, Floci, Moto (only if needed), k3d/k3s, KWOK, Karpenter (Helm chart), Docker Compose, Bash, Python 3 (tooling only).

## Scope decision (surfaced for the user)

This replaces the earlier all-custom G0 plan (`2026-10-09-meridian-g0-karpenter-gate.md`, kept for reference). It covers **G0 only**; S1 and M0 to M10 each get their own plan after G0 passes (sequencing, not a cut). Decisions you should know about:

1. **S0 comes first and decides.** Tasks 7 and 8 run Floci behind the gateway and produce a per-operation pass/fail report. Tasks 11 to 14 build a custom backend **only** for operations that fail, and each says so at the top. Task 8 ends by stopping for your approval of the operation-to-backend table.
2. **Spot interruption events no longer come from `ec2-sim`.** The spec says `ec2-sim` feeds the SQS queue. No emulator models that, so `simctl` posts the EventBridge message to SQS and terminates the instance through public AWS calls (Task 9). It works with any EC2 backend, including `ec2-sim`. The behavior the gate checks (criterion 4) is unchanged.
3. **Floci's Docker socket access is filtered, not raw** (stress-test branch 1). The raw socket is root-equivalent on the Docker daemon. Task 7 first probes whether Floci runs with EC2 container execution off (Karpenter needs instance records, and KWOK supplies nodes). If it does, no socket is mounted for G0. If it does not, Floci reaches the socket only through a filtering proxy that denies exec, host binds, privileged containers and builds, and the gate fails if any service mounts `/var/run/docker.sock` directly. ADR-0002 records the outcome (Task 17).
3a. **EC2 is the swing factor** (stress-test branch 2). Instance operations share state and move together, and `CreateFleet` is the hardest. Expect Floci to serve IAM, STS, SQS and SSM, and plan on `ec2-sim` (Tasks 11 and 12) staying unless Floci passes the whole EC2 group. Task 8 probes `CreateFleet` first and stops the EC2 matrix early on a failure, going straight to the Task 11 fallback.
3b. **Moto is a second EC2 candidate** (stress-test branch 3). S0 tries Floci, then Moto, then `ec2-sim` for the coupled EC2 group. Moto's `CreateFleet` is assumed thin (spot strategy, overrides, capacity errors) but that is untested; the probe decides. `ec2-sim` is built only if both fail. The matrix script and Task 14's trigger must accept `moto` as an EC2 backend; this is an implementation change for Tasks 8 and 14, not yet made in their code.
3c. **A probe pass means eligible, not proven** (stress-test branch 4). The probe's expectations and its reference backends both come from our reading of AWS, so agreement is partly circular. Karpenter is the real oracle. The S0 report is worded accordingly, the Task 8 approval stop requires a criteria 1 to 3 run against the proposed table, and the fields the probe checks come from the `-record` trace of what Karpenter reads. These are changes to Task 8 not yet made in its text.
3d. **Criterion 7 checks topology** (stress-test branch 5). Besides one unreachable-endpoint probe, Task 16 asserts through `docker network inspect` that `meridian-gate` is `Internal: true` and that every gate container is attached only to it, and runs the egress check from the Karpenter pod and the Floci container as well. CI runs it on a fresh runner so a missed image preload fails. This is a change to Task 16 not yet made in its text.
3e. **Criterion 4 must prove the interruption path** (stress-test branch 6). The `simctl` message is built from a golden sample taken from Karpenter's own fixtures for the pinned version and unit-tested against the fields its parser reads. Task 16 asserts from Karpenter's logs that the interruption handler received the message and cordoned and drained the node before the instance was terminated, so a node removed only by garbage collection does not pass. These are changes to Tasks 9 and 16 not yet made in their text.
3f. **The S0 result is valid only for the pinned Floci version** (stress-test branch 7). CI fails if the Floci pin changes without a refreshed `docs/s0/` report recording that version. The README records the digest and a pinned-commit build-from-source fallback. ADR-0002 states that any Floci-served operation can move to another backend by changing its `backend` line in `ops.yaml`. These are changes to Task 17 and CI not yet made in their text.
3g. **Evidence before infrastructure** (stress-test branch 8). A half-day Task 0 smoke test runs before Task 1: Floci starts and answers `DescribeInstances` and `CreateFleet`, and the pinned Karpenter image starts against it with a plain endpoint override (no gateway, no SigV4). Tasks 11 to 14 are treated as expand-when-triggered: their code here is a verified sketch, and they are re-planned from the S0 report before being built. Task 0 is not yet written into the task list.
3h. **Resource bound** (stress-test branch 9). Gate workloads cap NodePool `limits` (at most about 10 nodes), the gate script checks free memory and CPU first and fails fast, and S0 records peak stack memory for the README minimum. Not yet written into the task text.
4. **The gate's network isolation depends on k3d and Floci working on an `internal` Docker network.** If either cannot, Task 15 Step 8 stops and asks you, because the fallback (a firewall rule) changes the host.
5. **The `real` node backend stays deferred** to M0 (managed nodegroups), as before. G0 needs only KWOK-simulated capacity.
6. Validation of codecs against Smithy models moves to the conformance-report plan (ADR-0003).

The coverage table at the end maps every G0 requirement to a task.

## Global Constraints

- Go core for all services; Python and Bash only for tooling (spec: Decisions).
- No real AWS is ever contacted. The gate test runs on a Docker network with no route out and asserts a public AWS endpoint is unreachable (spec: G0 criterion 7).
- The supported-operation list lives in one file, `ops.yaml`, and drives everything else, including which backend serves each operation (spec: API generation).
- `make init` generates random local access keys into a gitignored `.env`. No default or committed keys, and no auth-bypass flags in any profile (spec: Security). Floci accepts any credentials, so it is never reachable except through the gateway.
- Every service listens on `127.0.0.1` or an internal Docker network by default (spec: Security). The gate publishes nothing.
- Images pinned by digest and charts by version, from `versions.yaml` (spec: Version pinning).
- k3s is pinned to a Kubernetes minor that EKS supports and that Karpenter's compatibility table lists; whichever backend serves `DescribeCluster` reports exactly that version (spec: Version pinning).
- Karpenter-launched nodes default to the `kwok` backend and carry KWOK's conventional taint; real workloads never land on them (spec: Node backends).
- An operation is served by exactly one backend, and operations that share instance state (`ops.Coupled`) share a backend.
- Public wording: "Not validated against real AWS." No "1:1 parity" claim is made until the conformance report exists (spec: Goal).
- G0 decision checkpoint: after criteria 1 to 3 pass, or after 3 weeks, whichever comes first, stop and ask the user whether to continue or narrow scope.

---

## File Structure

```
go.mod                                module github.com/tazzledazzle/meridian
Makefile                              init, test, env-images, build, build-linux, s0-*, seed, gate-*, golden-update
versions.yaml                         pinned compatibility matrix (floci required; moto optional)
ops.yaml                              supported-operation list; each entry names its backend
README.md                             public wording
.gitignore                            .env, .env.images, .ami-id, bin/, trace/raw/, trace/gate/
.github/workflows/gate.yml            unit tests, then the gate on a fresh runner
scripts/init-env.sh                   random local credentials
scripts/seed-floci.sh, seed-moto.sh   seed SSM AMI parameter, IAM role, SQS queue, network, AMI, EKS cluster
scripts/kc.sh                         kubectl through the k3s server container
scripts/k3d-up.sh, karpenter-install.sh, trace-show.sh
scripts/s0-run.sh, s0matrix.py        run the probe; turn its report into a backend decision
scripts/profiles.py                   Compose profiles from ops.yaml
scripts/localimg.py                   upstream image ref -> local name (offline cluster)
scripts/vendor-kwok.sh, images-load.sh, gate-up.sh, gate-down.sh, install-k3d.sh
internal/versions/                    pins: Validate, Load, Env
internal/ops/                         ops.yaml model: Validate, Find, Backends, Coupled
internal/awsapi/                      errors, query, jsonrpc, sigv4 (+ tests)
internal/gateway/                     per-operation routing, trace/record, bind check, proxy
internal/trace/                       Recorder, golden files
internal/awscall/                     signed request builders (query, JSON, REST)
internal/catalog/                     AZs and instance types (KWOK node sizing; pricing data if needed)
internal/s0probe/                     the contract probe, report, price helper
internal/spot/, internal/simctl/      spot interruption injector and its CLI
internal/fakeec2/                     tiny EC2 test double for client tests (never deployed)
internal/noderegistrar/               instances -> KWOK Nodes
internal/ec2sim/                      [fallback] EC2 simulator
internal/pricing/, internal/ekstub/   [fallback] Pricing facade, EKS stub
cmd/aws-gateway, awscall, versionsenv, s0probe, simctl, node-registrar
cmd/ec2-sim, pricing-facade, eks-facade   [fallback]
deploy/Dockerfile.service, Dockerfile.tools
deploy/compose.s0.yaml, compose.gate.yaml
deploy/karpenter-values.yaml, node-registrar.yaml, workloads/, kwok/, images.system.txt, floci.label
trace/golden/karpenter-calls.golden   committed, reviewed AWS call list
test/initenv/, test/gate/             credential script test; the eight criteria (build tag `gate`)
docs/s0/                              probe report and decision
docs/checkpoints/g0.md                the G0 decision checkpoint
docs/decisions/ADR-0002, ADR-0003
```

---

### Task 1: Repo scaffold, pinned versions, credential generation

**Files:**
- Create: `go.mod`, `.gitignore`, `Makefile`, `versions.yaml`
- Create: `internal/versions/versions.go`, `internal/versions/load.go`, `internal/versions/versions_test.go`
- Create: `scripts/init-env.sh`, `test/initenv/initenv_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `versions.Entry{Name, Kind, Repo, Version, Digest string}`; `versions.File{Components []Entry}`; `func (f File) Validate() error`; `func (f File) Get(name string) (Entry, bool)`; `func Load(path string) (File, error)`. Later tasks read pinned values with `Get`.

**Acceptance Criteria:**
- `go test ./internal/versions/... ./test/initenv/...` passes.
- `Validate` rejects empty versions, the string `latest`, images without a `sha256:` digest or a repo, unknown kinds, and missing required components (`karpenter`, `k3s`, `k3d`, `kwok`, `floci`, `golang`, `static`). `moto` is optional: Task 14 adds it only if S0 needs it.
- `make init` creates a `.env` with random credentials (mode 0600) and never overwrites an existing one; `.env` is gitignored.
- The repository's own `versions.yaml` passes `Validate`.

- [ ] **Step 1: Create the module and the failing test**

```bash
mkdir -p /home/claude/meridian && cd /home/claude/meridian
git init
go mod init github.com/tazzledazzle/meridian
mkdir -p internal/versions scripts test/initenv
```

`internal/versions/versions_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/versions/ -v`
Expected: FAIL with `undefined: File` (the package has no implementation yet).

- [ ] **Step 3: Write the implementation**

`internal/versions/versions.go`:

```go
// Package versions models versions.yaml, the single source of truth for every
// pinned image, chart and tool the platform depends on.
package versions

import (
	"errors"
	"fmt"
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
		if e.Kind == "image" && !(strings.HasPrefix(e.Digest, "sha256:") && len(e.Digest) == 71) {
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
```

`internal/versions/load.go`:

```go
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
```

Then add a test that the repository's own file is valid, appended to `versions_test.go`:

```go
func TestRepoVersionsFileIsValid(t *testing.T) {
	f, err := Load("../../versions.yaml") // relative to internal/versions
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("versions.yaml is not fully pinned:\n%v", err)
	}
}
```

- [ ] **Step 4: Fetch the yaml dependency and run the unit tests**

Run: `go get gopkg.in/yaml.v3 && go test ./internal/versions/ -run 'TestValidate|TestGet' -v`
Expected: PASS for the six `TestValidate*`/`TestGet*` tests. `TestRepoVersionsFileIsValid` is expected to fail until Step 5 (there is no `versions.yaml` yet).

- [ ] **Step 5: Resolve and record the real pins in `versions.yaml`**

Look up each value with these commands, then write the exact results (no `latest`, no blanks) into `versions.yaml`:

```bash
# Karpenter chart: pick the newest stable release, and confirm its Kubernetes
# compatibility table lists the k3s minor you choose below.
helm show chart oci://public.ecr.aws/karpenter/karpenter --version <candidate> | head -5

# k3s: choose a Kubernetes minor that EKS currently supports AND Karpenter's
# compatibility table lists; resolve its image digest.
docker buildx imagetools inspect rancher/k3s:<tag> | grep -m1 Digest

# kwok controller image, the Floci image (floci/floci on Docker Hub: pick a pinned x.y.z tag, never
# "latest" or "nightly"), the Go builder image (must match go.mod's Go version) and the distroless
# runtime image: resolve digests the same way.
docker buildx imagetools inspect registry.k8s.io/kwok/kwok:<tag> | grep -m1 Digest
docker buildx imagetools inspect floci/floci:<x.y.z> | grep -m1 Digest
docker buildx imagetools inspect golang:<tag> | grep -m1 Digest
docker buildx imagetools inspect gcr.io/distroless/static-debian12:nonroot | grep -m1 Digest

# k3d (tool): record the version printed by
k3d version
```

`versions.yaml` layout (fill every value from the commands above):

```yaml
components:
  - {name: karpenter, kind: chart, version: "<resolved>"}
  - {name: k3s,       kind: image, repo: rancher/k3s,                       version: "<resolved>", digest: "sha256:<resolved>"}
  - {name: k3d,       kind: tool,  version: "<resolved>"}
  - {name: kwok,      kind: image, repo: registry.k8s.io/kwok/kwok,         version: "<resolved>", digest: "sha256:<resolved>"}
  - {name: floci,     kind: image, repo: floci/floci,                       version: "<resolved>", digest: "sha256:<resolved>"}
  - {name: golang,    kind: image, repo: golang,                            version: "<resolved>", digest: "sha256:<resolved>"}
  - {name: static,    kind: image, repo: gcr.io/distroless/static-debian12, version: nonroot,      digest: "sha256:<resolved>"}
```

Run: `go test ./internal/versions/ -v`
Expected: PASS, including `TestRepoVersionsFileIsValid`. The angle-bracket values above are the ones to replace; the test fails while any remain.

- [ ] **Step 6: Credential generation (test first)**

`test/initenv/initenv_test.go`:

```go
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
```

Run: `go test ./test/initenv/ -v`
Expected: FAIL (`scripts/init-env.sh` and `.gitignore` do not exist).

`scripts/init-env.sh`:

```bash
#!/usr/bin/env bash
# Generates random local AWS-style credentials into a gitignored env file.
# Usage: init-env.sh [path]   (default: .env)
set -euo pipefail

ENV_FILE="${1:-.env}"

# Never overwrite: regenerating would silently invalidate running services.
if [[ -f "$ENV_FILE" ]]; then
  echo "$ENV_FILE exists; leaving it untouched"
  exit 0
fi

umask 077   # file is created readable by the owner only (0600)

# Access key looks like an AWS key id but is clearly local; secret is random.
AK="MRDN$(openssl rand -hex 8 | tr 'a-f' 'A-F')"
SK="$(openssl rand -base64 30 | tr -d '\n=+/')"

cat > "$ENV_FILE" <<EOF
AWS_ACCESS_KEY_ID=$AK
AWS_SECRET_ACCESS_KEY=$SK
AWS_REGION=us-west-2
EOF
echo "wrote $ENV_FILE"
```

`.gitignore`:

```
.env
.env.images
.ami-id
bin/
trace/raw/
trace/gate/
```

`Makefile`:

```make
.PHONY: init test lint-versions

# Generates local credentials; safe to run repeatedly.
init:
	bash scripts/init-env.sh .env

test:
	go test ./...

# Fails if versions.yaml has any unpinned entry.
lint-versions:
	go test ./internal/versions/ -run TestRepoVersionsFileIsValid -v
```

Run: `chmod +x scripts/init-env.sh && go test ./test/initenv/ ./internal/versions/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum .gitignore Makefile versions.yaml internal/versions scripts/init-env.sh test/initenv
git commit -m "feat: scaffold repo with pinned versions and generated credentials"
```

---

### Task 2: Supported-operation list (`ops.yaml`) with per-operation backends

**Files:**
- Create: `ops.yaml`, `internal/ops/ops.go`, `internal/ops/load.go`, `internal/ops/ops_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `ops.Op{Service, Name, Protocol, Backend, Fidelity string}`; `ops.List{Ops []Op}`; `func (l List) Validate() error`; `func (l List) Find(service, name string) (Op, bool)`; `func (l List) Has(service, name string) bool`; `func (l List) ForService(service string) []Op`; `func (l List) Backends() []string` (distinct backend names, sorted); `var Coupled []string` (operations that read and write the same instance state); `func Load(path string) (List, error)`. `Backend` is a routing name such as `floci` or `ec2-sim`; the gateway (Task 5) sends each operation to the backend its entry names, so one service (for example `ec2`) can be split across backends. The conformance report (a later plan) reads `Fidelity`.

**Acceptance Criteria:**
- `ops.yaml` contains every operation in Karpenter's published IAM policy (the initial expectation) plus the platform's own calls (seeding, the S0 probe, the spot injector, `node-registrar`): `ssm:PutParameter`, `iam:CreateRole`, `iam:GetRole`, `sqs:CreateQueue`, `sqs:SendMessage`, `eks:CreateCluster`. Every entry has `fidelity: behavior-inferred`.
- Before S0 every entry says `backend: floci`. S0 (Task 8) is what moves an operation to a custom backend, by editing this one field.
- `Validate` rejects duplicates, unknown protocols, backend names that are not lowercase identifiers, unknown fidelity labels, and `ops.Coupled` operations routed to different backends (a `CreateFleet` on one backend with `DescribeInstances` on another would never see its own instances).
- The trace in Task 7 can add operations to this file without changing code.

- [ ] **Step 1: Write the failing test**

`internal/ops/ops_test.go`:

```go
package ops

import (
	"strings"
	"testing"
)

func TestValidateRejectsDuplicateUnknownProtocolAndBadLabels(t *testing.T) {
	good := Op{Service: "ec2", Name: "CreateFleet", Protocol: "query", Backend: "ec2-sim", Fidelity: "behavior-inferred"}
	cases := map[string]List{
		"duplicate":        {Ops: []Op{good, good}},
		"unknown protocol": {Ops: []Op{{Service: "ec2", Name: "X", Protocol: "soap", Backend: "ec2-sim", Fidelity: "behavior-inferred"}}},
		"bad backend name": {Ops: []Op{{Service: "ec2", Name: "X", Protocol: "query", Backend: "Not A Name", Fidelity: "behavior-inferred"}}},
		"unknown fidelity": {Ops: []Op{{Service: "ec2", Name: "X", Protocol: "query", Backend: "ec2-sim", Fidelity: "perfect"}}},
	}
	for name, l := range cases {
		if err := l.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := (List{Ops: []Op{good}}).Validate(); err != nil {
		t.Errorf("good list rejected: %v", err)
	}
}

func TestCoupledOperationsMustShareABackend(t *testing.T) {
	mk := func(name, backend string) Op {
		return Op{Service: "ec2", Name: name, Protocol: "query", Backend: backend, Fidelity: "behavior-inferred"}
	}
	split := List{Ops: []Op{mk("CreateFleet", "floci"), mk("DescribeInstances", "ec2-sim")}}
	if err := split.Validate(); err == nil || !strings.Contains(err.Error(), "share instance state") {
		t.Fatalf("split lifecycle should be rejected, got %v", err)
	}
	// Reference data may live elsewhere.
	ok := List{Ops: []Op{mk("CreateFleet", "floci"), mk("DescribeInstances", "floci"), mk("DescribeSpotPriceHistory", "ec2-sim"), mk("DescribeInstanceTypes", "ec2-sim")}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("reference data on another backend should be accepted, got %v", err)
	}
}

func TestFindAndBackends(t *testing.T) {
	l := List{Ops: []Op{
		{Service: "ec2", Name: "CreateFleet", Protocol: "query", Backend: "floci", Fidelity: "behavior-inferred"},
		{Service: "ec2", Name: "DescribeSpotPriceHistory", Protocol: "query", Backend: "ec2-sim", Fidelity: "behavior-inferred"},
		{Service: "ssm", Name: "GetParameter", Protocol: "json", Backend: "floci", Fidelity: "behavior-inferred"},
	}}
	if op, ok := l.Find("ec2", "DescribeSpotPriceHistory"); !ok || op.Backend != "ec2-sim" {
		t.Fatalf("Find = %+v %v", op, ok)
	}
	if _, ok := l.Find("ec2", "Nope"); ok {
		t.Fatal("unexpected hit")
	}
	if got := l.Backends(); len(got) != 2 || got[0] != "ec2-sim" || got[1] != "floci" {
		t.Fatalf("Backends = %v", got)
	}
}

func TestHasAndForService(t *testing.T) {
	l := List{Ops: []Op{
		{Service: "ec2", Name: "CreateFleet", Protocol: "query", Backend: "ec2-sim", Fidelity: "behavior-inferred"},
		{Service: "ssm", Name: "GetParameter", Protocol: "json", Backend: "floci", Fidelity: "behavior-inferred"},
	}}
	if !l.Has("ec2", "CreateFleet") || l.Has("ec2", "RunInstances") {
		t.Fatal("Has gave the wrong answer")
	}
	if got := l.ForService("ssm"); len(got) != 1 || got[0].Name != "GetParameter" {
		t.Fatalf("ForService(ssm) = %+v", got)
	}
}

// karpenterIAMPolicy is the set of AWS actions in Karpenter's published IAM
// policy. It is the best public indicator of the call surface; Task 7's trace
// is the authority and may add to ops.yaml.
var karpenterIAMPolicy = [][2]string{
	{"ssm", "GetParameter"},
	{"ec2", "DescribeImages"}, {"ec2", "RunInstances"}, {"ec2", "DescribeLaunchTemplates"},
	{"ec2", "DescribeInstances"}, {"ec2", "DescribeInstanceTypes"}, {"ec2", "DescribeInstanceTypeOfferings"},
	{"ec2", "DescribeAvailabilityZones"}, {"ec2", "DeleteLaunchTemplate"}, {"ec2", "CreateTags"},
	{"ec2", "CreateLaunchTemplate"}, {"ec2", "CreateFleet"}, {"ec2", "DescribeSpotPriceHistory"},
	{"ec2", "TerminateInstances"}, {"ec2", "DescribeSubnets"}, {"ec2", "DescribeSecurityGroups"},
	{"pricing", "GetProducts"},
	{"eks", "DescribeCluster"},
	{"iam", "GetInstanceProfile"}, {"iam", "CreateInstanceProfile"}, {"iam", "DeleteInstanceProfile"},
	{"iam", "AddRoleToInstanceProfile"}, {"iam", "RemoveRoleFromInstanceProfile"}, {"iam", "TagInstanceProfile"},
	{"iam", "ListInstanceProfiles"},
	{"sqs", "GetQueueUrl"}, {"sqs", "ReceiveMessage"}, {"sqs", "DeleteMessage"},
	{"sts", "GetCallerIdentity"},
}

// platformOps are calls the platform's own tooling makes (seeding, the spot
// injector, the S0 probe, node-registrar), not Karpenter. They must be in
// ops.yaml too, because the gateway rejects anything that is not listed.
var platformOps = [][2]string{
	{"ssm", "PutParameter"}, {"iam", "CreateRole"}, {"iam", "GetRole"},
	{"sqs", "CreateQueue"}, {"sqs", "SendMessage"}, {"eks", "CreateCluster"},
}

func TestRepoOpsFileCoversKarpenterIAMPolicyAndIsValid(t *testing.T) {
	l, err := Load("../../ops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, sn := range append(append([][2]string{}, karpenterIAMPolicy...), platformOps...) {
		if !l.Has(sn[0], sn[1]) {
			t.Errorf("ops.yaml is missing %s:%s", sn[0], sn[1])
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/ops/ -v`
Expected: FAIL with `undefined: List`.

- [ ] **Step 3: Write the implementation**

`internal/ops/ops.go`:

```go
// Package ops models ops.yaml, the single list of AWS operations the platform
// supports. Handlers, the gateway and the conformance report all read it.
package ops

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Op is one supported AWS operation.
type Op struct {
	Service  string `yaml:"service"`  // sigv4 service name: ec2, iam, sts, sqs, ssm, pricing, eks
	Name     string `yaml:"name"`     // operation name, e.g. CreateFleet
	Protocol string `yaml:"protocol"` // query | json | restjson
	Backend  string `yaml:"backend"`  // routing key: names a -backend flag, e.g. floci, ec2-sim, pricing-facade, moto
	Fidelity string `yaml:"fidelity"` // shape-validated | behavior-inferred | real-aws-verified
}

// List is the parsed ops.yaml.
type List struct {
	Ops []Op `yaml:"ops"`
}

var (
	protocols = map[string]bool{"query": true, "json": true, "restjson": true}
	backendRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	fidelity  = map[string]bool{"shape-validated": true, "behavior-inferred": true, "real-aws-verified": true}
)

// Coupled lists the operations that must share one backend because they read
// and write the same state: an instance created by CreateFleet has to be
// visible to DescribeInstances, and the subnet, security group and image it
// names have to exist in the backend that launches it. Reference data that
// holds no per-instance state (instance types, offerings, zones, spot prices)
// may be served by a different backend.
var Coupled = []string{
	"ec2:CreateFleet", "ec2:RunInstances", "ec2:TerminateInstances", "ec2:DescribeInstances", "ec2:CreateTags",
	"ec2:CreateLaunchTemplate", "ec2:DescribeLaunchTemplates", "ec2:DeleteLaunchTemplate",
	"ec2:DescribeImages", "ec2:DescribeSubnets", "ec2:DescribeSecurityGroups",
}

// Validate reports every problem at once.
func (l List) Validate() error {
	var errs []error
	seen := map[string]bool{}
	for _, o := range l.Ops {
		key := o.Service + ":" + o.Name
		if seen[key] {
			errs = append(errs, fmt.Errorf("%s: duplicate operation", key))
		}
		seen[key] = true
		if !protocols[o.Protocol] {
			errs = append(errs, fmt.Errorf("%s: unknown protocol %q", key, o.Protocol))
		}
		if !backendRE.MatchString(o.Backend) {
			errs = append(errs, fmt.Errorf("%s: backend %q must be a lowercase name like floci or ec2-sim", key, o.Backend))
		}
		if !fidelity[o.Fidelity] {
			errs = append(errs, fmt.Errorf("%s: unknown fidelity label %q", key, o.Fidelity))
		}
	}
	errs = append(errs, l.coupledErrors()...)
	return errors.Join(errs...)
}

// coupledErrors reports operations in Coupled that are routed to different backends.
func (l List) coupledErrors() []error {
	var first, firstKey string
	var errs []error
	for _, key := range Coupled {
		svc, name, _ := strings.Cut(key, ":")
		o, ok := l.Find(svc, name)
		if !ok {
			continue
		}
		if first == "" {
			first, firstKey = o.Backend, key
		} else if o.Backend != first {
			errs = append(errs, fmt.Errorf("%s is routed to %q but %s to %q: these operations share instance state and must use one backend", key, o.Backend, firstKey, first))
		}
	}
	return errs
}

// Find returns the entry for one operation.
func (l List) Find(service, name string) (Op, bool) {
	for _, o := range l.Ops {
		if o.Service == service && o.Name == name {
			return o, true
		}
	}
	return Op{}, false
}

// Has reports whether the operation is supported.
func (l List) Has(service, name string) bool {
	_, ok := l.Find(service, name)
	return ok
}

// Backends returns the distinct backend names the list routes to, sorted. The
// gateway refuses to start if any of them is not configured.
func (l List) Backends() []string {
	seen := map[string]bool{}
	for _, o := range l.Ops {
		seen[o.Backend] = true
	}
	out := make([]string, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// ForService returns every supported operation of one service.
func (l List) ForService(service string) []Op {
	var out []Op
	for _, o := range l.Ops {
		if o.Service == service {
			out = append(out, o)
		}
	}
	return out
}
```

`internal/ops/load.go`:

```go
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
```

- [ ] **Step 4: Write `ops.yaml`**

Protocols: `ec2`, `iam`, `sts` use `query`; `pricing`, `ssm`, `sqs` use `json`; `eks` uses `restjson`. All backends start as `floci`; the header comment says so.

`ops.yaml`:

```yaml
# Supported AWS operations: the single source of truth for the gateway, the probe and the conformance report.
# backend names the -backend flag that serves the operation. Before S0 every operation points at floci;
# docs/s0/decision.md records which ones move to a custom backend and why.
ops:
  # --- ec2 ---
  - {service: ec2, name: CreateFleet, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: RunInstances, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: TerminateInstances, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: DescribeInstances, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: DescribeInstanceTypes, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: DescribeInstanceTypeOfferings, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: DescribeAvailabilityZones, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: DescribeSubnets, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: DescribeSecurityGroups, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: DescribeImages, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: DescribeLaunchTemplates, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: CreateLaunchTemplate, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: DeleteLaunchTemplate, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: CreateTags, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: ec2, name: DescribeSpotPriceHistory, protocol: query, backend: floci, fidelity: behavior-inferred}
  # --- pricing ---
  - {service: pricing, name: GetProducts, protocol: json, backend: floci, fidelity: behavior-inferred}
  # --- eks ---
  - {service: eks, name: DescribeCluster, protocol: restjson, backend: floci, fidelity: behavior-inferred}
  - {service: eks, name: CreateCluster, protocol: restjson, backend: floci, fidelity: behavior-inferred}
  # --- iam ---
  - {service: iam, name: GetInstanceProfile, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: iam, name: CreateInstanceProfile, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: iam, name: DeleteInstanceProfile, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: iam, name: AddRoleToInstanceProfile, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: iam, name: RemoveRoleFromInstanceProfile, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: iam, name: TagInstanceProfile, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: iam, name: ListInstanceProfiles, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: iam, name: CreateRole, protocol: query, backend: floci, fidelity: behavior-inferred}
  - {service: iam, name: GetRole, protocol: query, backend: floci, fidelity: behavior-inferred}
  # --- sqs ---
  - {service: sqs, name: GetQueueUrl, protocol: json, backend: floci, fidelity: behavior-inferred}
  - {service: sqs, name: ReceiveMessage, protocol: json, backend: floci, fidelity: behavior-inferred}
  - {service: sqs, name: DeleteMessage, protocol: json, backend: floci, fidelity: behavior-inferred}
  - {service: sqs, name: CreateQueue, protocol: json, backend: floci, fidelity: behavior-inferred}
  - {service: sqs, name: SendMessage, protocol: json, backend: floci, fidelity: behavior-inferred}
  # --- ssm ---
  - {service: ssm, name: GetParameter, protocol: json, backend: floci, fidelity: behavior-inferred}
  - {service: ssm, name: PutParameter, protocol: json, backend: floci, fidelity: behavior-inferred}
  # --- sts ---
  - {service: sts, name: GetCallerIdentity, protocol: query, backend: floci, fidelity: behavior-inferred}
```

Run: `go test ./internal/ops/ -v`
Expected: PASS (all four tests, including `TestRepoOpsFileCoversKarpenterIAMPolicyAndIsValid`).

- [ ] **Step 5: Commit**

```bash
git add ops.yaml internal/ops
git commit -m "feat: add the supported-operation list with per-operation backends"
```

### Task 3: AWS wire helpers (errors, query parameters, JSON-RPC)

**Files:**
- Create: `internal/awsapi/errors.go`, `internal/awsapi/query.go`, `internal/awsapi/jsonrpc.go`
- Test: `internal/awsapi/errors_test.go`, `internal/awsapi/query_test.go`, `internal/awsapi/jsonrpc_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Style string` with `StyleQuery`, `StyleEC2`, `StyleJSON`, `StyleRESTJSON`
  - `func WriteError(w http.ResponseWriter, s Style, status int, code, message string)`
  - `func NewRequestID() string`
  - `type Node struct{ Val string; ... }`; `func Nest(v url.Values) *Node`; methods `Get(path ...string) *Node`, `Str(path ...string) string`, `Has(path ...string) bool`, `List() []*Node`, `Strings() []string`
  - `func ReadBody(r *http.Request, limit int64) ([]byte, error)` (restores `r.Body`)
  - `func Form(r *http.Request, body []byte) (url.Values, error)`
  - `func TargetOp(r *http.Request) (string, bool)`
  - `func WriteXML(w http.ResponseWriter, status int, v any)` and `func WriteJSON(w http.ResponseWriter, status int, contentType string, v any)`

**Acceptance Criteria:**
- Each error style produces the wire shape the real AWS SDK parses (query `<ErrorResponse>`, EC2 `<Response><Errors>`, JSON `__type`, REST-JSON `X-Amzn-Errortype`).
- `Nest` turns flat dotted EC2 parameters into a tree whose lists are ordered numerically (`...2` before `...10`).
- `ReadBody` returns the body and leaves `r.Body` readable again; bodies over the limit are rejected.

- [ ] **Step 1: Write the failing tests**

`internal/awsapi/errors_test.go`:

```go
package awsapi

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteErrorQueryStyle(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, StyleQuery, http.StatusNotFound, "NoSuchEntity", "profile <x> not found")
	var got struct {
		Error     struct{ Type, Code, Message string }
		RequestId string
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not valid XML: %v\n%s", err, rec.Body)
	}
	if rec.Code != 404 || got.Error.Code != "NoSuchEntity" || got.Error.Message != "profile <x> not found" || got.RequestId == "" {
		t.Fatalf("status=%d body=%+v", rec.Code, got)
	}
}

func TestWriteErrorEC2Style(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, StyleEC2, http.StatusBadRequest, "InsufficientInstanceCapacity", "no capacity")
	var got struct {
		Errors struct {
			Error struct{ Code, Message string }
		}
		RequestID string
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not valid XML: %v\n%s", err, rec.Body)
	}
	if got.Errors.Error.Code != "InsufficientInstanceCapacity" || got.RequestID == "" {
		t.Fatalf("body=%+v", got)
	}
}

func TestWriteErrorJSONAndRESTJSONStyles(t *testing.T) {
	for _, s := range []Style{StyleJSON, StyleRESTJSON} {
		rec := httptest.NewRecorder()
		WriteError(rec, s, http.StatusBadRequest, "ResourceNotFoundException", "no such cluster")
		if rec.Header().Get("X-Amzn-Errortype") != "ResourceNotFoundException" {
			t.Errorf("%s: missing X-Amzn-Errortype header", s)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["message"] != "no such cluster" {
			t.Errorf("%s: body=%s err=%v", s, rec.Body, err)
		}
	}
}

func TestNewRequestIDLooksLikeAUUIDAndDiffers(t *testing.T) {
	a, b := NewRequestID(), NewRequestID()
	if len(a) != 36 || a == b {
		t.Fatalf("ids %q %q", a, b)
	}
}
```

`internal/awsapi/query_test.go`:

```go
package awsapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestNestBuildsTreeAndOrdersListsNumerically(t *testing.T) {
	v := url.Values{}
	v.Set("TargetCapacitySpecification.TotalTargetCapacity", "1")
	v.Set("LaunchTemplateConfigs.1.LaunchTemplateSpecification.LaunchTemplateName", "lt-a")
	v.Set("LaunchTemplateConfigs.1.Overrides.10.InstanceType", "c5.large")
	v.Set("LaunchTemplateConfigs.1.Overrides.2.InstanceType", "m5.large")
	n := Nest(v)

	if got := n.Str("TargetCapacitySpecification", "TotalTargetCapacity"); got != "1" {
		t.Fatalf("capacity = %q", got)
	}
	cfgs := n.Get("LaunchTemplateConfigs").List()
	if len(cfgs) != 1 {
		t.Fatalf("configs = %d", len(cfgs))
	}
	var types []string
	for _, o := range cfgs[0].Get("Overrides").List() {
		types = append(types, o.Str("InstanceType"))
	}
	if !reflect.DeepEqual(types, []string{"m5.large", "c5.large"}) { // index 2 before index 10
		t.Fatalf("override order = %v", types)
	}
	if n.Has("Nope") || !n.Has("TargetCapacitySpecification") {
		t.Fatal("Has gave the wrong answer")
	}
	if n.Str("Nope", "Deeper") != "" {
		t.Fatal("missing paths must yield empty strings, not panic")
	}
}

func TestStringsReturnsLeafListValuesInOrder(t *testing.T) {
	v := url.Values{}
	v.Set("InstanceId.1", "i-1")
	v.Set("InstanceId.2", "i-2")
	if got := Nest(v).Get("InstanceId").Strings(); !reflect.DeepEqual(got, []string{"i-1", "i-2"}) {
		t.Fatalf("got %v", got)
	}
}

func TestReadBodyRestoresBodyAndEnforcesLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Action=DescribeInstances"))
	b, err := ReadBody(r, 1024)
	if err != nil || string(b) != "Action=DescribeInstances" {
		t.Fatalf("b=%q err=%v", b, err)
	}
	again := make([]byte, 64)
	n, _ := r.Body.Read(again)
	if string(again[:n]) != "Action=DescribeInstances" {
		t.Fatalf("body not restored: %q", again[:n])
	}
	big := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 100)))
	if _, err := ReadBody(big, 10); err == nil {
		t.Fatal("expected an error for an oversized body")
	}
}

func TestFormMergesBodyAndURLQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/?Version=2016-11-15", nil)
	v, err := Form(r, []byte("Action=DescribeSubnets&SubnetId.1=subnet-1"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Get("Action") != "DescribeSubnets" || v.Get("Version") != "2016-11-15" || v.Get("SubnetId.1") != "subnet-1" {
		t.Fatalf("v=%v", v)
	}
}
```

`internal/awsapi/jsonrpc_test.go`:

```go
package awsapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTargetOpExtractsOperationName(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Amz-Target", "AWSPriceListService.GetProducts")
	if op, ok := TargetOp(r); !ok || op != "GetProducts" {
		t.Fatalf("op=%q ok=%v", op, ok)
	}
	if _, ok := TargetOp(httptest.NewRequest(http.MethodPost, "/", nil)); ok {
		t.Fatal("no target header should report ok=false")
	}
}

func TestWriteJSONAndWriteXMLSetContentTypes(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, 200, "application/x-amz-json-1.1", map[string]string{"a": "b"})
	if rec.Header().Get("Content-Type") != "application/x-amz-json-1.1" || rec.Body.String() != "{\"a\":\"b\"}\n" {
		t.Fatalf("json: ct=%q body=%q", rec.Header().Get("Content-Type"), rec.Body)
	}
	type resp struct {
		XMLName struct{} `xml:"Ping"`
		V       string   `xml:"v"`
	}
	rec = httptest.NewRecorder()
	WriteXML(rec, 200, resp{V: "x"})
	if rec.Header().Get("Content-Type") != "text/xml;charset=UTF-8" || rec.Body.String() != "<Ping><v>x</v></Ping>" {
		t.Fatalf("xml: ct=%q body=%q", rec.Header().Get("Content-Type"), rec.Body)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/awsapi/ -v`
Expected: FAIL with `undefined: WriteError` (and the other symbols).

- [ ] **Step 3: Write the implementation**

`internal/awsapi/errors.go`:

```go
// Package awsapi holds the wire-level helpers shared by every facade: error
// envelopes, query-parameter parsing, JSON-RPC helpers and SigV4 verification.
package awsapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

// Style selects which AWS error envelope to emit. Real SDKs parse each style
// differently, so using the wrong one makes errors unreadable to them.
type Style string

const (
	StyleQuery    Style = "query"    // iam, sts: <ErrorResponse><Error>
	StyleEC2      Style = "ec2"      // ec2: <Response><Errors><Error>
	StyleJSON     Style = "json"     // pricing, ssm, sqs: {"message": ...} + X-Amzn-Errortype
	StyleRESTJSON Style = "restjson" // eks: same body, application/json
)

// NewRequestID returns a random UUID-shaped request id.
func NewRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand does not fail on supported platforms
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// esc XML-escapes a string for embedding in a hand-built XML body.
func esc(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	return sb.String()
}

// WriteError writes an AWS-shaped error response.
func WriteError(w http.ResponseWriter, s Style, status int, code, message string) {
	id := NewRequestID()
	w.Header().Set("X-Amzn-Requestid", id)
	switch s {
	case StyleEC2:
		w.Header().Set("Content-Type", "text/xml;charset=UTF-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, "<Response><Errors><Error><Code>%s</Code><Message>%s</Message></Error></Errors><RequestID>%s</RequestID></Response>",
			esc(code), esc(message), id)
	case StyleQuery:
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(status)
		fmt.Fprintf(w, "<ErrorResponse><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>",
			esc(code), esc(message), id)
	default: // StyleJSON, StyleRESTJSON
		ct := "application/json"
		if s == StyleJSON {
			ct = "application/x-amz-json-1.1"
		}
		w.Header().Set("Content-Type", ct)
		// The SDKs read the error code from this header before looking at the body.
		w.Header().Set("X-Amzn-Errortype", code)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
	}
}
```

`internal/awsapi/query.go`:

```go
package awsapi

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Node is one position in a parameter tree built from flat dotted EC2/query
// parameters such as "LaunchTemplateConfigs.1.Overrides.2.InstanceType".
type Node struct {
	Val     string
	Kids    map[string]*Node
	present bool
}

func newNode(present bool) *Node { return &Node{Kids: map[string]*Node{}, present: present} }

// Nest converts flat query parameters into a tree. A parameter's first value wins.
func Nest(v url.Values) *Node {
	root := newNode(true)
	for k, vals := range v {
		if len(vals) == 0 {
			continue
		}
		cur := root
		for _, seg := range strings.Split(k, ".") {
			next, ok := cur.Kids[seg]
			if !ok {
				next = newNode(true)
				cur.Kids[seg] = next
			}
			cur = next
		}
		cur.Val = vals[0]
	}
	return root
}

// Get walks a path; a missing path yields an empty node, so calls can be chained safely.
func (n *Node) Get(path ...string) *Node {
	cur := n
	for _, seg := range path {
		next, ok := cur.Kids[seg]
		if !ok {
			return newNode(false)
		}
		cur = next
	}
	return cur
}

// Str returns the string at a path, or "" if absent.
func (n *Node) Str(path ...string) string { return n.Get(path...).Val }

// Has reports whether the path exists.
func (n *Node) Has(path ...string) bool { return n.Get(path...).present }

// List returns the children whose keys are integers, ordered numerically
// (so "2" sorts before "10", unlike a plain string sort).
func (n *Node) List() []*Node {
	type kv struct {
		idx  int
		node *Node
	}
	var items []kv
	for k, child := range n.Kids {
		if i, err := strconv.Atoi(k); err == nil {
			items = append(items, kv{i, child})
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].idx < items[b].idx })
	out := make([]*Node, len(items))
	for i, it := range items {
		out[i] = it.node
	}
	return out
}

// Strings returns the leaf values of a list node in order.
func (n *Node) Strings() []string {
	var out []string
	for _, c := range n.List() {
		out = append(out, c.Val)
	}
	return out
}

// ReadBody reads at most limit bytes and restores r.Body so later readers
// (the signature check, a reverse proxy) still see the full body.
func ReadBody(r *http.Request, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("request body exceeds %d bytes", limit)
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}

// Form parses a query-protocol request: the body (form-encoded) merged with the URL query.
func Form(r *http.Request, body []byte) (url.Values, error) {
	v, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	for k, vals := range r.URL.Query() {
		if _, exists := v[k]; !exists {
			v[k] = vals
		}
	}
	return v, nil
}
```

`internal/awsapi/jsonrpc.go`:

```go
package awsapi

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strings"
)

// TargetOp extracts the operation name from the X-Amz-Target header
// ("AWSPriceListService.GetProducts" -> "GetProducts").
func TargetOp(r *http.Request) (string, bool) {
	t := r.Header.Get("X-Amz-Target")
	if t == "" {
		return "", false
	}
	if i := strings.LastIndex(t, "."); i >= 0 {
		return t[i+1:], true
	}
	return t, true
}

// WriteJSON writes a JSON body with the given content type (JSON-RPC services use
// application/x-amz-json-1.0 or 1.1; REST-JSON uses application/json).
func WriteJSON(w http.ResponseWriter, status int, contentType string, v any) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Amzn-Requestid", NewRequestID())
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteXML writes an XML body, as query-protocol services do.
func WriteXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "text/xml;charset=UTF-8")
	w.Header().Set("X-Amzn-Requestid", NewRequestID())
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/awsapi/ -v`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add internal/awsapi
git commit -m "feat: add AWS wire helpers (error envelopes, query tree, JSON-RPC)"
```

---

### Task 4: SigV4 verification

**Files:**
- Create: `internal/awsapi/sigv4.go`
- Test: `internal/awsapi/sigv4_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Auth struct{ AccessKey, Date, Region, Service, Signature string; SignedHeaders []string }`
  - `type SecretLookup func(accessKey string) (secret string, ok bool)`
  - `func Verify(r *http.Request, body []byte, lookup SecretLookup, now time.Time, skew time.Duration) (Auth, error)`
  - `func Sign(r *http.Request, body []byte, accessKey, secret, region, service string, t time.Time)` (used by tests and test clients)
  - `var ErrMissingAuth, ErrInvalidAuth, ErrUnknownKey, ErrSignatureMismatch, ErrRequestExpired error`

**Acceptance Criteria:**
- `Verify` accepts the signature from AWS's published SigV4 example (an independent oracle, not our own signer).
- Requests signed by `Sign` verify; a tampered body, a wrong secret, an unknown access key, a stale timestamp, and a missing header are each rejected with the matching sentinel error.
- Implemented with the standard library only.

- [ ] **Step 1: Write the failing tests**

`internal/awsapi/sigv4_test.go`:

```go
package awsapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// lookupFor returns a SecretLookup that knows one key pair.
func lookupFor(access, secret string) SecretLookup {
	return func(k string) (string, bool) { return secret, k == access }
}

// TestVerifyAcceptsAWSPublishedExample uses the worked example from AWS's
// "Signature Version 4 signing process" documentation (GET iam ListUsers).
// It is an oracle independent of this package's Sign function.
func TestVerifyAcceptsAWSPublishedExample(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	r.Header.Set("X-Amz-Date", "20150830T123600Z")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, "+
		"SignedHeaders=content-type;host;x-amz-date, "+
		"Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7")
	now := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

	auth, err := Verify(r, nil, lookupFor("AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"), now, 15*time.Minute)
	if err != nil {
		t.Fatalf("AWS example rejected: %v", err)
	}
	if auth.Service != "iam" || auth.Region != "us-east-1" || auth.AccessKey != "AKIDEXAMPLE" {
		t.Fatalf("auth = %+v", auth)
	}
}

// signed builds a POST signed with Sign, the shape every SDK sends to a query-protocol service.
func signed(t *testing.T, body string, at time.Time) (*http.Request, []byte) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	Sign(r, []byte(body), "AK", "SECRET", "us-west-2", "ec2", at)
	return r, []byte(body)
}

func TestVerifyAcceptsRequestSignedBySign(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	r, body := signed(t, "Action=DescribeInstances&Version=2016-11-15", at)
	auth, err := Verify(r, body, lookupFor("AK", "SECRET"), at, 15*time.Minute)
	if err != nil || auth.Service != "ec2" || auth.Region != "us-west-2" {
		t.Fatalf("auth=%+v err=%v", auth, err)
	}
}

func TestVerifyRejections(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	good := lookupFor("AK", "SECRET")

	r, body := signed(t, "Action=DescribeInstances", at)
	if _, err := Verify(r, []byte("Action=TerminateInstances"), good, at, time.Hour); !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("tampered body: %v", err)
	}

	r, body = signed(t, "Action=DescribeInstances", at)
	if _, err := Verify(r, body, lookupFor("AK", "WRONG"), at, time.Hour); !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("wrong secret: %v", err)
	}

	r, body = signed(t, "Action=DescribeInstances", at)
	if _, err := Verify(r, body, lookupFor("OTHER", "SECRET"), at, time.Hour); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("unknown key: %v", err)
	}

	r, body = signed(t, "Action=DescribeInstances", at)
	if _, err := Verify(r, body, good, at.Add(2*time.Hour), 15*time.Minute); !errors.Is(err, ErrRequestExpired) {
		t.Errorf("stale request: %v", err)
	}

	r, body = signed(t, "Action=DescribeInstances", at)
	r.Header.Del("Authorization")
	if _, err := Verify(r, body, good, at, time.Hour); !errors.Is(err, ErrMissingAuth) {
		t.Errorf("missing header: %v", err)
	}

	r, body = signed(t, "Action=DescribeInstances", at)
	r.Header.Set("Authorization", "Bearer abc")
	if _, err := Verify(r, body, good, at, time.Hour); !errors.Is(err, ErrInvalidAuth) {
		t.Errorf("malformed header: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/awsapi/ -run 'Verify' -v`
Expected: FAIL with `undefined: Verify`.

- [ ] **Step 3: Write the implementation**

`internal/awsapi/sigv4.go`:

```go
package awsapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Sentinel errors let the gateway map each failure to the AWS error code the
// real service would return.
var (
	ErrMissingAuth       = errors.New("missing Authorization header")             // MissingAuthenticationToken
	ErrInvalidAuth       = errors.New("malformed Authorization header")           // IncompleteSignature
	ErrUnknownKey        = errors.New("unknown access key")                       // InvalidClientTokenId
	ErrSignatureMismatch = errors.New("signature does not match")                 // SignatureDoesNotMatch
	ErrRequestExpired    = errors.New("request timestamp outside allowed window") // RequestExpired
)

// Auth is the parsed credential scope of a verified request.
type Auth struct {
	AccessKey     string
	Date          string // yyyymmdd
	Region        string
	Service       string // the sigv4 signing name, e.g. "ec2"; the gateway routes on this
	SignedHeaders []string
	Signature     string
}

// SecretLookup returns the secret for an access key.
type SecretLookup func(accessKey string) (secret string, ok bool)

var authRe = regexp.MustCompile(`^AWS4-HMAC-SHA256\s+Credential=([^/]+)/(\d{8})/([^/]+)/([^/]+)/aws4_request,\s*SignedHeaders=([^,\s]+),\s*Signature=([0-9a-f]{64})$`)

const amzDateFormat = "20060102T150405Z"

// parseAuth parses an Authorization header value.
func parseAuth(h string) (Auth, error) {
	m := authRe.FindStringSubmatch(strings.TrimSpace(h))
	if m == nil {
		return Auth{}, ErrInvalidAuth
	}
	return Auth{AccessKey: m[1], Date: m[2], Region: m[3], Service: m[4],
		SignedHeaders: strings.Split(m[5], ";"), Signature: m[6]}, nil
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// uriEncode percent-encodes per the SigV4 rules: unreserved characters stay,
// everything else becomes %XX (uppercase); '/' is kept only when encodeSlash is false.
func uriEncode(s string, encodeSlash bool) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			sb.WriteByte(c)
		case c == '/' && !encodeSlash:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

// canonicalQuery sorts query parameters by key then value and encodes them.
func canonicalQuery(r *http.Request) string {
	type kv struct{ k, v string }
	var pairs []kv
	for k, vals := range r.URL.Query() {
		for _, v := range vals {
			pairs = append(pairs, kv{uriEncode(k, true), uriEncode(v, true)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

// canonicalRequest builds the string that is hashed and signed.
func canonicalRequest(r *http.Request, body []byte, signedHeaders []string, service string) string {
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	// Every service except S3 double-encodes the path.
	if service != "s3" {
		path = uriEncode(path, false)
	}
	var hdr strings.Builder
	for _, name := range signedHeaders {
		var val string
		if name == "host" {
			val = r.Host
			if val == "" {
				val = r.URL.Host
			}
		} else {
			val = strings.Join(r.Header.Values(name), ",")
		}
		// Header values are trimmed and runs of whitespace collapsed to one space.
		hdr.WriteString(name + ":" + strings.Join(strings.Fields(val), " ") + "\n")
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload == "" {
		payload = hexSHA256(body)
	}
	return strings.Join([]string{
		r.Method, path, canonicalQuery(r), hdr.String(), strings.Join(signedHeaders, ";"), payload,
	}, "\n")
}

// signature computes the hex signature for a request and credential scope.
func signature(r *http.Request, body []byte, a Auth, secret string, t time.Time) string {
	scope := fmt.Sprintf("%s/%s/%s/aws4_request", a.Date, a.Region, a.Service)
	toSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", t.UTC().Format(amzDateFormat), scope,
		hexSHA256([]byte(canonicalRequest(r, body, a.SignedHeaders, a.Service))),
	}, "\n")
	k := hmacSHA256([]byte("AWS4"+secret), a.Date)
	k = hmacSHA256(k, a.Region)
	k = hmacSHA256(k, a.Service)
	k = hmacSHA256(k, "aws4_request")
	return hex.EncodeToString(hmacSHA256(k, toSign))
}

// Verify checks a request's SigV4 signature against the secret for its access
// key. It never reads r.Body; the caller passes the already-read body.
func Verify(r *http.Request, body []byte, lookup SecretLookup, now time.Time, skew time.Duration) (Auth, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return Auth{}, ErrMissingAuth
	}
	a, err := parseAuth(h)
	if err != nil {
		return Auth{}, err
	}
	t, err := time.Parse(amzDateFormat, r.Header.Get("X-Amz-Date"))
	if err != nil || t.UTC().Format("20060102") != a.Date {
		return Auth{}, ErrInvalidAuth
	}
	if d := now.Sub(t); d > skew || d < -skew {
		return Auth{}, ErrRequestExpired
	}
	secret, ok := lookup(a.AccessKey)
	if !ok {
		return Auth{}, ErrUnknownKey
	}
	// hmac.Equal compares in constant time, so timing leaks nothing about the signature.
	want := signature(r, body, a, secret, t)
	if !hmac.Equal([]byte(want), []byte(a.Signature)) {
		return Auth{}, ErrSignatureMismatch
	}
	return a, nil
}

// Sign signs a request in place (sets X-Amz-Date and Authorization). It exists
// for tests and test clients; production traffic is signed by the AWS SDKs.
func Sign(r *http.Request, body []byte, accessKey, secret, region, service string, t time.Time) {
	t = t.UTC()
	r.Header.Set("X-Amz-Date", t.Format(amzDateFormat))
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	r.Host = host
	signedNames := []string{"host", "x-amz-date"}
	if r.Header.Get("Content-Type") != "" {
		signedNames = []string{"content-type", "host", "x-amz-date"}
	}
	a := Auth{AccessKey: accessKey, Date: t.Format("20060102"), Region: region, Service: service, SignedHeaders: signedNames}
	a.Signature = signature(r, body, a, secret, t)
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s/%s/%s/aws4_request, SignedHeaders=%s, Signature=%s",
		accessKey, a.Date, region, service, strings.Join(signedNames, ";"), a.Signature))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/awsapi/ -v`
Expected: PASS, including `TestVerifyAcceptsAWSPublishedExample`.

- [ ] **Step 5: Commit**

```bash
git add internal/awsapi/sigv4.go internal/awsapi/sigv4_test.go
git commit -m "feat: verify SigV4 signatures with the standard library"
```

### Task 5: The `aws-gateway` package

**Files:**
- Create: `internal/gateway/gateway.go`, `internal/gateway/bind.go`, `internal/gateway/proxy.go`
- Test: `internal/gateway/gateway_test.go`, `internal/gateway/bind_test.go`

**Interfaces:**
- Consumes: `ops.List.Find` and `ops.Op.Backend` (Task 2); `awsapi.Verify`, `awsapi.Sign`, `awsapi.WriteError`, `awsapi.TargetOp`, `awsapi.Form`, `awsapi.ReadBody`, `awsapi.SecretLookup` (Tasks 3 and 4).
- Produces:
  - `type Config struct{ Ops ops.List; Lookup awsapi.SecretLookup; Now func() time.Time; Skew time.Duration; Backends map[string]http.Handler /* keyed by backend name */; TraceRoutes map[string]string /* service -> backend, trace mode only */; TraceMode bool; OnCall func(service, op string, supported bool); Logf func(string, ...any) }`
  - `func (c Config) MissingBackends() []string`: backend names `ops.yaml` uses that have no handler
  - `func New(cfg Config) http.Handler`
  - `func Proxy(rawURL string) (http.Handler, error)`
  - `func CheckBind(addr string, allowAnyInterface bool) error`
  - Service names are SigV4 signing names: `ec2`, `iam`, `sts`, `sqs`, `ssm`, `pricing`, `eks`.

**Acceptance Criteria:**
- A correctly signed request is routed to the backend its operation's `ops.yaml` entry names, so one service can be split across backends; operations are identified from `Action`, `X-Amz-Target`, or (for `eks`) the REST path.
- An operation whose backend is not configured gets `503` naming the backend, and `MissingBackends` lets the binary refuse to start instead.
- Unsigned, wrongly signed, unknown-key and expired requests get `403` with the matching AWS error code and never reach a backend.
- An operation missing from `ops.yaml` gets `400 UnsupportedOperation` and never reaches a backend, unless `TraceMode` is on (which forwards it to the backend named by `TraceRoutes[service]` but still enforces authentication).
- `CheckBind` refuses non-loopback listen addresses unless explicitly opted in.

- [ ] **Step 1: Write the failing tests**

`internal/gateway/gateway_test.go`:

```go
package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/ops"
)

var testNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// backend returns a handler that records that it was called and echoes a marker.
func backend(marker string, called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		_, _ = w.Write([]byte(marker))
	})
}

func testOps() ops.List {
	mk := func(svc, name, proto string) ops.Op {
		return ops.Op{Service: svc, Name: name, Protocol: proto, Backend: svc, Fidelity: "behavior-inferred"}
	}
	return ops.List{Ops: []ops.Op{
		mk("ec2", "DescribeInstances", "query"),
		mk("pricing", "GetProducts", "json"),
		mk("eks", "DescribeCluster", "restjson"),
	}}
}

func newGateway(called map[string]*bool, trace bool, onCall func(string, string, bool)) http.Handler {
	backends := map[string]http.Handler{}
	for _, svc := range []string{"ec2", "pricing", "eks"} {
		called[svc] = new(bool)
		backends[svc] = backend(svc+"-ok", called[svc])
	}
	return New(Config{
		Ops:         testOps(),
		Lookup:      func(k string) (string, bool) { return "SECRET", k == "AK" },
		Now:         func() time.Time { return testNow },
		Backends:    backends,
		TraceRoutes: map[string]string{"ec2": "ec2"},
		TraceMode:   trace,
		OnCall:      onCall,
		Logf:        func(string, ...any) {},
	})
}

// signedPost builds a POST signed for the given service.
func signedPost(service, contentType, body string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	awsapi.Sign(r, []byte(body), "AK", "SECRET", "us-west-2", service, testNow)
	return r
}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestRoutesQueryJSONAndRESTRequestsToTheirBackends(t *testing.T) {
	called := map[string]*bool{}
	g := newGateway(called, false, nil)

	ec2 := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances&Version=2016-11-15", nil))
	if ec2.Body.String() != "ec2-ok" {
		t.Fatalf("ec2: %d %s", ec2.Code, ec2.Body)
	}

	price := do(g, signedPost("pricing", "application/x-amz-json-1.1", `{}`, map[string]string{"X-Amz-Target": "AWSPriceListService.GetProducts"}))
	if price.Body.String() != "pricing-ok" {
		t.Fatalf("pricing: %d %s", price.Code, price.Body)
	}

	get := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566/clusters/meridian", nil)
	awsapi.Sign(get, nil, "AK", "SECRET", "us-west-2", "eks", testNow)
	if eks := do(g, get); eks.Body.String() != "eks-ok" {
		t.Fatalf("eks: %d %s", eks.Code, eks.Body)
	}
}

func TestAuthFailuresAreRejectedBeforeAnyBackend(t *testing.T) {
	called := map[string]*bool{}
	g := newGateway(called, false, nil)

	unsigned := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("Action=DescribeInstances"))
	// Auth errors use the JSON envelope, where the AWS error code is the X-Amzn-Errortype header.
	if rec := do(g, unsigned); rec.Code != 403 || rec.Header().Get("X-Amzn-Errortype") != "MissingAuthenticationToken" {
		t.Errorf("unsigned: %d %s %v", rec.Code, rec.Body, rec.Header())
	}

	// Reuse the headers of a validly signed request on a different body: the
	// signature was computed over "DescribeInstances", so it must not verify.
	signedA := signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances", nil)
	tampered := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("Action=TerminateInstances"))
	tampered.Header = signedA.Header.Clone()
	if rec := do(g, tampered); rec.Code != 403 || rec.Header().Get("X-Amzn-Errortype") != "SignatureDoesNotMatch" {
		t.Errorf("tampered: %d %s %v", rec.Code, rec.Body, rec.Header())
	}

	if *called["ec2"] {
		t.Fatal("a rejected request reached the backend")
	}
}

func TestUnsupportedOperationIsRejectedAndReported(t *testing.T) {
	called := map[string]*bool{}
	type call struct {
		svc, op   string
		supported bool
	}
	var seen []call
	g := newGateway(called, false, func(s, o string, ok bool) { seen = append(seen, call{s, o, ok}) })

	rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=ImportKeyPair", nil))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "UnsupportedOperation") || *called["ec2"] {
		t.Fatalf("code=%d body=%s called=%v", rec.Code, rec.Body, *called["ec2"])
	}
	if len(seen) != 1 || seen[0] != (call{"ec2", "ImportKeyPair", false}) {
		t.Fatalf("OnCall saw %+v", seen)
	}
}

func TestTraceModeForwardsUnsupportedOperationsButStillAuthenticates(t *testing.T) {
	called := map[string]*bool{}
	g := newGateway(called, true, nil)

	if rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=ImportKeyPair", nil)); rec.Body.String() != "ec2-ok" {
		t.Fatalf("trace mode should forward: %d %s", rec.Code, rec.Body)
	}
	unsigned := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("Action=ImportKeyPair"))
	if rec := do(g, unsigned); rec.Code != 403 {
		t.Fatalf("trace mode must not bypass authentication, got %d", rec.Code)
	}
}

func TestEachOperationGoesToItsOwnBackend(t *testing.T) {
	var fromFloci, fromSim bool
	l := ops.List{Ops: []ops.Op{
		{Service: "ec2", Name: "CreateFleet", Protocol: "query", Backend: "floci", Fidelity: "behavior-inferred"},
		{Service: "ec2", Name: "DescribeSpotPriceHistory", Protocol: "query", Backend: "ec2-sim", Fidelity: "behavior-inferred"},
	}}
	g := New(Config{
		Ops: l, Lookup: func(k string) (string, bool) { return "SECRET", k == "AK" },
		Now:      func() time.Time { return testNow },
		Backends: map[string]http.Handler{"floci": backend("floci", &fromFloci), "ec2-sim": backend("sim", &fromSim)},
		Logf:     func(string, ...any) {},
	})
	if rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=CreateFleet", nil)); rec.Body.String() != "floci" {
		t.Fatalf("CreateFleet: %s", rec.Body)
	}
	if rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeSpotPriceHistory", nil)); rec.Body.String() != "sim" {
		t.Fatalf("DescribeSpotPriceHistory: %s", rec.Body)
	}
	if !fromFloci || !fromSim {
		t.Fatal("both backends should have been used")
	}
	if missing := (Config{Ops: l, Backends: map[string]http.Handler{"floci": nil}}).MissingBackends(); len(missing) != 1 || missing[0] != "ec2-sim" {
		t.Fatalf("MissingBackends = %v", missing)
	}
}

func TestServiceWithoutBackendIsUnavailable(t *testing.T) {
	g := New(Config{
		Ops:    ops.List{Ops: []ops.Op{{Service: "ec2", Name: "DescribeInstances", Protocol: "query", Backend: "ec2", Fidelity: "behavior-inferred"}}},
		Lookup: func(k string) (string, bool) { return "SECRET", k == "AK" },
		Now:    func() time.Time { return testNow },
		Logf:   func(string, ...any) {},
	})
	rec := do(g, signedPost("ec2", "application/x-www-form-urlencoded", "Action=DescribeInstances", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "ServiceUnavailable") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestProxyForwardsMethodBodyAndHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		_, _ = w.Write([]byte(r.Method + ":" + string(b[:n]) + ":" + r.Header.Get("X-Test")))
	}))
	defer upstream.Close()
	p, err := Proxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader("payload"))
	r.Header.Set("X-Test", "yes")
	if rec := do(p, r); rec.Body.String() != "POST:payload:yes" {
		t.Fatalf("got %q", rec.Body)
	}
}
```

`internal/gateway/bind_test.go`:

```go
package gateway

import "testing"

func TestCheckBind(t *testing.T) {
	cases := []struct {
		addr     string
		allowAny bool
		wantErr  bool
	}{
		{"127.0.0.1:4566", false, false},
		{"localhost:4566", false, false},
		{"[::1]:4566", false, false},
		{":4566", false, true},
		{"0.0.0.0:4566", false, true},
		{"192.168.1.20:4566", false, true},
		{"0.0.0.0:4566", true, false},
		{"not-an-address", false, true},
	}
	for _, c := range cases {
		err := CheckBind(c.addr, c.allowAny)
		if (err != nil) != c.wantErr {
			t.Errorf("CheckBind(%q, %v) error = %v, wantErr %v", c.addr, c.allowAny, err, c.wantErr)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/gateway/ -v`
Expected: FAIL with `undefined: New`.

- [ ] **Step 3: Write the implementation**

`internal/gateway/bind.go`:

```go
// Package gateway is the single AWS endpoint: it verifies SigV4, identifies the
// service and operation, enforces ops.yaml, and routes to a backend.
package gateway

import (
	"fmt"
	"net"
)

// CheckBind refuses to listen on anything but a loopback address unless the
// caller explicitly opts in. Inside a container the opt-in is normal (the
// container's network namespace is private and its published ports are bound to
// 127.0.0.1 by compose); on a bare host it exposes an AWS control plane to the LAN.
func CheckBind(addr string, allowAnyInterface bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	if allowAnyInterface {
		return nil
	}
	return fmt.Errorf("refusing to listen on %q: not a loopback address; pass -allow-any-interface only inside a container whose published ports are bound to 127.0.0.1", addr)
}
```

`internal/gateway/proxy.go`:

```go
package gateway

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Proxy returns a reverse proxy to a backend service. The incoming Authorization
// and Host headers are forwarded unchanged, because emulators such as Floci
// and Moto identify the service from the SigV4 credential scope.
func Proxy(rawURL string) (http.Handler, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	return httputil.NewSingleHostReverseProxy(u), nil
}
```

`internal/gateway/gateway.go`:

```go
package gateway

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/ops"
)

// Config wires the gateway. Each operation in ops.yaml names its backend, so one
// service can be served by several backends (for example most EC2 operations by
// Floci and a few by ec2-sim).
type Config struct {
	Ops      ops.List
	Lookup   awsapi.SecretLookup
	Now      func() time.Time        // nil means time.Now
	Skew     time.Duration           // zero means 15 minutes
	Backends map[string]http.Handler // keyed by backend name (the ops.yaml "backend" value)
	// TraceRoutes maps a SigV4 service name to the backend that receives
	// operations missing from ops.yaml, in trace mode only.
	TraceRoutes map[string]string
	// TraceMode forwards operations missing from ops.yaml instead of rejecting
	// them, so the Karpenter trace can discover the real call surface.
	// Authentication is still enforced in this mode.
	TraceMode bool
	// OnCall is told about every authenticated call (the trace recorder uses it).
	OnCall func(service, op string, supported bool)
	Logf   func(format string, args ...any)
}

const maxBody = 1 << 20 // 1 MiB is far more than any call Karpenter makes

// styleFor picks the error envelope a service's SDK expects.
func styleFor(service string) awsapi.Style {
	switch service {
	case "ec2":
		return awsapi.StyleEC2
	case "iam", "sts":
		return awsapi.StyleQuery
	case "eks":
		return awsapi.StyleRESTJSON
	default: // pricing, ssm, sqs
		return awsapi.StyleJSON
	}
}

// authError maps a verification failure to the AWS error code the real service returns.
func authError(err error) (code string) {
	switch {
	case errors.Is(err, awsapi.ErrMissingAuth):
		return "MissingAuthenticationToken"
	case errors.Is(err, awsapi.ErrInvalidAuth):
		return "IncompleteSignature"
	case errors.Is(err, awsapi.ErrUnknownKey):
		return "InvalidClientTokenId"
	case errors.Is(err, awsapi.ErrRequestExpired):
		return "RequestExpired"
	default:
		return "SignatureDoesNotMatch"
	}
}

// operation identifies the AWS operation of a request.
func operation(r *http.Request, body []byte, service string) string {
	if op, ok := awsapi.TargetOp(r); ok { // JSON-RPC: X-Amz-Target
		return op
	}
	if service == "eks" { // REST-JSON: the operation is implied by method and path
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "clusters" {
			return "DescribeCluster"
		}
		return ""
	}
	if form, err := awsapi.Form(r, body); err == nil { // query protocol: Action parameter
		return form.Get("Action")
	}
	return ""
}

// MissingBackends lists backend names that ops.yaml routes to but that are not
// configured, so a typo or a forgotten flag stops startup instead of causing 503s later.
func (c Config) MissingBackends() []string {
	var missing []string
	for _, name := range c.Ops.Backends() {
		if _, ok := c.Backends[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// New builds the gateway handler.
func New(cfg Config) http.Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Skew == 0 {
		cfg.Skew = 15 * time.Minute
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := awsapi.ReadBody(r, maxBody)
		if err != nil {
			awsapi.WriteError(w, awsapi.StyleJSON, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", err.Error())
			return
		}
		// Authenticate first: nothing about operations or backends is revealed to unauthenticated callers.
		auth, err := awsapi.Verify(r, body, cfg.Lookup, cfg.Now(), cfg.Skew)
		if err != nil {
			// The service is unknown until the credential scope is trusted, so auth errors use the JSON envelope.
			awsapi.WriteError(w, awsapi.StyleJSON, http.StatusForbidden, authError(err), err.Error())
			return
		}
		style := styleFor(auth.Service)
		op := operation(r, body, auth.Service)
		if op == "" {
			awsapi.WriteError(w, style, http.StatusBadRequest, "InvalidAction", "could not determine the requested operation")
			return
		}
		entry, supported := cfg.Ops.Find(auth.Service, op)
		if cfg.OnCall != nil {
			cfg.OnCall(auth.Service, op, supported)
		}
		if !supported && !cfg.TraceMode {
			cfg.Logf("unsupported operation %s:%s", auth.Service, op)
			awsapi.WriteError(w, style, http.StatusBadRequest, "UnsupportedOperation", auth.Service+":"+op+" is not in ops.yaml")
			return
		}
		name := entry.Backend
		if !supported { // trace mode: forward to the service's trace route
			name = cfg.TraceRoutes[auth.Service]
		}
		backend, ok := cfg.Backends[name]
		if !ok {
			awsapi.WriteError(w, style, http.StatusServiceUnavailable, "ServiceUnavailable",
				"no backend "+strconv.Quote(name)+" configured for "+auth.Service+":"+op)
			return
		}
		backend.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/gateway/ -v`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add internal/gateway
git commit -m "feat: add the aws-gateway package (SigV4, ops filter, routing, bind check)"
```

### Task 6: Trace recorder, `awscall` tool, gateway binary, pinned-image env

**Files:**
- Create: `internal/trace/trace.go`, `internal/trace/trace_test.go`
- Create: `internal/awscall/awscall.go`, `internal/awscall/awscall_test.go`, `cmd/awscall/main.go`
- Create: `internal/versions/env.go`, `internal/versions/env_test.go`, `cmd/versionsenv/main.go`
- Create: `cmd/aws-gateway/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `gateway.New`, `gateway.Proxy`, `gateway.CheckBind`, `gateway.Config.OnCall` (Task 5); `ops.Load` (Task 2); `versions.Load` (Task 1); `awsapi.Sign`, `awsapi.Verify` (Task 4).
- Produces:
  - `trace.Recorder` with `Record(service, op string, supported bool)`, `Golden() []string`, `Unsupported() []string`, `JSON() ([]byte, error)`; `trace.Diff(old, cur []string) (added, removed []string)`; `trace.WriteGolden(path string, lines []string) error`; `trace.ReadGolden(path string) ([]string, error)`
  - `awscall.BuildQuery(endpoint, service, region, action, version string, params []string, access, secret string, now time.Time) (*http.Request, []byte, error)`, `awscall.BuildJSON(endpoint, service, region, target, jsonVersion, body, access, secret string, now time.Time) (*http.Request, []byte, error)` and `awscall.BuildREST(endpoint, service, region, method, path, body, access, secret string, now time.Time) (*http.Request, []byte, error)` (for EKS)
  - `func (f versions.File) Env() []string`, producing `NAME_IMAGE=repo:version@digest` and `NAME_VERSION=version` lines
  - The `aws-gateway` binary with flags `-listen`, `-ops`, `-backend name=url` (repeatable; names are the `backend` values in `ops.yaml`), `-trace-route service=backend` (repeatable), `-allow-any-interface`, `-trace` (forward unlisted operations and record), `-record` (record without forwarding unlisted operations; the gate uses it), `-trace-out`
  - `awscall` CLI: `awscall -service ec2 -action DescribeInstances [k=v ...]` (query) and `awscall -service ssm -target AmazonSSM.PutParameter -body '{...}'` (JSON), signing with `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` from the environment.

**Acceptance Criteria:**
- `trace.Golden()` is sorted, deduplicated `service:op` lines; `Diff` reports additions and removals; golden files round-trip.
- `awscall` builds requests that `awsapi.Verify` accepts.
- `versions.File.Env()` emits a digest-pinned image reference for every image and a version for every component.
- `aws-gateway` refuses to start without credentials, refuses non-loopback binds without the opt-in flag, refuses to start if `ops.yaml` routes to a backend with no `-backend` flag, and in `-trace` or `-record` mode writes `calls.golden` and `calls.json` on shutdown.
- `go build ./...` succeeds.

- [ ] **Step 1: Write the failing tests**

`internal/trace/trace_test.go`:

```go
package trace

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestRecorderGoldenIsSortedAndDeduplicated(t *testing.T) {
	r := NewRecorder()
	r.Record("ec2", "CreateFleet", true)
	r.Record("ec2", "CreateFleet", true)
	r.Record("iam", "GetInstanceProfile", true)
	r.Record("ec2", "DescribeCapacityReservations", false)
	want := []string{"ec2:CreateFleet", "ec2:DescribeCapacityReservations", "iam:GetInstanceProfile"}
	if got := r.Golden(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Golden() = %v, want %v", got, want)
	}
	if got := r.Unsupported(); !reflect.DeepEqual(got, []string{"ec2:DescribeCapacityReservations"}) {
		t.Fatalf("Unsupported() = %v", got)
	}
}

func TestRecorderJSONIncludesCountsAndUnsupported(t *testing.T) {
	r := NewRecorder()
	r.Record("ec2", "CreateFleet", true)
	r.Record("ec2", "CreateFleet", true)
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Calls       map[string]int `json:"calls"`
		Unsupported []string       `json:"unsupported"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Calls["ec2:CreateFleet"] != 2 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestRecorderIsSafeForConcurrentUse(t *testing.T) {
	r := NewRecorder()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Record("ec2", "DescribeInstances", true) }()
	}
	wg.Wait()
	b, _ := r.JSON()
	var out struct{ Calls map[string]int }
	_ = json.Unmarshal(b, &out)
	if out.Calls["ec2:DescribeInstances"] != 50 {
		t.Fatalf("count = %d, want 50", out.Calls["ec2:DescribeInstances"])
	}
}

func TestDiffReportsAddedAndRemoved(t *testing.T) {
	added, removed := Diff([]string{"a:1", "a:2"}, []string{"a:2", "a:3"})
	if !reflect.DeepEqual(added, []string{"a:3"}) || !reflect.DeepEqual(removed, []string{"a:1"}) {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
}

func TestGoldenFileRoundTripSkipsComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.golden")
	lines := []string{"ec2:CreateFleet", "iam:GetInstanceProfile"}
	if err := WriteGolden(path, lines); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGolden(path)
	if err != nil || !reflect.DeepEqual(got, lines) {
		t.Fatalf("got=%v err=%v", got, err)
	}
}
```

`internal/awscall/awscall_test.go`:

```go
package awscall

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

var now = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func lookup(k string) (string, bool) { return "SECRET", k == "AK" }

func TestBuildQuerySignsAVerifiableRequest(t *testing.T) {
	r, body, err := BuildQuery("http://127.0.0.1:4566", "ec2", "us-west-2", "CreateVpc", "2016-11-15",
		[]string{"CidrBlock=10.0.0.0/16"}, "AK", "SECRET", now)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := awsapi.Verify(r, body, lookup, now, time.Minute)
	if err != nil || auth.Service != "ec2" {
		t.Fatalf("auth=%+v err=%v", auth, err)
	}
	form, _ := awsapi.Form(r, body)
	if form.Get("Action") != "CreateVpc" || form.Get("Version") != "2016-11-15" || form.Get("CidrBlock") != "10.0.0.0/16" {
		t.Fatalf("form=%v", form)
	}
	if r.Method != http.MethodPost {
		t.Fatalf("method = %s", r.Method)
	}
}

func TestBuildQueryRejectsMalformedParam(t *testing.T) {
	if _, _, err := BuildQuery("http://x", "ec2", "r", "A", "v", []string{"novalue"}, "AK", "S", now); err == nil {
		t.Fatal("expected an error for a param without '='")
	}
}

func TestBuildJSONSetsTargetAndContentType(t *testing.T) {
	r, body, err := BuildJSON("http://127.0.0.1:4566", "ssm", "us-west-2", "AmazonSSM.PutParameter", "1.1",
		`{"Name":"/a","Value":"b","Type":"String"}`, "AK", "SECRET", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := awsapi.Verify(r, body, lookup, now, time.Minute); err != nil {
		t.Fatalf("verify: %v", err)
	}
	b, _ := io.ReadAll(r.Body)
	if r.Header.Get("X-Amz-Target") != "AmazonSSM.PutParameter" ||
		r.Header.Get("Content-Type") != "application/x-amz-json-1.1" ||
		!strings.Contains(string(b), `"Name":"/a"`) {
		t.Fatalf("headers=%v body=%s", r.Header, b)
	}
}

func TestBuildRESTSignsAGetAndAPost(t *testing.T) {
	get, body, err := BuildREST("http://127.0.0.1:4566", "eks", "us-west-2", http.MethodGet, "/clusters/meridian-g0", "", "AK", "SECRET", now)
	if err != nil {
		t.Fatal(err)
	}
	if auth, err := awsapi.Verify(get, body, lookup, now, time.Minute); err != nil || auth.Service != "eks" {
		t.Fatalf("GET: auth=%+v err=%v", auth, err)
	}
	post, body, err := BuildREST("http://127.0.0.1:4566", "eks", "us-west-2", http.MethodPost, "/clusters", `{"name":"x"}`, "AK", "SECRET", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := awsapi.Verify(post, body, lookup, now, time.Minute); err != nil {
		t.Fatalf("POST: %v", err)
	}
	if post.Header.Get("Content-Type") != "application/json" || get.Header.Get("Content-Type") != "" {
		t.Fatalf("content types: %q / %q", post.Header.Get("Content-Type"), get.Header.Get("Content-Type"))
	}
}
```

`internal/versions/env_test.go`:

```go
package versions

import (
	"reflect"
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/trace/ ./internal/awscall/ ./internal/versions/ -run 'Recorder|Diff|Golden|Build|Env' -v`
Expected: FAIL with `undefined: NewRecorder` (and the other symbols).

- [ ] **Step 3: Write the implementations**

`internal/trace/trace.go`:

```go
// Package trace records which AWS operations a client (Karpenter) actually
// calls, so the supported-operation list is derived from evidence.
package trace

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
)

// Recorder counts calls by "service:operation". It is safe for concurrent use.
type Recorder struct {
	mu          sync.Mutex
	counts      map[string]int
	unsupported map[string]bool
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder {
	return &Recorder{counts: map[string]int{}, unsupported: map[string]bool{}}
}

// Record matches gateway.Config.OnCall.
func (r *Recorder) Record(service, op string, supported bool) {
	key := service + ":" + op
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[key]++
	if !supported {
		r.unsupported[key] = true
	}
}

// Golden returns every distinct call, sorted (counts are omitted because they vary run to run).
func (r *Recorder) Golden() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return sortedKeys(r.counts)
}

// Unsupported returns the calls that are not in ops.yaml, sorted.
func (r *Recorder) Unsupported() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := map[string]int{}
	for k := range r.unsupported {
		m[k] = 1
	}
	return sortedKeys(m)
}

// JSON renders counts and the unsupported list for humans and scripts.
func (r *Recorder) JSON() ([]byte, error) {
	r.mu.Lock()
	counts := make(map[string]int, len(r.counts))
	for k, v := range r.counts {
		counts[k] = v
	}
	r.mu.Unlock()
	return json.MarshalIndent(struct {
		Calls       map[string]int `json:"calls"`
		Unsupported []string       `json:"unsupported"`
	}{counts, r.Unsupported()}, "", "  ")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Diff compares two golden call lists.
func Diff(old, cur []string) (added, removed []string) {
	o, c := map[string]bool{}, map[string]bool{}
	for _, s := range old {
		o[s] = true
	}
	for _, s := range cur {
		c[s] = true
		if !o[s] {
			added = append(added, s)
		}
	}
	for _, s := range old {
		if !c[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

const goldenHeader = "# Karpenter AWS call surface (service:operation), sorted, counts omitted.\n" +
	"# Regenerate with `make trace`; review any diff by hand.\n"

// WriteGolden writes a golden call list.
func WriteGolden(path string, lines []string) error {
	return os.WriteFile(path, []byte(goldenHeader+strings.Join(lines, "\n")+"\n"), 0o644)
}

// ReadGolden reads a golden call list, skipping comments and blank lines.
func ReadGolden(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}
```

`internal/awscall/awscall.go`:

```go
// Package awscall builds signed AWS requests for seeding and debugging the
// simulated plane (a tiny stand-in for the AWS CLI, with no dependencies).
package awscall

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

// BuildQuery builds a signed query-protocol POST (EC2, IAM, STS). params are "key=value" pairs.
func BuildQuery(endpoint, service, region, action, version string, params []string, access, secret string, now time.Time) (*http.Request, []byte, error) {
	form := url.Values{"Action": {action}, "Version": {version}}
	for _, p := range params {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, nil, fmt.Errorf("parameter %q must look like key=value", p)
		}
		form.Set(k, v)
	}
	body := []byte(form.Encode())
	r, err := http.NewRequest(http.MethodPost, strings.TrimRight(endpoint, "/")+"/", strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	awsapi.Sign(r, body, access, secret, region, service, now)
	return r, body, nil
}

// BuildJSON builds a signed JSON-RPC POST (SSM, SQS, Pricing). jsonVersion is "1.0" or "1.1".
func BuildJSON(endpoint, service, region, target, jsonVersion, body, access, secret string, now time.Time) (*http.Request, []byte, error) {
	r, err := http.NewRequest(http.MethodPost, strings.TrimRight(endpoint, "/")+"/", strings.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	r.Header.Set("Content-Type", "application/x-amz-json-"+jsonVersion)
	r.Header.Set("X-Amz-Target", target)
	awsapi.Sign(r, []byte(body), access, secret, region, service, now)
	return r, []byte(body), nil
}

// BuildREST builds a signed REST-JSON request (EKS). path starts with "/".
func BuildREST(endpoint, service, region, method, path, body, access, secret string, now time.Time) (*http.Request, []byte, error) {
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	r, err := http.NewRequest(method, strings.TrimRight(endpoint, "/")+path, rd)
	if err != nil {
		return nil, nil, err
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	awsapi.Sign(r, []byte(body), access, secret, region, service, now)
	return r, []byte(body), nil
}
```

`cmd/awscall/main.go`:

```go
// Command awscall sends one signed request to the simulated AWS endpoint.
//
//	awscall -service ec2 -action CreateVpc CidrBlock=10.0.0.0/16
//	awscall -service ssm -target AmazonSSM.PutParameter -body '{"Name":"/a","Value":"b","Type":"String"}'
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/tazzledazzle/meridian/internal/awscall"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:4566", "AWS endpoint (the aws-gateway)")
	service := flag.String("service", "", "SigV4 service name, e.g. ec2, iam, ssm")
	region := flag.String("region", envOr("AWS_REGION", "us-west-2"), "signing region")
	action := flag.String("action", "", "query-protocol action name")
	version := flag.String("version", "2016-11-15", "query-protocol API version")
	target := flag.String("target", "", "JSON-RPC target, e.g. AmazonSSM.PutParameter")
	jsonVersion := flag.String("json-version", "1.1", "JSON-RPC version: 1.0 or 1.1")
	body := flag.String("body", "{}", "JSON-RPC request body")
	flag.Parse()

	access, secret := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
	if access == "" || secret == "" || *service == "" {
		log.Fatal("need -service plus AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY (run `make init`, then `set -a; . ./.env; set +a`)")
	}

	var req *http.Request
	var err error
	if *target != "" {
		req, _, err = awscall.BuildJSON(*endpoint, *service, *region, *target, *jsonVersion, *body, access, secret, time.Now())
	} else {
		req, _, err = awscall.BuildQuery(*endpoint, *service, *region, *action, *version, flag.Args(), access, secret, time.Now())
	}
	if err != nil {
		log.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	fmt.Printf("HTTP %d\n%s\n", resp.StatusCode, out)
	if resp.StatusCode >= 400 {
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
```

`internal/versions/env.go`:

```go
package versions

import (
	"fmt"
	"sort"
	"strings"
)

// Env renders the matrix as environment lines for Compose and the shell scripts:
// NAME_VERSION for every component and NAME_IMAGE (repo:version@digest) for images.
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
```

`cmd/versionsenv/main.go`:

```go
// Command versionsenv prints versions.yaml as KEY=VALUE lines (see `make env-images`).
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/tazzledazzle/meridian/internal/versions"
)

func main() {
	path := "versions.yaml"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	f, err := versions.Load(path)
	if err != nil {
		log.Fatal(err)
	}
	// Refuse to emit anything from an unpinned file: Compose would silently pull "latest".
	if err := f.Validate(); err != nil {
		log.Fatalf("versions.yaml is not fully pinned:\n%v", err)
	}
	for _, line := range f.Env() {
		fmt.Println(line)
	}
}
```

`cmd/aws-gateway/main.go`:

```go
// Command aws-gateway is the single AWS endpoint for the simulated plane.
package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tazzledazzle/meridian/internal/gateway"
	"github.com/tazzledazzle/meridian/internal/ops"
	"github.com/tazzledazzle/meridian/internal/trace"
)

// backendFlag collects repeated name=value flags.
type backendFlag map[string]string

func (b backendFlag) String() string { return fmt.Sprint(map[string]string(b)) }

func (b backendFlag) Set(v string) error {
	name, url, ok := strings.Cut(v, "=")
	if !ok || name == "" || url == "" {
		return fmt.Errorf("want service=url, got %q", v)
	}
	b[name] = url
	return nil
}

// writeTrace persists the recorded call surface.
func writeTrace(rec *trace.Recorder, dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("trace: %v", err)
		return
	}
	if err := trace.WriteGolden(filepath.Join(dir, "calls.golden"), rec.Golden()); err != nil {
		log.Printf("trace: %v", err)
	}
	if b, err := rec.JSON(); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "calls.json"), b, 0o644)
	}
}

func main() {
	listen := flag.String("listen", "127.0.0.1:4566", "address to listen on")
	opsPath := flag.String("ops", "ops.yaml", "path to ops.yaml")
	allowAny := flag.Bool("allow-any-interface", false, "allow a non-loopback listen address (containers only; published ports must stay on 127.0.0.1)")
	traceMode := flag.Bool("trace", false, "forward operations missing from ops.yaml and record every call")
	record := flag.Bool("record", false, "record every call without forwarding unsupported operations (the gate uses this; -trace implies it)")
	traceOut := flag.String("trace-out", "trace/raw", "directory for calls.golden and calls.json (trace and record modes)")
	backends := backendFlag{}
	flag.Var(backends, "backend", "name=url, repeatable; names are the \"backend\" values in ops.yaml (e.g. -backend floci=http://floci:4566)")
	traceRoutes := backendFlag{}
	flag.Var(traceRoutes, "trace-route", "service=backend, repeatable; trace mode sends operations missing from ops.yaml for that service to the named backend")
	flag.Parse()

	// No default credentials, ever: the keys come from `make init`.
	access, secret := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
	if access == "" || secret == "" {
		log.Fatal("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required; run `make init`")
	}
	if err := gateway.CheckBind(*listen, *allowAny); err != nil {
		log.Fatal(err)
	}
	if *allowAny {
		log.Printf("WARNING: listening on %s; make sure the published port is bound to 127.0.0.1", *listen)
	}
	list, err := ops.Load(*opsPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := list.Validate(); err != nil {
		log.Fatal(err)
	}
	handlers := map[string]http.Handler{}
	for name, url := range backends {
		h, err := gateway.Proxy(url)
		if err != nil {
			log.Fatalf("backend %s: %v", name, err)
		}
		handlers[name] = h
	}

	rec := trace.NewRecorder()
	cfg := gateway.Config{
		Ops: list,
		// Constant-time comparison so response timing reveals nothing about the key.
		Lookup: func(k string) (string, bool) {
			if subtle.ConstantTimeCompare([]byte(k), []byte(access)) == 1 {
				return secret, true
			}
			return "", false
		},
		Backends:    handlers,
		TraceRoutes: traceRoutes,
		TraceMode:   *traceMode,
	}
	// A route to a backend nobody configured would turn into runtime 503s; refuse to start instead.
	if missing := cfg.MissingBackends(); len(missing) > 0 {
		log.Fatalf("ops.yaml routes to backends that have no -backend flag: %v", missing)
	}
	for svc, name := range traceRoutes {
		if _, ok := handlers[name]; !ok {
			log.Fatalf("-trace-route %s=%s names a backend that has no -backend flag", svc, name)
		}
	}
	recording := *traceMode || *record
	if recording {
		cfg.OnCall = rec.Record
	}
	srv := &http.Server{Addr: *listen, Handler: gateway.New(cfg), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("aws-gateway listening on %s (trace=%v)", *listen, *traceMode)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	if recording { // flush periodically so a killed container still leaves a trace behind
		go func() {
			for range time.Tick(10 * time.Second) {
				writeTrace(rec, *traceOut)
			}
		}()
	}
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if recording {
		writeTrace(rec, *traceOut)
	}
}
```

- [ ] **Step 4: Update the Makefile and build everything**

Append to `Makefile`:

```make
# Renders versions.yaml as KEY=VALUE lines for Compose and the shell scripts.
env-images:
	go run ./cmd/versionsenv > .env.images

build:
	mkdir -p bin && go build -o bin/ ./cmd/...
```

Add `.env.images` to `.gitignore` (it is generated). Then:

Run: `go build ./... && go test ./internal/... -v`
Expected: build succeeds; all tests PASS (the `TestRepo*` tests pass once `versions.yaml` and `ops.yaml` exist, as in Tasks 1 and 2).

- [ ] **Step 5: Commit**

```bash
git add internal cmd Makefile .gitignore
git commit -m "feat: add trace recorder, awscall tool, aws-gateway binary and image env generator"
```

### Task 7: S0 harness (Floci behind the gateway, k3d, real Karpenter, seeding)

**Why this task exists:** S0 asks whether Floci can replace the custom components. To answer it we need Floci running behind the same `aws-gateway` that everything else uses, real Karpenter pointed at that gateway, and the data Karpenter expects already seeded. The gateway runs in trace mode, so the first thing the harness records is the AWS call surface Karpenter actually uses.

**Files:**
- Create: `deploy/Dockerfile.service`, `deploy/compose.s0.yaml`, `deploy/karpenter-values.yaml`
- Create: `scripts/kc.sh`, `scripts/k3d-up.sh`, `scripts/karpenter-install.sh`, `scripts/seed-floci.sh`, `scripts/trace-show.sh`
- Create: `scripts/profiles.py`, `scripts/test_profiles.py`
- Modify: `Makefile`, `.gitignore`

**Interfaces:**
- Consumes: `bin/aws-gateway` flags from Task 6 (`-listen`, `-ops`, `-backend name=url`, `-trace-route service=backend`, `-allow-any-interface`, `-trace`, `-trace-out`); `.env` (Task 1); `.env.images` with `GOLANG_IMAGE`, `STATIC_IMAGE`, `FLOCI_IMAGE`, `K3S_IMAGE`, `K3S_VERSION`, `KWOK_IMAGE`, `KARPENTER_VERSION` (Tasks 1 and 6); `bin/linux/awscall` (this task's Makefile target).
- Produces: Compose profiles named after the fallback backends (`ec2-sim`, `pricing-facade`, `eks-facade`, `moto`), started only when `ops.yaml` routes an operation to them; Docker network `meridian`; containers `floci` (no published port) and `aws-gateway` (published on `127.0.0.1:4566`); k3d cluster `meridian-g0` on that network; Karpenter in namespace `kube-system`; `make s0-up`, `make s0-down`, `make seed`, `make trace-show`; `trace/raw/calls.golden` and `trace/raw/calls.json` written by the gateway.

**Acceptance Criteria:**
- `make s0-up` from a clean checkout (after `make init env-images build-linux`) starts Floci, the gateway in trace mode, a k3d cluster and a Karpenter pod.
- The Karpenter pod's AWS endpoint is the gateway, not Floci.
- `make trace-show` prints the recorded `service:operation` list, marking each as supported or unsupported by `ops.yaml`.
- `make seed` is idempotent and talks to Floci directly, because the setup calls (create VPC, register image) are not in the supported-operation list.
- The gateway publishes only on `127.0.0.1`; Floci publishes nothing.

- [ ] **Step 1: Add the Linux build target**

`versions.yaml` already pins Floci (Task 1). Containers need Linux binaries even when the host is not Linux, so replace the `build` target in `Makefile` and add `build-linux`:

```make
build:
	mkdir -p bin && go build -o bin/ ./cmd/...

# Static Linux binaries for the host's CPU architecture; scripts mount them into containers.
build-linux:
	mkdir -p bin/linux && CGO_ENABLED=0 GOOS=linux GOARCH=$(shell go env GOARCH) go build -trimpath -o bin/linux/ ./cmd/...
```

Run: `make env-images && grep FLOCI_IMAGE .env.images`
Expected: one line `FLOCI_IMAGE=floci/floci:<x.y.z>@sha256:...`.

- [ ] **Step 2: Write the Dockerfile that builds any one command**

`deploy/Dockerfile.service`:

```dockerfile
# One Dockerfile for every Go service: docker build --build-arg CMD=aws-gateway ...
ARG GOLANG_IMAGE
ARG STATIC_IMAGE
FROM ${GOLANG_IMAGE} AS build
ARG CMD
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
# CGO off so the binary runs in the static (distroless) image.
RUN CGO_ENABLED=0 go build -trimpath -o /out/service ./cmd/${CMD}

FROM ${STATIC_IMAGE}
COPY --from=build /out/service /service
# ops.yaml is read at startup (the gateway's -ops flag).
COPY ops.yaml /ops.yaml
USER 65532:65532
ENTRYPOINT ["/service"]
```

- [ ] **Step 3: Write the Compose file**

`deploy/compose.s0.yaml`:

```yaml
# S0 stack: Floci behind our aws-gateway, with the gateway in trace mode.
# Run with: docker compose --env-file .env.images --env-file .env -f deploy/compose.s0.yaml up -d --build
name: meridian-g0

networks:
  meridian:
    name: meridian            # k3d joins this network so pods can resolve "aws-gateway"

services:
  floci:
    image: ${FLOCI_IMAGE}     # pinned by digest in versions.yaml
    networks: [meridian]
    environment:
      FLOCI_DEFAULT_REGION: us-west-2     # the region Karpenter is configured for
      FLOCI_HOSTNAME: floci               # URLs Floci returns (SQS QueueUrl) must resolve from other containers
    volumes:
      # Floci runs EC2 instances and EKS clusters as Docker containers, so it needs the Docker socket.
      # That is root-equivalent access to the host's Docker daemon: this stack is for local development
      # only, publishes no ports, and ADR-0002 records the risk.
      - /var/run/docker.sock:/var/run/docker.sock
    # No published port: only the gateway (and the seeding helper on this network) can reach Floci.
    expose: ["4566"]

  aws-gateway:
    build:
      context: ..
      dockerfile: deploy/Dockerfile.service
      args:
        CMD: aws-gateway
        GOLANG_IMAGE: ${GOLANG_IMAGE}
        STATIC_IMAGE: ${STATIC_IMAGE}
    networks: [meridian]
    environment:
      AWS_ACCESS_KEY_ID: ${AWS_ACCESS_KEY_ID:?run make init}
      AWS_SECRET_ACCESS_KEY: ${AWS_SECRET_ACCESS_KEY:?run make init}
    command:
      - -listen=0.0.0.0:4566
      - -allow-any-interface          # needed inside a container; the published port below stays on loopback
      - -ops=/ops.yaml
      - -trace
      - -trace-out=/trace
      # Every backend name ops.yaml can use is declared here. Backends that are not running are never
      # dialed; only the profiles named by `scripts/profiles.py ops.yaml` are started.
      - -backend=floci=http://floci:4566
      - -backend=ec2-sim=http://ec2-sim:4570
      - -backend=pricing-facade=http://pricing-facade:4571
      - -backend=eks-facade=http://eks-facade:4572
      - -backend=moto=http://moto:5000
      # Operations Karpenter calls that ops.yaml does not list yet are forwarded to Floci and recorded.
      - -trace-route=ec2=floci
      - -trace-route=iam=floci
      - -trace-route=sts=floci
      - -trace-route=sqs=floci
      - -trace-route=ssm=floci
      - -trace-route=pricing=floci
      - -trace-route=eks=floci
    ports:
      - "127.0.0.1:4566:4566"         # host loopback only (spec: Security)
    volumes:
      - ../trace/raw:/trace           # the recorded call list lands here
    user: "0:0"                       # lets the container write the bind-mounted trace directory
    depends_on: [floci]

  # ---- Fallback backends: each starts only if S0 moved operations to it (profile name = backend name) ----
  ec2-sim:
    profiles: [ec2-sim]
    build:
      context: ..
      dockerfile: deploy/Dockerfile.service
      args: {CMD: ec2-sim, GOLANG_IMAGE: "${GOLANG_IMAGE}", STATIC_IMAGE: "${STATIC_IMAGE}"}
    networks: [meridian]
    command: ["-listen=0.0.0.0:4570", "-allow-any-interface"]
    expose: ["4570"]            # internal only: the simulator trusts the gateway, so it is never published

  pricing-facade:
    profiles: [pricing-facade]
    build:
      context: ..
      dockerfile: deploy/Dockerfile.service
      args: {CMD: pricing-facade, GOLANG_IMAGE: "${GOLANG_IMAGE}", STATIC_IMAGE: "${STATIC_IMAGE}"}
    networks: [meridian]
    command: ["-listen=0.0.0.0:4571", "-allow-any-interface"]
    expose: ["4571"]

  eks-facade:
    profiles: [eks-facade]
    build:
      context: ..
      dockerfile: deploy/Dockerfile.service
      args: {CMD: eks-facade, GOLANG_IMAGE: "${GOLANG_IMAGE}", STATIC_IMAGE: "${STATIC_IMAGE}"}
    networks: [meridian]
    command: ["-listen=0.0.0.0:4572", "-allow-any-interface", "-cluster-name=meridian-g0"]
    environment:
      K3S_VERSION: ${K3S_VERSION:-}     # the reported EKS version is derived from the pinned k3s
    expose: ["4572"]

  moto:
    profiles: [moto]
    image: ${MOTO_IMAGE:-}              # set only when versions.yaml pins moto (Task 14)
    networks: [meridian]
    expose: ["5000"]
```

The file also declares the four fallback backends under Compose **profiles**. They do nothing until Tasks 11 to 14 supply their code, and they start only when `ops.yaml` routes an operation to them, which `scripts/profiles.py` works out:

`scripts/profiles.py`:

```python
#!/usr/bin/env python3
"""Prints the Compose profiles to start: the distinct non-floci backends named in ops.yaml.

  COMPOSE_PROFILES=$(scripts/profiles.py ops.yaml) docker compose ... up -d
  scripts/profiles.py --backend-of ec2:DescribeImages ops.yaml     print the backend serving one operation

Profile names equal backend names (ec2-sim, pricing-facade, eks-facade, moto), so an operation moved to a
fallback in ops.yaml is the only thing that makes its service start.
"""
import re
import sys

BACKEND = re.compile(r"backend:\s*([a-z][a-z0-9-]*)")


def profiles(text):
    return sorted({m.group(1) for m in BACKEND.finditer(text)} - {"floci"})


OP = re.compile(r"service:\s*(\w+),\s*name:\s*(\w+),.*?backend:\s*([a-z][a-z0-9-]*)")


def backend_of(text, op):
    """Returns the backend that ops.yaml routes 'service:Name' to, or '' if the operation is not listed."""
    for m in OP.finditer(text):
        if f"{m.group(1)}:{m.group(2)}" == op:
            return m.group(3)
    return ""


if __name__ == "__main__":
    if sys.argv[1] == "--backend-of":
        with open(sys.argv[3]) as f:
            print(backend_of(f.read(), sys.argv[2]))
    else:
        with open(sys.argv[1]) as f:
            print(",".join(profiles(f.read())))
```

`scripts/test_profiles.py`:

```python
import unittest

import profiles


class ProfilesTests(unittest.TestCase):
    def test_floci_only_starts_no_fallbacks(self):
        self.assertEqual(profiles.profiles("- {service: ec2, name: A, protocol: query, backend: floci, fidelity: x}"), [])

    def test_distinct_sorted_and_floci_excluded(self):
        text = "\n".join(
            f"- {{service: s, name: {n}, protocol: query, backend: {b}, fidelity: x}}"
            for n, b in [("A", "floci"), ("B", "moto"), ("C", "ec2-sim"), ("D", "moto")]
        )
        self.assertEqual(profiles.profiles(text), ["ec2-sim", "moto"])


class BackendOfTests(unittest.TestCase):
    TEXT = ("- {service: ec2, name: DescribeImages, protocol: query, backend: ec2-sim, fidelity: x}\n"
            "- {service: ec2, name: CreateFleet, protocol: query, backend: floci, fidelity: x}\n")

    def test_finds_the_backend_of_one_operation(self):
        self.assertEqual(profiles.backend_of(self.TEXT, "ec2:DescribeImages"), "ec2-sim")
        self.assertEqual(profiles.backend_of(self.TEXT, "ec2:CreateFleet"), "floci")

    def test_unknown_operation_gives_empty(self):
        self.assertEqual(profiles.backend_of(self.TEXT, "ec2:Nope"), "")


if __name__ == "__main__":
    unittest.main()
```

Run: `cd scripts && python3 -W error -m unittest test_profiles -v`
Expected: 2 tests PASS.

- [ ] **Step 4: Write the cluster scripts**

Every `kubectl` call in this plan goes through `scripts/kc.sh`, which runs the `kubectl` bundled in the k3s server container. The host needs no kubeconfig and no published API port, so the same scripts work on the isolated network the gate uses (Task 15), where Docker publishes no ports to the host.

`scripts/kc.sh`:

```bash
#!/usr/bin/env bash
# Runs kubectl inside the k3d server container: kc.sh get nodes, kc.sh apply -f - < file.yaml
set -euo pipefail
# -i passes stdin through (for "apply -f -"); the k3s image ships kubectl and its kubeconfig.
exec docker exec -i k3d-meridian-g0-server-0 kubectl "$@"
```

`scripts/k3d-up.sh`:

```bash
#!/usr/bin/env bash
# Creates the G0 k3d cluster on the Docker network "meridian" so pods can resolve the gateway by name.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env.images; set +a

command -v k3d >/dev/null || { echo "k3d is required (see versions.yaml for the pinned version)"; exit 1; }

# The network is created by Compose (make s0-up and gate-up run Compose first).
NET="${MERIDIAN_NET:-meridian}"   # the gate uses the isolated network meridian-gate
docker network inspect "$NET" >/dev/null 2>&1 || { echo "network '$NET' missing: run docker compose first"; exit 1; }

if k3d cluster list meridian-g0 >/dev/null 2>&1; then
  echo "cluster meridian-g0 already exists"
else
  # --no-lb: no host port is published by the cluster; --network: shares the Compose network.
  k3d cluster create meridian-g0 \
    --image "${K3S_IMAGE}" \
    --network "$NET" \
    --servers 1 --agents 0 \
    --no-lb \
    --k3s-arg "--disable=traefik@server:0" \
    --wait
fi
scripts/kc.sh wait --for=condition=Ready node --all --timeout=120s
```

- [ ] **Step 5: Write the Karpenter values and install script**

`deploy/karpenter-values.yaml`:

```yaml
# Real Karpenter, unmodified, with its AWS SDK pointed at the simulated gateway.
replicas: 1
imagePullPolicy: IfNotPresent                 # the gate cluster has no route to a registry; images are preloaded
logLevel: debug                              # debug logs show every AWS call attempt
settings:
  clusterName: meridian-g0
  interruptionQueue: meridian-g0-interruption   # SQS queue seeded in Task 8
controller:
  env:
    - name: AWS_ENDPOINT_URL                 # SDK-wide endpoint override: every AWS service goes to the gateway
      value: http://aws-gateway:4566
    - name: AWS_REGION
      value: us-west-2
    - name: AWS_ACCESS_KEY_ID
      valueFrom:
        secretKeyRef: {name: aws-creds, key: AWS_ACCESS_KEY_ID}
    - name: AWS_SECRET_ACCESS_KEY
      valueFrom:
        secretKeyRef: {name: aws-creds, key: AWS_SECRET_ACCESS_KEY}
  resources:
    requests: {cpu: 100m, memory: 256Mi}
    limits: {memory: 512Mi}
```

`scripts/karpenter-install.sh`:

```bash
#!/usr/bin/env bash
# Installs the pinned Karpenter chart into the G0 cluster with the local gateway credentials.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env; . ./.env.images; set +a
command -v helm >/dev/null || { echo "helm is required"; exit 1; }
KC=scripts/kc.sh

# Credentials come from .env (never committed). The Secret lives only in this local cluster.
$KC -n kube-system create secret generic aws-creds \
  --from-literal=AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
  --from-literal=AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
  --dry-run=client -o yaml | $KC apply -f -

# Render on the host, apply inside the cluster: helm never needs to reach the API server.
# --include-crds: the chart ships Karpenter's CRDs. --kube-version avoids asking a cluster for it.
# Server-side apply because Karpenter's CRDs are larger than client-side apply's annotation limit.
helm template karpenter oci://public.ecr.aws/karpenter/karpenter \
  --version "${KARPENTER_VERSION}" \
  --namespace kube-system \
  --kube-version "${K3S_VERSION%%+*}" \
  --include-crds \
  --values deploy/karpenter-values.yaml \
  | $KC apply --server-side --force-conflicts -f -
# Karpenter retries after a failed AWS call, so a CrashLoop here is expected until later tasks add services.
echo "installed; watch with: scripts/kc.sh -n kube-system logs deploy/karpenter -f"
```

- [ ] **Step 6: Write the seeding script**

`scripts/seed-floci.sh`:

```bash
#!/usr/bin/env bash
# Seeds the data Karpenter expects directly into Floci: an SSM AMI parameter, the node IAM role, the SQS
# interruption queue, a VPC with two tagged subnets and a tagged security group, and a registered AMI.
# Seeding talks to Floci itself (not the gateway) because these setup calls are not part of the supported
# operation list; they are what a real account would already contain.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env; . ./.env.images; set +a
NET="${MERIDIAN_NET:-meridian}"   # the gate sets meridian-gate
export AWS_REGION=us-west-2
FLOCI="http://floci:4566"
CLUSTER=meridian-g0
AMI=ami-0a1b2c3d4e5f60718   # used only if the backend cannot register an image (an S0 finding)

# Runs awscall on the Compose network, where "floci" resolves. bin/linux/awscall is mounted read-only.
call() { docker run --rm --network "$NET" -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_REGION \
  -v "$PWD/bin/linux/awscall:/awscall:ro" --entrypoint /awscall "${STATIC_IMAGE}" -endpoint "$FLOCI" "$@"; }

# Kubernetes minor from the pinned k3s version, e.g. v1.32.4+k3s1 -> 1.32. Karpenter's alias "al2023@latest"
# resolves to an SSM path containing this minor.
MINOR=$(echo "$K3S_VERSION" | sed -E 's/^v?([0-9]+\.[0-9]+).*/\1/')

# 0. The AMI. Floci assigns the ID, so register first and use whatever it returns; the SSM parameter must point at an
# image the EC2 backend can describe. If S0 moved image lookups to ec2-sim, it serves the fixed ID instead.
# The ID is saved for seed-moto.sh and for the gate test.
if [ "$(python3 scripts/profiles.py --backend-of ec2:DescribeImages ops.yaml)" != "ec2-sim" ]; then
  REGISTERED=$(call -service ec2 -action RegisterImage Name=meridian-al2023-$$ Architecture=x86_64 RootDeviceName=/dev/xvda \
    VirtualizationType=hvm 2>/dev/null | sed -n 's:.*<imageId>\(.*\)</imageId>.*:\1:p' | head -1 || true)
  if [ -n "$REGISTERED" ]; then AMI="$REGISTERED"; else echo "note: RegisterImage failed; S0 must record how Floci exposes AMIs"; fi
fi
echo "$AMI" > .ami-id

# 1. SSM: the recommended-AMI parameter. Overwrite=true makes this idempotent.
call -service ssm -target AmazonSSM.PutParameter -body "{
  \"Name\": \"/aws/service/eks/optimized-ami/${MINOR}/amazon-linux-2023/x86_64/standard/recommended/image_id\",
  \"Value\": \"${AMI}\", \"Type\": \"String\", \"Overwrite\": true}" >/dev/null

# 2. IAM: the node role (Karpenter creates the instance profile itself and attaches this role to it).
TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
call -service iam -version 2010-05-08 -action CreateRole RoleName=KarpenterNodeRole-${CLUSTER} \
  "AssumeRolePolicyDocument=${TRUST}" >/dev/null 2>&1 || true   # already exists on a re-run

# 3. SQS: the interruption queue.
call -service sqs -target AmazonSQS.CreateQueue -json-version 1.0 \
  -body "{\"QueueName\":\"${CLUSTER}-interruption\"}" >/dev/null

# 4. EC2 network: one VPC, two subnets in different zones, one security group, all tagged for Karpenter discovery.
# Re-runs create duplicates in Floci, so skip when the discovery-tagged subnets already exist.
if [ "$(python3 scripts/profiles.py --backend-of ec2:DescribeSubnets ops.yaml)" != "ec2-sim" ] && \
   ! call -service ec2 -action DescribeSubnets "Filter.1.Name=tag:karpenter.sh/discovery" "Filter.1.Value.1=${CLUSTER}" | grep -q '<subnetId>'; then
  VPC=$(call -service ec2 -action CreateVpc CidrBlock=10.0.0.0/16 | sed -n 's:.*<vpcId>\(.*\)</vpcId>.*:\1:p' | head -1)
  for spec in "10.0.1.0/24 ${AWS_REGION}a" "10.0.2.0/24 ${AWS_REGION}b"; do
    set -- $spec
    SUBNET=$(call -service ec2 -action CreateSubnet VpcId="$VPC" CidrBlock="$1" AvailabilityZone="$2" | sed -n 's:.*<subnetId>\(.*\)</subnetId>.*:\1:p' | head -1)
    call -service ec2 -action CreateTags "ResourceId.1=$SUBNET" Tag.1.Key=karpenter.sh/discovery "Tag.1.Value=${CLUSTER}" >/dev/null
  done
  SG=$(call -service ec2 -action CreateSecurityGroup GroupName=${CLUSTER}-nodes GroupDescription=nodes VpcId="$VPC" | sed -n 's:.*<groupId>\(.*\)</groupId>.*:\1:p' | head -1)
  call -service ec2 -action CreateTags "ResourceId.1=$SG" Tag.1.Key=karpenter.sh/discovery "Tag.1.Value=${CLUSTER}" >/dev/null
fi

# 6. The EKS cluster record. Karpenter reads its version and service CIDR. The probe's EKS check creates it
# (a 409 means it already exists) and waits until it is ACTIVE.
if [ -x bin/linux/s0probe ]; then
  docker run --rm --network "$NET" -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
    -v "$PWD/bin/linux/s0probe:/s0probe:ro" --entrypoint /s0probe "${STATIC_IMAGE}" \
    -endpoint "$FLOCI" -region us-west-2 -cluster "$CLUSTER" -groups eks -eks-create -wait 120s >/dev/null || \
    echo "note: creating the EKS cluster in Floci failed; S0 must record whether its EKS mock mode is needed"
else
  echo "note: bin/linux/s0probe is not built yet (Task 8 adds it); the probe creates the EKS cluster itself"
fi

echo "seeded floci: ssm ${MINOR}/${AMI}, role KarpenterNodeRole-${CLUSTER}, queue ${CLUSTER}-interruption, network + AMI"
```

If a seeding call fails, that is an S0 finding, not a script bug to paper over: note which call and what Floci answered in `trace/NOTES.md`; Task 8's report turns it into a decision.

- [ ] **Step 7: Write the trace viewer**

`scripts/trace-show.sh`:

```bash
#!/usr/bin/env bash
# Prints the recorded AWS calls and whether ops.yaml supports each one.
set -euo pipefail
cd "$(dirname "$0")/.."
f=trace/raw/calls.json
[ -s "$f" ] || { echo "no trace yet: $f is empty (the gateway flushes every 10s)"; exit 1; }
# calls.json is {"calls": {"ec2:CreateFleet": 3, ...}, "unsupported": ["ec2:CreateFleet", ...]}
python3 - "$f" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
bad = set(d["unsupported"])
for name in sorted(d["calls"]):
    mark = "UNSUPPORTED" if name in bad else "ok         "
    print(f'{mark} {name} x{d["calls"][name]}')
print(f'\n{len(d["calls"])} distinct operations, {len(bad)} not in ops.yaml')
PY
```

- [ ] **Step 8: Add Makefile targets and ignore generated files**

Append to `Makefile`:

```make
COMPOSE = docker compose --env-file .env.images --env-file .env -f deploy/compose.s0.yaml

# Brings up Floci + the gateway (trace mode), the k3d cluster, and real Karpenter.
s0-up: init env-images build-linux
	mkdir -p trace/raw
	COMPOSE_PROFILES=$$(python3 scripts/profiles.py ops.yaml) $(COMPOSE) up -d --build
	./scripts/seed-floci.sh
	./scripts/k3d-up.sh
	./scripts/karpenter-install.sh

s0-down:
	-k3d cluster delete meridian-g0
	$(COMPOSE) --profile '*' down -v

seed: build-linux
	./scripts/seed-floci.sh

trace-show:
	./scripts/trace-show.sh
```

`.gitignore` already covers `bin/` (so `bin/linux/` too) and `trace/raw/`; the S0 report under `docs/s0/` is committed on purpose. Run `chmod +x scripts/*.sh scripts/profiles.py`.

- [ ] **Step 9: Run it and look at the first trace**

Run: `make s0-up && sleep 60 && make trace-show`
Expected: Karpenter's pod starts (it may restart while it waits for something Floci does not serve). `trace-show` lists at least one call. Record, in `trace/NOTES.md` (not committed), what the first failing call is and what Floci answered. Also record where Floci attaches the containers it launches for EC2 (`docker network ls`, `docker inspect`): Task 15 depends on the answer.
If an image or the chart cannot be pulled, the registry is unreachable from this machine; fix that locally and do not change the stack to reach real AWS.

- [ ] **Step 10: Check the isolation claims**

Run: `docker port meridian-g0-aws-gateway-1` and `docker inspect meridian-g0-floci-1 --format '{{json .NetworkSettings.Ports}}'`
Expected: the gateway shows only `127.0.0.1:4566`; Floci shows no published ports.

- [ ] **Step 11: Commit**

```bash
git add deploy scripts Makefile
git commit -m "feat: add S0 harness (Floci behind the gateway, k3d, Karpenter, seeding)"
```

### Task 8: S0 contract probe, report, and the backend decision

**Why this task exists:** this is the spike. The probe sends Karpenter's AWS calls through the gateway to Floci and checks that each answer carries the fields Karpenter reads (instance types with CPU/memory/ENI data, CreateFleet instances that reach `running` with tags, a Pricing document with the right JSON path, an EKS cluster with a service CIDR, and so on). The output is a per-operation table. Operations that pass stay on Floci; operations that fail move to a custom backend in the tasks that follow. Nothing is built on a guess about what Floci supports.

**Files:**
- Create: `internal/s0probe/xmltree.go`, `probe.go`, `checks.go`, `prices.go`, `report.go`
- Test: `internal/s0probe/xmltree_test.go`, `probe_test.go`, `prices_test.go`
- Create: `cmd/s0probe/main.go`
- Create: `scripts/s0-run.sh`, `scripts/s0matrix.py`, `scripts/test_s0matrix.py`
- Create: `docs/s0/report.md`, `docs/s0/report.json` (generated, committed), `docs/s0/decision.md`
- Modify: `Makefile`, `ops.yaml` (backend fields, by `s0matrix.py --apply`)

**Interfaces:**
- Consumes: `awscall.BuildQuery`, `BuildJSON`, `BuildREST` (Task 6); `ops.Load` (Task 2); a running S0 stack (Task 7).
- Produces:
  - `s0probe.Config{Endpoint, Region, Access, Secret, Cluster, AMIParam, QueueName string; EKSCreate bool; Groups []string; Wait time.Duration; Client *http.Client; Now func() time.Time}`
  - `s0probe.Check{Name, Group string; Needs []string; Run func(ctx, *Env) (string, error)}`, `s0probe.Checks() []Check`, `s0probe.Run(ctx, Config, []Check) []Result`
  - `s0probe.Result{Name, Group, Status, Detail}` with `Status` one of `pass`, `fail`, `skip`; `Summarize`, `FailedGroups`, `Markdown`, `JSON`
  - `s0probe.Env` with `Query`, `JSONCall`, `REST`, `State`, `Lists`; `s0probe.Node` with `ParseXML`, `Text`, `Texts`, `All`
  - `s0probe.OnDemandPrices(ctx, Config, types []string) (map[string]float64, error)`: USD per hour per instance type, asked of the backend the way Karpenter asks (the gate's price criterion uses it)
  - The `s0probe` binary (flags `-endpoint -region -cluster -ami-param -queue -eks-create -groups -wait -out -title -prices`; exit 0 only when every selected check passed; `-prices c5.large,m5.large` prints their on-demand prices as JSON and exits)
  - `scripts/s0matrix.py REPORT.json OPS.yaml [--apply]`: prints the per-operation decision and rewrites `backend:` fields
- Check names start with the operation (`ec2:CreateFleet`, optionally followed by a qualifier in parentheses). The decision script and `TestEveryProbedOperationIsInOpsYaml` rely on that.

**Acceptance Criteria:**
- The probe exercises Karpenter's call surface in dependency order: STS identity, the AMI alias SSM path, IAM instance-profile lifecycle, SQS round trip, EKS `DescribeCluster` fields, EC2 reference data (zones, discovery-tagged subnets and security groups, images, instance-type fields, offerings, spot prices), launch template, `CreateFleet` to a `running` tagged instance, `RunInstances`, `TerminateInstances`, and Pricing with Karpenter's filter set.
- Every failure message names the missing field or HTTP error, so the report says exactly what the backend lacks.
- A check whose prerequisite failed is `skip`, and counts as not passing.
- `ops.yaml` contains every operation the probe uses.
- `s0matrix.py` keeps an operation on Floci only if all its checks passed, and moves operations that share instance state together (`ops.Coupled`).

- [ ] **Step 1: Write the failing tests**

`internal/s0probe/xmltree_test.go`:

```go
package s0probe

import "testing"

func TestXMLTreeNavigation(t *testing.T) {
	n, err := ParseXML([]byte(`<?xml version="1.0"?><R xmlns="urn:x"><requestId>r1</requestId>
	<set><item><id>a</id><tags><item><k>x</k></item></tags></item><item><id>b</id></item></set></R>`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "R" || n.Text("requestId") != "r1" {
		t.Fatalf("root = %s, requestId = %q", n.Name, n.Text("requestId"))
	}
	if got := n.Texts("set/item/id"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("ids = %v", got)
	}
	if n.Text("set/item/tags/item/k") != "x" || n.Text("nope/at/all") != "" || len(n.All("set/missing")) != 0 {
		t.Fatal("path lookups misbehaved")
	}
	if _, err := ParseXML([]byte("")); err == nil {
		t.Fatal("empty input must fail")
	}
}
```

`internal/s0probe/probe_test.go`:

```go
package s0probe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tazzledazzle/meridian/internal/ops"
)

func TestRunPassFailSkipAndGroups(t *testing.T) {
	checks := []Check{
		{Name: "a:One", Group: "a", Run: func(ctx context.Context, e *Env) (string, error) {
			e.State["x"] = "1"
			return "ok", nil
		}},
		{Name: "a:Two", Group: "a", Needs: []string{"x"}, Run: func(ctx context.Context, e *Env) (string, error) {
			return "", errors.New("missing field")
		}},
		{Name: "b:Three", Group: "b", Needs: []string{"never"}, Run: func(ctx context.Context, e *Env) (string, error) {
			return "unreachable", nil
		}},
		{Name: "c:Four", Group: "c", Run: func(ctx context.Context, e *Env) (string, error) { return "", nil }},
	}
	rs := Run(context.Background(), Config{}, checks)
	got := []string{rs[0].Status, rs[1].Status, rs[2].Status, rs[3].Status}
	want := []string{"pass", "fail", "skip", "pass"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("statuses = %v, want %v", got, want)
		}
	}
	if s := Summarize(rs); s.Pass != 2 || s.Fail != 1 || s.Skip != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if fg := FailedGroups(rs); strings.Join(fg, ",") != "a,b" {
		t.Fatalf("failed groups = %v", fg)
	}
	only := Run(context.Background(), Config{Groups: []string{"c"}}, checks)
	if len(only) != 1 || only[0].Name != "c:Four" {
		t.Fatalf("group filter = %+v", only)
	}
}

func TestMarkdownAndJSON(t *testing.T) {
	rs := []Result{{Name: "x:Y", Group: "x", Status: "fail", Detail: "a|b\nc"}}
	md := Markdown("T", rs)
	if !strings.Contains(md, "| x:Y | x | fail | a/b c |") || !strings.Contains(md, "custom backend") {
		t.Fatalf("markdown:\n%s", md)
	}
	js, err := JSON(rs)
	if err != nil || !strings.Contains(string(js), `"failed_groups"`) {
		t.Fatalf("json: %v %s", err, js)
	}
	if !strings.Contains(Markdown("T", []Result{{Name: "p", Group: "p", Status: "pass"}}), "Every group passed") {
		t.Fatal("all-pass markdown should say so")
	}
}

// TestSTSCheckAgainstFake runs a real check end to end over HTTP against a tiny fake.
func TestSTSCheckAgainstFake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "/sts/aws4_request") {
			http.Error(w, "not signed for sts", http.StatusForbidden)
			return
		}
		w.Write([]byte(`<GetCallerIdentityResponse><GetCallerIdentityResult><Account>000000000000</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`))
	}))
	defer srv.Close()
	rs := Run(context.Background(), Config{Endpoint: srv.URL, Region: "us-east-1", Access: "a", Secret: "s", Groups: []string{"sts"}}, Checks())
	if len(rs) != 1 || rs[0].Status != "pass" || !strings.Contains(rs[0].Detail, "000000000000") {
		t.Fatalf("results = %+v", rs)
	}
}

func TestChecksHaveUniqueNamesAndKnownGroups(t *testing.T) {
	seen := map[string]bool{}
	groups := map[string]bool{"sts": true, "ssm": true, "iam": true, "sqs": true, "eks": true, "ec2": true, "pricing": true}
	for _, c := range Checks() {
		if seen[c.Name] {
			t.Errorf("duplicate check %q", c.Name)
		}
		seen[c.Name] = true
		if !groups[c.Group] {
			t.Errorf("check %q has unknown group %q", c.Name, c.Group)
		}
	}
}

// TestEveryProbedOperationIsInOpsYaml keeps the probe honest: it may only
// exercise operations the gateway would let through.
func TestEveryProbedOperationIsInOpsYaml(t *testing.T) {
	l, err := ops.Load("../../ops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range Checks() {
		name := strings.Fields(c.Name)[0] // "ec2:DescribeSubnets (discovery tag)" -> "ec2:DescribeSubnets"
		svc, op, _ := strings.Cut(name, ":")
		if !l.Has(svc, op) {
			t.Errorf("check %q uses %s, which is not in ops.yaml", c.Name, name)
		}
	}
}
```

`internal/s0probe/prices_test.go`:

```go
package s0probe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func priceDoc(typ, usd string) string {
	return `{"product":{"attributes":{"instanceType":"` + typ + `"}},"terms":{"OnDemand":{"T":{"priceDimensions":{"D":{"pricePerUnit":{"USD":"` + usd + `"}}}}}}}`
}

func TestParsePrice(t *testing.T) {
	typ, usd, err := parsePrice(priceDoc("c5.large", "0.085"))
	if err != nil || typ != "c5.large" || usd != 0.085 {
		t.Fatalf("parsePrice = %q %v %v", typ, usd, err)
	}
	if _, _, err := parsePrice("not json"); err == nil {
		t.Fatal("a non-JSON entry must be an error")
	}
}

func TestOnDemandPricesAsksPerTypeAndRejectsMismatches(t *testing.T) {
	prices := map[string]string{"c5.large": "0.085", "m5.large": "0.096"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct{ Filters []map[string]string }
		_ = json.Unmarshal(b, &req)
		var list []string
		for _, f := range req.Filters {
			if f["Field"] == "instanceType" {
				if usd, ok := prices[f["Value"]]; ok {
					list = append(list, priceDoc(f["Value"], usd))
				}
			}
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(map[string]any{"PriceList": list})
	}))
	defer srv.Close()
	cfg := Config{Endpoint: srv.URL, Region: "us-west-2", Access: "AK", Secret: "SK"}
	got, err := OnDemandPrices(context.Background(), cfg, []string{"c5.large", "m5.large"})
	if err != nil || got["c5.large"] != 0.085 || got["m5.large"] != 0.096 {
		t.Fatalf("prices = %v, %v", got, err)
	}
	if _, err := OnDemandPrices(context.Background(), cfg, []string{"t3.nano"}); err == nil || !strings.Contains(err.Error(), "no on-demand price") {
		t.Fatalf("missing type should fail, got %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/s0probe/ -v`
Expected: FAIL with `undefined: ParseXML` (and the other symbols).

- [ ] **Step 3: Write the XML helper and the probe framework**

`internal/s0probe/xmltree.go`:

```go
package s0probe

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// Node is a namespace-free XML element. AWS query-protocol responses are deep
// trees of repeated <item> elements, and the checks only need "does this path
// exist and what does it say", so a tiny tree beats a struct per operation.
type Node struct {
	Name  string
	Chars string // character data directly inside the element
	Kids  []*Node
}

// ParseXML parses a response body into a tree rooted at the document element.
func ParseXML(b []byte) (*Node, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	root := &Node{}
	stack := []*Node{root}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &Node{Name: t.Name.Local}
			top := stack[len(stack)-1]
			top.Kids = append(top.Kids, n)
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			stack[len(stack)-1].Chars += string(t)
		}
	}
	if len(root.Kids) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	return root.Kids[0], nil
}

// All returns every node at a slash-separated path below n ("a/b/item").
func (n *Node) All(path string) []*Node {
	cur := []*Node{n}
	for _, seg := range strings.Split(path, "/") {
		var next []*Node
		for _, c := range cur {
			for _, k := range c.Kids {
				if k.Name == seg {
					next = append(next, k)
				}
			}
		}
		cur = next
	}
	return cur
}

// Text returns the trimmed text of the first node at path, or "".
func (n *Node) Text(path string) string {
	if got := n.All(path); len(got) > 0 {
		return strings.TrimSpace(got[0].Chars)
	}
	return ""
}

// Texts returns the trimmed text of every node at path.
func (n *Node) Texts(path string) []string {
	var out []string
	for _, g := range n.All(path) {
		out = append(out, strings.TrimSpace(g.Chars))
	}
	return out
}
```

`internal/s0probe/probe.go`:

```go
// Package s0probe is the S0 spike's contract probe. It sends the AWS calls
// Karpenter makes to an endpoint (the aws-gateway in front of Floci, or in
// front of our own simulators) and checks that the answers contain the fields
// Karpenter reads. The pass/fail table it produces decides, operation by
// operation, whether Floci can serve it or a custom service must.
package s0probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tazzledazzle/meridian/internal/awscall"
)

// Config says where to probe and what the seeded environment is called.
type Config struct {
	Endpoint  string // gateway URL
	Region    string
	Access    string
	Secret    string
	Cluster   string // cluster name; subnets and security groups are tagged karpenter.sh/discovery=<Cluster>
	AMIParam  string // SSM parameter holding the AMI ID, the way Karpenter's alias resolves it
	QueueName string // SQS interruption queue name (also the probe's scratch-queue prefix)
	EKSCreate bool   // call eks:CreateCluster before DescribeCluster (false when the cluster already exists)
	Groups    []string
	Wait      time.Duration // how long to poll for an instance to run
	Client    *http.Client
	Now       func() time.Time
}

func (c *Config) wantGroup(g string) bool {
	if len(c.Groups) == 0 {
		return true
	}
	for _, x := range c.Groups {
		if x == g {
			return true
		}
	}
	return false
}

// Result is the outcome of one check.
type Result struct {
	Name     string        `json:"name"`   // service:Operation
	Group    string        `json:"group"`  // ec2, iam, sqs, ssm, sts, pricing, eks
	Status   string        `json:"status"` // pass | fail | skip
	Detail   string        `json:"detail"`
	Duration time.Duration `json:"-"`
}

// Env is what a check can use: the client and values earlier checks produced.
type Env struct {
	Cfg    *Config
	State  map[string]string
	Lists  map[string][]string
	Suffix string
}

// Check is one probe step.
type Check struct {
	Name  string
	Group string
	Needs []string // State keys that earlier checks must have produced; otherwise the check is skipped
	Run   func(ctx context.Context, e *Env) (detail string, err error)
}

// Resp is a raw HTTP answer.
type Resp struct {
	Status int
	Body   []byte
}

func (e *Env) do(ctx context.Context, req *http.Request) (Resp, error) {
	c := e.Cfg.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return Resp{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return Resp{Status: resp.StatusCode, Body: b}, nil
}

func (e *Env) now() time.Time {
	if e.Cfg.Now != nil {
		return e.Cfg.Now()
	}
	return time.Now()
}

// failure turns a non-200 answer into an error that shows what the backend said.
func failure(r Resp) error {
	return fmt.Errorf("HTTP %d: %.240s", r.Status, strings.TrimSpace(string(r.Body)))
}

// Query sends a query-protocol call and returns the parsed XML on HTTP 200.
func (e *Env) Query(ctx context.Context, service, version, action string, params ...string) (*Node, error) {
	req, _, err := awscall.BuildQuery(e.Cfg.Endpoint, service, e.Cfg.Region, action, version, params, e.Cfg.Access, e.Cfg.Secret, e.now())
	if err != nil {
		return nil, err
	}
	r, err := e.do(ctx, req)
	if err != nil {
		return nil, err
	}
	if r.Status != http.StatusOK {
		return nil, failure(r)
	}
	n, err := ParseXML(r.Body)
	if err != nil {
		return nil, fmt.Errorf("response is not XML: %w (%.120s)", err, r.Body)
	}
	return n, nil
}

// JSONCall sends a JSON-RPC call and decodes the JSON object on HTTP 200.
func (e *Env) JSONCall(ctx context.Context, service, target, jsonVersion string, body any) (map[string]any, error) {
	b, _ := json.Marshal(body)
	req, _, err := awscall.BuildJSON(e.Cfg.Endpoint, service, e.Cfg.Region, target, jsonVersion, string(b), e.Cfg.Access, e.Cfg.Secret, e.now())
	if err != nil {
		return nil, err
	}
	r, err := e.do(ctx, req)
	if err != nil {
		return nil, err
	}
	if r.Status != http.StatusOK {
		return nil, failure(r)
	}
	return decodeObject(r.Body)
}

// REST sends a REST-JSON call and decodes the JSON object on HTTP 200 or 201.
func (e *Env) REST(ctx context.Context, service, method, path string, body any) (map[string]any, error) {
	payload := ""
	if body != nil {
		b, _ := json.Marshal(body)
		payload = string(b)
	}
	req, _, err := awscall.BuildREST(e.Cfg.Endpoint, service, e.Cfg.Region, method, path, payload, e.Cfg.Access, e.Cfg.Secret, e.now())
	if err != nil {
		return nil, err
	}
	r, err := e.do(ctx, req)
	if err != nil {
		return nil, err
	}
	if r.Status != http.StatusOK && r.Status != http.StatusCreated {
		return nil, failure(r)
	}
	return decodeObject(r.Body)
}

func decodeObject(b []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("response is not a JSON object: %w (%.120s)", err, b)
	}
	return m, nil
}

// Get walks a dotted path through decoded JSON ("cluster.kubernetesNetworkConfig.serviceIpv4Cidr").
func Get(m map[string]any, path string) any {
	var cur any = m
	for _, seg := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[seg]
	}
	return cur
}

// GetString is Get for string values; non-strings and missing paths give "".
func GetString(m map[string]any, path string) string {
	s, _ := Get(m, path).(string)
	return s
}

// poll calls fn every 500ms until it returns true or the deadline passes.
func poll(ctx context.Context, d time.Duration, fn func() (bool, error)) error {
	deadline := time.Now().Add(d)
	for {
		ok, err := fn()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s", d)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Run executes the checks in order and returns one result per check.
func Run(ctx context.Context, cfg Config, checks []Check) []Result {
	e := &Env{Cfg: &cfg, State: map[string]string{}, Lists: map[string][]string{},
		Suffix: fmt.Sprintf("%x", time.Now().UnixNano()&0xffffff)}
	var out []Result
	for _, c := range checks {
		if !cfg.wantGroup(c.Group) {
			continue
		}
		r := Result{Name: c.Name, Group: c.Group}
		var missing []string
		for _, k := range c.Needs {
			if e.State[k] == "" {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			r.Status, r.Detail = "skip", "needs "+strings.Join(missing, ", ")+" from an earlier check"
			out = append(out, r)
			continue
		}
		start := time.Now()
		detail, err := c.Run(ctx, e)
		r.Duration = time.Since(start)
		if err != nil {
			r.Status, r.Detail = "fail", err.Error()
		} else {
			r.Status, r.Detail = "pass", detail
		}
		out = append(out, r)
	}
	return out
}
```

- [ ] **Step 4: Write the checks**

Each check sends the call exactly as Karpenter's SDK would and asserts only fields Karpenter reads. Comments in the file say which field matters and why.

`internal/s0probe/checks.go`:

```go
package s0probe

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	ec2Version = "2016-11-15"
	iamVersion = "2010-05-08"
	stsVersion = "2011-06-15"
	probeType  = "m5.large"
)

var versionRE = regexp.MustCompile(`^\d+\.\d+`)

// need reports a failed expectation as an error naming the missing field, so
// the S0 report says exactly what the backend left out.
func need(ok bool, format string, args ...any) error {
	if ok {
		return nil
	}
	return fmt.Errorf(format, args...)
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func tagFilter(cluster string) []string {
	return []string{"Filter.1.Name=tag:karpenter.sh/discovery", "Filter.1.Value.1=" + cluster}
}

// Checks returns the probe, in dependency order.
func Checks() []Check {
	return []Check{
		// ---- STS ----
		{Name: "sts:GetCallerIdentity", Group: "sts", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "sts", stsVersion, "GetCallerIdentity")
			if err != nil {
				return "", err
			}
			acct := n.Text("GetCallerIdentityResult/Account")
			return "account " + acct, need(acct != "", "no Account in GetCallerIdentityResult")
		}},

		// ---- SSM ----
		{Name: "ssm:PutParameter", Group: "ssm", Run: func(ctx context.Context, e *Env) (string, error) {
			e.State["ssmName"] = "/s0probe/" + e.Suffix
			_, err := e.JSONCall(ctx, "ssm", "AmazonSSM.PutParameter", "1.1",
				map[string]any{"Name": e.State["ssmName"], "Value": "hello", "Type": "String", "Overwrite": true})
			return e.State["ssmName"], err
		}},
		{Name: "ssm:GetParameter", Group: "ssm", Needs: []string{"ssmName"}, Run: func(ctx context.Context, e *Env) (string, error) {
			m, err := e.JSONCall(ctx, "ssm", "AmazonSSM.GetParameter", "1.1", map[string]any{"Name": e.State["ssmName"]})
			if err != nil {
				return "", err
			}
			return "value round-trips", need(GetString(m, "Parameter.Value") == "hello", "Parameter.Value = %q, want hello", GetString(m, "Parameter.Value"))
		}},
		{Name: "ssm:GetParameter (AMI alias path)", Group: "ssm", Run: func(ctx context.Context, e *Env) (string, error) {
			m, err := e.JSONCall(ctx, "ssm", "AmazonSSM.GetParameter", "1.1", map[string]any{"Name": e.Cfg.AMIParam})
			if err != nil {
				return "", fmt.Errorf("%w (run the seed step first)", err)
			}
			ami := GetString(m, "Parameter.Value")
			e.State["ami"] = ami
			return ami, need(strings.HasPrefix(ami, "ami-"), "parameter value %q is not an AMI ID", ami)
		}},

		// ---- IAM instance profile ----
		{Name: "iam:CreateRole", Group: "iam", Run: func(ctx context.Context, e *Env) (string, error) {
			e.State["role"] = "s0probe-role-" + e.Suffix
			trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
			_, err := e.Query(ctx, "iam", iamVersion, "CreateRole", "RoleName="+e.State["role"], "AssumeRolePolicyDocument="+trust)
			return e.State["role"], err
		}},
		{Name: "iam:CreateInstanceProfile", Group: "iam", Run: func(ctx context.Context, e *Env) (string, error) {
			e.State["profile"] = "s0probe-profile-" + e.Suffix
			_, err := e.Query(ctx, "iam", iamVersion, "CreateInstanceProfile", "InstanceProfileName="+e.State["profile"],
				"Tags.member.1.Key=karpenter.sh/managed-by", "Tags.member.1.Value=s0probe")
			return e.State["profile"], err
		}},
		{Name: "iam:AddRoleToInstanceProfile", Group: "iam", Needs: []string{"role", "profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.Query(ctx, "iam", iamVersion, "AddRoleToInstanceProfile", "InstanceProfileName="+e.State["profile"], "RoleName="+e.State["role"])
			return "", err
		}},
		{Name: "iam:GetInstanceProfile", Group: "iam", Needs: []string{"role", "profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "iam", iamVersion, "GetInstanceProfile", "InstanceProfileName="+e.State["profile"])
			if err != nil {
				return "", err
			}
			roles := n.Texts("GetInstanceProfileResult/InstanceProfile/Roles/member/RoleName")
			return "roles " + strings.Join(roles, ","), firstErr(
				need(n.Text("GetInstanceProfileResult/InstanceProfile/Arn") != "", "no InstanceProfile.Arn"),
				need(len(roles) == 1 && roles[0] == e.State["role"], "attached roles = %v, want [%s]", roles, e.State["role"]))
		}},
		{Name: "iam:ListInstanceProfiles", Group: "iam", Needs: []string{"profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "iam", iamVersion, "ListInstanceProfiles")
			if err != nil {
				return "", err
			}
			names := n.Texts("ListInstanceProfilesResult/InstanceProfiles/member/InstanceProfileName")
			found := false
			for _, x := range names {
				found = found || x == e.State["profile"]
			}
			return fmt.Sprintf("%d profiles", len(names)), need(found, "created profile %s not listed (got %v)", e.State["profile"], names)
		}},
		{Name: "iam:TagInstanceProfile", Group: "iam", Needs: []string{"profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.Query(ctx, "iam", iamVersion, "TagInstanceProfile", "InstanceProfileName="+e.State["profile"],
				"Tags.member.1.Key=karpenter.k8s.aws/ec2nodeclass", "Tags.member.1.Value=default")
			return "", err
		}},

		// ---- SQS (JSON protocol) ----
		{Name: "sqs:CreateQueue", Group: "sqs", Run: func(ctx context.Context, e *Env) (string, error) {
			e.State["queueName"] = "s0probe-" + e.Suffix
			m, err := e.JSONCall(ctx, "sqs", "AmazonSQS.CreateQueue", "1.0", map[string]any{"QueueName": e.State["queueName"]})
			if err != nil {
				return "", err
			}
			e.State["queueURL"] = GetString(m, "QueueUrl")
			return e.State["queueURL"], need(e.State["queueURL"] != "", "no QueueUrl")
		}},
		{Name: "sqs:GetQueueUrl", Group: "sqs", Needs: []string{"queueName"}, Run: func(ctx context.Context, e *Env) (string, error) {
			m, err := e.JSONCall(ctx, "sqs", "AmazonSQS.GetQueueUrl", "1.0", map[string]any{"QueueName": e.State["queueName"]})
			if err != nil {
				return "", err
			}
			return GetString(m, "QueueUrl"), need(GetString(m, "QueueUrl") == e.State["queueURL"], "GetQueueUrl %q != CreateQueue %q", GetString(m, "QueueUrl"), e.State["queueURL"])
		}},
		{Name: "sqs:SendMessage", Group: "sqs", Needs: []string{"queueURL"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.JSONCall(ctx, "sqs", "AmazonSQS.SendMessage", "1.0",
				map[string]any{"QueueUrl": e.State["queueURL"], "MessageBody": `{"detail-type":"probe"}`})
			return "", err
		}},
		{Name: "sqs:ReceiveMessage", Group: "sqs", Needs: []string{"queueURL"}, Run: func(ctx context.Context, e *Env) (string, error) {
			var handle string
			err := poll(ctx, 10*time.Second, func() (bool, error) {
				m, err := e.JSONCall(ctx, "sqs", "AmazonSQS.ReceiveMessage", "1.0",
					map[string]any{"QueueUrl": e.State["queueURL"], "MaxNumberOfMessages": 1, "WaitTimeSeconds": 1, "VisibilityTimeout": 30})
				if err != nil {
					return false, err
				}
				msgs, _ := m["Messages"].([]any)
				if len(msgs) == 0 {
					return false, nil
				}
				msg, _ := msgs[0].(map[string]any)
				if msg["Body"] != `{"detail-type":"probe"}` {
					return false, fmt.Errorf("message body = %v", msg["Body"])
				}
				handle, _ = msg["ReceiptHandle"].(string)
				return true, nil
			})
			e.State["receipt"] = handle
			return "body round-trips", firstErr(err, need(handle != "", "no ReceiptHandle"))
		}},
		{Name: "sqs:DeleteMessage", Group: "sqs", Needs: []string{"queueURL", "receipt"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.JSONCall(ctx, "sqs", "AmazonSQS.DeleteMessage", "1.0",
				map[string]any{"QueueUrl": e.State["queueURL"], "ReceiptHandle": e.State["receipt"]})
			return "", err
		}},

		// ---- EKS ----
		{Name: "eks:CreateCluster", Group: "eks", Run: func(ctx context.Context, e *Env) (string, error) {
			if !e.Cfg.EKSCreate {
				return "skipped by configuration (cluster is expected to exist)", nil
			}
			_, err := e.REST(ctx, "eks", http.MethodPost, "/clusters", map[string]any{
				"name": e.Cfg.Cluster, "roleArn": "arn:aws:iam::123456789012:role/s0probe-cluster",
				"resourcesVpcConfig": map[string]any{"subnetIds": []string{}},
			})
			if err != nil && strings.Contains(err.Error(), "HTTP 409") {
				return "already exists", nil
			}
			return e.Cfg.Cluster, err
		}},
		{Name: "eks:DescribeCluster", Group: "eks", Run: func(ctx context.Context, e *Env) (string, error) {
			var m map[string]any
			err := poll(ctx, e.Cfg.Wait, func() (bool, error) {
				var err error
				m, err = e.REST(ctx, "eks", http.MethodGet, "/clusters/"+e.Cfg.Cluster, nil)
				if err != nil {
					return false, err
				}
				return GetString(m, "cluster.status") == "ACTIVE", nil
			})
			if err != nil {
				return "", err
			}
			v := GetString(m, "cluster.version")
			return "version " + v, firstErr(
				need(versionRE.MatchString(v), "cluster.version = %q", v),
				need(GetString(m, "cluster.endpoint") != "", "no cluster.endpoint"),
				need(GetString(m, "cluster.kubernetesNetworkConfig.serviceIpv4Cidr") != "", "no cluster.kubernetesNetworkConfig.serviceIpv4Cidr (Karpenter reads it for the cluster DNS address)"))
		}},

		// ---- EC2 reference data ----
		{Name: "ec2:DescribeAvailabilityZones", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeAvailabilityZones")
			if err != nil {
				return "", err
			}
			names, ids := n.Texts("availabilityZoneInfo/item/zoneName"), n.Texts("availabilityZoneInfo/item/zoneId")
			e.Lists["azs"] = names
			return strings.Join(names, ","), firstErr(need(len(names) >= 2, "only %d zones", len(names)), need(len(ids) == len(names), "zoneId missing on some zones"))
		}},
		{Name: "ec2:DescribeSubnets (discovery tag)", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeSubnets", tagFilter(e.Cfg.Cluster)...)
			if err != nil {
				return "", err
			}
			for _, it := range n.All("subnetSet/item") {
				e.Lists["subnets"] = append(e.Lists["subnets"], it.Text("subnetId")+"|"+it.Text("availabilityZone"))
			}
			e.State["subnets"] = strconv.Itoa(len(e.Lists["subnets"]))
			return e.State["subnets"] + " subnets", need(len(e.Lists["subnets"]) >= 2 && !strings.Contains(strings.Join(e.Lists["subnets"], ","), "|,"),
				"found %d tagged subnets with zones; seed at least 2 (karpenter.sh/discovery=%s)", len(e.Lists["subnets"]), e.Cfg.Cluster)
		}},
		{Name: "ec2:DescribeSecurityGroups (discovery tag)", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeSecurityGroups", tagFilter(e.Cfg.Cluster)...)
			if err != nil {
				return "", err
			}
			ids := n.Texts("securityGroupInfo/item/groupId")
			if len(ids) > 0 {
				e.State["sg"] = ids[0]
			}
			return strings.Join(ids, ","), need(len(ids) >= 1, "no security group tagged karpenter.sh/discovery=%s", e.Cfg.Cluster)
		}},
		{Name: "ec2:DescribeImages", Group: "ec2", Needs: []string{"ami"}, Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeImages", "ImageId.1="+e.State["ami"])
			if err != nil {
				return "", err
			}
			it := n.All("imagesSet/item")
			if len(it) != 1 {
				return "", fmt.Errorf("DescribeImages returned %d images for %s", len(it), e.State["ami"])
			}
			return it[0].Text("architecture"), firstErr(
				need(it[0].Text("architecture") != "", "no architecture"),
				need(it[0].Text("creationDate") != "", "no creationDate (Karpenter sorts AMIs by it)"),
				need(it[0].Text("rootDeviceName") != "" || it[0].Text("blockDeviceMapping/item/deviceName") != "", "no root device name"))
		}},
		{Name: "ec2:DescribeInstanceTypes", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstanceTypes", "InstanceType.1="+probeType)
			if err != nil {
				return "", err
			}
			it := n.All("instanceTypeSet/item")
			if len(it) != 1 {
				return "", fmt.Errorf("%d results for %s", len(it), probeType)
			}
			t := it[0]
			usage := strings.Join(t.Texts("supportedUsageClasses/item"), ",")
			if err := firstErr(
				need(t.Text("vCpuInfo/defaultVCpus") != "", "no vCpuInfo.defaultVCpus"),
				need(t.Text("memoryInfo/sizeInMiB") != "", "no memoryInfo.sizeInMiB"),
				need(len(t.Texts("processorInfo/supportedArchitectures/item")) > 0, "no processorInfo.supportedArchitectures"),
				need(t.Text("networkInfo/maximumNetworkInterfaces") != "", "no networkInfo.maximumNetworkInterfaces (pod density is computed from it)"),
				need(t.Text("networkInfo/ipv4AddressesPerInterface") != "", "no networkInfo.ipv4AddressesPerInterface"),
				need(strings.Contains(usage, "spot") && strings.Contains(usage, "on-demand"), "supportedUsageClasses = %q, want on-demand and spot", usage)); err != nil {
				return "", err
			}
			all, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstanceTypes")
			if err != nil {
				return "", fmt.Errorf("unfiltered listing: %w", err)
			}
			e.State["typeCount"] = strconv.Itoa(len(all.All("instanceTypeSet/item")))
			return e.State["typeCount"] + " types listed", nil
		}},
		{Name: "ec2:DescribeInstanceTypeOfferings", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			az := "us-west-2a"
			if len(e.Lists["azs"]) > 0 {
				az = e.Lists["azs"][0]
			}
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstanceTypeOfferings", "LocationType=availability-zone",
				"Filter.1.Name=location", "Filter.1.Value.1="+az)
			if err != nil {
				return "", err
			}
			types := n.Texts("instanceTypeOfferingSet/item/instanceType")
			found := false
			for _, t := range types {
				found = found || t == probeType
			}
			return fmt.Sprintf("%d offerings in %s", len(types), az), need(found, "%s not offered in %s", probeType, az)
		}},
		{Name: "ec2:DescribeSpotPriceHistory", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeSpotPriceHistory", "InstanceType.1="+probeType,
				"ProductDescription.1=Linux/UNIX", "StartTime="+e.now().UTC().Format(time.RFC3339))
			if err != nil {
				return "", err
			}
			prices := n.Texts("spotPriceHistorySet/item/spotPrice")
			if len(prices) == 0 {
				return "", fmt.Errorf("no spot prices for %s", probeType)
			}
			p, perr := strconv.ParseFloat(prices[0], 64)
			return fmt.Sprintf("%d prices, first %s", len(prices), prices[0]), firstErr(perr,
				need(p > 0, "non-positive spot price %q", prices[0]),
				need(n.Text("spotPriceHistorySet/item/availabilityZone") != "", "no availabilityZone on price entries"))
		}},

		// ---- EC2 launch path ----
		{Name: "ec2:CreateLaunchTemplate", Group: "ec2", Needs: []string{"ami"}, Run: func(ctx context.Context, e *Env) (string, error) {
			name := "s0probe-" + e.Suffix
			// A representative subset of what Karpenter puts in a launch template.
			n, err := e.Query(ctx, "ec2", ec2Version, "CreateLaunchTemplate",
				"LaunchTemplateName="+name,
				"LaunchTemplateData.ImageId="+e.State["ami"],
				"LaunchTemplateData.UserData="+base64.StdEncoding.EncodeToString([]byte("#!/bin/bash\ntrue\n")),
				"LaunchTemplateData.IamInstanceProfile.Name="+e.State["profile"],
				"LaunchTemplateData.BlockDeviceMapping.1.DeviceName=/dev/xvda",
				"LaunchTemplateData.BlockDeviceMapping.1.Ebs.VolumeSize=20",
				"LaunchTemplateData.BlockDeviceMapping.1.Ebs.VolumeType=gp3",
				"LaunchTemplateData.MetadataOptions.HttpTokens=required",
				"LaunchTemplateData.MetadataOptions.HttpPutResponseHopLimit=1",
				"LaunchTemplateData.Monitoring.Enabled=false",
				"TagSpecification.1.ResourceType=launch-template",
				"TagSpecification.1.Tag.1.Key=karpenter.k8s.aws/cluster", "TagSpecification.1.Tag.1.Value="+e.Cfg.Cluster)
			if err != nil {
				return "", err
			}
			e.State["lt"] = name
			e.State["ltID"] = n.Text("launchTemplate/launchTemplateId")
			return e.State["ltID"], need(e.State["ltID"] != "", "no launchTemplate.launchTemplateId")
		}},
		{Name: "ec2:DescribeLaunchTemplates", Group: "ec2", Needs: []string{"lt"}, Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeLaunchTemplates", "LaunchTemplateName.1="+e.State["lt"])
			if err != nil {
				return "", err
			}
			names := n.Texts("launchTemplates/item/launchTemplateName")
			if err := need(len(names) == 1 && names[0] == e.State["lt"], "got %v", names); err != nil {
				return "", err
			}
			// Karpenter also lists by tag filter when cleaning up.
			f, err := e.Query(ctx, "ec2", ec2Version, "DescribeLaunchTemplates",
				"Filter.1.Name=tag:karpenter.k8s.aws/cluster", "Filter.1.Value.1="+e.Cfg.Cluster)
			if err != nil {
				return "", fmt.Errorf("filter by tag: %w", err)
			}
			got := f.Texts("launchTemplates/item/launchTemplateName")
			ok := false
			for _, g := range got {
				ok = ok || g == e.State["lt"]
			}
			return "by name and by tag", need(ok, "tag filter did not return the template (got %v)", got)
		}},
		{Name: "ec2:CreateFleet", Group: "ec2", Needs: []string{"lt", "subnets"}, Run: func(ctx context.Context, e *Env) (string, error) {
			subs := e.Lists["subnets"]
			s1 := strings.SplitN(subs[0], "|", 2)
			s2 := s1
			if len(subs) > 1 {
				s2 = strings.SplitN(subs[1], "|", 2)
			}
			n, err := e.Query(ctx, "ec2", ec2Version, "CreateFleet",
				"Type=instant",
				"TargetCapacitySpecification.TotalTargetCapacity=1",
				"TargetCapacitySpecification.DefaultTargetCapacityType=spot",
				"SpotOptions.AllocationStrategy=price-capacity-optimized",
				"LaunchTemplateConfigs.1.LaunchTemplateSpecification.LaunchTemplateName="+e.State["lt"],
				"LaunchTemplateConfigs.1.LaunchTemplateSpecification.Version=$Latest",
				"LaunchTemplateConfigs.1.Overrides.1.InstanceType="+probeType,
				"LaunchTemplateConfigs.1.Overrides.1.SubnetId="+s1[0],
				"LaunchTemplateConfigs.1.Overrides.1.AvailabilityZone="+s1[1],
				"LaunchTemplateConfigs.1.Overrides.1.Priority=1",
				"LaunchTemplateConfigs.1.Overrides.2.InstanceType=c5.large",
				"LaunchTemplateConfigs.1.Overrides.2.SubnetId="+s2[0],
				"LaunchTemplateConfigs.1.Overrides.2.AvailabilityZone="+s2[1],
				"LaunchTemplateConfigs.1.Overrides.2.Priority=2",
				"TagSpecification.1.ResourceType=instance",
				"TagSpecification.1.Tag.1.Key=kubernetes.io/cluster/"+e.Cfg.Cluster, "TagSpecification.1.Tag.1.Value=owned",
				"TagSpecification.1.Tag.2.Key=karpenter.sh/nodepool", "TagSpecification.1.Tag.2.Value=default",
				"TagSpecification.1.Tag.3.Key=karpenter.sh/nodeclaim", "TagSpecification.1.Tag.3.Value=default-"+e.Suffix)
			if err != nil {
				return "", err
			}
			ids := n.Texts("fleetInstanceSet/item/instanceIds/item")
			if len(ids) == 0 {
				return "", fmt.Errorf("no instances launched; errors: %v %v", n.Texts("errorSet/item/errorCode"), n.Texts("errorSet/item/errorMessage"))
			}
			e.State["instance"] = ids[0]
			return ids[0] + " (" + n.Text("fleetInstanceSet/item/instanceType") + ")", firstErr(
				need(n.Text("fleetId") != "", "no fleetId"),
				need(len(n.Texts("errorSet/item/errorCode")) == 0, "unexpected fleet errors %v", n.Texts("errorSet/item/errorCode")))
		}},
		{Name: "ec2:DescribeInstances (running, tagged)", Group: "ec2", Needs: []string{"instance"}, Run: func(ctx context.Context, e *Env) (string, error) {
			var it *Node
			err := poll(ctx, e.Cfg.Wait, func() (bool, error) {
				n, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstances",
					"Filter.1.Name=tag-key", "Filter.1.Value.1=kubernetes.io/cluster/"+e.Cfg.Cluster,
					"Filter.2.Name=instance-state-name", "Filter.2.Value.1=pending", "Filter.2.Value.2=running")
				if err != nil {
					return false, err
				}
				for _, cand := range n.All("reservationSet/item/instancesSet/item") {
					if cand.Text("instanceId") == e.State["instance"] {
						it = cand
						return cand.Text("instanceState/name") == "running", nil
					}
				}
				return false, nil
			})
			if err != nil {
				return "", fmt.Errorf("instance %s never showed as running under the tag filter: %w", e.State["instance"], err)
			}
			tags := map[string]string{}
			for _, t := range it.All("tagSet/item") {
				tags[t.Text("key")] = t.Text("value")
			}
			note := ""
			if it.Text("instanceLifecycle") != "spot" {
				note = "; NOTE: no instanceLifecycle=spot (spot-only checks need simctl -any-market)"
			}
			return "running at " + it.Text("privateIpAddress") + note, firstErr(
				need(it.Text("privateIpAddress") != "", "no privateIpAddress (node-registrar names the Node from it)"),
				need(it.Text("placement/availabilityZone") != "", "no placement.availabilityZone (providerID needs it)"),
				need(it.Text("instanceType") != "", "no instanceType"),
				need(tags["karpenter.sh/nodepool"] == "default" && tags["kubernetes.io/cluster/"+e.Cfg.Cluster] == "owned",
					"fleet TagSpecification tags were not applied to the instance: %v", tags))
		}},
		{Name: "ec2:CreateTags", Group: "ec2", Needs: []string{"instance"}, Run: func(ctx context.Context, e *Env) (string, error) {
			if _, err := e.Query(ctx, "ec2", ec2Version, "CreateTags", "ResourceId.1="+e.State["instance"], "Tag.1.Key=Name", "Tag.1.Value=s0probe"); err != nil {
				return "", err
			}
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstances", "InstanceId.1="+e.State["instance"])
			if err != nil {
				return "", err
			}
			found := false
			for _, t := range n.All("reservationSet/item/instancesSet/item/tagSet/item") {
				found = found || (t.Text("key") == "Name" && t.Text("value") == "s0probe")
			}
			return "tag visible", need(found, "tag added by CreateTags is not visible in DescribeInstances")
		}},
		{Name: "ec2:RunInstances", Group: "ec2", Needs: []string{"ami", "subnets"}, Run: func(ctx context.Context, e *Env) (string, error) {
			s := strings.SplitN(e.Lists["subnets"][0], "|", 2)
			n, err := e.Query(ctx, "ec2", ec2Version, "RunInstances", "ImageId="+e.State["ami"], "InstanceType=t3.medium",
				"MinCount=1", "MaxCount=1", "SubnetId="+s[0])
			if err != nil {
				return "", err
			}
			ids := n.Texts("instancesSet/item/instanceId")
			if len(ids) != 1 {
				return "", fmt.Errorf("RunInstances returned %d instances", len(ids))
			}
			e.State["runInstance"] = ids[0]
			return ids[0], nil
		}},
		{Name: "ec2:TerminateInstances", Group: "ec2", Needs: []string{"instance"}, Run: func(ctx context.Context, e *Env) (string, error) {
			ids := []string{e.State["instance"]}
			if e.State["runInstance"] != "" {
				ids = append(ids, e.State["runInstance"])
			}
			params := make([]string, len(ids))
			for i, id := range ids {
				params[i] = fmt.Sprintf("InstanceId.%d=%s", i+1, id)
			}
			n, err := e.Query(ctx, "ec2", ec2Version, "TerminateInstances", params...)
			if err != nil {
				return "", err
			}
			cur := n.Texts("instancesSet/item/currentState/name")
			ok := len(cur) == len(ids)
			for _, c := range cur {
				ok = ok && (c == "shutting-down" || c == "terminated")
			}
			if err := need(ok, "current states = %v", cur); err != nil {
				return "", err
			}
			// Karpenter waits for "terminated"; make sure the instance gets there.
			err = poll(ctx, e.Cfg.Wait, func() (bool, error) {
				d, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstances", "InstanceId.1="+e.State["instance"])
				if err != nil {
					return false, err
				}
				return d.Text("reservationSet/item/instancesSet/item/instanceState/name") == "terminated", nil
			})
			return strings.Join(cur, ","), err
		}},
		{Name: "ec2:DeleteLaunchTemplate", Group: "ec2", Needs: []string{"lt"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.Query(ctx, "ec2", ec2Version, "DeleteLaunchTemplate", "LaunchTemplateName="+e.State["lt"])
			return "", err
		}},

		// ---- Pricing ----
		{Name: "pricing:GetProducts", Group: "pricing", Run: func(ctx context.Context, e *Env) (string, error) {
			one, err := listProducts(ctx, e, priceFilters(e.Cfg.Region, map[string]string{"Type": "TERM_MATCH", "Field": "instanceType", "Value": probeType}))
			if err != nil {
				return "", err
			}
			if len(one) == 0 {
				return "", fmt.Errorf("no on-demand price for %s in %s (the backend's price data does not cover this region or type)", probeType, e.Cfg.Region)
			}
			typ, price, err := parsePrice(one[0])
			if err != nil {
				return "", err
			}
			all, err := listProducts(ctx, e, priceFilters(e.Cfg.Region))
			if err != nil {
				return "", fmt.Errorf("unfiltered-by-type listing: %w", err)
			}
			detail := fmt.Sprintf("%s $%v/h; %d priced types", probeType, price, len(all))
			if tc := e.State["typeCount"]; tc != "" {
				detail += " of " + tc + " listed by EC2"
			}
			return detail, firstErr(
				need(typ == probeType, "attributes.instanceType = %q", typ),
				need(price > 0, "no terms.OnDemand.*.priceDimensions.*.pricePerUnit.USD"))
		}},

		// ---- IAM cleanup (also operations Karpenter calls) ----
		{Name: "iam:RemoveRoleFromInstanceProfile", Group: "iam", Needs: []string{"role", "profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.Query(ctx, "iam", iamVersion, "RemoveRoleFromInstanceProfile", "InstanceProfileName="+e.State["profile"], "RoleName="+e.State["role"])
			return "", err
		}},
		{Name: "iam:DeleteInstanceProfile", Group: "iam", Needs: []string{"profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.Query(ctx, "iam", iamVersion, "DeleteInstanceProfile", "InstanceProfileName="+e.State["profile"])
			return "", err
		}},
	}
}
```

The Pricing check and the gate's price criterion share one set of helpers, so what the probe checks and what the gate compares cannot drift apart:

`internal/s0probe/prices.go`:

```go
package s0probe

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// priceFilters is the filter set Karpenter sends to Pricing GetProducts for Linux on-demand prices.
func priceFilters(region string, extra ...map[string]string) []map[string]string {
	f := []map[string]string{
		{"Type": "TERM_MATCH", "Field": "regionCode", "Value": region},
		{"Type": "TERM_MATCH", "Field": "tenancy", "Value": "Shared"},
		{"Type": "TERM_MATCH", "Field": "preInstalledSw", "Value": "NA"},
		{"Type": "TERM_MATCH", "Field": "operatingSystem", "Value": "Linux"},
		{"Type": "TERM_MATCH", "Field": "capacitystatus", "Value": "Used"},
		{"Type": "TERM_MATCH", "Field": "marketoption", "Value": "OnDemand"},
	}
	return append(f, extra...)
}

// listProducts pages through GetProducts and returns every PriceList entry (each a JSON document in a string).
func listProducts(ctx context.Context, e *Env, filters []map[string]string) ([]string, error) {
	var all []string
	token := ""
	for page := 0; page < 50; page++ {
		body := map[string]any{"ServiceCode": "AmazonEC2", "FormatVersion": "aws_v1", "Filters": filters, "MaxResults": 100}
		if token != "" {
			body["NextToken"] = token
		}
		m, err := e.JSONCall(ctx, "pricing", "AWSPriceListService.GetProducts", "1.1", body)
		if err != nil {
			return nil, err
		}
		pl, _ := m["PriceList"].([]any)
		for _, p := range pl {
			s, ok := p.(string)
			if !ok {
				return nil, fmt.Errorf("PriceList entries must be JSON strings, got %T", p)
			}
			all = append(all, s)
		}
		token, _ = m["NextToken"].(string)
		if token == "" {
			return all, nil
		}
	}
	return nil, fmt.Errorf("pagination did not finish")
}

// parsePrice reads the instance type and the on-demand USD price from one PriceList document, using the paths
// Karpenter reads: product.attributes.instanceType and terms.OnDemand.*.priceDimensions.*.pricePerUnit.USD.
func parsePrice(raw string) (instanceType string, usd float64, err error) {
	var doc struct {
		Product struct {
			Attributes map[string]string `json:"attributes"`
		} `json:"product"`
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct {
					PricePerUnit map[string]string `json:"pricePerUnit"`
				} `json:"priceDimensions"`
			} `json:"OnDemand"`
		} `json:"terms"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", 0, fmt.Errorf("PriceList entry is not JSON: %w", err)
	}
	for _, t := range doc.Terms.OnDemand {
		for _, d := range t.PriceDimensions {
			usd, _ = strconv.ParseFloat(d.PricePerUnit["USD"], 64)
		}
	}
	return doc.Product.Attributes["instanceType"], usd, nil
}

// OnDemandPrices returns USD per hour for each instance type, asked of the backend exactly as Karpenter asks.
// The gate uses it to work out which type Karpenter should have chosen.
func OnDemandPrices(ctx context.Context, cfg Config, types []string) (map[string]float64, error) {
	e := &Env{Cfg: &cfg, State: map[string]string{}, Lists: map[string][]string{}}
	out := map[string]float64{}
	for _, t := range types {
		docs, err := listProducts(ctx, e, priceFilters(cfg.Region, map[string]string{"Type": "TERM_MATCH", "Field": "instanceType", "Value": t}))
		if err != nil {
			return nil, err
		}
		if len(docs) == 0 {
			return nil, fmt.Errorf("no on-demand price for %s", t)
		}
		typ, usd, err := parsePrice(docs[0])
		if err != nil {
			return nil, err
		}
		if typ != t || usd <= 0 {
			return nil, fmt.Errorf("price document for %s says type %q price %v", t, typ, usd)
		}
		out[t] = usd
	}
	return out, nil
}
```

- [ ] **Step 5: Write the report and the binary**

`internal/s0probe/report.go`:

```go
package s0probe

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Summary counts results by status.
type Summary struct {
	Pass, Fail, Skip int
}

// Summarize counts pass, fail and skip results.
func Summarize(rs []Result) Summary {
	var s Summary
	for _, r := range rs {
		switch r.Status {
		case "pass":
			s.Pass++
		case "fail":
			s.Fail++
		default:
			s.Skip++
		}
	}
	return s
}

// FailedGroups returns the service groups with at least one failed or
// skipped check, sorted. The S0 decision matrix reads this list: a group in
// it needs a custom backend (or a documented gap) before Karpenter can run.
func FailedGroups(rs []Result) []string {
	set := map[string]bool{}
	for _, r := range rs {
		if r.Status != "pass" {
			set[r.Group] = true
		}
	}
	out := make([]string, 0, len(set))
	for g := range set {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// Markdown renders the per-operation table that goes into docs/s0/report.md.
func Markdown(title string, rs []Result) string {
	var b strings.Builder
	s := Summarize(rs)
	fmt.Fprintf(&b, "# %s\n\n%d passed, %d failed, %d skipped.\n\n", title, s.Pass, s.Fail, s.Skip)
	b.WriteString("| Check | Group | Status | Detail |\n|---|---|---|---|\n")
	for _, r := range rs {
		// Pipes and newlines would break the table, so flatten them.
		d := strings.NewReplacer("|", "/", "\n", " ").Replace(r.Detail)
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", r.Name, r.Group, r.Status, d)
	}
	if fg := FailedGroups(rs); len(fg) > 0 {
		fmt.Fprintf(&b, "\nGroups needing a custom backend or a recorded gap: %s\n", strings.Join(fg, ", "))
	} else {
		b.WriteString("\nEvery group passed on this backend.\n")
	}
	return b.String()
}

// JSON renders the results for scripts (the gate and the decision matrix).
func JSON(rs []Result) ([]byte, error) {
	return json.MarshalIndent(struct {
		Summary      Summary  `json:"summary"`
		FailedGroups []string `json:"failed_groups"`
		Results      []Result `json:"results"`
	}{Summarize(rs), FailedGroups(rs), rs}, "", "  ")
}
```

`cmd/s0probe/main.go`:

```go
// Command s0probe runs the S0 contract probe against an AWS endpoint (the
// aws-gateway in front of Floci or our own simulators) and writes a report.
// Exit status is 0 only when every selected check passed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/tazzledazzle/meridian/internal/s0probe"
)

func main() {
	var (
		endpoint = flag.String("endpoint", "http://localhost:4566", "gateway URL")
		region   = flag.String("region", "us-east-1", "AWS region")
		cluster  = flag.String("cluster", "meridian", "cluster name used for the karpenter.sh/discovery tag")
		ami      = flag.String("ami-param", "/aws/service/eks/optimized-ami/1.31/amazon-linux-2023/x86_64/standard/recommended/image_id", "SSM parameter holding the AMI id")
		queue    = flag.String("queue", "meridian-interruptions", "SQS interruption queue name")
		create   = flag.Bool("eks-create", false, "call eks:CreateCluster before DescribeCluster")
		groups   = flag.String("groups", "", "comma-separated groups to run (default all)")
		wait     = flag.Duration("wait", 60*time.Second, "how long to wait for an instance to run")
		out      = flag.String("out", "", "directory for report.md and report.json (default: print only)")
		title    = flag.String("title", "S0 contract probe", "report title")
		prices   = flag.String("prices", "", "comma-separated instance types: print their on-demand prices as JSON and exit (used by the gate)")
	)
	flag.Parse()

	cfg := s0probe.Config{
		Endpoint: *endpoint, Region: *region, Cluster: *cluster, AMIParam: *ami,
		QueueName: *queue, EKSCreate: *create, Wait: *wait,
		// Access keys are fixed test credentials; the gateway verifies the signature against them.
		Access: envOr("AWS_ACCESS_KEY_ID", "test"), Secret: envOr("AWS_SECRET_ACCESS_KEY", "test"),
	}
	if *groups != "" {
		cfg.Groups = strings.Split(*groups, ",")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *prices != "" {
		got, err := s0probe.OnDemandPrices(ctx, cfg, strings.Split(*prices, ","))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(got)
		return
	}
	results := s0probe.Run(ctx, cfg, s0probe.Checks())
	md := s0probe.Markdown(*title, results)
	fmt.Print(md)

	if *out != "" {
		js, err := s0probe.JSON(results)
		if err == nil {
			err = os.MkdirAll(*out, 0o755)
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(*out, "report.md"), []byte(md), 0o644)
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(*out, "report.json"), js, 0o644)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "write report:", err)
			os.Exit(2)
		}
	}
	if s := s0probe.Summarize(results); s.Fail+s.Skip > 0 {
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `gofmt -l internal cmd && go vet ./internal/s0probe/ ./cmd/s0probe/ && go test -race ./internal/s0probe/ -v`
Expected: no gofmt output; all tests PASS (`TestEveryProbedOperationIsInOpsYaml` reads the real `ops.yaml` from Task 2).

- [ ] **Step 7: Write the decision script and its tests**

`scripts/s0matrix.py`:

```python
#!/usr/bin/env python3
"""Turns the S0 probe report into a per-operation backend decision.

  s0matrix.py REPORT.json OPS.yaml            print the decision table (markdown)
  s0matrix.py REPORT.json OPS.yaml --apply    also rewrite the backend field of each operation in OPS.yaml

Rule: an operation stays on floci only if every probe check for it passed. An operation that failed or
was skipped moves to the custom backend for its service. Operations that share instance state
(ops.Coupled in internal/ops/ops.go) move together, because splitting them would give one backend an
instance that another cannot see.
"""
import json
import re
import sys

# The custom backend that replaces Floci for each service, and the plan task that builds it.
FALLBACK = {
    "ec2": ("ec2-sim", "Tasks 11 and 12"),
    "pricing": ("pricing-facade", "Task 13"),
    "eks": ("eks-facade", "Task 13"),
    "iam": ("moto", "Task 14"),
    "sts": ("moto", "Task 14"),
    "sqs": ("moto", "Task 14"),
    "ssm": ("moto", "Task 14"),
}

# Keep in step with ops.Coupled in internal/ops/ops.go; test_s0matrix.py compares the two.
COUPLED = [
    "ec2:CreateFleet", "ec2:RunInstances", "ec2:TerminateInstances", "ec2:DescribeInstances", "ec2:CreateTags",
    "ec2:CreateLaunchTemplate", "ec2:DescribeLaunchTemplates", "ec2:DeleteLaunchTemplate",
    "ec2:DescribeImages", "ec2:DescribeSubnets", "ec2:DescribeSecurityGroups",
]

OP_LINE = re.compile(r"^(\s*- \{service: (\w+), name: (\w+),.*?backend: )([a-z][a-z0-9-]*)(,.*)$")


def operation_status(results):
    """Maps 'service:Op' to 'pass' or 'fail'. A check's name starts with the operation ("ec2:DescribeSubnets (discovery tag)")."""
    status = {}
    for r in results:
        op = r["name"].split(" ")[0]
        bad = r["status"] != "pass"
        status[op] = "fail" if bad or status.get(op) == "fail" else "pass"
    return status


def decide(results, ops):
    """Returns {op: (backend, reason)} for every operation in ops (a list of (service, name, current_backend))."""
    status = operation_status(results)
    failed = {op for op, s in status.items() if s == "fail"}
    if any(c in failed for c in COUPLED):
        failed |= set(COUPLED)  # move the whole instance-state group, not one operation
    out = {}
    for svc, name, _cur in ops:
        key = f"{svc}:{name}"
        if key in failed:
            out[key] = (FALLBACK[svc][0], "probe failed" if status.get(key) == "fail" else "coupled to a failing operation")
        elif key in status:
            out[key] = ("floci", "probe passed")
        else:
            out[key] = ("floci", "not probed (keeps its current backend until the gate traces it)")
    return out


def read_ops(path):
    ops = []
    with open(path) as f:
        for line in f:
            m = OP_LINE.match(line)
            if m:
                ops.append((m.group(2), m.group(3), m.group(4)))
    return ops


def apply(path, decision):
    with open(path) as f:
        lines = f.read().split("\n")
    for i, line in enumerate(lines):
        m = OP_LINE.match(line)
        if m:
            backend = decision[f"{m.group(2)}:{m.group(3)}"][0]
            lines[i] = m.group(1) + backend + m.group(5)
    with open(path, "w") as f:
        f.write("\n".join(lines))


def table(decision):
    rows = ["| Operation | Backend | Why |", "|---|---|---|"]
    for key in sorted(decision):
        backend, why = decision[key]
        rows.append(f"| {key} | {backend} | {why} |")
    tasks = sorted({FALLBACK[k.split(":")[0]][1] for k, (b, _) in decision.items() if b != "floci"})
    rows.append("")
    rows.append("Tasks to execute: " + (", ".join(tasks) if tasks else "none (Floci serves every probed operation)"))
    return "\n".join(rows)


def main(argv):
    if len(argv) < 3:
        print(__doc__)
        return 2
    with open(argv[1]) as f:
        report = json.load(f)
    decision = decide(report["results"], read_ops(argv[2]))
    print(table(decision))
    if "--apply" in argv:
        apply(argv[2], decision)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
```

`scripts/test_s0matrix.py`:

```python
import os
import re
import tempfile
import unittest

import s0matrix as m

OPS = [("ec2", "CreateFleet", "floci"), ("ec2", "DescribeInstances", "floci"), ("ec2", "DescribeSpotPriceHistory", "floci"),
       ("ec2", "CreateTags", "floci"), ("pricing", "GetProducts", "floci"), ("sqs", "CreateQueue", "floci")]


def res(name, status):
    return {"name": name, "group": name.split(":")[0], "status": status, "detail": ""}


class DecideTests(unittest.TestCase):
    def test_everything_passing_stays_on_floci(self):
        d = m.decide([res("ec2:CreateFleet", "pass"), res("pricing:GetProducts", "pass")], OPS)
        self.assertTrue(all(b == "floci" for b, _ in d.values()))
        self.assertIn("none", m.table(d))

    def test_reference_data_failure_moves_only_that_operation(self):
        d = m.decide([res("ec2:DescribeSpotPriceHistory", "fail"), res("ec2:CreateFleet", "pass")], OPS)
        self.assertEqual(d["ec2:DescribeSpotPriceHistory"][0], "ec2-sim")
        self.assertEqual(d["ec2:CreateFleet"][0], "floci")

    def test_lifecycle_failure_moves_the_whole_coupled_group(self):
        d = m.decide([res("ec2:CreateFleet", "fail"), res("ec2:DescribeInstances (running, tagged)", "pass")], OPS)
        for key in ("ec2:CreateFleet", "ec2:DescribeInstances", "ec2:CreateTags"):
            self.assertEqual(d[key][0], "ec2-sim", key)
        self.assertEqual(d["ec2:DescribeSpotPriceHistory"][0], "floci")
        self.assertIn("coupled", d["ec2:CreateTags"][1])

    def test_skipped_counts_as_failed_and_services_get_their_own_fallback(self):
        d = m.decide([res("pricing:GetProducts", "skip"), res("sqs:CreateQueue", "fail")], OPS)
        self.assertEqual(d["pricing:GetProducts"][0], "pricing-facade")
        self.assertEqual(d["sqs:CreateQueue"][0], "moto")
        self.assertIn("Task 13", m.table(d))
        self.assertIn("Task 14", m.table(d))

    def test_apply_rewrites_only_the_backend_field(self):
        src = "ops:\n  - {service: ec2, name: CreateFleet, protocol: query, backend: floci, fidelity: behavior-inferred}\n"
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            f.write(src)
        try:
            d = m.decide([res("ec2:CreateFleet", "fail")], m.read_ops(f.name))
            m.apply(f.name, d)
            with open(f.name) as g:
                self.assertEqual(g.read(), src.replace("backend: floci", "backend: ec2-sim"))
        finally:
            os.unlink(f.name)


class CoupledListTests(unittest.TestCase):
    def test_python_list_matches_the_go_list(self):
        with open(os.path.join(os.path.dirname(__file__), "..", "internal", "ops", "ops.go")) as f:
            go = f.read()
        block = re.search(r"var Coupled = \[\]string\{(.*?)\n\}", go, re.S).group(1)
        self.assertEqual(sorted(re.findall(r'"([^"]+)"', block)), sorted(m.COUPLED))


if __name__ == "__main__":
    unittest.main()
```

Run: `cd scripts && python3 -W error -m unittest test_s0matrix -v`
Expected: 6 tests PASS, including the check that the Python and Go lists of coupled operations agree.

- [ ] **Step 8: Write the probe runner and Makefile target**

`scripts/s0-run.sh`:

```bash
#!/usr/bin/env bash
# Runs the S0 contract probe through the gateway and writes docs/s0/report.md and report.json.
# The probe runs on the Compose network so it reaches the gateway by name, exactly as Karpenter does.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env; . ./.env.images; set +a
NET="${MERIDIAN_NET:-meridian}"   # the gate sets meridian-gate
OUT="${1:-docs/s0}"
mkdir -p "$OUT"
# --user keeps the report files owned by the caller; the probe image is the distroless static image plus our binary.
docker run --rm --network "$NET" --user "$(id -u):$(id -g)" \
  -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
  -v "$PWD/bin/linux/s0probe:/s0probe:ro" -v "$PWD/$OUT:/out" \
  --entrypoint /s0probe "${STATIC_IMAGE}" \
  -endpoint http://aws-gateway:4566 -region us-west-2 -cluster meridian-g0 -eks-create \
  -queue meridian-g0-interruption \
  -ami-param "/aws/service/eks/optimized-ami/$(echo "$K3S_VERSION" | sed -E 's/^v?([0-9]+\.[0-9]+).*/\1/')/amazon-linux-2023/x86_64/standard/recommended/image_id" \
  -title "S0 contract probe: ${S0_LABEL:-floci}" -out /out || echo "probe reported failures (expected during S0); see $OUT/report.md"
```

Append to `Makefile`:

```make
# Runs the contract probe against the running S0 stack and writes docs/s0/report.{md,json}.
s0-probe: build-linux
	./scripts/s0-run.sh docs/s0

# Prints the per-operation decision from the latest report (add APPLY=1 to rewrite ops.yaml).
s0-decide:
	python3 scripts/s0matrix.py docs/s0/report.json ops.yaml $(if $(APPLY),--apply,)
```

Run `chmod +x scripts/*.sh scripts/s0matrix.py`.

- [ ] **Step 9: Run the probe against Floci**

Run: `make s0-up && make s0-probe`
Expected: the Markdown table prints and `docs/s0/report.md` and `report.json` are written. Failures are expected and are the point of the spike. Before trusting a failure, read its detail and decide whether Floci is wrong or the probe is: the probe was validated against a reference EC2 implementation in this plan's design, but a check can still be too strict for an emulator that is correct on the fields Karpenter reads. If the probe is wrong, fix the check, add a test case, and re-run; do not edit the report by hand.

- [ ] **Step 10: Look at the decision, then stop for the user**

Run: `make s0-decide`
Expected: a table of every operation with its proposed backend and the reason, plus a `Tasks to execute:` line naming which of Tasks 11 to 14 are needed (or `none`).

**Decision point (do not skip):** present that table to the user. It is a trade-off they own: each operation that stays on Floci is code we do not write and a dependency we take on; each operation that moves is a component we keep. When they approve, run `make s0-decide APPLY=1`, then write `docs/s0/decision.md` with the table, the date, the Floci version from `versions.yaml`, and one paragraph on anything surprising (for example where Floci attaches EC2 instance containers). `ops.Validate` (Task 2) rejects an `ops.yaml` that splits the instance-state operations across backends, so a wrong edit fails `go test ./internal/ops/`.

- [ ] **Step 11: Commit**

```bash
git add internal/s0probe cmd/s0probe scripts docs/s0 Makefile ops.yaml
git commit -m "feat: add the S0 contract probe, the Floci report and the backend decision"
```

### Task 9: Spot interruptions (`spot` package, `simctl`) and a fake EC2 for client tests

**Why:** no AWS emulator models EC2's spot interruption notice. On real AWS, EC2 emits an EventBridge event two minutes before it reclaims a spot instance, and a rule forwards it to the SQS queue Karpenter watches. We reproduce it with public AWS calls only (post the event to SQS, wait out the grace period, call `TerminateInstances`), so it works with Floci, `ec2-sim` or anything else behind the gateway. This is the one capability every S0 outcome needs.

**Files:**
- Create: `internal/fakeec2/fakeec2.go`, `internal/fakeec2/fakeec2_test.go`
- Create: `internal/spot/spot.go`, `internal/spot/spot_test.go`
- Create: `internal/simctl/simctl.go`, `internal/simctl/simctl_test.go`, `cmd/simctl/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `awsapi.NewRequestID` (Task 3); `awscall.BuildQuery`, `BuildJSON` (Task 6); `catalog.Region` (the `catalog` package is created in this task's Step 1).
- Produces:
  - `spot.Event(region, instanceID string, at time.Time) []byte`: the EventBridge "EC2 Spot Instance Interruption Warning" body Karpenter parses
  - `spot.Injector{Endpoint, Region, QueueName, Access, Secret string; AnyMarket bool; Client *http.Client; Sleep func(context.Context, time.Duration) error; Now func() time.Time}` with `Check`, `Warn`, `Reclaim`, `Interrupt(ctx, id, grace)`
  - `simctl.Run(ctx, args, getenv, out, errOut) int` and the `simctl` binary: `simctl [-gateway URL] [-queue NAME] interrupt [-grace 2m] [-any-market] INSTANCE_ID` and `simctl probe URL` (exit 0 if the URL answers, 1 if not; the gate uses it to prove there is no route to the internet)
  - `fakeec2.New(now, pendingFor) *Fake` with `Launch`, `State` and an `http.Handler` answering `DescribeInstances` and `TerminateInstances`. It is a test double for EC2 clients and is never deployed.

**Acceptance Criteria:**
- The event carries `version`, `detail-type`, `source`, `detail.instance-id` and `detail.instance-action`.
- `Interrupt` checks the instance exists, is running and is a spot instance, posts the warning, waits the grace period, then terminates it. A rejected instance (on-demand, pending, unknown) sends nothing. A failed warning leaves the instance running.
- `AnyMarket` skips the spot check for backends that omit `instanceLifecycle`.
- `simctl` exits 2 on bad usage or missing credentials, 1 on failure, 0 on success.
- None of this depends on which backend serves EC2 and SQS.

- [ ] **Step 1: Write the failing tests (and the pieces they need)**

The injector and `simctl` use the region constant from `internal/catalog`. Create the catalog now. It is the data source for KWOK node sizing, and for the fallback `ec2-sim` and `pricing-facade` if S0 requires them (Tasks 11 and 13):

`internal/catalog/catalog.go`:

```go
// Package catalog is the platform's fixed region data: availability zones and
// instance types (CPU, memory, ENI limits) with approximate list prices.
// node-registrar reads it to size KWOK nodes. If S0 requires custom services,
// ec2-sim and pricing-facade read it too, so they always agree on what exists
// and what it costs.
package catalog

import "sort"

// Region is the only region the platform serves.
const Region = "us-west-2"

// AZ is one availability zone.
type AZ struct {
	Name string // us-west-2a
	ID   string // usw2-az1 (the zone ID, stable across accounts)
}

// AZs are the zones, in order.
var AZs = []AZ{
	{"us-west-2a", "usw2-az2"},
	{"us-west-2b", "usw2-az1"},
	{"us-west-2c", "usw2-az3"},
}

// Type is one instance type.
type Type struct {
	Name       string
	VCPU       int
	MemoryMiB  int
	Arch       string  // x86_64 | arm64
	MaxENIs    int     // maximum network interfaces
	IPv4PerENI int     // IPv4 addresses per interface
	OnDemand   float64 // USD per hour, approximate Linux list price
}

// Types is sorted by name. Prices are approximate public list prices, used only
// so that cost-based decisions in Karpenter are plausible.
var Types = []Type{
	{"c5.2xlarge", 8, 16384, "x86_64", 4, 15, 0.34},
	{"c5.large", 2, 4096, "x86_64", 3, 10, 0.085},
	{"c5.xlarge", 4, 8192, "x86_64", 4, 15, 0.17},
	{"m5.2xlarge", 8, 32768, "x86_64", 4, 15, 0.384},
	{"m5.large", 2, 8192, "x86_64", 3, 10, 0.096},
	{"m5.xlarge", 4, 16384, "x86_64", 4, 15, 0.192},
	{"m6g.large", 2, 8192, "arm64", 3, 10, 0.077},
	{"m6g.xlarge", 4, 16384, "arm64", 4, 15, 0.154},
	{"r5.large", 2, 16384, "x86_64", 3, 10, 0.126},
	{"r5.xlarge", 4, 32768, "x86_64", 4, 15, 0.252},
	{"t3.large", 2, 8192, "x86_64", 3, 12, 0.0832},
	{"t3.medium", 2, 4096, "x86_64", 3, 6, 0.0416},
}

func init() {
	// Lookup and binary-search callers rely on this order; enforce it once.
	sort.Slice(Types, func(i, j int) bool { return Types[i].Name < Types[j].Name })
}

// Lookup finds an instance type by name.
func Lookup(name string) (Type, bool) {
	for _, t := range Types {
		if t.Name == name {
			return t, true
		}
	}
	return Type{}, false
}

// HasAZ reports whether the zone name exists.
func HasAZ(name string) bool {
	for _, z := range AZs {
		if z.Name == name {
			return true
		}
	}
	return false
}

// SpotPrice is a deterministic spot price: about 30% of on-demand, with a small
// per-zone offset so zones differ and Karpenter has something to choose between.
func SpotPrice(t Type, az string) float64 {
	offset := 0.0
	for i, z := range AZs {
		if z.Name == az {
			offset = float64(i) * 0.01
		}
	}
	p := t.OnDemand * (0.30 + offset)
	// Round to 4 decimals like the real API.
	return float64(int(p*10000+0.5)) / 10000
}
```

`internal/catalog/catalog_test.go`:

```go
package catalog

import "testing"

func TestLookupAndSortOrder(t *testing.T) {
	if _, ok := Lookup("m5.large"); !ok {
		t.Fatal("m5.large missing")
	}
	if _, ok := Lookup("nope.large"); ok {
		t.Fatal("unexpected type")
	}
	for i := 1; i < len(Types); i++ {
		if Types[i-1].Name >= Types[i].Name {
			t.Fatalf("Types not sorted at %s", Types[i].Name)
		}
	}
}

func TestSpotPriceIsBelowOnDemandAndVariesByZone(t *testing.T) {
	m5, _ := Lookup("m5.large")
	a, b := SpotPrice(m5, "us-west-2a"), SpotPrice(m5, "us-west-2b")
	if a >= m5.OnDemand || b >= m5.OnDemand {
		t.Fatalf("spot %v/%v should be below on-demand %v", a, b, m5.OnDemand)
	}
	if a == b {
		t.Fatal("zones should differ in spot price")
	}
	if SpotPrice(m5, "us-west-2a") != a {
		t.Fatal("spot price must be deterministic")
	}
}
```

Then the fake EC2 and its test:

`internal/fakeec2/fakeec2_test.go`:

```go
package fakeec2

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func post(t *testing.T, srv *httptest.Server, form url.Values) string {
	t.Helper()
	resp, err := srv.Client().PostForm(srv.URL, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

func TestStatesFollowTheClockAndTerminateIsVisible(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	f := New(func() time.Time { return now }, 3*time.Second)
	id := f.Launch("m5.large", "us-west-2a", "spot", map[string]string{"k": "v"})
	if s, _ := f.State(id); s != "pending" {
		t.Fatalf("state = %s, want pending", s)
	}
	now = now.Add(5 * time.Second)
	if s, _ := f.State(id); s != "running" {
		t.Fatalf("state = %s, want running", s)
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	out := post(t, srv, url.Values{"Action": {"DescribeInstances"}, "InstanceId.1": {id}})
	for _, want := range []string{"<instanceId>" + id + "</instanceId>", "<name>running</name>", "<instanceLifecycle>spot</instanceLifecycle>", "<key>k</key>"} {
		if !strings.Contains(out, want) {
			t.Errorf("DescribeInstances missing %s in %s", want, out)
		}
	}
	post(t, srv, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {id}})
	if s, _ := f.State(id); s != "shutting-down" {
		t.Fatalf("state = %s, want shutting-down", s)
	}
}

func TestTagKeyFilterAndUnknownIDs(t *testing.T) {
	f := New(time.Now, 0)
	mine := f.Launch("m5.large", "us-west-2a", "", map[string]string{"cluster": "owned"})
	f.Launch("c5.large", "us-west-2a", "", nil)
	srv := httptest.NewServer(f)
	defer srv.Close()
	out := post(t, srv, url.Values{"Action": {"DescribeInstances"}, "Filter.1.Name": {"tag-key"}, "Filter.1.Value.1": {"cluster"}})
	if strings.Count(out, "<instanceId>") != 1 || !strings.Contains(out, mine) {
		t.Fatalf("filter result = %s", out)
	}
	if out := post(t, srv, url.Values{"Action": {"DescribeInstances"}, "InstanceId.1": {"i-nope"}}); strings.Contains(out, "<instanceId>") {
		t.Fatalf("unknown ID returned instances: %s", out)
	}
}
```

The injector and `simctl` tests:

`internal/spot/spot_test.go`:

```go
package spot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/fakeec2"
)

// fakeAWS serves EC2 from the fake EC2 and SQS from a recorder, behind one URL.
type fakeAWS struct {
	ec2      http.Handler
	sqsCalls []string
	sqsBody  []string
	sqsFail  bool
}

func (f *fakeAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if target := r.Header.Get("X-Amz-Target"); strings.HasPrefix(target, "AmazonSQS.") {
		b, _ := io.ReadAll(r.Body)
		f.sqsCalls = append(f.sqsCalls, strings.TrimPrefix(target, "AmazonSQS."))
		f.sqsBody = append(f.sqsBody, string(b))
		if f.sqsFail {
			http.Error(w, `{"message":"down"}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"QueueUrl":"http://aws-gateway:4566/123456789012/meridian-g0-interruption","MessageId":"m1"}`))
		return
	}
	f.ec2.ServeHTTP(w, r)
}

type rig struct {
	in    *Injector
	store *fakeec2.Fake
	aws   *fakeAWS
	now   *time.Time
	slept []time.Duration
}

func newRig(t *testing.T) *rig {
	t.Helper()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	r := &rig{now: &now}
	r.store = fakeec2.New(func() time.Time { return now }, 3*time.Second)
	r.aws = &fakeAWS{ec2: r.store}
	srv := httptest.NewServer(r.aws)
	t.Cleanup(srv.Close)
	r.in = &Injector{Endpoint: srv.URL, Region: "us-west-2", QueueName: "meridian-g0-interruption", Access: "AK", Secret: "SK",
		Sleep: func(_ context.Context, d time.Duration) error {
			r.slept = append(r.slept, d)
			now = now.Add(d)
			return nil
		},
		Now: func() time.Time { return now }}
	return r
}

func (r *rig) launch(t *testing.T, market string) string {
	t.Helper()
	id := r.store.Launch("m5.large", "us-west-2a", market, nil)
	*r.now = r.now.Add(5 * time.Second) // past pending
	return id
}

func TestEventHasTheFieldsKarpenterParses(t *testing.T) {
	var ev struct {
		Version, Source string
		DetailType      string `json:"detail-type"`
		Resources       []string
		Detail          map[string]string
	}
	if err := json.Unmarshal(Event("us-west-2", "i-abc", time.Unix(0, 0)), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Version != "0" || ev.Source != "aws.ec2" || ev.DetailType != "EC2 Spot Instance Interruption Warning" ||
		ev.Detail["instance-id"] != "i-abc" || ev.Detail["instance-action"] != "terminate" || len(ev.Resources) != 1 {
		t.Fatalf("event = %+v", ev)
	}
}

func TestInterruptWarnsWaitsThenTerminates(t *testing.T) {
	r := newRig(t)
	id := r.launch(t, "spot")
	if err := r.in.Interrupt(context.Background(), id, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.aws.sqsCalls, ",") != "GetQueueUrl,SendMessage" || !strings.Contains(r.aws.sqsBody[1], id) {
		t.Fatalf("sqs calls = %v bodies = %v", r.aws.sqsCalls, r.aws.sqsBody)
	}
	if len(r.slept) != 1 || r.slept[0] != 2*time.Minute {
		t.Fatalf("slept %v, want one 2m grace period", r.slept)
	}
	if state, _ := r.store.State(id); state == "running" {
		t.Fatalf("instance should be terminating, is %s", state)
	}
}

func TestInterruptRejectsOnDemandPendingAndUnknownInstances(t *testing.T) {
	r := newRig(t)
	od := r.launch(t, "")
	if err := r.in.Interrupt(context.Background(), od, time.Minute); err == nil {
		t.Error("on-demand instance must be rejected")
	}
	r.in.AnyMarket = true
	if err := r.in.Check(context.Background(), od); err != nil {
		t.Errorf("AnyMarket should accept it: %v", err)
	}
	r.in.AnyMarket = false
	if err := r.in.Interrupt(context.Background(), "i-nope", time.Minute); err == nil {
		t.Error("unknown instance must be rejected")
	}
	fresh := r.store.Launch("m5.large", "us-west-2a", "spot", nil)
	if err := r.in.Interrupt(context.Background(), fresh, time.Minute); err == nil {
		t.Error("pending instance must be rejected")
	}
	if len(r.aws.sqsCalls) != 0 || len(r.slept) != 0 {
		t.Errorf("rejections must send nothing: %v %v", r.aws.sqsCalls, r.slept)
	}
}

func TestFailedWarningLeavesTheInstanceRunning(t *testing.T) {
	r := newRig(t)
	id := r.launch(t, "spot")
	r.aws.sqsFail = true
	if err := r.in.Interrupt(context.Background(), id, time.Minute); err == nil {
		t.Fatal("expected an error")
	}
	if state, _ := r.store.State(id); state != "running" || len(r.slept) != 0 {
		t.Fatalf("state=%s slept=%v: a failed warning must not terminate anything", state, r.slept)
	}
}
```

`internal/simctl/simctl_test.go`:

```go
package simctl

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/fakeec2"
)

func run(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(context.Background(), args, func(k string) string { return env[k] }, &out, &errOut)
	return code, out.String(), errOut.String()
}

var creds = map[string]string{"AWS_ACCESS_KEY_ID": "AK", "AWS_SECRET_ACCESS_KEY": "SK"}

func TestBadUsageAndMissingCredentialsExitTwo(t *testing.T) {
	for _, args := range [][]string{{}, {"interrupt"}, {"probe"}, {"nonsense"}} {
		if code, _, _ := run(t, creds, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if code, _, e := run(t, nil, "interrupt", "i-abc"); code != 2 || !strings.Contains(e, "AWS_ACCESS_KEY_ID") {
		t.Errorf("no credentials: %d %s", code, e)
	}
}

func TestInterruptCommandRunsTheSequenceAgainstTheGateway(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store := fakeec2.New(func() time.Time { return now }, time.Second)
	id := store.Launch("m5.large", "us-west-2a", "spot", nil)
	now = now.Add(5 * time.Second)
	ec2 := http.Handler(store)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("X-Amz-Target"), "AmazonSQS.") {
			_, _ = w.Write([]byte(`{"QueueUrl":"http://q/1"}`))
			return
		}
		ec2.ServeHTTP(w, r)
	}))
	defer srv.Close()
	code, out, e := run(t, creds, "-gateway", srv.URL, "interrupt", "-grace", "1ms", id)
	if code != 0 || !strings.Contains(out, "interrupted "+id) {
		t.Fatalf("exit %d out=%q err=%q", code, out, e)
	}
	if state, _ := store.State(id); state == "running" {
		t.Fatalf("instance should be terminating, is %s", state)
	}
	if code, _, _ := run(t, creds, "-gateway", srv.URL, "interrupt", "-grace", "1ms", "i-nope"); code != 1 {
		t.Fatalf("unknown instance: exit %d, want 1", code)
	}
}

func TestProbeDistinguishesReachableFromUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	code, out, _ := run(t, creds, "probe", srv.URL)
	if code != 0 || !strings.Contains(out, "reachable") {
		t.Fatalf("reachable: %d %q", code, out)
	}
	srv.Close()
	code, out, _ = run(t, creds, "-timeout", "500ms", "probe", srv.URL)
	if code != 1 || !strings.Contains(out, "unreachable") {
		t.Fatalf("unreachable: %d %q", code, out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/fakeec2/ ./internal/spot/ ./internal/simctl/ -v`
Expected: FAIL with `undefined: New`, `undefined: Event`, `undefined: Run`.

- [ ] **Step 3: Write the implementations**

`internal/fakeec2/fakeec2.go`:

```go
// Package fakeec2 is a deliberately tiny EC2 stand-in for unit tests of EC2
// clients (the spot injector, simctl, node-registrar's EC2 reader). It answers
// DescribeInstances and TerminateInstances with the response shape AWS
// documents, and nothing else. It is not a simulator and is never deployed:
// the real answers come from Floci or, where S0 requires it, from ec2-sim.
package fakeec2

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type instance struct {
	id, typ, az, lifecycle string
	tags                   map[string]string
	launched               time.Time
	terminated             bool
}

// Fake holds instances in memory; its clock decides when pending becomes running.
type Fake struct {
	mu         sync.Mutex
	now        func() time.Time
	pendingFor time.Duration
	seq        int
	items      map[string]*instance
}

// New returns a Fake whose instances stay pending for pendingFor.
func New(now func() time.Time, pendingFor time.Duration) *Fake {
	return &Fake{now: now, pendingFor: pendingFor, items: map[string]*instance{}}
}

// Launch adds an instance and returns its ID. lifecycle is "spot" or "".
func (f *Fake) Launch(typ, az, lifecycle string, tags map[string]string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("i-%017x", f.seq)
	f.items[id] = &instance{id: id, typ: typ, az: az, lifecycle: lifecycle, tags: tags, launched: f.now()}
	return id
}

// State returns pending, running or shutting-down.
func (f *Fake) State(id string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.items[id]
	if !ok {
		return "", false
	}
	return f.stateLocked(i), true
}

func (f *Fake) stateLocked(i *instance) string {
	switch {
	case i.terminated:
		return "shutting-down"
	case f.now().Sub(i.launched) < f.pendingFor:
		return "pending"
	default:
		return "running"
	}
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	q, _ := url.ParseQuery(string(b))
	f.mu.Lock()
	defer f.mu.Unlock()
	switch q.Get("Action") {
	case "DescribeInstances":
		f.describe(w, q)
	case "TerminateInstances":
		var sb strings.Builder
		for _, id := range listParam(q, "InstanceId") {
			if i, ok := f.items[id]; ok {
				i.terminated = true
				fmt.Fprintf(&sb, "<item><instanceId>%s</instanceId></item>", id)
			}
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, "<TerminateInstancesResponse><instancesSet>%s</instancesSet></TerminateInstancesResponse>", sb.String())
	default:
		http.Error(w, "<Response><Errors><Error><Code>InvalidAction</Code></Error></Errors></Response>", http.StatusBadRequest)
	}
}

// listParam collects Name.1, Name.2, ... in order.
func listParam(q url.Values, name string) []string {
	var out []string
	for n := 1; ; n++ {
		v := q.Get(fmt.Sprintf("%s.%d", name, n))
		if v == "" {
			return out
		}
		out = append(out, v)
	}
}

func (f *Fake) describe(w http.ResponseWriter, q url.Values) {
	ids := listParam(q, "InstanceId")
	tagKey := ""
	if q.Get("Filter.1.Name") == "tag-key" {
		tagKey = q.Get("Filter.1.Value.1")
	}
	keys := make([]string, 0, len(f.items))
	for id := range f.items {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, id := range keys {
		i := f.items[id]
		if len(ids) > 0 && !contains(ids, id) {
			continue
		}
		if _, ok := i.tags[tagKey]; tagKey != "" && !ok {
			continue
		}
		lifecycle := ""
		if i.lifecycle != "" {
			lifecycle = "<instanceLifecycle>" + i.lifecycle + "</instanceLifecycle>"
		}
		var tags strings.Builder
		for k, v := range i.tags {
			fmt.Fprintf(&tags, "<item><key>%s</key><value>%s</value></item>", k, v)
		}
		fmt.Fprintf(&sb, `<item><instancesSet><item><instanceId>%s</instanceId><instanceState><name>%s</name></instanceState>`+
			`<instanceType>%s</instanceType><placement><availabilityZone>%s</availabilityZone></placement>`+
			`<privateDnsName>ip-10-0-0-%d.us-west-2.compute.internal</privateDnsName><privateIpAddress>10.0.0.%d</privateIpAddress>`+
			`%s<tagSet>%s</tagSet></item></instancesSet></item>`,
			id, f.stateLocked(i), i.typ, i.az, len(id), len(id), lifecycle, tags.String())
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, "<DescribeInstancesResponse><reservationSet>%s</reservationSet></DescribeInstancesResponse>", sb.String())
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
```

`internal/spot/spot.go`:

```go
// Package spot injects spot interruptions into the simulated AWS plane. On real
// AWS, EC2 emits an EventBridge event two minutes before it reclaims a spot
// instance, and a rule forwards it to the SQS queue Karpenter watches. No
// emulator models that, so this package does it through the public API only:
// it posts the same event to the queue, waits out the grace period, then calls
// TerminateInstances. Because it uses only AWS calls through the gateway it
// works with any EC2 backend.
package spot

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/awscall"
)

const accountID = "123456789012"

// Event returns the EventBridge body for "EC2 Spot Instance Interruption
// Warning". Karpenter's interruption controller parses version, detail-type,
// source and detail.instance-id.
func Event(region, instanceID string, at time.Time) []byte {
	ev := map[string]any{
		"version":     "0",
		"id":          awsapi.NewRequestID(),
		"detail-type": "EC2 Spot Instance Interruption Warning",
		"source":      "aws.ec2",
		"account":     accountID,
		"time":        at.UTC().Format(time.RFC3339),
		"region":      region,
		"resources":   []string{fmt.Sprintf("arn:aws:ec2:%s:%s:instance/%s", region, accountID, instanceID)},
		"detail":      map[string]string{"instance-id": instanceID, "instance-action": "terminate"},
	}
	b, _ := json.Marshal(ev) // a map of strings and slices cannot fail to marshal
	return b
}

// Injector talks to the aws-gateway like any AWS client.
type Injector struct {
	Endpoint  string // gateway URL
	Region    string
	QueueName string
	Access    string
	Secret    string
	// AnyMarket skips the check that the instance is a spot instance. It is for
	// backends whose DescribeInstances does not report instanceLifecycle.
	AnyMarket bool
	Client    *http.Client
	// Sleep waits out the grace period; nil means a context-aware time.Sleep.
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
}

func (in *Injector) client() *http.Client {
	if in.Client != nil {
		return in.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (in *Injector) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func (in *Injector) send(ctx context.Context, req *http.Request) ([]byte, error) {
	resp, err := in.client().Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, b)
	}
	return b, nil
}

func (in *Injector) query(ctx context.Context, action string, params ...string) ([]byte, error) {
	req, _, err := awscall.BuildQuery(in.Endpoint, "ec2", in.Region, action, "2016-11-15", params, in.Access, in.Secret, in.now())
	if err != nil {
		return nil, err
	}
	b, err := in.send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}
	return b, nil
}

func (in *Injector) json(ctx context.Context, target, body string) ([]byte, error) {
	req, _, err := awscall.BuildJSON(in.Endpoint, "sqs", in.Region, target, "1.0", body, in.Access, in.Secret, in.now())
	if err != nil {
		return nil, err
	}
	b, err := in.send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", target, err)
	}
	return b, nil
}

// Check confirms the instance exists, is running, and (unless AnyMarket) is a spot instance.
func (in *Injector) Check(ctx context.Context, instanceID string) error {
	b, err := in.query(ctx, "DescribeInstances", "InstanceId.1="+instanceID)
	if err != nil {
		return err
	}
	var out struct {
		Items []struct {
			State     string `xml:"instanceState>name"`
			Lifecycle string `xml:"instanceLifecycle"`
		} `xml:"reservationSet>item>instancesSet>item"`
	}
	if err := xml.Unmarshal(b, &out); err != nil || len(out.Items) != 1 {
		return fmt.Errorf("DescribeInstances did not return exactly one instance for %s", instanceID)
	}
	it := out.Items[0]
	if it.State != "running" {
		return fmt.Errorf("instance %s is %s; only running instances can be interrupted", instanceID, it.State)
	}
	if !in.AnyMarket && it.Lifecycle != "spot" {
		return fmt.Errorf("instance %s is not a spot instance (lifecycle %q); use AnyMarket if this backend omits instanceLifecycle", instanceID, it.Lifecycle)
	}
	return nil
}

// Warn posts the interruption warning to the queue.
func (in *Injector) Warn(ctx context.Context, instanceID string) error {
	b, err := in.json(ctx, "AmazonSQS.GetQueueUrl", mustJSON(map[string]string{"QueueName": in.QueueName}))
	if err != nil {
		return err
	}
	var q struct {
		QueueURL string `json:"QueueUrl"`
	}
	if err := json.Unmarshal(b, &q); err != nil || q.QueueURL == "" {
		return fmt.Errorf("GetQueueUrl returned no QueueUrl: %s", b)
	}
	_, err = in.json(ctx, "AmazonSQS.SendMessage", mustJSON(map[string]string{
		"QueueUrl": q.QueueURL, "MessageBody": string(Event(in.Region, instanceID, in.now())),
	}))
	return err
}

// Reclaim terminates the instance, as AWS does when the grace period ends.
func (in *Injector) Reclaim(ctx context.Context, instanceID string) error {
	_, err := in.query(ctx, "TerminateInstances", "InstanceId.1="+instanceID)
	return err
}

// Interrupt runs the whole sequence: check, warn, wait grace, reclaim. If the
// warning cannot be delivered, the instance is left alone.
func (in *Injector) Interrupt(ctx context.Context, instanceID string, grace time.Duration) error {
	if err := in.Check(ctx, instanceID); err != nil {
		return err
	}
	if err := in.Warn(ctx, instanceID); err != nil {
		return fmt.Errorf("deliver warning: %w", err)
	}
	sleep := in.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-time.After(d):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if err := sleep(ctx, grace); err != nil {
		return err
	}
	return in.Reclaim(ctx, instanceID)
}

func mustJSON(m map[string]string) string {
	b, _ := json.Marshal(m)
	return strings.TrimSpace(string(b))
}
```

`internal/simctl/simctl.go`:

```go
// Package simctl is the command-line client for spot interruptions and the
// egress probe. It ships as a container image so tests can run it on an
// isolated Docker network that publishes no ports to the host.
package simctl

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tazzledazzle/meridian/internal/catalog"
	"github.com/tazzledazzle/meridian/internal/spot"
)

const usage = "usage: simctl [-gateway URL] [-queue NAME] interrupt [-grace 2m] [-any-market] INSTANCE_ID | probe URL"

// Run executes one simctl invocation and returns the process exit code.
// getenv supplies AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY.
func Run(ctx context.Context, args []string, getenv func(string) string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("simctl", flag.ContinueOnError)
	fs.SetOutput(errOut)
	gateway := fs.String("gateway", "http://aws-gateway:4566", "aws-gateway URL")
	queue := fs.String("queue", "meridian-g0-interruption", "interruption queue name")
	timeout := fs.Duration("timeout", 5*time.Second, "probe timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(errOut, usage)
		return 2
	}
	switch rest[0] {
	case "interrupt":
		sub := flag.NewFlagSet("interrupt", flag.ContinueOnError)
		sub.SetOutput(errOut)
		grace := sub.Duration("grace", 2*time.Minute, "time between the warning and termination")
		anyMarket := sub.Bool("any-market", false, "skip the spot-lifecycle check (for backends that omit instanceLifecycle)")
		if err := sub.Parse(rest[1:]); err != nil || sub.NArg() != 1 {
			fmt.Fprintln(errOut, usage)
			return 2
		}
		access, secret := getenv("AWS_ACCESS_KEY_ID"), getenv("AWS_SECRET_ACCESS_KEY")
		if access == "" || secret == "" {
			fmt.Fprintln(errOut, "AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required")
			return 2
		}
		in := &spot.Injector{Endpoint: *gateway, Region: catalog.Region, QueueName: *queue, Access: access, Secret: secret, AnyMarket: *anyMarket}
		id := sub.Arg(0)
		if err := in.Interrupt(ctx, id, *grace); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		fmt.Fprintf(out, "interrupted %s\n", id)
		return 0
	case "probe":
		// Exit 0 means the URL answered, 1 means it did not. The gate runs this
		// against a public AWS endpoint and requires 1.
		if len(rest) != 2 {
			fmt.Fprintln(errOut, usage)
			return 2
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rest[1], nil)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 2
		}
		resp, err := (&http.Client{Timeout: *timeout}).Do(req)
		if err != nil {
			fmt.Fprintf(out, "unreachable: %v\n", err)
			return 1
		}
		resp.Body.Close()
		fmt.Fprintf(out, "reachable: HTTP %d\n", resp.StatusCode)
		return 0
	default:
		fmt.Fprintf(errOut, "unknown command %q\n%s\n", rest[0], usage)
		return 2
	}
}
```

`cmd/simctl/main.go`:

```go
// Command simctl injects spot interruptions through the gateway and probes network reachability.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/tazzledazzle/meridian/internal/simctl"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := simctl.Run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `gofmt -l internal cmd && go vet ./internal/catalog/ ./internal/fakeec2/ ./internal/spot/ ./internal/simctl/ ./cmd/simctl/ && go test -race ./internal/catalog/ ./internal/fakeec2/ ./internal/spot/ ./internal/simctl/ -v`
Expected: no gofmt output; all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/catalog internal/fakeec2 internal/spot internal/simctl cmd/simctl
git commit -m "feat: add the spot interruption injector, simctl and a fake EC2 for client tests"
```

### Task 10: `node-registrar` (instances become Nodes)

**Why:** nothing boots in the simulator, so nothing registers a Node when Karpenter's `CreateFleet` succeeds. `node-registrar` plays the kubelet's part: it watches EC2 (Floci or `ec2-sim`, through the gateway like any AWS client) and creates a KWOK-backed Node for each running instance, then deletes it when the instance terminates. Karpenter matches the Node to its NodeClaim by `providerID`.

**Files:**
- Create: `internal/noderegistrar/types.go`, `kwok.go`, `reconcile.go`, `fleet.go`, `kube.go`
- Create: `cmd/node-registrar/main.go`
- Test: `internal/noderegistrar/kwok_test.go`, `reconcile_test.go`, `kube_test.go`, `fleet_test.go`

**Interfaces:**
- Consumes: `catalog.Lookup`, `catalog.Region` (Task 9); `awscall.BuildQuery` (Task 6); `fakeec2.New` (Task 9; tests only).
- Produces: `noderegistrar.Instance`, `Node`, `Taint`; interfaces `Fleet`, `Cluster`, `Backend`; `KWOK{}`; `Reconciler{Fleet, Cluster, Backends, Default, Logf}` with `Reconcile(ctx) (Result, error)`; `EC2Fleet`; `Kube` and `InCluster()`; label keys `ManagedByKey`, `InstanceIDKey`, `BackendKey` (`meridian.io/node-backend`, also the EC2 tag that selects a backend per instance); the `node-registrar` binary.

**Acceptance Criteria:**
- A `running` instance gets exactly one Node, with `providerID` `aws:///<az>/<instance-id>`, instance-type and zone labels, capacity and allocatable (ENI-limited pod count), and both taints (`karpenter.sh/unregistered:NoExecute` and KWOK's `kwok.x-k8s.io/node=fake:NoSchedule`).
- A `pending` instance gets no Node. A `shutting-down` or `terminated` instance, or one that disappears from the listing, loses its Node.
- If EC2 cannot be listed, no Node is deleted.
- The registrar lists and deletes only Nodes carrying its own label, so it can never touch the control-plane Node.
- An instance that asks for a backend this build does not have (`meridian.io/node-backend=real`) is reported as skipped and gets no Node; it never silently gets a KWOK node.
- No Kubernetes client library is needed: the REST client uses only the standard library.

**Scope note (surfaced decision):** the `real` backend (a k3d agent container that runs real pods) is not part of G0. G0 criteria need only simulated capacity. `real` arrives with managed nodegroups in M0; the `Backend` interface and the tag-based selection are in place now so M0 adds a backend and changes no reconcile logic.

- [ ] **Step 1: Write the failing tests**

`internal/noderegistrar/kwok_test.go`:

```go
package noderegistrar

import "testing"

func runningInstance() Instance {
	return Instance{ID: "i-0123", State: "running", Type: "m5.large", AZ: "us-west-2a",
		PrivateDNS: "ip-10-0-1-10.us-west-2.compute.internal", PrivateIP: "10.0.1.10"}
}

func TestKWOKNodeMatchesTheInstance(t *testing.T) {
	n, err := KWOK{}.Node(runningInstance())
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "ip-10-0-1-10.us-west-2.compute.internal" || n.ProviderID != "aws:///us-west-2a/i-0123" {
		t.Fatalf("name/providerID = %s / %s", n.Name, n.ProviderID)
	}
	want := map[string]string{
		"node.kubernetes.io/instance-type": "m5.large", "topology.kubernetes.io/zone": "us-west-2a",
		"kubernetes.io/arch": "amd64", ManagedByKey: ManagedByValue, InstanceIDKey: "i-0123", "type": "kwok",
	}
	for k, v := range want {
		if n.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, n.Labels[k], v)
		}
	}
	if n.Annotations["kwok.x-k8s.io/node"] != "fake" {
		t.Error("the KWOK controller only manages nodes annotated kwok.x-k8s.io/node=fake")
	}
	// m5.large: 3 ENIs x (10 - 1) + 2 = 29 pods.
	if n.Allocatable["pods"] != "29" || n.Allocatable["cpu"] != "2" || n.Allocatable["memory"] != "8192Mi" || n.Allocatable["ephemeral-storage"] != "20Gi" {
		t.Errorf("allocatable = %v", n.Allocatable)
	}
}

func TestKWOKNodeCarriesBothTaints(t *testing.T) {
	n, _ := KWOK{}.Node(runningInstance())
	has := func(key, effect string) bool {
		for _, tn := range n.Taints {
			if tn.Key == key && tn.Effect == effect {
				return true
			}
		}
		return false
	}
	if !has("karpenter.sh/unregistered", "NoExecute") {
		t.Error("missing karpenter.sh/unregistered:NoExecute (what a Karpenter-bootstrapped kubelet registers with)")
	}
	if !has("kwok.x-k8s.io/node", "NoSchedule") {
		t.Error("missing KWOK's conventional taint, so real workloads could land on a simulated node")
	}
}

func TestKWOKRejectsIncompleteOrUnknownInstances(t *testing.T) {
	bad := runningInstance()
	bad.Type = "x9.huge"
	if _, err := (KWOK{}).Node(bad); err == nil {
		t.Error("unknown type must fail")
	}
	bad = runningInstance()
	bad.PrivateDNS, bad.PrivateIP = "", ""
	if _, err := (KWOK{}).Node(bad); err == nil {
		t.Error("an instance with neither a DNS name nor an IP must fail")
	}
}

func TestArm64MapsToTheKubernetesArchLabel(t *testing.T) {
	i := runningInstance()
	i.Type = "m6g.large"
	n, _ := KWOK{}.Node(i)
	if n.Labels["kubernetes.io/arch"] != "arm64" {
		t.Fatalf("arch = %s", n.Labels["kubernetes.io/arch"])
	}
}

func TestNodeNameIsDerivedFromThePrivateIPWhenTheBackendOmitsDNS(t *testing.T) {
	i := runningInstance()
	i.PrivateDNS = ""
	n, err := KWOK{}.Node(i)
	if err != nil || n.Name != "ip-10-0-1-10.us-west-2.compute.internal" || n.Labels["kubernetes.io/hostname"] != n.Name {
		t.Fatalf("name = %q err = %v", n.Name, err)
	}
}
```

`internal/noderegistrar/reconcile_test.go`:

```go
package noderegistrar

import (
	"context"
	"errors"
	"sort"
	"testing"
)

type fakeFleet struct {
	instances []Instance
	err       error
}

func (f *fakeFleet) Instances(context.Context) ([]Instance, error) { return f.instances, f.err }

type fakeCluster struct {
	nodes map[string]Node // by name
	err   error
}

func newFakeCluster() *fakeCluster { return &fakeCluster{nodes: map[string]Node{}} }

func (c *fakeCluster) ManagedNodes(context.Context) ([]Node, error) {
	var out []Node
	for _, n := range c.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, c.err
}
func (c *fakeCluster) Create(_ context.Context, n Node) error { c.nodes[n.Name] = n; return nil }
func (c *fakeCluster) Delete(_ context.Context, name string) error {
	delete(c.nodes, name)
	return nil
}

func inst(id, state string, tags map[string]string) Instance {
	return Instance{ID: id, State: state, Type: "m5.large", AZ: "us-west-2a",
		PrivateDNS: "ip-" + id + ".internal", Tags: tags}
}

func newRec(f *fakeFleet, c *fakeCluster) *Reconciler {
	return &Reconciler{Fleet: f, Cluster: c, Backends: map[string]Backend{"kwok": KWOK{}}, Default: "kwok"}
}

func TestRunningInstanceGetsExactlyOneNode(t *testing.T) {
	f := &fakeFleet{instances: []Instance{inst("i-1", "running", nil), inst("i-2", "pending", nil)}}
	c := newFakeCluster()
	r := newRec(f, c)
	res, err := r.Reconcile(context.Background())
	if err != nil || len(res.Created) != 1 || len(c.nodes) != 1 {
		t.Fatalf("res=%+v err=%v nodes=%d (a pending instance must not get a node)", res, err, len(c.nodes))
	}
	res, _ = r.Reconcile(context.Background())
	if len(res.Created) != 0 || len(c.nodes) != 1 {
		t.Fatalf("second pass must be a no-op: %+v", res)
	}
	f.instances[1].State = "running"
	res, _ = r.Reconcile(context.Background())
	if len(res.Created) != 1 || len(c.nodes) != 2 {
		t.Fatalf("pending -> running should add a node: %+v", res)
	}
}

func TestNodeIsDeletedWhenTheInstanceTerminatesOrDisappears(t *testing.T) {
	f := &fakeFleet{instances: []Instance{inst("i-1", "running", nil), inst("i-2", "running", nil), inst("i-3", "running", nil)}}
	c := newFakeCluster()
	r := newRec(f, c)
	if _, err := r.Reconcile(context.Background()); err != nil || len(c.nodes) != 3 {
		t.Fatalf("setup: %v %d", err, len(c.nodes))
	}
	f.instances[0].State = "shutting-down"
	f.instances[1].State = "terminated"
	f.instances = f.instances[:2] // i-3 vanished from the listing (for example ec2-sim restarted)
	res, err := r.Reconcile(context.Background())
	if err != nil || len(res.Deleted) != 3 || len(c.nodes) != 0 {
		t.Fatalf("res=%+v err=%v nodes=%d", res, err, len(c.nodes))
	}
}

func TestAFailedFleetListingNeverDeletesNodes(t *testing.T) {
	f := &fakeFleet{instances: []Instance{inst("i-1", "running", nil)}}
	c := newFakeCluster()
	r := newRec(f, c)
	_, _ = r.Reconcile(context.Background())
	f.instances, f.err = nil, errors.New("ec2 unreachable")
	if _, err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if len(c.nodes) != 1 {
		t.Fatal("an EC2 outage must not delete Nodes")
	}
}

func TestBackendTagSelectsTheBackendAndNeverFallsBack(t *testing.T) {
	f := &fakeFleet{instances: []Instance{
		inst("i-kwok", "running", map[string]string{BackendKey: "kwok"}),
		inst("i-real", "running", map[string]string{BackendKey: "real"}),
		inst("i-none", "running", nil),
	}}
	c := newFakeCluster()
	res, err := newRec(f, c).Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 2 {
		t.Fatalf("created = %v (the untagged instance uses the default backend)", res.Created)
	}
	if _, ok := res.Skipped["i-real"]; !ok || len(c.nodes) != 2 {
		t.Fatalf("a request for the unavailable 'real' backend must be reported, not turned into a KWOK node: %+v", res.Skipped)
	}
}

func TestUnmanagedNodesAreNeverTouched(t *testing.T) {
	// The cluster interface only ever lists managed nodes, so the control-plane node is invisible here.
	c := newFakeCluster()
	f := &fakeFleet{}
	res, err := newRec(f, c).Reconcile(context.Background())
	if err != nil || len(res.Deleted) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
```

`internal/noderegistrar/kube_test.go`:

```go
package noderegistrar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI records what the registrar sends, like a tiny kube-apiserver.
type fakeAPI struct {
	auth    string
	created []kNode
	deleted []string
	status  map[string]int // method -> forced status
	query   string
}

func (a *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.auth = r.Header.Get("Authorization")
	if code, ok := a.status[r.Method]; ok {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"message":"forced"}`))
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
		a.query = r.URL.Query().Get("labelSelector")
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"n1","labels":{"meridian.io/instance-id":"i-1","meridian.io/managed-by":"node-registrar"}},"spec":{"providerID":"aws:///us-west-2a/i-1"}}]}`))
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/nodes":
		var n kNode
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &n)
		a.created = append(a.created, n)
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/nodes/"):
		a.deleted = append(a.deleted, strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestKubeClientSendsTheNodeAsKubernetesJSON(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	k := &Kube{Base: srv.URL, Token: "tok"}
	n, _ := KWOK{}.Node(runningInstance())
	if err := k.Create(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if api.auth != "Bearer tok" {
		t.Fatalf("auth = %q", api.auth)
	}
	got := api.created[0]
	if got.Kind != "Node" || got.APIVersion != "v1" || got.Spec.ProviderID != n.ProviderID ||
		len(got.Spec.Taints) != 2 || got.Status.Allocatable["pods"] != "29" ||
		got.Metadata.Annotations["kwok.x-k8s.io/node"] != "fake" {
		t.Fatalf("sent %+v", got)
	}
}

func TestKubeListsOnlyManagedNodesByLabelSelector(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	nodes, err := (&Kube{Base: srv.URL, Token: "t"}).ManagedNodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].Labels[InstanceIDKey] != "i-1" {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	if api.query != "meridian.io/managed-by=node-registrar" {
		t.Fatalf("selector = %q; without it the registrar could delete nodes it does not own", api.query)
	}
}

func TestKubeTreatsConflictAndNotFoundAsSuccessButSurfacesOtherErrors(t *testing.T) {
	api := &fakeAPI{status: map[string]int{http.MethodPost: 409, http.MethodDelete: 404}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	k := &Kube{Base: srv.URL, Token: "t"}
	n, _ := KWOK{}.Node(runningInstance())
	if err := k.Create(context.Background(), n); err != nil {
		t.Errorf("409 on create should be success: %v", err)
	}
	if err := k.Delete(context.Background(), "n1"); err != nil {
		t.Errorf("404 on delete should be success: %v", err)
	}
	api.status = map[string]int{http.MethodPost: 403, http.MethodDelete: 500, http.MethodGet: 401}
	if k.Create(context.Background(), n) == nil || k.Delete(context.Background(), "n1") == nil {
		t.Error("403/500 must be errors")
	}
	if _, err := k.ManagedNodes(context.Background()); err == nil {
		t.Error("401 on list must be an error")
	}
}

func TestInClusterFailsOutsideACluster(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if _, err := InCluster(); err == nil {
		t.Fatal("expected an error outside a cluster")
	}
}
```

`internal/noderegistrar/fleet_test.go`:

```go
package noderegistrar

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/fakeec2"
)

// TestEC2FleetReadsInstancesFromDescribeInstances runs the real EC2 client
// against a fake that answers in the shape AWS documents, so a wrong element
// name fails here. The S0 probe checks the same shape against the real backend.
func TestEC2FleetReadsInstancesFromDescribeInstances(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store := fakeec2.New(func() time.Time { return now }, 3*time.Second)
	tags := map[string]string{"kubernetes.io/cluster/meridian-g0": "owned", BackendKey: "kwok"}
	own := store.Launch("m5.large", "us-west-2b", "spot", tags)
	store.Launch("c5.large", "us-west-2a", "", nil) // not ours
	now = now.Add(5 * time.Second)

	srv := httptest.NewServer(store)
	defer srv.Close()
	f := &EC2Fleet{Endpoint: srv.URL, Region: "us-west-2", Access: "AK", Secret: "SK", ClusterTag: "kubernetes.io/cluster/meridian-g0"}
	got, err := f.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d instances, want only the one tagged for this cluster: %+v", len(got), got)
	}
	g := got[0]
	if g.ID != own || g.State != "running" || g.Type != "m5.large" || g.AZ != "us-west-2b" ||
		g.PrivateDNS == "" || g.Tags[BackendKey] != "kwok" {
		t.Fatalf("instance = %+v", g)
	}
	// And the node it produces is well formed.
	if _, err := (KWOK{}).Node(g); err != nil {
		t.Fatal(err)
	}
}

func TestEC2FleetReportsHTTPFailures(t *testing.T) {
	srv := httptest.NewServer(fakeec2.New(time.Now, 0))
	srv.Close() // nothing is listening any more
	f := &EC2Fleet{Endpoint: srv.URL, Region: "us-west-2", Access: "AK", Secret: "SK", ClusterTag: "x"}
	if _, err := f.Instances(context.Background()); err == nil {
		t.Fatal("expected a connection error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/noderegistrar/ -v`
Expected: FAIL with `undefined: KWOK` and similar.

- [ ] **Step 3: Write the types and the KWOK backend**

`internal/noderegistrar/types.go`:

```go
// Package noderegistrar turns simulated EC2 instances into Kubernetes Nodes.
// On real AWS the kubelet on a new instance registers itself; here nothing
// boots, so this controller plays the kubelet's part: when ec2-sim reports an
// instance running it creates the Node, and when the instance is gone it
// deletes the Node. It talks to EC2 through the gateway and to Kubernetes
// through its REST API, so it needs no client library.
package noderegistrar

import "context"

// Label and annotation keys the registrar owns.
const (
	ManagedByKey   = "meridian.io/managed-by"
	ManagedByValue = "node-registrar"
	InstanceIDKey  = "meridian.io/instance-id"
	BackendKey     = "meridian.io/node-backend" // also the EC2 tag that picks the backend per instance
)

// Instance is what the registrar needs to know about one EC2 instance.
type Instance struct {
	ID         string
	State      string // pending | running | shutting-down | terminated
	Type       string
	AZ         string
	PrivateDNS string
	PrivateIP  string
	Tags       map[string]string
}

// ProviderID is the Node.spec.providerID Karpenter matches NodeClaims against.
func (i Instance) ProviderID() string { return "aws:///" + i.AZ + "/" + i.ID }

// Taint mirrors a Kubernetes taint.
type Taint struct {
	Key, Value, Effect string
}

// Node is the registrar's view of a Kubernetes Node.
type Node struct {
	Name        string
	ProviderID  string
	Labels      map[string]string
	Annotations map[string]string
	Taints      []Taint
	Capacity    map[string]string
	Allocatable map[string]string
}

// Fleet lists the instances that belong to this cluster.
type Fleet interface {
	Instances(ctx context.Context) ([]Instance, error)
}

// Cluster reads and writes the Nodes the registrar manages. Nodes it did not
// create (the k3d server node, anything else) are never listed or deleted.
type Cluster interface {
	ManagedNodes(ctx context.Context) ([]Node, error)
	Create(ctx context.Context, n Node) error
	Delete(ctx context.Context, name string) error
}

// Backend builds the Node for an instance.
type Backend interface {
	Node(i Instance) (Node, error)
}
```

`internal/noderegistrar/kwok.go`:

```go
package noderegistrar

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

// KWOK builds simulated nodes that the KWOK controller keeps "Ready". They
// simulate capacity, not execution: no containers run on them.
type KWOK struct{}

// Taints every KWOK node carries. The first is what a kubelet started by
// Karpenter's user data registers with; Karpenter removes it once it has
// matched the Node to its NodeClaim. The second is KWOK's conventional taint,
// so a pod only lands here if it explicitly tolerates simulated nodes.
var kwokTaints = []Taint{
	{Key: "karpenter.sh/unregistered", Value: "", Effect: "NoExecute"},
	{Key: "kwok.x-k8s.io/node", Value: "fake", Effect: "NoSchedule"},
}

// Node implements Backend.
func (KWOK) Node(i Instance) (Node, error) {
	t, ok := catalog.Lookup(i.Type)
	if !ok {
		return Node{}, fmt.Errorf("instance %s has unknown type %q", i.ID, i.Type)
	}
	// Not every EC2 backend fills in privateDnsName; derive the name AWS would
	// give from the private IP so the Node name is stable either way.
	name := i.PrivateDNS
	if name == "" && i.PrivateIP != "" {
		name = "ip-" + strings.ReplaceAll(i.PrivateIP, ".", "-") + "." + catalog.Region + ".compute.internal"
	}
	if name == "" || i.AZ == "" {
		return Node{}, fmt.Errorf("instance %s has no private DNS name, private IP or zone yet", i.ID)
	}
	// The ENI-limited pod count EKS and Karpenter both use: ENIs x (IPs per ENI - 1) + 2.
	pods := t.MaxENIs*(t.IPv4PerENI-1) + 2
	res := map[string]string{
		"cpu":               strconv.Itoa(t.VCPU),
		"memory":            strconv.Itoa(t.MemoryMiB) + "Mi",
		"pods":              strconv.Itoa(pods),
		"ephemeral-storage": "20Gi", // matches the 20 GiB root volume DescribeImages reports
	}
	return Node{
		Name:       name,
		ProviderID: i.ProviderID(),
		Labels: map[string]string{
			"kubernetes.io/hostname":           name,
			"kubernetes.io/os":                 "linux",
			"kubernetes.io/arch":               goArch(t.Arch),
			"node.kubernetes.io/instance-type": t.Name,
			"topology.kubernetes.io/zone":      i.AZ,
			"topology.kubernetes.io/region":    catalog.Region,
			"type":                             "kwok", // the label KWOK's own examples select on
			BackendKey:                         "kwok",
			ManagedByKey:                       ManagedByValue,
			InstanceIDKey:                      i.ID,
		},
		Annotations: map[string]string{
			"kwok.x-k8s.io/node":           "fake", // opts the node in to the KWOK controller
			"node.alpha.kubernetes.io/ttl": "0",
		},
		Taints:      append([]Taint(nil), kwokTaints...),
		Capacity:    res,
		Allocatable: res,
	}, nil
}

// goArch maps EC2 architecture names to the values Kubernetes uses.
func goArch(ec2Arch string) string {
	if ec2Arch == "arm64" {
		return "arm64"
	}
	return "amd64"
}
```

- [ ] **Step 4: Write the reconcile loop**

`internal/noderegistrar/reconcile.go`:

```go
package noderegistrar

import (
	"context"
	"fmt"
	"sort"
)

// Result summarizes one reconcile pass.
type Result struct {
	Created []string          // node names created
	Deleted []string          // node names deleted
	Skipped map[string]string // instance ID -> why no node was made
}

// Reconciler makes the set of managed Nodes match the set of running instances.
type Reconciler struct {
	Fleet    Fleet
	Cluster  Cluster
	Backends map[string]Backend // keyed by the meridian.io/node-backend tag value
	Default  string             // backend for instances without the tag
	Logf     func(format string, args ...any)
}

func (r *Reconciler) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Reconcile runs one pass. If the fleet cannot be listed it changes nothing:
// deleting Nodes because EC2 was briefly unreachable would be much worse than
// leaving them for one more interval.
func (r *Reconciler) Reconcile(ctx context.Context) (Result, error) {
	res := Result{Skipped: map[string]string{}}
	instances, err := r.Fleet.Instances(ctx)
	if err != nil {
		return res, fmt.Errorf("list instances: %w", err)
	}
	nodes, err := r.Cluster.ManagedNodes(ctx)
	if err != nil {
		return res, fmt.Errorf("list nodes: %w", err)
	}
	have := map[string]Node{} // by instance ID
	for _, n := range nodes {
		have[n.Labels[InstanceIDKey]] = n
	}

	seen := map[string]bool{}
	for _, in := range instances {
		seen[in.ID] = true
		node, exists := have[in.ID]
		switch in.State {
		case "running":
			if exists {
				continue
			}
			name := in.Tags[BackendKey]
			if name == "" {
				name = r.Default
			}
			b, ok := r.Backends[name]
			if !ok {
				// Never fall back to another backend silently: a pool that asked
				// for real nodes must not quietly get simulated ones.
				res.Skipped[in.ID] = fmt.Sprintf("node backend %q is not available in this build", name)
				r.logf("skip %s: %s", in.ID, res.Skipped[in.ID])
				continue
			}
			n, err := b.Node(in)
			if err != nil {
				res.Skipped[in.ID] = err.Error()
				r.logf("skip %s: %v", in.ID, err)
				continue
			}
			if err := r.Cluster.Create(ctx, n); err != nil {
				return res, fmt.Errorf("create node %s: %w", n.Name, err)
			}
			res.Created = append(res.Created, n.Name)
			r.logf("created node %s for %s", n.Name, in.ID)
		case "shutting-down", "terminated":
			if exists {
				if err := r.Cluster.Delete(ctx, node.Name); err != nil {
					return res, fmt.Errorf("delete node %s: %w", node.Name, err)
				}
				res.Deleted = append(res.Deleted, node.Name)
				r.logf("deleted node %s (instance %s %s)", node.Name, in.ID, in.State)
			}
		}
		// pending: the instance is still booting; a Node appears when it runs.
	}

	// Nodes whose instance is no longer listed at all (for example ec2-sim restarted).
	for id, n := range have {
		if seen[id] {
			continue
		}
		if err := r.Cluster.Delete(ctx, n.Name); err != nil {
			return res, fmt.Errorf("delete orphan node %s: %w", n.Name, err)
		}
		res.Deleted = append(res.Deleted, n.Name)
		r.logf("deleted orphan node %s (instance %s is gone)", n.Name, id)
	}
	sort.Strings(res.Created)
	sort.Strings(res.Deleted)
	return res, nil
}
```

- [ ] **Step 5: Write the EC2 and Kubernetes clients**

`internal/noderegistrar/fleet.go`:

```go
package noderegistrar

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tazzledazzle/meridian/internal/awscall"
)

// EC2Fleet lists instances with DescribeInstances through the aws-gateway,
// signed with the platform credentials, exactly as any other AWS client would.
type EC2Fleet struct {
	Endpoint string // gateway URL
	Region   string
	Access   string
	Secret   string
	// ClusterTag selects the instances that belong to this cluster. Karpenter
	// tags every instance it launches with kubernetes.io/cluster/<name>=owned.
	ClusterTag string // tag key, e.g. kubernetes.io/cluster/meridian-g0
	Client     *http.Client
}

type describeInstancesOut struct {
	Instances []struct {
		ID    string `xml:"instanceId"`
		State string `xml:"instanceState>name"`
		Type  string `xml:"instanceType"`
		AZ    string `xml:"placement>availabilityZone"`
		DNS   string `xml:"privateDnsName"`
		IP    string `xml:"privateIpAddress"`
		Tags  []struct {
			Key   string `xml:"key"`
			Value string `xml:"value"`
		} `xml:"tagSet>item"`
	} `xml:"reservationSet>item>instancesSet>item"`
}

// Instances implements Fleet.
func (f *EC2Fleet) Instances(ctx context.Context) ([]Instance, error) {
	params := []string{"Filter.1.Name=tag-key", "Filter.1.Value.1=" + f.ClusterTag}
	req, _, err := awscall.BuildQuery(f.Endpoint, "ec2", f.Region, "DescribeInstances", "2016-11-15", params, f.Access, f.Secret, time.Now())
	if err != nil {
		return nil, err
	}
	c := f.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DescribeInstances: HTTP %d: %.200s", resp.StatusCode, body)
	}
	var out describeInstancesOut
	if err := xml.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("DescribeInstances: bad XML: %w", err)
	}
	res := make([]Instance, 0, len(out.Instances))
	for _, i := range out.Instances {
		tags := map[string]string{}
		for _, t := range i.Tags {
			tags[t.Key] = t.Value
		}
		res = append(res, Instance{ID: i.ID, State: i.State, Type: i.Type, AZ: i.AZ, PrivateDNS: i.DNS, PrivateIP: i.IP, Tags: tags})
	}
	return res, nil
}
```

`internal/noderegistrar/kube.go`:

```go
package noderegistrar

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Kube is a minimal Kubernetes REST client for Nodes. It uses the pod's
// service account, so the registrar needs no client library.
type Kube struct {
	Base   string // https://10.43.0.1:443
	Token  string
	Client *http.Client
}

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// InCluster builds a client from the pod's service account.
func InCluster() (*Kube, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not running in a cluster (KUBERNETES_SERVICE_HOST/PORT unset)")
	}
	tok, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("no certificates in %s/ca.crt", saDir)
	}
	return &Kube{
		Base:  "https://" + host + ":" + port,
		Token: string(bytes.TrimSpace(tok)),
		Client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		},
	}, nil
}

func (k *Kube) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.Base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := k.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, b, nil
}

// Wire shapes: only the fields the registrar sets or reads.
type kTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

type kNode struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Metadata   struct {
		Name        string            `json:"name"`
		Labels      map[string]string `json:"labels,omitempty"`
		Annotations map[string]string `json:"annotations,omitempty"`
	} `json:"metadata"`
	Spec struct {
		ProviderID string   `json:"providerID,omitempty"`
		Taints     []kTaint `json:"taints,omitempty"`
	} `json:"spec"`
	Status struct {
		Capacity    map[string]string `json:"capacity,omitempty"`
		Allocatable map[string]string `json:"allocatable,omitempty"`
	} `json:"status"`
}

func toK(n Node) kNode {
	var k kNode
	k.APIVersion, k.Kind = "v1", "Node"
	k.Metadata.Name, k.Metadata.Labels, k.Metadata.Annotations = n.Name, n.Labels, n.Annotations
	k.Spec.ProviderID = n.ProviderID
	for _, t := range n.Taints {
		k.Spec.Taints = append(k.Spec.Taints, kTaint{t.Key, t.Value, t.Effect})
	}
	k.Status.Capacity, k.Status.Allocatable = n.Capacity, n.Allocatable
	return k
}

// ManagedNodes lists only Nodes carrying the registrar's label.
func (k *Kube) ManagedNodes(ctx context.Context) ([]Node, error) {
	sel := url.QueryEscape(ManagedByKey + "=" + ManagedByValue)
	code, body, err := k.do(ctx, http.MethodGet, "/api/v1/nodes?labelSelector="+sel, nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("list nodes: HTTP %d: %.200s", code, body)
	}
	var list struct {
		Items []kNode `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(list.Items))
	for _, it := range list.Items {
		out = append(out, Node{Name: it.Metadata.Name, ProviderID: it.Spec.ProviderID,
			Labels: it.Metadata.Labels, Annotations: it.Metadata.Annotations})
	}
	return out, nil
}

// Create makes the Node. A 409 means it already exists, which is success.
func (k *Kube) Create(ctx context.Context, n Node) error {
	code, body, err := k.do(ctx, http.MethodPost, "/api/v1/nodes", toK(n))
	if err != nil {
		return err
	}
	if code == http.StatusConflict {
		return nil
	}
	if code != http.StatusCreated && code != http.StatusOK {
		return fmt.Errorf("create node: HTTP %d: %.200s", code, body)
	}
	return nil
}

// Delete removes the Node. A 404 means it is already gone, which is success.
func (k *Kube) Delete(ctx context.Context, name string) error {
	code, body, err := k.do(ctx, http.MethodDelete, "/api/v1/nodes/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	if code != http.StatusOK && code != http.StatusAccepted && code != http.StatusNotFound {
		return fmt.Errorf("delete node: HTTP %d: %.200s", code, body)
	}
	return nil
}
```

- [ ] **Step 6: Write the binary**

`cmd/node-registrar/main.go`:

```go
// Command node-registrar creates a Kubernetes Node for every running simulated
// EC2 instance and deletes it when the instance is gone. It runs inside the
// cluster and uses the pod's service account.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tazzledazzle/meridian/internal/catalog"
	"github.com/tazzledazzle/meridian/internal/noderegistrar"
)

func main() {
	endpoint := flag.String("ec2-endpoint", "http://aws-gateway:4566", "aws-gateway URL")
	cluster := flag.String("cluster", "meridian-g0", "cluster name; selects instances tagged kubernetes.io/cluster/<name>")
	interval := flag.Duration("interval", 2*time.Second, "time between reconcile passes")
	def := flag.String("default-backend", "kwok", "backend for instances without a meridian.io/node-backend tag (only kwok is built in G0)")
	flag.Parse()

	access, secret := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
	if access == "" || secret == "" {
		log.Fatal("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required")
	}
	kube, err := noderegistrar.InCluster()
	if err != nil {
		log.Fatal(err)
	}
	r := &noderegistrar.Reconciler{
		Fleet: &noderegistrar.EC2Fleet{Endpoint: *endpoint, Region: catalog.Region, Access: access, Secret: secret,
			ClusterTag: "kubernetes.io/cluster/" + *cluster},
		Cluster:  kube,
		Backends: map[string]noderegistrar.Backend{"kwok": noderegistrar.KWOK{}},
		Default:  *def,
		Logf:     log.Printf,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("node-registrar started: cluster=%s endpoint=%s interval=%s", *cluster, *endpoint, *interval)
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for {
		if _, err := r.Reconcile(ctx); err != nil && ctx.Err() == nil {
			log.Printf("reconcile: %v", err) // transient: the next pass retries
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `gofmt -l internal cmd && go vet ./internal/noderegistrar/ ./cmd/node-registrar/ && go test -race ./internal/noderegistrar/ -v`
Expected: no gofmt output; all tests PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/noderegistrar cmd/node-registrar
git commit -m "feat: add node-registrar (running instances become KWOK Nodes)"
```

### Task 11: [Fallback] EC2 simulator core (state machine, launch templates, fleet)

> **Conditional task.** Execute this only if `docs/s0/decision.md` (Task 8) moved operations to the backend this task builds (`ec2-sim`). If every operation stayed on Floci, skip it and record the skip in `docs/s0/decision.md`.



**Files:**
- Create: `internal/ec2sim/errors.go`, `fixtures.go`, `filters.go`, `store.go`, `templates.go`, `fleet.go`
- Test: `internal/ec2sim/filters_test.go`, `store_test.go`, `fleet_test.go`

**Interfaces:**
- Consumes: `awsapi.Node` (`Nest`, `Get`, `Str`, `List`, `Strings`) from Task 3.
- Produces:
  - `catalog.Region`, `catalog.AZs []AZ{Name, ID}`, `catalog.Types []Type{Name, VCPU, MemoryMiB, Arch, MaxENIs, IPv4PerENI, OnDemand}`, `catalog.Lookup(name) (Type, bool)`, `catalog.HasAZ(name) bool`, `catalog.SpotPrice(Type, az) float64`. `pricing-facade` (Task 13) reads the same data. The `catalog` package already exists (Task 9).
  - `ec2sim.NewStore(Options{Now, PendingFor, ShuttingDownFor}) *Store`; `Launch(LaunchRequest) ([]InstanceView, error)`; `Terminate([]string) ([]Transition, error)`; `Instances() []InstanceView`; `Instance(id) (InstanceView, bool)`; `Tag(resources []string, tags map[string]string) error`; `CreateTemplate(name, imageID string, tags map[string]string) (Template, error)`; `Template(nameOrID)`; `Templates()`; `DeleteTemplate(nameOrID)`; `CreateFleet(FleetRequest) (FleetResult, error)` (each `FleetOverride` may name its own launch template; `FleetRequest.Template` is the default); `MarkUnavailable(type, az)`; `ClearUnavailable()`; `Subnets()`, `SecurityGroups()`, `Images()`, `Now()`.
  - `ec2sim.APIError{Code, Message}` (the handler in Task 12 turns it into an EC2 error response).
  - `ec2sim.ParseFilters`, `Match`, `TagAttr`, `ParseTagSpecs`; constants `DefaultAMI`, `VPCID`, `DiscoveryKey`, `DiscoveryValue`.

**Acceptance Criteria:**
- An instance moves `pending` to `running` after `PendingFor`, and `shutting-down` to `terminated` after `ShuttingDownFor`; state is derived from the clock, never stored.
- Launch errors use real EC2 error codes (`InvalidInstanceType`, `InvalidAMIID.NotFound`, `InvalidSubnetID.NotFound`, `InsufficientInstanceCapacity`).
- `CreateFleet` tries overrides in priority order, reports exhausted pools in `Errors`, and launches from the first pool with capacity.
- A returned view is a copy: mutating it never changes the store.
- All of this works with no network and no dependency outside the standard library plus Task 3's helpers.

- [ ] **Step 1: Write the failing tests**

`internal/ec2sim/filters_test.go`:

```go
package ec2sim

import (
	"net/url"
	"testing"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

func TestParseFiltersAndMatch(t *testing.T) {
	v := url.Values{
		"Filter.1.Name":    {"instance-state-name"},
		"Filter.1.Value.1": {"pending"},
		"Filter.1.Value.2": {"running"},
		"Filter.2.Name":    {"tag:karpenter.sh/nodepool"},
		"Filter.2.Value.1": {"default"},
	}
	fs := ParseFilters(awsapi.Nest(v))
	if len(fs) != 2 || len(fs[0].Values) != 2 {
		t.Fatalf("parsed %+v", fs)
	}
	tags := map[string]string{"karpenter.sh/nodepool": "default"}
	attr := func(state string) func(string) []string {
		return func(name string) []string {
			if name == "instance-state-name" {
				return []string{state}
			}
			vals, _ := TagAttr(tags, name)
			return vals
		}
	}
	if !Match(fs, attr("running")) {
		t.Fatal("running + nodepool=default should match")
	}
	if Match(fs, attr("terminated")) {
		t.Fatal("terminated should not match")
	}
	tags["karpenter.sh/nodepool"] = "other"
	if Match(fs, attr("running")) {
		t.Fatal("a different nodepool tag should not match")
	}
}

func TestWildcardAndTagKey(t *testing.T) {
	if !Match([]Filter{{Name: "tag-key", Values: []string{"karpenter.sh/*"}}},
		func(n string) []string { v, _ := TagAttr(map[string]string{"karpenter.sh/nodepool": "x"}, n); return v }) {
		t.Fatal("wildcard tag-key should match")
	}
}

func TestParseTagSpecsPicksOnlyTheRequestedResourceType(t *testing.T) {
	v := url.Values{
		"TagSpecification.1.ResourceType": {"instance"},
		"TagSpecification.1.Tag.1.Key":    {"a"}, "TagSpecification.1.Tag.1.Value": {"1"},
		"TagSpecification.2.ResourceType": {"fleet"},
		"TagSpecification.2.Tag.1.Key":    {"b"}, "TagSpecification.2.Tag.1.Value": {"2"},
	}
	got := ParseTagSpecs(awsapi.Nest(v), "instance")
	if len(got) != 1 || got["a"] != "1" {
		t.Fatalf("got %v", got)
	}
}
```

`internal/ec2sim/store_test.go`:

```go
package ec2sim

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestStore() (*Store, *clock) {
	c := &clock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	return NewStore(Options{Now: c.now, PendingFor: 3 * time.Second, ShuttingDownFor: 2 * time.Second}), c
}

func launchOne(t *testing.T, s *Store, typ, market string) InstanceView {
	t.Helper()
	got, err := s.Launch(LaunchRequest{Type: typ, AZ: "us-west-2a", Market: market, Count: 1,
		Tags: map[string]string{"karpenter.sh/nodepool": "default"}})
	if err != nil {
		t.Fatal(err)
	}
	return got[0]
}

func TestInstanceMovesPendingRunningShuttingDownTerminated(t *testing.T) {
	s, c := newTestStore()
	i := launchOne(t, s, "m5.large", "spot")
	if i.State != StatePending {
		t.Fatalf("state = %s, want pending", i.State)
	}
	c.advance(3 * time.Second)
	if v, _ := s.Instance(i.ID); v.State != StateRunning {
		t.Fatalf("state = %s, want running", v.State)
	}
	tr, err := s.Terminate([]string{i.ID})
	if err != nil || tr[0].Previous != StateRunning || tr[0].Current != StateShuttingDown {
		t.Fatalf("transition = %+v, err = %v", tr, err)
	}
	c.advance(2 * time.Second)
	if v, _ := s.Instance(i.ID); v.State != StateTerminated {
		t.Fatalf("state = %s, want terminated", v.State)
	}
	if _, err := s.Terminate([]string{i.ID}); err != nil {
		t.Fatalf("terminating twice must succeed: %v", err)
	}
}

func TestLaunchAssignsSubnetFromZoneAndDistinctPrivateIPs(t *testing.T) {
	s, _ := newTestStore()
	a, b := launchOne(t, s, "m5.large", ""), launchOne(t, s, "m5.large", "")
	if a.SubnetID == "" || a.AZ != "us-west-2a" {
		t.Fatalf("subnet/zone = %q/%q", a.SubnetID, a.AZ)
	}
	if a.PrivateIP == b.PrivateIP || a.ID == b.ID {
		t.Fatalf("ids/ips must differ: %v %v", a, b)
	}
	if a.Market != "on-demand" {
		t.Fatalf("default market = %q", a.Market)
	}
}

func TestLaunchErrorsUseEC2Codes(t *testing.T) {
	s, _ := newTestStore()
	cases := []struct {
		name string
		req  LaunchRequest
		code string
	}{
		{"unknown type", LaunchRequest{Type: "x9.huge", AZ: "us-west-2a", Count: 1}, "InvalidInstanceType"},
		{"unknown image", LaunchRequest{Type: "m5.large", AZ: "us-west-2a", ImageID: "ami-nope", Count: 1}, "InvalidAMIID.NotFound"},
		{"unknown subnet", LaunchRequest{Type: "m5.large", SubnetID: "subnet-nope", Count: 1}, "InvalidSubnetID.NotFound"},
		{"no placement", LaunchRequest{Type: "m5.large", Count: 1}, "MissingParameter"},
		{"bad zone", LaunchRequest{Type: "m5.large", AZ: "us-east-9z", Count: 1}, "InvalidParameterValue"},
		{"zero count", LaunchRequest{Type: "m5.large", AZ: "us-west-2a"}, "InvalidParameterValue"},
	}
	for _, tc := range cases {
		_, err := s.Launch(tc.req)
		ae, ok := err.(*APIError)
		if !ok || ae.Code != tc.code {
			t.Errorf("%s: err = %v, want code %s", tc.name, err, tc.code)
		}
	}
}

func TestUnavailableCapacityFailsOnlyThatTypeAndZone(t *testing.T) {
	s, _ := newTestStore()
	s.MarkUnavailable("m5.large", "us-west-2a")
	if _, err := s.Launch(LaunchRequest{Type: "m5.large", AZ: "us-west-2a", Count: 1}); err == nil {
		t.Fatal("expected InsufficientInstanceCapacity")
	} else if ae := err.(*APIError); ae.Code != "InsufficientInstanceCapacity" {
		t.Fatalf("code = %s", ae.Code)
	}
	if _, err := s.Launch(LaunchRequest{Type: "m5.large", AZ: "us-west-2b", Count: 1}); err != nil {
		t.Fatalf("other zone must work: %v", err)
	}
	s.ClearUnavailable()
	if _, err := s.Launch(LaunchRequest{Type: "m5.large", AZ: "us-west-2a", Count: 1}); err != nil {
		t.Fatalf("after clear: %v", err)
	}
}

func TestTagMergesAndRejectsUnknownIDs(t *testing.T) {
	s, _ := newTestStore()
	i := launchOne(t, s, "m5.large", "")
	if err := s.Tag([]string{i.ID}, map[string]string{"Name": "n1"}); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Instance(i.ID)
	if v.Tags["Name"] != "n1" || v.Tags["karpenter.sh/nodepool"] != "default" {
		t.Fatalf("tags = %v", v.Tags)
	}
	if err := s.Tag([]string{"i-nope"}, map[string]string{"a": "b"}); err == nil {
		t.Fatal("expected an error for an unknown ID")
	}
}

func TestViewIsACopy(t *testing.T) {
	s, _ := newTestStore()
	i := launchOne(t, s, "m5.large", "")
	i.Tags["mutated"] = "yes"
	v, _ := s.Instance(i.ID)
	if _, bad := v.Tags["mutated"]; bad {
		t.Fatal("mutating a view must not change the store")
	}
}
```

`internal/ec2sim/fleet_test.go`:

```go
package ec2sim

import "testing"

func fleetFixture(t *testing.T) (*Store, *clock) {
	t.Helper()
	s, c := newTestStore()
	if _, err := s.CreateTemplate("karpenter.k8s.aws/abc", DefaultAMI, map[string]string{"karpenter.sh/managed-by": "meridian-g0"}); err != nil {
		t.Fatal(err)
	}
	return s, c
}

func TestCreateFleetLaunchesFromTheLowestPriorityPoolWithCapacity(t *testing.T) {
	s, _ := fleetFixture(t)
	s.MarkUnavailable("m5.large", "us-west-2a") // the preferred pool is exhausted
	res, err := s.CreateFleet(FleetRequest{
		Template: "karpenter.k8s.aws/abc", Market: "spot", Count: 1,
		Overrides: []FleetOverride{
			{Type: "m5.large", AZ: "us-west-2a", Priority: 1},
			{Type: "c5.large", AZ: "us-west-2b", Priority: 2},
			{Type: "r5.large", AZ: "us-west-2c", Priority: 3},
		},
		Tags: map[string]string{"karpenter.sh/nodepool": "default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || res.Errors[0].Code != "InsufficientInstanceCapacity" || res.Errors[0].Override.Type != "m5.large" {
		t.Fatalf("errors = %+v", res.Errors)
	}
	if len(res.Instances) != 1 || res.Instances[0].Type != "c5.large" || res.Instances[0].AZ != "us-west-2b" {
		t.Fatalf("instances = %+v", res.Instances)
	}
	v, _ := s.Instance(res.Instances[0].IDs[0])
	if v.Market != "spot" || v.Tags["karpenter.sh/nodepool"] != "default" || v.LaunchTemplateID == "" {
		t.Fatalf("instance = %+v", v)
	}
}

func TestCreateFleetReportsEveryPoolWhenNothingHasCapacity(t *testing.T) {
	s, _ := fleetFixture(t)
	s.MarkUnavailable("m5.large", "")
	res, err := s.CreateFleet(FleetRequest{Template: "karpenter.k8s.aws/abc", Count: 1,
		Overrides: []FleetOverride{{Type: "m5.large", AZ: "us-west-2a"}, {Type: "m5.large", AZ: "us-west-2b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Instances) != 0 || len(res.Errors) != 2 {
		t.Fatalf("result = %+v", res)
	}
}

func TestCreateFleetRejectsBadRequests(t *testing.T) {
	s, _ := fleetFixture(t)
	ov := []FleetOverride{{Type: "m5.large", AZ: "us-west-2a"}}
	cases := map[string]FleetRequest{
		"no overrides": {Template: "karpenter.k8s.aws/abc", Count: 1},
		"zero count":   {Template: "karpenter.k8s.aws/abc", Overrides: ov},
		"no template":  {Template: "missing", Count: 1, Overrides: ov},
	}
	for name, req := range cases {
		if _, err := s.CreateFleet(req); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestTemplatesCreateFindDeleteAndDuplicate(t *testing.T) {
	s, _ := newTestStore()
	tpl, err := s.CreateTemplate("lt-a", DefaultAMI, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTemplate("lt-a", DefaultAMI, nil); err == nil {
		t.Fatal("duplicate name must fail")
	}
	if _, ok := s.Template(tpl.ID); !ok {
		t.Fatal("lookup by ID failed")
	}
	if _, err := s.CreateTemplate("lt-bad", "ami-nope", nil); err == nil {
		t.Fatal("unknown AMI must fail")
	}
	if _, err := s.DeleteTemplate("lt-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteTemplate("lt-a"); err == nil {
		t.Fatal("deleting twice must fail")
	}
	if len(s.Templates()) != 0 {
		t.Fatal("templates should be empty")
	}
}

func TestCreateFleetSupportsPerOverrideTemplates(t *testing.T) {
	s, _ := fleetFixture(t)
	if _, err := s.CreateTemplate("second", DefaultAMI, nil); err != nil {
		t.Fatal(err)
	}
	s.MarkUnavailable("m5.large", "")
	res, err := s.CreateFleet(FleetRequest{Count: 1, Overrides: []FleetOverride{
		{Type: "m5.large", AZ: "us-west-2a", Template: "karpenter.k8s.aws/abc", Priority: 1},
		{Type: "m6g.large", AZ: "us-west-2a", Template: "second", Priority: 2},
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := s.Template("second")
	if len(res.Instances) != 1 || res.Instances[0].TemplateID != second.ID {
		t.Fatalf("instances = %+v, want template %s", res.Instances, second.ID)
	}
	if _, err := s.CreateFleet(FleetRequest{Count: 1, Overrides: []FleetOverride{{Type: "m5.large", AZ: "us-west-2a", Template: "nope"}}}); err == nil {
		t.Fatal("an unknown template must fail the whole request")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/ec2sim/ -v`
Expected: FAIL with `undefined` errors (`Lookup`, `NewStore`, `ParseFilters`, and so on).

- [ ] **Step 3: Write the simulator core**

Errors and fixtures:

`internal/ec2sim/errors.go`:

```go
package ec2sim

import "fmt"

// APIError is an EC2 error the handler turns into a wire error response.
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func apiErr(code, format string, args ...any) *APIError {
	return &APIError{Code: code, Message: fmt.Sprintf(format, args...)}
}
```

`internal/ec2sim/fixtures.go`:

```go
package ec2sim

import (
	"fmt"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

// DefaultAMI is the image the seeded SSM parameter points at (scripts/seed-floci.sh).
const DefaultAMI = "ami-0a1b2c3d4e5f60718"

// VPCID is the single simulated VPC.
const VPCID = "vpc-0a1b2c3d4e5f60718"

// DiscoveryTag is the tag Karpenter's EC2NodeClass selects subnets and security
// groups with (deploy/nodepool-kwok.yaml uses the same key and value).
const (
	DiscoveryKey   = "karpenter.sh/discovery"
	DiscoveryValue = "meridian-g0"
)

// Subnet is a fixture subnet, one per availability zone.
type Subnet struct {
	ID     string
	AZ     string
	AZID   string
	CIDR   string
	Prefix string // first three octets, used to hand out private IPs
	Tags   map[string]string
}

// SecurityGroup is a fixture security group.
type SecurityGroup struct {
	ID   string
	Name string
	Tags map[string]string
}

// Image is a fixture AMI.
type Image struct {
	ID           string
	Name         string
	Architecture string
	CreationDate string
}

// Fixtures returns the subnets, security groups and images the simulator serves.
// They are code, not state: DescribeSubnets always answers the same.
func Fixtures() (subnets []Subnet, sgs []SecurityGroup, images []Image) {
	for i, z := range catalog.AZs {
		subnets = append(subnets, Subnet{
			ID:     fmt.Sprintf("subnet-0%016x", i+1),
			AZ:     z.Name,
			AZID:   z.ID,
			CIDR:   fmt.Sprintf("10.0.%d.0/24", i+1),
			Prefix: fmt.Sprintf("10.0.%d.", i+1),
			Tags:   map[string]string{DiscoveryKey: DiscoveryValue, "Name": "meridian-g0-" + z.Name},
		})
	}
	sgs = []SecurityGroup{{
		ID:   "sg-0a1b2c3d4e5f60718",
		Name: "meridian-g0-nodes",
		Tags: map[string]string{DiscoveryKey: DiscoveryValue},
	}}
	images = []Image{{
		ID:           DefaultAMI,
		Name:         "amazon-eks-node-al2023-x86_64-standard-sim",
		Architecture: "x86_64",
		CreationDate: "2026-01-15T00:00:00.000Z",
	}}
	return
}
```

Filters (EC2's `Filter.N.Name` / `Filter.N.Value.M`, tag filters and `TagSpecification.N`):

`internal/ec2sim/filters.go`:

```go
package ec2sim

import (
	"path"
	"sort"
	"strings"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

// Filter is one EC2 Filter.N: a name and the values it accepts (OR).
type Filter struct {
	Name   string
	Values []string
}

// ParseFilters reads Filter.N.Name and Filter.N.Value.M from a request tree.
func ParseFilters(root *awsapi.Node) []Filter {
	var out []Filter
	for _, f := range root.Get("Filter").List() {
		out = append(out, Filter{Name: f.Str("Name"), Values: f.Get("Value").Strings()})
	}
	return out
}

// Match reports whether a resource passes every filter (AND across filters, OR
// across values). attr returns the resource's values for a filter name; nil
// (an unknown name or an absent tag) fails the match.
func Match(filters []Filter, attr func(name string) []string) bool {
	for _, f := range filters {
		have := attr(f.Name)
		ok := false
		for _, want := range f.Values {
			for _, h := range have {
				// EC2 filters allow * and ? wildcards.
				if m, _ := path.Match(want, h); m || want == h {
					ok = true
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// TagAttr answers the tag-related filter names: tag:KEY, tag-key and tag-value.
// The second result is false when the name is not a tag filter.
func TagAttr(tags map[string]string, name string) ([]string, bool) {
	switch {
	case strings.HasPrefix(name, "tag:"):
		if v, ok := tags[strings.TrimPrefix(name, "tag:")]; ok {
			return []string{v}, true
		}
		return nil, true
	case name == "tag-key":
		keys := make([]string, 0, len(tags))
		for k := range tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys, true
	case name == "tag-value":
		vals := make([]string, 0, len(tags))
		for _, v := range tags {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		return vals, true
	}
	return nil, false
}

// ParseTagSpecs reads TagSpecification.N: it returns the tags for one resource
// type (instance, launch-template, fleet, ...).
func ParseTagSpecs(root *awsapi.Node, resourceType string) map[string]string {
	tags := map[string]string{}
	for _, spec := range root.Get("TagSpecification").List() {
		if spec.Str("ResourceType") != resourceType {
			continue
		}
		for _, t := range spec.Get("Tag").List() {
			tags[t.Str("Key")] = t.Str("Value")
		}
	}
	return tags
}
```

The store and instance state machine:

`internal/ec2sim/store.go`:

```go
package ec2sim

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

// InstanceState is the EC2 instance lifecycle state.
type InstanceState string

const (
	StatePending      InstanceState = "pending"
	StateRunning      InstanceState = "running"
	StateShuttingDown InstanceState = "shutting-down"
	StateTerminated   InstanceState = "terminated"
)

// Instance is the stored record. State is derived from time, never stored.
type Instance struct {
	ID               string
	Type             string
	AZ               string
	SubnetID         string
	ImageID          string
	LaunchTemplateID string
	Market           string // "spot" | "on-demand"
	PrivateIP        string
	SecurityGroupIDs []string
	LaunchTime       time.Time
	TerminatedAt     time.Time // zero until Terminate
	Tags             map[string]string
}

// InstanceView is an Instance with its state at one instant, safe to read
// without the store lock.
type InstanceView struct {
	Instance
	State InstanceState
}

// Template is a launch template (only the fields the simulator uses).
type Template struct {
	ID        string
	Name      string
	ImageID   string
	CreatedAt time.Time
	Tags      map[string]string
}

// Options configures the lifecycle timing and the clock.
type Options struct {
	Now             func() time.Time // nil means time.Now
	PendingFor      time.Duration    // pending -> running (default 3s)
	ShuttingDownFor time.Duration    // shutting-down -> terminated (default 2s)
}

// Store holds all simulated EC2 state in memory. It is safe for concurrent use.
type Store struct {
	mu          sync.Mutex
	opt         Options
	seq         int
	perSubnet   map[string]int
	instances   map[string]*Instance
	order       []string
	templates   map[string]*Template
	unavailable map[string]bool // "type|az" pairs that fail with InsufficientInstanceCapacity
	subnets     []Subnet
	images      map[string]Image
	sgs         []SecurityGroup
}

// NewStore builds an empty store with the fixtures loaded.
func NewStore(o Options) *Store {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.PendingFor == 0 {
		o.PendingFor = 3 * time.Second
	}
	if o.ShuttingDownFor == 0 {
		o.ShuttingDownFor = 2 * time.Second
	}
	subnets, sgs, images := Fixtures()
	s := &Store{
		opt: o, perSubnet: map[string]int{},
		instances: map[string]*Instance{}, templates: map[string]*Template{},
		unavailable: map[string]bool{}, subnets: subnets, sgs: sgs, images: map[string]Image{},
	}
	for _, im := range images {
		s.images[im.ID] = im
	}
	return s
}

// Now is the store's clock, so handlers stamp responses consistently.
func (s *Store) Now() time.Time { return s.opt.Now() }

// Subnets, SecurityGroups and Images expose the fixtures (read-only copies).
func (s *Store) Subnets() []Subnet               { return append([]Subnet(nil), s.subnets...) }
func (s *Store) SecurityGroups() []SecurityGroup { return append([]SecurityGroup(nil), s.sgs...) }
func (s *Store) Images() []Image {
	var out []Image
	for _, im := range s.images {
		out = append(out, im)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) stateOf(i *Instance, now time.Time) InstanceState {
	if !i.TerminatedAt.IsZero() {
		if now.Before(i.TerminatedAt.Add(s.opt.ShuttingDownFor)) {
			return StateShuttingDown
		}
		return StateTerminated
	}
	if now.Before(i.LaunchTime.Add(s.opt.PendingFor)) {
		return StatePending
	}
	return StateRunning
}

func (s *Store) view(i *Instance, now time.Time) InstanceView {
	c := *i
	c.Tags = copyTags(i.Tags)
	c.SecurityGroupIDs = append([]string(nil), i.SecurityGroupIDs...)
	return InstanceView{Instance: c, State: s.stateOf(i, now)}
}

func copyTags(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// LaunchRequest describes one batch of identical instances.
type LaunchRequest struct {
	Type, AZ, SubnetID, ImageID, LaunchTemplateID, Market string
	Count                                                 int
	SecurityGroupIDs                                      []string
	Tags                                                  map[string]string
}

// MarkUnavailable makes launches of typ in az (or in every zone when az is "")
// fail with InsufficientInstanceCapacity, so tests and demos can exercise
// Karpenter's capacity-error handling.
func (s *Store) MarkUnavailable(typ, az string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unavailable[typ+"|"+az] = true
}

// ClearUnavailable removes every capacity restriction.
func (s *Store) ClearUnavailable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unavailable = map[string]bool{}
}

func (s *Store) isUnavailable(typ, az string) bool {
	return s.unavailable[typ+"|"+az] || s.unavailable[typ+"|"]
}

// Launch creates req.Count instances in pending state.
func (s *Store) Launch(req LaunchRequest) ([]InstanceView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.launchLocked(req)
}

// launchLocked is Launch for callers that already hold s.mu (CreateFleet).
func (s *Store) launchLocked(req LaunchRequest) ([]InstanceView, error) {
	if req.Count < 1 {
		return nil, apiErr("InvalidParameterValue", "Count must be at least 1")
	}
	typ, ok := catalog.Lookup(req.Type)
	if !ok {
		return nil, apiErr("InvalidInstanceType", "The instance type '%s' does not exist", req.Type)
	}
	if req.ImageID == "" {
		req.ImageID = DefaultAMI
	}
	if _, ok := s.images[req.ImageID]; !ok {
		return nil, apiErr("InvalidAMIID.NotFound", "The image id '[%s]' does not exist", req.ImageID)
	}
	sub, err := s.resolveSubnet(req.SubnetID, req.AZ)
	if err != nil {
		return nil, err
	}
	if s.isUnavailable(typ.Name, sub.AZ) {
		return nil, apiErr("InsufficientInstanceCapacity", "There is no capacity for %s in %s", typ.Name, sub.AZ)
	}
	market := req.Market
	if market == "" {
		market = "on-demand"
	}
	if market != "spot" && market != "on-demand" {
		return nil, apiErr("InvalidParameterValue", "unknown market %q", market)
	}
	sgs := req.SecurityGroupIDs
	if len(sgs) == 0 {
		sgs = []string{s.sgs[0].ID}
	}
	now := s.opt.Now()
	var out []InstanceView
	for n := 0; n < req.Count; n++ {
		s.seq++
		s.perSubnet[sub.ID]++
		inst := &Instance{
			ID:               fmt.Sprintf("i-%017x", s.seq),
			Type:             typ.Name,
			AZ:               sub.AZ,
			SubnetID:         sub.ID,
			ImageID:          req.ImageID,
			LaunchTemplateID: req.LaunchTemplateID,
			Market:           market,
			PrivateIP:        fmt.Sprintf("%s%d", sub.Prefix, 9+s.perSubnet[sub.ID]),
			SecurityGroupIDs: append([]string(nil), sgs...),
			LaunchTime:       now,
			Tags:             copyTags(req.Tags),
		}
		s.instances[inst.ID] = inst
		s.order = append(s.order, inst.ID)
		out = append(out, s.view(inst, now))
	}
	return out, nil
}

// resolveSubnet finds the subnet for a launch from a subnet ID, a zone, or both.
func (s *Store) resolveSubnet(subnetID, az string) (Subnet, error) {
	if subnetID == "" && az == "" {
		return Subnet{}, apiErr("MissingParameter", "a subnet or an availability zone is required")
	}
	for _, sub := range s.subnets {
		if subnetID != "" && sub.ID != subnetID {
			continue
		}
		if az != "" && sub.AZ != az {
			continue
		}
		return sub, nil
	}
	if subnetID != "" {
		return Subnet{}, apiErr("InvalidSubnetID.NotFound", "The subnet ID '%s' does not exist", subnetID)
	}
	return Subnet{}, apiErr("InvalidParameterValue", "Value (%s) for parameter availabilityZone is invalid", az)
}

// Transition records a state change caused by Terminate.
type Transition struct {
	ID       string
	Previous InstanceState
	Current  InstanceState
}

// Terminate starts termination. Terminating an already-terminated instance is
// not an error, as in EC2. An unknown ID fails the whole call.
func (s *Store) Terminate(ids []string) ([]Transition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	for _, id := range ids {
		if _, ok := s.instances[id]; !ok {
			return nil, apiErr("InvalidInstanceID.NotFound", "The instance ID '%s' does not exist", id)
		}
	}
	var out []Transition
	for _, id := range ids {
		inst := s.instances[id]
		prev := s.stateOf(inst, now)
		if inst.TerminatedAt.IsZero() {
			inst.TerminatedAt = now
		}
		out = append(out, Transition{ID: id, Previous: prev, Current: s.stateOf(inst, now)})
	}
	return out, nil
}

// Instances returns every instance (including terminated ones) in launch order.
func (s *Store) Instances() []InstanceView {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	out := make([]InstanceView, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.view(s.instances[id], now))
	}
	return out
}

// Instance returns one instance.
func (s *Store) Instance(id string) (InstanceView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.instances[id]
	if !ok {
		return InstanceView{}, false
	}
	return s.view(i, s.opt.Now()), true
}

// Tag adds tags to instances or launch templates. Unknown IDs fail the call.
func (s *Store) Tag(resources []string, tags map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range resources {
		if _, ok := s.instances[r]; ok {
			continue
		}
		if _, ok := s.templateByID(r); ok {
			continue
		}
		return apiErr("InvalidID", "The ID '%s' is not valid", r)
	}
	for _, r := range resources {
		target := map[string]string(nil)
		if i, ok := s.instances[r]; ok {
			target = i.Tags
		} else if t, ok := s.templateByID(r); ok {
			target = t.Tags
		}
		for k, v := range tags {
			target[k] = v
		}
	}
	return nil
}

func (s *Store) templateByID(id string) (*Template, bool) {
	for _, t := range s.templates {
		if t.ID == id {
			return t, true
		}
	}
	return nil, false
}
```

Launch templates:

`internal/ec2sim/templates.go`:

```go
package ec2sim

import (
	"fmt"
	"sort"
	"strings"
)

// CreateTemplate stores a launch template. Names are unique, as in EC2.
func (s *Store) CreateTemplate(name, imageID string, tags map[string]string) (Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		return Template{}, apiErr("MissingParameter", "The request must contain the parameter LaunchTemplateName")
	}
	if _, dup := s.templates[name]; dup {
		return Template{}, apiErr("InvalidLaunchTemplateName.AlreadyExistsException",
			"Launch template name already in use.")
	}
	if imageID != "" {
		if _, ok := s.images[imageID]; !ok {
			return Template{}, apiErr("InvalidAMIID.NotFound", "The image id '[%s]' does not exist", imageID)
		}
	}
	s.seq++
	t := &Template{
		ID:        fmt.Sprintf("lt-%017x", s.seq),
		Name:      name,
		ImageID:   imageID,
		CreatedAt: s.opt.Now(),
		Tags:      copyTags(tags),
	}
	s.templates[name] = t
	return *t, nil
}

// findTemplate resolves a template by name or ID. Callers hold s.mu.
func (s *Store) findTemplate(nameOrID string) (*Template, bool) {
	if t, ok := s.templates[nameOrID]; ok {
		return t, true
	}
	if strings.HasPrefix(nameOrID, "lt-") {
		return s.templateByID(nameOrID)
	}
	return nil, false
}

// Template returns one launch template by name or ID.
func (s *Store) Template(nameOrID string) (Template, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.findTemplate(nameOrID)
	if !ok {
		return Template{}, false
	}
	c := *t
	c.Tags = copyTags(t.Tags)
	return c, true
}

// Templates returns every launch template sorted by name.
func (s *Store) Templates() []Template {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Template, 0, len(s.templates))
	for _, t := range s.templates {
		c := *t
		c.Tags = copyTags(t.Tags)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DeleteTemplate removes a launch template by name or ID and returns it.
func (s *Store) DeleteTemplate(nameOrID string) (Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.findTemplate(nameOrID)
	if !ok {
		return Template{}, apiErr("InvalidLaunchTemplateName.NotFoundException",
			"The specified launch template, with template name %s, does not exist.", nameOrID)
	}
	delete(s.templates, t.Name)
	return *t, nil
}
```

Instant fleets:

`internal/ec2sim/fleet.go`:

```go
package ec2sim

import (
	"fmt"
	"sort"
)

// FleetOverride is one (instance type, zone) pool CreateFleet may use.
type FleetOverride struct {
	Type     string
	AZ       string
	SubnetID string
	Priority float64 // lower is tried first; ties keep request order
	Template string  // launch template name or ID; empty means FleetRequest.Template
}

// FleetRequest is an instant fleet request, the only kind Karpenter makes.
type FleetRequest struct {
	Template  string // default launch template name or ID for overrides without their own
	Market    string // "spot" | "on-demand"
	Count     int
	Overrides []FleetOverride
	Tags      map[string]string // applied to the launched instances
}

// FleetInstance is one launched group, as in the CreateFleet response.
type FleetInstance struct {
	IDs        []string
	Type       string
	AZ         string
	Market     string
	TemplateID string
}

// FleetError is one failed pool, as in the CreateFleet response.
type FleetError struct {
	Code, Message string
	Override      FleetOverride
	Market        string
	TemplateID    string
}

// FleetResult is the outcome of CreateFleet. Partial failure is normal:
// Instances and Errors can both be non-empty.
type FleetResult struct {
	FleetID   string
	Instances []FleetInstance
	Errors    []FleetError
}

// CreateFleet tries the overrides in priority order and launches the whole
// request from the first pool that has capacity. Pools that fail are reported
// in Errors, which is how Karpenter learns a pool is exhausted.
func (s *Store) CreateFleet(req FleetRequest) (FleetResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(req.Overrides) == 0 {
		return FleetResult{}, apiErr("MissingParameter", "The request must contain at least one launch template override")
	}
	if req.Count < 1 {
		return FleetResult{}, apiErr("InvalidParameterValue", "TotalTargetCapacity must be at least 1")
	}
	// Resolve every template up front: an unknown template fails the whole request.
	tpls := make([]*Template, len(req.Overrides))
	for i := range req.Overrides {
		if req.Overrides[i].Template == "" {
			req.Overrides[i].Template = req.Template
		}
		t, ok := s.findTemplate(req.Overrides[i].Template)
		if !ok {
			return FleetResult{}, apiErr("InvalidLaunchTemplateName.NotFoundException",
				"The specified launch template, with template name %s, does not exist.", req.Overrides[i].Template)
		}
		tpls[i] = t
	}
	if req.Market == "" {
		req.Market = "on-demand"
	}

	// Stable sort keeps request order for equal priorities.
	type pool struct {
		o FleetOverride
		t *Template
	}
	pools := make([]pool, len(req.Overrides))
	for i := range req.Overrides {
		pools[i] = pool{req.Overrides[i], tpls[i]}
	}
	sort.SliceStable(pools, func(i, j int) bool { return pools[i].o.Priority < pools[j].o.Priority })

	s.seq++
	res := FleetResult{FleetID: fmt.Sprintf("fleet-%08x-0000-4000-8000-%012x", s.seq, s.seq)}
	for _, p := range pools {
		launched, err := s.launchLocked(LaunchRequest{
			Type: p.o.Type, AZ: p.o.AZ, SubnetID: p.o.SubnetID, ImageID: p.t.ImageID,
			LaunchTemplateID: p.t.ID, Market: req.Market, Count: req.Count, Tags: req.Tags,
		})
		if err != nil {
			ae := err.(*APIError)
			res.Errors = append(res.Errors, FleetError{Code: ae.Code, Message: ae.Message,
				Override: p.o, Market: req.Market, TemplateID: p.t.ID})
			continue
		}
		fi := FleetInstance{Type: launched[0].Type, AZ: launched[0].AZ, Market: req.Market, TemplateID: p.t.ID}
		for _, l := range launched {
			fi.IDs = append(fi.IDs, l.ID)
		}
		res.Instances = append(res.Instances, fi)
		break
	}
	return res, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `gofmt -l internal && go vet ./internal/catalog/ ./internal/ec2sim/ && go test ./internal/ec2sim/ -v -race`
Expected: no gofmt output; all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ec2sim
git commit -m "feat: add EC2 simulator core (instances, launch templates, instant fleets)"
```

### Task 12: [Fallback] EC2 wire layer, `ec2-sim` binary, and the contract check

> **Conditional task.** Execute this only if `docs/s0/decision.md` (Task 8) moved operations to `ec2-sim`. If not, skip it and record the skip in `docs/s0/decision.md`.



**Files:**
- Create: `internal/ec2sim/xmlb.go`, `render.go`, `handler.go`, `cmd/ec2-sim/main.go`
- Test: `internal/ec2sim/handler_test.go`, `internal/ec2sim/gateway_test.go`
- Create: `internal/s0probe/selftest_ec2_test.go` (and `fakemisc_test.go` if absent)

**Interfaces:**
- Consumes: Task 11's `Store` API; `awsapi.ReadBody`, `Form`, `Nest`, `WriteError` (`StyleEC2`); `gateway.New`, `gateway.CheckBind`; `awscall.BuildQuery`; `ops.List`.
- Produces: `ec2sim.NewHandler(*Store) *Handler` (an `http.Handler` speaking the EC2 query protocol); `ec2sim.Operations() []string`; the `ec2-sim` binary; Compose service `ec2-sim` registered as the gateway's `ec2` backend.

**Acceptance Criteria:**
- All 15 EC2 operations in `ops.yaml` are served: `CreateLaunchTemplate`, `DeleteLaunchTemplate`, `DescribeLaunchTemplates`, `CreateFleet`, `RunInstances`, `TerminateInstances`, `DescribeInstances`, `CreateTags`, `DescribeInstanceTypes`, `DescribeInstanceTypeOfferings`, `DescribeAvailabilityZones`, `DescribeImages`, `DescribeSubnets`, `DescribeSecurityGroups`, `DescribeSpotPriceHistory`.
- Errors use the EC2 error envelope (`<Response><Errors><Error>`); capacity errors return HTTP 500, all others 400.
- A SigV4-signed request through the gateway reaches the simulator; a bad signature is rejected by the gateway and never reaches it.
- `ec2-sim` is not published on any host port and refuses a non-loopback address without `-allow-any-interface`.

- [ ] **Step 1: Write the failing tests**

These drive the handler with real query-protocol parameters (the names the AWS SDK sends) and decode the XML back, so a wrong element name fails here instead of inside Karpenter.

`internal/ec2sim/handler_test.go`:

```go
package ec2sim

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// call posts a query-protocol request to the handler and returns status and body.
func call(t *testing.T, h http.Handler, action string, kv ...string) (int, string) {
	t.Helper()
	v := url.Values{"Action": {action}, "Version": {"2016-11-15"}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// parse decodes the XML response into out (field tags use the lower-camel EC2 names).
func parse(t *testing.T, body string, out any) {
	t.Helper()
	if err := xml.Unmarshal([]byte(body), out); err != nil {
		t.Fatalf("bad XML: %v\n%s", err, body)
	}
}

type describeInstancesOut struct {
	Instances []struct {
		ID    string `xml:"instanceId"`
		State string `xml:"instanceState>name"`
		Type  string `xml:"instanceType"`
		IP    string `xml:"privateIpAddress"`
		Life  string `xml:"instanceLifecycle"`
		Tags  []struct {
			Key   string `xml:"key"`
			Value string `xml:"value"`
		} `xml:"tagSet>item"`
	} `xml:"reservationSet>item>instancesSet>item"`
}

type fleetOut struct {
	FleetID   string `xml:"fleetId"`
	Instances []struct {
		IDs       []string `xml:"instanceIds>item"`
		Type      string   `xml:"instanceType"`
		Lifecycle string   `xml:"lifecycle"`
	} `xml:"fleetInstanceSet>item"`
	Errors []struct {
		Code string `xml:"errorCode"`
	} `xml:"errorSet>item"`
}

func newHandler() (*Handler, *Store, *clock) {
	s, c := newTestStore()
	return NewHandler(s), s, c
}

func TestFleetThenDescribeInstancesShowsPendingThenRunning(t *testing.T) {
	h, _, c := newHandler()
	if code, body := call(t, h, "CreateLaunchTemplate",
		"LaunchTemplateName", "karpenter.k8s.aws/abc", "LaunchTemplateData.ImageId", DefaultAMI,
		"TagSpecification.1.ResourceType", "launch-template",
		"TagSpecification.1.Tag.1.Key", "karpenter.k8s.aws/cluster", "TagSpecification.1.Tag.1.Value", "meridian-g0"); code != 200 {
		t.Fatalf("CreateLaunchTemplate = %d %s", code, body)
	}
	code, body := call(t, h, "CreateFleet",
		"Type", "instant",
		"TargetCapacitySpecification.TotalTargetCapacity", "1",
		"TargetCapacitySpecification.DefaultTargetCapacityType", "spot",
		"LaunchTemplateConfigs.1.LaunchTemplateSpecification.LaunchTemplateName", "karpenter.k8s.aws/abc",
		"LaunchTemplateConfigs.1.LaunchTemplateSpecification.Version", "$Latest",
		"LaunchTemplateConfigs.1.Overrides.1.InstanceType", "m5.large",
		"LaunchTemplateConfigs.1.Overrides.1.AvailabilityZone", "us-west-2a",
		"LaunchTemplateConfigs.1.Overrides.1.Priority", "1",
		"TagSpecification.1.ResourceType", "instance",
		"TagSpecification.1.Tag.1.Key", "karpenter.sh/nodepool", "TagSpecification.1.Tag.1.Value", "default")
	if code != 200 {
		t.Fatalf("CreateFleet = %d %s", code, body)
	}
	var f fleetOut
	parse(t, body, &f)
	if len(f.Instances) != 1 || len(f.Instances[0].IDs) != 1 || f.Instances[0].Lifecycle != "spot" || len(f.Errors) != 0 {
		t.Fatalf("fleet = %+v", f)
	}
	id := f.Instances[0].IDs[0]

	_, body = call(t, h, "DescribeInstances", "InstanceId.1", id)
	var d describeInstancesOut
	parse(t, body, &d)
	if len(d.Instances) != 1 || d.Instances[0].State != "pending" || d.Instances[0].Life != "spot" || d.Instances[0].Tags[0].Key != "karpenter.sh/nodepool" {
		t.Fatalf("pending describe = %+v", d)
	}

	c.advance(3 * time.Second)
	_, body = call(t, h, "DescribeInstances",
		"Filter.1.Name", "instance-state-name", "Filter.1.Value.1", "running",
		"Filter.2.Name", "tag-key", "Filter.2.Value.1", "karpenter.sh/nodepool")
	d = describeInstancesOut{}
	parse(t, body, &d)
	if len(d.Instances) != 1 || d.Instances[0].State != "running" || d.Instances[0].IP == "" {
		t.Fatalf("running describe = %+v", d)
	}
}

func TestFleetCapacityErrorIsReportedInTheErrorSet(t *testing.T) {
	h, s, _ := newHandler()
	call(t, h, "CreateLaunchTemplate", "LaunchTemplateName", "lt", "LaunchTemplateData.ImageId", DefaultAMI)
	s.MarkUnavailable("m5.large", "")
	code, body := call(t, h, "CreateFleet", "Type", "instant",
		"TargetCapacitySpecification.TotalTargetCapacity", "1",
		"LaunchTemplateConfigs.1.LaunchTemplateSpecification.LaunchTemplateName", "lt",
		"LaunchTemplateConfigs.1.Overrides.1.InstanceType", "m5.large",
		"LaunchTemplateConfigs.1.Overrides.1.AvailabilityZone", "us-west-2a")
	var f fleetOut
	parse(t, body, &f)
	if code != 200 || len(f.Instances) != 0 || len(f.Errors) != 1 || f.Errors[0].Code != "InsufficientInstanceCapacity" {
		t.Fatalf("code=%d fleet=%+v", code, f)
	}
}

func TestTerminateInstancesReportsStateTransition(t *testing.T) {
	h, s, c := newHandler()
	v, _ := s.Launch(LaunchRequest{Type: "m5.large", AZ: "us-west-2a", Count: 1})
	c.advance(3 * time.Second)
	code, body := call(t, h, "TerminateInstances", "InstanceId.1", v[0].ID)
	var out struct {
		Cur  string `xml:"instancesSet>item>currentState>name"`
		Prev string `xml:"instancesSet>item>previousState>name"`
	}
	parse(t, body, &out)
	if code != 200 || out.Prev != "running" || out.Cur != "shutting-down" {
		t.Fatalf("code=%d out=%+v", code, out)
	}
}

func TestErrorsUseTheEC2Envelope(t *testing.T) {
	h, _, _ := newHandler()
	for _, tc := range []struct {
		action, code string
		kv           []string
	}{
		{"DescribeInstances", "InvalidInstanceID.NotFound", []string{"InstanceId.1", "i-nope"}},
		{"DescribeSubnets", "InvalidSubnetID.NotFound", []string{"SubnetId.1", "subnet-nope"}},
		{"DescribeLaunchTemplates", "InvalidLaunchTemplateName.NotFoundException", []string{"LaunchTemplateName.1", "nope"}},
		{"NoSuchAction", "InvalidAction", nil},
	} {
		status, body := call(t, h, tc.action, tc.kv...)
		var e struct {
			Code string `xml:"Errors>Error>Code"`
		}
		parse(t, body, &e)
		if status != 400 || e.Code != tc.code {
			t.Errorf("%s: status=%d code=%q, want 400 %s", tc.action, status, e.Code, tc.code)
		}
	}
}

func TestDescribeSubnetsAndSecurityGroupsByDiscoveryTag(t *testing.T) {
	h, _, _ := newHandler()
	filter := []string{"Filter.1.Name", "tag:" + DiscoveryKey, "Filter.1.Value.1", DiscoveryValue}
	_, body := call(t, h, "DescribeSubnets", filter...)
	var sn struct {
		IDs []string `xml:"subnetSet>item>subnetId"`
		AZs []string `xml:"subnetSet>item>availabilityZone"`
	}
	parse(t, body, &sn)
	if len(sn.IDs) != 3 || len(sn.AZs) != 3 {
		t.Fatalf("subnets = %+v", sn)
	}
	_, body = call(t, h, "DescribeSecurityGroups", filter...)
	var sg struct {
		IDs []string `xml:"securityGroupInfo>item>groupId"`
	}
	parse(t, body, &sg)
	if len(sg.IDs) != 1 {
		t.Fatalf("security groups = %+v", sg)
	}
	_, body = call(t, h, "DescribeSubnets", "Filter.1.Name", "tag:"+DiscoveryKey, "Filter.1.Value.1", "other")
	sn.IDs, sn.AZs = nil, nil // xml.Unmarshal appends to existing slices
	parse(t, body, &sn)
	if len(sn.IDs) != 0 {
		t.Fatalf("a different discovery tag must match nothing: %+v", sn)
	}
}

func TestDescribeInstanceTypesReturnsCapacityFields(t *testing.T) {
	h, _, _ := newHandler()
	_, body := call(t, h, "DescribeInstanceTypes", "InstanceType.1", "m5.large")
	var out struct {
		Items []struct {
			Name string   `xml:"instanceType"`
			CPU  int      `xml:"vCpuInfo>defaultVCpus"`
			Mem  int      `xml:"memoryInfo>sizeInMiB"`
			Arch []string `xml:"processorInfo>supportedArchitectures>item"`
			ENI  int      `xml:"networkInfo>maximumNetworkInterfaces"`
		} `xml:"instanceTypeSet>item"`
	}
	parse(t, body, &out)
	if len(out.Items) != 1 || out.Items[0].CPU != 2 || out.Items[0].Mem != 8192 || out.Items[0].Arch[0] != "x86_64" || out.Items[0].ENI != 3 {
		t.Fatalf("types = %+v", out)
	}
	_, body = call(t, h, "DescribeInstanceTypes",
		"Filter.1.Name", "processor-info.supported-architecture", "Filter.1.Value.1", "arm64")
	out.Items = nil
	parse(t, body, &out)
	if len(out.Items) != 2 {
		t.Fatalf("arm64 types = %d, want 2", len(out.Items))
	}
}

func TestOfferingsByZoneAndZoneListAndSpotPrices(t *testing.T) {
	h, _, _ := newHandler()
	_, body := call(t, h, "DescribeInstanceTypeOfferings", "LocationType", "availability-zone",
		"Filter.1.Name", "location", "Filter.1.Value.1", "us-west-2a")
	var off struct {
		Locs []string `xml:"instanceTypeOfferingSet>item>location"`
	}
	parse(t, body, &off)
	if len(off.Locs) != 12 {
		t.Fatalf("offerings in 2a = %d, want 12 (one per type)", len(off.Locs))
	}
	_, body = call(t, h, "DescribeAvailabilityZones")
	var az struct {
		Names []string `xml:"availabilityZoneInfo>item>zoneName"`
	}
	parse(t, body, &az)
	if len(az.Names) != 3 {
		t.Fatalf("zones = %v", az.Names)
	}
	_, body = call(t, h, "DescribeSpotPriceHistory", "InstanceType.1", "m5.large", "ProductDescription.1", "Linux/UNIX")
	var sp struct {
		Prices []string `xml:"spotPriceHistorySet>item>spotPrice"`
	}
	parse(t, body, &sp)
	if len(sp.Prices) != 3 {
		t.Fatalf("spot prices = %v, want one per zone", sp.Prices)
	}
}

func TestDescribeImagesByIDAndCreateTags(t *testing.T) {
	h, s, _ := newHandler()
	_, body := call(t, h, "DescribeImages", "ImageId.1", DefaultAMI)
	var im struct {
		IDs  []string `xml:"imagesSet>item>imageId"`
		Arch []string `xml:"imagesSet>item>architecture"`
	}
	parse(t, body, &im)
	if len(im.IDs) != 1 || im.Arch[0] != "x86_64" {
		t.Fatalf("images = %+v", im)
	}
	v, _ := s.Launch(LaunchRequest{Type: "m5.large", AZ: "us-west-2a", Count: 1})
	if code, body := call(t, h, "CreateTags", "ResourceId.1", v[0].ID, "Tag.1.Key", "Name", "Tag.1.Value", "n1"); code != 200 || !strings.Contains(body, "<return>true</return>") {
		t.Fatalf("CreateTags = %d %s", code, body)
	}
	got, _ := s.Instance(v[0].ID)
	if got.Tags["Name"] != "n1" {
		t.Fatalf("tags = %v", got.Tags)
	}
}

func TestRunInstancesAndTemplateDelete(t *testing.T) {
	h, s, _ := newHandler()
	code, body := call(t, h, "RunInstances", "ImageId", DefaultAMI, "InstanceType", "c5.large", "MinCount", "1", "MaxCount", "2",
		"Placement.AvailabilityZone", "us-west-2c", "InstanceMarketOptions.MarketType", "spot")
	var out struct {
		IDs []string `xml:"instancesSet>item>instanceId"`
	}
	parse(t, body, &out)
	if code != 200 || len(out.IDs) != 2 {
		t.Fatalf("RunInstances = %d %s", code, body)
	}
	if v, _ := s.Instance(out.IDs[0]); v.Market != "spot" || v.AZ != "us-west-2c" {
		t.Fatalf("instance = %+v", v)
	}
	call(t, h, "CreateLaunchTemplate", "LaunchTemplateName", "gone")
	if code, _ := call(t, h, "DeleteLaunchTemplate", "LaunchTemplateName", "gone"); code != 200 {
		t.Fatalf("DeleteLaunchTemplate = %d", code)
	}
	if code, _ := call(t, h, "DeleteLaunchTemplate", "LaunchTemplateName", "gone"); code != 400 {
		t.Fatalf("second delete = %d, want 400", code)
	}
}
```

`internal/ec2sim/gateway_test.go`:

```go
package ec2sim

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awscall"
	"github.com/tazzledazzle/meridian/internal/gateway"
	"github.com/tazzledazzle/meridian/internal/ops"
)

// TestSignedRequestThroughTheGatewayReachesEC2Sim proves the pieces fit: a
// SigV4-signed request is verified by the gateway, routed by service name, and
// answered by the simulator.
func TestSignedRequestThroughTheGatewayReachesEC2Sim(t *testing.T) {
	const access, secret = "MRDNTESTKEY", "test-secret-value"
	var list ops.List
	for _, name := range Operations() {
		list.Ops = append(list.Ops, ops.Op{Service: "ec2", Name: name, Protocol: "query", Backend: "ec2-sim", Fidelity: "behavior-inferred"})
	}
	store, _ := newTestStore()
	gw := httptest.NewServer(gateway.New(gateway.Config{
		Ops: list,
		Lookup: func(k string) (string, bool) {
			if k == access {
				return secret, true
			}
			return "", false
		},
		Backends: map[string]http.Handler{"ec2-sim": NewHandler(store)},
	}))
	defer gw.Close()

	send := func(access, secret string) (*http.Response, string) {
		req, _, err := awscall.BuildQuery(gw.URL, "ec2", "us-west-2", "DescribeAvailabilityZones", "2016-11-15", nil, access, secret, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	resp, body := send(access, secret)
	if resp.StatusCode != 200 || !strings.Contains(body, "us-west-2a") {
		t.Fatalf("signed request: %d %s", resp.StatusCode, body)
	}
	resp, _ = send(access, "wrong-secret")
	if resp.StatusCode != 403 {
		t.Fatalf("bad signature must be rejected by the gateway, got %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/ec2sim/ -run 'Fleet|Terminate|Errors|Describe|Offerings|Run|Signed' -v`
Expected: FAIL with `undefined: NewHandler` and `undefined: Operations`.

- [ ] **Step 3: Write the XML builder and renderers**

EC2 responses are deep trees of `<item>` lists, which are clumsy as `encoding/xml` struct tags, so handlers build small `X` trees instead.

`internal/ec2sim/xmlb.go`:

```go
package ec2sim

import (
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

// X is a tiny XML element builder. EC2 responses are deeply nested lists of
// <item> elements, which is clumsy with encoding/xml struct tags, so handlers
// build X trees instead.
type X struct {
	Name string
	Text string
	Kids []X
}

// E is a container element.
func E(name string, kids ...X) X { return X{Name: name, Kids: kids} }

// T is a text element.
func T(name, text string) X { return X{Name: name, Text: text} }

// Item is one <item> of a set.
func Item(kids ...X) X { return X{Name: "item", Kids: kids} }

// Set is a list element: <name><item>..</item><item>..</item></name>.
func Set(name string, items ...X) X { return X{Name: name, Kids: items} }

// Strs is a set of <item>text</item> elements.
func Strs(name string, vals ...string) X {
	items := make([]X, len(vals))
	for i, v := range vals {
		items[i] = T("item", v)
	}
	return Set(name, items...)
}

func (x X) write(sb *strings.Builder) {
	sb.WriteString("<" + x.Name + ">")
	if len(x.Kids) == 0 {
		_ = xml.EscapeText(sb, []byte(x.Text))
	}
	for _, k := range x.Kids {
		k.write(sb)
	}
	sb.WriteString("</" + x.Name + ">")
}

const ec2NS = "http://ec2.amazonaws.com/doc/2016-11-15/"

// respond writes <ActionResponse xmlns=...><requestId/>kids...</ActionResponse>.
func respond(w http.ResponseWriter, action string, kids ...X) {
	id := awsapi.NewRequestID()
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	sb.WriteString("<" + action + `Response xmlns="` + ec2NS + `">`)
	T("requestId", id).write(&sb)
	for _, k := range kids {
		k.write(&sb)
	}
	sb.WriteString("</" + action + "Response>")
	w.Header().Set("Content-Type", "text/xml;charset=UTF-8")
	w.Header().Set("X-Amzn-Requestid", id)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(sb.String()))
}
```

`internal/ec2sim/render.go`:

```go
package ec2sim

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

const (
	accountID = "123456789012"
	tsLayout  = "2006-01-02T15:04:05.000Z"
)

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func tagSet(tags map[string]string) X {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	items := make([]X, len(keys))
	for i, k := range keys {
		items[i] = Item(T("key", k), T("value", tags[k]))
	}
	return Set("tagSet", items...)
}

func stateCode(s InstanceState) string {
	switch s {
	case StatePending:
		return "0"
	case StateRunning:
		return "16"
	case StateShuttingDown:
		return "32"
	default: // terminated
		return "48"
	}
}

func stateEl(name string, s InstanceState) X {
	return E(name, T("code", stateCode(s)), T("name", string(s)))
}

func privateDNS(ip string) string {
	return "ip-" + strings.ReplaceAll(ip, ".", "-") + "." + catalog.Region + ".compute.internal"
}

func arch(typ string) string {
	if t, ok := catalog.Lookup(typ); ok {
		return t.Arch
	}
	return "x86_64"
}

// instanceItem renders one instance as it appears in DescribeInstances and RunInstances.
func instanceItem(v InstanceView) X {
	groups := make([]X, len(v.SecurityGroupIDs))
	for i, g := range v.SecurityGroupIDs {
		groups[i] = Item(T("groupId", g), T("groupName", "meridian-g0-nodes"))
	}
	kids := []X{
		T("instanceId", v.ID),
		T("imageId", v.ImageID),
		stateEl("instanceState", v.State),
		T("privateDnsName", privateDNS(v.PrivateIP)),
		T("dnsName", ""),
		T("amiLaunchIndex", "0"),
		T("instanceType", v.Type),
		T("launchTime", ts(v.LaunchTime)),
		E("placement", T("availabilityZone", v.AZ), T("groupName", ""), T("tenancy", "default")),
		E("monitoring", T("state", "disabled")),
		T("subnetId", v.SubnetID),
		T("vpcId", VPCID),
		T("privateIpAddress", v.PrivateIP),
		T("architecture", arch(v.Type)),
		T("rootDeviceType", "ebs"),
		T("rootDeviceName", "/dev/xvda"),
		T("virtualizationType", "hvm"),
		T("hypervisor", "xen"),
		T("ebsOptimized", "false"),
		T("enaSupport", "true"),
		Set("groupSet", groups...),
		tagSet(v.Tags),
	}
	if v.Market == "spot" {
		kids = append(kids, T("instanceLifecycle", "spot"))
	}
	return Item(kids...)
}

func templateEl(name string, t Template) X {
	return E(name,
		T("launchTemplateId", t.ID),
		T("launchTemplateName", t.Name),
		T("createTime", ts(t.CreatedAt)),
		T("createdBy", "arn:aws:iam::"+accountID+":root"),
		T("defaultVersionNumber", "1"),
		T("latestVersionNumber", "1"),
		tagSet(t.Tags),
	)
}

func typeItem(t catalog.Type) X {
	burst := "false"
	if strings.HasPrefix(t.Name, "t3.") {
		burst = "true"
	}
	perf := "Up to 10 Gigabit"
	return Item(
		T("instanceType", t.Name),
		T("currentGeneration", "true"),
		T("freeTierEligible", "false"),
		Strs("supportedUsageClasses", "on-demand", "spot"),
		Strs("supportedRootDeviceTypes", "ebs"),
		Strs("supportedVirtualizationTypes", "hvm"),
		T("bareMetal", "false"),
		T("hypervisor", "nitro"),
		E("processorInfo", Strs("supportedArchitectures", t.Arch), T("sustainedClockSpeedInGhz", "3.1")),
		E("vCpuInfo", T("defaultVCpus", fmt.Sprint(t.VCPU)), T("defaultCores", fmt.Sprint(t.VCPU/2)), T("defaultThreadsPerCore", "2")),
		E("memoryInfo", T("sizeInMiB", fmt.Sprint(t.MemoryMiB))),
		T("instanceStorageSupported", "false"),
		E("ebsInfo", T("ebsOptimizedSupport", "default"), T("encryptionSupport", "supported"), T("nvmeSupport", "required")),
		E("networkInfo",
			T("networkPerformance", perf),
			T("maximumNetworkInterfaces", fmt.Sprint(t.MaxENIs)),
			T("maximumNetworkCards", "1"),
			T("defaultNetworkCardIndex", "0"),
			T("ipv4AddressesPerInterface", fmt.Sprint(t.IPv4PerENI)),
			T("ipv6AddressesPerInterface", fmt.Sprint(t.IPv4PerENI)),
			T("ipv6Supported", "true"),
			T("enaSupport", "required"),
			T("efaSupported", "false"),
			T("encryptionInTransitSupported", "false"),
		),
		E("placementGroupInfo", Strs("supportedStrategies", "cluster", "partition", "spread")),
		T("hibernationSupported", "false"),
		T("burstablePerformanceSupported", burst),
		T("dedicatedHostsSupported", "true"),
		T("autoRecoverySupported", "true"),
		Strs("supportedBootModes", "legacy-bios", "uefi"),
		T("nitroEnclavesSupport", "unsupported"),
		T("nitroTpmSupport", "unsupported"),
	)
}
```

- [ ] **Step 4: Write the handler**

One function per operation, registered in a table. Parameter names (`LaunchTemplateConfigs.N.Overrides.M.InstanceType`, `TagSpecification.N.Tag.M.Key`, `Filter.N.Value.M`, and so on) are the query-protocol names the AWS SDK sends. The gate's trace (Task 16) confirms them against real Karpenter; if the trace shows a name differs, fix it here and add the case to `handler_test.go`.

`internal/ec2sim/handler.go`:

```go
package ec2sim

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/catalog"
)

// Handler serves the EC2 query protocol over a Store. It sits behind the
// aws-gateway, which has already verified SigV4 and routed by service.
type Handler struct {
	store *Store
}

// NewHandler returns the EC2 handler for a store.
func NewHandler(s *Store) *Handler { return &Handler{store: s} }

const maxBody = 1 << 20

// op is one operation: it reads the parsed request and answers with the
// elements that follow <requestId> in the response.
type op func(h *Handler, root *awsapi.Node) ([]X, error)

var operations = map[string]op{
	"CreateLaunchTemplate":          (*Handler).createLaunchTemplate,
	"DeleteLaunchTemplate":          (*Handler).deleteLaunchTemplate,
	"DescribeLaunchTemplates":       (*Handler).describeLaunchTemplates,
	"CreateFleet":                   (*Handler).createFleet,
	"RunInstances":                  (*Handler).runInstances,
	"TerminateInstances":            (*Handler).terminateInstances,
	"DescribeInstances":             (*Handler).describeInstances,
	"CreateTags":                    (*Handler).createTags,
	"DescribeInstanceTypes":         (*Handler).describeInstanceTypes,
	"DescribeInstanceTypeOfferings": (*Handler).describeInstanceTypeOfferings,
	"DescribeAvailabilityZones":     (*Handler).describeAvailabilityZones,
	"DescribeImages":                (*Handler).describeImages,
	"DescribeSubnets":               (*Handler).describeSubnets,
	"DescribeSecurityGroups":        (*Handler).describeSecurityGroups,
	"DescribeSpotPriceHistory":      (*Handler).describeSpotPriceHistory,
}

// Operations lists the operation names this handler serves (used by a test that
// checks ops.yaml and the handler agree).
func Operations() []string {
	out := make([]string, 0, len(operations))
	for k := range operations {
		out = append(out, k)
	}
	return out
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := awsapi.ReadBody(r, maxBody)
	if err != nil {
		awsapi.WriteError(w, awsapi.StyleEC2, http.StatusBadRequest, "InvalidParameterValue", err.Error())
		return
	}
	form, err := awsapi.Form(r, body)
	if err != nil {
		awsapi.WriteError(w, awsapi.StyleEC2, http.StatusBadRequest, "MalformedQueryString", err.Error())
		return
	}
	action := form.Get("Action")
	fn, ok := operations[action]
	if !ok {
		awsapi.WriteError(w, awsapi.StyleEC2, http.StatusBadRequest, "InvalidAction",
			fmt.Sprintf("The action %s is not valid for this web service.", action))
		return
	}
	kids, err := fn(h, awsapi.Nest(form))
	if err != nil {
		var ae *APIError
		if !errors.As(err, &ae) {
			ae = &APIError{Code: "InternalError", Message: err.Error()}
		}
		status := http.StatusBadRequest
		if ae.Code == "InsufficientInstanceCapacity" || ae.Code == "InternalError" {
			status = http.StatusInternalServerError // EC2 returns 500 for capacity errors
		}
		awsapi.WriteError(w, awsapi.StyleEC2, status, ae.Code, ae.Message)
		return
	}
	respond(w, action, kids...)
}

// ---- launch templates ----

func (h *Handler) createLaunchTemplate(root *awsapi.Node) ([]X, error) {
	t, err := h.store.CreateTemplate(root.Str("LaunchTemplateName"), root.Str("LaunchTemplateData", "ImageId"),
		ParseTagSpecs(root, "launch-template"))
	if err != nil {
		return nil, err
	}
	return []X{templateEl("launchTemplate", t)}, nil
}

func (h *Handler) deleteLaunchTemplate(root *awsapi.Node) ([]X, error) {
	key := root.Str("LaunchTemplateName")
	if key == "" {
		key = root.Str("LaunchTemplateId")
	}
	t, err := h.store.DeleteTemplate(key)
	if err != nil {
		return nil, err
	}
	return []X{templateEl("launchTemplate", t)}, nil
}

func (h *Handler) describeLaunchTemplates(root *awsapi.Node) ([]X, error) {
	want := append(root.Get("LaunchTemplateName").Strings(), root.Get("LaunchTemplateId").Strings()...)
	for _, k := range want {
		if _, ok := h.store.Template(k); !ok {
			return nil, apiErr("InvalidLaunchTemplateName.NotFoundException",
				"At least one of the launch templates specified in the request does not exist.")
		}
	}
	filters := ParseFilters(root)
	var items []X
	for _, t := range h.store.Templates() {
		if len(want) > 0 && !contains(want, t.Name) && !contains(want, t.ID) {
			continue
		}
		t := t
		if !Match(filters, func(n string) []string {
			if v, ok := TagAttr(t.Tags, n); ok {
				return v
			}
			switch n {
			case "launch-template-name":
				return []string{t.Name}
			case "launch-template-id":
				return []string{t.ID}
			}
			return nil
		}) {
			continue
		}
		items = append(items, Item(
			T("launchTemplateId", t.ID), T("launchTemplateName", t.Name), T("createTime", ts(t.CreatedAt)),
			T("createdBy", "arn:aws:iam::"+accountID+":root"), T("defaultVersionNumber", "1"),
			T("latestVersionNumber", "1"), tagSet(t.Tags)))
	}
	return []X{Set("launchTemplates", items...)}, nil
}

// ---- launching and terminating ----

func (h *Handler) createFleet(root *awsapi.Node) ([]X, error) {
	if t := root.Str("Type"); t != "" && t != "instant" {
		return nil, apiErr("InvalidParameterValue", "only fleets of Type=instant are supported, got %q", t)
	}
	count, err := strconv.Atoi(root.Str("TargetCapacitySpecification", "TotalTargetCapacity"))
	if err != nil {
		return nil, apiErr("MissingParameter", "TargetCapacitySpecification.TotalTargetCapacity is required")
	}
	req := FleetRequest{
		Market: root.Str("TargetCapacitySpecification", "DefaultTargetCapacityType"),
		Count:  count,
		Tags:   ParseTagSpecs(root, "instance"),
	}
	for _, cfg := range root.Get("LaunchTemplateConfigs").List() {
		spec := cfg.Get("LaunchTemplateSpecification")
		tpl := spec.Str("LaunchTemplateName")
		if tpl == "" {
			tpl = spec.Str("LaunchTemplateId")
		}
		for _, o := range cfg.Get("Overrides").List() {
			prio, _ := strconv.ParseFloat(o.Str("Priority"), 64)
			req.Overrides = append(req.Overrides, FleetOverride{
				Type: o.Str("InstanceType"), AZ: o.Str("AvailabilityZone"), SubnetID: o.Str("SubnetId"),
				Priority: prio, Template: tpl,
			})
		}
	}
	res, err := h.store.CreateFleet(req)
	if err != nil {
		return nil, err
	}
	lifecycle := func(m string) string {
		if m == "spot" {
			return "spot"
		}
		return "on-demand"
	}
	overrides := func(o FleetOverride, typ, az string) X {
		kids := []X{T("instanceType", typ), T("availabilityZone", az)}
		if o.SubnetID != "" {
			kids = append(kids, T("subnetId", o.SubnetID))
		}
		return E("overrides", kids...)
	}
	spec := func(id string) X {
		return E("launchTemplateSpecification", T("launchTemplateId", id), T("version", "1"))
	}
	var errItems, instItems []X
	for _, e := range res.Errors {
		errItems = append(errItems, Item(
			E("launchTemplateAndOverrides", spec(e.TemplateID), overrides(e.Override, e.Override.Type, e.Override.AZ)),
			T("lifecycle", lifecycle(e.Market)), T("errorCode", e.Code), T("errorMessage", e.Message)))
	}
	for _, in := range res.Instances {
		ids := make([]X, len(in.IDs))
		for i, id := range in.IDs {
			ids[i] = T("item", id)
		}
		instItems = append(instItems, Item(
			E("launchTemplateAndOverrides", spec(in.TemplateID), overrides(FleetOverride{}, in.Type, in.AZ)),
			T("lifecycle", lifecycle(in.Market)), Set("instanceIds", ids...),
			T("instanceType", in.Type), T("platform", "")))
	}
	return []X{T("fleetId", res.FleetID), Set("errorSet", errItems...), Set("fleetInstanceSet", instItems...)}, nil
}

func (h *Handler) runInstances(root *awsapi.Node) ([]X, error) {
	count, err := strconv.Atoi(root.Str("MaxCount"))
	if err != nil {
		return nil, apiErr("MissingParameter", "The request must contain the parameter MaxCount")
	}
	req := LaunchRequest{
		Type: root.Str("InstanceType"), AZ: root.Str("Placement", "AvailabilityZone"), SubnetID: root.Str("SubnetId"),
		ImageID: root.Str("ImageId"), Count: count, Tags: ParseTagSpecs(root, "instance"),
		SecurityGroupIDs: root.Get("SecurityGroupId").Strings(),
	}
	if root.Str("InstanceMarketOptions", "MarketType") == "spot" {
		req.Market = "spot"
	}
	if name := firstNonEmpty(root.Str("LaunchTemplate", "LaunchTemplateName"), root.Str("LaunchTemplate", "LaunchTemplateId")); name != "" {
		tpl, ok := h.store.Template(name)
		if !ok {
			return nil, apiErr("InvalidLaunchTemplateName.NotFoundException",
				"The specified launch template, with template name %s, does not exist.", name)
		}
		req.LaunchTemplateID = tpl.ID
		if req.ImageID == "" {
			req.ImageID = tpl.ImageID
		}
	}
	views, err := h.store.Launch(req)
	if err != nil {
		return nil, err
	}
	items := make([]X, len(views))
	for i, v := range views {
		items[i] = instanceItem(v)
	}
	return []X{T("reservationId", "r-"+views[0].ID[2:]), T("ownerId", accountID), Set("groupSet"), Set("instancesSet", items...)}, nil
}

func (h *Handler) terminateInstances(root *awsapi.Node) ([]X, error) {
	trs, err := h.store.Terminate(root.Get("InstanceId").Strings())
	if err != nil {
		return nil, err
	}
	items := make([]X, len(trs))
	for i, t := range trs {
		items[i] = Item(T("instanceId", t.ID), stateEl("currentState", t.Current), stateEl("previousState", t.Previous))
	}
	return []X{Set("instancesSet", items...)}, nil
}

func (h *Handler) createTags(root *awsapi.Node) ([]X, error) {
	tags := map[string]string{}
	for _, t := range root.Get("Tag").List() {
		tags[t.Str("Key")] = t.Str("Value")
	}
	if err := h.store.Tag(root.Get("ResourceId").Strings(), tags); err != nil {
		return nil, err
	}
	return []X{T("return", "true")}, nil
}

// ---- describe ----

func (h *Handler) describeInstances(root *awsapi.Node) ([]X, error) {
	ids := root.Get("InstanceId").Strings()
	for _, id := range ids {
		if _, ok := h.store.Instance(id); !ok {
			return nil, apiErr("InvalidInstanceID.NotFound", "The instance ID '%s' does not exist", id)
		}
	}
	filters := ParseFilters(root)
	var items []X
	for _, v := range h.store.Instances() {
		v := v
		if len(ids) > 0 && !contains(ids, v.ID) {
			continue
		}
		if !Match(filters, func(n string) []string {
			if vals, ok := TagAttr(v.Tags, n); ok {
				return vals
			}
			switch n {
			case "instance-id":
				return []string{v.ID}
			case "instance-state-name":
				return []string{string(v.State)}
			case "instance-state-code":
				return []string{stateCode(v.State)}
			case "instance-type":
				return []string{v.Type}
			case "availability-zone":
				return []string{v.AZ}
			case "subnet-id":
				return []string{v.SubnetID}
			case "vpc-id":
				return []string{VPCID}
			case "image-id":
				return []string{v.ImageID}
			case "private-ip-address":
				return []string{v.PrivateIP}
			case "instance-lifecycle":
				if v.Market == "spot" {
					return []string{"spot"}
				}
				return []string{"normal"}
			}
			return nil
		}) {
			continue
		}
		items = append(items, Item(T("reservationId", "r-"+v.ID[2:]), T("ownerId", accountID), Set("groupSet"),
			Set("instancesSet", instanceItem(v))))
	}
	return []X{Set("reservationSet", items...)}, nil
}

func (h *Handler) describeInstanceTypes(root *awsapi.Node) ([]X, error) {
	names := root.Get("InstanceType").Strings()
	for _, n := range names {
		if _, ok := catalog.Lookup(n); !ok {
			return nil, apiErr("InvalidInstanceType", "The following supplied instance types do not exist: [%s]", n)
		}
	}
	filters := ParseFilters(root)
	var items []X
	for _, t := range catalog.Types {
		t := t
		if len(names) > 0 && !contains(names, t.Name) {
			continue
		}
		if !Match(filters, func(n string) []string {
			switch n {
			case "instance-type":
				return []string{t.Name}
			case "current-generation":
				return []string{"true"}
			case "bare-metal":
				return []string{"false"}
			case "hypervisor":
				return []string{"nitro"}
			case "supported-virtualization-type":
				return []string{"hvm"}
			case "supported-usage-class":
				return []string{"on-demand", "spot"}
			case "supported-root-device-type":
				return []string{"ebs"}
			case "processor-info.supported-architecture":
				return []string{t.Arch}
			case "vcpu-info.default-vcpus":
				return []string{strconv.Itoa(t.VCPU)}
			case "memory-info.size-in-mib":
				return []string{strconv.Itoa(t.MemoryMiB)}
			case "network-info.maximum-network-interfaces":
				return []string{strconv.Itoa(t.MaxENIs)}
			}
			return nil
		}) {
			continue
		}
		items = append(items, typeItem(t))
	}
	return []X{Set("instanceTypeSet", items...)}, nil
}

func (h *Handler) describeInstanceTypeOfferings(root *awsapi.Node) ([]X, error) {
	locType := root.Str("LocationType")
	if locType == "" {
		locType = "region"
	}
	var locations []string
	switch locType {
	case "region":
		locations = []string{catalog.Region}
	case "availability-zone":
		for _, z := range catalog.AZs {
			locations = append(locations, z.Name)
		}
	case "availability-zone-id":
		for _, z := range catalog.AZs {
			locations = append(locations, z.ID)
		}
	default:
		return nil, apiErr("InvalidParameterValue", "Value (%s) for parameter locationType is invalid.", locType)
	}
	filters := ParseFilters(root)
	var items []X
	for _, t := range catalog.Types {
		for _, loc := range locations {
			t, loc := t, loc
			if !Match(filters, func(n string) []string {
				switch n {
				case "instance-type":
					return []string{t.Name}
				case "location":
					return []string{loc}
				}
				return nil
			}) {
				continue
			}
			items = append(items, Item(T("instanceType", t.Name), T("locationType", locType), T("location", loc)))
		}
	}
	return []X{Set("instanceTypeOfferingSet", items...)}, nil
}

func (h *Handler) describeAvailabilityZones(root *awsapi.Node) ([]X, error) {
	names := root.Get("ZoneName").Strings()
	ids := root.Get("ZoneId").Strings()
	filters := ParseFilters(root)
	var items []X
	for _, z := range catalog.AZs {
		z := z
		if (len(names) > 0 && !contains(names, z.Name)) || (len(ids) > 0 && !contains(ids, z.ID)) {
			continue
		}
		if !Match(filters, func(n string) []string {
			switch n {
			case "zone-name":
				return []string{z.Name}
			case "zone-id":
				return []string{z.ID}
			case "state":
				return []string{"available"}
			case "region-name":
				return []string{catalog.Region}
			case "zone-type":
				return []string{"availability-zone"}
			}
			return nil
		}) {
			continue
		}
		items = append(items, Item(T("zoneName", z.Name), T("zoneId", z.ID), T("zoneState", "available"),
			T("regionName", catalog.Region), T("zoneType", "availability-zone"),
			T("optInStatus", "opt-in-not-required"), T("groupName", catalog.Region), T("networkBorderGroup", catalog.Region),
			Set("messageSet")))
	}
	return []X{Set("availabilityZoneInfo", items...)}, nil
}

func (h *Handler) describeImages(root *awsapi.Node) ([]X, error) {
	ids := root.Get("ImageId").Strings()
	known := h.store.Images()
	for _, id := range ids {
		found := false
		for _, im := range known {
			found = found || im.ID == id
		}
		if !found {
			return nil, apiErr("InvalidAMIID.NotFound", "The image id '[%s]' does not exist", id)
		}
	}
	filters := ParseFilters(root)
	var items []X
	for _, im := range known {
		im := im
		if len(ids) > 0 && !contains(ids, im.ID) {
			continue
		}
		if !Match(filters, func(n string) []string {
			switch n {
			case "image-id":
				return []string{im.ID}
			case "name":
				return []string{im.Name}
			case "architecture":
				return []string{im.Architecture}
			case "state":
				return []string{"available"}
			case "root-device-type":
				return []string{"ebs"}
			case "virtualization-type":
				return []string{"hvm"}
			case "owner-id":
				return []string{accountID}
			}
			return nil
		}) {
			continue
		}
		items = append(items, Item(
			T("imageId", im.ID), T("imageLocation", accountID+"/"+im.Name), T("imageState", "available"),
			T("imageOwnerId", accountID), T("creationDate", im.CreationDate), T("isPublic", "true"),
			T("architecture", im.Architecture), T("imageType", "machine"), T("name", im.Name),
			T("description", im.Name), T("rootDeviceType", "ebs"), T("rootDeviceName", "/dev/xvda"),
			Set("blockDeviceMapping", Item(T("deviceName", "/dev/xvda"),
				E("ebs", T("volumeSize", "20"), T("volumeType", "gp3"), T("deleteOnTermination", "true")))),
			T("virtualizationType", "hvm"), T("hypervisor", "xen"), T("enaSupport", "true"), tagSet(nil)))
	}
	return []X{Set("imagesSet", items...)}, nil
}

func (h *Handler) describeSubnets(root *awsapi.Node) ([]X, error) {
	ids := root.Get("SubnetId").Strings()
	subnets := h.store.Subnets()
	for _, id := range ids {
		found := false
		for _, s := range subnets {
			found = found || s.ID == id
		}
		if !found {
			return nil, apiErr("InvalidSubnetID.NotFound", "The subnet ID '%s' does not exist", id)
		}
	}
	filters := ParseFilters(root)
	var items []X
	for _, s := range subnets {
		s := s
		if len(ids) > 0 && !contains(ids, s.ID) {
			continue
		}
		if !Match(filters, func(n string) []string {
			if v, ok := TagAttr(s.Tags, n); ok {
				return v
			}
			switch n {
			case "subnet-id":
				return []string{s.ID}
			case "vpc-id":
				return []string{VPCID}
			case "availability-zone":
				return []string{s.AZ}
			case "availability-zone-id":
				return []string{s.AZID}
			case "cidr-block", "cidrBlock":
				return []string{s.CIDR}
			case "state":
				return []string{"available"}
			}
			return nil
		}) {
			continue
		}
		items = append(items, Item(T("subnetId", s.ID), T("state", "available"), T("vpcId", VPCID),
			T("cidrBlock", s.CIDR), T("availableIpAddressCount", "240"), T("availabilityZone", s.AZ),
			T("availabilityZoneId", s.AZID), T("defaultForAz", "false"), T("mapPublicIpOnLaunch", "false"),
			T("ownerId", accountID), tagSet(s.Tags)))
	}
	return []X{Set("subnetSet", items...)}, nil
}

func (h *Handler) describeSecurityGroups(root *awsapi.Node) ([]X, error) {
	ids := root.Get("GroupId").Strings()
	names := root.Get("GroupName").Strings()
	sgs := h.store.SecurityGroups()
	for _, id := range ids {
		found := false
		for _, g := range sgs {
			found = found || g.ID == id
		}
		if !found {
			return nil, apiErr("InvalidGroup.NotFound", "The security group '%s' does not exist", id)
		}
	}
	filters := ParseFilters(root)
	var items []X
	for _, g := range sgs {
		g := g
		if (len(ids) > 0 && !contains(ids, g.ID)) || (len(names) > 0 && !contains(names, g.Name)) {
			continue
		}
		if !Match(filters, func(n string) []string {
			if v, ok := TagAttr(g.Tags, n); ok {
				return v
			}
			switch n {
			case "group-id":
				return []string{g.ID}
			case "group-name":
				return []string{g.Name}
			case "vpc-id":
				return []string{VPCID}
			}
			return nil
		}) {
			continue
		}
		items = append(items, Item(T("ownerId", accountID), T("groupId", g.ID), T("groupName", g.Name),
			T("groupDescription", g.Name), T("vpcId", VPCID), Set("ipPermissions"), Set("ipPermissionsEgress"), tagSet(g.Tags)))
	}
	return []X{Set("securityGroupInfo", items...)}, nil
}

func (h *Handler) describeSpotPriceHistory(root *awsapi.Node) ([]X, error) {
	types := root.Get("InstanceType").Strings()
	products := root.Get("ProductDescription").Strings()
	az := root.Str("AvailabilityZone")
	if len(products) > 0 && !contains(products, "Linux/UNIX") {
		return []X{Set("spotPriceHistorySet"), T("nextToken", "")}, nil // only Linux/UNIX is modeled
	}
	now := h.store.Now()
	var items []X
	for _, t := range catalog.Types {
		if len(types) > 0 && !contains(types, t.Name) {
			continue
		}
		for _, z := range catalog.AZs {
			if az != "" && z.Name != az {
				continue
			}
			items = append(items, Item(T("instanceType", t.Name), T("productDescription", "Linux/UNIX"),
				T("spotPrice", strconv.FormatFloat(catalog.SpotPrice(t, z.Name), 'f', 4, 64)),
				T("timestamp", ts(now)), T("availabilityZone", z.Name)))
		}
	}
	return []X{Set("spotPriceHistorySet", items...), T("nextToken", "")}, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
```

- [ ] **Step 5: Write the binary**

`cmd/ec2-sim/main.go`:

```go
// Command ec2-sim serves a simulated EC2 API. It exists only if S0 found that
// Floci's EC2 cannot give Karpenter what it reads. It trusts the aws-gateway in
// front of it to have verified SigV4, so it must never be published directly.
// Spot interruptions are not its job: simctl injects them through public AWS
// calls, so they work with any EC2 backend.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tazzledazzle/meridian/internal/ec2sim"
	"github.com/tazzledazzle/meridian/internal/gateway"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:4570", "address for the EC2 API (behind the gateway)")
	allowAny := flag.Bool("allow-any-interface", false, "allow non-loopback addresses (containers only; never publish the EC2 port)")
	pending := flag.Duration("pending-for", 3*time.Second, "time an instance stays pending before it is running")
	shutting := flag.Duration("shutting-down-for", 2*time.Second, "time an instance stays shutting-down before it is terminated")
	flag.Parse()

	if err := gateway.CheckBind(*listen, *allowAny); err != nil {
		log.Fatal(err)
	}
	store := ec2sim.NewStore(ec2sim.Options{PendingFor: *pending, ShuttingDownFor: *shutting})
	srv := &http.Server{Addr: *listen, Handler: ec2sim.NewHandler(store), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("ec2-sim listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `gofmt -l internal cmd && go vet ./internal/ec2sim/ ./cmd/ec2-sim/ && go test -race ./internal/ec2sim/ -v`
Expected: no gofmt output; all tests PASS.

- [ ] **Step 7: Prove the simulator satisfies the S0 contract**

The same probe that judged Floci must pass against `ec2-sim`. That shows the probe is not stricter than a spec-conforming backend, and that the fallback really does what Karpenter needs.

If `internal/s0probe/fakemisc_test.go` does not exist yet (Task 12 or Task 13 creates it, whichever runs first), create it:

`internal/s0probe/fakemisc_test.go`:

```go
package s0probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMisc is a tiny in-memory STS/SSM/IAM/SQS, just enough to satisfy the
// probe. Together with the ec2-sim, pricing and EKS handlers it forms a
// reference backend: if the probe passes here, a failure against Floci means
// Floci differs from what the probe (and so Karpenter) expects, not that the
// probe is wrong.
type fakeMisc struct {
	mu       sync.Mutex
	params   map[string]string
	profiles map[string][]string // profile name -> role names
	queues   map[string][]string // queue name -> bodies
}

func (f *fakeMisc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		f.json(w, target, body)
		return
	}
	form, _ := url.ParseQuery(string(body))
	f.query(w, form)
}

func (f *fakeMisc) reply(w http.ResponseWriter, ct, s string) {
	w.Header().Set("Content-Type", ct)
	io.WriteString(w, s)
}

func (f *fakeMisc) query(w http.ResponseWriter, q url.Values) {
	name := q.Get("InstanceProfileName")
	switch q.Get("Action") {
	case "GetCallerIdentity":
		f.reply(w, "text/xml", `<GetCallerIdentityResponse><GetCallerIdentityResult><Account>000000000000</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`)
	case "CreateRole", "TagInstanceProfile", "RemoveRoleFromInstanceProfile", "DeleteInstanceProfile", "CreateInstanceProfile":
		switch q.Get("Action") {
		case "CreateInstanceProfile":
			f.profiles[name] = nil
		case "RemoveRoleFromInstanceProfile":
			f.profiles[name] = nil
		case "DeleteInstanceProfile":
			delete(f.profiles, name)
		}
		f.reply(w, "text/xml", `<R><Result/></R>`)
	case "AddRoleToInstanceProfile":
		f.profiles[name] = append(f.profiles[name], q.Get("RoleName"))
		f.reply(w, "text/xml", `<R/>`)
	case "GetInstanceProfile":
		var roles strings.Builder
		for _, r := range f.profiles[name] {
			fmt.Fprintf(&roles, "<member><RoleName>%s</RoleName></member>", r)
		}
		f.reply(w, "text/xml", fmt.Sprintf(`<GetInstanceProfileResponse><GetInstanceProfileResult><InstanceProfile><InstanceProfileName>%s</InstanceProfileName><Arn>arn:aws:iam::000000000000:instance-profile/%s</Arn><Roles>%s</Roles></InstanceProfile></GetInstanceProfileResult></GetInstanceProfileResponse>`, name, name, roles.String()))
	case "ListInstanceProfiles":
		var ps strings.Builder
		for p := range f.profiles {
			fmt.Fprintf(&ps, "<member><InstanceProfileName>%s</InstanceProfileName></member>", p)
		}
		f.reply(w, "text/xml", `<ListInstanceProfilesResponse><ListInstanceProfilesResult><InstanceProfiles>`+ps.String()+`</InstanceProfiles></ListInstanceProfilesResult></ListInstanceProfilesResponse>`)
	default:
		http.Error(w, "unsupported "+q.Get("Action"), http.StatusBadRequest)
	}
}

func (f *fakeMisc) json(w http.ResponseWriter, target string, body []byte) {
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	op := target[strings.Index(target, ".")+1:]
	out := map[string]any{}
	switch op {
	case "PutParameter":
		f.params[str("Name")] = str("Value")
	case "GetParameter":
		v, ok := f.params[str("Name")]
		if !ok {
			http.Error(w, `{"__type":"ParameterNotFound"}`, http.StatusBadRequest)
			return
		}
		out["Parameter"] = map[string]any{"Name": str("Name"), "Value": v}
	case "CreateQueue", "GetQueueUrl":
		if op == "CreateQueue" {
			f.queues[str("QueueName")] = nil
		}
		out["QueueUrl"] = "http://sqs.test/000000000000/" + str("QueueName")
	case "SendMessage":
		n := str("QueueUrl")[strings.LastIndex(str("QueueUrl"), "/")+1:]
		f.queues[n] = append(f.queues[n], str("MessageBody"))
	case "ReceiveMessage":
		n := str("QueueUrl")[strings.LastIndex(str("QueueUrl"), "/")+1:]
		if len(f.queues[n]) > 0 {
			out["Messages"] = []any{map[string]any{"Body": f.queues[n][0], "ReceiptHandle": "rh-1"}}
		}
	case "DeleteMessage":
	default:
		http.Error(w, "unsupported "+op, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	_ = json.NewEncoder(w).Encode(out)
}

var scopeRE = regexp.MustCompile(`Credential=[^/]+/[^/]+/[^/]+/([^/]+)/aws4_request`)

// refConfig is the probe configuration the reference-backend tests share.
func refConfig() Config {
	return Config{Region: "us-west-2", Access: "AKIAEXAMPLE", Secret: "secret", Cluster: "meridian-g0",
		AMIParam: "/aws/service/eks/optimized-ami/1.32/amazon-linux-2023/x86_64/standard/recommended/image_id",
		Wait:     5 * time.Second}
}

// newMisc returns the fake STS/SSM/IAM/SQS with the AMI parameter pre-seeded.
func newMisc(ami string) *fakeMisc {
	return &fakeMisc{params: map[string]string{refConfig().AMIParam: ami}, profiles: map[string][]string{}, queues: map[string][]string{}}
}

// refServer routes by the SigV4 service in the Authorization header: services
// in routes go to their handler, everything else to the fake misc services.
func refServer(t *testing.T, routes map[string]http.Handler, misc http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := scopeRE.FindStringSubmatch(r.Header.Get("Authorization"))
		if m == nil {
			http.Error(w, "unsigned", http.StatusForbidden)
			return
		}
		if h, ok := routes[m[1]]; ok {
			h.ServeHTTP(w, r)
			return
		}
		misc.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// expectAllPass fails the test for every check that did not pass.
func expectAllPass(t *testing.T, cfg Config, minPass int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	results := Run(ctx, cfg, Checks())
	if s := Summarize(results); s.Pass < minPass {
		t.Errorf("only %d checks passed, want at least %d", s.Pass, minPass)
	}
	for _, r := range results {
		if r.Status != "pass" {
			t.Errorf("%s: %s: %s", r.Name, r.Status, r.Detail)
		}
	}
}
```

`internal/s0probe/selftest_ec2_test.go`:

```go
package s0probe

import (
	"net/http"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/ec2sim"
)

// TestProbeEC2GroupPassesAgainstEC2Sim runs the SSM, IAM and EC2 checks against
// the fallback simulator. It proves two things at once: the probe is not
// stricter than a spec-conforming backend, and ec2-sim satisfies the contract
// Karpenter needs.
func TestProbeEC2GroupPassesAgainstEC2Sim(t *testing.T) {
	cfg := refConfig()
	store := ec2sim.NewStore(ec2sim.Options{PendingFor: 10 * time.Millisecond, ShuttingDownFor: 10 * time.Millisecond})
	cfg.Endpoint = refServer(t, map[string]http.Handler{"ec2": ec2sim.NewHandler(store)}, newMisc(ec2sim.DefaultAMI))
	cfg.Groups = []string{"ssm", "iam", "ec2"}
	expectAllPass(t, cfg, 25)
}
```

Run: `gofmt -l internal && go test -race ./internal/s0probe/ -run ProbeEC2 -v`
Expected: PASS.

- [ ] **Step 8: Start it through the gateway and probe it**

The Compose service and the gateway's `-backend=ec2-sim=...` flag already exist (Task 7); the profile starts because `ops.yaml` now routes `ec2` operations to `ec2-sim`.

Run: `make s0-down; make s0-up && S0_LABEL=ec2-sim make s0-probe`
Expected: every `ec2:*` check that was moved passes in `docs/s0/report.md`. If one fails, fix `render.go` or the handler, adding a test first.

- [ ] **Step 9: Commit**

```bash
git add internal/ec2sim cmd/ec2-sim internal/s0probe docs/s0
git commit -m "feat: add EC2 wire layer and ec2-sim binary (S0 fallback)"
```

### Task 13: [Fallback] `pricing-facade` and the `eks-facade` stub

> **Conditional task.** Execute this only if `docs/s0/decision.md` (Task 8) moved operations to `pricing-facade` or `eks-facade` (build only the one that was chosen; they are independent). If not, skip it and record the skip in `docs/s0/decision.md`.



Two small services in one task because each is a single handler with its own tests. Build whichever the S0 decision names; both read the same `catalog` data.

**Files:**
- Create: `internal/pricing/handler.go`, `cmd/pricing-facade/main.go`
- Create: `internal/ekstub/handler.go`, `cmd/eks-facade/main.go`
- Test: `internal/pricing/handler_test.go`, `internal/ekstub/handler_test.go`
- Create: `internal/s0probe/selftest_facades_test.go` (and `fakemisc_test.go` if absent)

**Interfaces:**
- Consumes: `catalog.Types`, `catalog.Region` (Task 9); `awsapi.TargetOp`, `ReadBody`, `WriteJSON`, `WriteError` (Task 3); `gateway.CheckBind`.
- Produces: `pricing.NewHandler() *Handler` (JSON-RPC target `AWSPriceListService.GetProducts`); `ekstub.Config{Name, Version, Endpoint, ServiceCIDR, CreatedAt}`, `ekstub.NewHandler(Config)`, `ekstub.MinorOf(k3sVersion) (string, error)`; binaries `pricing-facade` and `eks-facade`; the Compose services of the same names (declared in Task 7) start when `ops.yaml` routes `pricing` or `eks` operations to them.

**Acceptance Criteria:**
- `GetProducts` with Karpenter's filter set returns one `PriceList` entry per catalog instance type; each entry is a JSON document inside a string, with `product.attributes.instanceType` and `terms.OnDemand.*.priceDimensions.*.pricePerUnit.USD`.
- Prices equal `catalog.Type.OnDemand`, the same data `ec2-sim` uses for spot prices if it is built, so the two services cannot drift apart.
- Pagination returns every product exactly once; filters on unknown fields match nothing; only `TERM_MATCH` and service `AmazonEC2` are accepted, and anything else is an explicit error, not an empty success.
- `CreateCluster` for the configured cluster is `ResourceInUseException` (409), as EKS answers for a duplicate; any other name is `InvalidParameterException`.
- `DescribeCluster` for the configured cluster returns `version`, `endpoint`, `status: ACTIVE` and `kubernetesNetworkConfig.serviceIpv4Cidr`; any other cluster is `ResourceNotFoundException` (404).
- The EKS version reported is derived from the pinned `K3S_VERSION`, not typed in a second place.

- [ ] **Step 1: Write the failing tests**

`internal/pricing/handler_test.go`:

```go
package pricing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

func post(t *testing.T, target, body string) (int, []byte, http.Header) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Amz-Target", target)
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), rec.Header()
}

// karpenterQuery is the filter set Karpenter sends to list on-demand Linux prices.
const karpenterQuery = `{"ServiceCode":"AmazonEC2","FormatVersion":"aws_v1","Filters":[
 {"Type":"TERM_MATCH","Field":"regionCode","Value":"us-west-2"},
 {"Type":"TERM_MATCH","Field":"tenancy","Value":"Shared"},
 {"Type":"TERM_MATCH","Field":"preInstalledSw","Value":"NA"},
 {"Type":"TERM_MATCH","Field":"operatingSystem","Value":"Linux"},
 {"Type":"TERM_MATCH","Field":"capacitystatus","Value":"Used"},
 {"Type":"TERM_MATCH","Field":"marketoption","Value":"OnDemand"}]}`

func TestGetProductsReturnsPricesKarpenterCanParse(t *testing.T) {
	code, body, hdr := post(t, "AWSPriceListService.GetProducts", karpenterQuery)
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "application/x-amz-json-1.1") {
		t.Fatalf("code=%d type=%s body=%s", code, hdr.Get("Content-Type"), body)
	}
	var out struct {
		FormatVersion string   `json:"FormatVersion"`
		PriceList     []string `json:"PriceList"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.FormatVersion != "aws_v1" || len(out.PriceList) != len(catalog.Types) {
		t.Fatalf("format=%s entries=%d, want %d", out.FormatVersion, len(out.PriceList), len(catalog.Types))
	}
	// Every entry is itself a JSON document inside a string, as in the real API.
	prices := map[string]float64{}
	for _, entry := range out.PriceList {
		var doc struct {
			Product struct {
				Attributes map[string]string `json:"attributes"`
			} `json:"product"`
			Terms struct {
				OnDemand map[string]struct {
					PriceDimensions map[string]struct {
						PricePerUnit map[string]string `json:"pricePerUnit"`
					} `json:"priceDimensions"`
				} `json:"OnDemand"`
			} `json:"terms"`
		}
		if err := json.Unmarshal([]byte(entry), &doc); err != nil {
			t.Fatalf("entry is not JSON: %v", err)
		}
		for _, term := range doc.Terms.OnDemand {
			for _, dim := range term.PriceDimensions {
				p, err := strconv.ParseFloat(dim.PricePerUnit["USD"], 64)
				if err != nil {
					t.Fatal(err)
				}
				prices[doc.Product.Attributes["instanceType"]] = p
			}
		}
	}
	m5, _ := catalog.Lookup("m5.large")
	if prices["m5.large"] != m5.OnDemand {
		t.Fatalf("m5.large price = %v, want %v", prices["m5.large"], m5.OnDemand)
	}
}

func TestFiltersNarrowResultsAndUnknownFieldsMatchNothing(t *testing.T) {
	_, body, _ := post(t, "AWSPriceListService.GetProducts",
		`{"ServiceCode":"AmazonEC2","Filters":[{"Type":"TERM_MATCH","Field":"instanceType","Value":"c5.large"}]}`)
	var out struct{ PriceList []string }
	_ = json.Unmarshal(body, &out)
	if len(out.PriceList) != 1 || !strings.Contains(out.PriceList[0], `"instanceType":"c5.large"`) {
		t.Fatalf("got %v", out.PriceList)
	}
	_, body, _ = post(t, "AWSPriceListService.GetProducts",
		`{"ServiceCode":"AmazonEC2","Filters":[{"Type":"TERM_MATCH","Field":"nonsense","Value":"x"}]}`)
	out.PriceList = nil
	_ = json.Unmarshal(body, &out)
	if len(out.PriceList) != 0 {
		t.Fatalf("unknown field matched %d products", len(out.PriceList))
	}
}

func TestPaginationWalksEveryProductExactlyOnce(t *testing.T) {
	seen, token := map[string]bool{}, ""
	for page := 0; page < 20; page++ {
		req := `{"ServiceCode":"AmazonEC2","MaxResults":5` + func() string {
			if token != "" {
				return `,"NextToken":"` + token + `"`
			}
			return ""
		}() + `}`
		code, body, _ := post(t, "AWSPriceListService.GetProducts", req)
		if code != 200 {
			t.Fatalf("page %d: %d %s", page, code, body)
		}
		var out struct {
			PriceList []string `json:"PriceList"`
			NextToken string   `json:"NextToken"`
		}
		_ = json.Unmarshal(body, &out)
		for _, e := range out.PriceList {
			if seen[e] {
				t.Fatal("duplicate product across pages")
			}
			seen[e] = true
		}
		if out.NextToken == "" {
			if len(seen) != len(catalog.Types) {
				t.Fatalf("saw %d products, want %d", len(seen), len(catalog.Types))
			}
			return
		}
		token = out.NextToken
	}
	t.Fatal("pagination never finished")
}

func TestErrorsUseTheJSONEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, target, body, code string
	}{
		{"missing service", "AWSPriceListService.GetProducts", `{}`, "InvalidParameterException"},
		{"other service", "AWSPriceListService.GetProducts", `{"ServiceCode":"AmazonS3"}`, "InvalidParameterException"},
		{"bad filter type", "AWSPriceListService.GetProducts", `{"ServiceCode":"AmazonEC2","Filters":[{"Type":"ANY_OF","Field":"a","Value":"b"}]}`, "InvalidParameterException"},
		{"bad token", "AWSPriceListService.GetProducts", `{"ServiceCode":"AmazonEC2","NextToken":"zzz"}`, "InvalidNextTokenException"},
		{"bad json", "AWSPriceListService.GetProducts", `{`, "SerializationException"},
		{"other operation", "AWSPriceListService.DescribeServices", `{}`, "UnknownOperationException"},
	} {
		code, _, hdr := post(t, tc.target, tc.body)
		if code != 400 || hdr.Get("X-Amzn-Errortype") != tc.code {
			t.Errorf("%s: status=%d type=%q, want 400 %s", tc.name, code, hdr.Get("X-Amzn-Errortype"), tc.code)
		}
	}
}
```

`internal/ekstub/handler_test.go`:

```go
package ekstub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testHandler() http.Handler {
	return NewHandler(Config{Name: "meridian-g0", Version: "1.32", Endpoint: "https://meridian-g0.eks.sim",
		ServiceCIDR: "10.43.0.0/16", CreatedAt: time.Unix(1_700_000_000, 0),
		VPCID: "vpc-1", SubnetIDs: []string{"subnet-a", "subnet-b"}, SecurityGroupIDs: []string{"sg-1"}})
}

func get(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestDescribeClusterReturnsWhatKarpenterReads(t *testing.T) {
	rec := get(testHandler(), http.MethodGet, "/clusters/meridian-g0")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Cluster struct {
			Name     string `json:"name"`
			Version  string `json:"version"`
			Endpoint string `json:"endpoint"`
			Status   string `json:"status"`
			Network  struct {
				ServiceIpv4Cidr string `json:"serviceIpv4Cidr"`
			} `json:"kubernetesNetworkConfig"`
			VPC struct {
				Subnets []string `json:"subnetIds"`
			} `json:"resourcesVpcConfig"`
		} `json:"cluster"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	c := out.Cluster
	if c.Name != "meridian-g0" || c.Version != "1.32" || c.Endpoint != "https://meridian-g0.eks.sim" ||
		c.Status != "ACTIVE" || c.Network.ServiceIpv4Cidr != "10.43.0.0/16" || len(c.VPC.Subnets) != 2 {
		t.Fatalf("cluster = %+v", c)
	}
}

func TestUnknownClusterAndOtherRoutesFailLikeEKS(t *testing.T) {
	rec := get(testHandler(), http.MethodGet, "/clusters/other")
	if rec.Code != 404 || rec.Header().Get("X-Amzn-Errortype") != "ResourceNotFoundException" {
		t.Fatalf("unknown cluster: %d %s", rec.Code, rec.Header())
	}
	rec = get(testHandler(), http.MethodDelete, "/clusters/meridian-g0")
	if rec.Code != 404 || rec.Header().Get("X-Amzn-Errortype") != "UnknownOperationException" {
		t.Fatalf("other route: %d %s", rec.Code, rec.Header())
	}
}

func TestCreateClusterOfTheModeledClusterIsAConflict(t *testing.T) {
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		testHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/clusters", strings.NewReader(body)))
		return rec
	}
	if rec := post(`{"name":"meridian-g0"}`); rec.Code != 409 || rec.Header().Get("X-Amzn-Errortype") != "ResourceInUseException" {
		t.Fatalf("existing cluster: %d %s", rec.Code, rec.Header())
	}
	if rec := post(`{"name":"other"}`); rec.Code != 400 || rec.Header().Get("X-Amzn-Errortype") != "InvalidParameterException" {
		t.Fatalf("other cluster: %d %s", rec.Code, rec.Header())
	}
	if rec := post(`not json`); rec.Code != 400 {
		t.Fatalf("bad body: %d", rec.Code)
	}
}

func TestMinorOf(t *testing.T) {
	for in, want := range map[string]string{"v1.32.4+k3s1": "1.32", "1.31.0": "1.31", "v1.33": "1.33"} {
		got, err := MinorOf(in)
		if err != nil || got != want {
			t.Errorf("MinorOf(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := MinorOf("latest"); err == nil {
		t.Error("a non-version must fail")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/pricing/ ./internal/ekstub/ -v`
Expected: FAIL with `undefined: NewHandler`.

- [ ] **Step 3: Write the pricing handler and binary**

`internal/pricing/handler.go`:

```go
// Package pricing is the simulated AWS Price List service. It answers the one
// call Karpenter makes, GetProducts for on-demand EC2 prices, from the same
// catalog ec2-sim uses, so the two services never disagree.
package pricing

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/catalog"
)

const (
	maxBody        = 1 << 20
	defaultPage    = 100
	jsonVersion11  = "application/x-amz-json-1.1"
	offerTermCode  = "JRTCKXETXF" // the on-demand term code real price lists use
	rateCodeSuffix = "6YS6EN2CT7" // the hourly rate dimension suffix
)

// Handler serves AWSPriceListService over JSON-RPC.
type Handler struct{}

// NewHandler returns the pricing handler.
func NewHandler() *Handler { return &Handler{} }

type filter struct {
	Type  string `json:"Type"`
	Field string `json:"Field"`
	Value string `json:"Value"`
}

type getProductsIn struct {
	ServiceCode   string   `json:"ServiceCode"`
	Filters       []filter `json:"Filters"`
	FormatVersion string   `json:"FormatVersion"`
	NextToken     string   `json:"NextToken"`
	MaxResults    int      `json:"MaxResults"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	op, _ := awsapi.TargetOp(r)
	if op != "GetProducts" {
		awsapi.WriteError(w, awsapi.StyleJSON, http.StatusBadRequest, "UnknownOperationException", "operation "+op+" is not supported")
		return
	}
	body, err := awsapi.ReadBody(r, maxBody)
	if err != nil {
		awsapi.WriteError(w, awsapi.StyleJSON, http.StatusBadRequest, "InvalidParameterException", err.Error())
		return
	}
	var in getProductsIn
	if err := json.Unmarshal(body, &in); err != nil {
		awsapi.WriteError(w, awsapi.StyleJSON, http.StatusBadRequest, "SerializationException", err.Error())
		return
	}
	out, code, msg := getProducts(in)
	if code != "" {
		awsapi.WriteError(w, awsapi.StyleJSON, http.StatusBadRequest, code, msg)
		return
	}
	awsapi.WriteJSON(w, http.StatusOK, jsonVersion11, out)
}

// attributes are the product attributes TERM_MATCH filters can match.
func attributes(t catalog.Type) map[string]string {
	return map[string]string{
		"instanceType":    t.Name,
		"regionCode":      catalog.Region,
		"operatingSystem": "Linux",
		"tenancy":         "Shared",
		"preInstalledSw":  "NA",
		"capacitystatus":  "Used",
		"marketoption":    "OnDemand",
		"productFamily":   "Compute Instance",
		"vcpu":            strconv.Itoa(t.VCPU),
		"memory":          fmt.Sprintf("%g GiB", float64(t.MemoryMiB)/1024),
		"usagetype":       "USW2-BoxUsage:" + t.Name,
		"servicecode":     "AmazonEC2",
	}
}

func sku(t catalog.Type) string {
	sum := sha1.Sum([]byte("sim-sku/" + t.Name))
	return strings.ToUpper(fmt.Sprintf("%x", sum[:])[:16])
}

// priceListEntry renders one product as the JSON document AWS returns inside PriceList.
func priceListEntry(t catalog.Type) string {
	s := sku(t)
	doc := map[string]any{
		"product": map[string]any{
			"productFamily": "Compute Instance",
			"attributes":    attributes(t),
			"sku":           s,
		},
		"serviceCode": "AmazonEC2",
		"terms": map[string]any{
			"OnDemand": map[string]any{
				s + "." + offerTermCode: map[string]any{
					"offerTermCode": offerTermCode,
					"sku":           s,
					"priceDimensions": map[string]any{
						s + "." + offerTermCode + "." + rateCodeSuffix: map[string]any{
							"unit":         "Hrs",
							"rateCode":     s + "." + offerTermCode + "." + rateCodeSuffix,
							"description":  "$" + strconv.FormatFloat(t.OnDemand, 'f', -1, 64) + " per On Demand Linux " + t.Name + " Instance Hour",
							"pricePerUnit": map[string]string{"USD": fmt.Sprintf("%.10f", t.OnDemand)},
						},
					},
				},
			},
		},
		"version":         "20260101000000",
		"publicationDate": "2026-01-01T00:00:00Z",
	}
	b, _ := json.Marshal(doc) // plain maps and strings cannot fail to marshal
	return string(b)
}

type getProductsOut struct {
	FormatVersion string   `json:"FormatVersion"`
	PriceList     []string `json:"PriceList"`
	NextToken     string   `json:"NextToken,omitempty"`
}

// getProducts is separate from the HTTP code so it can be tested directly.
// A non-empty code means an error response.
func getProducts(in getProductsIn) (out getProductsOut, code, msg string) {
	if in.ServiceCode == "" {
		return out, "InvalidParameterException", "ServiceCode is required"
	}
	if in.ServiceCode != "AmazonEC2" {
		return out, "InvalidParameterException", "Service code " + in.ServiceCode + " is not available in this simulator; only AmazonEC2 is modeled"
	}
	for _, f := range in.Filters {
		if f.Type != "TERM_MATCH" {
			return out, "InvalidParameterException", "unsupported filter type " + f.Type + "; only TERM_MATCH is modeled"
		}
	}
	var matches []catalog.Type
	for _, t := range catalog.Types {
		attrs := attributes(t)
		ok := true
		for _, f := range in.Filters {
			if attrs[f.Field] != f.Value {
				ok = false // an unknown field never matches, like the real service
				break
			}
		}
		if ok {
			matches = append(matches, t)
		}
	}
	start := 0
	if in.NextToken != "" {
		n, err := strconv.Atoi(in.NextToken)
		if err != nil || n < 0 || n > len(matches) {
			return out, "InvalidNextTokenException", "invalid NextToken"
		}
		start = n
	}
	size := in.MaxResults
	if size <= 0 || size > defaultPage {
		size = defaultPage
	}
	end := start + size
	if end >= len(matches) {
		end = len(matches)
	} else {
		out.NextToken = strconv.Itoa(end)
	}
	out.FormatVersion = "aws_v1"
	out.PriceList = []string{}
	for _, t := range matches[start:end] {
		out.PriceList = append(out.PriceList, priceListEntry(t))
	}
	return out, "", ""
}
```

`cmd/pricing-facade/main.go`:

```go
// Command pricing-facade serves the simulated AWS Price List API behind the
// aws-gateway. Like ec2-sim it trusts the gateway for authentication.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tazzledazzle/meridian/internal/gateway"
	"github.com/tazzledazzle/meridian/internal/pricing"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:4571", "address to listen on")
	allowAny := flag.Bool("allow-any-interface", false, "allow a non-loopback address (containers only; never publish this port)")
	flag.Parse()
	if err := gateway.CheckBind(*listen, *allowAny); err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *listen, Handler: pricing.NewHandler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("pricing-facade listening on %s", *listen)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
```

- [ ] **Step 4: Write the EKS stub and binary**

`internal/ekstub/handler.go`:

```go
// Package ekstub is a minimal EKS: it answers eks:DescribeCluster for one
// cluster, which is all Karpenter needs. The full eks-facade (CreateCluster,
// node groups, k3d-backed clusters) arrives in milestone M0.
package ekstub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/catalog"
)

// Config describes the one cluster the stub reports.
type Config struct {
	Name        string    // cluster name, e.g. meridian-g0
	Version     string    // Kubernetes minor EKS would report, e.g. 1.32
	Endpoint    string    // API server URL
	ServiceCIDR string    // service IPv4 CIDR (k3s default 10.43.0.0/16)
	CreatedAt   time.Time // creation time
	// Network placement reported in resourcesVpcConfig. Karpenter does not read these;
	// they are here so the response has the shape a real cluster has.
	VPCID            string
	SubnetIDs        []string
	SecurityGroupIDs []string
}

// MinorOf turns a k3s version like "v1.32.4+k3s1" into the minor "1.32" EKS reports.
func MinorOf(k3sVersion string) (string, error) {
	m := regexp.MustCompile(`^v?(\d+\.\d+)(\.\d+)?`).FindStringSubmatch(k3sVersion)
	if m == nil {
		return "", fmt.Errorf("cannot read a Kubernetes minor from %q", k3sVersion)
	}
	return m[1], nil
}

// Handler serves eks:DescribeCluster (GET /clusters/{name}) and eks:CreateCluster
// (POST /clusters). The one cluster it models already exists, so creating it
// again is ResourceInUseException, exactly what EKS answers for a duplicate name.
type Handler struct{ cfg Config }

// NewHandler returns the stub for one cluster.
func NewHandler(cfg Config) *Handler { return &Handler{cfg: cfg} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodPost && len(parts) == 1 && parts[0] == "clusters":
		h.create(w, r)
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "clusters":
		if parts[1] != h.cfg.Name {
			awsapi.WriteError(w, awsapi.StyleRESTJSON, http.StatusNotFound, "ResourceNotFoundException",
				"No cluster found for name: "+parts[1]+".")
			return
		}
		awsapi.WriteJSON(w, http.StatusOK, "application/json", map[string]any{"cluster": h.cluster()})
	default:
		awsapi.WriteError(w, awsapi.StyleRESTJSON, http.StatusNotFound, "UnknownOperationException",
			"only DescribeCluster (GET /clusters/{name}) and CreateCluster (POST /clusters) are modeled")
	}
}

// create answers CreateCluster for the one modeled cluster.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	body, err := awsapi.ReadBody(r, 1<<20)
	var req struct {
		Name string `json:"name"`
	}
	if err != nil || json.Unmarshal(body, &req) != nil || req.Name == "" {
		awsapi.WriteError(w, awsapi.StyleRESTJSON, http.StatusBadRequest, "InvalidParameterException", "a cluster name is required")
		return
	}
	if req.Name != h.cfg.Name {
		awsapi.WriteError(w, awsapi.StyleRESTJSON, http.StatusBadRequest, "InvalidParameterException",
			"this stub models only the cluster "+h.cfg.Name)
		return
	}
	awsapi.WriteError(w, awsapi.StyleRESTJSON, http.StatusConflict, "ResourceInUseException",
		"Cluster already exists with name: "+h.cfg.Name)
}

// cluster renders the cluster document.
func (h *Handler) cluster() map[string]any {
	subnets := h.cfg.SubnetIDs
	if subnets == nil {
		subnets = []string{}
	}
	sgs := h.cfg.SecurityGroupIDs
	if sgs == nil {
		sgs = []string{}
	}
	return map[string]any{
		"name":            h.cfg.Name,
		"arn":             fmt.Sprintf("arn:aws:eks:%s:123456789012:cluster/%s", catalog.Region, h.cfg.Name),
		"createdAt":       float64(h.cfg.CreatedAt.Unix()), // REST-JSON timestamps are epoch seconds
		"version":         h.cfg.Version,
		"endpoint":        h.cfg.Endpoint,
		"roleArn":         "arn:aws:iam::123456789012:role/meridian-g0-cluster",
		"platformVersion": "eks.1",
		"status":          "ACTIVE",
		"resourcesVpcConfig": map[string]any{
			"subnetIds":             subnets,
			"securityGroupIds":      sgs,
			"vpcId":                 h.cfg.VPCID,
			"endpointPublicAccess":  true,
			"endpointPrivateAccess": false,
		},
		"kubernetesNetworkConfig": map[string]any{"serviceIpv4Cidr": h.cfg.ServiceCIDR, "ipFamily": "ipv4"},
		"certificateAuthority":    map[string]any{"data": "c2ltdWxhdGVkLWNhLW5vdC1hLXJlYWwtY2VydGlmaWNhdGU="},
		"tags":                    map[string]string{},
	}
}
```

`cmd/eks-facade/main.go`:

```go
// Command eks-facade serves a minimal EKS API behind the aws-gateway: it answers
// DescribeCluster for one cluster. It exists only if S0 found that Floci's EKS
// cannot give Karpenter what it reads; the full facade is a later milestone.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tazzledazzle/meridian/internal/ekstub"
	"github.com/tazzledazzle/meridian/internal/gateway"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:4572", "address to listen on")
	allowAny := flag.Bool("allow-any-interface", false, "allow a non-loopback address (containers only; never publish this port)")
	name := flag.String("cluster-name", "meridian-g0", "name of the one cluster to report")
	version := flag.String("k8s-version", "", "Kubernetes minor to report; empty derives it from K3S_VERSION")
	endpoint := flag.String("endpoint", "https://meridian-g0.eks.sim", "API server URL to report")
	cidr := flag.String("service-cidr", "10.43.0.0/16", "service IPv4 CIDR to report (k3s default)")
	vpc := flag.String("vpc-id", "", "VPC ID to report in resourcesVpcConfig")
	subnets := flag.String("subnet-ids", "", "comma-separated subnet IDs to report")
	groups := flag.String("security-group-ids", "", "comma-separated security group IDs to report")
	flag.Parse()

	if *version == "" {
		v, err := ekstub.MinorOf(os.Getenv("K3S_VERSION"))
		if err != nil {
			log.Fatalf("set -k8s-version or K3S_VERSION: %v", err)
		}
		*version = v
	}
	if err := gateway.CheckBind(*listen, *allowAny); err != nil {
		log.Fatal(err)
	}
	h := ekstub.NewHandler(ekstub.Config{Name: *name, Version: *version, Endpoint: *endpoint, ServiceCIDR: *cidr, CreatedAt: time.Now(),
		VPCID: *vpc, SubnetIDs: split(*subnets), SecurityGroupIDs: split(*groups)})
	srv := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("eks-facade (stub) listening on %s, reporting Kubernetes %s", *listen, *version)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// split turns "a,b" into []string{"a","b"} and "" into nil.
func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `gofmt -l internal cmd && go vet ./internal/pricing/ ./internal/ekstub/ ./cmd/... && go test -race ./internal/pricing/ ./internal/ekstub/ -v`
Expected: no gofmt output; all tests PASS.

- [ ] **Step 6: Prove the facades satisfy the S0 contract**

If `internal/s0probe/fakemisc_test.go` does not exist yet (Task 12 or this task creates it, whichever runs first), create it:

`internal/s0probe/fakemisc_test.go`:

```go
package s0probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMisc is a tiny in-memory STS/SSM/IAM/SQS, just enough to satisfy the
// probe. Together with the ec2-sim, pricing and EKS handlers it forms a
// reference backend: if the probe passes here, a failure against Floci means
// Floci differs from what the probe (and so Karpenter) expects, not that the
// probe is wrong.
type fakeMisc struct {
	mu       sync.Mutex
	params   map[string]string
	profiles map[string][]string // profile name -> role names
	queues   map[string][]string // queue name -> bodies
}

func (f *fakeMisc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		f.json(w, target, body)
		return
	}
	form, _ := url.ParseQuery(string(body))
	f.query(w, form)
}

func (f *fakeMisc) reply(w http.ResponseWriter, ct, s string) {
	w.Header().Set("Content-Type", ct)
	io.WriteString(w, s)
}

func (f *fakeMisc) query(w http.ResponseWriter, q url.Values) {
	name := q.Get("InstanceProfileName")
	switch q.Get("Action") {
	case "GetCallerIdentity":
		f.reply(w, "text/xml", `<GetCallerIdentityResponse><GetCallerIdentityResult><Account>000000000000</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`)
	case "CreateRole", "TagInstanceProfile", "RemoveRoleFromInstanceProfile", "DeleteInstanceProfile", "CreateInstanceProfile":
		switch q.Get("Action") {
		case "CreateInstanceProfile":
			f.profiles[name] = nil
		case "RemoveRoleFromInstanceProfile":
			f.profiles[name] = nil
		case "DeleteInstanceProfile":
			delete(f.profiles, name)
		}
		f.reply(w, "text/xml", `<R><Result/></R>`)
	case "AddRoleToInstanceProfile":
		f.profiles[name] = append(f.profiles[name], q.Get("RoleName"))
		f.reply(w, "text/xml", `<R/>`)
	case "GetInstanceProfile":
		var roles strings.Builder
		for _, r := range f.profiles[name] {
			fmt.Fprintf(&roles, "<member><RoleName>%s</RoleName></member>", r)
		}
		f.reply(w, "text/xml", fmt.Sprintf(`<GetInstanceProfileResponse><GetInstanceProfileResult><InstanceProfile><InstanceProfileName>%s</InstanceProfileName><Arn>arn:aws:iam::000000000000:instance-profile/%s</Arn><Roles>%s</Roles></InstanceProfile></GetInstanceProfileResult></GetInstanceProfileResponse>`, name, name, roles.String()))
	case "ListInstanceProfiles":
		var ps strings.Builder
		for p := range f.profiles {
			fmt.Fprintf(&ps, "<member><InstanceProfileName>%s</InstanceProfileName></member>", p)
		}
		f.reply(w, "text/xml", `<ListInstanceProfilesResponse><ListInstanceProfilesResult><InstanceProfiles>`+ps.String()+`</InstanceProfiles></ListInstanceProfilesResult></ListInstanceProfilesResponse>`)
	default:
		http.Error(w, "unsupported "+q.Get("Action"), http.StatusBadRequest)
	}
}

func (f *fakeMisc) json(w http.ResponseWriter, target string, body []byte) {
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	op := target[strings.Index(target, ".")+1:]
	out := map[string]any{}
	switch op {
	case "PutParameter":
		f.params[str("Name")] = str("Value")
	case "GetParameter":
		v, ok := f.params[str("Name")]
		if !ok {
			http.Error(w, `{"__type":"ParameterNotFound"}`, http.StatusBadRequest)
			return
		}
		out["Parameter"] = map[string]any{"Name": str("Name"), "Value": v}
	case "CreateQueue", "GetQueueUrl":
		if op == "CreateQueue" {
			f.queues[str("QueueName")] = nil
		}
		out["QueueUrl"] = "http://sqs.test/000000000000/" + str("QueueName")
	case "SendMessage":
		n := str("QueueUrl")[strings.LastIndex(str("QueueUrl"), "/")+1:]
		f.queues[n] = append(f.queues[n], str("MessageBody"))
	case "ReceiveMessage":
		n := str("QueueUrl")[strings.LastIndex(str("QueueUrl"), "/")+1:]
		if len(f.queues[n]) > 0 {
			out["Messages"] = []any{map[string]any{"Body": f.queues[n][0], "ReceiptHandle": "rh-1"}}
		}
	case "DeleteMessage":
	default:
		http.Error(w, "unsupported "+op, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	_ = json.NewEncoder(w).Encode(out)
}

var scopeRE = regexp.MustCompile(`Credential=[^/]+/[^/]+/[^/]+/([^/]+)/aws4_request`)

// refConfig is the probe configuration the reference-backend tests share.
func refConfig() Config {
	return Config{Region: "us-west-2", Access: "AKIAEXAMPLE", Secret: "secret", Cluster: "meridian-g0",
		AMIParam: "/aws/service/eks/optimized-ami/1.32/amazon-linux-2023/x86_64/standard/recommended/image_id",
		Wait:     5 * time.Second}
}

// newMisc returns the fake STS/SSM/IAM/SQS with the AMI parameter pre-seeded.
func newMisc(ami string) *fakeMisc {
	return &fakeMisc{params: map[string]string{refConfig().AMIParam: ami}, profiles: map[string][]string{}, queues: map[string][]string{}}
}

// refServer routes by the SigV4 service in the Authorization header: services
// in routes go to their handler, everything else to the fake misc services.
func refServer(t *testing.T, routes map[string]http.Handler, misc http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := scopeRE.FindStringSubmatch(r.Header.Get("Authorization"))
		if m == nil {
			http.Error(w, "unsigned", http.StatusForbidden)
			return
		}
		if h, ok := routes[m[1]]; ok {
			h.ServeHTTP(w, r)
			return
		}
		misc.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// expectAllPass fails the test for every check that did not pass.
func expectAllPass(t *testing.T, cfg Config, minPass int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	results := Run(ctx, cfg, Checks())
	if s := Summarize(results); s.Pass < minPass {
		t.Errorf("only %d checks passed, want at least %d", s.Pass, minPass)
	}
	for _, r := range results {
		if r.Status != "pass" {
			t.Errorf("%s: %s: %s", r.Name, r.Status, r.Detail)
		}
	}
}
```

`internal/s0probe/selftest_facades_test.go`:

```go
package s0probe

import (
	"net/http"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/ekstub"
	"github.com/tazzledazzle/meridian/internal/pricing"
)

// TestProbePricingAndEKSPassAgainstTheFacades runs the Pricing and EKS checks
// against the fallback facades.
func TestProbePricingAndEKSPassAgainstTheFacades(t *testing.T) {
	cfg := refConfig()
	eks := ekstub.NewHandler(ekstub.Config{Name: cfg.Cluster, Version: "1.32", Endpoint: "https://10.0.0.1:6443", ServiceCIDR: "10.43.0.0/16", CreatedAt: time.Now()})
	cfg.Endpoint = refServer(t, map[string]http.Handler{"pricing": pricing.NewHandler(), "eks": eks}, newMisc("ami-0"))
	cfg.Groups = []string{"pricing", "eks"}
	expectAllPass(t, cfg, 2)
}
```

Run: `gofmt -l internal && go test -race ./internal/s0probe/ -run 'ProbePricing' -v`
Expected: PASS.

- [ ] **Step 7: Start them through the gateway and probe**

Run: `make s0-down; make s0-up && S0_LABEL=facades make s0-probe`
Expected: the `pricing:*` and `eks:*` checks that moved pass in `docs/s0/report.md`. If a response fails to decode, fix the handler and add a test for the field first.

- [ ] **Step 8: Commit**

```bash
git add internal/pricing internal/ekstub cmd/pricing-facade cmd/eks-facade internal/s0probe docs/s0
git commit -m "feat: add pricing-facade and the eks-facade stub (S0 fallbacks)"
```

### Task 14: [Fallback] Moto for IAM, STS, SQS and SSM

> **Conditional task.** Execute this only if `docs/s0/decision.md` (Task 8) moved `iam`, `sts`, `sqs` or `ssm` operations to `moto`. If not, skip it and record the skip in `docs/s0/decision.md`.

Moto is an off-the-shelf emulator, so this task writes no service code: it pins the image, supplies the Compose profile's missing variable, and seeds Moto the way Task 7 seeded Floci.

**Files:**
- Create: `scripts/seed-moto.sh`
- Modify: `versions.yaml`, `Makefile`

**Interfaces:**
- Consumes: the `moto` Compose profile and `-backend=moto=http://moto:5000` gateway flag (Task 7); `bin/linux/awscall` (Task 7).
- Produces: `MOTO_IMAGE` in `.env.images`; SSM AMI parameter, IAM node role and SQS interruption queue inside Moto; `make seed` seeding Moto too when `ops.yaml` routes to it.

**Acceptance Criteria:**
- `versions.yaml` pins `moto` by digest; `make env-images` emits `MOTO_IMAGE`.
- After `make seed`, the probe's `ssm`, `iam`, `sqs` and `sts` checks that moved pass through the gateway.
- Seeding is idempotent and talks to Moto directly.

- [ ] **Step 1: Pin Moto**

```bash
docker buildx imagetools inspect motoserver/moto:<tag> | grep -m1 Digest
```

Add under `components:` in `versions.yaml` (`moto` is optional, so `Validate` accepts the file with or without it):

```yaml
  - {name: moto,      kind: image, repo: motoserver/moto,                   version: "<tag>",     digest: "sha256:<resolved>"}
```

Run: `make env-images && grep MOTO_IMAGE .env.images`
Expected: one `MOTO_IMAGE=motoserver/moto:<tag>@sha256:...` line.

- [ ] **Step 2: Write the seeding script**

`scripts/seed-moto.sh`:

```bash
#!/usr/bin/env bash
# Seeds Moto directly with what Karpenter expects from SSM, IAM and SQS. Mirrors seed-floci.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env; . ./.env.images; set +a
export AWS_REGION=us-west-2
NET="${MERIDIAN_NET:-meridian}"   # the gate sets meridian-gate
MOTO="http://moto:5000"
CLUSTER=meridian-g0
AMI="$(cat .ami-id 2>/dev/null || echo ami-0a1b2c3d4e5f60718)"   # the ID seed-floci.sh recorded, or the fixed ec2-sim one

call() { docker run --rm --network "$NET" -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_REGION \
  -v "$PWD/bin/linux/awscall:/awscall:ro" --entrypoint /awscall "${STATIC_IMAGE}" -endpoint "$MOTO" "$@"; }

MINOR=$(echo "$K3S_VERSION" | sed -E 's/^v?([0-9]+\.[0-9]+).*/\1/')
call -service ssm -target AmazonSSM.PutParameter -body "{
  \"Name\": \"/aws/service/eks/optimized-ami/${MINOR}/amazon-linux-2023/x86_64/standard/recommended/image_id\",
  \"Value\": \"${AMI}\", \"Type\": \"String\", \"Overwrite\": true}" >/dev/null

TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
call -service iam -version 2010-05-08 -action CreateRole RoleName=KarpenterNodeRole-${CLUSTER} \
  "AssumeRolePolicyDocument=${TRUST}" >/dev/null 2>&1 || true   # already exists on a re-run

call -service sqs -target AmazonSQS.CreateQueue -json-version 1.0 \
  -body "{\"QueueName\":\"${CLUSTER}-interruption\"}" >/dev/null
echo "seeded moto: ssm ${MINOR}/${AMI}, role KarpenterNodeRole-${CLUSTER}, queue ${CLUSTER}-interruption"
```

Replace the `seed` target in `Makefile` so it seeds whichever backends are in use:

```make
seed: build-linux
	./scripts/seed-floci.sh
	@if python3 scripts/profiles.py ops.yaml | grep -q moto; then ./scripts/seed-moto.sh; fi
```

Run `chmod +x scripts/seed-moto.sh`.

- [ ] **Step 3: Start it and probe**

Run: `make s0-down; make s0-up && make seed && S0_LABEL=moto make s0-probe`
Expected: the moved `ssm`, `iam`, `sqs` and `sts` checks pass. If Moto rejects the SQS JSON protocol, the pinned Moto is too old: bump `moto` in `versions.yaml`; do not change Karpenter's protocol.

- [ ] **Step 4: Commit**

```bash
git add scripts/seed-moto.sh Makefile versions.yaml docs/s0
git commit -m "feat: add Moto as the S0 fallback for IAM, STS, SQS and SSM"
```

### Task 15: Workloads, KWOK, and the isolated gate stack

**Why:** the gate must run with no route to the internet (criterion 7), so it needs its own stack: a Docker network marked `internal`, no published ports, every image loaded in advance, and the gateway in strict record mode. This task builds that stack and the Kubernetes objects the criteria use (KWOK, `node-registrar`, an `EC2NodeClass`, two `NodePool`s, two pause-pod Deployments).

**Files:**
- Create: `deploy/compose.gate.yaml`, `deploy/Dockerfile.tools`, `deploy/node-registrar.yaml`
- Create: `deploy/workloads/nodepools.yaml`, `deploy/workloads/inflate.yaml`
- Create: `deploy/kwok/kwok.yaml`, `deploy/kwok/stage-fast.yaml`, `deploy/kwok/SHA256SUMS` (vendored by script), `deploy/images.system.txt`, `deploy/floci.label`
- Create: `scripts/vendor-kwok.sh`, `scripts/images-load.sh`, `scripts/gate-up.sh`, `scripts/gate-down.sh`, `scripts/install-k3d.sh`
- Modify: `Makefile`, `.gitignore`, `deploy/karpenter-values.yaml` (pull policy)

**Interfaces:**
- Consumes: everything from Tasks 7 to 10 (and 11 to 14 if they ran); `scripts/localimg.py` (Step 1 of this task); `.env.images` values `KWOK_VERSION`, `K3D_VERSION`, `KARPENTER_VERSION`, `K3S_VERSION`, `STATIC_IMAGE`, `GOLANG_IMAGE`, `FLOCI_IMAGE`.
- Produces: Docker network `meridian-gate` (internal, no published ports); the tools image `meridian/tools:gate` (`awscall`, `simctl`, `s0probe`); `make gate-up`, `make gate-down`; a cluster with Karpenter, KWOK, `node-registrar`, `EC2NodeClass/default`, `NodePool/ondemand`, `NodePool/spot`, `Deployment/inflate-ondemand` and `Deployment/inflate-spot` (both at 0 replicas).

**Acceptance Criteria:**
- `make gate-up` from a clean checkout brings the whole stack up with the network `meridian-gate` reporting `Internal: true`, and no container publishing a port.
- Every image the cluster runs is loaded before the workloads start; manifests are rewritten to local names with `imagePullPolicy` that never reaches a registry.
- `scripts/gate-up.sh` always starts with `scripts/gate-down.sh`, so repeated runs never inherit state.
- `scripts/localimg.py` maps upstream image references to stable local names (tested).

- [ ] **Step 1: Image-name mapping script and its tests**

The gate cluster cannot pull images, so each image is pulled on the host, re-tagged with a local name, imported into the cluster, and the manifests are rewritten to use the same name. This script is the single place that mapping lives.

`scripts/test_localimg.py`:

```python
import unittest

import localimg

KARPENTER = "public.ecr.aws/karpenter/controller:1.2.3@sha256:" + "a" * 64


class LocalName(unittest.TestCase):
    def test_tag_and_digest(self):
        self.assertEqual(localimg.local_name(KARPENTER), "meridian-local/public.ecr.aws-karpenter-controller:1.2.3")

    def test_digest_only_uses_a_short_digest_as_the_tag(self):
        self.assertEqual(localimg.local_name("registry.k8s.io/kwok/kwok@sha256:" + "b" * 64),
                         "meridian-local/registry.k8s.io-kwok-kwok:" + "b" * 12)

    def test_registry_port_is_not_mistaken_for_a_tag(self):
        self.assertEqual(localimg.local_name("localhost:5000/team/app:v1"), "meridian-local/localhost-5000-team-app:v1")

    def test_untagged_defaults_to_latest(self):
        self.assertEqual(localimg.local_name("busybox"), "meridian-local/busybox:latest")


MANIFEST = f"""\
containers:
  - name: controller
    image: {KARPENTER}
  - name: sidecar
    image: "registry.k8s.io/kwok/kwok:v0.7.0"
  - name: mine
    image: meridian/node-registrar:g0
  - name: done
    image: meridian-local/already:1
"""


class Rewrite(unittest.TestCase):
    def test_list_skips_project_and_local_images(self):
        pairs = sorted(set(localimg.refs(MANIFEST)))
        self.assertEqual(pairs, sorted([KARPENTER, "registry.k8s.io/kwok/kwok:v0.7.0"]))

    def test_rewrite_changes_only_upstream_images_and_keeps_indentation(self):
        out = localimg.rewrite(MANIFEST)
        self.assertIn("    image: meridian-local/public.ecr.aws-karpenter-controller:1.2.3\n", out)
        self.assertIn("    image: meridian-local/registry.k8s.io-kwok-kwok:v0.7.0\n", out)
        self.assertIn("    image: meridian/node-registrar:g0\n", out)
        self.assertIn("    image: meridian-local/already:1\n", out)
        self.assertNotIn("public.ecr.aws/karpenter", out)

    def test_rewrite_is_idempotent(self):
        once = localimg.rewrite(MANIFEST)
        self.assertEqual(localimg.rewrite(once), once)


if __name__ == "__main__":
    unittest.main()
```

`scripts/localimg.py`:

```python
#!/usr/bin/env python3
"""Maps upstream image references to local names, for an offline cluster.

The gate runs on a Docker network with no route out, so the cluster cannot pull
images. images-load.sh pulls each image on the host, re-tags it with a local
name, and imports it into the cluster; the manifests are rewritten to use the
same local names so the pull policy never reaches a registry.

  localimg.py list     read manifests on stdin, print "<upstream ref> <local ref>" pairs
  localimg.py rewrite  read manifests on stdin, write them with image refs replaced
"""
import re
import sys

PREFIX = "meridian-local/"
# Images the project builds itself are already local; leave them alone.
SKIP = ("meridian/", PREFIX)
IMAGE_LINE = re.compile(r'^(\s*-?\s*image:\s*)["\']?([^\s"\']+)["\']?\s*$')


def local_name(ref: str) -> str:
    """public.ecr.aws/karpenter/controller:1.2.3@sha256:abcd... -> meridian-local/public.ecr.aws-karpenter-controller:1.2.3"""
    name, _, digest = ref.partition("@")
    repo, tag = name, ""
    # A colon after the last slash is a tag separator; before it, a registry port.
    last = name.rsplit("/", 1)[-1]
    if ":" in last:
        repo, tag = name.rsplit(":", 1)
    if not tag:
        tag = digest.split(":")[-1][:12] if digest else "latest"
    repo = re.sub(r"[^a-z0-9._-]+", "-", repo.lower()).strip("-")
    return f"{PREFIX}{repo}:{tag}"


def refs(text: str):
    for line in text.splitlines():
        m = IMAGE_LINE.match(line)
        if m and not m.group(2).startswith(SKIP):
            yield m.group(2)


def rewrite(text: str) -> str:
    out = []
    for line in text.splitlines():
        m = IMAGE_LINE.match(line)
        if m and not m.group(2).startswith(SKIP):
            line = m.group(1) + local_name(m.group(2))
        out.append(line)
    return "\n".join(out) + ("\n" if text.endswith("\n") else "")


def main(argv) -> int:
    if len(argv) != 2 or argv[1] not in ("list", "rewrite"):
        print(__doc__, file=sys.stderr)
        return 2
    text = sys.stdin.read()
    if argv[1] == "rewrite":
        sys.stdout.write(rewrite(text))
    else:
        for ref in sorted(set(refs(text))):
            print(ref, local_name(ref))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
```

Run: `cd scripts && python3 -W error -m unittest test_localimg -v`
Expected: 7 tests PASS.

- [ ] **Step 2: Vendor the KWOK manifests**

KWOK's controller and its pod/node lifecycle stages come from the pinned release; the gate applies committed copies so it needs no network.

`scripts/vendor-kwok.sh`:

```bash
#!/usr/bin/env bash
# Downloads the pinned KWOK release manifests into deploy/kwok/ so the gate can apply them without a network.
# Run once online (and again when versions.yaml changes the kwok version); the files are committed.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env.images; set +a
V="${KWOK_VERSION:?run make env-images}"
mkdir -p deploy/kwok
for f in kwok.yaml stage-fast.yaml; do
  curl -fsSL "https://github.com/kubernetes-sigs/kwok/releases/download/${V}/${f}" -o "deploy/kwok/${f}"
done
# The controller image tag in the manifest must be the version we pinned by digest.
grep -q "registry.k8s.io/kwok/kwok:${V}" deploy/kwok/kwok.yaml || { echo "kwok.yaml does not reference kwok:${V}"; exit 1; }
# Keep the pull policy from ever reaching a registry once the image is preloaded.
sed -i.bak 's/imagePullPolicy: Always/imagePullPolicy: IfNotPresent/' deploy/kwok/kwok.yaml && rm -f deploy/kwok/kwok.yaml.bak
( cd deploy/kwok && shasum -a 256 kwok.yaml stage-fast.yaml > SHA256SUMS )
echo "vendored KWOK ${V} into deploy/kwok"
```

Run: `chmod +x scripts/vendor-kwok.sh && make env-images && ./scripts/vendor-kwok.sh && git status --short deploy/kwok`
Expected: `vendored KWOK <version> into deploy/kwok`, and three new files.

- [ ] **Step 3: Write the workloads and the registrar manifest**

KWOK nodes carry the taint `kwok.x-k8s.io/node=fake:NoSchedule`, so the pause pods tolerate it. The two NodePools differ on purpose: `ondemand` allows two sizes with different prices (criterion 5), `spot` is the pool the gate interrupts (criterion 4). Consolidation waits only 30 seconds so criterion 3 does not take minutes.

`deploy/workloads/nodepools.yaml`:

```yaml
# One EC2NodeClass and two NodePools. Karpenter resolves the AMI through the SSM alias, the subnets and
# security groups through the discovery tag, and the instance profile from the role (IAM).
apiVersion: karpenter.k8s.aws/v1
kind: EC2NodeClass
metadata:
  name: default
spec:
  role: KarpenterNodeRole-meridian-g0            # created by the seed script; Karpenter builds the profile from it
  amiSelectorTerms:
    - alias: al2023@latest                       # resolved through the SSM parameter the seed script wrote
  subnetSelectorTerms:
    - tags: {karpenter.sh/discovery: meridian-g0}
  securityGroupSelectorTerms:
    - tags: {karpenter.sh/discovery: meridian-g0}
---
# On-demand pool: two interchangeable sizes with different prices, so the choice shows Karpenter used the
# price data (gate criterion 5).
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: ondemand
spec:
  template:
    spec:
      nodeClassRef: {group: karpenter.k8s.aws, kind: EC2NodeClass, name: default}
      requirements:
        - {key: karpenter.sh/capacity-type, operator: In, values: ["on-demand"]}
        - {key: node.kubernetes.io/instance-type, operator: In, values: ["c5.large", "m5.large"]}
        - {key: kubernetes.io/arch, operator: In, values: ["amd64"]}
  disruption:
    consolidationPolicy: WhenEmptyOrUnderutilized
    consolidateAfter: 30s                        # short so the gate does not wait minutes (criterion 3)
  limits: {cpu: "64"}
---
# Spot pool: the gate interrupts a node from this pool (criterion 4).
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: spot
spec:
  template:
    spec:
      nodeClassRef: {group: karpenter.k8s.aws, kind: EC2NodeClass, name: default}
      requirements:
        - {key: karpenter.sh/capacity-type, operator: In, values: ["spot"]}
        - {key: node.kubernetes.io/instance-type, operator: In, values: ["m5.large"]}
        - {key: kubernetes.io/arch, operator: In, values: ["amd64"]}
  disruption:
    consolidationPolicy: WhenEmptyOrUnderutilized
    consolidateAfter: 30s
  limits: {cpu: "64"}
```

`deploy/workloads/inflate.yaml`:

```yaml
# Pending pods that force Karpenter to provision capacity. replicas start at 0; the gate scales them.
# Each pod tolerates KWOK's node taint, because every Karpenter node here is a KWOK node.
apiVersion: apps/v1
kind: Deployment
metadata:
  name: inflate-ondemand
spec:
  replicas: 0
  selector: {matchLabels: {app: inflate-ondemand}}
  template:
    metadata: {labels: {app: inflate-ondemand}}
    spec:
      nodeSelector: {karpenter.sh/nodepool: ondemand}
      tolerations:
        - {key: kwok.x-k8s.io/node, operator: Equal, value: fake, effect: NoSchedule}
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.9
          imagePullPolicy: IfNotPresent
          resources: {requests: {cpu: "1", memory: 512Mi}}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: inflate-spot
spec:
  replicas: 0
  selector: {matchLabels: {app: inflate-spot}}
  template:
    metadata: {labels: {app: inflate-spot}}
    spec:
      nodeSelector: {karpenter.sh/nodepool: spot}
      tolerations:
        - {key: kwok.x-k8s.io/node, operator: Equal, value: fake, effect: NoSchedule}
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.9
          imagePullPolicy: IfNotPresent
          resources: {requests: {cpu: "1", memory: 512Mi}}
```

`deploy/node-registrar.yaml`:

```yaml
# node-registrar runs in the cluster and creates/deletes only Nodes it labelled itself.
apiVersion: v1
kind: ServiceAccount
metadata: {name: node-registrar, namespace: kube-system}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: node-registrar}
rules:
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list", "create", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: node-registrar}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: node-registrar}
subjects: [{kind: ServiceAccount, name: node-registrar, namespace: kube-system}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: node-registrar, namespace: kube-system}
spec:
  replicas: 1
  selector: {matchLabels: {app: node-registrar}}
  template:
    metadata: {labels: {app: node-registrar}}
    spec:
      serviceAccountName: node-registrar
      containers:
        - name: node-registrar
          image: meridian/node-registrar:gate     # built locally and imported into the cluster
          imagePullPolicy: Never                  # never reaches a registry
          args: ["-ec2-endpoint=http://aws-gateway:4566", "-cluster=meridian-g0"]
          envFrom: [{secretRef: {name: aws-creds}}]
```

Add one line at the top level of `deploy/karpenter-values.yaml`, so Karpenter's image is never pulled from a registry once preloaded:

```yaml
imagePullPolicy: IfNotPresent
```

- [ ] **Step 4: Write the gate Compose file and the tools image**

`deploy/compose.gate.yaml`:

```yaml
# The gate stack: the same services as the S0 stack, but on a Docker network with NO route out, no published
# ports, and the gateway in strict record mode (unsupported operations are rejected, not forwarded).
# Run through scripts/gate-up.sh, which sets COMPOSE_PROFILES from ops.yaml.
name: meridian-gate

networks:
  meridian:
    name: meridian-gate
    internal: true            # Docker gives containers on this network no gateway, so nothing can leave it

services:
  floci:
    image: ${FLOCI_IMAGE}
    networks: [meridian]
    environment:
      FLOCI_DEFAULT_REGION: us-west-2
      FLOCI_HOSTNAME: floci
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock   # see ADR-0002: root-equivalent on the host's Docker daemon
    expose: ["4566"]

  aws-gateway:
    build:
      context: ..
      dockerfile: deploy/Dockerfile.service
      args:
        CMD: aws-gateway
        GOLANG_IMAGE: ${GOLANG_IMAGE}
        STATIC_IMAGE: ${STATIC_IMAGE}
    networks: [meridian]
    environment:
      AWS_ACCESS_KEY_ID: ${AWS_ACCESS_KEY_ID:?run make init}
      AWS_SECRET_ACCESS_KEY: ${AWS_SECRET_ACCESS_KEY:?run make init}
    command:
      - -listen=0.0.0.0:4566
      - -allow-any-interface
      - -ops=/ops.yaml
      - -record                      # record every call for the golden comparison; reject unsupported operations
      - -trace-out=/trace
      - -backend=floci=http://floci:4566
      - -backend=ec2-sim=http://ec2-sim:4570
      - -backend=pricing-facade=http://pricing-facade:4571
      - -backend=eks-facade=http://eks-facade:4572
      - -backend=moto=http://moto:5000
    # No ports: on an internal network nothing on the host can reach the gateway either. Tests and tools
    # run as containers on this network.
    volumes:
      - ../trace/gate:/trace
    user: "0:0"
    depends_on: [floci]

  ec2-sim:
    profiles: [ec2-sim]
    build:
      context: ..
      dockerfile: deploy/Dockerfile.service
      args: {CMD: ec2-sim, GOLANG_IMAGE: "${GOLANG_IMAGE}", STATIC_IMAGE: "${STATIC_IMAGE}"}
    networks: [meridian]
    command: ["-listen=0.0.0.0:4570", "-allow-any-interface"]
    expose: ["4570"]

  pricing-facade:
    profiles: [pricing-facade]
    build:
      context: ..
      dockerfile: deploy/Dockerfile.service
      args: {CMD: pricing-facade, GOLANG_IMAGE: "${GOLANG_IMAGE}", STATIC_IMAGE: "${STATIC_IMAGE}"}
    networks: [meridian]
    command: ["-listen=0.0.0.0:4571", "-allow-any-interface"]
    expose: ["4571"]

  eks-facade:
    profiles: [eks-facade]
    build:
      context: ..
      dockerfile: deploy/Dockerfile.service
      args: {CMD: eks-facade, GOLANG_IMAGE: "${GOLANG_IMAGE}", STATIC_IMAGE: "${STATIC_IMAGE}"}
    networks: [meridian]
    command: ["-listen=0.0.0.0:4572", "-allow-any-interface", "-cluster-name=meridian-g0"]
    environment:
      K3S_VERSION: ${K3S_VERSION:-}
    expose: ["4572"]

  moto:
    profiles: [moto]
    image: ${MOTO_IMAGE:-}
    networks: [meridian]
    expose: ["5000"]
```

`deploy/Dockerfile.tools`:

```dockerfile
# The command-line tools the gate runs on the isolated network: awscall, simctl and s0probe.
# Build context is bin/linux (static Linux binaries from `make build-linux`).
ARG STATIC_IMAGE
FROM ${STATIC_IMAGE}
COPY awscall simctl s0probe /usr/local/bin/
```

- [ ] **Step 5: Discover what k3s itself needs, and what Floci labels**

k3s pulls its own system images (the pause sandbox image, CoreDNS). Find them on the S0 stack (which has network access), once:

```bash
make s0-up
docker exec k3d-meridian-g0-server-0 crictl images | awk 'NR>1 && $1 ~ /rancher\/mirrored/ {print $1":"$2}' | sort -u > deploy/images.system.txt
```

Expected: a short list (pause, CoreDNS, and any others k3s uses). Review it; every line must be a pinned tag, never `latest`.

While the S0 stack runs a Floci instance (after the probe has launched one), record the label Floci puts on the containers it creates, so teardown can remove leaks:

```bash
docker ps -a --format '{{.Names}} {{.Label "<candidate>"}}'   # find the label key from: docker inspect <container> --format '{{json .Config.Labels}}'
echo '<key>=<value>' > deploy/floci.label
```

Expected: `deploy/floci.label` holds one `key=value` line that matches Floci's instance containers and nothing else.

- [ ] **Step 6: Write the image loader and the gate scripts**

`scripts/images-load.sh`:

```bash
#!/usr/bin/env bash
# Loads every image the gate cluster needs, because the cluster has no route to a registry.
#   1. Images in rendered manifests (stdin): pulled on the host, re-tagged with a local name (localimg.py),
#      and imported; the manifests are rewritten to the same names by the caller.
#   2. Images k3s itself needs (deploy/images.system.txt): imported under their own names.
# Pulling happens on the host, which is outside the isolated network.
set -euo pipefail
cd "$(dirname "$0")/.."
CLUSTER="${1:-meridian-g0}"
MANIFESTS="$(cat)"

while read -r upstream local_ref; do
  [ -n "$upstream" ] || continue
  docker image inspect "$upstream" >/dev/null 2>&1 || docker pull "$upstream"
  docker tag "$upstream" "$local_ref"
  k3d image import "$local_ref" --cluster "$CLUSTER" --mode direct
done < <(printf '%s\n' "$MANIFESTS" | python3 scripts/localimg.py list | sort -u)

while read -r ref; do
  case "$ref" in ''|'#'*) continue ;; esac
  docker image inspect "$ref" >/dev/null 2>&1 || docker pull "$ref"
  k3d image import "$ref" --cluster "$CLUSTER" --mode direct
done < deploy/images.system.txt
```

`scripts/gate-up.sh`:

```bash
#!/usr/bin/env bash
# Brings up the isolated gate stack from a clean state: Floci/fallbacks and the gateway on a network with no
# route out, a k3d cluster on that network, preloaded images, KWOK, node-registrar, real Karpenter, and the
# NodePools. Idempotent only through teardown: it always starts with gate-down.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
export MERIDIAN_NET=meridian-gate
./scripts/gate-down.sh
rm -rf trace/gate && mkdir -p trace/gate     # a fresh recording for this run
make init env-images build-linux >/dev/null
set -a; . ./.env; . ./.env.images; set +a
KC=scripts/kc.sh
COMPOSE="docker compose --env-file .env.images --env-file .env -f deploy/compose.gate.yaml"

# Tools image (awscall, simctl, s0probe) and the registrar image; both are built on the host.
docker build -q -f deploy/Dockerfile.tools --build-arg STATIC_IMAGE="$STATIC_IMAGE" -t meridian/tools:gate bin/linux >/dev/null
docker build -q -f deploy/Dockerfile.service --build-arg CMD=node-registrar \
  --build-arg GOLANG_IMAGE="$GOLANG_IMAGE" --build-arg STATIC_IMAGE="$STATIC_IMAGE" -t meridian/node-registrar:gate . >/dev/null

COMPOSE_PROFILES="$(python3 scripts/profiles.py ops.yaml)" $COMPOSE up -d --build

# Wait until the gateway answers a signed STS call from inside the network.
for i in $(seq 1 60); do
  if docker run --rm --network "$MERIDIAN_NET" -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_REGION=us-west-2 \
       --entrypoint /usr/local/bin/awscall meridian/tools:gate -endpoint http://aws-gateway:4566 \
       -service sts -version 2011-06-15 -action GetCallerIdentity >/dev/null 2>&1; then break; fi
  [ "$i" = 60 ] && { echo "gateway never became ready"; exit 1; }
  sleep 2
done

./scripts/seed-floci.sh
if python3 scripts/profiles.py ops.yaml | grep -q moto; then ./scripts/seed-moto.sh; fi

./scripts/k3d-up.sh
k3d image import meridian/node-registrar:gate --cluster meridian-g0 --mode direct

# Render every manifest once, load the images they name, then apply the rewritten (local-name) copies.
RENDER="$(mktemp)"
trap 'rm -f "$RENDER"' EXIT
{
  helm template karpenter oci://public.ecr.aws/karpenter/karpenter --version "$KARPENTER_VERSION" \
    --namespace kube-system --kube-version "${K3S_VERSION%%+*}" --include-crds --values deploy/karpenter-values.yaml
  echo '---'; cat deploy/kwok/kwok.yaml
  echo '---'; cat deploy/workloads/inflate.yaml
} > "$RENDER"
./scripts/images-load.sh meridian-g0 < "$RENDER"

# Credentials Secret used by Karpenter and node-registrar (lives only in this local cluster).
$KC -n kube-system create secret generic aws-creds \
  --from-literal=AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" --from-literal=AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
  --dry-run=client -o yaml | $KC apply -f -

python3 scripts/localimg.py rewrite < "$RENDER" | $KC apply --server-side --force-conflicts -f -
python3 scripts/localimg.py rewrite < deploy/kwok/stage-fast.yaml | $KC apply --server-side --force-conflicts -f -
$KC apply -f - < deploy/node-registrar.yaml
$KC -n kube-system rollout status deploy/karpenter --timeout=180s
$KC -n kube-system rollout status deploy/node-registrar --timeout=120s
# CRDs are established before the NodePools are applied.
$KC wait --for=condition=Established crd/nodepools.karpenter.sh crd/ec2nodeclasses.karpenter.k8s.aws --timeout=60s
$KC apply -f - < deploy/workloads/nodepools.yaml
echo "gate stack is up (network $MERIDIAN_NET has no route out)"
```

`scripts/gate-down.sh`:

```bash
#!/usr/bin/env bash
# Removes everything the gate created: cluster, Compose project, network, and containers Floci launched.
# The recorded call list in trace/gate is kept so `make golden-update` can use it after a run.
set -uo pipefail
cd "$(dirname "$0")/.."
k3d cluster delete meridian-g0 >/dev/null 2>&1
docker compose -f deploy/compose.gate.yaml --profile '*' down -v --remove-orphans >/dev/null 2>&1
# Floci starts instance containers itself; remove any it leaked. deploy/floci.label holds the label Floci puts
# on them (recorded during S0, Task 7 Step 9).
[ -s deploy/floci.label ] && docker ps -aq --filter "label=$(cat deploy/floci.label)" | xargs -r docker rm -f >/dev/null 2>&1
exit 0
```

`scripts/install-k3d.sh`:

```bash
#!/usr/bin/env bash
# Installs the pinned k3d release and verifies it against the checksums the release publishes.
# Used by CI; on a laptop, install k3d however you like at the version in versions.yaml.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env.images; set +a
V="v${K3D_VERSION#v}"
BASE="https://github.com/k3d-io/k3d/releases/download/${V}"
ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
curl -fsSL -o "$TMP/k3d-linux-${ARCH}" "${BASE}/k3d-linux-${ARCH}"
curl -fsSL -o "$TMP/checksums.txt" "${BASE}/checksums.txt"
( cd "$TMP" && grep " k3d-linux-${ARCH}\$" checksums.txt | sha256sum -c - )
sudo install -m 0755 "$TMP/k3d-linux-${ARCH}" /usr/local/bin/k3d
k3d version
```

- [ ] **Step 7: Add the Makefile targets**

Append to `Makefile`:

```make
gate-up:
	./scripts/gate-up.sh

gate-down:
	./scripts/gate-down.sh

# The Karpenter proof gate (G0): tear down, build from scratch, run the eight criteria, tear down.
# GATE_KEEP=1 leaves the stack running afterwards for debugging.
gate-karpenter:
	./scripts/gate-up.sh
	status=0; go test -tags gate -count=1 -timeout 45m ./test/gate/ -v || status=$$?; \
	  if [ -z "$$GATE_KEEP" ]; then ./scripts/gate-down.sh; fi; exit $$status

# Accepts the recorded call surface from the last gate run as the new golden file. Review the diff first.
golden-update:
	mkdir -p trace/golden && cp trace/gate/calls.golden trace/golden/karpenter-calls.golden
```

Append `.ami-id` to `.gitignore` (written by the seed script). Run `chmod +x scripts/*.sh scripts/*.py`.

- [ ] **Step 8: Bring the stack up and check isolation by hand**

Run: `make s0-down; make gate-up`
Expected: it ends with `gate stack is up (network meridian-gate has no route out)`. Then:

```bash
docker network inspect meridian-gate --format '{{.Internal}}'      # true
docker ps --format '{{.Names}} {{.Ports}}'                           # no host port mappings on any container of this stack
scripts/kc.sh get nodes                                              # only the control-plane node: no workload nodes yet
scripts/kc.sh -n kube-system get pods                                # karpenter, kwok-controller, node-registrar Running
```

If the k3d cluster refuses to start on an `internal` network, stop and tell the user. The fallback is a normal network plus a firewall rule that drops its outbound traffic, which needs root and changes the host; that is their decision, not an automatic switch.

- [ ] **Step 9: Commit**

```bash
git add deploy scripts Makefile .gitignore
git commit -m "feat: add the isolated gate stack, KWOK, workloads and image preloading"
```

### Task 16: The gate test (eight criteria) and the decision checkpoint

**Files:**
- Create: `test/gate/helpers_test.go`, `test/gate/gate_test.go` (build tag `gate`, so `go test ./...` never starts Docker)
- Create: `trace/golden/karpenter-calls.golden` (generated, committed), `docs/checkpoints/g0.md`
- Modify: `Makefile` (already has `gate-karpenter` and `golden-update` from Task 15)

**Interfaces:**
- Consumes: `trace.ReadGolden`, `trace.Diff` (Task 6); the running gate stack (Task 15); `simctl`, `awscall`, `s0probe -prices` via the tools image.
- Produces: `TestGate` with sub-tests `C1_karpenter_starts`, `C2_pending_pod_becomes_a_running_node`, `C5_cheaper_offering_chosen_from_pricing_data`, `C6_ami_alias_and_instance_profile`, `C3_consolidation_terminates_instance_and_deletes_node`, `C4_spot_interruption_cordons_drains_and_replaces`, `C7_no_route_to_the_internet`, `C8_call_surface_matches_the_golden_file`. They run in that order because they share one cluster and C5 and C6 read the node C2 created before C3 removes it.

**Acceptance Criteria:**
- C1: the Karpenter deployment is available, has never restarted, and has not panicked.
- C2: a pending pod produces a NodeClaim with a provider ID (CreateFleet succeeded), the EC2 instance reaches `running`, a Ready Node with that provider ID appears, and the pod is Running on it.
- C3: scaling the pod to zero makes Karpenter terminate the instance and delete the Node.
- C4: `simctl interrupt` on a spot node's instance leads to a replacement Node that runs the pod and the old instance being terminated.
- C5: the instance type Karpenter launched is the cheapest allowed type according to the prices the backend returns, and the two prices differ.
- C6: `EC2NodeClass` is Ready, its resolved AMI is the one the SSM alias points to, and the instance profile it created carries the node role.
- C7: the Docker network is internal; a public AWS endpoint is unreachable from a container on it and from the Kubernetes node.
- C8: the recorded call surface equals the committed golden file and contains no operation missing from `ops.yaml`; the gate starts from teardown, and CI runs it on a fresh runner (Task 17).

- [ ] **Step 1: Write the gate test**

The helpers run `kubectl` through `scripts/kc.sh`, and AWS calls through the tools container on the isolated network, signed with the local credentials from `.env`.

`test/gate/helpers_test.go`:

```go
//go:build gate

package gate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	network = "meridian-gate" // the isolated network (deploy/compose.gate.yaml)
	gateway = "http://aws-gateway:4566"
	region  = "us-west-2"
	queue   = "meridian-g0-interruption"
)

// rig runs commands from the repository root with the local credentials in the environment.
type rig struct {
	root string
	env  []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	env := os.Environ()
	f, err := os.Open(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatalf("no .env: run `make init` (and `make gate-up`): %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok && k != "" && !strings.HasPrefix(k, "#") {
			env = append(env, k+"="+v)
		}
	}
	return &rig{root: root, env: env}
}

// sh runs a command and returns its combined output. A non-zero exit is an error that carries the output.
func (r *rig) sh(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = r.root, r.env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// kc runs kubectl inside the k3s server container (see scripts/kc.sh).
func (r *rig) kc(args ...string) (string, error) {
	return r.sh(60*time.Second, "./scripts/kc.sh", args...)
}

// kcJSON runs kubectl with -o json and decodes the answer into v.
func (r *rig) kcJSON(v any, args ...string) error {
	out, err := r.kc(append(args, "-o", "json")...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), v)
}

// tool runs one of the project's binaries in a container on the isolated network, as Karpenter's pod would reach
// the gateway: by name, signed with the local credentials (passed by name so they never appear in `ps`).
func (r *rig) tool(binary string, args ...string) (string, error) {
	base := []string{"run", "--rm", "--network", network, "-e", "AWS_ACCESS_KEY_ID", "-e", "AWS_SECRET_ACCESS_KEY",
		"-e", "AWS_REGION=" + region, "--entrypoint", "/usr/local/bin/" + binary, "meridian/tools:gate"}
	return r.sh(90*time.Second, "docker", append(base, args...)...)
}

// awsQuery calls an EC2/IAM/STS query action through the gateway.
func (r *rig) awsQuery(service, version, action string, params ...string) (string, error) {
	args := []string{"-endpoint", gateway, "-service", service, "-version", version, "-action", action}
	return r.tool("awscall", append(args, params...)...)
}

// eventually polls fn every 2 seconds until it reports done or the timeout passes; the last message is the failure.
func eventually(t *testing.T, timeout time.Duration, what string, fn func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := "never ran"
	for {
		ok, msg := fn()
		if ok {
			return
		}
		last = msg
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s: %s", timeout, what, last)
		}
		time.Sleep(2 * time.Second)
	}
}

// Minimal views of the Kubernetes objects the criteria read.
type meta struct {
	Name              string            `json:"name"`
	Labels            map[string]string `json:"labels"`
	DeletionTimestamp string            `json:"deletionTimestamp"`
}

type condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type node struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		ProviderID    string `json:"providerID"`
		Unschedulable bool   `json:"unschedulable"`
		Taints        []struct {
			Key string `json:"key"`
		} `json:"taints"`
	} `json:"spec"`
	Status struct {
		Conditions []condition `json:"conditions"`
	} `json:"status"`
}

func (n node) ready() bool {
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			return true
		}
	}
	return false
}

func (n node) tainted(key string) bool {
	for _, t := range n.Spec.Taints {
		if t.Key == key {
			return true
		}
	}
	return false
}

// instanceID extracts i-... from a providerID like aws:///us-west-2a/i-0123456789abcdef0.
func (n node) instanceID() string {
	i := strings.LastIndex(n.Spec.ProviderID, "/")
	return n.Spec.ProviderID[i+1:]
}

type nodeList struct {
	Items []node `json:"items"`
}

type pod struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		NodeName string `json:"nodeName"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type podList struct {
	Items []pod `json:"items"`
}

func (r *rig) nodesOf(pool string) ([]node, error) {
	var l nodeList
	err := r.kcJSON(&l, "get", "nodes", "-l", "karpenter.sh/nodepool="+pool)
	return l.Items, err
}

// runningPodOn reports whether an app=<app> pod is Running on the named node.
func (r *rig) runningPodOn(app, nodeName string) (bool, error) {
	var l podList
	if err := r.kcJSON(&l, "get", "pods", "-l", "app="+app); err != nil {
		return false, err
	}
	for _, p := range l.Items {
		if p.Spec.NodeName == nodeName && p.Status.Phase == "Running" {
			return true, nil
		}
	}
	return false, nil
}

// instanceState asks EC2 (through the gateway) for the state of one instance.
func (r *rig) instanceState(id string) (string, error) {
	out, err := r.awsQuery("ec2", "2016-11-15", "DescribeInstances", "InstanceId.1="+id)
	if err != nil {
		return "", err
	}
	const open, closeTag = "<instanceState>", "</instanceState>"
	i, j := strings.Index(out, open), strings.Index(out, closeTag)
	if i < 0 || j < i {
		return "", fmt.Errorf("no instanceState in %.200s", out)
	}
	seg := out[i:j]
	a, b := strings.Index(seg, "<name>"), strings.Index(seg, "</name>")
	if a < 0 || b < a {
		return "", fmt.Errorf("no state name in %.200s", seg)
	}
	return seg[a+len("<name>") : b], nil
}

func (r *rig) scale(t *testing.T, deployment string, n int) {
	t.Helper()
	if _, err := r.kc("scale", "deployment/"+deployment, fmt.Sprintf("--replicas=%d", n)); err != nil {
		t.Fatal(err)
	}
}
```

`test/gate/gate_test.go`:

```go
//go:build gate

// Package gate holds the Karpenter proof gate (G0): eight criteria that must pass from a clean checkout with
// `make gate-karpenter`. The tests run on the host and reach the isolated stack only through docker; nothing
// here talks to a real AWS endpoint.
package gate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/trace"
)

// state carries facts from one criterion to the next (the criteria run in order and share one cluster).
type state struct {
	odNode     node
	odInstance string
}

func TestGate(t *testing.T) {
	r := newRig(t)
	s := &state{}

	t.Run("C1_karpenter_starts", func(t *testing.T) {
		eventually(t, 3*time.Minute, "the Karpenter deployment to be available", func() (bool, string) {
			var d struct {
				Status struct {
					AvailableReplicas int `json:"availableReplicas"`
				} `json:"status"`
			}
			if err := r.kcJSON(&d, "-n", "kube-system", "get", "deploy/karpenter"); err != nil {
				return false, err.Error()
			}
			return d.Status.AvailableReplicas >= 1, "no available replica yet"
		})
		var pods struct {
			Items []struct {
				Status struct {
					ContainerStatuses []struct {
						RestartCount int `json:"restartCount"`
					} `json:"containerStatuses"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := r.kcJSON(&pods, "-n", "kube-system", "get", "pods", "-l", "app.kubernetes.io/name=karpenter"); err != nil {
			t.Fatal(err)
		}
		for _, p := range pods.Items {
			for _, c := range p.Status.ContainerStatuses {
				if c.RestartCount > 0 {
					t.Errorf("Karpenter restarted %d times: it could not get what it needs from the simulated AWS", c.RestartCount)
				}
			}
		}
		logs, err := r.kc("-n", "kube-system", "logs", "deploy/karpenter", "--tail=200")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logs, "panic:") {
			t.Errorf("Karpenter panicked:\n%s", logs)
		}
	})

	t.Run("C2_pending_pod_becomes_a_running_node", func(t *testing.T) {
		r.scale(t, "inflate-ondemand", 1)
		var providerID string
		eventually(t, 3*time.Minute, "a NodeClaim with a provider ID (CreateFleet succeeded)", func() (bool, string) {
			var l struct {
				Items []struct {
					Status struct {
						ProviderID string `json:"providerID"`
					} `json:"status"`
				} `json:"items"`
			}
			if err := r.kcJSON(&l, "get", "nodeclaims", "-l", "karpenter.sh/nodepool=ondemand"); err != nil {
				return false, err.Error()
			}
			if len(l.Items) == 0 || l.Items[0].Status.ProviderID == "" {
				return false, "no NodeClaim with a provider ID yet"
			}
			providerID = l.Items[0].Status.ProviderID
			return true, ""
		})
		s.odInstance = providerID[strings.LastIndex(providerID, "/")+1:]
		eventually(t, 2*time.Minute, "the EC2 instance to be running", func() (bool, string) {
			st, err := r.instanceState(s.odInstance)
			if err != nil {
				return false, err.Error()
			}
			return st == "running", "instance is " + st
		})
		eventually(t, 3*time.Minute, "a Ready Node for that instance and the pod running on it", func() (bool, string) {
			nodes, err := r.nodesOf("ondemand")
			if err != nil {
				return false, err.Error()
			}
			for _, n := range nodes {
				if n.instanceID() == s.odInstance && n.ready() {
					ok, err := r.runningPodOn("inflate-ondemand", n.Metadata.Name)
					if err != nil {
						return false, err.Error()
					}
					if ok {
						s.odNode = n
						return true, ""
					}
					return false, "node is Ready but the pod is not Running on it yet"
				}
			}
			return false, "no Ready node with providerID for " + s.odInstance
		})
	})

	t.Run("C5_cheaper_offering_chosen_from_pricing_data", func(t *testing.T) {
		if s.odNode.Metadata.Name == "" {
			t.Skip("C2 did not produce a node")
		}
		out, err := r.tool("s0probe", "-endpoint", gateway, "-region", region, "-prices", "c5.large,m5.large")
		if err != nil {
			t.Fatal(err)
		}
		var prices map[string]float64
		if err := json.Unmarshal([]byte(out), &prices); err != nil {
			t.Fatalf("prices output %q: %v", out, err)
		}
		if prices["c5.large"] == prices["m5.large"] {
			t.Fatalf("both types cost %v: the price data cannot distinguish them, so this criterion would prove nothing", prices["c5.large"])
		}
		want := "c5.large"
		if prices["m5.large"] < prices["c5.large"] {
			want = "m5.large"
		}
		if got := s.odNode.Metadata.Labels["node.kubernetes.io/instance-type"]; got != want {
			t.Errorf("Karpenter launched %s but the cheapest allowed type is %s (prices %v)", got, want, prices)
		}
	})

	t.Run("C6_ami_alias_and_instance_profile", func(t *testing.T) {
		var nc struct {
			Status struct {
				AMIs []struct {
					ID string `json:"id"`
				} `json:"amis"`
				InstanceProfile string      `json:"instanceProfile"`
				Conditions      []condition `json:"conditions"`
			} `json:"status"`
		}
		if err := r.kcJSON(&nc, "get", "ec2nodeclass", "default"); err != nil {
			t.Fatal(err)
		}
		ready := false
		for _, c := range nc.Status.Conditions {
			ready = ready || (c.Type == "Ready" && c.Status == "True")
		}
		if !ready {
			t.Errorf("EC2NodeClass is not Ready: %+v", nc.Status.Conditions)
		}
		want := "ami-0a1b2c3d4e5f60718"
		if b, err := os.ReadFile(filepath.Join(r.root, ".ami-id")); err == nil {
			want = strings.TrimSpace(string(b))
		}
		if len(nc.Status.AMIs) == 0 || nc.Status.AMIs[0].ID != want {
			t.Errorf("status.amis = %+v, want the seeded AMI %s (resolved through the SSM alias)", nc.Status.AMIs, want)
		}
		if nc.Status.InstanceProfile == "" {
			t.Fatal("EC2NodeClass has no status.instanceProfile")
		}
		out, err := r.awsQuery("iam", "2010-05-08", "GetInstanceProfile", "InstanceProfileName="+nc.Status.InstanceProfile)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "KarpenterNodeRole-meridian-g0") {
			t.Errorf("instance profile %s does not carry the node role:\n%.400s", nc.Status.InstanceProfile, out)
		}
	})

	t.Run("C3_consolidation_terminates_instance_and_deletes_node", func(t *testing.T) {
		if s.odNode.Metadata.Name == "" {
			t.Skip("C2 did not produce a node")
		}
		r.scale(t, "inflate-ondemand", 0)
		eventually(t, 4*time.Minute, "the empty node to be deleted", func() (bool, string) {
			nodes, err := r.nodesOf("ondemand")
			if err != nil {
				return false, err.Error()
			}
			for _, n := range nodes {
				if n.Metadata.Name == s.odNode.Metadata.Name {
					return false, "node " + n.Metadata.Name + " still exists"
				}
			}
			return true, ""
		})
		eventually(t, 2*time.Minute, "the EC2 instance to be terminated", func() (bool, string) {
			st, err := r.instanceState(s.odInstance)
			if err != nil {
				return false, err.Error()
			}
			return st == "shutting-down" || st == "terminated", "instance is " + st
		})
	})

	t.Run("C4_spot_interruption_cordons_drains_and_replaces", func(t *testing.T) {
		r.scale(t, "inflate-spot", 1)
		var first node
		eventually(t, 4*time.Minute, "a Ready spot node with the pod running", func() (bool, string) {
			nodes, err := r.nodesOf("spot")
			if err != nil {
				return false, err.Error()
			}
			for _, n := range nodes {
				if n.ready() {
					if ok, _ := r.runningPodOn("inflate-spot", n.Metadata.Name); ok {
						first = n
						return true, ""
					}
				}
			}
			return false, "no Ready spot node with a Running pod yet"
		})
		args := []string{"-gateway", gateway, "-queue", queue, "interrupt", "-grace", "10s"}
		if os.Getenv("GATE_ANY_MARKET") == "1" { // for a backend that omits instanceLifecycle; recorded in docs/s0/decision.md
			args = append(args, "-any-market")
		}
		if out, err := r.tool("simctl", append(args, first.instanceID())...); err != nil {
			t.Fatalf("simctl interrupt failed: %v\n%s", err, out)
		}
		eventually(t, 5*time.Minute, "a replacement node running the pod, and the old instance gone", func() (bool, string) {
			nodes, err := r.nodesOf("spot")
			if err != nil {
				return false, err.Error()
			}
			for _, n := range nodes {
				if n.instanceID() != first.instanceID() && n.ready() {
					if ok, _ := r.runningPodOn("inflate-spot", n.Metadata.Name); ok {
						st, err := r.instanceState(first.instanceID())
						if err != nil {
							return false, err.Error()
						}
						return st == "shutting-down" || st == "terminated", "old instance is " + st
					}
				}
			}
			return false, "no replacement node with a Running pod yet"
		})
		r.scale(t, "inflate-spot", 0)
	})

	t.Run("C7_no_route_to_the_internet", func(t *testing.T) {
		out, err := r.sh(30*time.Second, "docker", "network", "inspect", "-f", "{{.Internal}}", network)
		if err != nil || strings.TrimSpace(out) != "true" {
			t.Fatalf("network %s is not internal: %q %v", network, out, err)
		}
		// A public AWS endpoint must be unreachable from a container on the network...
		if out, err := r.tool("simctl", "-timeout", "5s", "probe", "https://ec2.us-west-2.amazonaws.com"); err == nil {
			t.Errorf("a container on the gate network reached a public AWS endpoint:\n%s", out)
		}
		// ...and from the Kubernetes node itself, which is where Karpenter's pod runs.
		if out, err := r.sh(30*time.Second, "docker", "exec", "k3d-meridian-g0-server-0", "wget", "-q", "-T", "5", "-O", "-", "https://sts.amazonaws.com"); err == nil {
			t.Errorf("the cluster reached a public AWS endpoint:\n%s", out)
		}
	})

	t.Run("C8_call_surface_matches_the_golden_file", func(t *testing.T) {
		time.Sleep(12 * time.Second) // the gateway flushes its recording every 10 seconds
		goldenPath := filepath.Join(r.root, "trace", "golden", "karpenter-calls.golden")
		got, err := trace.ReadGolden(filepath.Join(r.root, "trace", "gate", "calls.golden"))
		if err != nil {
			t.Fatal(err)
		}
		want, err := trace.ReadGolden(goldenPath)
		if err != nil {
			t.Fatalf("no golden file yet: review trace/gate/calls.golden and run `make golden-update` (%v)", err)
		}
		added, removed := trace.Diff(want, got)
		if len(added)+len(removed) > 0 {
			t.Errorf("Karpenter's AWS call surface changed.\n  now calls (not in golden): %v\n  no longer calls: %v\nReview the diff, then `make golden-update`.", added, removed)
		}
		raw, err := os.ReadFile(filepath.Join(r.root, "trace", "gate", "calls.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rec struct {
			Unsupported []string `json:"unsupported"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		if len(rec.Unsupported) > 0 {
			t.Errorf("Karpenter called operations that ops.yaml does not list: %v", rec.Unsupported)
		}
	})
}
```

- [ ] **Step 2: Check that it compiles and that the normal test run ignores it**

Run: `gofmt -l test && go vet -tags gate ./test/gate/ && go vet ./... && go test ./... 2>&1 | tail -3`
Expected: no gofmt output; vet clean with and without the tag; the normal test run does not mention `test/gate`.

- [ ] **Step 3: Run the gate for the first time**

Run: `make gate-karpenter`
Expected: this is where the plan meets reality. C8 fails on a first run because there is no golden file yet; every other criterion should pass if S0's decisions were right. For each failing criterion:

1. Run with the stack kept: `GATE_KEEP=1 make gate-karpenter`, then look: `scripts/kc.sh -n kube-system logs deploy/karpenter --tail=100`, `scripts/kc.sh get nodeclaims,nodes`, `scripts/kc.sh -n kube-system logs deploy/node-registrar`, and `cat trace/gate/calls.json`.
2. Decide which component is wrong. If Karpenter gets a 400 `UnsupportedOperation`, the operation belongs in `ops.yaml` (add it, with a backend, and make that backend serve it). If a backend returns a field Karpenter cannot decode, add a check for that field to `internal/s0probe/checks.go` (test first), re-run `make s0-probe`, and re-apply `make s0-decide`: the gate has just taught the probe something.
3. Fix, then re-run from the top. The gate always tears down first, so no run depends on the one before it.

- [ ] **Step 4: Accept the golden call surface**

When C1 to C7 pass: read `trace/gate/calls.golden` line by line against `ops.yaml` (each entry should be a call Karpenter really makes, not noise), then:

Run: `make golden-update && make gate-karpenter`
Expected: all eight criteria PASS, including C8 against the file you just accepted.

- [ ] **Step 5: Decision checkpoint (spec: after criteria 1 to 3 pass, or after three weeks, whichever comes first)**

Write `docs/checkpoints/g0.md` with: the date; which of C1 to C8 pass; the S0 outcome (how many of the supported operations Floci serves versus custom services, from `docs/s0/decision.md`); the number of operations in `ops.yaml`; what was harder than expected; and a recommendation (continue to the next plan, or narrow scope, with the reason).

**Stop here and ask the user** whether to continue or narrow scope. The next plans (S1 and M0 to M10) are not started until they answer.

- [ ] **Step 6: Commit**

```bash
git add test/gate trace/golden docs/checkpoints Makefile
git commit -m "feat: add the Karpenter proof gate test and the golden call surface"
```

### Task 17: Decision records, README wording, CI, and the final check

**Files:**
- Create: `docs/decisions/ADR-0002-adopt-floci-where-it-passes.md`, `docs/decisions/ADR-0003-handwritten-aws-protocol-codecs.md`
- Modify: `docs/decisions/INDEX.md`, `docs/decisions/ADR-0001-simulated-aws-plane-and-karpenter-gate.md`
- Create: `README.md`, `.github/workflows/gate.yml`

**Interfaces:**
- Consumes: the S0 outcome (`docs/s0/decision.md`), the gate result (Task 16).
- Produces: records a reader can use to see why Floci was adopted for some operations and not others; the public wording; CI that runs the gate on a fresh runner (criterion 8).

**Acceptance Criteria:**
- The README says "Not validated against real AWS" and makes no parity claim.
- ADR-0002 states the Docker-socket risk and that which operations Floci serves is recorded data, not design.
- The ADR index lists all three records; ADR-0001 notes that ADR-0002 amends its decision 1.
- CI runs vet, unit tests and script tests, then `make gate-karpenter` on a clean runner, and keeps the recorded call surface as an artifact.

- [ ] **Step 1: Day-1 generator check (time-boxed)**

Spend at most half a day: search for a maintained Go server-side generator for AWS's published Smithy models (look at `smithy-go`'s README and issues, the `aws-sdk-go-v2` repository, and a GitHub code search for "smithy" with Go server generation). If none is usable, record that with the date and the links you checked at the end of `docs/decisions/ADR-0003-handwritten-aws-protocol-codecs.md`. If one is usable, stop and tell the user before going further.

- [ ] **Step 2: Write the decision records**

`docs/decisions/ADR-0002-adopt-floci-where-it-passes.md`:

```markdown
# ADR-0002: Adopt Floci for the simulated AWS plane where it passes the S0 contract probe

Date: 2026-10-09
Status: Proposed (becomes Accepted when the user approves the S0 decision in `docs/s0/decision.md`)
Amends: ADR-0001 (decision 1: which components serve the AWS surface)
Spec: `.internal/specs/2026-10-09-meridian-platform-design.md`

## Context

ADR-0001 planned a custom Go facade for each AWS service Karpenter touches (`ec2-sim`, `pricing-facade`, `eks-facade`) plus Moto for IAM, STS, SQS and SSM. Floci (floci-io/floci, MIT) is an open-source AWS emulator that already implements most of that surface, including Docker-backed EC2 and EKS. Writing an EC2 simulator, a Pricing facade and an EKS stub is the largest block of custom work in G0, so using Floci for whatever it serves correctly could remove most of it. Whether it serves each operation correctly enough for real Karpenter is unknown until measured.

## Decision

1. Run a spike (S0) before committing to either path. The `s0probe` contract probe sends the AWS calls Karpenter makes through our `aws-gateway` to Floci and records a pass or fail per operation.
2. Route each operation to a backend by name in `ops.yaml` (`backend: floci`, `ec2-sim`, `pricing-facade`, `eks-facade` or `moto`). The gateway sends every operation to the backend its entry names. An operation stays on Floci only if every probe check for it passed; otherwise it moves to the custom backend for its service. Operations that share instance state (`ops.Coupled`) move together.
3. Build the custom backends only for operations S0 moved (Tasks 11 to 14 are conditional). The pieces that no emulator provides are built regardless: the spot interruption injector (`spot`, `simctl`; it uses public AWS calls only, so it works with any backend) and `node-registrar` (instances become KWOK nodes).
4. The gateway stays in front of everything. It owns SigV4 verification, the supported-operation list and the recorded call surface, none of which Floci provides (it accepts any credentials by default).

## Rationale

- A measured, per-operation answer beats a guess in either direction. If Floci passes everything, the custom services are never written; if it passes nothing, the plan degrades to ADR-0001 with no wasted work.
- Per-operation routing lets one service be split (for example Floci for launching instances and `ec2-sim` for spot prices) without changing any client.
- Keeping the gateway means the public claim stays checkable: the operation list, fidelity labels and golden call surface are ours, whatever sits behind them.

## Consequences

- A new dependency on a young project. Floci is pinned by digest in `versions.yaml`; behavior changes on upgrade are caught by re-running the probe and the gate.
- Floci launches EC2 instances and EKS clusters as Docker containers, so by default it needs the host's Docker socket, which is root-equivalent access to the Docker daemon. G0 first tries Floci with EC2 container execution off (Karpenter needs instance records, and KWOK supplies the Nodes). Where the socket is still needed, Floci reaches it only through a filtering proxy that denies exec, host binds, privileged containers and image builds, and the gate fails if any service mounts `/var/run/docker.sock` directly. The stack publishes no ports and the gate runs it on a network with no route out. Do not run it on a shared machine.
- Any Floci-served operation can be moved to another backend by changing its `backend` line in `ops.yaml`. The S0 result is valid only for the pinned Floci version; CI fails if the pin changes without a refreshed `docs/s0/` report, and the README records the digest and a pinned-commit build-from-source fallback.
- A probe pass means an operation is eligible for Floci, not proven. Real Karpenter (gate criteria 1 to 3, run before the S0 table is approved) is the final check.
- Instances launched by Floci are real containers, but KWOK still supplies the Nodes, so Floci's instance containers prove only that the EC2 API works, not that pods run on them.
- Which operations Floci serves is data, not design: `docs/s0/report.md` and `docs/s0/decision.md` are the record, and re-running `make s0-probe` after a Floci upgrade can move operations back or forth.
- The public wording is unchanged: the platform is not validated against real AWS, and no "1:1 parity" claim is made until the conformance report exists.
```

`docs/decisions/ADR-0003-handwritten-aws-protocol-codecs.md`:

```markdown
# ADR-0003: Handwritten AWS protocol codecs for G0, not a Smithy-model generator

Date: 2026-10-09
Status: Accepted
Spec: `.internal/specs/2026-10-09-meridian-platform-design.md` (section 4, G0 "Day-1 check"; section 6, "API generation")

## Context

The spec asks for a day-1 check: is there an existing Go server-side generator for AWS's published Smithy models? If not, build a small in-repo generator, time-boxed to three days, and fall back to handwritten codecs per protocol if it is not paying off. Smithy's Go tooling (`smithy-go`, which `aws-sdk-go-v2` is generated with) targets clients; no maintained Go server-side generator for AWS's models was found when this plan was written.

## Decision

G0 uses handwritten codecs, one per protocol family (`internal/awsapi`: XML query and EC2, JSON-RPC, REST-JSON), verified by tests that use AWS's own published SigV4 example and, where the SDK is available, the real `aws-sdk-go-v2` clients as the oracle. The in-repo generator is not built in G0. Validation of the codecs against the Smithy models themselves moves to the conformance-report plan, where it belongs.

## Rationale

- With Floci (ADR-0002) serving most operations, G0's hand-written protocol code shrinks to the gateway's request parsing and the few fallback services, which do not justify a generator.
- The three-day time-box exists to avoid sinking effort into tooling; taking the named fallback up front honors it.

## Consequences

- Operation shapes in the fallback services are handwritten and covered by tests that decode the XML or JSON the way Karpenter's SDK does, not by generated types.
- If a maintained Go server-side generator for AWS models appears, it can replace the fallback handlers without touching `ops.yaml` or the gateway.
- Re-check before accepting this ADR: spend at most half a day searching for an existing Go server-side Smithy generator. If a usable one exists, stop and tell the user: it would replace the handwritten codecs in Tasks 3 to 5 and 11 to 13.
```

Change the status line of ADR-0001 to `Accepted (decision 1 amended by ADR-0002)`, and replace `docs/decisions/INDEX.md` with:

```markdown
# Architecture Decision Records

| ADR | Title | Status | Date |
|---|---|---|---|
| [ADR-0001](ADR-0001-simulated-aws-plane-and-karpenter-gate.md) | Simulated AWS plane with a Karpenter proof gate (G0) | Accepted (decision 1 amended by ADR-0002) | 2026-10-09 |
| [ADR-0002](ADR-0002-adopt-floci-where-it-passes.md) | Adopt Floci for the simulated AWS plane where it passes the S0 contract probe | Proposed until the S0 decision is approved | 2026-10-09 |
| [ADR-0003](ADR-0003-handwritten-aws-protocol-codecs.md) | Handwritten AWS protocol codecs for G0, not a Smithy-model generator | Accepted | 2026-10-09 |
```

When the user approved the S0 decision in Task 8, change ADR-0002's status to `Accepted` in both files.

- [ ] **Step 3: Write the README and the CI workflow**

`README.md`:

```markdown
# meridian

A local, reproducible Kubernetes and AWS platform-engineering showcase. Nothing here talks to real AWS: every AWS call goes to a local gateway that fronts an open-source AWS emulator (Floci) and, only where the emulator falls short, small custom services.

**Not validated against real AWS.** The platform is checked against the behavior real AWS clients depend on (see the contract probe report in `docs/s0/`), not against AWS itself. No API-parity claim is made until the generated conformance report exists.

## Where things stand

The first milestone is the Karpenter proof gate: real, unmodified Karpenter provisions, consolidates and replaces (after a spot interruption) KWOK-backed nodes against the simulated AWS plane, on a network with no route to the internet.

```bash
make init            # generates random local credentials into .env (never committed)
make gate-karpenter  # tears everything down, builds the stack, runs the eight gate criteria, tears down
```

Requirements: Docker, Go (version in `go.mod`), k3d and Helm (versions in `versions.yaml`), Python 3.

## How the pieces fit

| Piece | Role |
|---|---|
| `aws-gateway` | Verifies SigV4, allows only operations listed in `ops.yaml`, routes each operation to its backend, records the call surface |
| Floci | The AWS emulator behind the gateway for every operation it passes (`docs/s0/decision.md`) |
| `ec2-sim`, `pricing-facade`, `eks-facade`, Moto | Fallbacks, started only for operations Floci failed the contract probe on |
| `node-registrar` | Turns running instances into KWOK nodes, the way a kubelet would register |
| `simctl` | Injects spot interruptions with public AWS calls (post to SQS, wait, terminate) |

Design decisions are in `docs/decisions/`.
```

`.github/workflows/gate.yml`:

```yaml
name: gate

on:
  push:
    branches: [main]
  pull_request:
  workflow_dispatch:

permissions:
  contents: read

jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Go vet and tests
        run: go vet ./... && go vet -tags gate ./test/gate/ && go test -race ./...
      - name: Script tests
        run: python3 -W error -m unittest discover -s scripts

  # Gate criterion 8: the proof runs on a fresh runner from a clean checkout, with no state from any earlier run.
  karpenter-gate:
    needs: unit
    runs-on: ubuntu-latest
    timeout-minutes: 60
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Pinned tool versions
        run: make init env-images
      - name: Install k3d (pinned, checksum-verified)
        run: ./scripts/install-k3d.sh
      - name: Karpenter proof gate
        run: make gate-karpenter
      - name: Keep the recorded AWS call surface
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: gate-trace
          path: trace/gate/
```

- [ ] **Step 4: Run everything once more from clean**

Run:

```bash
gofmt -l . ; go vet ./... && go vet -tags gate ./test/gate/ && go test -race ./...
python3 -W error -m unittest discover -s scripts
make gate-karpenter
```

Expected: no gofmt output; all unit and script tests pass; the gate prints all eight criteria PASS and tears the stack down.

- [ ] **Step 5: Commit, then tag only after the user's checkpoint answer**

```bash
git add docs README.md .github
git commit -m "docs: record the Floci adoption, codec and gate decisions; add README and CI"
```

The first release line (`v0.1.0`) is G0 alone. Tag it only when the user has answered the decision checkpoint in `docs/checkpoints/g0.md` (Task 16): `git tag v0.1.0`.

## Coverage: every G0 requirement mapped to a task

| Spec requirement (G0) | Task |
|---|---|
| Trace real Karpenter to fix the exact AWS call list | 6 (recorder), 7 (trace mode and viewer), 16 (golden file, C8) |
| One YAML operation list drives everything | 2 (`ops.yaml`, per-operation backend), 5 (gateway), 8 (probe and `s0matrix.py` read it) |
| Day-1 generator check, 3-day box, handwritten fallback | 17 Step 1, ADR-0003 |
| `ec2-sim` (fleet, launch templates, instance and offering lookups, subnets, security groups, images, tagging) | Served by Floci wherever the probe passes (7, 8); custom `ec2-sim` for the rest (11, 12) |
| `node-registrar` (KWOK Node on `running`, deleted on terminate) | 10, deployed in 15 |
| `pricing-facade` (GetProducts, spot price history) | Floci or 13 (GetProducts); spot price history is an EC2 reference operation (8, 11, 12) |
| SSM parameters (AMI aliases), IAM instance profiles, STS | Floci (7, 8); Moto where it fails (14); seeded in 7 |
| SQS interruption queue fed by spot events | Queue: 7, 14. Events: `simctl`/`spot` (9), a surfaced change from "fed by `ec2-sim`" |
| Minimal `eks-facade` stub serving DescribeCluster | Floci EKS (7, 8) or 13 |
| Hand-created k3d cluster; full `eks-facade` stays in M0 | 7, 15 |
| Criterion 1: Karpenter starts, no real credentials, no startup errors | 16 C1 |
| Criterion 2: pending pod to NodeClaim, CreateFleet, running instance, Ready Node, scheduled pod | 16 C2 |
| Criterion 3: consolidation terminates the instance and deletes the Node | 16 C3 |
| Criterion 4: spot interruption to cordon, drain, replacement | 9 (injector), 16 C4 |
| Criterion 5: cheaper offering chosen from pricing data | 8 (`OnDemandPrices`), 16 C5 |
| Criterion 6: AMI alias through SSM, instance profile through IAM | 7 (seed), 16 C6 |
| Criterion 7: egress blocked, zero outbound calls | 15 (internal network), 16 C7 |
| Criterion 8: repeatable from a clean checkout | 15 (`gate-up` starts from teardown), 16 C8, 17 (CI on a fresh runner) |
| Decision checkpoint after criteria 1 to 3 or 3 weeks | 16 Step 5 |
| Security: random keys, no bypass flags, loopback/internal only | 1, 6, 7, 15 |
| Version pinning by digest/version; EKS version from pinned k3s | 1, 7, 13 |
| Public wording "Not validated against real AWS" | 17 (README) |
| Node backends: `kwok` default with KWOK's taint; `real` selectable | `kwok` in 10; `real` is an explicit skip with a clear error, built in M0 |
| **Not in this plan (deferred, tracked):** S1 (Spinnaker slot), M0 to M10, the `real` node backend, the conformance report and Smithy-model validation of codecs, Terraform/Argo CD/observability/Istio milestones | Later plans, after the G0 checkpoint |

## Stress Test Results: Floci-first G0 plan

### Resolved Decisions
All 9 branches were agreed as recommended (9/9).
1. Docker socket: filtered proxy, and try Floci with EC2 container execution off first (Scope 3).
2. EC2 is the swing factor; probe `CreateFleet` first and stop early (Scope 3a).
3. Moto is a second EC2 candidate before `ec2-sim` (Scope 3b).
4. A probe pass means eligible, not proven; early criteria 1 to 3 run before the table is approved (Scope 3c).
5. Criterion 7 asserts network topology and probes from the Karpenter pod and Floci (Scope 3d).
6. Criterion 4 must prove the interruption path ran, using a golden message (Scope 3e).
7. The S0 result is tied to the pinned Floci version; build-from-source fallback documented (Scope 3f).
8. Task 0 smoke test first; Tasks 11 to 14 expand when triggered (Scope 3g).
9. Bounded NodePool `limits`, a pre-flight resource check, and measured peak memory recorded as the README minimum.

### Changes Made
Only the Scope section was edited (items 3, 3a to 3g). The task text and code were **not** changed, and the verified code in this plan predates these decisions. They need to be applied to Tasks 0, 7, 8, 9, 14, 16 and 17 and CI before execution. The "verified" status therefore covers the code as originally written, not these amendments.

### Deferred / Parking Lot
- Smithy-model codec validation (ADR-0003) and the `real` node backend (M0), unchanged.

### Confidence Assessment
- Overall: Medium. The design is sound, but the amendments above are decisions on paper, and Floci's actual fidelity is still unmeasured until Task 0 and S0.
- Areas of concern: all nine branches were accepted without pushback, so none of the recommendations was tested against a competing view. Moto's EC2 capability (branch 3) is an untested assumption.

<!-- PLAN-END -->
