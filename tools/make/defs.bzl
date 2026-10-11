"""Macros for the make-target equivalents."""

load("@io_bazel_rules_go//go:def.bzl", "go_cross_binary")
load("@rules_shell//shell:sh_binary.bzl", "sh_binary")

CMDS = [
    "aws-gateway",
    "awscall",
    "ec2-sim",
    "node-registrar",
    "pricing-facade",
    "s0probe",
    "simctl",
    "versionsenv",
]

def linux_binaries():
    """One static linux binary per cmd/, for the host CPU architecture."""
    for c in CMDS:
        go_cross_binary(
            name = c + "_linux",
            platform = select({
                "@platforms//cpu:arm64": "//tools/platforms:linux_arm64",
                "//conditions:default": "//tools/platforms:linux_amd64",
            }),
            target = "//cmd/%s:%s" % (c, c),
            visibility = ["//visibility:public"],
        )

def make_target(name, tools = {}, bin_sets = {}):
    """sh_binary for one Makefile target, run by `bazel run //tools/make:<name>` (aliased at //:<name>).

    tools maps MB_<key> to a label whose single runfiles path it carries; bin_sets maps MB_<key> to a
    filegroup whose binaries (all runfiles paths) the target copies out (build, build-linux).
    """
    env = {"MB_" + k: "$(rlocationpath %s)" % v for k, v in tools.items()}
    data = list(tools.values())
    for k, v in bin_sets.items():
        env["MB_" + k] = "$(rlocationpaths %s)" % v
        data.append(v)
    sh_binary(
        name = name,
        srcs = ["meridian.sh"],
        args = [name],
        data = data,
        env = env,
        visibility = ["//visibility:public"],
    )
