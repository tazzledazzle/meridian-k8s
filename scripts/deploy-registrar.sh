#!/usr/bin/env bash
# Builds the node-registrar image, imports it into the k3d cluster and applies its manifest. Idempotent.
set -euo pipefail
cd "${BUILD_WORKSPACE_DIRECTORY:-$(dirname "$0")/..}"   # bazel run: the real workspace, not the runfiles tree
set -a; . ./.env; . ./.env.images; set +a
export PATH="$PWD/bin:$PATH"   # the checksum-verified k3d
KC=scripts/kc.sh
IMG=meridian/node-registrar:s0

# Same Dockerfile as every Go service; the base images are pinned by digest in .env.images.
docker build -q -f deploy/Dockerfile.service --build-arg CMD=node-registrar \
  --build-arg GOLANG_IMAGE="$GOLANG_IMAGE" --build-arg STATIC_IMAGE="$STATIC_IMAGE" -t "$IMG" .
k3d image import "$IMG" --cluster meridian-g0 --mode direct
echo "image id: $(docker image inspect "$IMG" --format '{{.Id}}')"

# The credentials Secret is created by karpenter-install.sh; fail early if it is missing.
$KC -n kube-system get secret aws-creds >/dev/null
$KC apply -f - < deploy/node-registrar.yaml
# The tag does not change between builds, so restart the pod to pick up a re-imported image.
$KC -n kube-system rollout restart deploy/node-registrar
$KC -n kube-system rollout status deploy/node-registrar --timeout=120s
