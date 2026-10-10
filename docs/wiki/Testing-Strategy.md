# Testing Strategy

Tests come first, and a second author attacks the first author's work. Numbers below come from `go test ./... -count=1 -v` and the Python suite on this checkout; they will drift as code changes.

## Test first

Go code follows test-driven development: a failing test comes first. `make test` runs `go test ./...`. `make test-scripts` runs the Python tests for `profiles.py`, `s0matrix.py` and `s0-run.sh` with `-W error`, so warnings fail the run. `make lint-versions` fails if `versions.yaml` has an unpinned entry.

## Adversarial tester pattern

Decision D6: for each Go task, one author writes the code and tests, and a separate tester writes `adversarial_test.go` without sharing the author's assumptions. The tester's findings are numbered (B1, B2, and so on), fixed test-first, or recorded as accepted deviations. Files with this pattern exist for `awsapi`, `gateway`, `trace`, `versions`, `awscall`, `s0probe`, `cmd/aws-gateway`, `cmd/awscall`, `cmd/s0probe` and `cmd/versionsenv`, plus the Python scripts.

Infrastructure tasks that share the Docker daemon run inline, without a tester.

## Fuzz targets

12 fuzz targets guard parsers and the request path:

| Package | Targets |
|---|---|
| `internal/gateway` | `FuzzGateway` |
| `internal/awsapi` | `FuzzVerify` |
| `internal/trace` | `FuzzReadGolden` |
| `internal/awscall` | `FuzzBuildQuery`, `FuzzBuildREST` |
| `internal/s0probe` | `FuzzParseXML`, `FuzzGetPath`, `FuzzParsePrice`, `FuzzSummarizeJSON`, `FuzzSummarizeMarkdown` |
| `internal/versions` | `FuzzCheckEnv` |
| `internal/spot` | `FuzzEvent`, `FuzzEventKarpenterRoundTrip`, `FuzzInjectorValidateNeverPanics`, `FuzzValidatorsAgreeWithSpec` |
| `internal/simctl` | `FuzzRunArgs`, `FuzzProbeURL` |
| `internal/ec2sim` | `FuzzDescribeInstanceTypes`, `FuzzGlobMatchesRegexp`, `FuzzPagedWalkEqualsUnpaged`, `FuzzNextToken` |
| `internal/pricingfacade` | `FuzzGetProducts`, `FuzzRawRequest`, `FuzzTokenMutation`, `FuzzFilterValues` |
| `cmd/awscall` | `FuzzCLIArgs` |

A plain `go test` runs only their seed corpus. To fuzz one target for 30 seconds: `go test ./internal/awsapi -run '^$' -fuzz FuzzVerify -fuzztime 30s`.

## Accepted-deviation tests

An accepted deviation is a known gap that the team chose not to fix. Each has a skipped test whose skip message states the gap, so the decision stays visible (D15):

- `TestDeviationPathNotNormalisedForNonS3` (`internal/awsapi/sigv4_adversarial_test.go`, B1): the canonical path is signed as sent, like aws-sdk-go, and is not normalised.
- `TestDeviationTraceModeCannotObserveNonDescribeEKSCalls` (`internal/gateway/adversarial_test.go`, B6): trace mode sees only eks `DescribeCluster` and `CreateCluster`.
- `TestDeviationGoldenRoundTripWithNewlineInName` (`internal/trace/adversarial_test.go`, B6): unreachable through the gateway, which validates operation names first.

Other skips state their reason in the same way, for example tests that need `make`, `bash` or IPv6, or that would take about 10 seconds.

## Numbers you can verify

Run on this checkout, `go test ./... -count=1 -v`:

- On 2026-10-10: 384 top-level Go tests passed, 0 failed, 5 skipped at the top level (the skips are the accepted deviations).
- On 2026-10-10: the Python suite ran 115 tests, all OK.
- Per-package coverage from `criterion3.md`: `catalog` 100.0%, `fakeec2` 98.5%, `noderegistrar` 86.2%. `cmd/node-registrar/main.go` is thin wiring with no unit test.
- The pre-merge check for the registrar work was `go build ./... && go vet ./... && gofmt -l internal cmd && go test -race -count=1 ./...`.

## What tests cannot show

Unit tests use fakes: `internal/fakeec2` is a test-only EC2 double. The behavior of Floci and Karpenter is checked only by the live stack ([[Running It]]) and by the by-hand criteria in [[The S0 Spike]]. The automated gate that would run those criteria does not exist yet ([[Roadmap and Status]]).
