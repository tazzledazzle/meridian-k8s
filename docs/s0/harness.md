# S0 Task 7: harness findings

Date: 2026-10-10. Host: macOS arm64, Docker VM 7.65 GiB / 12 CPU. Not validated against real AWS.
`make s0-up` passes from a torn-down state (exit 0, 111 s cold, 4 s on a live stack) and is idempotent.

## What runs

Floci 2.2.0 (no published port, **no Docker socket**) behind `aws-gateway` (trace mode, published on 127.0.0.1:4566 only,
runs as the invoking user, read-only root filesystem, all capabilities dropped), a k3d cluster `meridian-g0` on the
`meridian` network (API server on 127.0.0.1 only), and real Karpenter 1.14.1 with `AWS_ENDPOINT_URL=http://aws-gateway:4566`.
`scripts/check-no-sock.sh` fails the build if any Compose service mounts `/var/run/docker.sock`.

## Findings (inputs to Task 8's report and decision)

1. **Karpenter reaches Floci through the gateway end to end.** The SDK signs with SigV4, the gateway verifies, routes and
   records, Floci answers. First calls recorded: `ec2:DescribeInstanceTypes`, `eks:DescribeCluster`.
2. **Karpenter crash-loops on `eks:DescribeCluster` = 404** (`No cluster found for name: meridian-g0`). The cluster record is
   created by Task 8's `s0probe`; until then the pod cannot get past startup, so the trace is still only 2 operations.
   The trace of the full call surface therefore needs the EKS cluster created first.
3. **Floci refuses `ssm:PutParameter` under `/aws/`** (`can't be prefixed with "aws" or "ssm"`), like real AWS. It already serves
   the public AL2023 parameter for Kubernetes 1.35 (`ami-0abcdef1234567891`) and ships a matching AMI, so the seed reads and
   checks instead of writing. `ec2:RegisterImage` works but is not idempotent (new ID every call), so it is no longer used.
4. **Cold-start race:** Floci reports healthy before every service answers. STS answered first and EC2 `DescribeImages` a few
   seconds later; the seed now waits and retries. A tooling concern for Task 17 CI, not a Floci defect.
5. **No EC2 containers are launched** (no socket mounted), so there is no network attachment to record. `deploy/floci.label`
   is not needed unless a later probe shows Floci requires the socket. This answers open risk "Floci with EC2 container
   execution off" for CreateFleet, DescribeInstances and the seed calls; the wider EC2 matrix is still Task 8's.
6. **SQS `QueueUrl` points at `floci:4566`, not the gateway.** Harmless for JSON-protocol calls (the SDK sends to its configured
   endpoint), noted in case a client follows the URL.

## Isolation checks (plan Step 10), run live

- Gateway: `127.0.0.1:4566` only. Floci: no published ports. No container in the stack is published on `0.0.0.0`.
- k3d's default publishes the Kubernetes API on `0.0.0.0`; the harness passes `--api-port 127.0.0.1:0` (now `127.0.0.1:55001`).
- No stack container mounts the Docker socket (checked per container, including the k3d server and tools nodes).
- **k3s and k3d-tools run `privileged=true`.** This is inherent to k3s-in-Docker and is the one elevated container in the
  stack. ADR-0002 (Task 17) must record it next to the socket decision.

## Memory (A9 input), idle, about 1 minute after start

| Container | Memory |
|---|---|
| k3d server (k3s + Karpenter pod) | 487 MiB |
| Floci | 34 MiB |
| aws-gateway | 5 MiB |
| k3d tools | 7 MiB |
| **Total** | **about 533 MiB** |

Karpenter is crash-looping at this point, with no workloads, KWOK or fallback services. The real peak is measured in
Tasks 15 and 16.

## Deviations from the plan's Task 7

Floci gets no Docker socket (A1); the gateway runs as the invoking user, not root, with a read-only filesystem; `.dockerignore`
keeps `.env` out of the build context; `servicelb` and `metrics-server` are disabled with traefik; the k3d API port is loopback;
the node gets a zone label so the chart's topology spread schedules; the seed reads the AMI instead of writing it; `profiles.py`
ignores commented-out routes (3 tests beyond the plan); `bin/k3d` is a checksum-verified v5.9.0 download (matches `versions.yaml`).
