# Meridian Platform: Design Spec

Date: 2026-10-09
Status: Design approved in brainstorming; pending written-spec review
Working name: `meridian` (module path `github.com/tazzledazzle/meridian`, rename freely)

## 1. Goal

A single GitHub repo that demonstrates, through running code, every duty and requirement of a Kubernetes/AWS Platform Engineer role (EKS, ECS, S3, VPC, RDS, IAM, CloudFormation, Helm, Karpenter, Istio, CI/CD with Jenkins, GitLab, CircleCI, Terraform, Ansible and Spinnaker, Prometheus, Grafana, CloudWatch, ELK, multi-region, incident management, security and compliance, documentation).

Real AWS is never required. Every AWS surface the platform touches is served by either an open-source equivalent behind an adapter, or a custom Go facade that implements the AWS API. The real `aws` Terraform provider, AWS CLI, Karpenter's AWS provider and Spinnaker's AWS provider are used unmodified against these endpoints. The public claim is "1:1 API parity for the N supported operations", where N and the operation list come from the generated conformance report, so the claim never exceeds what the report shows. "Parity" means request and response shapes, error codes and documented state transitions match the AWS service models and documentation, as verified by the test suite. The project states plainly: "Not validated against real AWS. This project runs fully locally by design, so anyone can reproduce it at zero cost."

## 2. Decisions

| Topic | Decision |
|---|---|
| Runtime | Fully local. No real AWS. Compose plus k3d. |
| Cluster service | Custom EKS-API facade over real k3d clusters. A from-scratch Kubernetes API server was rejected (Helm, Istio, Karpenter and Argo need a conformant API). |
| EC2 | Custom `ec2-sim` implementing the EC2 calls Karpenter uses, registering KWOK-backed nodes on launch. |
| Language | Go for services. Python for `platformctl`. Bash for glue. |
| Primary CI/CD | GitHub Actions + Argo CD + Argo Rollouts + Terraform + Ansible. |
| Other CI/CD | Jenkins, GitLab CI, CircleCI and a "Spinnaker slot" each deliver the same sample service ("parity matrix", M9). |
| Spinnaker slot | Spinnaker's memory footprint is a known concern. Spike S1 (time-boxed, before M8) looks for an open-source alternative that fits a memory budget. If none fits, a custom Go component (`deploy-orchestrator`) recreates Spinnaker's functionality. See section 4a. |
| Sequencing | Gate G0 (Karpenter proven against simulated AWS) blocks everything else. After G0: walking skeleton, then deepen one posting bullet per milestone. |
| Regions | Two simulated regions, `us-west-2` and `us-east-1`, each its own k3d cluster. |

## 3. Architecture

### 3.1 AWS-compatible plane (all under `cmd/`, shared code in `internal/awsapi/`)

| Service | Kind | Backing | Key operations |
|---|---|---|---|
| `aws-gateway` | custom | routes by SigV4 service name | one endpoint for Terraform, CLI and tools; verifies SigV4 with local credentials |
| `eks-facade` | custom | k3d, own embedded state (not proxied to Moto's EKS, to avoid two sources of truth) | CreateCluster, DescribeCluster, ListClusters, DeleteCluster, CreateNodegroup/DescribeNodegroup/DeleteNodegroup, CreateAddon/DescribeAddon, GetToken. `CreateNodegroup` launches instances through `ec2-sim`, so managed nodegroup nodes and Karpenter nodes share the same `ec2-sim` → `node-registrar` path |
| `ec2-sim` | custom | in-process state + KWOK | CreateFleet, RunInstances, TerminateInstances, DescribeInstances/InstanceTypes/Subnets/SecurityGroups/Images, launch templates, pricing and spot pools, interruption events to SQS |
| `node-registrar` | custom controller | Kubernetes API, with two backends chosen per instance by a launch tag | watches `ec2-sim` instance state and, when an instance is `running`, creates a node: `real` (a k3d agent container that joins the cluster and runs real pods) or `kwok` (a simulated Node with no containers); deletes the node on terminate |
| `pricing-facade` | custom | static price table plus spot price model | GetProducts, DescribeSpotPriceHistory |
| `ecs-facade` | custom | Docker containers | CreateCluster, RegisterTaskDefinition, CreateService, RunTask, Describe* |
| `ecr-facade` | adapter | local OCI registry | repository and auth-token APIs |
| `rds-facade` | adapter | Postgres containers | CreateDBInstance, DescribeDBInstances, DeleteDBInstance |
| S3 | open source | MinIO | via gateway |
| IAM, STS, SQS, VPC, DynamoDB, CloudFormation, Secrets Manager, SSM | open source | Moto | via gateway |
| `cw-shim` | custom | Prometheus (metrics), Elasticsearch (logs) | PutMetricData, GetMetricData, PutLogEvents, FilterLogEvents |

Terraform state uses the S3 backend (MinIO) with DynamoDB locking (Moto).

### 3.2 Platform plane (real software)

Helm, Karpenter (real AWS provider pointed at the gateway), Istio (multi-cluster), Argo CD, Argo Rollouts, Prometheus, Grafana, Elasticsearch/Logstash/Kibana with Fluent Bit, Kyverno.

### 3.3 Delivery and tooling

Terraform modules use the real `aws` provider. Modules are written to be provider-clean so that removing the endpoint override would target real AWS; this has not been applied to real AWS and the README says so. One CloudFormation stack, one Ansible bootstrap playbook, `platformctl` (cost report, regional failover, drain, runbook automation), and per-system pipeline definitions under `pipelines/`.

### 3.4 Repository layout

```
cmd/            aws-gateway, eks-facade, ec2-sim, ecs-facade, ecr-facade, rds-facade, cw-shim,
                deploy-orchestrator (only if S1 outcome C)
internal/awsapi SigV4, protocol codecs (query/JSON/REST-XML), AWS error envelopes, state store (bbolt)
infra/          terraform/{modules,envs}, cloudformation/, ansible/
platform/       helm/, argocd/, istio/, karpenter/, kyverno/, observability/
pipelines/      github/, jenkins/, gitlab/, circleci/, spinnaker/
tools/          platformctl/ (Python), scripts/ (Bash)
test/           conformance/, e2e/, chaos/
docs/           adr/, runbooks/, diagrams/, site/
```

## 4. Milestones

Each milestone is a tagged release in which the repo still works end to end. M8 and M9 are independent of M0 to M7; if they slip, M0 to M7 remain a complete release.

| M | Name | Scope | Acceptance |
|---|---|---|---|
| G0 | Karpenter proof gate (blocks all other work) | Start by tracing real Karpenter through a recording proxy to fix the exact AWS call list. The supported-operation list lives in one YAML file that drives everything else (see "API generation" in section 6). Day-1 check: confirm whether an existing Go server-side generator for AWS's published Smithy models is usable; if not, build a small in-repo generator, time-boxed to 3 days, and fall back to handwritten codecs per protocol (still validated against the models) if it isn't paying off by then. Deliver `ec2-sim` (fleet, launch templates, instance and offering lookups, subnets, security groups, images, tagging), `node-registrar` controller (KWOK-backed Node on instance `running`, deleted on terminate), `pricing-facade` (GetProducts, spot price history), SSM parameters (AMI aliases), IAM instance profiles, SQS interruption queue fed by `ec2-sim` spot events, a minimal `eks-facade` stub serving DescribeCluster, and STS. SSM, IAM, SQS and STS come from Moto unless the trace shows gaps, which are then filled with custom code. Runs on a hand-created k3d cluster; the full `eks-facade` stays in M0. Decision checkpoint: after criteria 1 to 3 pass, or after 3 weeks, whichever comes first, the user decides whether to continue or narrow scope. | `make gate-karpenter` passes from a clean checkout: (1) Karpenter starts with no real credentials and no startup errors; (2) a pending pod leads to a NodeClaim, `CreateFleet`, an instance `running`, a Ready Node and a scheduled pod; (3) consolidation terminates the instance and deletes the Node; (4) a spot interruption message on SQS leads to cordon, drain and replacement; (5) Karpenter picks the cheaper offering from `pricing-facade` data; (6) the AMI alias resolves through SSM and the instance profile is created and attached through IAM; (7) the test runs with egress blocked and asserts zero outbound calls; (8) it repeats from a clean checkout |
| M0 | Walking skeleton | Repo scaffold, `aws-gateway`, `eks-facade` (create, describe, delete; accepts `kubernetesNetworkConfig.serviceIpv4Cidr` and a deterministic per-region pod CIDR so the two regions never overlap; managed nodegroups use the `real` node backend, 2 to 3 nodes per cluster), Terraform `eks` module, Argo CD bootstrap, one Helm-packaged sample service, Prometheus and Grafana, Actions lint and plan, README, ADR-001 (threat model), image-digest and chart-version pinning, Trivy in CI | `make up && make e2e-m0`: `terraform apply` creates a cluster through the gateway, Argo CD syncs the service, a metric is visible in Grafana |
| M1 | AWS depth | MinIO (S3), `rds-facade`, Moto (IAM, STS, SQS, VPC, DynamoDB), IRSA via OIDC, `ecr-facade`, Terraform remote state, one CloudFormation stack, tagging for cost allocation | Terraform modules for VPC, IAM, S3, RDS and ECR apply cleanly; pods assume a role via IRSA; CFN stack deploys |
| M2 | Karpenter on the platform | Gate G0 components integrated with the full `eks-facade` and Terraform; NodePools and EC2NodeClasses managed through Helm and Argo CD | G0 criteria 2 to 5 pass again against a cluster created by `eks-facade` through Terraform |
| M3 | Istio and progressive delivery | Strict mTLS, AuthorizationPolicy, Argo Rollouts canary with Istio traffic shifting, mesh telemetry | A bad canary auto-rolls back; plaintext traffic is rejected |
| M4 | Multi-region and HA | Starts with a 2-day probe of cross-cluster discovery and failover in a throwaway setup. Then: second region, Istio multi-network multi-primary on one shared Docker network with east-west gateways (MetalLB or equivalent) and `platformctl mesh link` for remote secrets, 3-server control planes, PDBs, topology spread, `platformctl failover`. Fallback if the probe fails: failover at the ingress layer (weighted routing between per-cluster meshes) and the README states that cross-cluster mesh is not demonstrated | Failover drill moves traffic to the second region within 60 seconds of the primary being marked unhealthy |
| M5 | Observability and incidents | Alert rules and SLOs, ELK, `cw-shim`, runbooks, chaos drills (fault injection), postmortem template | Injected faults page via Alertmanager; runbook steps resolve each drill |
| M6 | Security and compliance | Kyverno policies, least-privilege IRSA, NetworkPolicies, Trivy and Checkov in CI, controls-evidence report | CI fails on a policy violation; report maps controls to evidence |
| M7 | Platform automation | `platformctl` cost report and drain, Ansible bootstrap, HPA plus Karpenter scaling demo | One command provisions a node bootstrap; cost report totals match `ec2-sim` pricing |
| S1 | Spinnaker-alternative spike | Time-boxed to 2 working days, run before M8. See section 4a | ADR recording the outcome (A, B or C) with measured memory figures |
| M8 | ECS | `ecs-facade`, Terraform ECS service, ECS deploy through the Spinnaker-slot component chosen in S1 | Terraform-managed ECS service runs; the Spinnaker-slot component deploys a new task definition |
| M9 | CI/CD parity matrix | Jenkins, GitLab CI, CircleCI and Spinnaker-slot pipelines for the sample service; state persisted to MinIO; compose profiles | All five pipelines build, push to `ecr-facade`, deploy and pass the same canary check ("pipeline contract") |
| M10 | Polish | Docs site, diagrams, recorded demo, ADR on translating the design to real AWS | A new reader can run M0 from the README in under 15 minutes |

## 4a. Spike S1: the Spinnaker slot

**Why:** Spinnaker is a set of microservices (orchestration, cloud provider, API gateway, pipeline storage, UI, and others) and is memory-heavy for a laptop-class demo. The posting names it, so the slot has to be filled by something real.

**Requirements the slot must meet (the "Spinnaker functions"):**
1. Pipeline-as-code with ordered stages (build or bake, deploy, wait, manual judgment, canary analysis, rollback).
2. Deploys to ECS through `ecs-facade` and to Kubernetes.
3. Canary judgment from Prometheus metrics.
4. Durable pipeline state and execution history.
5. An API and CLI to trigger and inspect executions.

**Budget (a design parameter I chose; adjust if you disagree):** the whole slot, including its storage, must stay at or below 2 GiB resident memory while idle with one pipeline executing, measured with `docker stats` and recorded in the ADR.

**Candidates evaluated, in this order:**
1. **Spinnaker minimal profile:** only the services needed to run pipelines headless (orchestration, cloud provider, API gateway, pipeline storage, a cache), with no UI, notifications, SCM integration or image baking.
2. **Argo Workflows + Argo Rollouts (+ Kayenta or Flagger for canary analysis):** assessed for whether stages 1 to 5 can be met, in particular ECS deployment, which these tools do not do natively.
3. **Tekton:** assessed the same way.

**Outcomes:**
- **A. Spinnaker minimal fits the budget and meets requirements 1 to 5.** Use real Spinnaker. The repo can then legitimately claim Spinnaker usage.
- **B. An open-source alternative meets 1 to 5 within the budget.** Use it, with a thin adapter for ECS if needed. The repo must say it uses that tool and must not claim Spinnaker usage.
- **C. Neither fits.** Build `cmd/deploy-orchestrator` (Go). It recreates the functions above: it accepts a documented subset of Spinnaker's pipeline JSON (deploy, wait, manual judgment, canary analysis, rollback stages), executes against `ecs-facade` and Kubernetes, queries Prometheus for canary judgment, persists executions in MinIO, and exposes an HTTP API plus a `platformctl` subcommand. Its docs describe it as Spinnaker-inspired, never as Spinnaker.

**Honesty rule:** the README, the posting-coverage table and the demo state which outcome was chosen. The repo never claims Spinnaker experience unless outcome A was chosen.

## 4b. Release lines and contingency

- **No partial tags.** A milestone is tagged only when its acceptance test passes. A failing milestone stays unreleased on a branch.
- **Release lines, each presentable on its own:** v0.1 = G0; v1.0 = through M3 (the minimum credible portfolio: AWS-compatible plane, Karpenter, Helm, Terraform, Argo CD, Istio canaries); v2.0 = through M7; v3.0 = M8 to M10 (including S1 before M8).
- **README status table** maps every posting bullet to `done`, `in progress` or `planned` and is updated at each tag, so claims match reality from the first public commit.
- **No silent cuts.** Narrowing any milestone requires an explicit user decision recorded in an ADR and in the status table. Suggested narrowing order if the schedule forces it: M9 breadth, then `cw-shim` depth, then the M4 mesh (which already has a fallback).
- **Failures become evidence.** Any failed probe, gate checkpoint or spike produces a short ADR (what was tried, the evidence, the decision).

## 5. Posting coverage

| Posting requirement | Evidence | Milestone |
|---|---|---|
| Kubernetes platform (HA, networking, storage) | `eks-facade`, multi-server k3d, PDBs, topology spread, storage class | M0, M4 |
| AWS (EKS, ECS, S3, VPC, RDS, IAM, CloudFormation) | Terraform modules, MinIO, `rds-facade`, Moto, `ecs-facade`, CFN stack | M1, M8 |
| Helm | Library chart plus 2 to 3 app charts with tests | M0 |
| Karpenter | `ec2-sim`, NodePools, spot and consolidation demo | M2 |
| Istio | mTLS, AuthorizationPolicy, canary shifting, multi-cluster | M3, M4 |
| Automation, scaling, CI/CD | Actions, Argo, Rollouts, HPA, Ansible, Jenkins, GitLab, CircleCI, Spinnaker slot (S1 outcome A, B or C) | M0, M7, S1, M9 |
| Incident management | Alerts, SLOs, runbooks, chaos drills, postmortem template | M5 |
| Security and compliance | Kyverno, IRSA, Trivy, Checkov, controls report | M6 |
| Documentation | ADRs from M0, docs site, diagrams, demo | M0 to M10 |
| Multi-region | Two regions, failover drill | M4 |
| Python, Bash, Go | Go services, `platformctl`, scripts | all |
| Prometheus, Grafana, CloudWatch, ELK | Prometheus, Grafana, ELK, `cw-shim` | M5 |

## 6. Fidelity, errors, security

- **Fidelity:** every facade is validated against the published AWS service models (operation shapes and error codes). The real Terraform provider, AWS CLI, Karpenter and Spinnaker serve as the oracle.
- **API generation:** the supported-operation list is one YAML file. A generator reads AWS's published Smithy API models (Apache-2.0) and emits typed request/response structs and handler interfaces, so handwritten code is only business logic. Each protocol family (XML query, JSON-RPC, REST-JSON) is implemented once in `internal/awsapi/` and verified by round-trip tests using the real `aws-sdk-go-v2` clients as the oracle. The same YAML drives handler stubs, the conformance report and the Terraform support matrix, so they cannot disagree.
- **Version pinning:** one `versions.yaml` compatibility matrix is the single source of truth, consumed by Make, Compose, Terraform and Helm values; images are pinned by digest and charts and providers by version. Automated upgrade PRs (Renovate or Dependabot) never merge unless `make gate-karpenter` and the Terraform contract tests pass. G0's recorded Karpenter AWS call list is committed as a golden file; CI re-records it on every Karpenter upgrade and diffs it, so a newly required AWS call shows up as a visible diff. k3s is pinned to a Kubernetes minor that EKS supports and that Karpenter's compatibility table lists, and `eks-facade` reports exactly that version.
- **Node backends:** managed nodegroups use the `real` backend and host everything that must actually run (Argo CD, Prometheus, Istio, the sample service). Karpenter-launched nodes default to `kwok`, used with placeholder load-generator pods for scale-up, consolidation and spot demos; a NodePool or NodeClass flag lets a specific pool opt into `real` for a single end-to-end demo. KWOK nodes carry KWOK's conventional taint so real workloads never land on them, and real workloads (Istio, Rollouts canaries) are pinned to `real` nodes. Docs and the conformance report state that KWOK nodes simulate capacity, not execution. `ec2-sim` → `node-registrar` remains the single path and the single source of truth for instances.
- **Conformance report:** CI generates a per-operation table (supported, partial, unsupported) from the contract tests and publishes it in the docs, linked from the README. Every operation carries one fidelity label: `shape-validated` (checked against the service models), `behavior-inferred` (state machines and timings reconstructed from documentation and client behavior), or `real-aws-verified` (populated only if the user runs the optional, off-by-default `make aws-smoke` against their own account, with automatic teardown, never in CI). The spec does not claim real AWS is unnecessary to test: parity is measured against models and documentation, not against the real service.
- **Terraform contract:** the spec supports exactly these resources and data sources: VPC and subnets, security groups, IAM role/policy/instance profile, S3 bucket, EKS cluster/nodegroup/addon, ECR repository, RDS instance, SQS queue, ECS cluster/service. Anything else is out of scope. For every supported resource a contract test applies it, runs a second `terraform plan` that must show no changes (this catches perpetual-diff bugs), then destroys. The `aws` provider version is pinned and upgraded only through the contract tests. Endpoint overrides and `skip_*` flags live only in each environment's `providers.tf`; a CI lint fails if anything under `infra/terraform/modules/` contains them.
- **Errors:** AWS-faithful error codes and envelopes, idempotency tokens honored, a fault-injection API (capacity shortages, latency, spot interruptions), restart-safe reconciliation from embedded state.
- **Security:**
  - Scope: a single-user dev machine, never a shared host or a cloud VM (stated in the M0 threat-model ADR).
  - The gateway verifies SigV4. `make init` generates random local access keys into a gitignored `.env`; no default or committed keys, and no auth-bypass flags in any profile. Policy evaluation is delegated to Moto's IAM where applicable.
  - Docker access: rootless Docker or Podman is the documented preferred setup. Where the socket is needed, a socket proxy allows only the API categories k3d and the facades use (containers, networks, volumes, images, exec) and denies the rest. The ADR states honestly that this reduces but does not eliminate host risk.
  - Binding: every service listens on `127.0.0.1` or an internal Docker network by default. LAN exposure requires an explicit opt-in flag that prints a warning.
  - Supply chain: images pinned by digest and charts by version from M0, with Trivy in the M0 CI job.
  - k3d nodes are privileged by design; the ADR says so.
  - No security control is weakened for convenience in any milestone.

## 7. Testing

- Go unit tests, written test-first.
- Conformance tests per facade against the service models.
- Per-milestone end-to-end targets (`make e2e-mN`).
- GitHub Actions runs unit, conformance and `e2e-m0` (hosted runners can run k3d). Later end-to-end tests and the M9 matrix run locally behind compose profiles.

## 7a. Run profiles and resource budgets

Everything does not run at once. Three Compose/Make profiles:

| Profile | Contents | Starting memory target |
|---|---|---|
| `core` | G0 and M0 to M3: one region, Karpenter, Istio, Argo, Prometheus and Grafana, the AWS-compatible plane | fits in 8 GiB free |
| `full` | `core` plus the second region, ELK (single-node, capped heap), `cw-shim`, Kyverno | fits in 16 GiB |
| `ci-matrix` | Jenkins, GitLab CE, CircleCI runner and the Spinnaker-slot component, started one at a time on demand | one tool at a time on top of `core` |

Budgets are measured, not estimated: each profile's resident set is recorded from `docker stats` and re-measured whenever a milestone lands, and the figures are published in the docs. Every container has resource limits. The README states the minimum hardware. GitHub Actions runs only G0's unit tests and the M0 end-to-end test, both sized for hosted runners.

## 8. Risks and trade-offs

| Risk | Mitigation |
|---|---|
| Simulated AWS surface needed by Karpenter is larger than expected | Gate G0 blocks all other work; scope is fixed by tracing real Karpenter; a decision checkpoint (criteria 1 to 3 or 3 weeks) forces a continue-or-narrow decision |
| Scope: M8 and M9 add roughly a third more work | They are independent of M0 to M7; releases stay shippable |
| Spinnaker memory footprint | Spike S1 with a 2 GiB budget and a custom `deploy-orchestrator` fallback (section 4a) |
| Claiming Spinnaker experience without running Spinnaker | Honesty rule in section 4a; the README states the S1 outcome |
| GitLab and CircleCI cannot execute on GitHub | CI covers lint and validation; full runs are local, with the recorded demo as proof |
| "1:1 parity" overclaims | Each facade documents the supported operation subset and the conformance report is published |

## 9. Out of scope

Real AWS deployment, a from-scratch Kubernetes API server, services beyond those listed in section 3.1.

## Stress Test Results: Meridian platform design

### Resolved Decisions
- **Karpenter on simulated AWS (Branch 1):** a hard gate, G0, blocks all other work. All of Karpenter's AWS dependencies (EC2 fleet and launch templates, pricing, SSM, IAM instance profiles, SQS interruption queue, `eks:DescribeCluster`, STS) get a simulated surface plus a custom `node-registrar`. Eight acceptance criteria, plus a decision checkpoint (criteria 1 to 3 pass, or 3 weeks) where the user decides whether to continue or narrow scope.
- **Terraform vs facades (Branch 2):** supported-resource matrix, idempotency contract tests (second plan shows no changes), pinned provider, sim config isolated from modules and linted in CI.
- **EKS facade (Branch 3):** custom, owns its own state, not proxied to Moto's EKS.
- **Istio multi-cluster (Branch 4):** per-region CIDRs fixed in `eks-facade` from M0; multi-network multi-primary; 2-day probe at the start of M4; ingress-layer failover as written fallback.
- **Resource budget (Branch 5):** `core`, `full` and `ci-matrix` profiles with measured memory budgets.
- **Security (Branch 6):** threat-model ADR, socket proxy plus rootless guidance, generated credentials, localhost-only binding, pinning and Trivy from M0.
- **Parity claims (Branch 7):** the public "1:1 parity" claim is scoped to the supported operations and backed by a generated conformance report with three fidelity labels. The project states it is not validated against real AWS and does not claim real AWS is unnecessary to test.
- **Build effort (Branch 8):** model-driven generation from AWS's published Smithy models, one operation list as single source of truth, 3-day-boxed generator check in G0.
- **Version drift (Branch 9):** `versions.yaml`, upgrade PRs gated by G0 and contract tests, golden Karpenter call-surface diff.
- **Failure handling (Branch 10):** no partial tags, release lines v0.1 to v3.0, README status table, no silent cuts, ADRs for failures.
- **Node realism (Branch 11, added by reflexion):** `node-registrar` has `real` and `kwok` backends; nodegroups are real, Karpenter defaults to KWOK, with taints and an opt-in flag.

### Changes Made
- Added gate G0 and the new components `node-registrar` and `pricing-facade`; redefined M2 as Karpenter integration on the platform.
- Added the Terraform contract, API generation, version pinning, conformance report, node backends and security details to section 6; added section 7a (run profiles) and section 4b (release lines).
- Reworded the parity claim and the real-AWS statement.

### Deferred / Parking Lot
- Observability of the simulators themselves (metrics and logs from the facades) is not specified; to be decided during M5.
- The exact Karpenter AWS call list is unknown until G0's trace.
- Memory budget figures (8 GiB `core`, 16 GiB `full`) are starting targets, not measurements.

### Confidence Assessment
- Overall: Medium. The design is coherent and the biggest risks now have gates and fallbacks, but G0 is unproven and could change the scope of the AWS-compatible plane.
- Areas of concern: size of Karpenter's real AWS call surface; Istio multi-cluster on one Docker host; total effort for a single author.