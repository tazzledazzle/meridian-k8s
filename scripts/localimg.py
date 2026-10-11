#!/usr/bin/env python3
"""Maps upstream image references to local names, for an offline cluster.

The gate runs on a Docker network with no route out, so the cluster cannot pull
images. images-load.sh pulls each image on the host, re-tags it with a local
name, and imports it into the cluster; the manifests are rewritten to use the
same local names so the pull policy never reaches a registry.

  localimg.py list     read manifests on stdin, print "<upstream ref> <local ref>" pairs
  localimg.py rewrite  read manifests on stdin, write them with image refs replaced
"""
import re
import sys

PREFIX = "meridian-local/"
# Images the project builds itself are already local; leave them alone.
SKIP = ("meridian/", PREFIX)
IMAGE_LINE = re.compile(r'^(\s*-?\s*image:\s*)["\']?([^\s"\']+)["\']?\s*$')


def local_name(ref: str) -> str:
    """public.ecr.aws/karpenter/controller:1.2.3@sha256:abcd... -> meridian-local/public.ecr.aws-karpenter-controller:1.2.3"""
    name, _, digest = ref.partition("@")
    repo, tag = name, ""
    # A colon after the last slash is a tag separator; before it, a registry port.
    last = name.rsplit("/", 1)[-1]
    if ":" in last:
        repo, tag = name.rsplit(":", 1)
    if not tag:
        tag = digest.split(":")[-1][:12] if digest else "latest"
    repo = re.sub(r"[^a-z0-9._-]+", "-", repo.lower()).strip("-")
    return f"{PREFIX}{repo}:{tag}"


def refs(text: str):
    for line in text.splitlines():
        m = IMAGE_LINE.match(line)
        if m and not m.group(2).startswith(SKIP):
            yield m.group(2)


def rewrite(text: str) -> str:
    out = []
    for line in text.splitlines():
        m = IMAGE_LINE.match(line)
        if m and not m.group(2).startswith(SKIP):
            line = m.group(1) + local_name(m.group(2))
        out.append(line)
    return "\n".join(out) + ("\n" if text.endswith("\n") else "")


def main(argv) -> int:
    if len(argv) != 2 or argv[1] not in ("list", "rewrite"):
        print(__doc__, file=sys.stderr)
        return 2
    text = sys.stdin.read()
    if argv[1] == "rewrite":
        sys.stdout.write(rewrite(text))
    else:
        for ref in sorted(set(refs(text))):
            print(ref, local_name(ref))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
