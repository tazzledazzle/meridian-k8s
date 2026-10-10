import contextlib
import io
import json
import os
import re
import shutil
import tempfile
import unittest

import profiles
import s0matrix as m

HERE = os.path.dirname(os.path.abspath(__file__))
REAL_OPS = os.path.join(HERE, "..", "ops.yaml")

# The operations of the real ops.yaml as (service, name, backend); the tests do not depend on the backends.
OPS = m.read_ops(REAL_OPS)
KEYS = [f"{s}:{n}" for s, n, _ in OPS]


def res(name, status):
    return {"name": name, "group": name.split(":")[0], "status": status, "detail": ""}


def report(default="pass", **overrides):
    """A probe report with one result per operation in ops.yaml.

    overrides maps 'Service_Op' (colon replaced by underscore) to a status, or to None to leave the op out.
    """
    wanted = {k.replace("_", ":", 1): v for k, v in overrides.items()}
    out = []
    for key in KEYS:
        status = wanted.pop(key, default)
        if status is not None:
            # A qualifier in parentheses, as the Go probe writes it, must not matter.
            out.append(res(key + (" (discovery tag)" if key == "ec2:DescribeSubnets" else ""), status))
    assert not wanted, f"unknown operations {wanted}"
    return {"results": out}


def backends(decision, keys):
    return {decision[k][0] for k in keys}


class DecideTests(unittest.TestCase):
    def test_everything_passing_on_floci_stays_on_floci(self):
        d = m.decide({"floci": report()["results"]}, OPS)
        self.assertEqual(backends(d, KEYS), {"floci"})
        self.assertIn("none", m.table(d))

    def test_floci_createfleet_failure_moves_the_coupled_group_to_moto_when_moto_passes(self):
        floci = report(ec2_CreateFleet="fail", ec2_RunInstances="skip", ec2_DescribeInstances="skip")["results"]
        d = m.decide({"floci": floci, "moto": report()["results"]}, OPS)
        for key in m.COUPLED:
            self.assertEqual(d[key][0], "moto", key)
        # Reference data and other services stay where they passed.
        self.assertEqual(d["ec2:DescribeSpotPriceHistory"][0], "floci")
        self.assertEqual(d["pricing:GetProducts"][0], "floci")
        self.assertIn("Task 14", m.table(d))
        self.assertNotIn("Tasks 11", m.table(d))

    def test_both_failing_sends_the_coupled_group_to_ec2_sim(self):
        floci = report(ec2_CreateFleet="fail")["results"]
        moto = report(ec2_TerminateInstances="fail")["results"]
        d = m.decide({"floci": floci, "moto": moto}, OPS)
        for key in m.COUPLED:
            self.assertEqual(d[key][0], "ec2-sim", key)
        self.assertIn("Tasks 11 and 12", m.table(d))

    def test_single_report_failing_goes_straight_to_ec2_sim(self):
        d = m.decide({"floci": report(ec2_CreateFleet="fail")["results"]}, OPS)
        self.assertEqual({d[k][0] for k in m.COUPLED}, {"ec2-sim"})

    def test_moto_never_gets_the_group_unless_it_passed_every_coupled_check(self):
        floci = report(ec2_CreateFleet="fail")["results"]
        moto = report(ec2_CreateTags="skip")["results"]
        d = m.decide({"floci": floci, "moto": moto}, OPS)
        self.assertEqual({d[k][0] for k in m.COUPLED}, {"ec2-sim"})

    def test_coupled_operations_are_never_split_whatever_fails(self):
        # One coupled operation failing on floci, and every combination of what moto fails.
        for fl in m.COUPLED:
            for mo in [None] + list(m.COUPLED):
                with self.subTest(floci=fl, moto=mo):
                    floci = report(**{fl.replace(":", "_", 1): "fail"})["results"]
                    moto = report(**({mo.replace(":", "_", 1): "fail"} if mo else {}))["results"]
                    d = m.decide({"floci": floci, "moto": moto}, OPS)
                    got = {d[k][0] for k in m.COUPLED}
                    self.assertEqual(len(got), 1, f"split across {got}")
                    self.assertEqual(got, {"moto" if mo is None else "ec2-sim"})

    def test_a_single_failing_coupled_check_moves_the_whole_group_on_floci_only_report(self):
        for fl in m.COUPLED:
            with self.subTest(op=fl):
                d = m.decide({"floci": report(**{fl.replace(":", "_", 1): "fail"})["results"]}, OPS)
                self.assertEqual({d[k][0] for k in m.COUPLED}, {"ec2-sim"})

    def test_an_operation_missing_from_the_report_counts_as_failed(self):
        # Coupled: a missing CreateTags result moves the group.
        d = m.decide({"floci": report(ec2_CreateTags=None)["results"]}, OPS)
        self.assertEqual({d[k][0] for k in m.COUPLED}, {"ec2-sim"})
        # Uncoupled: a missing spot price result moves only that operation.
        d = m.decide({"floci": report(ec2_DescribeSpotPriceHistory=None)["results"]}, OPS)
        self.assertEqual(d["ec2:DescribeSpotPriceHistory"][0], "ec2-sim")
        self.assertEqual(d["ec2:CreateFleet"][0], "floci")
        # An empty report passes nothing.
        d = m.decide({"floci": []}, OPS)
        self.assertNotIn("floci", backends(d, KEYS))

    def test_skip_and_unknown_statuses_count_as_failed(self):
        d = m.decide({"floci": report(pricing_GetProducts="skip", sqs_CreateQueue="fail", eks_DescribeCluster="weird")["results"]}, OPS)
        self.assertEqual(d["pricing:GetProducts"][0], "pricing-facade")
        self.assertEqual(d["sqs:CreateQueue"][0], "moto")
        self.assertEqual(d["eks:DescribeCluster"][0], "eks-facade")
        self.assertEqual(d["ec2:CreateFleet"][0], "floci")
        t = m.table(d)
        self.assertIn("Task 13", t)
        self.assertIn("Task 14", t)

    def test_reference_data_failure_moves_only_that_operation(self):
        d = m.decide({"floci": report(ec2_DescribeSpotPriceHistory="fail")["results"]}, OPS)
        self.assertEqual(d["ec2:DescribeSpotPriceHistory"][0], "ec2-sim")
        self.assertEqual({d[k][0] for k in m.COUPLED}, {"floci"})

    def test_uncoupled_operation_takes_the_first_backend_that_passed_it(self):
        floci = report(ec2_DescribeInstanceTypes="fail")["results"]
        moto = report()["results"]
        d = m.decide({"floci": floci, "moto": moto}, OPS)
        self.assertEqual(d["ec2:DescribeInstanceTypes"][0], "moto")
        self.assertEqual(d["ec2:DescribeAvailabilityZones"][0], "floci")

    def test_any_failing_check_of_an_operation_fails_the_operation(self):
        results = report()["results"]
        results.append(res("ec2:DescribeSubnets (second filter)", "fail"))
        d = m.decide({"floci": results}, OPS)
        self.assertEqual({d[k][0] for k in m.COUPLED}, {"ec2-sim"})

    def test_reason_names_what_failed(self):
        d = m.decide({"floci": report(ec2_CreateFleet="fail")["results"], "moto": report()["results"]}, OPS)
        self.assertIn("floci", d["ec2:CreateFleet"][1])
        self.assertIn("ec2:CreateFleet", d["ec2:CreateFleet"][1])
        self.assertIn("coupled", d["ec2:CreateTags"][1])

    def test_the_decision_is_independent_of_report_argument_order(self):
        a = m.decide({"moto": report()["results"], "floci": report(ec2_CreateFleet="fail")["results"]}, OPS)
        b = m.decide({"floci": report(ec2_CreateFleet="fail")["results"], "moto": report()["results"]}, OPS)
        self.assertEqual(a, b)

    def test_unknown_backend_and_no_report_are_errors(self):
        with self.assertRaises(ValueError):
            m.decide({"nonesuch": report()["results"]}, OPS)
        with self.assertRaises(ValueError):
            m.decide({}, OPS)
        with self.assertRaises(ValueError):
            m.decide({"floci": report()["results"]}, [("ec2", "CreateFleet", "floci"), ("kms", "Encrypt", "floci")])

    def test_checks_are_matched_to_operations_by_the_first_word_of_the_name(self):
        self.assertEqual(m.operation_status([res("ec2:DescribeInstances (running, tagged)", "pass")]), {"ec2:DescribeInstances": "pass"})


class ApplyTests(unittest.TestCase):
    SRC = (
        "# header comment\n"
        "ops:\n"
        "  # --- ec2 ---\n"
        "  - {service: ec2, name: CreateFleet, protocol: query, backend: floci, fidelity: behavior-inferred}\n"
        "\n"
        "  - {service: ec2, name: DescribeInstances, protocol: query, backend: floci, fidelity: behavior-inferred} # keep me\n"
        "  # - {service: ec2, name: Disabled, protocol: query, backend: floci, fidelity: behavior-inferred}\n"
        "  - {service: pricing, name: GetProducts, protocol: json, backend: floci, fidelity: behavior-inferred}\n"
    )

    def write(self, text, newline=None):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        p = os.path.join(d, "ops.yaml")
        with open(p, "w", newline=newline) as f:
            f.write(text)
        return p

    def read(self, p):
        with open(p, newline="") as f:
            return f.read()

    def decision(self, p, **kw):
        floci = [res("ec2:CreateFleet", kw.get("fleet", "fail")), res("ec2:DescribeInstances", "pass"), res("pricing:GetProducts", kw.get("pricing", "pass"))]
        floci += [res(c, "pass") for c in m.COUPLED if c not in ("ec2:CreateFleet", "ec2:DescribeInstances")]
        return m.decide({"floci": floci}, m.read_ops(p))

    def test_apply_rewrites_only_backend_fields(self):
        p = self.write(self.SRC)
        m.apply(p, self.decision(p))
        want = self.SRC.replace("name: CreateFleet, protocol: query, backend: floci", "name: CreateFleet, protocol: query, backend: ec2-sim")
        want = want.replace("name: DescribeInstances, protocol: query, backend: floci", "name: DescribeInstances, protocol: query, backend: ec2-sim")
        self.assertEqual(self.read(p), want)

    def test_apply_is_idempotent(self):
        p = self.write(self.SRC)
        m.apply(p, self.decision(p))
        once = self.read(p)
        # Running the whole decision again on the rewritten file must not change it.
        m.apply(p, self.decision(p))
        self.assertEqual(self.read(p), once)

    def test_comments_blank_lines_order_and_trailing_text_survive(self):
        p = self.write(self.SRC)
        m.apply(p, self.decision(p))
        after = self.read(p).split("\n")
        before = self.SRC.split("\n")
        self.assertEqual(len(after), len(before))
        for a, b in zip(after, before):
            if "service:" in b and not b.lstrip().startswith("#"):
                self.assertEqual(re.sub(r"backend: [a-z0-9-]+", "backend: X", a), re.sub(r"backend: [a-z0-9-]+", "backend: X", b))
            else:
                self.assertEqual(a, b)
        self.assertIn("# keep me", self.read(p))
        self.assertIn("# - {service: ec2, name: Disabled, protocol: query, backend: floci", self.read(p))

    def test_line_endings_and_missing_final_newline_are_preserved(self):
        crlf = self.SRC.replace("\n", "\r\n").rstrip("\r\n")
        p = self.write(crlf, newline="")
        m.apply(p, self.decision(p))
        out = self.read(p)
        self.assertNotIn("\n", out.replace("\r\n", ""))
        self.assertFalse(out.endswith("\n"))
        self.assertIn("backend: ec2-sim", out)

    def test_backend_as_the_last_flow_key_is_rewritten(self):
        p = self.write("ops:\n  - {service: ec2, name: CreateFleet, protocol: query, fidelity: x, backend: floci}\n")
        d = {"ec2:CreateFleet": ("moto", "test")}
        m.apply(p, d)
        self.assertIn("backend: moto}", self.read(p))

    def test_an_unreadable_operation_line_fails_closed_and_leaves_the_file_alone(self):
        src = self.SRC + "  - {name: Odd, service: ec2, protocol: query, backend: floci}\n"
        p = self.write(src)
        with self.assertRaises(ValueError):
            m.apply(p, {"ec2:CreateFleet": ("moto", "x"), "ec2:DescribeInstances": ("moto", "x"), "pricing:GetProducts": ("floci", "x")})
        self.assertEqual(self.read(p), src)

    def test_an_operation_without_a_decision_fails_closed(self):
        p = self.write(self.SRC)
        with self.assertRaises(ValueError):
            m.apply(p, {"ec2:CreateFleet": ("moto", "x")})
        self.assertEqual(self.read(p), self.SRC)

    def test_applying_a_moto_decision_to_the_real_ops_yaml_changes_only_backend_fields_and_never_splits(self):
        p = self.write(self.read(REAL_OPS))
        d = m.decide({"floci": report(ec2_CreateFleet="fail")["results"], "moto": report()["results"]}, m.read_ops(p))
        m.apply(p, d)
        before, after = self.read(REAL_OPS).split("\n"), self.read(p).split("\n")
        self.assertEqual(len(before), len(after))
        for a, b in zip(before, after):
            self.assertEqual(re.sub(r"backend: [a-z0-9-]+", "backend: X", a), re.sub(r"backend: [a-z0-9-]+", "backend: X", b))
        now = {f"{s}:{n}": b for s, n, b in m.read_ops(p)}
        self.assertEqual({now[c] for c in m.COUPLED}, {"moto"})
        self.assertEqual(now["ec2:DescribeSpotPriceHistory"], "floci")
        # profiles.py (Compose profile selection) accepts moto as an EC2 backend with no change.
        self.assertEqual(profiles.profiles(self.read(p)), ["moto"])
        self.assertEqual(profiles.backend_of(self.read(p), "ec2:CreateFleet"), "moto")


class CliTests(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir)
        self.ops = os.path.join(self.dir, "ops.yaml")
        shutil.copy(REAL_OPS, self.ops)
        with open(self.ops) as f:
            self.original = f.read()

    def put(self, name, rep):
        p = os.path.join(self.dir, name)
        with open(p, "w") as f:
            f.write(rep if isinstance(rep, str) else json.dumps(rep))
        return p

    def cli(self, *argv):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = m.main(["s0matrix.py", *argv])
        return code, out.getvalue(), err.getvalue()

    def current(self):
        with open(self.ops) as f:
            return f.read()

    def test_single_report_mode_keeps_the_plan_usage(self):
        r = self.put("report.json", report())
        code, out, _ = self.cli(r, self.ops)
        self.assertEqual(code, 0)
        self.assertIn("| ec2:CreateFleet | floci |", out)
        self.assertEqual(self.current(), self.original)  # no --apply, no change

    def test_backend_flag_names_the_single_report(self):
        r = self.put("report-moto.json", report())
        code, out, _ = self.cli("--backend", "moto", r, self.ops)
        self.assertEqual(code, 0)
        self.assertIn("| ec2:CreateFleet | moto |", out)
        self.assertIn("| pricing:GetProducts | moto |", out)

    def test_multi_report_mode_with_apply(self):
        fl = self.put("report.json", report(ec2_CreateFleet="fail", ec2_RunInstances="skip"))
        mo = self.put("report-moto.json", report())
        code, out, _ = self.cli("--report", f"floci={fl}", "--report", f"moto={mo}", self.ops, "--apply")
        self.assertEqual(code, 0)
        self.assertIn("| ec2:CreateFleet | moto |", out)
        got = {f"{s}:{n}": b for s, n, b in m.read_ops(self.ops)}
        self.assertEqual({got[c] for c in m.COUPLED}, {"moto"})
        once = self.current()
        self.cli("--report", f"floci={fl}", "--report", f"moto={mo}", self.ops, "--apply")
        self.assertEqual(self.current(), once)

    def test_the_decision_table_is_printed_before_anything_is_written(self):
        fl = self.put("report.json", report(ec2_CreateFleet="fail"))
        code, out, _ = self.cli("--report", f"floci={fl}", self.ops)
        self.assertEqual(code, 0)
        self.assertIn("| ec2:CreateFleet | ec2-sim |", out)
        self.assertEqual(self.current(), self.original)

    def test_bad_input_exits_two_and_changes_nothing(self):
        good = self.put("good.json", report())
        bad_json = self.put("bad.json", "{not json")
        no_results = self.put("noresults.json", {"summary": {}})
        bad_entry = self.put("badentry.json", {"results": [{"name": "ec2:CreateFleet"}]})
        for argv in (
            [],
            [good],
            [os.path.join(self.dir, "missing.json"), self.ops],
            [bad_json, self.ops],
            [no_results, self.ops],
            [bad_entry, self.ops],
            [good, os.path.join(self.dir, "missing.yaml")],
            ["--report", f"floci={good}", "--report", f"floci={good}", self.ops],
            ["--report", f"nonesuch={good}", self.ops],
            ["--report", "floci", self.ops],
            ["--backend", "nonesuch", good, self.ops],
        ):
            with self.subTest(argv=argv):
                code, _, err = self.cli(*argv, "--apply")
                self.assertEqual(code, 2)
                self.assertTrue(err or code == 2)
                self.assertEqual(self.current(), self.original)

    def test_help_documents_both_modes_and_the_order(self):
        doc = m.__doc__
        for word in ("--report", "--backend", "floci, moto, ec2-sim", "eligible"):
            self.assertIn(word, doc)
        for banned in ("proven", "verified"):
            self.assertNotIn(banned, doc.lower())

    def test_the_table_says_a_pass_means_eligible(self):
        code, out, _ = self.cli(self.put("r.json", report()), self.ops)
        self.assertIn("eligible", out)
        self.assertIn("Karpenter", out)
        self.assertNotIn("proven", out.lower())
        self.assertNotIn("verified", out.lower())


class CoupledListTests(unittest.TestCase):
    def test_python_list_matches_the_go_list(self):
        with open(os.path.join(HERE, "..", "internal", "ops", "ops.go")) as f:
            go = f.read()
        block = re.search(r"var Coupled = \[\]string\{(.*?)\n\}", go, re.S).group(1)
        self.assertEqual(sorted(re.findall(r'"([^"]+)"', block)), sorted(m.COUPLED))

    def test_every_ops_yaml_service_has_a_fallback(self):
        for svc, _, _ in OPS:
            self.assertIn(svc, m.FALLBACK)


if __name__ == "__main__":
    unittest.main()
