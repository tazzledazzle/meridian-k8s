#!/usr/bin/env bash
# Installs the pinned k3d release on a Linux CI runner and verifies it against the checksums the release publishes.
# On a laptop, install k3d however you like at the version in versions.yaml (bin/k3d is the checksum-verified copy).
set -euo pipefail
cd "${BUILD_WORKSPACE_DIRECTORY:-$(dirname "$0")/..}"   # bazel run: the real workspace, not the runfiles tree
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
