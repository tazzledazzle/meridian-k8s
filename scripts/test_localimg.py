import unittest

import localimg

KARPENTER = "public.ecr.aws/karpenter/controller:1.2.3@sha256:" + "a" * 64


class LocalName(unittest.TestCase):
    def test_tag_and_digest(self):
        self.assertEqual(localimg.local_name(KARPENTER), "meridian-local/public.ecr.aws-karpenter-controller:1.2.3")

    def test_digest_only_uses_a_short_digest_as_the_tag(self):
        self.assertEqual(localimg.local_name("registry.k8s.io/kwok/kwok@sha256:" + "b" * 64),
                         "meridian-local/registry.k8s.io-kwok-kwok:" + "b" * 12)

    def test_registry_port_is_not_mistaken_for_a_tag(self):
        self.assertEqual(localimg.local_name("localhost:5000/team/app:v1"), "meridian-local/localhost-5000-team-app:v1")

    def test_untagged_defaults_to_latest(self):
        self.assertEqual(localimg.local_name("busybox"), "meridian-local/busybox:latest")

    def test_two_digests_of_one_tag_get_distinct_names_only_when_tagless(self):
        a = localimg.local_name("x/y@sha256:" + "1" * 64)
        b = localimg.local_name("x/y@sha256:" + "2" * 64)
        self.assertNotEqual(a, b)


MANIFEST = f"""\
containers:
  - name: controller
    image: {KARPENTER}
  - name: sidecar
    image: "registry.k8s.io/kwok/kwok:v0.7.0"
  - name: mine
    image: meridian/node-registrar:g0
  - name: done
    image: meridian-local/already:1
"""


class Rewrite(unittest.TestCase):
    def test_list_skips_project_and_local_images(self):
        pairs = sorted(set(localimg.refs(MANIFEST)))
        self.assertEqual(pairs, sorted([KARPENTER, "registry.k8s.io/kwok/kwok:v0.7.0"]))

    def test_rewrite_changes_only_upstream_images_and_keeps_indentation(self):
        out = localimg.rewrite(MANIFEST)
        self.assertIn("    image: meridian-local/public.ecr.aws-karpenter-controller:1.2.3\n", out)
        self.assertIn("    image: meridian-local/registry.k8s.io-kwok-kwok:v0.7.0\n", out)
        self.assertIn("    image: meridian/node-registrar:g0\n", out)
        self.assertIn("    image: meridian-local/already:1\n", out)
        self.assertNotIn("public.ecr.aws/karpenter", out)

    def test_rewrite_is_idempotent(self):
        once = localimg.rewrite(MANIFEST)
        self.assertEqual(localimg.rewrite(once), once)

    def test_trailing_newline_is_preserved_either_way(self):
        self.assertTrue(localimg.rewrite("image: a/b:1\n").endswith("\n"))
        self.assertFalse(localimg.rewrite("image: a/b:1").endswith("\n"))

    def test_commented_image_lines_are_not_rewritten(self):
        self.assertEqual(localimg.rewrite("# image: a/b:1\n"), "# image: a/b:1\n")


if __name__ == "__main__":
    unittest.main()
