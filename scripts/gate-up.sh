#!/usr/bin/env bash
# Brings up the isolated gate stack from a clean state: Floci, the gateway and any fallback backends on a Docker
# network with no route out; a k3d cluster on that network; preloaded images; KWOK, node-registrar, real Karpenter
# and the NodePools. Always starts with gate-down.sh, so repeated runs never inherit state.
set -euo pipefail
cd "${BUILD_WORKSPACE_DIRECTORY:-$(dirname "$0")/..}"   # bazel run: the real workspace, not the runfiles tree
export PATH="$PWD/bin:$PATH"   # the checksum-verified k3d
export MERIDIAN_NET=meridian-gate

./scripts/gate-precheck.sh
./scripts/gate-down.sh
rm -rf trace/gate && mkdir -p trace/gate     # a fresh recording for this run

[ -n "${MERIDIAN_PREBUILT:-}" ] || make init env-images build-linux >/dev/null   # Bazel's dispatcher does these itself
export MERIDIAN_UID="$(id -u)" MERIDIAN_GID="$(id -g)"
set -a; . ./.env; . ./.env.images; set +a
./scripts/check-no-sock.sh deploy/compose.gate.yaml
KC=scripts/kc.sh
COMPOSE="docker compose --env-file .env.images --env-file .env -f deploy/compose.gate.yaml"

# Tools image (awscall, simctl, s0probe) and the registrar image are built on the host.
docker build -q -f deploy/Dockerfile.tools --build-arg STATIC_IMAGE="$STATIC_IMAGE" -t meridian/tools:gate bin/linux >/dev/null
docker build -q -f deploy/Dockerfile.service --build-arg CMD=node-registrar \
  --build-arg GOLANG_IMAGE="$GOLANG_IMAGE" --build-arg STATIC_IMAGE="$STATIC_IMAGE" -t meridian/node-registrar:gate . >/dev/null

COMPOSE_PROFILES="$(python3 scripts/profiles.py ops.yaml)" $COMPOSE up -d --build
docker network inspect meridian-gate --format '{{.Internal}}' | grep -q true || { echo "FAIL: meridian-gate is not internal"; exit 1; }

./scripts/seed-floci.sh
if python3 scripts/profiles.py ops.yaml | grep -q moto; then ./scripts/seed-moto.sh; fi
./scripts/images-save-system.sh
./scripts/k3d-up.sh
k3d image import meridian/node-registrar:gate meridian/tools:gate --cluster meridian-g0 --mode direct >/dev/null   # tools: the gate probes egress from inside a pod

# Render every manifest once, load the images they name, then apply the rewritten (local-name) copies.
KWOK_TAG="registry.k8s.io/kwok/kwok:${KWOK_VERSION}"
RENDER="$(mktemp)"
trap 'rm -f "$RENDER"' EXIT
{
  helm template karpenter oci://public.ecr.aws/karpenter/karpenter --version "$KARPENTER_VERSION" \
    --namespace kube-system --kube-version "${K3S_VERSION%%+*}" --include-crds --values deploy/karpenter-values.yaml
  echo '---'; sed "s#image: ${KWOK_TAG}\$#image: ${KWOK_IMAGE}#" deploy/kwok/kwok.yaml
  echo '---'; cat deploy/workloads/gate/inflate.yaml
} > "$RENDER"
grep -q "image: ${KWOK_IMAGE}" "$RENDER" || { echo "kwok.yaml does not contain ${KWOK_TAG}"; exit 1; }
( cd deploy/kwok && shasum -a 256 -c SHA256SUMS >/dev/null ) || { echo "deploy/kwok does not match SHA256SUMS"; exit 1; }
./scripts/images-load.sh meridian-g0 < "$RENDER"

# Credentials Secret used by Karpenter and node-registrar (lives only in this local cluster).
$KC -n kube-system create secret generic aws-creds \
  --from-literal=AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" --from-literal=AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
  --dry-run=client -o yaml | $KC apply -f -

# KWOK CRDs come first: the stages and the Karpenter objects need them established.
python3 scripts/localimg.py rewrite < "$RENDER" | $KC apply --server-side --force-conflicts -f -
$KC wait --for=condition=Established crd/stages.kwok.x-k8s.io crd/nodepools.karpenter.sh crd/ec2nodeclasses.karpenter.k8s.aws --timeout=90s
$KC apply --server-side --force-conflicts -f - < deploy/kwok/stage-fast.yaml
$KC apply --server-side --field-manager=kwok-hardening --force-conflicts -f - < deploy/kwok/controller-patch.yaml
sed 's#meridian/node-registrar:s0#meridian/node-registrar:gate#' deploy/node-registrar.yaml | $KC apply -f -
$KC -n kube-system rollout status deploy/kwok-controller --timeout=180s
$KC -n kube-system rollout status deploy/karpenter --timeout=180s
$KC -n kube-system rollout status deploy/node-registrar --timeout=120s
$KC apply -f - < deploy/workloads/gate/nodepools.yaml
./scripts/gate-check-offline.sh
echo "gate stack is up (network $MERIDIAN_NET has no route out)"
