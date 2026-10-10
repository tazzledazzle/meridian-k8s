import unittest

import profiles


class ProfilesTests(unittest.TestCase):
    def test_floci_only_starts_no_fallbacks(self):
        self.assertEqual(profiles.profiles("- {service: ec2, name: A, protocol: query, backend: floci, fidelity: x}"), [])

    def test_distinct_sorted_and_floci_excluded(self):
        text = "\n".join(
            f"- {{service: s, name: {n}, protocol: query, backend: {b}, fidelity: x}}"
            for n, b in [("A", "floci"), ("B", "moto"), ("C", "ec2-sim"), ("D", "moto")]
        )
        self.assertEqual(profiles.profiles(text), ["ec2-sim", "moto"])


class BackendOfTests(unittest.TestCase):
    TEXT = ("- {service: ec2, name: DescribeImages, protocol: query, backend: ec2-sim, fidelity: x}\n"
            "- {service: ec2, name: CreateFleet, protocol: query, backend: floci, fidelity: x}\n")

    def test_finds_the_backend_of_one_operation(self):
        self.assertEqual(profiles.backend_of(self.TEXT, "ec2:DescribeImages"), "ec2-sim")
        self.assertEqual(profiles.backend_of(self.TEXT, "ec2:CreateFleet"), "floci")

    def test_unknown_operation_gives_empty(self):
        self.assertEqual(profiles.backend_of(self.TEXT, "ec2:Nope"), "")

    # A comment line must not be read as a route (a commented-out ec2-sim line would start a service).
    def test_commented_out_lines_are_ignored(self):
        text = "# - {service: ec2, name: X, protocol: query, backend: ec2-sim, fidelity: x}\n"
        self.assertEqual(profiles.profiles(text), [])
        self.assertEqual(profiles.backend_of(text, "ec2:X"), "")


if __name__ == "__main__":
    unittest.main()
