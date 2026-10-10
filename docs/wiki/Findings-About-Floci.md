# Findings About Floci

These facts about Floci 2.2.0 come from the S0 runs recorded in `docs/s0/smoke.md`, `harness.md`, `decision.md` and `criterion3.md`. Each entry says how it was observed. They may not hold for other Floci versions.

## Mock modes replace the Docker socket

Floci's EC2 and EKS services launch containers by default. With no Docker socket they retry the missing socket, and `eks:CreateCluster` hangs until the gateway times out. Setting `FLOCI_SERVICES_EC2_MOCK=true` and `FLOCI_SERVICES_EKS_MOCK=true` makes both metadata-only. The cluster is `ACTIVE` immediately, with `serviceIpv4Cidr 10.100.0.0/16`.

Observed: `CreateCluster` hung without the flags and succeeded with them (`harness.md` finding 7). After a fleet launched, `docker ps` showed only Floci and no instance containers (`smoke.md`).

## The AMI and SSM parameter are built in

Floci refuses `ssm:PutParameter` under `/aws/`, as real AWS does. It already serves the public AL2023 parameter for Kubernetes 1.35 (`ami-0abcdef1234567891`) and ships a matching AMI. `scripts/seed-floci.sh` reads and checks them instead of writing.

Observed: the write was refused, the read returned the ID, and `DescribeImages` found it. `ec2:RegisterImage` works but returns a new ID on every call, so the seed does not use it.

## Cold start: healthy before ready

Floci reports healthy before every service answers. STS answered first and EC2 `DescribeImages` a few seconds later. Observed during the first seed run. The seed now retries each service for up to 60 seconds.

## Pricing covers us-east-1 only

`pricing:GetProducts` returns one product per page for `us-east-1`, and `PriceList: []` for `us-west-2`, this stack's region. Observed: Karpenter logged `no on-demand pricing found` 14 times in about 60 seconds. This failed probe check led to `pricing-facade` (see [[The S0 Spike]]).

## networkInfo ENI field

`DescribeInstanceTypes` reports `maximumNetworkInterfaces` only inside `networkCards.item`. Real AWS also reports it at `networkInfo` level, and Karpenter derives pod density from it. Observed: the probe check failed. On `m5.large` the NodeClaim still reported `pods=29`, so the gap did not change that figure (`criterion3.md` finding 3). Other types are untested (assumption `meridian-17s.52`).

## No instanceLifecycle=spot

`CreateFleet` reports `Lifecycle: spot`, but `DescribeInstances` omits `instanceLifecycle=spot`. Observed in `smoke.md` and the probe report. Karpenter may therefore label spot instances on-demand. This is untested: the criterion 3 run used on-demand only.

## Other gaps

- `DescribeInstanceTypes` offers types the catalog lacks, such as `m6gd.large`. Karpenter chose it once and the registrar made no Node. The NodePool now pins four catalog types.
- Floci-derived Node labels differ from the catalog: `instance-hypervisor=""`, `instance-cpu-manufacturer=""`, and `topology.k8s.aws/zone-id=us-west-2-az1` where the catalog says `usw2-az2` for `us-west-2a`. Nothing selected on them, so nothing failed.
- Floci has a default VPC but no subnet. Create one before `CreateFleet`.
- `eks:DescribeCluster` returns 404 for an unseeded name. Karpenter crash-loops until the cluster exists.
- SQS `QueueUrl` points at `floci:4566`, not the gateway. This is harmless for JSON calls.
