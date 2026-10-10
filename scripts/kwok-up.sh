#!/usr/bin/env bash
# Applies the vendored KWOK release (CRDs, RBAC, controller, node/pod stages) to the running cluster.
# Idempotent. The controller image is pinned to the digest in versions.yaml (KWOK_IMAGE in .env.images).
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env.images; set +a
KC=scripts/kc.sh
# Refuse to apply files that differ from what the vendoring script recorded.
( cd deploy/kwok && shasum -a 256 -c SHA256SUMS >/dev/null ) || { echo "deploy/kwok does not match SHA256SUMS"; exit 1; }
TAG_REF="registry.k8s.io/kwok/kwok:${KWOK_VERSION}"
[ -n "${KWOK_IMAGE:-}" ] || { echo "KWOK_IMAGE unset: run make env-images"; exit 1; }
# Swap the tag reference for the digest-pinned one, then apply with server-side apply (the CRDs are large).
sed "s#image: ${TAG_REF}\$#image: ${KWOK_IMAGE}#" deploy/kwok/kwok.yaml | grep -q "image: ${KWOK_IMAGE}" \
  || { echo "kwok.yaml does not contain ${TAG_REF}"; exit 1; }
sed "s#image: ${TAG_REF}\$#image: ${KWOK_IMAGE}#" deploy/kwok/kwok.yaml | $KC apply --server-side --force-conflicts -f -
$KC wait --for=condition=Established crd/stages.kwok.x-k8s.io --timeout=60s
$KC apply --server-side --force-conflicts -f - < deploy/kwok/stage-fast.yaml
# A separate field manager keeps the hardening patch from being undone when kwok.yaml is applied again.
$KC apply --server-side --field-manager=kwok-hardening --force-conflicts -f - < deploy/kwok/controller-patch.yaml
$KC -n kube-system rollout status deploy/kwok-controller --timeout=180s
echo "KWOK ${KWOK_VERSION} is up"
