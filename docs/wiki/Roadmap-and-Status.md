# Roadmap and Status

This page records where the project stands. Update the Status block here and in [[Home]] together.

<!-- STATUS-BLOCK:START (update here and in Home only) -->
## Status

- Done: spike S0, gateway, node-registrar, KWOK, the reference `ec2-sim` and the `pricing-facade`, criteria 1 to 3 by hand. The approved table is applied: 34 operations run on Floci, `DescribeInstanceTypes` on `ec2-sim`, `pricing:GetProducts` on `pricing-facade`. The probe passes 37 of 37 checks against this routed plane (it passed 35 of 37 against Floci alone), and Karpenter logs no AWS errors.
- Does not exist yet: `simctl` (spot interruption injector), the isolated no-internet gate stack, `make gate-karpenter` with its eight criteria, the gate test, CI, ADRs.
<!-- STATUS-BLOCK:END -->

Not validated against real AWS.

## Done

- Pinned compatibility matrix (`versions.yaml`).
- `aws-gateway` with SigV4 verification, per-operation routing, trace and record modes ([[The Gateway and ops.yaml]]).
- Contract probe, decision script and the approved table ([[The S0 Spike]]).
- Local stack: Floci behind the gateway, k3d, Karpenter 1.14.1 ([[Running It]]).
- `node-registrar`, `internal/catalog` and vendored KWOK; criterion 3 passes ([[Nodes Without Machines]]).
- Reference `ec2-sim` (`DescribeInstanceTypes` from `internal/catalog`) and `pricing-facade` (`pricing:GetProducts`), with the approved table applied to `ops.yaml` ([[The S0 Spike]]).

## In progress

Nothing. `simctl` is next.

## Planned

| Item | Purpose |
|---|---|
| `simctl` | Inject a spot interruption message and terminate the instance after a grace period |
| Isolated gate stack | Docker network `meridian-gate` with no internet and no published ports, offline image preload |
| `make gate-karpenter` | Run the gate test, `TestGate`, with eight criteria C1 to C8, including node registration, spot interruption and no-egress isolation |
| ADRs | Record the Floci adoption, the Docker-socket outcome, the privileged k3s container and the hand-written protocol codecs |
| CI | Vet, unit tests, script tests and the gate on a fresh runner; fail if the Floci pin changes without a refreshed report |

None of these exists in the repository today.

## Open questions

- Should trace mode forward unknown services to Floci, so the trace shows the full call surface? The trade-off is wider discovery against a weaker allow-list. Unanswered.
- Does Karpenter label spot instances on-demand because `DescribeInstances` omits `instanceLifecycle=spot`? Not tested.
- Will the real peak memory fit the target host? Only idle and one-pod figures exist (about 850 MiB).
- Do k3d and Floci run on an internal Docker network? Unverified.
- Do results survive a Floci version bump? They hold only for 2.2.0 at the pinned digest.

See [[Decisions]] for the full assumption list.
