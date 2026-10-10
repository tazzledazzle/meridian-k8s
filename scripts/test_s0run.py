"""Tests scripts/s0-run.sh against a stub `docker` on PATH: no container is ever started."""
import os
import shutil
import stat
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "s0-run.sh")
SECRET = "sekret-value-that-must-not-reach-argv"
ACCESS = "AKIASTUBBEDONLY"

# The stub records its arguments one per line, may write the report the real probe would, and exits with STUB_RC.
STUB = """#!/usr/bin/env bash
printf '%s\\n' "$@" > "$STUB_DIR/args.log"
echo called >> "$STUB_DIR/calls.log"
env | grep -c '^AWS_SECRET_ACCESS_KEY=' > "$STUB_DIR/secret_in_env.log" || true
if [ "${STUB_WRITE:-0}" = 1 ]; then
  while [ $# -gt 0 ]; do
    if [ "$1" = -v ]; then case "$2" in *:/out) host="${2%:/out}"; echo '{}' > "$host/${STUB_NAME:-report}.json";; esac; fi
    shift
  done
fi
exit "${STUB_RC:-0}"
"""


class RunScriptTests(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root)
        os.makedirs(os.path.join(self.root, "scripts"))
        shutil.copy(SCRIPT, os.path.join(self.root, "scripts", "s0-run.sh"))
        os.makedirs(os.path.join(self.root, "bin", "linux"))
        with open(os.path.join(self.root, "bin", "linux", "s0probe"), "w") as f:
            f.write("binary")
        os.chmod(os.path.join(self.root, "bin", "linux", "s0probe"), 0o755)
        self.write(".env", f"AWS_ACCESS_KEY_ID={ACCESS}\nAWS_SECRET_ACCESS_KEY={SECRET}\n")
        self.write(".env.images", "STATIC_IMAGE=gcr.io/distroless/static@sha256:abc\nK3S_VERSION=v1.35.9-k3s1\n")
        self.stub = os.path.join(self.root, "stubbin")
        os.makedirs(self.stub)
        p = os.path.join(self.stub, "docker")
        with open(p, "w") as f:
            f.write(STUB)
        os.chmod(p, 0o755)
        self.state = os.path.join(self.root, "stubstate")
        os.makedirs(self.state)

    def write(self, rel, text):
        with open(os.path.join(self.root, rel), "w") as f:
            f.write(text)

    def run_script(self, *args, rc=0, write=False, name="report", env=None):
        e = {"PATH": f"{self.stub}:/usr/bin:/bin", "STUB_DIR": self.state, "STUB_RC": str(rc), "STUB_NAME": name,
             "STUB_WRITE": "1" if write else "0", "HOME": self.root}
        e.update(env or {})
        return subprocess.run(["bash", os.path.join(self.root, "scripts", "s0-run.sh"), *args], env=e, cwd="/",
                              capture_output=True, text=True, timeout=60)

    def docker_args(self):
        with open(os.path.join(self.state, "args.log")) as f:
            return f.read().split("\n")

    def docker_calls(self):
        p = os.path.join(self.state, "calls.log")
        return open(p).read().count("called") if os.path.exists(p) else 0

    def test_script_is_executable_and_parses(self):
        self.assertTrue(os.stat(SCRIPT).st_mode & stat.S_IXUSR)
        r = subprocess.run(["bash", "-n", SCRIPT], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_passing_run_builds_the_expected_docker_command(self):
        r = self.run_script(write=True)
        self.assertEqual(r.returncode, 0, r.stderr + r.stdout)
        a = self.docker_args()
        joined = " ".join(a)
        self.assertEqual(a[0], "run")
        for want in ("--rm", "--network meridian", "-endpoint http://aws-gateway:4566", "-region us-west-2", "-cluster meridian-g0",
                     "-eks-create", "-queue meridian-g0-interruption", "-name report", "-out /out",
                     "-title S0 contract probe: floci", "--entrypoint /s0probe", "--read-only", "--cap-drop ALL",
                     "--security-opt no-new-privileges", "gcr.io/distroless/static@sha256:abc"):
            self.assertIn(want, joined)
        self.assertIn("-ami-param /aws/service/eks/optimized-ami/1.35/amazon-linux-2023/x86_64/standard/recommended/image_id", joined)
        self.assertIn(f"{self.root}/docs/s0:/out", joined)
        self.assertIn(f"{self.root}/bin/linux/s0probe:/s0probe:ro", joined)
        self.assertTrue(os.path.isdir(os.path.join(self.root, "docs", "s0")))

    def test_credentials_are_passed_by_name_only(self):
        self.run_script(write=True)
        a = self.docker_args()
        self.assertNotIn(SECRET, "\n".join(a))
        self.assertNotIn(ACCESS, "\n".join(a))
        for i, x in enumerate(a):
            if x in ("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"):
                self.assertEqual(a[i - 1], "-e")
        self.assertIn("AWS_SECRET_ACCESS_KEY", a)
        # ...and they are in the environment of the docker client so that `-e NAME` can forward them.
        with open(os.path.join(self.state, "secret_in_env.log")) as f:
            self.assertEqual(f.read().strip(), "1")

    def test_label_name_out_and_network_are_honoured(self):
        r = self.run_script("docs/s0", "report-moto", write=True, name="report-moto", env={"S0_LABEL": "moto", "MERIDIAN_NET": "meridian-gate"})
        self.assertEqual(r.returncode, 0, r.stderr)
        joined = " ".join(self.docker_args())
        self.assertIn("-title S0 contract probe: moto", joined)
        self.assertIn("-name report-moto", joined)
        self.assertIn("--network meridian-gate", joined)

    def test_failing_checks_exit_zero_when_the_report_was_written(self):
        r = self.run_script(rc=1, write=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("expected during S0", r.stdout + r.stderr)

    def test_exit_one_without_a_fresh_report_is_an_error(self):
        os.makedirs(os.path.join(self.root, "docs", "s0"))
        self.write("docs/s0/report.json", "{}")  # a stale report from an earlier run must not count
        os.utime(os.path.join(self.root, "docs", "s0", "report.json"), (1, 1))
        r = self.run_script(rc=1, write=False)
        self.assertEqual(r.returncode, 1)
        self.assertIn("no new report", r.stderr)

    def test_other_probe_exit_codes_are_propagated(self):
        for rc in (2, 125, 127):
            with self.subTest(rc=rc):
                r = self.run_script(rc=rc, write=True)
                self.assertEqual(r.returncode, rc)

    def test_unsafe_out_or_name_is_refused_before_docker_runs(self):
        for args in (["../escape"], ["/abs/path"], ["docs/s0:rw"], ["docs,s0"], ["docs/s0", "../x"], ["docs/s0", "a/b"], ["docs/s0", ""], ["docs s0"], ["a/../b"]):
            with self.subTest(args=args):
                r = self.run_script(*args, write=True)
                self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertEqual(self.docker_calls(), 0)

    def test_missing_prerequisites_fail_with_a_hint_and_never_call_docker(self):
        os.unlink(os.path.join(self.root, "bin", "linux", "s0probe"))
        r = self.run_script()
        self.assertEqual(r.returncode, 2)
        self.assertIn("make build-linux", r.stderr)
        os.unlink(os.path.join(self.root, ".env"))
        r = self.run_script()
        self.assertEqual(r.returncode, 2)
        self.assertIn("make init", r.stderr)
        self.assertEqual(self.docker_calls(), 0)

    def test_empty_credentials_in_env_file_are_refused(self):
        self.write(".env", "AWS_ACCESS_KEY_ID=\nAWS_SECRET_ACCESS_KEY=\n")
        r = self.run_script()
        self.assertNotEqual(r.returncode, 0)
        self.assertEqual(self.docker_calls(), 0)

    def test_nothing_is_written_into_the_repo_outside_the_output_directory(self):
        before = set(os.listdir(self.root))
        self.run_script(write=True)
        self.assertEqual(set(os.listdir(self.root)) - before, {"docs"})


if __name__ == "__main__":
    unittest.main()
