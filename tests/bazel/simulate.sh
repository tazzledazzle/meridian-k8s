#!/usr/bin/env bash
# Dry-runs a docker-touching Bazel make-target against stub docker/k3d/helm in a scratch workspace, so the
# step order and arguments can be checked without a Docker daemon. Nothing real is started or stopped.
#   tests/bazel/simulate.sh s0-up        (also: s0-down, kwok-up, deploy-registrar, seed, s0-probe, ...)
# Prints the target's output and the calls the stubs recorded. The stubs succeed with empty output, so a target
# that parses docker output (s0-up's seeding) stops there; that is expected.
set -euo pipefail
target="${1:?usage: simulate.sh <make-target>}"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
mkdir -p "$scratch/ws" "$scratch/stubs"
cp -R "$repo/scripts" "$repo/deploy" "$repo/ops.yaml" "$repo/versions.yaml" "$scratch/ws/"
for c in docker k3d helm; do
  printf '#!/bin/bash\necho "STUB %s $*" >> "%s/log"\nexit 0\n' "$c" "$scratch/stubs" > "$scratch/stubs/$c"
  chmod +x "$scratch/stubs/$c"
done
cd "$repo"
set +e
PATH="$scratch/stubs:$PATH" bazelisk run --run_under="env BUILD_WORKSPACE_DIRECTORY=$scratch/ws" "//tools/make:$target" 2>&1 | grep -v '^INFO\|^Analyzing\|^Loading\|^$\|^Target\|^  bazel-bin'
echo "--- calls recorded by the stubs"
cut -c1-170 "$scratch/stubs/log" 2>/dev/null || echo "(none)"
