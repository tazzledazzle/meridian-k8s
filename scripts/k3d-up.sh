#!/usr/bin/env bash
# Creates the G0 k3d cluster on the Docker network "meridian" so pods can resolve the gateway by name.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env.images; set +a
export PATH="$PWD/bin:$PATH"   # picks up the checksum-verified bin/k3d when k3d is not installed system-wide

command -v k3d >/dev/null || { echo "k3d is required (see versions.yaml for the pinned version)"; exit 1; }

# The network is created by Compose (make s0-up and gate-up run Compose first).
NET="${MERIDIAN_NET:-meridian}"   # the gate uses the isolated network meridian-gate
docker network inspect "$NET" >/dev/null 2>&1 || { echo "network '$NET' missing: run docker compose first"; exit 1; }

if k3d cluster list meridian-g0 >/dev/null 2>&1; then
  echo "cluster meridian-g0 already exists"
else
  # --no-lb: no load balancer container. --api-port: k3d still publishes the API server on the host, and its default
  # is every interface (0.0.0.0), so bind it to loopback. --network: shares the Compose network.
  # traefik, servicelb and metrics-server are unused by G0; disabling them also shortens the offline image list.
  k3d cluster create meridian-g0 \
    --image "${K3S_IMAGE}" \
    --network "$NET" \
    --servers 1 --agents 0 \
    --no-lb \
    --api-port 127.0.0.1:0 \
    --k3s-arg "--disable=traefik@server:0" \
    --k3s-arg "--disable=servicelb@server:0" \
    --k3s-arg "--disable=metrics-server@server:0" \
    --wait
fi
scripts/kc.sh wait --for=condition=Ready node --all --timeout=120s
# Karpenter's chart spreads its pods across zones, which needs the zone label every EKS node carries and a bare k3s
# node lacks. Labelling the node (idempotent) keeps the chart unmodified.
scripts/kc.sh label node --all --overwrite topology.kubernetes.io/region=us-west-2 topology.kubernetes.io/zone=us-west-2a
