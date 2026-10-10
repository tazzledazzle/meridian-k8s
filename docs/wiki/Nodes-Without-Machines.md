# Nodes Without Machines

Floci launches no machines, so nothing joins the cluster on its own. `node-registrar` fills that gap: it creates a Kubernetes Node for each running instance, and KWOK keeps the Node Ready. Source: `internal/noderegistrar`, `internal/catalog`, `deploy/kwok`.

## node-registrar

Every 2 seconds the registrar lists instances through the gateway (`ec2:DescribeInstances`, so its traffic is authenticated and traced like any client) and lists Nodes that carry its `meridian.io/managed-by` label. It then:

- creates a Node for each running instance that has none;
- deletes the Node of each instance that is gone;
- changes nothing if listing instances fails, because deleting Nodes after a brief EC2 outage would be worse than waiting one interval;
- skips an instance whose type is not in the catalog and logs the reason.

It uses a stdlib REST client (no client-go) and its own ServiceAccount with rights on Nodes only. It cannot delete pods. The instance tag `meridian.io/node-backend` selects the node backend; only `kwok` exists today, and an unknown backend name is skipped rather than silently replaced.

## The catalog

`internal/catalog` is the fixed region data for `us-west-2`: three zones and 12 instance types with vCPU, memory, ENI limits and approximate prices. The registrar sizes Nodes from it, and the planned `ec2-sim` and `pricing-facade` will read it too, so all three agree on what exists.

Karpenter can only choose types the catalog contains. `deploy/workloads/nodepool.yaml` therefore pins `c5.large`, `c5.xlarge`, `m5.large`, `m5.xlarge` and `amd64`.

## What a KWOK Node looks like

- Annotation `kwok.x-k8s.io/node=fake` opts the Node in to the KWOK controller.
- Taints: `karpenter.sh/unregistered:NoExecute` (Karpenter removes it after matching the Node to its NodeClaim) and `kwok.x-k8s.io/node=fake:NoSchedule`.
- Labels include instance type, zone, region, `type=kwok`, `meridian.io/instance-id` and `meridian.io/node-backend=kwok`.
- `providerID` is `aws:///<zone>/<instance-id>`, which is how Karpenter pairs the Node with the NodeClaim.
- Pod capacity follows the ENI formula `ENIs x (IPs per ENI - 1) + 2`. An `m5.large` has 3 ENIs and 10 IPs each, so `pods=29`. Karpenter's own NodeClaim capacity also said 29.

Every workload on a Karpenter node must tolerate the KWOK taint. `deploy/workloads/inflate.yaml` does. Without the toleration, Karpenter saw the pod still unschedulable after the Node appeared and created a second NodeClaim.

## Timeline from criterion 3

Measured on 2026-10-10 (`docs/s0/criterion3.md`), NodeClaim `default-5x77j`:

| Time | Event |
|---|---|
| 08:55:13 | `inflate` scaled to 1 replica |
| 08:55:14 | Karpenter finds the pod, creates and launches the NodeClaim |
| 08:55:15 | `node-registrar` creates the Node |
| 08:55:15.9 | Karpenter logs `registered` |
| 08:55:17 | Karpenter logs `initialized`; the pod is Running on the Node |

Pending pod to Running pod took 3 seconds. After scale-down at 08:54:18, the Node was gone about 30 seconds later: Karpenter disrupted the empty node (08:54:35), the registrar deleted the Node (08:54:41), and Karpenter deleted the NodeClaim (08:54:45). The gateway trace showed `ec2:TerminateInstances` count rise from 2 to 3.

The first launch in that run waited 14 minutes because the registrar was not yet deployed. It registered one second after deployment.

## Not verified

Registrar behavior during an API server outage, more than one pod per node, and the registrar's readiness: it has no HTTP endpoint, so its Deployment has no probe.
