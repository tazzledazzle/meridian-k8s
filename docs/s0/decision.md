# S0 decision: which operations stay on Floci

Status: **APPROVED by the user on 2026-10-10** (handoff section 5 rule 3), with pricing decided as `pricing-facade` (region stays `us-west-2`).
The approval was conditional on Karpenter criterion 3, which has now **passed** (see `docs/s0/criterion3.md`), so it is unconditional.
`ops.yaml` is intentionally NOT yet changed: routing two operations to backends that are not built would break the running stack.
It is applied (`s0matrix.py --apply`) once the reference `ec2-sim` and `pricing-facade` exist.
Date: 2026-10-10. Floci pinned at `floci/floci:2.2.0@sha256:e97cd0c1dc2aa14e7697fb5ef5018404c4d6345169dbd276315f0bed2c7b0520`
(A7: this result is valid only for this version). Not validated against real AWS.

## Result in one paragraph

The EC2 group was the swing factor and it passes on Floci: `CreateFleet` reaches `running` with tags, and every coupled
instance operation passed. 35 of 37 probe checks passed; 2 failed. So Floci serves 34 of the 36 operations, and two small
custom backends are needed instead of a full EC2 simulator. A pass means *eligible*; Karpenter is the oracle (below).

## Proposed operation-to-backend table (`scripts/s0matrix.py`, from `docs/s0/report.json`)

| Operation | Backend | Why |
|---|---|---|
| ec2:CreateFleet | floci | probe passed on floci (all coupled checks); eligible to stay |
| ec2:CreateLaunchTemplate | floci | probe passed on floci (all coupled checks); eligible to stay |
| ec2:CreateTags | floci | probe passed on floci (all coupled checks); eligible to stay |
| ec2:DeleteLaunchTemplate | floci | probe passed on floci (all coupled checks); eligible to stay |
| ec2:DescribeAvailabilityZones | floci | probe passed on floci; eligible to stay |
| ec2:DescribeImages | floci | probe passed on floci (all coupled checks); eligible to stay |
| ec2:DescribeInstanceStatus | floci | probe passed on floci (all coupled checks); eligible to stay |
| ec2:DescribeInstanceTypeOfferings | floci | probe passed on floci; eligible to stay |
| ec2:DescribeInstanceTypes | ec2-sim | no probed backend passed it (floci failed) |
| ec2:DescribeInstances | floci | probe passed on floci (all coupled checks); eligible to stay |
| ec2:DescribeLaunchTemplates | floci | probe passed on floci (all coupled checks); eligible to stay |
| ec2:DescribeSecurityGroups | floci | probe passed on floci (all coupled checks); eligible to stay |
| ec2:DescribeSpotPriceHistory | floci | probe passed on floci; eligible to stay |
| ec2:DescribeSubnets | floci | probe passed on floci (all coupled checks); eligible to stay |
| ec2:RunInstances | floci | probe passed on floci (all coupled checks); eligible to stay |
| ec2:TerminateInstances | floci | probe passed on floci (all coupled checks); eligible to stay |
| eks:CreateCluster | floci | probe passed on floci; eligible to stay |
| eks:DescribeCluster | floci | probe passed on floci; eligible to stay |
| iam:AddRoleToInstanceProfile | floci | probe passed on floci; eligible to stay |
| iam:CreateInstanceProfile | floci | probe passed on floci; eligible to stay |
| iam:CreateRole | floci | probe passed on floci; eligible to stay |
| iam:DeleteInstanceProfile | floci | probe passed on floci; eligible to stay |
| iam:GetInstanceProfile | floci | probe passed on floci; eligible to stay |
| iam:GetRole | floci | probe passed on floci; eligible to stay |
| iam:ListInstanceProfiles | floci | probe passed on floci; eligible to stay |
| iam:RemoveRoleFromInstanceProfile | floci | probe passed on floci; eligible to stay |
| iam:TagInstanceProfile | floci | probe passed on floci; eligible to stay |
| pricing:GetProducts | pricing-facade | no probed backend passed it (floci failed) |
| sqs:CreateQueue | floci | probe passed on floci; eligible to stay |
| sqs:DeleteMessage | floci | probe passed on floci; eligible to stay |
| sqs:GetQueueUrl | floci | probe passed on floci; eligible to stay |
| sqs:ReceiveMessage | floci | probe passed on floci; eligible to stay |
| sqs:SendMessage | floci | probe passed on floci; eligible to stay |
| ssm:GetParameter | floci | probe passed on floci; eligible to stay |
| ssm:PutParameter | floci | probe passed on floci; eligible to stay |
| sts:GetCallerIdentity | floci | probe passed on floci; eligible to stay |

## The two failures

| Operation | What Floci lacks | Proposed backend |
|---|---|---|
| `ec2:DescribeInstanceTypes` | `networkInfo.maximumNetworkInterfaces` is only reported inside `networkCards.item`; real AWS reports it at `networkInfo` level too, and Karpenter derives pod density from it | `ec2-sim`, **reference data only** |
| `pricing:GetProducts` | Price data exists only for `us-east-1`, one product per page; `PriceList` is empty for `us-west-2` | `pricing-facade` |

**A8 re-plan (the plan's Task text overshoots).** `s0matrix.py` prints "Tasks 11 and 12 and 13", but Tasks 11 and 12 build a
whole EC2 simulator (store, launch templates, fleet). Only one uncoupled reference operation failed, so the fallback needed
is a thin reference-data service answering `DescribeInstanceTypes` from `internal/catalog`, which Task 9/11 already plan.
Task 13's `eks-facade` is **not** needed (EKS passes in mock mode); only `pricing-facade`. Task 14 (Moto) is **not** needed.
Alternative for pricing, user's choice: run the whole stack in `us-east-1`, where Floci's price data exists; this removes
`pricing-facade` but changes the region, AZ names and every seeded value.

## Karpenter as the oracle (criteria 1 to 3 by hand, A4), run on the live stack with all operations still on Floci

| Criterion | Result | Evidence |
|---|---|---|
| 1. Karpenter starts, stays up, discovers its world | **Pass** (one known error) | `1/1 Running`; discovered cluster endpoint, instance types, offerings, zones, kube DNS, Kubernetes version; `EC2NodeClass` `Ready=True` with `AMIsReady`, `SubnetsReady`, `SecurityGroupsReady`, `InstanceProfileReady` (instance-profile lifecycle ran through Floci IAM). The only error is `updating pricing: no on-demand pricing found` (the pricing failure above). |
| 2. Pending pod becomes a NodeClaim and an instance | **Pass** | Same second: `found provisionable pod(s)`, `created nodeclaim`, `launched nodeclaim` (`m5.large`, `aws:///us-west-2a/i-0a37af3f0ff865ffc`, AMI `ami-0abcdef1234567891`); instance present in Floci. |
| 3. Node registers as a KWOK-backed Node and the pod schedules | **Pass** (2026-10-10) | `node-registrar` created the Node 1 s after deploy for the waiting NodeClaim; Karpenter logged `registered` and `initialized`; NodeClaim `Launched/Registered/Initialized/Ready` all True; Node Ready via KWOK; `inflate` pod Running on it. Fresh cycle: Pending to Running in about 3 s. Scale to 0: Karpenter disrupted the empty node, terminated the instance through Floci (`TerminateInstances`), deleted the NodeClaim; the registrar removed the Node. Only Karpenter error in 25 min: the known pricing one. Details: `docs/s0/criterion3.md`. |

Karpenter did not move any operation the probe passed: nothing it called failed except pricing. All three criteria now pass with
every operation still on Floci, so the table stands. If a later task finds Karpenter failing on an operation Floci serves, that
operation moves to the next backend by editing one `backend` line (ADR-0002).

New constraint from criterion 3: Floci's `DescribeInstanceTypes` offers types the catalog does not have (for example
`m6gd.large`), and `node-registrar` makes no Node for them. The NodePool is pinned to catalog types, and the reference `ec2-sim`
must serve exactly the catalog.

## Observations that may affect criteria 3 and 4 (not failures)

- `DescribeInstances` carries no `instanceLifecycle=spot`, although `CreateFleet` reports `Lifecycle: spot`. Karpenter may
  therefore label spot instances on-demand; matters for the spot-interruption path (criterion 4, `simctl`).
- The missing `networkInfo.maximumNetworkInterfaces` may cap pods per node (ENI-limited pod density). It did not stop
  provisioning here; the workload or `EC2NodeClass` may need `kubelet.maxPods` if pods per node matters.
- The k3d server node is schedulable, so an unpinned pod never becomes pending. Workloads must select
  `karpenter.sh/nodepool` (done in this run); Task 15's workloads need it.
- The Karpenter-only call surface from the trace (12 operations, plus the NodePool-driven ones) is a subset of `ops.yaml`
  and of the probe; the trace records operation names only, so field-level comparison rests on the criteria above.
- Probe leaks (ephemeral stack): an IAM role, a launch template and instances; no `DeleteRole` in `ops.yaml`.

## Resource record (A9), idle plus one launched NodeClaim

| Container | Memory |
|---|---|
| k3d server (k3s + Karpenter) | 576 MiB |
| Floci | 65 MiB |
| aws-gateway | 11 MiB |
| k3d tools | 7 MiB |
| **Total** | **about 660 MiB** (no KWOK, workloads or fallbacks yet) |

## Decisions (answered 2026-10-10)

1. APPROVED. Table above (34 on Floci, `DescribeInstanceTypes` on a thin reference service, `pricing:GetProducts` on `pricing-facade`)?
2. Pricing: `pricing-facade`, keeping `us-west-2`.
3. Build Task 10 (`node-registrar`) and KWOK first so criterion 3 runs before the table is final. Done.
