#!/usr/bin/env bash
# Fails if any service in the given Compose files mounts the Docker socket (handoff A1).
# Usage: check-no-sock.sh <compose-file>...   (reads the fully rendered config, so overrides and anchors count)
set -euo pipefail
cd "${BUILD_WORKSPACE_DIRECTORY:-$(dirname "$0")/..}"   # bazel run: the real workspace, not the runfiles tree
args=()
for f in "$@"; do args+=(-f "$f"); done
# Placeholders for the variables the file requires: they are only needed to render it, not for this check.
rendered=$(MERIDIAN_UID=0 MERIDIAN_GID=0 AWS_ACCESS_KEY_ID=x AWS_SECRET_ACCESS_KEY=x \
  FLOCI_IMAGE=x GOLANG_IMAGE=x STATIC_IMAGE=x MOTO_IMAGE=x docker compose "${args[@]}" --profile '*' config)
if grep -q 'docker\.sock' <<<"$rendered"; then
  echo "FAIL: a service mounts the Docker socket:" >&2
  grep -n 'docker\.sock' <<<"$rendered" >&2
  exit 1
fi
echo "ok: no service mounts the Docker socket"
