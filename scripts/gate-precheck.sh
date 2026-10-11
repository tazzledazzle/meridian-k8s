#!/usr/bin/env bash
# Fails fast, before anything is started, when the Docker daemon has too little memory or CPU for the gate (A9).
# Reference: the S0 stack peaked at about 840 MiB (k3d 755, floci 67, gateway 11, tools 7) with one node; the gate
# adds KWOK, node-registrar and several pods, so the default floor leaves room. Override with GATE_MIN_MEM_MB / GATE_MIN_CPUS.
# GATE_DOCKER_MEM_BYTES / GATE_DOCKER_NCPU replace the values read from `docker info` (used by the tests).
set -euo pipefail
MIN_MEM_MB="${GATE_MIN_MEM_MB:-2048}"
MIN_CPUS="${GATE_MIN_CPUS:-2}"
mem_bytes="${GATE_DOCKER_MEM_BYTES-$(docker info --format '{{.MemTotal}}')}"
ncpu="${GATE_DOCKER_NCPU-$(docker info --format '{{.NCPU}}')}"
case "$mem_bytes$ncpu$MIN_MEM_MB$MIN_CPUS" in *[!0-9]*|'') echo "FAIL: cannot read Docker's memory/CPU limits (got mem='$mem_bytes' cpu='$ncpu')" >&2; exit 1 ;; esac
[ -n "$mem_bytes" ] && [ -n "$ncpu" ] || { echo "FAIL: cannot read Docker's memory/CPU limits" >&2; exit 1; }
mem_mb=$((mem_bytes / 1024 / 1024))
if [ "$mem_mb" -lt "$MIN_MEM_MB" ]; then
  echo "FAIL: the Docker daemon has ${mem_mb} MiB of memory; the gate needs at least ${MIN_MEM_MB} MiB. Raise Docker's memory limit." >&2
  exit 1
fi
if [ "$ncpu" -lt "$MIN_CPUS" ]; then
  echo "FAIL: the Docker daemon has ${ncpu} CPU(s); the gate needs at least ${MIN_CPUS}. Raise Docker's CPU limit." >&2
  exit 1
fi
echo "ok: Docker has ${mem_mb} MiB and ${ncpu} CPUs (gate floor ${MIN_MEM_MB} MiB, ${MIN_CPUS} CPUs)"
