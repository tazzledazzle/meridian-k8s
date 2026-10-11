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
# Any container-runtime socket (*.sock: docker.sock, podman.sock, containerd.sock, docker.proxy.sock) or a bind of a
# directory that holds one (/, /var, /var/run, /run) is root-equivalent on the host's daemon.
bad=$(grep -nE '\.sock\b|(source|target): *["]?(/|/var|/var/run|/run)/?["]?$|^ *- *["]?(/|/var|/var/run|/run)/?:' <<<"$rendered" | grep -vE 'target: *["]?/(run|var/run)/?["]?$' || true)
if [ -n "$bad" ]; then
  echo "FAIL: a service mounts a container-runtime socket or a directory holding one:" >&2
  echo "$bad" >&2
  exit 1
fi
echo "ok: no service mounts the Docker socket"
