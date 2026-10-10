# Criterion 4 by hand: a spot interruption is handled before the instance is terminated

Date: 2026-10-10 (stack clock 21:56 UTC). Not validated against real AWS.
Tool: `simctl` (Task 9), run on the `meridian` network through the gateway. Karpenter 1.14.1, spot NodePool.

## What passed

Karpenter's interruption handler received the warning, cordoned and drained the node, and replaced it. Karpenter then
terminated the instance, and `node-registrar` removed the Node. The garbage collector played no part.

| Time (UTC) | Actor | Event |
|---|---|---|
| 21:56:00.5 to 21:56:03.7 | Karpenter | `created`, `launched`, `registered`, `initialized` NodeClaim `default-wk9tr` (spot `m5.large`, instance `i-1980351e1ed997c3d`) |
| 21:56:22.209 | simctl | `interrupt -grace 45s -any-market i-1980351e1ed997c3d` starts and posts the warning to `meridian-g0-interruption` |
| 21:56:22.339 | Karpenter `interruption` | `initiating delete from interruption message` (kind `spot_interrupted`, action `CordonAndDrain`, queue `meridian-g0-interruption`) |
| 21:56:22.351 | Karpenter `node.termination` | `tainted node default-wk9tr` (cordon) |
| 21:56:22 | Deployment | the `inflate` pod is evicted and a replacement pod is created |
| 21:56:23.396 to 21:56:25.595 | Karpenter | replacement NodeClaim `default-gc72d` created, launched (`i-a87632d48e6457185`, `us-west-2b`), registered, initialized |
| 21:56:24 | node-registrar | `created node ip-10-0-2-11...` for the replacement instance |
| 21:56:28 | node-registrar | `deleted node ip-10-0-1-19...` because instance `i-1980351e1ed997c3d` terminated |
| 21:56:32.731 / .846 | Karpenter | `deleted node`, then `deleted nodeclaim default-wk9tr` |
| about 21:57:07 | simctl | grace period (45 s) ends; simctl reports `interrupted i-1980351e1ed997c3d`, exit 0 |

The message reached the handler 130 ms after `simctl` started. The pod ran on the replacement node (`ip-10-0-2-11`) 2 minutes later.

## What `simctl` refused

Without `-any-market`, `simctl` refused the same instance, sent nothing and left the NodeClaim untouched:
`instance i-1980351e1ed997c3d is not a spot instance (lifecycle ""); use AnyMarket if this backend omits instanceLifecycle`.
`simctl probe http://aws-gateway:4566` reported `reachable: HTTP 403` (exit 0).

## Findings and limits

1. **Floci omits `instanceLifecycle=spot`.** `CreateFleet` reports `Lifecycle: spot`, but `DescribeInstances` does not. Karpenter still
   treated the node as spot (the Node label `karpenter.sh/capacity-type=spot` and the `spot_interrupted` kind come from the NodeClaim),
   so the missing field did not stop the interruption path. `simctl` needs `-any-market` on this stack.
2. **Karpenter terminated the instance itself, 35 seconds before the grace period ended.** The grace period in `simctl` is therefore a
   backstop here. The `TerminateInstances` call that `simctl` sends at the end hits an instance that is already gone and still exits 0.
3. **The run used a 45 s grace, not the real 2 minutes.** The order of events does not depend on it.
4. **This run is manual.** The automated assertion (criterion C4 in the gate test, Task 16) must read Karpenter's log for the three
   lines above and check their order against the termination time. Not yet written.
5. **Not checked:** that Karpenter marked the spot offering unavailable (it logs this at debug level), `rebalancerecommendation`
   and `scheduledchange` events, and a spot interruption of a node that carries more than one pod.
6. The live NodePool was patched to `capacity-type In [spot]` for this run. `deploy/workloads/nodepool.yaml` still says `on-demand`.
   Task 15 needs a spot NodePool for the gate.
