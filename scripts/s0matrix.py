#!/usr/bin/env python3
"""Turns S0 probe reports into a per-operation backend decision.

  s0matrix.py REPORT.json OPS.yaml [--backend NAME] [--apply]
      one report, for the backend NAME (default floci)
  s0matrix.py --report floci=docs/s0/report.json --report moto=docs/s0/report-moto.json OPS.yaml [--apply]
      several reports, one per candidate backend (the multi-report mode)

Without --apply the decision table is printed and nothing is written. With --apply the backend field of each
operation in OPS.yaml is rewritten (comments, blank lines, key order and line endings are left alone; running it
twice changes nothing).

Rules
  * A probe pass means the backend is eligible to serve the operation. It is not acceptance: Karpenter
    (criteria 1 to 3, run by hand before the approval stop) is the oracle and can move an operation the probe passed.
  * An operation passes on a backend only if every check for it passed in that backend's report. A skipped check, a
    failed check, or an operation the report does not mention counts as a failure.
  * Operations that share instance state (ops.Coupled in internal/ops/ops.go) are never split. The whole group goes
    to the first backend, in the order floci, moto, ec2-sim, whose report passed ALL its coupled checks; if none did
    it goes to ec2-sim, which needs no report.
  * Every other operation goes to the first backend (floci, then moto) that passed it, else to the fallback for its
    service: ec2-sim for EC2 reference data, pricing-facade, eks-facade, and moto for iam, sts, sqs and ssm.
"""
import argparse
import json
import os
import re
import sys
import tempfile

# Candidate backends in order of trial. ec2-sim is not listed: it is the unconditional last resort for EC2.
ORDER = ["floci", "moto"]

# The custom backend that replaces Floci for each service, and the plan task that builds it.
FALLBACK = {
    "ec2": "ec2-sim",
    "pricing": "pricing-facade",
    "eks": "eks-facade",
    "iam": "moto",
    "sts": "moto",
    "sqs": "moto",
    "ssm": "moto",
}
TASKS = {"ec2-sim": "Tasks 11 and 12", "pricing-facade": "Task 13", "eks-facade": "Task 13", "moto": "Task 14"}

# Keep in step with ops.Coupled in internal/ops/ops.go; test_s0matrix.py compares the two.
COUPLED = [
    "ec2:CreateFleet", "ec2:RunInstances", "ec2:TerminateInstances", "ec2:DescribeInstances", "ec2:DescribeInstanceStatus", "ec2:CreateTags",
    "ec2:CreateLaunchTemplate", "ec2:DescribeLaunchTemplates", "ec2:DeleteLaunchTemplate",
    "ec2:DescribeImages", "ec2:DescribeSubnets", "ec2:DescribeSecurityGroups",
]

# One operation line of ops.yaml: group 1 is everything up to the backend name, group 2/3 service and name,
# group 4 the backend, group 5 the rest of the line (a comma or the closing brace, then any trailing comment).
OP_LINE = re.compile(r"^(\s*- \{service: (\w+), name: (\w+),(?:.*?[\s,])?backend: )([a-z][a-z0-9-]*)([,}].*)$")
BACKEND_NAME = re.compile(r"^[a-z][a-z0-9-]*$")


def operation_status(results):
    """Maps 'service:Op' to 'pass' or 'fail'. A check's name starts with the operation ("ec2:DescribeSubnets (discovery tag)")."""
    status = {}
    for r in results:
        if not (isinstance(r, dict) and isinstance(r.get("name"), str) and r["name"].split() and isinstance(r.get("status"), str)):
            raise ValueError(f"malformed result entry {r!r}: it needs a name and a status")
        op = r["name"].split()[0]
        bad = r["status"] != "pass"  # skip, fail and anything unknown all count as not passing
        status[op] = "fail" if bad or status.get(op) == "fail" else "pass"
    return status


def coupled_choice(statuses):
    """Picks the backend for the whole coupled EC2 group.

    statuses maps backend -> {op: status}. Returns (backend, failures) where failures maps each backend tried
    and rejected to the coupled operations that did not pass on it (a missing operation counts as not passing).
    """
    failures = {}
    for backend in ORDER:
        if backend not in statuses:
            continue
        bad = [c for c in COUPLED if statuses[backend].get(c) != "pass"]
        if not bad:
            return backend, failures
        failures[backend] = bad
    return "ec2-sim", failures


def describe_failures(failures):
    return "; ".join(f"{b}: {', '.join(ops)}" for b, ops in failures.items())


def decide(reports, ops):
    """Returns {op: (backend, reason)} for every operation in ops (a list of (service, name, current_backend)).

    reports maps a backend name (floci or moto) to the results list of its probe report.
    """
    unknown = sorted(set(reports) - set(ORDER))
    if unknown:
        raise ValueError(f"no report is accepted for backend {unknown[0]!r}; the candidates are {', '.join(ORDER)}")
    if not reports:
        raise ValueError("at least one probe report is required")
    for svc, name, _ in ops:
        if svc not in FALLBACK:
            raise ValueError(f"{svc}:{name}: no fallback backend is known for service {svc!r}")
    statuses = {b: operation_status(reports[b]) for b in ORDER if b in reports}
    first = next(iter(statuses))
    group, failures = coupled_choice(statuses)

    out = {}
    for svc, name, _cur in ops:
        key = f"{svc}:{name}"
        if key in COUPLED:
            # One decision for the whole group, so it can never be split.
            if group == "ec2-sim":
                why = "probe failed" if key in failures.get(first, []) else "coupled to a failing operation"
                out[key] = (group, f"{why} ({describe_failures(failures)}); no probed backend passed all coupled checks")
            elif failures:
                out[key] = (group, f"probe passed on {group} (all coupled checks); {describe_failures(failures)}")
            else:
                out[key] = (group, f"probe passed on {group} (all coupled checks); eligible to stay")
            continue
        for backend in ORDER:
            if backend in statuses and statuses[backend].get(key) == "pass":
                if backend == first:
                    out[key] = (backend, f"probe passed on {backend}; eligible to stay")
                else:
                    seen = "failed" if key in statuses[first] else "has no result"
                    out[key] = (backend, f"{first} {seen}; probe passed on {backend}")
                break
        else:
            seen = {b: ("failed" if key in s else "has no result") for b, s in statuses.items()}
            detail = ", ".join(f"{b} {v}" for b, v in seen.items())
            out[key] = (FALLBACK[svc], f"no probed backend passed it ({detail})")
    return out


def read_ops(path):
    """Returns [(service, name, backend)] for each operation line of ops.yaml; refuses lines it cannot read."""
    ops = []
    with open(path, newline="") as f:
        for n, line in enumerate(f.read().split("\n"), 1):
            m = OP_LINE.match(line)
            if m:
                ops.append((m.group(2), m.group(3), m.group(4)))
            elif "service:" in line.split("#", 1)[0]:
                # Fail closed: a line that looks like an operation but does not parse would silently keep its old backend.
                raise ValueError(f"{path}:{n}: cannot read this operation line: {line.strip()}")
    return ops


def apply(path, decision):
    """Rewrites only the backend name of each operation line, then replaces the file atomically (and only if it changed)."""
    path = os.path.realpath(path)  # write through a symlink: replace the target, keep the link
    with open(path, newline="") as f:
        text = f.read()
    lines = text.split("\n")
    for i, line in enumerate(lines):
        m = OP_LINE.match(line)
        if m:
            key = f"{m.group(2)}:{m.group(3)}"
            if key not in decision:
                raise ValueError(f"{path}:{i + 1}: no decision for {key}")
            backend = decision[key][0]
            if not BACKEND_NAME.match(backend):
                raise ValueError(f"{key}: {backend!r} is not a valid backend name")
            lines[i] = m.group(1) + backend + m.group(5)
        elif "service:" in line.split("#", 1)[0]:
            raise ValueError(f"{path}:{i + 1}: cannot read this operation line: {line.strip()}")
    new = "\n".join(lines)
    if new == text:
        return False  # already applied: leave the file (and its timestamp) alone
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(os.path.abspath(path)), prefix=".ops.", suffix=".tmp")
    try:
        with os.fdopen(fd, "w", newline="") as f:
            f.write(new)
        os.chmod(tmp, os.stat(path).st_mode & 0o777)
        os.replace(tmp, path)
    except BaseException:
        os.unlink(tmp)
        raise
    return True


def table(decision):
    rows = ["| Operation | Backend | Why |", "|---|---|---|"]
    for key in sorted(decision):
        backend, why = decision[key]
        rows.append(f"| {key} | {backend} | {why} |")
    tasks = sorted({TASKS[b] for b, _ in decision.values() if b in TASKS})
    rows.append("")
    rows.append("Tasks to execute: " + (", ".join(tasks) if tasks else "none (Floci serves every probed operation)"))
    rows.append("")
    rows.append("A probe pass means eligible, not accepted. Run Karpenter criteria 1 to 3 against this table before approving it; "
                "Karpenter is the oracle and can move an operation the probe passed to the next backend.")
    return "\n".join(rows)


def load_report(path):
    try:
        with open(path) as f:
            report = json.load(f)
    except (OSError, ValueError, RecursionError) as e:  # JSONDecodeError is a ValueError; a deeply nested file is a RecursionError
        raise ValueError(f"cannot read report {path}: {e}")
    if not isinstance(report, dict) or not isinstance(report.get("results"), list):
        raise ValueError(f"{path}: not a probe report (no results list)")
    return report["results"]


def main(argv):
    p = argparse.ArgumentParser(prog="s0matrix.py", description="Per-operation backend decision from S0 probe reports.", allow_abbrev=False)
    p.add_argument("files", nargs="*", help="REPORT.json OPS.yaml, or just OPS.yaml with --report")
    p.add_argument("--report", action="append", default=[], metavar="NAME=PATH", help="a probe report for backend NAME (floci or moto); repeat for each")
    p.add_argument("--backend", default=None, help="backend the single REPORT.json belongs to (default floci)")
    p.add_argument("--apply", action="store_true", help="rewrite the backend fields of OPS.yaml")
    try:
        args = p.parse_args(argv[1:])
    except SystemExit as e:
        return e.code if isinstance(e.code, int) else 2

    def fail(msg):
        print(f"s0matrix: {msg}", file=sys.stderr)
        return 2

    try:
        if args.report:
            if args.backend is not None or len(args.files) != 1:
                return fail("with --report give only OPS.yaml (and no --backend)")
            paths, ops_path = {}, args.files[0]
            for spec in args.report:
                name, sep, path = spec.partition("=")
                if not sep or not name or not path:
                    return fail(f"--report needs NAME=PATH, got {spec!r}")
                if name in paths:
                    return fail(f"--report {name} given twice")
                paths[name] = path
        else:
            if len(args.files) != 2:
                print(__doc__)
                return fail("expected REPORT.json OPS.yaml, or --report NAME=PATH ... OPS.yaml")
            paths, ops_path = {args.backend or "floci": args.files[0]}, args.files[1]
        bad = sorted(set(paths) - set(ORDER))
        if bad:
            return fail(f"no report is accepted for backend {bad[0]!r}; the candidates are {', '.join(ORDER)}")
        reports = {name: load_report(path) for name, path in paths.items()}
        decision = decide(reports, read_ops(ops_path))
        print(table(decision))
        if args.apply:
            changed = apply(ops_path, decision)
            print(f"\n{ops_path}: {'backend fields rewritten' if changed else 'already up to date'}")
    except (ValueError, OSError, RecursionError) as e:
        return fail(str(e))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
