# The Gateway and ops.yaml

`aws-gateway` is the only AWS endpoint in the stack. It authenticates every request, works out which operation the request names, and forwards it to the backend that `ops.yaml` assigns to that operation. Source: `internal/gateway`, `internal/awsapi`, `internal/ops`.

## ops.yaml

`ops.yaml` lists 36 operations across ec2, pricing, eks, iam, sqs, ssm and sts. Each entry has five fields:

```yaml
- {service: ec2, name: CreateFleet, protocol: query, backend: floci, fidelity: behavior-inferred}
```

The gateway, the probe and the conformance report all read this file. `backend` names a `-backend name=url` flag. `fidelity` is one of `shape-validated`, `behavior-inferred`, `real-aws-verified`; every entry is currently `behavior-inferred`.

Validation fails if two operations from the coupled set (`ops.Coupled` in `internal/ops/ops.go`) point at different backends. Those operations read and write the same instance state, so they must share a backend. `DescribeInstanceTypes`, offerings, zones and spot prices hold no instance state and may move alone.

## The one-line backend swap

To move an operation, change its `backend:` value and make sure the gateway has a matching `-backend` flag. The gateway refuses to start if `ops.yaml` names a backend that no flag defines, so a typo fails at startup rather than as a 503. `scripts/s0matrix.py ... --apply` rewrites those lines from probe reports.

## SigV4 verification rules

`awsapi.Verify` runs before anything else, so unauthenticated callers learn nothing about operations or backends. It enforces:

- Exactly one `Authorization` and one `X-Amz-Date` header.
- A strict credential format: bounded length, restricted characters.
- `SignedHeaders` lowercase, sorted, without duplicates, containing `host` and `x-amz-date`, with every named header present.
- `X-Amz-Target` signed whenever it is sent (decision D10).
- The real body is hashed. `X-Amz-Content-Sha256` is ignored, so `UNSIGNED-PAYLOAD` requests fail.
- Timestamps within 15 minutes of the gateway clock.
- Constant-time signature comparison.

Failures map to AWS error codes such as `SignatureDoesNotMatch`, `RequestExpired` and `InvalidClientTokenId`. Error messages never contain request data or secrets. Request bodies are capped at 1 MiB.

## What the gateway refuses

| Request | Result | Reason |
|---|---|---|
| Service outside ec2, iam, sts, sqs, ssm, pricing, eks | `UnsupportedOperation`, even in trace mode (D11) | A closed list keeps the call surface known |
| Operation not in `ops.yaml` | `UnsupportedOperation` (forwarded only in trace mode) | Same |
| Zero, repeated or conflicting operation selectors | `InvalidAction` | The gateway and the backend could disagree about which operation runs |
| `X-Amz-Target` prefix that does not match the signed service | `InvalidAction` | An sqs-scoped request cannot name an ssm operation |
| `X-Amz-Target` on a query-protocol service | `InvalidAction` | Query services read `Action` only |
| `Connection`, `Proxy-Connection`, `Keep-Alive` headers | Dropped after verification | The reverse proxy deletes every header named in `Connection`, so a client could make the backend lose the signed `Authorization` header |
| Non-loopback listen address | Startup error unless `-allow-any-interface` | Avoids exposing an AWS control plane to the LAN |

The operation comes from exactly one place per protocol: `Action` for query services, `X-Amz-Target` for JSON services, method and path for eks.

## Trace and record modes

Both modes write `calls.golden` and `calls.json` to `-trace-out` (default `trace/raw`), flushed every 10 seconds.

- `-trace`: forwards operations missing from `ops.yaml` to the backend given by `-trace-route service=backend`, and records every call. Authentication still applies.
- `-record`: records every call but forwards nothing unsupported. The planned gate would use this.

In trace mode the gateway sees only eks `GET /clusters/{name}` and `POST /clusters` (accepted deviation B6, see [[Testing Strategy]]). `make trace-show` prints the recorded calls and marks any that `ops.yaml` lacks.
