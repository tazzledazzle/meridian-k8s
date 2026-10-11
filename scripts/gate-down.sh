#!/usr/bin/env bash
# Removes everything the gate and the S0 stack created: the cluster, both Compose projects and both networks, so no
# state is inherited. Only the project's own resources are touched (fixed names), never other containers.
# The recorded call list in trace/gate is kept so `make golden-update` can use it after a run.
set -uo pipefail
cd "${BUILD_WORKSPACE_DIRECTORY:-$(dirname "$0")/..}"   # bazel run: the real workspace, not the runfiles tree
export PATH="$PWD/bin:$PATH"   # the checksum-verified k3d
k3d cluster delete meridian-g0 >/dev/null 2>&1
for project in meridian-gate meridian-g0; do
  docker compose -p "$project" --profile '*' down -v --remove-orphans >/dev/null 2>&1
done
# Floci runs EC2/EKS in mock mode and launches no containers, so nothing else can leak. If that changes, record the
# label Floci puts on them in deploy/floci.label (key=value) and they are removed here.
[ -s deploy/floci.label ] && docker ps -aq --filter "label=$(cat deploy/floci.label)" | xargs docker rm -f >/dev/null 2>&1
for net in meridian-gate meridian; do docker network rm "$net" >/dev/null 2>&1; done
exit 0
