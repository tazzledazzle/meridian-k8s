# Glossary

**backend**: A service that answers AWS calls for the operations `ops.yaml` assigns to it. Today only Floci runs; `ec2-sim` and `pricing-facade` are declared but unused. See [[The Gateway and ops.yaml]].

**coupled operations**: EC2 operations that read and write the same instance state, such as `CreateFleet` and `DescribeInstances`. They must share one backend. The list is `ops.Coupled` in `internal/ops/ops.go`.

**EC2NodeClass**: A Karpenter resource that says how to launch instances: AMI, subnets, security groups, instance profile role. See `deploy/workloads/nodepool.yaml`.

**eligible vs proven**: A probe pass makes a backend eligible for an operation. It is proven only when Karpenter, the oracle, works against it. See [[The S0 Spike]].

**Floci**: A local AWS emulator, pinned at 2.2.0. The default backend for all 36 operations.

**KWOK**: Kubernetes WithOut Kubelet. A controller that keeps annotated Nodes Ready and sets pods Running without running containers. Pinned at v0.8.0.

**mock mode**: A Floci setting (`FLOCI_SERVICES_EC2_MOCK`, `FLOCI_SERVICES_EKS_MOCK`) that makes a service metadata-only, so it starts no containers. See [[Findings About Floci]].

**node-registrar**: The controller that creates a Kubernetes Node for each running simulated instance. See [[Nodes Without Machines]].

**NodeClaim**: Karpenter's request for one node. It becomes Launched, Registered, Initialized and Ready.

**NodePool**: A Karpenter resource that sets the constraints for nodes it may create, such as instance types, architecture and CPU limits.

**oracle**: The component whose behavior decides correctness. Here, Karpenter.

**ops.yaml**: The list of supported AWS operations and the backend for each. The gateway, the probe and the report all read it.

**SigV4**: AWS Signature Version 4, the request signing scheme. The gateway verifies it on every request.

**trace mode**: A gateway mode that records every call and forwards unlisted operations. See [[The Gateway and ops.yaml]].
