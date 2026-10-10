#!/usr/bin/env bash
# Prints the recorded AWS calls and whether ops.yaml supports each one.
set -euo pipefail
cd "$(dirname "$0")/.."
f=trace/raw/calls.json
[ -s "$f" ] || { echo "no trace yet: $f is empty (the gateway flushes every 10s)"; exit 1; }
# calls.json is {"calls": {"ec2:CreateFleet": 3, ...}, "unsupported": ["ec2:CreateFleet", ...]}
python3 - "$f" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
bad = set(d["unsupported"])
for name in sorted(d["calls"]):
    mark = "UNSUPPORTED" if name in bad else "ok         "
    print(f'{mark} {name} x{d["calls"][name]}')
print(f'\n{len(d["calls"])} distinct operations, {len(bad)} not in ops.yaml')
PY
