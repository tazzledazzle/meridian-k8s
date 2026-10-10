#!/usr/bin/env bash
# Generates random local AWS-style credentials into a gitignored env file.
# Usage: init-env.sh [path]   (default: .env)
set -euo pipefail

ENV_FILE="${1:-.env}"

# Never overwrite: regenerating would silently invalidate running services.
if [[ -f "$ENV_FILE" ]]; then
  echo "$ENV_FILE exists; leaving it untouched"
  exit 0
fi

umask 077   # file is created readable by the owner only (0600)

# Access key looks like an AWS key id but is clearly local; secret is random.
AK="MRDN$(openssl rand -hex 8 | tr 'a-f' 'A-F')"
SK="$(openssl rand -base64 30 | tr -d '\n=+/')"

cat > "$ENV_FILE" <<EOF
AWS_ACCESS_KEY_ID=$AK
AWS_SECRET_ACCESS_KEY=$SK
AWS_REGION=us-west-2
EOF
echo "wrote $ENV_FILE"
