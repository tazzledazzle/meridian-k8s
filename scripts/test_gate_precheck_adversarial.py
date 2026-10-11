"""Adversarial tests for the gate's shell guards (bead meridian-17s.10, independent tester).

Covers gate-precheck.sh (A9), gate-check-offline.sh, check-no-sock.sh (A1) and images-load.sh. Nothing here touches
Docker: scripts that call docker/kubectl are run against a fake workspace (BUILD_WORKSPACE_DIRECTORY) and a fake
`docker` first on PATH.

Tests that pin a real defect are marked @unittest.expectedFailure with the finding id; when the script is fixed the
test starts to "unexpectedly succeed" and fails the suite, so the marker must then be removed.
"""
import os
import stat
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
PRECHECK = os.path.join(HERE, "gate-precheck.sh")
OFFLINE = os.path.join(HERE, "gate-check-offline.sh")
NOSOCK = os.path.join(HERE, "check-no-sock.sh")
IMAGES_LOAD = os.path.join(HERE, "images-load.sh")
GIB = 1024 ** 3
SYSTEM_PATH = "/usr/bin:/bin:/usr/sbin:/sbin"


def precheck(mem, cpus, **extra):
    env = {"PATH": SYSTEM_PATH, "GATE_DOCKER_MEM_BYTES": str(mem), "GATE_DOCKER_NCPU": str(cpus)}
    env.update(extra)
    return subprocess.run(["bash", PRECHECK], env=env, capture_output=True, text=True, timeout=30)


def executable(path, body):
    with open(path, "w") as f:
        f.write("#!/bin/sh\n" + body)
    os.chmod(path, os.stat(path).st_mode | stat.S_IXUSR)


class PrecheckEnvEdgeCases(unittest.TestCase):
    def assertRejected(self, r, msg=""):
        self.assertNotEqual(0, r.returncode, (msg, r.stdout, r.stderr))

    def test_hex_negative_float_exponent_and_whitespace_are_refused_with_a_clear_message(self):
        for bad in ["0x200000000", "-8589934592", "+8589934592", "8e9", "8589934592.0", " 8589934592",
                    "8589934592 ", "85899 34592", "8589934592\n", "\t8589934592", "٨٥٨٩٩٣٤٥٩٢"]:
            r = precheck(bad, 4)
            self.assertRejected(r, repr(bad))
            self.assertIn("cannot read Docker's memory/CPU limits", r.stderr, repr(bad))
            r = precheck(8 * GIB, bad)
            self.assertRejected(r, repr(bad))
            self.assertIn("cannot read Docker's memory/CPU limits", r.stderr, repr(bad))

    def test_empty_overrides_fail_closed(self):
        self.assertRejected(precheck("", 4))
        self.assertRejected(precheck(8 * GIB, ""))
        self.assertRejected(precheck("", ""))

    def test_zero_is_below_every_floor(self):
        self.assertRejected(precheck(0, 4))
        self.assertRejected(precheck(8 * GIB, 0))

    def test_boundary_is_inclusive_at_the_floor_and_exclusive_one_byte_below_in_mib_steps(self):
        self.assertEqual(0, precheck(2048 * 1024 * 1024, 2).returncode)
        self.assertEqual(0, precheck(2048 * 1024 * 1024 + 1024 * 1024 - 1, 2).returncode)  # floor division
        self.assertRejected(precheck(2048 * 1024 * 1024 - 1, 2))
        self.assertRejected(precheck(8 * GIB, 1))

    def test_leading_zeros_never_make_a_value_look_bigger(self):
        # "08..." is an invalid octal for bash arithmetic: the script aborts (fail closed, raw bash error).
        self.assertRejected(precheck("08589934592", 4))
        # a valid-octal prefix is read as octal: smaller than the decimal reading, so stricter, never laxer.
        self.assertRejected(precheck("010000000000", 4))  # decimal 10 GB, octal 1 GiB -> refused
        # CPU strings go through `[ -lt ]` (decimal), so zero padding is harmless there
        self.assertEqual(0, precheck(8 * GIB, "0004").returncode)
        self.assertEqual(0, precheck(8 * GIB, "010").returncode)
        self.assertRejected(precheck(8 * GIB, "01"))

    def test_values_that_wrap_int64_fail_closed(self):
        self.assertRejected(precheck(2 ** 63, 4))  # wraps negative
        self.assertRejected(precheck(2 ** 64, 4))  # wraps to 0

    def test_int64_max_is_accepted_as_a_big_machine(self):
        self.assertEqual(0, precheck(2 ** 63 - 1, 4).returncode)

    @unittest.expectedFailure
    def test_F_P1_mem_value_that_wraps_around_to_a_plausible_size_is_accepted(self):
        # FINDING P1 (low, unrealistic input): 2**64 + 8 GiB wraps to 8 GiB in $(( )) and passes. A daemon never
        # reports this; it shows the digit-only check is not a magnitude check.
        self.assertRejected(precheck(2 ** 64 + 8 * GIB, 4))

    @unittest.expectedFailure
    def test_F_P2_an_over_int64_floor_override_disables_the_check(self):
        # FINDING P2 (low): `[ 8192 -lt 99999999999999999999 ]` is an error (status 2), `if` treats it as false, so a
        # nonsense floor makes the guard pass instead of fail. Also prints a stray bash error. Fail-open on bad input.
        self.assertRejected(precheck(1 * GIB, 8, GATE_MIN_MEM_MB="99999999999999999999"))
        self.assertRejected(precheck(8 * GIB, 1, GATE_MIN_CPUS="99999999999999999999"))

    def test_floor_overrides_are_validated_like_the_readings(self):
        for bad in ["0x10", " 5", "-1", "1.5", "5\n"]:
            self.assertRejected(precheck(8 * GIB, 8, GATE_MIN_MEM_MB=bad), bad)
            self.assertRejected(precheck(8 * GIB, 8, GATE_MIN_CPUS=bad), bad)

    def test_an_empty_floor_override_falls_back_to_the_default(self):
        self.assertRejected(precheck(1 * GIB, 8, GATE_MIN_MEM_MB=""))
        self.assertRejected(precheck(8 * GIB, 1, GATE_MIN_CPUS=""))

    def test_a_leftover_zero_floor_silently_disables_the_guard(self):
        # Documents (not a defect of the script, a hazard of the env-var API): GATE_MIN_*=0 in the caller's shell
        # lets a 1-byte daemon pass. The pass line prints the floor, so it is at least visible in the log.
        r = precheck(1, 1, GATE_MIN_MEM_MB="0", GATE_MIN_CPUS="0")
        self.assertEqual(0, r.returncode)
        self.assertIn("floor 0 MiB, 0 CPUs", r.stdout)

    def test_without_overrides_it_asks_docker_and_a_failing_docker_is_a_failure(self):
        with tempfile.TemporaryDirectory() as d:
            executable(os.path.join(d, "docker"), "exit 1\n")
            r = subprocess.run(["bash", PRECHECK], env={"PATH": d + ":" + SYSTEM_PATH}, capture_output=True, text=True, timeout=30)
            self.assertNotEqual(0, r.returncode)

    def test_docker_info_garbage_or_empty_is_a_failure(self):
        for out in ["", "<no value>", "unknown", "-1", "8 GiB"]:
            with tempfile.TemporaryDirectory() as d:
                executable(os.path.join(d, "docker"), "echo '%s'\n" % out)
                r = subprocess.run(["bash", PRECHECK], env={"PATH": d + ":" + SYSTEM_PATH}, capture_output=True, text=True, timeout=30)
                self.assertNotEqual(0, r.returncode, out)

    def test_docker_info_good_values_pass(self):
        with tempfile.TemporaryDirectory() as d:
            # --format '{{.MemTotal}}' then '{{.NCPU}}': a fake that answers by the format string
            executable(os.path.join(d, "docker"),
                       'case "$*" in *MemTotal*) echo 8589934592;; *NCPU*) echo 4;; esac\n')
            r = subprocess.run(["bash", PRECHECK], env={"PATH": d + ":" + SYSTEM_PATH}, capture_output=True, text=True, timeout=30)
            self.assertEqual(0, r.returncode, r.stderr)


def fake_workspace(d, kc_body=None, localimg_body=None):
    os.makedirs(os.path.join(d, "scripts"))
    os.makedirs(os.path.join(d, "bin"))
    if kc_body is not None:
        executable(os.path.join(d, "scripts", "kc.sh"), kc_body)
    if localimg_body is not None:
        with open(os.path.join(d, "scripts", "localimg.py"), "w") as f:
            f.write(localimg_body)


def run_script(script, d, extra_path=None, args=()):
    env = {"PATH": (extra_path + ":" if extra_path else "") + SYSTEM_PATH, "BUILD_WORKSPACE_DIRECTORY": d}
    return subprocess.run(["bash", script, *args], env=env, capture_output=True, text=True, timeout=30)


class OfflineCheck(unittest.TestCase):
    def kc(self, body):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", d]))
        fake_workspace(d, kc_body=body)
        return run_script(OFFLINE, d)

    def test_clean_event_list_passes(self):
        r = self.kc("echo 'kube-system 1m Normal Pulled pod/x Container image already present on machine'\n")
        self.assertEqual(0, r.returncode, r.stderr)

    def test_every_pull_flavour_fails(self):
        for ev in ["default 5s Normal Pulling pod/x Pulling image \"a/b\"",
                   "default 5s Warning Failed pod/x Error: ErrImagePull",
                   "default 5s Warning Failed pod/x Error: ImagePullBackOff",
                   "default 5s Normal BackOff pod/x Back-off pulling image \"a/b\""]:
            r = self.kc("echo '%s'\n" % ev)
            self.assertEqual(1, r.returncode, ev)
            self.assertIn("tried to pull", r.stderr)

    @unittest.expectedFailure
    def test_F_O1_an_unreachable_cluster_is_reported_as_no_pull_attempted(self):
        # FINDING O1 (medium): `events=$(kc get events ... | grep ... || true)` swallows a kubectl failure, so when
        # the cluster cannot be queried the script prints "ok: no image pull was attempted" and exits 0.
        r = self.kc("echo 'The connection to the server was refused' >&2; exit 1\n")
        self.assertNotEqual(0, r.returncode, r.stdout)


class NoSockCheck(unittest.TestCase):
    def rendered(self, text):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", d]))
        fake_workspace(d)
        executable(os.path.join(d, "bin", "docker"), "cat <<'EOF'\n%s\nEOF\n" % text)
        return run_script(NOSOCK, d, extra_path=os.path.join(d, "bin"), args=["x.yaml"])

    def test_direct_socket_mount_fails(self):
        r = self.rendered("services:\n  a:\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock")
        self.assertEqual(1, r.returncode)

    def test_no_mount_passes(self):
        self.assertEqual(0, self.rendered("services:\n  a:\n    image: x").returncode)

    def test_long_form_mount_fails(self):
        r = self.rendered("volumes:\n  - type: bind\n    source: /var/run/docker.sock\n    target: /s")
        self.assertEqual(1, r.returncode)

    @unittest.expectedFailure
    def test_F_N1_parent_directory_bind_exposes_the_socket_but_passes(self):
        # FINDING N1 (medium, same class as Go finding GN-2): a bind of /var/run or /run contains docker.sock, but
        # the rendered config never spells "docker.sock", so the A1 grep passes it.
        r = self.rendered("services:\n  a:\n    volumes:\n      - /var/run:/host-run")
        self.assertEqual(1, r.returncode)

    @unittest.expectedFailure
    def test_F_N2_other_runtime_sockets_pass(self):
        r = self.rendered("services:\n  a:\n    volumes:\n      - /run/podman/podman.sock:/s\n      - /run/containerd/containerd.sock:/c")
        self.assertEqual(1, r.returncode)

    def test_a_failing_compose_render_fails_the_check(self):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", d]))
        fake_workspace(d)
        executable(os.path.join(d, "bin", "docker"), "echo 'invalid compose file' >&2; exit 15\n")
        r = run_script(NOSOCK, d, extra_path=os.path.join(d, "bin"), args=["x.yaml"])
        self.assertNotEqual(0, r.returncode)  # a render error must not read as "no socket"


class ImagesLoad(unittest.TestCase):
    @unittest.expectedFailure
    def test_F_I1_a_failing_manifest_lister_loads_nothing_and_still_succeeds(self):
        # FINDING I1 (low, caught later by rollout status/offline check): the loop reads from a process
        # substitution `< <(... localimg.py list)`; set -e does not see that command's failure, so when the lister
        # dies the script exits 0 having loaded no image.
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", d]))
        fake_workspace(d, localimg_body="import sys\nsys.exit(3)\n")
        executable(os.path.join(d, "bin", "docker"), "exit 0\n")
        executable(os.path.join(d, "bin", "k3d"), "exit 0\n")
        r = subprocess.run(["bash", IMAGES_LOAD, "c"], input="kind: Pod\n", env={
            "PATH": os.path.join(d, "bin") + ":" + SYSTEM_PATH, "BUILD_WORKSPACE_DIRECTORY": d},
            capture_output=True, text=True, timeout=30)
        self.assertNotEqual(0, r.returncode)


if __name__ == "__main__":
    unittest.main()
