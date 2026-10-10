#!/usr/bin/env python3
"""Prints the Compose profiles to start: the distinct non-floci backends named in ops.yaml.

  COMPOSE_PROFILES=$(scripts/profiles.py ops.yaml) docker compose ... up -d
  scripts/profiles.py --backend-of ec2:DescribeImages ops.yaml     print the backend serving one operation

Profile names equal backend names (ec2-sim, pricing-facade, eks-facade, moto), so an operation moved to a
fallback in ops.yaml is the only thing that makes its service start.
"""
import re
import sys

BACKEND = re.compile(r"backend:\s*([a-z][a-z0-9-]*)")
OP = re.compile(r"service:\s*(\w+),\s*name:\s*(\w+),.*?backend:\s*([a-z][a-z0-9-]*)")


def strip_comments(text):
    """Drops whole-line and trailing '#' comments so a commented-out route is never read as a live one."""
    return "\n".join(line.split("#", 1)[0] for line in text.splitlines())


def profiles(text):
    return sorted({m.group(1) for m in BACKEND.finditer(strip_comments(text))} - {"floci"})


def backend_of(text, op):
    """Returns the backend that ops.yaml routes 'service:Name' to, or '' if the operation is not listed."""
    for m in OP.finditer(strip_comments(text)):
        if f"{m.group(1)}:{m.group(2)}" == op:
            return m.group(3)
    return ""


if __name__ == "__main__":
    if sys.argv[1] == "--backend-of":
        with open(sys.argv[3]) as f:
            print(backend_of(f.read(), sys.argv[2]))
    else:
        with open(sys.argv[1]) as f:
            print(",".join(profiles(f.read())))
