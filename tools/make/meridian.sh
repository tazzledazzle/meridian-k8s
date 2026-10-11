#!/usr/bin/env bash
# Dispatcher behind the Bazel equivalents of the Makefile targets: `bazel run //:s0-up`, `//:build-linux`, ...
# Each sh_binary in //tools/make passes the target name as $1; the tools it needs arrive as runfiles whose
# runfiles paths are in MB_<name> environment variables (see defs.bzl). Works with bash 3.2 (macOS).
set -euo pipefail

target="${1:?usage: meridian.sh <target> [args...]}"
shift

# --- runfiles -----------------------------------------------------------------------------------------------
RF="${RUNFILES_DIR:-}"
[ -n "$RF" ] || { [ -d "$0.runfiles" ] && RF="$0.runfiles"; } || true
[ -n "$RF" ] || { echo "meridian: cannot find the runfiles tree" >&2; exit 2; }

# bin NAME: absolute path of the tool whose runfiles path is in MB_NAME.
bin() {
  local var="MB_$1" p
  p="${!var:-}"
  [ -n "$p" ] || { echo "meridian: no tool '$1' wired into //tools/make:$target" >&2; exit 2; }
  echo "$RF/$p"
}

# --- same environment the Makefile exports -------------------------------------------------------------------
ROOT="${BUILD_WORKSPACE_DIRECTORY:?run this with 'bazel run', not directly}"
cd "$ROOT"
export BUILD_WORKSPACE_DIRECTORY="$ROOT"
# GNU Make 3.81 (macOS) resolved k3d with the PATH it started with, so a system k3d wins over bin/k3d. Same here.
K3D="$(command -v k3d 2>/dev/null || echo "$ROOT/bin/k3d")"
export PATH="$ROOT/bin:$PATH"                 # bin/k3d is the checksum-verified pinned copy
export MERIDIAN_UID="$(id -u)"                # the gateway container runs as the invoking user
export MERIDIAN_GID="$(id -g)"
compose() { docker compose --env-file .env.images --env-file .env -f deploy/compose.s0.yaml "$@"; }

# --- steps (each equals one Makefile target's recipe) -------------------------------------------------------
step_init() { "$(bin init_env)" .env; }

step_env_images() {
  "$(bin versionsenv)" versions.yaml > .env.images.tmp || { rm -f .env.images.tmp; return 1; }
  mv .env.images.tmp .env.images
}

# copy_bins DEST PATHS: copies the binaries (space separated runfiles paths) into DEST, dropping "_linux".
copy_bins() {
  local dest="$1" paths="$2" p n
  mkdir -p "$dest"
  for p in $paths; do
    n="$(basename "$p")"; n="${n%_linux}"
    # Separate commands: a failure inside an && list does not trip set -e.
    cp -f "$RF/$p" "$dest/$n.tmp" || { echo "copy_bins: cannot copy $p to $dest/$n" >&2; exit 1; }
    chmod 0755 "$dest/$n.tmp"
    mv -f "$dest/$n.tmp" "$dest/$n"
  done
  echo "wrote $(echo $paths | wc -w | tr -d ' ') binaries to $dest/"
}
step_build()       { copy_bins bin "${MB_HOST_BINS:?}"; }
step_build_linux() { copy_bins bin/linux "${MB_LINUX_BINS:?}"; }

step_check_sock()  { "$(bin check_no_sock)" deploy/compose.s0.yaml; }

step_seed()        { "$(bin seed_floci)"; }

# --- targets ------------------------------------------------------------------------------------------------
case "$target" in
  init)       step_init ;;
  env-images) step_env_images ;;
  build)      step_build ;;
  build-linux) step_build_linux ;;
  check-sock) step_check_sock ;;
  s0-up)
    step_init; step_env_images; step_build_linux; step_check_sock
    mkdir -p trace/raw
    COMPOSE_PROFILES="$("$(bin profiles)" ops.yaml)" compose up -d --build
    "$(bin seed_floci)"
    "$(bin k3d_up)"
    "$(bin karpenter_install)"
    ;;
  s0-down)
    "$K3D" cluster delete meridian-g0 || true     # make: "-$(K3D) ..." ignores a failure
    compose --profile '*' down -v
    ;;
  seed)       step_build_linux; step_seed ;;
  trace-show) "$(bin trace_show)" ;;
  s0-probe)   step_build_linux; "$(bin s0_run)" docs/s0 report ;;
  s0-probe-moto) step_build_linux; S0_LABEL=moto "$(bin s0_run)" docs/s0 report-moto ;;
  s0-decide)
    # Prints the per-operation decision from the latest reports; APPLY=1 or --apply rewrites ops.yaml.
    # The Moto report is used only when it exists.
    apply=()
    [ -z "${APPLY:-}" ] || apply=(--apply)
    for a in "$@"; do [ "$a" = "--apply" ] && apply=(--apply); done
    reports=(--report floci=docs/s0/report.json)
    [ ! -e docs/s0/report-moto.json ] || reports+=(--report moto=docs/s0/report-moto.json)
    "$(bin s0matrix)" "${reports[@]}" ops.yaml ${apply[@]+"${apply[@]}"}
    ;;
  # gate-up.sh calls make for init/env-images/build-linux unless told they are done; here the dispatcher did them.
  gate-up)    step_init; step_env_images; step_build_linux; MERIDIAN_PREBUILT=1 "$(bin gate_up)" ;;
  gate-down)  "$(bin gate_down)" ;;
  kwok-up)    step_env_images; "$(bin kwok_up)" ;;
  vendor-kwok) step_env_images; "$(bin vendor_kwok)" ;;
  deploy-registrar) step_init; step_env_images; "$(bin deploy_registrar)" ;;
  *) echo "meridian: unknown target '$target'" >&2; exit 2 ;;
esac
