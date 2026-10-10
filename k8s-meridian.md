# Meridian G0 (Floci-first): Handoff for Claude Code in VS Code

Date: 2026-10-09
Owner: Terence (github.com/tazzledazzle)
Status: ready to start at Task 0. The detailed plan is verified as written; the nine amendments in section 4 are decided but not yet applied to its task text.

## 1. What you are building

`meridian` is a GitHub showcase repo for a Kubernetes/AWS Platform Engineer role. It runs with no real AWS. This handoff covers only **gate G0**: prove that real, unmodified Karpenter runs against a simulated AWS plane on a local k3d cluster with no route to the internet, and passes eight criteria from a clean checkout with `make gate-karpenter`.

The simulated AWS plane is a mix of backends behind one `aws-gateway`:
- **Floci** (open-source AWS emulator, MIT, image `floci/floci`, port 4566) for every operation where it passes a contract probe.
- **Custom Go services** (`ec2-sim`, `pricing-facade`, `eks-facade` stub) or **Moto** only for operations Floci fails.
- Always custom: `simctl`/`spot` (spot interruption injector using public AWS calls) and `node-registrar` (turns running instances into KWOK-backed Nodes).

Public wording is always: **"Not validated against real AWS."** Never write a "1:1 parity" claim until the conformance report exists.

## 2. Files to read, in order

Copy these three files into the repo under `.internal/` (keep the same relative paths):

| File | What it is |
|---|---|
| `.internal/specs/2026-10-09-meridian-platform-design.md` | The design spec (amended for Floci and S0). Source of requirements. |
| `.internal/plans/2026-10-09-meridian-g0-floci-spike.md` | The detailed plan: 17 tasks, about 12,000 lines, every step with exact code, commands and expected output. Follow it step by step. |
| `.internal/plans/2026-10-09-meridian-g0-handoff.md` | This file. Wins over the plan wherever they differ (sections 4 and 5). |

Optional reference: `2026-10-09-meridian-g0-karpenter-gate.md` is the older all-custom plan. Do not execute it.

Read in the plan only: the header, "Scope decision", "Global Constraints", "File Structure", and the task you are on. Do not read the whole file at once.

## 3. Prerequisites on your machine

- Docker (rootless Docker or Podman preferred), Docker Compose v2.
- Go 1.24, Python 3.10+, Bash, `git`, `make`, `jq`, `curl`.
- About 8 GiB of free RAM for the gate stack (the real minimum is measured in S0 and recorded in the README).
- Network access for the first run only, to pull pinned images and charts. The gate itself runs on an internal Docker network with no route out.
- Task 1 resolves actual digests and versions into `versions.yaml`. Placeholders such as `<resolved>` or `<x.y.z>` in the plan are lookup values, not omissions: look them up against the real registry and record them.

## 4. Amendments to apply before the tasks they affect

These came from the stress test. They are decided. The plan's Scope section (items 3, 3a to 3h) already states them, but the task text and code below do not yet implement them. Apply each one in the task named, write the test first, and note what you changed in the commit message.

| # | Applies to | Change |
|---|---|---|
| A0 | New Task 0, before Task 1 | Half-day smoke test, no gateway and no SigV4: start Floci, send `DescribeInstances` and `CreateFleet` with the AWS CLI or `awscall`, then start the pinned Karpenter image with a plain endpoint override pointing at Floci. Record the result in `docs/s0/smoke.md`. **Stop and tell the user** if Floci cannot start or Karpenter cannot start against it. |
| A1 | Task 7 (harness), Task 17 (ADR-0002) | Docker socket. First try Floci with EC2 container execution turned off (check Floci's configuration reference for the setting). If that passes the probe, mount no socket. If Floci needs the socket, put a filtering socket proxy in front (for example `tecnativa/docker-socket-proxy`) that denies exec, host binds, privileged containers and image builds. Add a gate assertion that fails if any service mounts `/var/run/docker.sock` directly. |
| A2 | Task 8 (probe) | Probe `CreateFleet` first. If Floci fails it, stop the rest of the EC2 matrix and go straight to the EC2 fallback. Say in the report that EC2 is the swing factor. |
| A3 | Tasks 8, 14 | Moto is a second EC2 candidate. Order of trial for the coupled EC2 group: Floci, then Moto, then `ec2-sim`. Extend `scripts/s0matrix.py` and `profiles.py` so `moto` is accepted as the backend for EC2 operations, and make Task 14's trigger cover that case. |
| A4 | Task 8 | A probe pass means "eligible", not "proven". Reword the report. Derive the fields the probe checks from the `-record` trace of real Karpenter calls. Before the approval stop, run criteria 1 to 3 against the proposed table; if Karpenter fails on an operation the probe passed, move that operation to the next backend. |
| A5 | Task 16 (criterion 7) | Assert `docker network inspect meridian-gate` shows `Internal: true`, assert every gate container is attached only to that network, and run the unreachable public endpoint check from the Karpenter pod, the Floci container and the tools container. |
| A6 | Tasks 9, 16 (criterion 4) | Build the `simctl` interruption message from a golden sample taken from Karpenter's own fixtures for the pinned version, and unit-test it against the fields Karpenter's parser reads. Criterion 4 must assert from Karpenter's logs that the interruption handler received the message and cordoned and drained the node before the instance was terminated. A node removed only by garbage collection fails. |
| A7 | Task 17, CI | CI fails if the Floci pin in `versions.yaml` changes without a refreshed `docs/s0/` report that records the same version. README records the Floci digest and a pinned-commit build-from-source fallback. ADR-0002 states that any Floci-served operation can move to another backend by changing one `backend` line in `ops.yaml`. |
| A8 | Tasks 11 to 14 | Expand only when triggered. They are conditional, and their code in the plan is a verified sketch. Before building one, re-read the S0 report and adjust the task to what actually failed. |
| A9 | Tasks 15, 16 | Cap Karpenter NodePool `limits` (about 10 nodes). The gate script checks free memory and CPU before starting and fails fast with a clear message. S0 records peak stack memory, and Task 17 writes that figure into the README as the minimum hardware. |

## 5. Working rules

1. **One task at a time, in order.** Within a task, follow the steps as written: failing test, run it and see it fail, implement, run it and see it pass, commit. Do not skip the "see it fail" step.
2. **Verify before claiming.** A step is done only when its stated command produced its stated output. Paste the real output in your report. Never mark a task complete with a failing or unrun test.
3. **Stop points are real.** Stop and ask the user at: the end of Task 0 if it fails, the end of Task 8 (approve the operation-to-backend table in `docs/s0/decision.md`), Task 15 Step 8 if k3d or Floci cannot run on an internal Docker network, the G0 decision checkpoint in Task 16 (after criteria 1 to 3 pass, or after 3 weeks), and any time the plan says "tell the user". Do not continue past a stop point on your own judgment.
4. **Do not weaken a security control to make something pass.** No default or committed keys, no auth-bypass flags, no raw Docker socket mounts, no published ports for the gate. If a control blocks you, stop and report.
5. **No silent cuts.** If a requirement cannot be met, say so and record it; do not drop it quietly. Narrowing scope needs a decision from the user, recorded in an ADR.
6. **Comments.** Add short descriptive comments on the important lines of code you write. The user is learning to write these himself.
7. **Language.** The project is Go for services, with Python and Bash only for tooling, as the spec requires. The user also works in Kotlin, Python and Java, but do not port anything.
8. **Evidence over assumption.** The open risks below are assumptions until measured. When one is measured, write the result into the S0 report or an ADR.
9. **Commits.** Commit after each task step the plan marks. Use clear messages and keep each commit small. There is no bead tracker in this repo.

## 6. Task order and what each delivers

| Task | Delivers | Needs |
|---|---|---|
| 0 | Smoke test result (A0) | Docker |
| 1 | Module, `versions.yaml` with real digests, `make init` credentials | none |
| 2 | `ops.yaml` with per-operation backend, `Validate`, `Find`, `Coupled` | 1 |
| 3 | AWS wire helpers: errors, query parameters, JSON-RPC | 1 |
| 4 | SigV4 verification | 3 |
| 5 | `aws-gateway` per-operation routing, trace and record | 2, 3, 4 |
| 6 | Trace recorder, `awscall` tool, gateway binary | 5 |
| 7 | S0 harness: Floci behind gateway, k3d, Karpenter, seeding (A1) | 6 |
| 8 | `s0probe`, report, decision script, **approval stop** (A2, A3, A4) | 7 |
| 9 | `spot`, `simctl`, `fakeec2`, `catalog` (A6) | 6 |
| 10 | `node-registrar` | 9 |
| 11 to 14 | Conditional fallbacks: EC2 core and wire, pricing and EKS stubs, Moto (A8) | 8 says which |
| 15 | Workloads, KWOK, isolated gate stack (A9) | 10, plus fallbacks if any |
| 16 | Gate test C1 to C8, **decision checkpoint** (A5, A6, A9) | 15 |
| 17 | ADR-0002/0003, ADR-0001 status amendment, README, CI (A7) | 16 |

Tasks 9 and 10 can run while Task 8 waits for the user's approval, because they do not depend on which backend serves EC2.

## 7. Open risks (unmeasured until the named task runs)

- Floci's fidelity for `CreateFleet`, `DescribeInstances`, pricing and `DescribeCluster` (Task 0, Task 8).
- Whether Floci runs with EC2 container execution off (Task 7).
- Moto's `CreateFleet` support: spot strategy, overrides, capacity errors (Task 8).
- Where Floci attaches EC2 instance containers if they do run; the label in `deploy/floci.label` (Task 7).
- k3d and Floci working on an internal Docker network (Task 15 Step 8).
- k3s system image names for offline preloading, `deploy/images.system.txt` (Task 15).
- Floci is young; behavior can change between versions (A7).

## 8. Definition of done for G0

- `make gate-karpenter` passes all eight criteria from a clean checkout, twice in a row (criterion 8), on a fresh CI runner.
- `docs/s0/report.md` and `docs/s0/decision.md` exist, record the pinned Floci version, and the user approved the table.
- `trace/golden/karpenter-calls.golden` is committed and reviewed.
- `docs/checkpoints/g0.md` records the continue-or-narrow decision.
- ADR-0002 and ADR-0003 are written; ADR-0001 status is amended; `docs/decisions/INDEX.md` is updated.
- README says "Not validated against real AWS" and makes no parity claim.
- Tag `v0.1` only after all of the above. No partial tags.

## 9. Prompt to paste into Claude Code

```
Read .internal/plans/2026-10-09-meridian-g0-handoff.md first, then the header, Scope, Global Constraints and File Structure sections of .internal/plans/2026-10-09-meridian-g0-floci-spike.md. Follow the handoff's working rules. Start with Task 0 (section 4, A0), then Task 1. Execute one task at a time, test first, and paste real command output when you report. Apply amendments A1 to A9 from the handoff before the tasks they name. Stop at every stop point listed in section 5 rule 3 and ask me before continuing.
```