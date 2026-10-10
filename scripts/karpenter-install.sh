#!/usr/bin/env bash
# Installs the pinned Karpenter chart into the G0 cluster with the local gateway credentials.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env; . ./.env.images; set +a
command -v helm >/dev/null || { echo "helm is required"; exit 1; }
KC=scripts/kc.sh

# Credentials come from .env (never committed). The Secret lives only in this local cluster.
$KC -n kube-system create secret generic aws-creds \
  --from-literal=AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
  --from-literal=AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
  --dry-run=client -o yaml | $KC apply -f -

# Render on the host, apply inside the cluster: helm never needs to reach the API server.
# --include-crds: the chart ships Karpenter's CRDs. --kube-version avoids asking a cluster for it.
# Server-side apply because Karpenter's CRDs are larger than client-side apply's annotation limit.
helm template karpenter oci://public.ecr.aws/karpenter/karpenter \
  --version "${KARPENTER_VERSION}" \
  --namespace kube-system \
  --kube-version "${K3S_VERSION%%+*}" \
  --include-crds \
  --values deploy/karpenter-values.yaml \
  | $KC apply --server-side --force-conflicts -f -
# Karpenter retries after a failed AWS call, so a CrashLoop here is expected until later tasks add services.
echo "installed; watch with: scripts/kc.sh -n kube-system logs deploy/karpenter -f"
