#!/usr/bin/env bash
# Saves the images k3s itself needs (deploy/images.system.txt) into bin/images/system.tar. k3d-up.sh mounts the tar into
# the server at /var/lib/rancher/k3s/agent/images/, which k3s imports at startup, so the system pods (CoreDNS,
# local-path) find their images already present and never try to reach a registry. Pulling happens on the host.
set -euo pipefail
cd "${BUILD_WORKSPACE_DIRECTORY:-$(dirname "$0")/..}"   # bazel run: the real workspace, not the runfiles tree
mkdir -p bin/images
refs=()
while read -r ref; do
  case "$ref" in ''|'#'*) continue ;; esac
  docker image inspect "$ref" >/dev/null 2>&1 || docker pull -q "$ref" >/dev/null
  refs+=("$ref")
done < deploy/images.system.txt
docker save -o bin/images/system.tar "${refs[@]}"
echo "saved ${#refs[@]} system images to bin/images/system.tar"
