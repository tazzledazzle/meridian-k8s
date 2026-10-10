# Security Model

The stack is local and simulated, but the gateway speaks the AWS control-plane protocol, so the defaults are strict. This page lists what is enforced, where, and what stays privileged.

## Default deny

- The gateway authenticates before it reveals anything. Unknown services and operations get `UnsupportedOperation` ([[The Gateway and ops.yaml]]).
- Operation names from attackers are never echoed or logged. Panic values from backends are never logged. Read errors return fixed messages.
- Backend URLs must be absolute http(s) with no credentials, query or fragment.
- The reverse proxy drops client-supplied `Forwarded` and `X-Forwarded-*` headers.

## No default or committed keys

`make init` runs `scripts/init-env.sh`. It writes `.env` with a random access key (prefix `MRDN`) and a random secret, mode 0600. It never overwrites an existing file. `.env` and `.env.images` are in `.gitignore`, and `.dockerignore` keeps `.env` out of build contexts. Compose files require the variables (`${AWS_ACCESS_KEY_ID:?run make init}`), so the stack does not start with blanks. The gateway accepts only that one key.

## Loopback-only ports

- The gateway publishes `127.0.0.1:4566`. Floci publishes nothing.
- The gateway refuses a non-loopback listen address unless `-allow-any-interface` is set, which the container needs.
- k3d publishes the Kubernetes API on `0.0.0.0` by default. `scripts/k3d-up.sh` passes `--api-port 127.0.0.1:0`.
- Checked live: no container in the stack is published on `0.0.0.0`.

## No Docker socket

`scripts/check-no-sock.sh` renders the Compose config and fails if any service mounts `docker.sock`. `make s0-up` runs it first. Floci needs no socket because its EC2 and EKS services run in mock mode ([[Findings About Floci]]). Checked live per container, including the k3d server and tools nodes.

## Hardened containers

| Container | Settings |
|---|---|
| `aws-gateway` | Runs as the invoking user, read-only root filesystem, `cap_drop: [ALL]`, `no-new-privileges` |
| Probe container | Distroless static image, `--read-only --cap-drop ALL`, `no-new-privileges`, runs as the caller |
| `node-registrar` | Non-root (65532), RBAC on Nodes only, `imagePullPolicy: Never` |
| KWOK controller | Image pinned by digest, resource limits, `allowPrivilegeEscalation: false`, `drop: [ALL]`; still runs as root because the vendored mount is under `/root` |

Base images are pinned by digest in `versions.yaml`.

## What stays privileged

k3s and k3d-tools run `privileged=true`. This is inherent to k3s inside Docker and is the one elevated container in the stack. It is recorded in decision D9, and the planned ADR-0002 will state it next to the socket decision. It is also why the stack is for local and CI use only.

## Secrets in reports and logs

- `s0probe` reads credentials from the environment only; it has no credential flags. Backend text is redacted before it enters a report, and the redaction runs before truncation so a secret split at the cut cannot leak its first characters.
- `scripts/s0-run.sh` passes credentials to containers by name (`-e NAME`), so they never appear in a process listing.
- `scripts/karpenter-install.sh` stores them in a Kubernetes Secret inside the local cluster only.
- Gateway error messages never carry request data.

## Not yet in place

No NetworkPolicy exists. The isolated no-internet gate network is planned, and assumption `meridian-17s.54` (k3d and Floci on an internal Docker network) is unverified. The KWOK controller currently pulls its image from the internet.
