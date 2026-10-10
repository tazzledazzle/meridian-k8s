"""Adversarial tests for scripts/s0-run.sh against a stub `docker` on PATH. No container is ever started.

A test named test_bug_* documents a defect present in the code as tested.
"""
import os
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "s0-run.sh")
SECRET = "sEcReT-must/not+reach=argv-0123456789"
ACCESS = "AKIASTUBACCESSKEYID"

# Records argv NUL-separated (newlines in arguments survive), the environment names, and may write reports.
STUB = r"""#!/usr/bin/env bash
printf '%s\0' "$@" > "$STUB_DIR/args.bin"
echo called >> "$STUB_DIR/calls.log"
env | grep -c '^AWS_SECRET_ACCESS_KEY=' > "$STUB_DIR/secret_in_env.log" || true
if [ -n "${STUB_WRITE:-}" ]; then
  while [ $# -gt 0 ]; do
    if [ "$1" = -v ]; then case "$2" in *:/out)
      host="${2%:/out}"
      case "$STUB_WRITE" in *md*) echo NEWMD > "$host/${STUB_NAME:-report}.md";; esac
      case "$STUB_WRITE" in *json*) echo '{"new":true}' > "$host/${STUB_NAME:-report}.json";; esac
    ;; esac; fi
    shift
  done
fi
exit "${STUB_RC:-0}"
"""


def tree(root):
    out = {}
    for d, dirs, files in os.walk(root):
        for n in dirs:
            out[os.path.relpath(os.path.join(d, n), root) + "/"] = None
        for n in files:
            p = os.path.join(d, n)
            with open(p, "rb") as f:
                out[os.path.relpath(p, root)] = f.read()
    return out


class RunScript(unittest.TestCase):
    def setUp(self):
        self.base = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.base, True)
        self.root = os.path.join(self.base, "repo")
        os.makedirs(os.path.join(self.root, "scripts"))
        shutil.copy(SCRIPT, os.path.join(self.root, "scripts", "s0-run.sh"))
        os.makedirs(os.path.join(self.root, "bin", "linux"))
        b = os.path.join(self.root, "bin", "linux", "s0probe")
        with open(b, "w") as f:
            f.write("binary")
        os.chmod(b, 0o755)
        self.write(".env", f"AWS_ACCESS_KEY_ID={ACCESS}\nAWS_SECRET_ACCESS_KEY={SECRET}\n")
        self.write(".env.images", "STATIC_IMAGE=gcr.io/distroless/static@sha256:abc\nK3S_VERSION=v1.35.9-k3s1\n")
        self.stub = os.path.join(self.base, "stubbin")
        os.makedirs(self.stub)
        p = os.path.join(self.stub, "docker")
        with open(p, "w") as f:
            f.write(STUB)
        os.chmod(p, 0o755)
        self.state = os.path.join(self.base, "state")
        os.makedirs(self.state)
        self.outside = os.path.join(self.base, "outside-marker")

    def write(self, rel, text):
        p = os.path.join(self.root, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "w") as f:
            f.write(text)
        return p

    def read(self, rel):
        with open(os.path.join(self.root, rel)) as f:
            return f.read()

    def run_script(self, *args, rc=0, write="", name="report", env=None, cwd="/"):
        e = {"PATH": f"{self.stub}:/usr/bin:/bin", "STUB_DIR": self.state, "STUB_RC": str(rc), "STUB_NAME": name, "STUB_WRITE": write, "HOME": self.base}
        e.update(env or {})
        return subprocess.run(["bash", os.path.join(self.root, "scripts", "s0-run.sh"), *args], env=e, cwd=cwd, capture_output=True, text=True, timeout=60)

    def argv(self):
        with open(os.path.join(self.state, "args.bin"), "rb") as f:
            data = f.read()
        return [x.decode() for x in data.split(b"\0")[:-1]]

    def calls(self):
        p = os.path.join(self.state, "calls.log")
        if not os.path.exists(p):
            return 0
        with open(p) as f:
            return f.read().count("called")


class ArgumentValidation(RunScript):
    BAD_OUT = ["a:b", "a,b", "../x", "a/../b", "..", "...", "/abs", "/", "", " ", "a b", "a\nb", "a\tb", "a;b", "$(touch X)", "`touch X`", "a'b", 'a"b',
               "~", "a*b", "a?b", "a|b", "a&b", "a>b", "a<b", "a\\b", "é", "a=b", "a#b", "a$b", "a!b"]
    GOOD_OUT = ["docs/s0", "out", "a/b/c", "a.b", "A_b-c", "./out", "out/", "a//b", "docs/s0/./x", ".hidden", "0"]

    def test_unsafe_output_directories_are_refused_before_anything_happens(self):
        for out in self.BAD_OUT:
            before = tree(self.root)
            r = self.run_script(out)
            self.assertEqual(r.returncode, 2, f"OUT={out!r}: {r.stdout} {r.stderr}")
            self.assertIn("unsafe", r.stderr, repr(out))
            self.assertEqual(self.calls(), 0, f"docker called for OUT={out!r}")
            self.assertEqual(tree(self.root), before, f"OUT={out!r} changed the tree")
            self.assertFalse(os.path.exists(os.path.join(self.root, "X")))

    def test_unsafe_report_names_are_refused(self):
        for name in ["a:b", "a,b", "..", "a..b", "../x", "a/b", "/abs", "", " ", "a b", "a\nb", "-x", ".x", "_x", "a;b", "$(touch X)", "é", "a*", "a\\b"]:
            before = tree(self.root)
            r = self.run_script("out", name)
            self.assertEqual(r.returncode, 2, f"NAME={name!r}: {r.stdout} {r.stderr}")
            self.assertEqual(self.calls(), 0, repr(name))
            self.assertEqual(tree(self.root), before)

    def test_safe_paths_reach_docker_inside_the_repo_mount(self):
        for out in self.GOOD_OUT:
            r = self.run_script(out, write="md json")
            self.assertEqual(r.returncode, 0, f"OUT={out!r}: {r.stderr}")
            mounts = [a for a in self.argv() if a.endswith(":/out")]
            self.assertEqual(len(mounts), 1)
            self.assertTrue(mounts[0].startswith(self.root + "/"), mounts[0])
            self.assertTrue(os.path.isdir(os.path.join(self.root, out)))
            self.assertFalse(os.path.exists(self.outside))

    def test_defaults_are_docs_s0_and_report(self):
        r = self.run_script(write="md json")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(os.path.exists(os.path.join(self.root, "docs", "s0", "report.json")))
        self.assertIn("-name", self.argv())
        self.assertEqual(self.argv()[self.argv().index("-name") + 1], "report")

    def test_empty_first_argument_is_not_silently_replaced_by_the_default(self):
        r = self.run_script("")
        self.assertEqual(r.returncode, 2)
        self.assertFalse(os.path.exists(os.path.join(self.root, "docs")))

    def test_bug_output_directory_that_starts_with_a_dash_is_accepted(self):
        for out in ["-x", "--help", "-"]:
            r = self.run_script(out)
            # BUG: the pattern allows a leading '-', so OUT is handed to `mkdir -p`, `[ -e ]` and mv as if it were an
            # option, and docker is started with a bind mount of a directory that was never created.
            self.assertEqual(r.returncode, 2, f"OUT={out!r} was not refused (exit {r.returncode}, docker called {self.calls()} times): {r.stderr}")

    def test_missing_prerequisites_stop_before_docker(self):
        for victim in [".env", ".env.images", "bin/linux/s0probe"]:
            path = os.path.join(self.root, victim)
            with open(path, "rb") as f:
                saved = f.read()
            os.unlink(path)
            r = self.run_script("out")
            self.assertEqual(r.returncode, 2, victim)
            self.assertEqual(self.calls(), 0)
            with open(path, "wb") as f:
                f.write(saved)
            os.chmod(path, 0o755)

    def test_non_executable_probe_binary_is_refused(self):
        os.chmod(os.path.join(self.root, "bin", "linux", "s0probe"), 0o644)
        r = self.run_script("out")
        self.assertEqual(r.returncode, 2)
        self.assertEqual(self.calls(), 0)

    def test_empty_or_missing_settings_stop_before_docker(self):
        for env_text, images in [
            ("AWS_ACCESS_KEY_ID=\nAWS_SECRET_ACCESS_KEY=x\n", None),
            ("AWS_ACCESS_KEY_ID=x\nAWS_SECRET_ACCESS_KEY=\n", None),
            ("AWS_ACCESS_KEY_ID=x\n", None),
            (None, "K3S_VERSION=v1.35.9-k3s1\n"),
            (None, "STATIC_IMAGE=img\n"),
            (None, "STATIC_IMAGE=\nK3S_VERSION=v1.35\n"),
        ]:
            if env_text is not None:
                self.write(".env", env_text)
            if images is not None:
                self.write(".env.images", images)
            r = self.run_script("out")
            self.assertNotEqual(r.returncode, 0, (env_text, images))
            self.assertEqual(self.calls(), 0, (env_text, images))
            self.write(".env", f"AWS_ACCESS_KEY_ID={ACCESS}\nAWS_SECRET_ACCESS_KEY={SECRET}\n")
            self.write(".env.images", "STATIC_IMAGE=gcr.io/distroless/static@sha256:abc\nK3S_VERSION=v1.35.9-k3s1\n")


class CredentialsAndHardening(RunScript):
    def test_credentials_are_never_in_argv_but_reach_the_container_by_name(self):
        for rc in (0, 1, 2):
            self.run_script("out", rc=rc, write="md json")
            args = self.argv()
            for a in args:
                self.assertNotIn(SECRET, a)
                self.assertNotIn(ACCESS, a)
                self.assertNotIn("sEcReT", a)
            joined = "\0".join(args)
            self.assertIn("-e\0AWS_ACCESS_KEY_ID\0-e\0AWS_SECRET_ACCESS_KEY", joined)
            with open(os.path.join(self.state, "secret_in_env.log")) as f:
                self.assertEqual(f.read().strip(), "1", "the secret must be in docker's environment so -e NAME can forward it")

    def test_credentials_never_reach_the_scripts_own_output(self):
        for rc in (0, 1, 2, 125):
            r = self.run_script("out", rc=rc)
            self.assertNotIn(SECRET, r.stdout + r.stderr)
            self.assertNotIn(ACCESS, r.stdout + r.stderr)

    def test_dollar_and_quote_characters_in_env_values_do_not_reach_argv_or_break_the_script(self):
        self.write(".env", "AWS_ACCESS_KEY_ID='AK IA'\nAWS_SECRET_ACCESS_KEY='p@ss $HOME \"q\" `id`'\n")
        r = self.run_script("out", write="md json")
        self.assertEqual(r.returncode, 0, r.stderr)
        for a in self.argv():
            self.assertNotIn("p@ss", a)

    def test_container_is_locked_down(self):
        self.run_script("out", write="md json")
        a = self.argv()
        self.assertEqual(a[0], "run")
        self.assertIn("--rm", a)
        self.assertIn("--read-only", a)
        self.assertEqual(a[a.index("--cap-drop") + 1], "ALL")
        self.assertEqual(a[a.index("--security-opt") + 1], "no-new-privileges")
        self.assertEqual(a[a.index("--user") + 1], f"{os.getuid()}:{os.getgid()}")
        self.assertEqual(a[a.index("--network") + 1], "meridian")
        self.assertEqual(a[a.index("--entrypoint") + 1], "/s0probe")
        self.assertNotIn("--privileged", a)
        self.assertNotIn("--cap-add", a)
        self.assertNotIn("-p", a)
        self.assertNotIn("--publish", a)
        self.assertNotIn("--pid=host", a)
        self.assertFalse([x for x in a if "docker.sock" in x], "the Docker socket must not be mounted")
        mounts = [a[i + 1] for i, x in enumerate(a) if x == "-v"]
        self.assertEqual(len(mounts), 2)
        self.assertTrue(any(m.endswith("bin/linux/s0probe:/s0probe:ro") for m in mounts))
        self.assertEqual(sum(1 for m in mounts if m.endswith(":/out")), 1)
        self.assertEqual(a[a.index("-endpoint") + 1], "http://aws-gateway:4566")
        self.assertIn("-eks-create", a)
        self.assertEqual(a[a.index("-out") + 1], "/out")

    def test_network_name_comes_from_meridian_net(self):
        self.run_script("out", write="md json", env={"MERIDIAN_NET": "meridian-gate"})
        a = self.argv()
        self.assertEqual(a[a.index("--network") + 1], "meridian-gate")

    def test_ami_parameter_uses_the_kubernetes_minor_of_the_pinned_k3s(self):
        for k3s, minor in [("v1.35.9-k3s1", "1.35"), ("1.36.0", "1.36"), ("v1.9.12+k3s2", "1.9"), ("v1.35", "1.35")]:
            self.write(".env.images", f"STATIC_IMAGE=img\nK3S_VERSION={k3s}\n")
            self.run_script("out", write="md json")
            a = self.argv()
            self.assertEqual(a[a.index("-ami-param") + 1], f"/aws/service/eks/optimized-ami/{minor}/amazon-linux-2023/x86_64/standard/recommended/image_id", k3s)

    def test_s0_label_is_passed_as_one_inert_argument(self):
        marker = os.path.join(self.base, "pwned")
        cases = {
            None: "S0 contract probe: floci",
            "": "S0 contract probe: floci",
            "moto": "S0 contract probe: moto",
            "a b  c": "S0 contract probe: a b  c",
            "x\nsecond": "S0 contract probe: x\nsecond",
            f"$(touch {marker})": f"S0 contract probe: $(touch {marker})",
            f"`touch {marker}`": f"S0 contract probe: `touch {marker}`",
            "'; touch " + marker + "; '": "S0 contract probe: '; touch " + marker + "; '",
            "-bogus": "S0 contract probe: -bogus",
            '"quoted"': 'S0 contract probe: "quoted"',
        }
        for label, want in cases.items():
            env = {} if label is None else {"S0_LABEL": label}
            r = self.run_script("out", write="md json", env=env)
            self.assertEqual(r.returncode, 0, (label, r.stderr))
            a = self.argv()
            self.assertEqual(a[a.index("-title") + 1], want, repr(label))
            self.assertFalse(os.path.exists(marker), f"S0_LABEL={label!r} was executed")

    def test_nothing_is_written_outside_the_output_directory(self):
        before = tree(self.root)
        self.run_script("sub/dir", write="md json")
        after = tree(self.root)
        for k in set(after) - set(before):
            self.assertTrue(k.startswith("sub/"), f"new path outside OUT: {k}")
        for k in before:
            self.assertEqual(before[k], after.get(k), k)
        self.assertEqual(os.listdir(self.base).count("outside-marker"), 0)
        self.assertEqual(sorted(os.listdir(os.path.join(self.root, "sub", "dir"))), ["report.json", "report.md"])


class ExitCodesAndReportHandling(RunScript):
    OLD_MD, OLD_JSON = "OLD MD\n", '{"old":true}\n'

    def seed_old(self, out="out"):
        self.write(f"{out}/report.md", self.OLD_MD)
        self.write(f"{out}/report.json", self.OLD_JSON)

    def leftovers(self, out="out"):
        return [n for n in os.listdir(os.path.join(self.root, out)) if n.startswith(".") or ".prev" in n or ".tmp" in n]

    def test_exit_code_table_with_a_new_report(self):
        table = {0: 0, 1: 0, 2: 2, 3: 3, 64: 64, 125: 125, 126: 126, 127: 127, 130: 130, 137: 137, 139: 139, 255: 255}
        for rc, want in table.items():
            self.seed_old()
            r = self.run_script("out", rc=rc, write="md json" if rc in (0, 1) else "")
            self.assertEqual(r.returncode, want, f"probe exit {rc}: {r.stderr}")
            self.assertEqual(self.leftovers(), [], f"rc={rc}")
            if rc in (0, 1):
                self.assertEqual(self.read("out/report.json"), '{"new":true}\n')
            else:
                # the probe could not run: the earlier report is put back untouched
                self.assertEqual(self.read("out/report.json"), self.OLD_JSON)
                self.assertEqual(self.read("out/report.md"), self.OLD_MD)

    def test_probe_exit_1_without_a_new_json_is_a_failure_and_the_old_report_returns(self):
        self.seed_old()
        r = self.run_script("out", rc=1, write="")
        self.assertEqual(r.returncode, 1)
        self.assertIn("wrote no new report", r.stderr)
        self.assertEqual(self.read("out/report.json"), self.OLD_JSON)
        self.assertEqual(self.leftovers(), [])

    def test_probe_exit_1_with_an_empty_json_is_a_failure(self):
        self.seed_old()
        # a probe that created an empty file
        self.write("out/.keep", "")
        r = self.run_script("out", rc=1, write="")
        self.assertEqual(r.returncode, 1)

    def test_a_stale_report_never_counts_as_the_new_one(self):
        self.seed_old()
        r = self.run_script("out", rc=1, write="md")  # md written, json not
        self.assertEqual(r.returncode, 1)
        self.assertEqual(self.read("out/report.json"), self.OLD_JSON)

    def test_bug_exit_0_without_a_new_report_is_reported_as_success(self):
        self.seed_old()
        r = self.run_script("out", rc=0, write="")
        # BUG: for exit 0 the script prints "probe passed; report in ..." without checking that a report was written,
        # then the EXIT trap restores the stale one. The header promises non-zero "when the probe ... wrote no new report".
        self.assertNotEqual(r.returncode, 0, f"stale report presented as a pass: {r.stdout}")

    def test_bug_mixed_old_and_new_report_when_only_the_json_is_written(self):
        self.seed_old()
        self.run_script("out", rc=1, write="json")
        # BUG: the md and json of a pair can come from different runs: the old md is restored next to the new json.
        self.assertEqual((self.read("out/report.md") == self.OLD_MD), (self.read("out/report.json") == self.OLD_JSON), "report.md and report.json come from different runs")

    def test_report_name_is_respected_and_other_names_are_untouched(self):
        self.write("out/report.md", "OTHER MD")
        self.write("out/report.json", "OTHER JSON")
        r = self.run_script("out", "report-moto", rc=1, write="md json", name="report-moto")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.read("out/report.md"), "OTHER MD")
        self.assertEqual(self.read("out/report.json"), "OTHER JSON")
        a = self.argv()
        self.assertEqual(a[a.index("-name") + 1], "report-moto")

    def test_running_twice_leaves_no_prev_files_and_replaces_the_report(self):
        for _ in range(3):
            r = self.run_script("out", rc=1, write="md json")
            self.assertEqual(r.returncode, 0)
            self.assertEqual(self.leftovers(), [])
        self.assertEqual(sorted(os.listdir(os.path.join(self.root, "out"))), ["report.json", "report.md"])

    def test_works_from_any_working_directory(self):
        elsewhere = os.path.join(self.base, "elsewhere")
        os.makedirs(elsewhere)
        r = self.run_script("out", rc=1, write="md json", cwd=elsewhere)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(os.path.exists(os.path.join(self.root, "out", "report.json")))
        self.assertEqual(os.listdir(elsewhere), [])

    def test_docker_missing_from_path_is_an_error_and_restores_the_old_report(self):
        self.seed_old()
        r = self.run_script("out", env={"PATH": "/usr/bin:/bin"})
        self.assertNotEqual(r.returncode, 0)
        self.assertEqual(self.read("out/report.json"), self.OLD_JSON)
        self.assertEqual(self.leftovers(), [])


if __name__ == "__main__":
    unittest.main()
