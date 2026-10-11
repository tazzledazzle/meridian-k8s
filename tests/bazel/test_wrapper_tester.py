"""Tester (meridian-17s.61): adversarial checks on tools/make/meridian.sh and the parity test itself.

Wrapper behaviour is compared with what `make` really does, using stub docker/k3d binaries in a scratch
workspace (nothing real is touched). Tests named *_defect_* assert the correct behaviour and are marked
expectedFailure: each one reproduces a defect the writer should fix; remove the marker when fixed.
"""
import os
import re
import shutil
import stat
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, "..", ".."))
WRAPPER = os.path.join(ROOT, "tools", "make", "meridian.sh")
MAKEFILE = os.path.join(ROOT, "Makefile")


def write_text(path, text):
    with open(path, "w") as f:
        f.write(text)


def read_text(path):
    with open(path) as f:
        return f.read()


def write_exe(path, text):
    with open(path, "w") as f:
        f.write(text)
    os.chmod(path, os.stat(path).st_mode | stat.S_IXUSR)


class Scratch:
    """A scratch workspace with recording stubs for docker, k3d and a fake s0matrix tool."""

    def __init__(self, parent_name="ws"):
        self.tmp = tempfile.mkdtemp(prefix="tester-")
        self.ws = os.path.join(self.tmp, parent_name)
        self.stubs = os.path.join(self.tmp, "stubs")
        self.rf = os.path.join(self.tmp, "rf")
        self.log = os.path.join(self.tmp, "log")
        for d in (self.ws, self.stubs, os.path.join(self.rf, "_main"), os.path.join(self.ws, "docs", "s0")):
            os.makedirs(d, exist_ok=True)
        for c in ("docker", "k3d", "python3"):
            if c == "python3":
                continue
            write_exe(
                os.path.join(self.stubs, c),
                '#!/bin/bash\necho "%s $* | UID=$MERIDIAN_UID GID=$MERIDIAN_GID PROFILES=${COMPOSE_PROFILES-}"'
                ' >> "%s"\n' % (c, self.log),
            )
        shutil.copy(MAKEFILE, os.path.join(self.ws, "Makefile"))
        write_text(os.path.join(self.ws, "docs", "s0", "report.json"), "{}")
        write_text(os.path.join(self.ws, "ops.yaml"), "x: 1\n")

    def env(self, **extra):
        e = dict(os.environ)
        e["PATH"] = self.stubs + os.pathsep + e["PATH"]
        for k in ("APPLY", "BUILD_WORKSPACE_DIRECTORY", "RUNFILES_DIR"):
            e.pop(k, None)
        e.update(extra)
        return e

    def run_wrapper(self, target, *args, **extra):
        env = self.env(BUILD_WORKSPACE_DIRECTORY=self.ws, RUNFILES_DIR=self.rf, **extra)
        return subprocess.run(["bash", WRAPPER, target, *args], env=env, cwd="/", capture_output=True, text=True)

    def read_log(self):
        try:
            return read_text(self.log).splitlines()
        except FileNotFoundError:
            return []

    def reset_log(self):
        if os.path.exists(self.log):
            os.remove(self.log)

    def close(self):
        shutil.rmtree(self.tmp, ignore_errors=True)


def have(tool):
    return shutil.which(tool) is not None


@unittest.skipUnless(have("make") and have("bash"), "needs make and bash")
class WrapperVersusMake(unittest.TestCase):
    def setUp(self):
        self.s = Scratch()
        self.addCleanup(self.s.close)

    def test_s0_down_runs_the_same_commands_with_the_same_environment_as_make(self):
        r = subprocess.run(["make", "s0-down"], cwd=self.s.ws, env=self.s.env(), capture_output=True, text=True)
        self.assertEqual(0, r.returncode, r.stderr)
        want = self.s.read_log()
        self.s.reset_log()
        got_run = self.s.run_wrapper("s0-down")
        self.assertEqual(0, got_run.returncode, got_run.stderr)
        got = self.s.read_log()
        # make exports PATH with <ws>/bin first; compare only the stub-visible columns, which carry UID/GID/profiles.
        self.assertEqual(want, got)
        self.assertEqual(2, len(got))
        self.assertIn("UID=%d" % os.getuid(), got[0])

    def test_s0_down_survives_k3d_failure_like_make_dash_prefix(self):
        write_exe(os.path.join(self.s.stubs, "k3d"), "#!/bin/bash\nexit 7\n")
        r = self.s.run_wrapper("s0-down")
        self.assertEqual(0, r.returncode, r.stderr)
        self.assertTrue(any(l.startswith("docker compose") for l in self.s.read_log()))

    def test_s0_down_fails_when_compose_fails_like_make(self):
        write_exe(os.path.join(self.s.stubs, "docker"), "#!/bin/bash\nexit 9\n")
        m = subprocess.run(["make", "s0-down"], cwd=self.s.ws, env=self.s.env(), capture_output=True, text=True)
        w = self.s.run_wrapper("s0-down")
        self.assertNotEqual(0, m.returncode)
        self.assertNotEqual(0, w.returncode)

    def _decide_args(self, apply_env=None, extra=(), moto=False):
        if moto:
            write_text(os.path.join(self.s.ws, "docs", "s0", "report-moto.json"), "{}")
        argfile = os.path.join(self.s.tmp, "decide.args")
        write_exe(os.path.join(self.s.rf, "_main", "s0m"), '#!/bin/bash\nprintf "%%s\\n" "$@" > "%s"\n' % argfile)
        extra_env = {"MB_s0matrix": "_main/s0m"}
        if apply_env is not None:
            extra_env["APPLY"] = apply_env
        r = self.s.run_wrapper("s0-decide", *extra, **extra_env)
        self.assertEqual(0, r.returncode, r.stderr)
        return read_text(argfile).split()

    def _make_decide_args(self, apply_env=None, moto=False):
        if moto:
            write_text(os.path.join(self.s.ws, "docs", "s0", "report-moto.json"), "{}")
        env = self.s.env()
        if apply_env is not None:
            env["APPLY"] = apply_env
        r = subprocess.run(["make", "-n", "s0-decide"], cwd=self.s.ws, env=env, capture_output=True, text=True)
        self.assertEqual(0, r.returncode, r.stderr)
        m = re.search(r"s0matrix\.py (.*)", r.stdout)
        return m.group(1).split()

    def test_s0_decide_arguments_match_make(self):
        for apply_env in (None, "1", "0", "yes"):
            for moto in (False, True):
                with self.subTest(APPLY=apply_env, moto=moto):
                    s = Scratch()
                    self.addCleanup(s.close)
                    self.s = s
                    want = self._make_decide_args(apply_env, moto)
                    got = self._decide_args(apply_env, moto=moto)
                    self.assertEqual(want, got)

    def test_s0_decide_dashdash_apply(self):
        self.assertEqual("--apply", self._decide_args(extra=["--apply"])[-1])


@unittest.skipUnless(have("bash"), "needs bash")
class WrapperEdges(unittest.TestCase):
    def setUp(self):
        self.s = Scratch(parent_name="work space")
        self.addCleanup(self.s.close)

    def test_unknown_target_is_exit_2(self):
        r = self.s.run_wrapper("nope")
        self.assertEqual(2, r.returncode)

    def test_missing_target_argument_fails(self):
        r = subprocess.run(["bash", WRAPPER], env=self.s.env(), capture_output=True, text=True)
        self.assertNotEqual(0, r.returncode)

    def test_refuses_to_run_without_bazel_run(self):
        r = subprocess.run(["bash", WRAPPER, "build"], env=self.s.env(RUNFILES_DIR=self.s.rf), capture_output=True, text=True)
        self.assertNotEqual(0, r.returncode)

    def test_workspace_path_with_spaces_and_foreign_cwd(self):
        write_exe(os.path.join(self.s.rf, "_main", "svc"), "#!/bin/sh\n")
        r = self.s.run_wrapper("build", MB_HOST_BINS="_main/svc")
        self.assertEqual(0, r.returncode, r.stderr)
        self.assertTrue(os.access(os.path.join(self.s.ws, "bin", "svc"), os.X_OK))

    def test_unwired_tool_is_a_clear_error(self):
        r = self.s.run_wrapper("check-sock")
        self.assertNotEqual(0, r.returncode)  # 127 in practice: the exit 2 inside $(bin ..) is lost, the empty command fails
        self.assertIn("no tool", r.stderr)

    @unittest.expectedFailure
    def test_defect_t_wrap_1_build_reports_success_when_a_copy_fails(self):
        """DEFECT T-WRAP-1 (High): copy_bins chains `cp && chmod && mv`; set -e ignores a failure that is not the
        last command of an && list, so `bazel run //:build` / `//:build-linux` exit 0 with 'wrote N binaries' even
        when nothing was copied (read-only bin/, full disk, missing runfile). s0-up then starts with stale binaries."""
        write_exe(os.path.join(self.s.rf, "_main", "ok"), "#!/bin/sh\n")
        r = self.s.run_wrapper("build", MB_HOST_BINS="_main/ok _main/does-not-exist")
        self.assertNotEqual(0, r.returncode, "exit 0 although _main/does-not-exist could not be copied: " + r.stdout)

    def test_build_copies_and_strips_linux_suffix(self):
        write_exe(os.path.join(self.s.rf, "_main", "svc_linux"), "#!/bin/sh\n")
        r = self.s.run_wrapper("build-linux", MB_LINUX_BINS="_main/svc_linux")
        self.assertEqual(0, r.returncode, r.stderr)
        self.assertTrue(os.access(os.path.join(self.s.ws, "bin", "linux", "svc"), os.X_OK))
        self.assertEqual([], [f for f in os.listdir(os.path.join(self.s.ws, "bin", "linux")) if f.endswith(".tmp")])


class ParityTestIsWeak(unittest.TestCase):
    """The parity test is regex over text. These pin the holes found by mutating the repo (see the report)."""

    def parity_passes_with(self, name, mutate):
        """Runs the real MakeParity suite with read(name) passed through mutate; True if the suite still passes."""
        import importlib.util

        spec = importlib.util.spec_from_file_location("tmp_parity", os.path.join(HERE, "test_make_parity.py"))
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        orig = mod.read

        def patched(*parts):
            text = orig(*parts)
            return mutate(text) if "/".join(parts) == name else text

        mod.read = patched
        suite = unittest.TestLoader().loadTestsFromTestCase(mod.MakeParity)
        res = unittest.TestResult()
        suite.run(res)
        return res.wasSuccessful()

    def test_unmutated_parity_suite_passes(self):
        self.assertTrue(self.parity_passes_with("none", lambda t: t))

    def test_removing_a_doc_row_is_caught(self):
        self.assertFalse(self.parity_passes_with("docs/build/bazel.md", lambda t: re.sub(r"^\| `make seed` .*$", "", t, flags=re.M)))

    @unittest.expectedFailure
    def test_defect_t_par_1_commented_out_alias_still_counts(self):
        """DEFECT T-PAR-1 (Medium): target_names() matches quoted strings in comments, so commenting out an alias
        in //:BUILD.bazel (`# "trace-show",`) leaves the parity test green."""
        self.assertFalse(
            self.parity_passes_with("BUILD.bazel", lambda t: t.replace('        "trace-show",', '        # "trace-show",'))
        )

    @unittest.expectedFailure
    def test_defect_t_par_2_prose_mention_satisfies_doc_check(self):
        """DEFECT T-PAR-2 (Low): the doc check is a substring test for `make T`; a prose mention elsewhere satisfies
        it after the table row is deleted."""
        self.assertFalse(
            self.parity_passes_with(
                "docs/build/bazel.md", lambda t: re.sub(r"^\| `make seed` .*$", "see `make seed` maybe", t, flags=re.M)
            )
        )

    @unittest.expectedFailure
    def test_defect_t_par_3_second_phony_line_ignored(self):
        """DEFECT T-PAR-3 (Low): phony_targets() returns at the first .PHONY line; a target declared on a second
        .PHONY line is never required to have a Bazel counterpart."""
        self.assertFalse(
            self.parity_passes_with("Makefile", lambda t: t + "\n.PHONY: newthing\nnewthing:\n\techo hi\n")
        )

    def test_cmd_directories_match_wrapper_binary_list(self):
        """Guard: a new cmd/<x> must be added to CMDS (build/build-linux), the images, and //:go_tree."""
        cmds = sorted(d for d in os.listdir(os.path.join(ROOT, "cmd")) if os.path.isdir(os.path.join(ROOT, "cmd", d)))
        defs = read_text(os.path.join(ROOT, "tools", "make", "defs.bzl"))
        listed = sorted(re.findall(r'^\s+"([a-z0-9-]+)",$', defs.split("CMDS = [")[1].split("]")[0], re.M))
        self.assertEqual(cmds, listed)
        root = read_text(os.path.join(ROOT, "BUILD.bazel"))
        for c in cmds:
            self.assertIn('"//cmd/%s:gosrcs"' % c, root, "go_tree misses cmd/%s" % c)

    def test_every_python_test_file_has_a_py_test(self):
        """Guard: scripts/test_*.py not listed in scripts/BUILD.bazel would silently not run under Bazel."""
        files = sorted(f for f in os.listdir(os.path.join(ROOT, "scripts")) if re.match(r"test_.*\.py$", f))
        build = read_text(os.path.join(ROOT, "scripts", "BUILD.bazel"))
        for f in files:
            self.assertIn('srcs = ["%s"]' % f, build, "%s has no py_test" % f)


if __name__ == "__main__":
    unittest.main()
