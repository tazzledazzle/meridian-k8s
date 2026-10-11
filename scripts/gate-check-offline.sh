#!/usr/bin/env bash
# Fails if any pod in the gate cluster ever tried to pull an image (a Pulling / ErrImagePull / ImagePullBackOff event),
# or if the events cannot be read. Run after gate-up.
set -euo pipefail
cd "${BUILD_WORKSPACE_DIRECTORY:-$(dirname "$0")/..}"   # bazel run: the real workspace, not the runfiles tree
# A kubectl failure must fail the check: reading the events and filtering them are separate steps on purpose.
all_events=$(scripts/kc.sh get events -A --no-headers) || { echo "FAIL: cannot read the cluster's events" >&2; exit 1; }
events=$(printf '%s\n' "$all_events" | grep -E 'Pulling|ErrImagePull|ImagePullBackOff|Back-off pulling' || true)
if [ -n "$events" ]; then
  echo "FAIL: the cluster tried to pull images (it has no route to a registry):" >&2
  echo "$events" >&2
  exit 1
fi
echo "ok: no image pull was attempted"
