#!/usr/bin/env bash
# Runs kubectl inside the k3d server container: kc.sh get nodes, kc.sh apply -f - < file.yaml
set -euo pipefail
# -i passes stdin through (for "apply -f -"); the k3s image ships kubectl and its kubeconfig.
exec docker exec -i k3d-meridian-g0-server-0 kubectl "$@"
