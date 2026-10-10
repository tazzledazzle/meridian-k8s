# Meridian

Meridian lets you run unmodified Karpenter against a local, simulated AWS. Karpenter's AWS SDK calls go to one gateway. The gateway verifies the SigV4 signature, picks a backend per operation, and forwards the call. No machines are created: a small controller turns each simulated EC2 instance into a KWOK node.

Not validated against real AWS. Every result in this wiki comes from a local run of the pinned versions listed in [[Running It]].

## What exists today

- `aws-gateway`: SigV4 verification, per-operation routing from `ops.yaml`, trace and record modes.
- A contract probe (`s0probe`) and a decision script (`scripts/s0matrix.py`) that choose a backend for each operation.
- `node-registrar` and KWOK, which turn instances into schedulable Nodes.
- A local stack (`make s0-up`) with Floci 2.2.0, the gateway, a k3d cluster and Karpenter 1.14.1.
- Result: Karpenter criteria 1 to 3 pass by hand on this stack.

<!-- STATUS-BLOCK:START (update here and in Roadmap-and-Status only) -->
## Status

- Done: spike S0 (35 of 37 probe checks pass), gateway, node-registrar, KWOK, criteria 1 to 3 by hand.
- Being built now: a reference `ec2-sim` (serves `DescribeInstanceTypes` from `internal/catalog`) and a `pricing-facade`. Until they exist, `ops.yaml` still routes every operation to Floci.
- Does not exist yet: `simctl` (spot interruption injector), the isolated no-internet gate stack, `make gate-karpenter` with its eight criteria, the gate test, CI, ADRs.
<!-- STATUS-BLOCK:END -->

## Map of the wiki

| Page | What you learn |
|---|---|
| [[Architecture]] | Components and the path of one request |
| [[The Gateway and ops.yaml]] | What the gateway accepts, refuses and records |
| [[The S0 Spike]] | How the backend decision was made |
| [[Findings About Floci]] | Verified facts about Floci 2.2.0 |
| [[Nodes Without Machines]] | How instances become Nodes |
| [[Security Model]] | Defaults, isolation, and the one privileged container |
| [[Testing Strategy]] | How the code is tested |
| [[Running It]] | Commands, in order, and known problems |
| [[Decisions]] | D1 to D15 and the open assumptions |
| [[Roadmap and Status]] | Done, in progress, planned, open questions |
| [[Glossary]] | Terms used across the pages |

Start with [[Architecture]], then [[Running It]].
