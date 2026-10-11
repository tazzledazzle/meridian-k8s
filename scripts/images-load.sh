#!/usr/bin/env bash
# Loads every image the gate cluster needs, because the cluster has no route to a registry.
#   Images named in rendered manifests (stdin): pulled on the host, re-tagged with a local name (localimg.py) and
#      imported; the caller applies manifests rewritten to the same names.
#   (The images k3s itself needs, deploy/images.system.txt, are mounted at cluster creation: images-save-system.sh.)
# Pulling happens on the host, which is outside the isolated network.
set -euo pipefail
cd "${BUILD_WORKSPACE_DIRECTORY:-$(dirname "$0")/..}"   # bazel run: the real workspace, not the runfiles tree
export PATH="$PWD/bin:$PATH"   # the checksum-verified k3d
CLUSTER="${1:-meridian-g0}"
MANIFESTS="$(cat)"

# The list is read into a variable first, so a failing lister stops the script (set -e ignores a failed process substitution).
PAIRS="$(printf '%s\n' "$MANIFESTS" | python3 scripts/localimg.py list)"
while read -r upstream local_ref; do
  [ -n "$upstream" ] || continue
  docker image inspect "$upstream" >/dev/null 2>&1 || docker pull -q "$upstream" >/dev/null
  docker tag "$upstream" "$local_ref"
  k3d image import "$local_ref" --cluster "$CLUSTER" --mode direct >/dev/null
  echo "loaded $local_ref"
done <<< "$PAIRS"
# The k3s system images are not loaded here: they are mounted into the server before it starts (images-save-system.sh).
