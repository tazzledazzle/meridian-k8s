#!/usr/bin/env bash
# Creates the G0 k3d cluster on the Docker network "meridian" so pods can resolve the gateway by name.
set -euo pipefail
cd "${BUILD_WORKSPACE_DIRECTORY:-$(dirname "$0")/..}"   # bazel run: the real workspace, not the runfiles tree
set -a; . ./.env.images; set +a
export PATH="$PWD/bin:$PATH"   # picks up the checksum-verified bin/k3d when k3d is not installed system-wide

command -v k3d >/dev/null || { echo "k3d is required (see versions.yaml for the pinned version)"; exit 1; }

# The network is created by Compose (make s0-up and gate-up run Compose first).
NET="${MERIDIAN_NET:-meridian}"   # the gate uses the isolated network meridian-gate
docker network inspect "$NET" >/dev/null 2>&1 || { echo "network '$NET' missing: run docker compose first"; exit 1; }

if k3d cluster list meridian-g0 >/dev/null 2>&1; then
  echo "cluster meridian-g0 already exists"
else
  # --no-lb: no load balancer container. --network: shares the Compose network.
  # traefik, servicelb and metrics-server are unused by G0; disabling them also shortens the offline image list.
  # --api-port: k3d publishes the API server on the host, by default on every interface (0.0.0.0). Bind it to loopback;
  # on the internal gate network Docker opens no host port at all, and kubectl goes through scripts/kc.sh.
  EXTRA=(--api-port 127.0.0.1:0)
  if docker network inspect "$NET" --format '{{.Internal}}' | grep -q true; then
    # The gate network is internal: Docker gives containers no gateway, so k3s needs the in-container default route.
    EXTRA+=(--volume "$PWD/deploy/k3d-entrypoint-route.sh:/bin/k3d-entrypoint-route.sh@server:0")
    # k3s imports tarballs from this directory at startup: the system images are there before any pod is scheduled.
    [ -f bin/images/system.tar ] || { echo "bin/images/system.tar missing: run scripts/images-save-system.sh first"; exit 1; }
    EXTRA+=(--volume "$PWD/bin/images/system.tar:/var/lib/rancher/k3s/agent/images/system.tar@server:0")
  fi
  k3d cluster create meridian-g0 \
    --image "${K3S_IMAGE}" \
    --network "$NET" \
    --servers 1 --agents 0 \
    --no-lb \
    "${EXTRA[@]}" \
    --k3s-arg "--disable=traefik@server:0" \
    --k3s-arg "--disable=servicelb@server:0" \
    --k3s-arg "--disable=metrics-server@server:0" \
    --wait
  if docker network inspect "$NET" --format '{{.Internal}}' | grep -q true; then
    # k3d starts a helper container (k3d-<cluster>-tools) that mounts the host's Docker socket, which is
    # root-equivalent on the host's daemon. It is only needed while the cluster is created (host-IP discovery); images
    # are imported with --mode direct, which does not use it. Remove it so nothing on the isolated network holds the
    # socket (the gate's audit, C7, fails if any container does).
    docker rm -f k3d-meridian-g0-tools >/dev/null
  fi
fi
scripts/kc.sh wait --for=condition=Ready node --all --timeout=120s
# Karpenter's chart spreads its pods across zones, which needs the zone label every EKS node carries and a bare k3s
# node lacks. Labelling the node (idempotent) keeps the chart unmodified.
scripts/kc.sh label node --all --overwrite topology.kubernetes.io/region=us-west-2 topology.kubernetes.io/zone=us-west-2a
