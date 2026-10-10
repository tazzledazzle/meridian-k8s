# S0 Task 0: Floci and Karpenter smoke test

Date: 2026-10-09. Host: macOS (arm64), Docker 29.6.1 (VM limit 7.65 GiB).
Scope: no gateway, no SigV4, plain endpoint override. Not validated against real AWS.

## Verdict: PASS (no stop condition hit)

Floci starts. It serves `DescribeInstances` and `CreateFleet`. The pinned Karpenter image starts
against it and stays up. Two Karpenter errors remain; both are expected or are Task 8 inputs (below).

## Pins used (candidates for Task 1's versions.yaml)

| Component | Pin |
|---|---|
| Floci | `floci/floci:2.2.0` = `sha256:e97cd0c1dc2aa14e7697fb5ef5018404c4d6345169dbd276315f0bed2c7b0520` (arm64 pulled; floci 2.2.0 native, Quarkus 3.39.2) |
| Karpenter controller | `public.ecr.aws/karpenter/controller:1.14.1` = `sha256:445baefaaa689029cc12ac1f659b916b0b82fb14308ce536f8521fcf079116fa` (commit bde0065) |
| k3s (smoke only) | `rancher/k3s:v1.35.9-k3s1` (Karpenter 1.14 compat table lists K8s up to 1.36) |

## Results

| Check | Result |
|---|---|
| Floci start, no Docker socket mounted | Healthy after about 15 s. It retries the socket 6 times at boot, then continues. |
| `DescribeInstances` (empty) | `{"Reservations": []}` |
| `CreateLaunchTemplate` | OK |
| `CreateFleet` (type=instant, spot, price-capacity-optimized, 2 overrides) | OK. Returned FleetId, one instance `i-71cc9feec714d88ec`, `m5.large`, `Lifecycle: spot`. |
| `DescribeInstances` after fleet | Instance `running`. `InstanceLifecycle` was `None`; check against what Karpenter reads in Task 8. |
| Instance containers started? | None. `docker ps` showed only Floci. |
| Karpenter 1.14.1 vs Floci (AWS_ENDPOINT_URL override) | Starts, all controllers run, 60+ s up. Needs a kube API: it exits without one. |

## A1 evidence (Docker socket)

Floci with no socket mounted: starts, `RunInstances`-path via `CreateFleet` succeeds, and no
container is created. Floci's docs list no EC2 container-execution flag, but it is not needed here.
Task 7 can plan on mounting no socket. Still to confirm: the probe passes the wider EC2 matrix the same way.

## Open items handed to later tasks

1. **Pricing (Task 8).** Karpenter logs `Reconciler error updating pricing, no on-demand pricing found` (14 times in 80 s).
   Floci answers a hand-written `GetProducts` filter for m5.large and returns a product, and Karpenter does call
   `GetProducts` and `DescribeServices`, so Floci's reply does not satisfy Karpenter's own filters or parsing.
   Derive the exact request from the `-record` trace. Candidate for `pricing-facade` (Task 12/13).
2. **SQS (Task 7).** `GetQueueUrl` fails with NonExistentQueue because nothing is seeded. Expected; the seed script creates it.
3. **EKS `DescribeCluster`.** Not found for an unseeded name. Smoke used `CLUSTER_ENDPOINT` to skip it. Seed or fall back to `eks-facade`.
4. **Spot price history, SSM AL2023 AMI parameter.** Both answered by Floci out of the box.
5. **No default subnet.** Floci has a default VPC but no subnet; create one before `CreateFleet`.

## Memory (A9 input)

Idle, after about 80 s: Floci 48 MiB, Karpenter 51 MiB, k3s 451 MiB. About 550 MiB total. This is idle and
does not include KWOK, workloads or the gateway. Real peak is measured in Task 15/16.

## Method

Floci on loopback only (127.0.0.1:4566), throwaway credentials, scratch network `smoke-net`.
k3s ran `--privileged` (as k3d will). CRDs applied from the v1.14.1 tag. Karpenter run with
`KUBECONFIG`, `AWS_ENDPOINT_URL=http://floci:4566`, `CLUSTER_ENDPOINT`, `INTERRUPTION_QUEUE`, `DISABLE_LEADER_ELECTION=true`.
