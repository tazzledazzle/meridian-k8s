"""gate-precheck.sh fails fast, with a clear message, when the Docker daemon has too little memory or CPU (A9)."""
import os
import subprocess
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "gate-precheck.sh")
GIB = 1024 ** 3


def run(mem_bytes, cpus, **extra):
    env = {"PATH": os.environ["PATH"], "GATE_DOCKER_MEM_BYTES": str(mem_bytes), "GATE_DOCKER_NCPU": str(cpus)}
    env.update(extra)
    return subprocess.run(["bash", SCRIPT], env=env, capture_output=True, text=True)


class Precheck(unittest.TestCase):
    def test_enough_resources_passes(self):
        r = run(8 * GIB, 8)
        self.assertEqual(0, r.returncode, r.stderr)
        self.assertIn("ok", r.stdout)

    def test_too_little_memory_fails_and_says_how_much_is_needed(self):
        r = run(1 * GIB, 8)
        self.assertEqual(1, r.returncode)
        self.assertIn("memory", r.stderr)
        self.assertIn("2048", r.stderr)  # the threshold, in MiB

    def test_too_few_cpus_fails(self):
        r = run(8 * GIB, 1)
        self.assertEqual(1, r.returncode)
        self.assertIn("CPU", r.stderr)

    def test_thresholds_can_be_raised(self):
        r = run(8 * GIB, 8, GATE_MIN_MEM_MB="16384")
        self.assertEqual(1, r.returncode)

    def test_exactly_the_threshold_passes(self):
        self.assertEqual(0, run(2048 * 1024 * 1024, 2).returncode)

    def test_garbage_values_fail_closed(self):
        self.assertNotEqual(0, run("lots", 8).returncode)
        self.assertNotEqual(0, run(8 * GIB, "").returncode)


if __name__ == "__main__":
    unittest.main()
