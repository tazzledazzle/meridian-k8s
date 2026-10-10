"""Adversarial and property-style tests for scripts/s0matrix.py, written by an independent tester.

A test named test_bug_* documents a defect present in the code as tested.
"""
import contextlib
import io
import json
import os
import random
import re
import shutil
import tempfile
import unittest
from unittest import mock

import s0matrix as m

HERE = os.path.dirname(os.path.abspath(__file__))
REAL_OPS = os.path.join(HERE, "..", "ops.yaml")
GO_OPS = os.path.join(HERE, "..", "internal", "ops", "ops.go")

OPS = m.read_ops(REAL_OPS)
KEYS = [f"{s}:{n}" for s, n, _ in OPS]
FALLBACK = {"ec2": "ec2-sim", "pricing": "pricing-facade", "eks": "eks-facade", "iam": "moto", "sts": "moto", "sqs": "moto", "ssm": "moto"}


# ---------------------------------------------------------------------------------------------------------
# an independent re-implementation of the Go rules (internal/ops/ops.go), reading the Coupled list from the Go source

def go_coupled():
    with open(GO_OPS) as f:
        src = f.read()
    block = re.search(r"var Coupled = \[\]string\{(.*?)\n\}", src, re.S)
    assert block, "cannot find ops.Coupled in ops.go"
    return re.findall(r'"([a-z0-9]+:[A-Za-z0-9]+)"', block.group(1))


GO_COUPLED = go_coupled()
GO_BACKEND_RE = re.compile(r"^[a-z][a-z0-9-]*$")


def go_validate(text):
    """Returns a list of problems with the same meaning as ops.List.Validate (duplicates, backend name, split coupled group)."""
    ops = []
    for line in text.replace("\r\n", "\n").split("\n"):
        code = line.split("#", 1)[0]
        if "service:" not in code:
            continue
        svc = re.search(r"service:\s*([A-Za-z0-9]+)", code).group(1)
        name = re.search(r"\bname:\s*([A-Za-z0-9]+)", code).group(1)
        be = re.search(r"\bbackend:\s*([^,}\s]+)", code).group(1)
        ops.append((f"{svc}:{name}", be))
    problems, seen = [], set()
    for key, be in ops:
        if key in seen:
            problems.append(f"{key}: duplicate")
        seen.add(key)
        if not GO_BACKEND_RE.match(be):
            problems.append(f"{key}: bad backend {be!r}")
    first = None
    for key in GO_COUPLED:
        for k, be in ops:
            if k == key:
                if first is None:
                    first = (key, be)
                elif be != first[1]:
                    problems.append(f"{key} on {be} but {first[0]} on {first[1]}")
                break
    return problems


# ---------------------------------------------------------------------------------------------------------
# helpers

def res(name, status):
    return {"name": name, "group": name.split(":")[0], "status": status, "detail": ""}


def all_pass(**overrides):
    wanted = {k.replace("_", ":", 1): v for k, v in overrides.items()}
    out = []
    for key in KEYS:
        status = wanted.pop(key, "pass")
        if status is not None:
            out.append(res(key, status))
    assert not wanted, wanted
    return out


def oracle(reports, keys):
    """Independent statement of the rules in the s0matrix docstring."""
    st = {}
    for b, results in reports.items():
        d = {}
        for r in results:
            d.setdefault(r["name"].split()[0], []).append(r["status"])
        st[b] = d

    def passes(b, op):
        return b in st and op in st[b] and all(s == "pass" for s in st[b][op])

    group = "ec2-sim"
    for b in ("floci", "moto"):
        if b in st and all(passes(b, c) for c in GO_COUPLED):
            group = b
            break
    out = {}
    for key in keys:
        if key in GO_COUPLED:
            out[key] = group
            continue
        for b in ("floci", "moto"):
            if passes(b, key):
                out[key] = b
                break
        else:
            out[key] = FALLBACK[key.split(":")[0]]
    return out


def random_report(rng, keys, junk=True):
    statuses = ["pass"] * 8 + ["fail", "skip", "PASS", "Pass", "", "weird", " pass"]
    out = []
    for key in keys:
        roll = rng.random()
        if roll < 0.08:
            continue  # missing
        for _ in range(2 if roll > 0.9 else 1):
            suffix = rng.choice(["", "", " (discovery tag)", " (x, y)", "  extra"])
            out.append(res(key + suffix, rng.choice(statuses)))
    if junk:
        for extra in ("ec2:Bogus", "zzz:Thing", "ec2:CreateFleetX"):
            if rng.random() < 0.5:
                out.append(res(extra, rng.choice(statuses)))
    rng.shuffle(out)
    return out


class TempOps(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir, True)

    def path(self, name="ops.yaml"):
        return os.path.join(self.dir, name)

    def write(self, text, name="ops.yaml", binary=False):
        p = self.path(name)
        with open(p, "wb" if binary else "w", **({} if binary else {"newline": ""})) as f:
            f.write(text)
        return p

    def read(self, name="ops.yaml"):
        with open(self.path(name), "rb") as f:
            return f.read()

    def report_file(self, results, name="report.json"):
        return self.write(json.dumps({"results": results}), name)

    def cli(self, *argv):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = m.main(["s0matrix.py", *argv])
        return code, out.getvalue(), err.getvalue()


# ---------------------------------------------------------------------------------------------------------
class ContractWithGoTests(unittest.TestCase):
    def test_python_coupled_list_equals_go_coupled_list(self):
        self.assertEqual(sorted(m.COUPLED), sorted(GO_COUPLED))
        self.assertEqual(len(set(GO_COUPLED)), len(GO_COUPLED))

    def test_real_ops_yaml_has_the_expected_shape(self):
        self.assertEqual(len(OPS), 36)
        self.assertIn("ec2:DescribeInstanceStatus", KEYS)
        self.assertEqual(len(set(KEYS)), len(KEYS))
        for c in GO_COUPLED:
            self.assertIn(c, KEYS)
        with open(REAL_OPS) as f:
            self.assertEqual(go_validate(f.read()), [])

    def test_fallback_table_matches_the_documented_one(self):
        self.assertEqual(m.FALLBACK, FALLBACK)
        self.assertEqual(m.ORDER, ["floci", "moto"])
        for svc in {k.split(":")[0] for k in KEYS}:
            self.assertIn(svc, m.FALLBACK)


class PropertyTests(unittest.TestCase):
    def test_random_reports_agree_with_the_independent_oracle_and_never_split_the_group(self):
        rng = random.Random(20261010)
        for i in range(600):
            reports = {}
            which = rng.choice([("floci",), ("moto",), ("floci", "moto"), ("moto", "floci")])
            for b in which:
                reports[b] = random_report(rng, KEYS)
            if rng.random() < 0.3 and "floci" in reports:
                for c in rng.sample(GO_COUPLED, rng.randint(0, 3)):  # make "almost all pass" common
                    reports["floci"] = [r for r in reports["floci"] if r["name"].split()[0] != c] + [res(c, rng.choice(["pass", "fail", "skip"]))]
            d = m.decide(reports, OPS)
            want = oracle(reports, KEYS)
            self.assertEqual({k: v[0] for k, v in d.items()}, want, f"iteration {i} order {which}")
            self.assertEqual(len({d[c][0] for c in GO_COUPLED}), 1, f"coupled group split in iteration {i}")
            for k, (backend, why) in d.items():
                self.assertTrue(m.BACKEND_NAME.match(backend))
                self.assertTrue(why)

    def test_applying_any_decision_to_the_real_ops_yaml_satisfies_the_go_rules_and_changes_only_backend_tokens(self):
        rng = random.Random(7)
        with open(REAL_OPS, newline="") as f:
            original = f.read()
        for i in range(120):
            reports = {b: random_report(rng, KEYS) for b in rng.choice([("floci",), ("floci", "moto"), ("moto",)])}
            d = m.decide(reports, OPS)
            d_dir = tempfile.mkdtemp()
            try:
                p = os.path.join(d_dir, "ops.yaml")
                with open(p, "w", newline="") as f:
                    f.write(original)
                m.apply(p, d)
                with open(p, newline="") as f:
                    new = f.read()
                self.assertEqual(go_validate(new), [], f"iteration {i}")
                a, b = original.split("\n"), new.split("\n")
                self.assertEqual(len(a), len(b))
                for x, y in zip(a, b):
                    if x != y:
                        self.assertEqual(re.sub(r"backend: [a-z0-9-]+", "backend: X", x), re.sub(r"backend: [a-z0-9-]+", "backend: X", y))
                self.assertEqual([(s, n, d[f"{s}:{n}"][0]) for s, n, _ in OPS], m.read_ops(p))
                # idempotent
                before = os.stat(p).st_mtime_ns
                self.assertFalse(m.apply(p, d))
                with open(p, newline="") as f:
                    self.assertEqual(f.read(), new)
                self.assertEqual(os.stat(p).st_mtime_ns, before)
            finally:
                shutil.rmtree(d_dir, True)

    def test_any_non_pass_status_of_any_coupled_check_keeps_floci_out_of_the_group(self):
        for bad in ("fail", "skip", "FAIL", "", "unknown", None):
            for c in GO_COUPLED:
                floci = all_pass(**{c.replace(":", "_", 1): bad})
                d = m.decide({"floci": floci}, OPS)
                self.assertEqual({d[k][0] for k in GO_COUPLED}, {"ec2-sim"}, f"{c} = {bad!r}")
                # the rest stays on floci
                self.assertEqual(d["ec2:DescribeSpotPriceHistory"][0], "floci")
                self.assertEqual(d["pricing:GetProducts"][0], "floci")

    def test_one_failing_duplicate_poisons_the_operation(self):
        floci = all_pass() + [res("ec2:DescribeSpotPriceHistory (second)", "fail")]
        self.assertEqual(m.decide({"floci": floci}, OPS)["ec2:DescribeSpotPriceHistory"][0], "ec2-sim")
        floci = [res("ec2:DescribeSpotPriceHistory", "fail")] + all_pass()
        self.assertEqual(m.decide({"floci": floci}, OPS)["ec2:DescribeSpotPriceHistory"][0], "ec2-sim")

    def test_trial_order_is_floci_then_moto_then_ec2_sim_whatever_order_the_reports_are_given(self):
        floci_bad = all_pass(ec2_CreateFleet="fail")
        for reports, want in (
            ({"floci": all_pass(), "moto": all_pass()}, "floci"),
            ({"moto": all_pass(), "floci": all_pass()}, "floci"),
            ({"moto": all_pass(), "floci": floci_bad}, "moto"),
            ({"floci": floci_bad, "moto": all_pass(ec2_DescribeImages="skip")}, "ec2-sim"),
            ({"moto": all_pass()}, "moto"),
            ({"floci": floci_bad}, "ec2-sim"),
        ):
            d = m.decide(reports, OPS)
            self.assertEqual({d[k][0] for k in GO_COUPLED}, {want}, list(reports))

    def test_non_coupled_operations_choose_per_operation_and_per_service_fallback(self):
        floci = all_pass(pricing_GetProducts="fail", eks_DescribeCluster="skip", ec2_DescribeInstanceTypes="fail", iam_GetRole="fail",
                         sqs_SendMessage="fail", ssm_GetParameter="fail", sts_GetCallerIdentity="fail", eks_CreateCluster=None)
        d = m.decide({"floci": floci}, OPS)
        self.assertEqual(d["pricing:GetProducts"][0], "pricing-facade")
        self.assertEqual(d["eks:DescribeCluster"][0], "eks-facade")
        self.assertEqual(d["eks:CreateCluster"][0], "eks-facade")
        self.assertEqual(d["ec2:DescribeInstanceTypes"][0], "ec2-sim")
        for k in ("iam:GetRole", "sqs:SendMessage", "ssm:GetParameter", "sts:GetCallerIdentity"):
            self.assertEqual(d[k][0], "moto", k)
        # moto's own pass wins over the fallback for the same answer, and the pricing/eks facades are never reported by moto
        moto = all_pass()
        d = m.decide({"floci": floci, "moto": moto}, OPS)
        self.assertEqual(d["iam:GetRole"][0], "moto")
        self.assertEqual(d["pricing:GetProducts"][0], "moto")  # moto passed it; the report says eligible
        self.assertIn("Task", m.table(d))

    def test_unknown_report_names_and_empty_input_are_refused(self):
        with self.assertRaises(ValueError):
            m.decide({"ec2-sim": all_pass()}, OPS)
        with self.assertRaises(ValueError):
            m.decide({}, OPS)
        with self.assertRaises(ValueError):
            m.decide({"floci": all_pass()}, [("kms", "Encrypt", "floci")])
        for bad in ([None], ["x"], [{"name": "a"}], [{"status": "pass"}], [{"name": "", "status": "pass"}], [{"name": "   ", "status": "pass"}],
                    [{"name": 5, "status": "pass"}], [{"name": "a:b", "status": 1}], [[]]):
            with self.assertRaises(ValueError, msg=repr(bad)):
                m.decide({"floci": bad}, OPS)

    def test_extra_operations_in_the_report_are_ignored(self):
        floci = all_pass() + [res("ec2:Brand", "fail"), res("kms:Encrypt", "fail")]
        d = m.decide({"floci": floci}, OPS)
        self.assertEqual({v[0] for v in d.values()}, {"floci"})
        self.assertEqual(set(d), set(KEYS))

    def test_reason_text_never_breaks_the_markdown_table(self):
        rng = random.Random(3)
        for _ in range(50):
            d = m.decide({"floci": random_report(rng, KEYS), "moto": random_report(rng, KEYS)}, OPS)
            for line in m.table(d).split("\n"):
                if line.startswith("| ") and not line.startswith("| Operation"):
                    self.assertEqual(line.count("|"), 4, line)


# ---------------------------------------------------------------------------------------------------------
class RealOpsYamlTests(TempOps):
    def copy_real(self):
        shutil.copy(REAL_OPS, self.path())
        return self.path()

    def test_all_pass_report_leaves_the_real_file_byte_identical(self):
        p = self.copy_real()
        before = self.read()
        mtime = os.stat(p).st_mtime_ns
        rp = self.report_file(all_pass())
        code, out, err = self.cli(rp, p, "--apply")
        self.assertEqual((code, err), (0, ""))
        self.assertIn("already up to date", out)
        self.assertEqual(self.read(), before)
        self.assertEqual(os.stat(p).st_mtime_ns, mtime)
        self.assertIn("none (Floci serves every probed operation)", out)

    def test_floci_createfleet_failure_moves_the_whole_group_together_and_nothing_else(self):
        p = self.copy_real()
        before = self.read().decode().split("\n")
        rp = self.report_file(all_pass(ec2_CreateFleet="fail"))
        code, out, err = self.cli(rp, p, "--apply")
        self.assertEqual((code, err), (0, ""))
        now = m.read_ops(p)
        moved = {f"{s}:{n}" for s, n, b in now if b != "floci"}
        self.assertEqual(moved, set(GO_COUPLED))
        self.assertEqual({b for s, n, b in now if f"{s}:{n}" in GO_COUPLED}, {"ec2-sim"})
        self.assertEqual(go_validate(self.read().decode()), [])
        after = self.read().decode().split("\n")
        self.assertEqual(sum(1 for a, b in zip(before, after) if a != b), len(GO_COUPLED))
        self.assertIn("Tasks 11 and 12", out)

    def test_createfleet_failure_with_a_passing_moto_report_sends_the_group_to_moto(self):
        p = self.copy_real()
        fr = self.report_file(all_pass(ec2_CreateFleet="fail"), "f.json")
        mr = self.report_file(all_pass(), "m.json")
        code, out, err = self.cli("--report", f"floci={fr}", "--report", f"moto={mr}", p, "--apply")
        self.assertEqual((code, err), (0, ""))
        self.assertEqual({b for s, n, b in m.read_ops(p) if f"{s}:{n}" in GO_COUPLED}, {"moto"})
        self.assertEqual(go_validate(self.read().decode()), [])
        # a second run is a no-op
        before = self.read()
        code, out, err = self.cli("--report", f"floci={fr}", "--report", f"moto={mr}", p, "--apply")
        self.assertIn("already up to date", out)
        self.assertEqual(self.read(), before)

    def test_floci_pass_after_a_previous_move_moves_the_group_back(self):
        p = self.copy_real()
        self.cli(self.report_file(all_pass(ec2_CreateFleet="fail")), p, "--apply")
        self.assertEqual({b for s, n, b in m.read_ops(p)} & {"ec2-sim"}, {"ec2-sim"})
        self.cli(self.report_file(all_pass()), p, "--apply")
        self.assertEqual({b for _, _, b in m.read_ops(p)}, {"floci"})
        with open(REAL_OPS, "rb") as f:
            self.assertEqual(self.read(), f.read())

    def test_pricing_and_eks_fall_back_to_their_facades(self):
        p = self.copy_real()
        self.cli(self.report_file(all_pass(pricing_GetProducts="fail", eks_DescribeCluster="fail")), p, "--apply")
        now = {f"{s}:{n}": b for s, n, b in m.read_ops(p)}
        self.assertEqual(now["pricing:GetProducts"], "pricing-facade")
        self.assertEqual(now["eks:DescribeCluster"], "eks-facade")
        self.assertEqual(now["eks:CreateCluster"], "floci")
        self.assertEqual(sum(1 for b in now.values() if b != "floci"), 2)

    def test_a_partial_report_fails_closed(self):
        p = self.copy_real()
        only_pricing = [res("pricing:GetProducts", "pass")]
        self.cli(self.report_file(only_pricing), p, "--apply")
        now = {f"{s}:{n}": b for s, n, b in m.read_ops(p)}
        self.assertEqual(now["pricing:GetProducts"], "floci")
        self.assertEqual({now[c] for c in GO_COUPLED}, {"ec2-sim"})
        self.assertEqual(go_validate(self.read().decode()), [])

    def test_without_apply_nothing_is_written(self):
        p = self.copy_real()
        before, mtime = self.read(), os.stat(p).st_mtime_ns
        code, out, _ = self.cli(self.report_file(all_pass(ec2_CreateFleet="fail")), p)
        self.assertEqual(code, 0)
        self.assertEqual(self.read(), before)
        self.assertEqual(os.stat(p).st_mtime_ns, mtime)
        self.assertEqual([f for f in os.listdir(self.dir) if f.startswith(".ops")], [])
        self.assertIn("| ec2:CreateFleet | ec2-sim |", out)


# ---------------------------------------------------------------------------------------------------------
class OpsFileDialectTests(TempOps):
    LINE = "  - {service: ec2, name: CreateFleet, protocol: query, backend: floci, fidelity: behavior-inferred}"

    def parse(self, text):
        return m.read_ops(self.write(text))

    def test_variants_either_parse_correctly_or_are_refused_never_misread(self):
        base = "ops:\n"
        variants = {
            "trailing comment": (self.LINE + "  # backend: moto, nope", ("ec2", "CreateFleet", "floci")),
            "backend is the last key": ("  - {service: ec2, name: CreateFleet, protocol: query, fidelity: x, backend: floci}", ("ec2", "CreateFleet", "floci")),
            "backend first": ("  - {service: ec2, name: CreateFleet, backend: floci, protocol: query}", ("ec2", "CreateFleet", "floci")),
            "deep indent": ("      - {service: ec2, name: CreateFleet, backend: moto}", ("ec2", "CreateFleet", "moto")),
            "tab indent": ("\t- {service: ec2, name: CreateFleet, backend: ec2-sim}", ("ec2", "CreateFleet", "ec2-sim")),
            "hyphenated backend": ("  - {service: ec2, name: CreateFleet, backend: pricing-facade}", ("ec2", "CreateFleet", "pricing-facade")),
            "two spaces after colon": ("  - {service:  ec2, name: CreateFleet, backend: floci}", None),
            "space before closing brace": ("  - {service: ec2, name: CreateFleet, backend: floci }", None),
            "quoted backend": ('  - {service: ec2, name: CreateFleet, backend: "floci"}', None),
            "swapped service and name": ("  - {name: CreateFleet, service: ec2, backend: floci}", None),
            "block style": ("  - service: ec2", None),
            "no backend key": ("  - {service: ec2, name: CreateFleet, protocol: query}", None),
            "uppercase backend": ("  - {service: ec2, name: CreateFleet, backend: Floci}", None),
        }
        for label, (line, want) in variants.items():
            for eol in ("\n", "\r\n"):
                for final in (True, False):
                    text = base.replace("\n", eol) + line + (eol if final else "")
                    try:
                        got = self.parse(text)
                    except ValueError:
                        got = None
                    if want is None:
                        self.assertIn(got, (None,), f"{label}: a line the script cannot read was accepted as {got}")
                    else:
                        self.assertEqual(got, [want], f"{label} {eol!r} final={final}")

    def test_comment_only_and_blank_lines_are_ignored(self):
        text = "# service: ec2, name: X\n\n   # - {service: ec2, name: Y, backend: floci}\nops:\n" + self.LINE + "\n"
        self.assertEqual(self.parse(text), [("ec2", "CreateFleet", "floci")])

    def test_empty_and_op_free_files_give_no_operations_and_apply_does_nothing(self):
        for text in ("", "\n", "ops:\n", "# nothing\n"):
            p = self.write(text)
            self.assertEqual(m.read_ops(p), [])
            self.assertFalse(m.apply(p, {}))
            self.assertEqual(self.read(), text.encode())

    def test_crlf_and_missing_final_newline_survive_apply_byte_for_byte(self):
        for eol in ("\n", "\r\n"):
            for final in (True, False):
                text = "# head" + eol + "ops:" + eol + self.LINE + eol + "  # comment" + eol + self.LINE.replace("CreateFleet", "RunInstances") + (eol if final else "")
                p = self.write(text)
                self.assertTrue(m.apply(p, {"ec2:CreateFleet": ("ec2-sim", ""), "ec2:RunInstances": ("ec2-sim", "")}))
                want = text.replace("backend: floci", "backend: ec2-sim")
                self.assertEqual(self.read(), want.encode(), f"eol={eol!r} final={final}")

    def test_utf8_comments_survive(self):
        text = "# Überprüfung ✓ 日本語\nops:\n" + self.LINE + "\n"
        p = self.write(text, binary=False)
        # write() used the locale encoding; use bytes to be sure of UTF-8
        with open(p, "wb") as f:
            f.write(text.encode())
        try:
            changed = m.apply(p, {"ec2:CreateFleet": ("moto", "")})
        except UnicodeError:
            self.skipTest("the locale cannot read UTF-8")
        self.assertTrue(changed)
        self.assertEqual(self.read(), text.replace("backend: floci", "backend: moto").encode())

    def test_missing_decision_and_bad_backend_names_are_refused_before_anything_is_written(self):
        text = "ops:\n" + self.LINE + "\n" + self.LINE.replace("CreateFleet", "RunInstances") + "\n"
        p = self.write(text)
        for decision in ({"ec2:CreateFleet": ("moto", "")}, {"ec2:CreateFleet": ("moto", ""), "ec2:RunInstances": ("Bad Name", "")},
                         {"ec2:CreateFleet": ("moto", ""), "ec2:RunInstances": ("", "")}, {"ec2:CreateFleet": ("moto", ""), "ec2:RunInstances": ("a,b", "")},
                         {"ec2:CreateFleet": ("moto", ""), "ec2:RunInstances": ("x}\n  - {service: evil", "")}):
            with self.assertRaises(ValueError):
                m.apply(p, decision)
            self.assertEqual(self.read(), text.encode())
            self.assertEqual(os.listdir(self.dir), ["ops.yaml"])

    def test_apply_is_atomic_when_the_rename_fails(self):
        text = "ops:\n" + self.LINE + "\n"
        p = self.write(text)
        with mock.patch("os.replace", side_effect=OSError("disk gone")):
            with self.assertRaises(OSError):
                m.apply(p, {"ec2:CreateFleet": ("moto", "")})
        self.assertEqual(self.read(), text.encode())
        self.assertEqual(os.listdir(self.dir), ["ops.yaml"], "a temporary file was left behind")

    def test_apply_preserves_the_file_mode(self):
        p = self.write("ops:\n" + self.LINE + "\n")
        os.chmod(p, 0o640)
        m.apply(p, {"ec2:CreateFleet": ("moto", "")})
        self.assertEqual(os.stat(p).st_mode & 0o777, 0o640)

    def test_cli_apply_on_unwritable_directory_fails_with_2_and_keeps_the_file(self):
        if os.getuid() == 0:
            self.skipTest("root")
        text = "ops:\n" + self.LINE + "\n"
        p = self.write(text)
        rp = self.report_file([res("ec2:CreateFleet", "fail")])
        os.chmod(self.dir, 0o555)
        self.addCleanup(os.chmod, self.dir, 0o755)
        code, _, err = self.cli(rp, p, "--apply")
        self.assertEqual(code, 2)
        self.assertIn("s0matrix:", err)
        self.assertEqual(self.read(), text.encode())

    def test_bug_apply_replaces_a_symlinked_ops_yaml_with_a_regular_file(self):
        real_dir = os.path.join(self.dir, "real")
        os.mkdir(real_dir)
        target = os.path.join(real_dir, "ops.yaml")
        with open(target, "w") as f:
            f.write("ops:\n" + self.LINE + "\n")
        link = self.path("link.yaml")
        os.symlink(target, link)
        m.apply(link, {"ec2:CreateFleet": ("moto", "")})
        # BUG: os.replace(tmp, link) replaces the symlink itself, so the real file is not updated and the link is gone.
        self.assertTrue(os.path.islink(link), "the symlink was replaced by a regular file")
        with open(target) as f:
            self.assertIn("backend: moto", f.read())

    def test_bug_a_key_that_ends_in_backend_is_mistaken_for_the_backend_key(self):
        line = "  - {service: ec2, name: CreateFleet, fallback_backend: moto, backend: floci}"
        p = self.write("ops:\n" + line + "\n")
        try:
            got = m.read_ops(p)
        except ValueError:
            return  # refusing is acceptable
        # BUG: OP_LINE has no word boundary before "backend: ", so the lazy match stops inside "fallback_backend: ".
        self.assertEqual(got, [("ec2", "CreateFleet", "floci")])

    def test_duplicate_operation_lines_get_the_same_decision(self):
        text = "ops:\n" + self.LINE + "\n" + self.LINE + "\n"
        p = self.write(text)
        m.apply(p, {"ec2:CreateFleet": ("moto", "")})
        self.assertEqual(self.read().decode().count("backend: moto"), 2)


# ---------------------------------------------------------------------------------------------------------
class CliTests(TempOps):
    OPS_TEXT = "ops:\n  - {service: ec2, name: CreateFleet, protocol: query, backend: floci, fidelity: x}\n"

    def test_usage_errors_exit_2_without_touching_anything(self):
        p = self.write(self.OPS_TEXT)
        rp = self.report_file([res("ec2:CreateFleet", "pass")])
        cases = [
            [],
            [rp],
            [rp, p, "extra"],
            ["--report", f"floci={rp}"],
            ["--report", f"floci={rp}", rp, p],
            ["--report", f"floci={rp}", "--report", f"floci={rp}", p],
            ["--report", f"ec2-sim={rp}", p],
            ["--report", "floci", p],
            ["--report", "=x", p],
            ["--report", "floci=", p],
            ["--report", f"floci={rp}", "--backend", "moto", p],
            ["--backend", "ec2-sim", rp, p],
            [rp, p, "--bogus"],
            ["--rep", f"floci={rp}", p],  # abbreviations are disabled
            [os.path.join(self.dir, "nope.json"), p],
            [rp, os.path.join(self.dir, "nope.yaml")],
        ]
        for argv in cases:
            code, out, err = self.cli(*argv, "--apply")
            self.assertEqual(code, 2, argv)
            self.assertEqual(self.read(), self.OPS_TEXT.encode(), argv)

    def test_malformed_reports_are_rejected_cleanly(self):
        p = self.write(self.OPS_TEXT)
        for name, content in {
            "empty": "", "not json": "{", "list": "[]", "string": '"x"', "no results": "{}", "results dict": '{"results": {}}',
            "results null": '{"results": null}', "bad entry": '{"results": [1]}', "bad name": '{"results": [{"name": 3, "status": "pass"}]}',
            "nan": '{"results": [NaN]}',
        }.items():
            rp = self.write(content, "r.json")
            code, out, err = self.cli(rp, p, "--apply")
            self.assertEqual(code, 2, name)
            self.assertIn("s0matrix:", err, name)
            self.assertEqual(self.read(), self.OPS_TEXT.encode(), name)

    def test_bug_deeply_nested_json_report_crashes_with_a_traceback(self):
        p = self.write(self.OPS_TEXT)
        rp = self.write("[" * 100000, "r.json")
        # BUG: load_report catches OSError and JSONDecodeError only; json.load raises RecursionError, which escapes
        # main() as an uncaught exception (traceback, exit 1) instead of "s0matrix: ..." and exit 2.
        try:
            code, out, err = self.cli(rp, p, "--apply")
        except RecursionError:
            self.fail("RecursionError escaped main()")
        self.assertEqual(code, 2)

    def test_binary_garbage_in_either_file_is_an_error_not_a_traceback(self):
        p = self.write(self.OPS_TEXT)
        rp = self.write(b"\xff\xfe\x00garbage", "r.json", binary=True)
        self.assertEqual(self.cli(rp, p)[0], 2)
        rp = self.report_file([res("ec2:CreateFleet", "pass")])
        p2 = self.write(b"\xff\xfe\x00 service: x", "bad.yaml", binary=True)
        self.assertEqual(self.cli(rp, p2)[0], 2)

    def test_single_report_mode_with_backend_flag(self):
        shutil.copy(REAL_OPS, self.path())
        rp = self.report_file(all_pass())
        code, out, _ = self.cli("--backend", "moto", rp, self.path())
        self.assertEqual(code, 0)
        self.assertIn("| ec2:CreateFleet | moto |", out)
        self.assertIn("| sts:GetCallerIdentity | moto |", out)

    def test_json_report_keys_the_probe_writes_are_accepted(self):
        # shape written by internal/s0probe.JSON: summary, failed_groups, results[], required_fields
        p = self.write(self.OPS_TEXT)
        doc = {"summary": {"Pass": 1}, "failed_groups": [], "results": [{"name": "ec2:CreateFleet", "group": "ec2", "status": "pass", "detail": "i-1 (m5.large)"}],
               "required_fields": {"ec2:CreateFleet": ["fleetId"]}}
        rp = self.write(json.dumps(doc), "r.json")
        self.assertEqual(self.cli(rp, p)[0], 0)


class FuzzishTests(TempOps):
    """Arbitrary text as ops.yaml and arbitrary JSON as a report: never a traceback, never a garbage file."""

    ALPHABET = list("{}[],:# \t-\r\n\"'") + list("abcxyzABC019_") + ["service", "name", "backend", "floci", "moto", ": ", ", ", "- {", "}\n", "\x00", "é"]

    def mutate(self, rng, line):
        s = list(line)
        for _ in range(rng.randint(0, 4)):
            i = rng.randint(0, len(s))
            op = rng.choice("dir")
            if op == "d" and s:
                del s[min(i, len(s) - 1)]
            elif op == "i":
                s.insert(i, rng.choice(self.ALPHABET))
            elif s:
                s[min(i, len(s) - 1)] = rng.choice(self.ALPHABET)
        return "".join(s)

    def test_mutated_ops_files_are_read_exactly_or_refused_and_apply_never_writes_garbage(self):
        rng = random.Random(99)
        with open(REAL_OPS) as f:
            real = f.read().split("\n")
        for i in range(1500):
            lines = [self.mutate(rng, l) if rng.random() < 0.15 else l for l in real]
            if rng.random() < 0.1:
                lines.insert(rng.randrange(len(lines)), "".join(rng.choice(self.ALPHABET) for _ in range(rng.randint(0, 40))))
            text = rng.choice(["\n", "\r\n"]).join(lines)
            p = self.write(text)
            try:
                ops = m.read_ops(p)
            except ValueError:
                self.assertEqual(self.read(), text.encode())
                continue
            decision = {f"{s}:{n}": (rng.choice(["floci", "moto", "ec2-sim", "eks-facade"]), "") for s, n, _ in ops}
            try:
                changed = m.apply(p, decision)
            except ValueError:
                self.assertEqual(self.read(), text.encode(), f"iteration {i}")
                continue
            new = self.read().decode()
            self.assertEqual(changed, new != text)
            self.assertEqual([(s, n, decision[f"{s}:{n}"][0]) for s, n, _ in ops], m.read_ops(p), f"iteration {i}")
            self.assertEqual(len(new.split("\n")), len(text.split("\n")))
            self.assertEqual([f for f in os.listdir(self.dir) if f != "ops.yaml"], [])

    def test_random_json_documents_as_reports(self):
        rng = random.Random(5)
        p = self.write("ops:\n  - {service: ec2, name: CreateFleet, backend: floci}\n")

        def val(depth=0):
            return rng.choice([None, True, 0, 1.5, "", "ec2:CreateFleet", "pass", " ", [], {}, "x y"]) if depth > 2 or rng.random() < 0.5 else (
                [val(depth + 1) for _ in range(rng.randint(0, 3))] if rng.random() < 0.5 else {k: val(depth + 1) for k in rng.sample(["name", "status", "results", "x"], rng.randint(0, 3))})

        for _ in range(500):
            doc = val()
            if rng.random() < 0.5:
                doc = {"results": [val() if rng.random() < 0.3 else {"name": rng.choice(["ec2:CreateFleet", "ec2:CreateFleet (x)", "", " "]), "status": rng.choice(["pass", "fail", None, 3])}
                                   for _ in range(rng.randint(0, 4))]}
            rp = self.write(json.dumps(doc), "r.json")
            code, out, err = self.cli(rp, p, "--apply")
            self.assertIn(code, (0, 2), json.dumps(doc))
            self.assertTrue(go_validate(self.read().decode()) == [])


if __name__ == "__main__":
    unittest.main()
