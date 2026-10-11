# Building Meridian with Bazel

Bazel (via bazelisk, pinned by `.bazelversion` = 9.3.0) builds, tests and runs everything `make` does.
`make` keeps working unchanged; each make target has a Bazel equivalent at the repo root.

```sh
bazel build //...        # every Go binary, Python library, image
bazel test //...         # 22 Go test packages + 5 Python test files (116 tests) + the make-parity test
```

`versions.yaml` is still the single source of pins. Two literals bzlmod forces into `MODULE.bazel`
(the Go SDK version and the distroless base image digest) are checked against `versions.yaml` by
`bazel test //:lint-versions` (`internal/versions/modulepins.go`), so they cannot drift silently.

## make to Bazel

Runnable targets are `bazel run //:<name> [-- args]`; they act on the real workspace (`.env`, `bin/`,
`deploy/`, `docs/s0/`) exactly like make, not on Bazel's runfiles tree.

| make | Bazel | Notes |
|---|---|---|
| `make init` | `bazel run //:init` | runs `scripts/init-env.sh .env` |
| `make test` | `bazel test //:test` | same 22 packages as `go test ./...` (`bazel test //...` also runs them) |
| `make test-scripts` | `bazel test //:test-scripts` | 5 `py_test` files; `PYTHONWARNINGS=error` mirrors `python3 -W error` |
| `make lint-versions` | `bazel test //:lint-versions` | `TestRepoVersionsFileIsValid` plus the MODULE.bazel pin check; `versions.yaml` is test data |
| `make env-images` | `bazel run //:env-images` | atomically writes `.env.images`; `bazel build //:env_images_file` gives `bazel-bin/.env.images` |
| `make build` | `bazel run //:build` | copies host binaries to `bin/` |
| `make build-linux` | `bazel run //:build-linux` | static linux binaries (CGO off, trimpath, host CPU arch) into `bin/linux/`; `bazel build //tools/make:linux_bins` only builds them |
| `make check-sock` | `bazel run //:check-sock` | |
| `make s0-up` | `bazel run //:s0-up` | init, env-images, build-linux, check-sock, then compose up, seed, k3d-up, karpenter-install |
| `make s0-down` | `bazel run //:s0-down` | |
| `make seed` | `bazel run //:seed` | build-linux first |
| `make trace-show` | `bazel run //:trace-show` | |
| `make s0-probe` | `bazel run //:s0-probe` | build-linux first |
| `make s0-probe-moto` | `bazel run //:s0-probe-moto` | build-linux first; `S0_LABEL=moto` |
| `make s0-decide` | `bazel run //:s0-decide` | `APPLY=1 make s0-decide` is `bazel run //:s0-decide -- --apply` (or `APPLY=1 bazel run ...`) |
| `make kwok-up` | `bazel run //:kwok-up` | env-images first |
| `make vendor-kwok` | `bazel run //:vendor-kwok` | env-images first; needs a network |
| `make deploy-registrar` | `bazel run //:deploy-registrar` | init and env-images first |

`PATH` (with `bin/` first, so the checksum-verified `bin/k3d` is found), `MERIDIAN_UID`/`MERIDIAN_GID` and the
`docker compose --env-file ...` invocation are set by `tools/make/meridian.sh`, the dispatcher behind every
runnable target above, as the Makefile exports them. As in the Makefile, a system `k3d` wins over `bin/k3d`.

### Every script is a `sh_binary`

`bazel run //scripts:<name> -- args` for `init-env`, `check-no-sock`, `seed-floci`, `k3d-up`,
`karpenter-install`, `kc`, `kwok-up`, `vendor-kwok`, `deploy-registrar`, `trace-show`, `s0-run`. Running one
directly skips the dispatcher's environment (PATH, UID/GID); prefer the `//:` targets for the docker/k3d ones.
`profiles.py` and `s0matrix.py` are `py_library` (tests) and `py_binary` (`//scripts:profiles_tool`,
`//scripts:s0matrix_tool`, used by `s0-up` and `s0-decide`). `bazel query 'kind(sh_binary, //...)'` lists them.

## Service images

`bazel build //tools/images:aws-gateway` (also `ec2-sim`, `pricing-facade`, `node-registrar`) builds an OCI
image equivalent to `deploy/Dockerfile.service`: `/service` (static linux binary built by Bazel with the pinned
Go SDK), `/ops.yaml`, `USER 65532:65532`, `ENTRYPOINT ["/service"]`, on `gcr.io/distroless/static-debian12`
pinned by digest. `bazel run //tools/images:aws-gateway_load` loads it into the local Docker daemon as
`meridian/aws-gateway:bazel`. Compose still builds from `deploy/Dockerfile.service`; migrating it to these images
is a separate step.

## Offline use

The first build downloads Bazel itself, the Go SDK, Go modules, Python and the base image. After that:

```sh
bazel fetch //...                                   # populate the repository cache while online
bazel build //... --distdir=$HOME/.cache/bazel-dist --repository_cache=$HOME/.cache/bazel-repo
bazel build //... --nofetch                         # after a fetch: fail instead of touching the network
```

Put the flags you want in the (git-ignored) `user.bazelrc`. `go.sum` and `MODULE.bazel.lock` are committed;
Go modules come from `go.mod` via `go_deps.from_file`.

## Deviations worth knowing

* Bazel builds Go with the pinned SDK 1.24.13 (`versions.yaml` golang); `make` uses whatever `go` is installed,
  so binaries are not byte-identical between the two systems.
* gazelle and rules_go pull `golang.org/x/{sync,mod,sys,tools}` versions that need Go 1.25. `MODULE.bazel` pins
  older archives of those four for the build tooling only (the `go_deps.archive_override` entries).
* Tests that run `go build` / `make` (`cmd/simctl`, `cmd/versionsenv`) are tagged `local`, `no-sandbox`: they need
  `go` and `make` on `PATH`.
* `make env-images` and `make build` stay native: `cmd/versionsenv/makefile_test.go` asserts their recipes.
