.PHONY: init test test-scripts lint-versions env-images build build-linux s0-up s0-down seed trace-show check-sock s0-probe s0-probe-moto s0-decide

# Generates local credentials; safe to run repeatedly.
init:
	bash scripts/init-env.sh .env

test:
	go test ./...

# Python tests for the tooling scripts (profiles.py, s0matrix.py, s0-run.sh); -W error turns warnings into failures.
test-scripts:
	cd scripts && python3 -W error -m unittest discover -p 'test_*.py'

# Fails if versions.yaml has any unpinned entry.
lint-versions:
	go test ./internal/versions/ -run TestRepoVersionsFileIsValid -v

# Renders versions.yaml as KEY=VALUE lines for Compose and the shell scripts.
env-images:
	go run ./cmd/versionsenv > .env.images.tmp && mv .env.images.tmp .env.images

build:
	mkdir -p bin && go build -o bin/ ./cmd/...

# Static Linux binaries for the host's CPU architecture; scripts mount them into containers.
build-linux:
	mkdir -p bin/linux && CGO_ENABLED=0 GOOS=linux GOARCH=$(shell go env GOARCH) go build -trimpath -o bin/linux/ ./cmd/...

# bin/k3d is the checksum-verified pinned copy (see versions.yaml); put it on PATH for the scripts.
export PATH := $(CURDIR)/bin:$(PATH)
# The gateway container runs as the invoking user so it can write trace/raw without being root.
export MERIDIAN_UID := $(shell id -u)
export MERIDIAN_GID := $(shell id -g)

# GNU Make 3.81 (macOS) finds simple commands with the PATH it started with, so name k3d explicitly.
K3D := $(shell command -v k3d 2>/dev/null || echo $(CURDIR)/bin/k3d)
COMPOSE = docker compose --env-file .env.images --env-file .env -f deploy/compose.s0.yaml

# Fails if any service mounts the Docker socket (handoff A1).
check-sock:
	./scripts/check-no-sock.sh deploy/compose.s0.yaml

# Brings up Floci + the gateway (trace mode), the k3d cluster, and real Karpenter.
s0-up: init env-images build-linux check-sock
	mkdir -p trace/raw
	COMPOSE_PROFILES=$$(python3 scripts/profiles.py ops.yaml) $(COMPOSE) up -d --build
	./scripts/seed-floci.sh
	./scripts/k3d-up.sh
	./scripts/karpenter-install.sh

s0-down:
	-$(K3D) cluster delete meridian-g0
	$(COMPOSE) --profile '*' down -v

seed: build-linux
	./scripts/seed-floci.sh

trace-show:
	./scripts/trace-show.sh

# Runs the contract probe against the running S0 stack and writes docs/s0/report.{md,json}.
s0-probe: build-linux
	./scripts/s0-run.sh docs/s0 report

# Same probe against a stack whose coupled EC2 operations are routed to Moto (ops.yaml backend: moto); writes report-moto.{md,json}.
s0-probe-moto: build-linux
	S0_LABEL=moto ./scripts/s0-run.sh docs/s0 report-moto

# Prints the per-operation decision from the latest reports (add APPLY=1 to rewrite ops.yaml).
# The Moto report is used only when it exists. Order of trial for the coupled EC2 group: floci, moto, ec2-sim.
s0-decide:
	python3 scripts/s0matrix.py --report floci=docs/s0/report.json $(if $(wildcard docs/s0/report-moto.json),--report moto=docs/s0/report-moto.json,) ops.yaml $(if $(APPLY),--apply,)
