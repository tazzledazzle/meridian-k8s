# Decisions

Fifteen decisions (D1 to D15) are closed in the issue tracker, all dated 2026-10-10. Eleven assumptions stay open. Pull them with `bd list --type decision` and `bd list --label assumption`; `bd show <id>` gives the full text. The planned ADRs will restate the architectural ones.

## Closed decisions

| # | Decision | Reason | Evidence |
|---|---|---|---|
| D1 | Approve the operation-to-backend table: 34 operations on Floci, `DescribeInstanceTypes` on a thin `ec2-sim`, `pricing:GetProducts` on `pricing-facade`. Conditional on criterion 3, which later passed | Only one uncoupled operation failed, so a full EC2 simulator is unnecessary | `docs/s0/decision.md` |
| D2 | Pricing uses `pricing-facade` and the stack keeps `us-west-2` | Moving to `us-east-1` would change AZ names and every seeded value | `harness.md` finding 9 |
| D3 | Build `node-registrar` and KWOK before continuing | Karpenter, the oracle, should validate the table before it is final | `docs/s0/criterion3.md` |
| D4 | Commits carry no co-author trailer. History was rewritten and a backup tag kept | Owner's instruction | tag `backup/pre-trailer-strip` |
| D5 | Track work in beads (`bd`) | Replaces the handoff rule of no tracker | `.beads/` |
| D6 | Per Go task: one author and one independent adversarial tester. Infrastructure tasks run inline | Independent attack finds what the author's assumptions hide | [[Testing Strategy]] |
| D7 | Floci runs in EC2 and EKS mock mode with no Docker socket | Without mock mode `eks:CreateCluster` hangs on the missing socket | `harness.md` finding 7; `deploy/compose.s0.yaml` |
| D8 | Gateway runs as the invoking user, read-only root, capabilities dropped | Least privilege; trace directory stays writable by the caller | `harness.md`, "What runs" |
| D9 | k3d API binds to 127.0.0.1; k3s stays privileged | k3d defaults to `0.0.0.0`; privilege is inherent to k3s in Docker | `harness.md`, "Isolation checks" |
| D10 | `Verify` hashes the real body, rejects `UNSIGNED-PAYLOAD`, requires `X-Amz-Target` to be signed | An unsigned target or payload lets a request differ from what was authenticated | `internal/awsapi/sigv4_target_test.go`; `curl --aws-sigv4` accepted |
| D11 | Gateway accepts only ec2, iam, sts, sqs, ssm, pricing, eks, even in trace mode | Strict allow-list keeps the call surface known | `internal/gateway` |
| D12 | k3d v5.9.0 installs as a checksum-verified `bin/k3d` | Matches the `versions.yaml` pin | `versions.yaml` |
| D13 | Pins: Karpenter 1.14.1, k3s v1.35.9-k3s1 (Kubernetes 1.35), Floci 2.2.0 by digest | Karpenter 1.14 lists Kubernetes up to 1.36; results hold only for this Floci version | `versions.yaml`, `smoke.md` |
| D14 | Add `ec2:DescribeInstanceStatus` to `ops.yaml` and to the coupled set | Karpenter 1.14.1 calls it and it reads instance state | `ops.yaml` (36 operations) |
| D15 | Accept deviations B1 (SigV4 path not normalised) and B6 (trace mode sees only eks Describe and Create) | Matches aws-sdk-go; trace mode is a discovery aid | skipped tests, see [[Testing Strategy]] |

## Open assumptions

Each is open until a test or run records a result.

| Assumption | Verify with |
|---|---|
| Floci mock modes stay adequate for `CreateFleet` and the instance lifecycle | The planned gate; criterion 3 showed launch and terminate |
| Missing `instanceLifecycle=spot` makes Karpenter label spot as on-demand | Partly verified: the criterion 4 run handled the node as spot ([[Spot Interruptions]]) |
| Missing `networkInfo.maximumNetworkInterfaces` may cap pods per node | Rerun after `ec2-sim` exists |
| Karpenter never sends `UNSIGNED-PAYLOAD` | Count `Verify` rejections during the gate |
| Floci 2.2.0 behavior is stable (A7) | Planned CI rule: fail if the pin changes without a refreshed report |
| Karpenter's calls all fall within `ops.yaml` | Diff the gate trace against `ops.yaml` |
| The host has enough RAM at peak | `docker stats` during the gate; only idle figures exist |
| ECR rate limits do not break fresh-runner CI | First CI runs |
| `ec2-sim` can answer `DescribeInstanceTypes` from the catalog well enough | Karpenter reconcile after `ec2-sim` exists |
| `pricing-facade` documents match what Karpenter parses | Karpenter logs after `ops.yaml` is applied |
| k3d and Floci run on an internal Docker network | Gate stack build |
