"""Service images: distroless static base (pinned by digest in MODULE.bazel, checked against versions.yaml)."""

load("@rules_oci//oci:defs.bzl", "oci_image", "oci_load")
load("@rules_pkg//pkg:mappings.bzl", "pkg_attributes", "pkg_files")
load("@rules_pkg//pkg:tar.bzl", "pkg_tar")

def service_image(name, cmd):
    """oci_image for cmd/<cmd>: /service (static, host-arch linux), /ops.yaml, user 65532, entrypoint /service.

    Mirrors deploy/Dockerfile.service. Targets: :<name> (image), :<name>_load (docker-loadable tarball,
    `bazel run :<name>_load` loads it into the local Docker daemon as meridian/<cmd>:bazel).
    """
    pkg_files(
        name = name + "_files",
        srcs = ["//tools/make:%s_linux" % cmd],
        attributes = pkg_attributes(mode = "0755"),
        renames = {"//tools/make:%s_linux" % cmd: "/service"},
    )
    pkg_tar(
        name = name + "_bin",
        srcs = [":" + name + "_files"],
    )
    oci_image(
        name = name,
        base = select({
            "@platforms//cpu:arm64": "@distroless_static_linux_arm64_v8",
            "//conditions:default": "@distroless_static_linux_amd64",
        }),
        entrypoint = ["/service"],
        user = "65532:65532",
        tars = [
            ":" + name + "_bin",
            ":ops_yaml_layer",
        ],
        visibility = ["//visibility:public"],
    )
    oci_load(
        name = name + "_load",
        image = ":" + name,
        repo_tags = ["meridian/%s:bazel" % cmd],
    )
