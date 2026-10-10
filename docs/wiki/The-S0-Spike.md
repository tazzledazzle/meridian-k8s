# The S0 Spike

S0 answered one question: can Floci serve the AWS calls Karpenter makes, or must Meridian build its own backends? Not validated against real AWS.

## The contract probe

`s0probe` (`cmd/s0probe`, `internal/s0probe`) sends 37 checks through the gateway. They cover the 36 operations in `ops.yaml`; `ssm:GetParameter` has a second check for the AMI alias path. Each check lists the response fields it reads. For example, `ec2:DescribeInstanceTypes` requires `networkInfo.maximumNetworkInterfaces`. A check passes only if every listed field is present. `docs/s0/report.md` prints the full field table.

`make s0-probe` runs the probe in a container on the Compose network, the way Karpenter reaches the gateway. It writes `docs/s0/report.md` and `docs/s0/report.json`.

## The decision table

`make s0-decide` runs `scripts/s0matrix.py` on the reports and prints one backend per operation. Add `APPLY=1` to rewrite `ops.yaml`. The rules:

- An operation passes on a backend only if all its checks passed. A skipped, failed or unreported check counts as a failure.
- Coupled EC2 operations never split. The whole group goes to the first backend whose report passed all of them.
- Other operations go to the first backend that passed, else to the fallback for their service.

## Results

35 of 37 checks passed on Floci 2.2.0 and 2 failed (`docs/s0/report.md`):

| Operation | Why it failed | Approved backend |
|---|---|---|
| `ec2:DescribeInstanceTypes` | `networkInfo.maximumNetworkInterfaces` appears only inside `networkCards.item`, not at `networkInfo` level | `ec2-sim`, reference data only |
| `pricing:GetProducts` | Price data exists for `us-east-1` only; `PriceList` is empty for `us-west-2` | `pricing-facade` |

The user approved the table on 2026-10-10 (decisions D1 and D2). Floci keeps 34 operations. `docs/s0/decision.md` holds the full table.

## Eligible is not proven

A probe pass means the backend is eligible: it returned every field the probe reads. It does not mean Karpenter accepts the answers. Karpenter is the oracle. If Karpenter fails on an operation the probe passed, that operation moves to the next backend by editing one `backend:` line.

Karpenter criteria 1 to 3 ran by hand with every operation still on Floci:

| Criterion | Result |
|---|---|
| 1. Starts, stays up, discovers its world | Pass, with one known error: `updating pricing: no on-demand pricing found` |
| 2. Pending pod becomes a NodeClaim and an instance | Pass |
| 3. Node registers as a KWOK Node and the pod schedules | Pass (2026-10-10); see [[Nodes Without Machines]] |

The user approved the table conditionally on criterion 3. The pass made the approval unconditional.

## What the spike changed

- Only one uncoupled operation failed, so a thin `ec2-sim` replaces the full EC2 simulator the original plan called for.
- `eks-facade` is unnecessary: EKS passes in mock mode.
- Moto is unnecessary.
- `ec2:DescribeInstanceStatus` was missing from `ops.yaml`. Karpenter calls it, so it was added (D14).
- `ops.yaml` stays unchanged until `ec2-sim` and `pricing-facade` exist. Routing to missing backends would break the running stack.

The result holds only for Floci 2.2.0 at the pinned digest (assumption A7 in [[Decisions]]).
