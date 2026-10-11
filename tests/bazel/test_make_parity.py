"""Every Makefile .PHONY target keeps a Bazel counterpart, and every script has a sh_binary.

Run by `bazel test //tests/bazel:test_make_parity` (the Makefile and BUILD files are test data).
The counterpart of make target T is the root-level Bazel target //:T (a runnable alias, or a test
target for test, test-scripts and lint-versions), so `make T` always maps to `bazel run|test //:T`.
"""
import os
import re
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, "..", ".."))

# How each target is invoked, so the documentation table cannot rot silently.
TEST_TARGETS = {"test", "test-scripts", "lint-versions"}


def read(*parts):
    with open(os.path.join(ROOT, *parts)) as f:
        return f.read()


def phony_targets():
    names = []
    for line in read("Makefile").splitlines():
        if line.startswith(".PHONY:"):
            names += line.split(":", 1)[1].split()
    if not names:
        raise AssertionError("Makefile has no .PHONY line")
    return names


def target_names(build_text):
    """Names declared literally (name = "x") or by the alias comprehension `for t in [ "x", ... ]`."""
    build_text = re.sub(r"#[^\n]*", "", build_text)  # commented-out targets do not count
    names = set(re.findall(r'\bname\s*=\s*"([^"]+)"', build_text))
    for body in re.findall(r"for t in \[(.*?)\]", build_text, re.S):
        names.update(re.findall(r'"([^"]+)"', body))
    return names


class MakeParity(unittest.TestCase):
    def test_phony_line_is_parsed(self):
        self.assertIn("s0-up", phony_targets())
        self.assertGreaterEqual(len(phony_targets()), 17)

    def test_every_phony_target_has_a_root_bazel_target(self):
        names = target_names(read("BUILD.bazel"))
        missing = [t for t in phony_targets() if t not in names]
        self.assertEqual([], missing, "make targets without //:<name> in BUILD.bazel")

    def test_every_runnable_target_is_wired_to_tools_make(self):
        root = read("BUILD.bazel")
        self.assertIn('actual = "//tools/make:" + t', root)
        made = target_names(read("tools", "make", "BUILD.bazel"))
        for t in phony_targets():
            if t not in TEST_TARGETS:
                self.assertIn(t, made, "//tools/make:%s missing" % t)

    def test_every_script_is_an_sh_binary(self):
        scripts = sorted(f[:-3] for f in os.listdir(os.path.join(ROOT, "scripts")) if f.endswith(".sh"))
        self.assertIn("s0-run", scripts)
        names = target_names(read("scripts", "BUILD.bazel"))
        # The sh_binary list is a comprehension over a dict literal: the keys are quoted names.
        listed = set(re.findall(r'^\s+"([a-z0-9-]+)":\s*[\[]', read("scripts", "BUILD.bazel"), re.M))
        missing = [s for s in scripts if s not in listed]
        self.assertEqual([], missing, "scripts without an sh_binary in scripts/BUILD.bazel")
        self.assertTrue(names)

    def test_documentation_table_lists_every_make_target(self):
        doc = read("docs", "build", "bazel.md")
        missing = [t for t in phony_targets() if not re.search(r"^\|\s*`make %s`" % re.escape(t), doc, re.M)]
        self.assertEqual([], missing, "docs/build/bazel.md lacks a row for these make targets")


if __name__ == "__main__":
    unittest.main()
