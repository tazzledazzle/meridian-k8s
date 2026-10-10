#!/usr/bin/env bash
# Runs the S0 contract probe through the gateway and writes OUT/NAME.md and OUT/NAME.json.
#
#   scripts/s0-run.sh [OUT [NAME]]      defaults: docs/s0 and report (use NAME=report-moto for the Moto stack)
#
# The probe runs on the Compose network so it reaches the gateway by name, exactly as Karpenter does.
# Credentials come from .env and are forwarded by name (-e NAME), so they never appear in a process listing.
# Exit status: 0 when the probe ran and wrote a report (check failures are expected during S0 and are the point of
# the spike); non-zero when the probe could not run or wrote no new report.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT="${1-docs/s0}"
NAME="${2-report}"
# OUT and NAME end up inside a docker -v argument, so allow plain relative paths only (no ':' ',' '..' or absolute paths).
[[ "$OUT" =~ ^[A-Za-z0-9._/-]+$ && "$OUT" != /* && "$OUT" != *..* ]] || { echo "s0-run: unsafe output directory '$OUT'" >&2; exit 2; }
[[ "$NAME" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ && "$NAME" != *..* ]] || { echo "s0-run: unsafe report name '$NAME'" >&2; exit 2; }

[ -f .env ] || { echo "s0-run: .env is missing; run 'make init'" >&2; exit 2; }
[ -f .env.images ] || { echo "s0-run: .env.images is missing; run 'make env-images'" >&2; exit 2; }
[ -x bin/linux/s0probe ] || { echo "s0-run: bin/linux/s0probe is missing; run 'make build-linux'" >&2; exit 2; }
set -a; . ./.env; . ./.env.images; set +a
: "${AWS_ACCESS_KEY_ID:?s0-run: AWS_ACCESS_KEY_ID is empty in .env}" "${AWS_SECRET_ACCESS_KEY:?s0-run: AWS_SECRET_ACCESS_KEY is empty in .env}"
: "${STATIC_IMAGE:?s0-run: STATIC_IMAGE is not set in .env.images}" "${K3S_VERSION:?s0-run: K3S_VERSION is not set in .env.images}"

NET="${MERIDIAN_NET:-meridian}"   # the gate sets meridian-gate
mkdir -p "$OUT"

# Set an earlier report aside, so "a new report exists" really means this run wrote one; put it back if this run fails.
for ext in md json; do
  [ -e "$OUT/$NAME.$ext" ] && mv "$OUT/$NAME.$ext" "$OUT/.$NAME.$ext.prev"
done
cleanup() {
  for ext in md json; do
    if [ -e "$OUT/.$NAME.$ext.prev" ]; then
      if [ -e "$OUT/$NAME.$ext" ]; then rm -f "$OUT/.$NAME.$ext.prev"; else mv "$OUT/.$NAME.$ext.prev" "$OUT/$NAME.$ext"; fi
    fi
  done
}
trap cleanup EXIT

# Kubernetes minor from the pinned k3s version (v1.35.9-k3s1 -> 1.35); Karpenter's alias resolves to an SSM path with it.
MINOR=$(echo "$K3S_VERSION" | sed -E 's/^v?([0-9]+\.[0-9]+).*/\1/')

# --user keeps the report files owned by the caller; the probe is the static binary on the distroless static image.
# The container needs no writes outside /out, no capabilities and no privilege escalation.
set +e
docker run --rm --network "$NET" --user "$(id -u):$(id -g)" \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
  -v "$PWD/bin/linux/s0probe:/s0probe:ro" -v "$PWD/$OUT:/out" \
  --entrypoint /s0probe "${STATIC_IMAGE}" \
  -endpoint http://aws-gateway:4566 -region us-west-2 -cluster meridian-g0 -eks-create \
  -queue meridian-g0-interruption \
  -ami-param "/aws/service/eks/optimized-ami/${MINOR}/amazon-linux-2023/x86_64/standard/recommended/image_id" \
  -title "S0 contract probe: ${S0_LABEL:-floci}" -name "$NAME" -out /out
rc=$?
set -e

case "$rc" in
  0) echo "probe passed; report in $OUT/$NAME.md" ;;
  1)
    # Exit 1 means checks failed (expected during S0); anything else, or no report, is a real problem.
    [ -s "$OUT/$NAME.json" ] || { echo "s0-run: the probe exited 1 but wrote no new report to $OUT/$NAME.json" >&2; exit 1; }
    echo "probe reported failures (expected during S0); see $OUT/$NAME.md" ;;
  *) echo "s0-run: the probe could not run (exit $rc)" >&2; exit "$rc" ;;
esac
