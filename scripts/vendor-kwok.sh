#!/usr/bin/env bash
# Downloads the pinned KWOK release manifests into deploy/kwok/ so the stack can apply them without a network.
# Run once online (and again when versions.yaml changes the kwok version); the files are committed.
set -euo pipefail
cd "${BUILD_WORKSPACE_DIRECTORY:-$(dirname "$0")/..}"   # bazel run: the real workspace, not the runfiles tree
set -a; . ./.env.images; set +a
V="${KWOK_VERSION:?run make env-images}"
OUT=deploy/kwok
# Download into a scratch directory first so a failed or tampered download never replaces the committed files.
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
for f in kwok.yaml stage-fast.yaml; do
  curl -fsSL "https://github.com/kubernetes-sigs/kwok/releases/download/${V}/${f}" -o "${TMP}/${f}"
done
# The controller image tag in the manifest must be the version we pinned by digest.
grep -q "image: registry.k8s.io/kwok/kwok:${V}\$" "${TMP}/kwok.yaml" || { echo "kwok.yaml does not reference kwok:${V}"; exit 1; }
# Keep the pull policy from ever reaching a registry once the image is preloaded.
sed -i.bak 's/imagePullPolicy: Always/imagePullPolicy: IfNotPresent/' "${TMP}/kwok.yaml" && rm -f "${TMP}/kwok.yaml.bak"
mkdir -p "$OUT"
cp "${TMP}/kwok.yaml" "${TMP}/stage-fast.yaml" "$OUT/"
# Record the checksums, then verify them straight away so the file on disk is known to match.
( cd "$OUT" && shasum -a 256 kwok.yaml stage-fast.yaml > SHA256SUMS && shasum -a 256 -c SHA256SUMS )
echo "vendored KWOK ${V} into ${OUT}"
