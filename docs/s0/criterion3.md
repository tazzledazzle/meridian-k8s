# S0 criterion 3, run by hand on the live stack

Date: 2026-10-10. Stack: Floci 2.2.0 behind `aws-gateway` (trace mode), k3d cluster `meridian-g0` (k3s v1.35.9), Karpenter 1.14.1,
KWOK v0.8.0 (controller image pinned by digest), `node-registrar` (this repo, `cmd/node-registrar`). All operations still on Floci;
`ops.yaml` unchanged.

**Result: criterion 3 PASSES.** A NodeClaim launched through Floci gets a Node from `node-registrar`; Karpenter registers and
initialises it; KWOK makes it Ready; the pod schedules and runs; scaling to zero makes Karpenter terminate the instance through
Floci and delete the NodeClaim; `node-registrar` removes the Node. Karpenter logged no new AWS error beyond the known pricing one.

## What was built for this run

| Item | Where |
|---|---|
| `catalog` package (region, AZs, instance types with ENI limits and prices) | `internal/catalog` (Task 9 Step 1) |
| EC2 test double used by the registrar's client test | `internal/fakeec2` (test-only, needed by Task 10's tests) |
| `node-registrar` (stdlib REST client, no client-go) | `internal/noderegistrar`, `cmd/node-registrar`, `deploy/node-registrar.yaml` |
| KWOK v0.8.0 vendored + checksummed | `scripts/vendor-kwok.sh`, `deploy/kwok/{kwok.yaml,stage-fast.yaml,SHA256SUMS}` |
| KWOK apply (digest-pinned, hardened) | `scripts/kwok-up.sh`, `deploy/kwok/controller-patch.yaml`, `make kwok-up` |
| Registrar build + import + apply | `scripts/deploy-registrar.sh`, `make deploy-registrar` |
| Criterion workloads | `deploy/workloads/nodepool.yaml`, `deploy/workloads/inflate.yaml` |

Test run (`go build ./... && go vet ./... && gofmt -l internal cmd && go test -race -count=1 ./...`): all exit 0.
Coverage: `catalog` 100.0%, `fakeec2` 98.5%, `noderegistrar` 86.2% (`cmd/node-registrar/main.go` is a thin wiring file with no unit test).

## Evidence

### Deployment (item 2)

```
$ scripts/kc.sh -n kube-system get pods
coredns-b644fd95b-vn8wr                  1/1     Running   0          47m
karpenter-79dbcf9696-5pf86               1/1     Running   0          40m
kwok-controller-7cfccbd55f-mbfqq         1/1     Running   0          21s
local-path-provisioner-d9d8769b6-gpdgj   1/1     Running   0          47m
(node-registrar applied by `make deploy-registrar`: "deployment "node-registrar" successfully rolled out")
$ scripts/kc.sh auth can-i --list --as=system:serviceaccount:kube-system:node-registrar     (own ServiceAccount, nodes only)
nodes   []   []   [get list watch create update patch delete]
$ scripts/kc.sh auth can-i delete pods --as=system:serviceaccount:kube-system:node-registrar
no
```

KWOK controller image in the cluster: `registry.k8s.io/kwok/kwok:v0.8.0@sha256:6d25aa8fbdfe78845423160bf125b5513f9522e2770981f0945c2a250c2b26f0`
(re-running `make kwok-up` is a no-op: the same pod keeps running).

### Registration (item 3), first launch (NodeClaim `default-r4hqw`, instance `i-0a37af3f0ff865ffc`, launched 08:38:51Z)

The claim waited 14 minutes with no Node. Deploying the registrar at 08:52:57Z:

```
node-registrar: 2026/10/10 08:52:58 created node ip-10-0-1-12.us-west-2.compute.internal for i-0a37af3f0ff865ffc
karpenter 08:52:58.040Z INFO registered nodeclaim default-r4hqw
karpenter 08:52:59.100Z INFO initialized nodeclaim default-r4hqw

NodeClaim conditions: Launched=True Registered=True Initialized=True Consolidatable=True Ready=True

providerID: aws:///us-west-2a/i-0a37af3f0ff865ffc
taints (after Karpenter removed karpenter.sh/unregistered): [{"effect":"NoSchedule","key":"kwok.x-k8s.io/node","value":"fake"}]
allocatable: {"cpu":"2","ephemeral-storage":"20Gi","memory":"8Gi","pods":"29"}
node conditions: Ready=True MemoryPressure=False DiskPressure=False PIDPressure=False NetworkUnavailable=False
labels: karpenter.sh/registered=true karpenter.sh/initialized=true karpenter.sh/nodepool=default
        node.kubernetes.io/instance-type=m5.large topology.kubernetes.io/zone=us-west-2a
        meridian.io/managed-by=node-registrar meridian.io/instance-id=i-0a37af3f0ff865ffc meridian.io/node-backend=kwok type=kwok
annotations: kwok.x-k8s.io/node=fake
NodeClaim status.capacity: {"cpu":"2","memory":"7577Mi","pods":"29","vpc.amazonaws.com/pod-eni":"9"}   (Karpenter's own ENI math gives 29 too)
```

### Fresh cycle with timings (NodeClaim `default-5x77j`, instance `i-8b1e508cdab176611`)

```
08:55:13  scale deploy/inflate --replicas=1
08:55:14.165Z karpenter  found provisionable pod(s)
08:55:14.177Z karpenter  created nodeclaim default-5x77j
08:55:14.234Z karpenter  launched nodeclaim default-5x77j
08:55:15      node-registrar created node ip-10-0-1-14.us-west-2.compute.internal for i-8b1e508cdab176611
08:55:15.938Z karpenter  registered nodeclaim default-5x77j
08:55:16.968Z karpenter  initialized nodeclaim default-5x77j
08:55:16  pod/inflate-75b7ddd6cd-wcc7r 1/1 Running on node ip-10-0-1-14.us-west-2.compute.internal (pod IP 10.42.2.1, assigned by KWOK)
```

Pending pod to Running pod: **3 seconds**.

### Teardown (item 4)

```
08:54:18  scale deploy/inflate --replicas=0
08:54:35.199Z karpenter  disrupting node(s) (Empty)
08:54:35.256Z karpenter  tainted node
08:54:41      node-registrar deleted node ip-10-0-1-12... (instance i-0a37af3f0ff865ffc terminated)
08:54:45.646Z karpenter  deleted node
08:54:45.764Z karpenter  deleted nodeclaim default-r4hqw
gateway trace: ec2:TerminateInstances count 2 -> 3
```

The Node was gone from `kubectl get nodes` at 08:54:48; only the k3d server node remained.

### Karpenter errors (item 5)

Over the 25 minutes covering the whole run, every ERROR/WARN line:

```
2  ERROR  updating pricing, no on-demand pricing found      (the known pricing failure)
```

### Trace (item 6)

`make trace-show`: 36 distinct operations, 0 not in `ops.yaml` (full listing in the run output; includes `ec2:CreateFleet x6`,
`ec2:RunInstances x3`, `ec2:TerminateInstances x3`, `ec2:DescribeInstances x181`, `ec2:DescribeInstanceStatus x124`).

### Memory (`docker stats --no-stream`, with KWOK, registrar and one running pod)

| Container | Memory |
|---|---|
| k3d server (k3s + Karpenter + KWOK + registrar) | 755 MiB |
| Floci | 67 MiB |
| aws-gateway | 11 MiB |
| k3d tools | 7 MiB |
| **Total** | **about 840 MiB** (was about 660 MiB before KWOK and the registrar) |

## Findings and deviations

1. **A pod that does not tolerate the KWOK taint leaves the node unused.** The first run had a Pending `inflate` pod without the
   toleration; once the Node appeared, Karpenter saw the pod still unschedulable and created a second NodeClaim (`default-d66f8`).
   `deploy/workloads/inflate.yaml` now tolerates `kwok.x-k8s.io/node=fake:NoSchedule`. Every workload on a Karpenter node needs it.
2. **The catalog bounds what Karpenter may choose.** With the original NodePool, Karpenter picked `m6gd.large`
   (arm64 with local disk), which Floci's `DescribeInstanceTypes` offers but `internal/catalog` does not. The registrar reported it as
   skipped (`unknown type "m6gd.large"`, logged every 2 s) and made no Node; the claim was deleted by hand. `deploy/workloads/nodepool.yaml`
   now adds `node.kubernetes.io/instance-type In [c5.large c5.xlarge m5.large m5.xlarge]` and `kubernetes.io/arch In [amd64]`.
   Later tasks (the `DescribeInstanceTypes` reference service) must serve exactly the catalog, so the two agree.
3. **ENI-limited pod density works.** Node allocatable `pods=29` (3 ENIs x (10-1) + 2) and Karpenter's own NodeClaim capacity says 29,
   so the missing `networkInfo.maximumNetworkInterfaces` in Floci's `DescribeInstanceTypes` did not change the figure on `m5.large`.
4. **Spot lifecycle not exercised.** This run used on-demand only. Floci's `DescribeInstances` omitting `instanceLifecycle=spot`
   (decision.md) remains open for criterion 4.
5. **Floci gaps visible in Node labels.** Karpenter labels come from Floci data: `karpenter.k8s.aws/instance-hypervisor=""`,
   `instance-cpu-manufacturer=""`, `topology.k8s.aws/zone-id=us-west-2-az1` (the catalog says `usw2-az2` for `us-west-2a`).
   Nothing selected on these, so nothing failed.
6. **Instance IDs and node names.** Floci supplies `privateDnsName` (`ip-10-0-1-N...`), so node names are unique per instance. The
   registrar's deletion is idempotent: it deleted the Node while Karpenter's finalizer still held it and logged the delete twice
   (08:54:41 and :43). Harmless; a later tidy-up could skip Nodes that already have a `deletionTimestamp`.
7. **KWOK needs.** CRDs (`stages`, `attaches`, `execs`, `logs`, `metrics`, `portforwards`, `resourceusages` and their cluster
   variants), a ClusterRole over nodes (get/list/watch), `nodes/status` and `pods/status` (patch/update), pods (get/list/watch/patch/
   update/delete), events, leases, and its own CRDs; plus a FlowSchema. It manages only Nodes annotated `kwok.x-k8s.io/node=fake`
   (the registrar sets that), keeps them Ready via the `node-initialize` and `node-heartbeat-with-lease` stages, and sets pods
   Running via `pod-ready`. It runs as root in the vendored manifest; `deploy/kwok/controller-patch.yaml` adds resource limits,
   `allowPrivilegeEscalation: false` and `drop: [ALL]` (it was not made non-root: the vendored mount is under `/root`).
8. **Deviations from the plan.** (a) Probes: `node-registrar` has no HTTP endpoint, so its Deployment has no liveness/readiness probe
   (a crash restarts the pod; a stuck loop is not detected). (b) The image comes from `docker build` + `k3d image import` with
   `imagePullPolicy: Never`; the offline preload (Task 15) is not done. (c) The KWOK controller image is pulled by digest by the
   cluster itself (it has internet during S0). (d) `cmd/versionsenv/makefile_test.go` now expects five binaries. (e) No
   NetworkPolicy was added. (f) `fakeec2` was created in Task 10 rather than Task 9 because the registrar's tests need it.
9. **Not verified.** The `real` backend path (skipped, by design). Behaviour of the registrar under an apiserver outage. The gate's
   offline (no-internet) image loading. Pod placement of more than one pod per node (density beyond one pod).
