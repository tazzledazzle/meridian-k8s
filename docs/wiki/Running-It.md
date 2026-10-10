# Running It

Everything here runs on one machine. Karpenter's AWS calls go to the local gateway; images and the Karpenter chart still download from public registries. Not validated against real AWS.

## Prerequisites

- Docker with Compose v2. The S0 results were recorded with Docker 29.6.1 and a VM limit of 7.65 GiB. The stack used about 840 MiB with one running pod.
- Go 1.24 (`go.mod`). Service images build with golang 1.24.13.
- Python 3, `openssl`, `shasum`, `make`, and `helm` (for `scripts/karpenter-install.sh`).
- k3d v5.9.0, either on `PATH` or as a checksum-verified `bin/k3d`. The Makefile prefers `bin/` on `PATH`. The planned `scripts/install-k3d.sh` does not exist yet, so install k3d yourself.

`versions.yaml` pins: Karpenter chart 1.14.1, k3s v1.35.9-k3s1, k3d v5.9.0, KWOK v0.8.0, Floci 2.2.0, golang 1.24.13, distroless static (nonroot). Images carry digests.

## Commands, in order

| Step | Command | What it does |
|---|---|---|
| 1 | `make init` | Writes `.env` with random local credentials. Safe to repeat |
| 2 | `make env-images` | Renders `versions.yaml` to `.env.images` |
| 3 | `make build-linux` | Builds static Linux binaries into `bin/linux/` |
| 4 | `make s0-up` | Runs steps 1 to 3, checks for the Docker socket, starts Floci and the gateway, seeds Floci, creates cluster `meridian-g0`, installs Karpenter |
| 5 | `make s0-probe` | Runs the 37 checks, writes `docs/s0/report.md` and `report.json`, and creates the EKS cluster record Karpenter needs |
| 6 | `make s0-decide` | Prints the backend per operation. `APPLY=1` rewrites `ops.yaml` |
| 7 | `make kwok-up` | Applies the vendored KWOK release to the cluster |
| 8 | `make deploy-registrar` | Builds `node-registrar`, imports it into k3d, applies its manifest |
| 9 | `make trace-show` | Lists calls the gateway recorded and flags any missing from `ops.yaml` |
| 10 | `make s0-down` | Deletes the cluster and the Compose stack with its volumes |

`make s0-up` took 111 seconds from a torn-down state and 4 seconds on a live stack. A probe run exits non-zero on check failures; `s0-run.sh` treats that as expected during S0.

To launch a node after step 8, apply the workloads and scale up. These `kc.sh` commands run kubectl inside the k3d server container:

```
scripts/kc.sh apply -f - < deploy/workloads/nodepool.yaml
scripts/kc.sh apply -f - < deploy/workloads/inflate.yaml
scripts/kc.sh get nodeclaims,nodes
```

Scale to zero with `scripts/kc.sh scale deploy/inflate --replicas=0` and Karpenter removes the Node ([[Nodes Without Machines]]).

## Troubleshooting

Each entry was seen during the S0 runs.

**The pod never becomes Pending, or no NodeClaim appears.** The k3d server node is schedulable, so an unpinned pod lands on the control-plane node. Give the workload `nodeSelector: karpenter.sh/nodepool: default`, as `deploy/workloads/inflate.yaml` does.

**The Node exists but Karpenter creates a second NodeClaim.** The pod does not tolerate `kwok.x-k8s.io/node=fake:NoSchedule`. Add the toleration.

**A NodeClaim has no Node for a long time.** Check `scripts/kc.sh -n kube-system logs deploy/node-registrar`. If it logs `unknown type`, Karpenter chose a type outside `internal/catalog`. Restrict the NodePool to catalog types. If the registrar is not deployed, run `make deploy-registrar`.

**Karpenter crash-loops at startup.** `eks:DescribeCluster` returns 404 until the cluster record exists. Run `make s0-probe`, which creates it.

**Seed calls fail right after Floci starts.** Floci reports healthy before every service answers. The seed retries for 60 seconds per service. Rerun `make seed` if it still fails.

**Karpenter logs `updating pricing: no on-demand pricing found`.** Expected until `pricing-facade` exists. It was the only Karpenter error in a 25-minute run.

**`make s0-up` fails the socket check.** A Compose service mounts `docker.sock`. Remove the mount; the stack needs none.

**`make kwok-up` says the files do not match `SHA256SUMS`.** Run `make vendor-kwok` (needs network) to re-download and re-verify.
