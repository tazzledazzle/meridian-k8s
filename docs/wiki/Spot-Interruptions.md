# Spot Interruptions

`simctl` simulates the two-minute warning that AWS sends before it reclaims a spot instance. No emulator models this event, so Meridian injects it with public AWS calls.

## What simctl does

`simctl interrupt` runs four steps through the gateway, each signed with SigV4:

1. `DescribeInstances` checks that the instance exists and is running.
2. `GetQueueUrl` and `SendMessage` post an EventBridge-shaped warning to the SQS queue that Karpenter watches (`meridian-g0-interruption`).
3. The command waits for the grace period.
4. `TerminateInstances` reclaims the instance.

It refuses an instance that is on-demand, pending, terminated or unknown, and it sends nothing in those cases. If the warning fails, it never terminates. If you cancel during the grace period, it exits with an error and leaves the instance running.

`-any-market` skips only the spot check. This stack needs it, because Floci's `DescribeInstances` omits `instanceLifecycle=spot` ([[Findings About Floci]]).

## The message

The warning must match what Karpenter's parser reads, or Karpenter ignores it without an error. The code follows Karpenter v1.14.1:

- The routing key is `version` `"0"`, `source` `"aws.ec2"` and `detail-type` `"EC2 Spot Instance Interruption Warning"`, matched exactly.
- The fields Karpenter uses are `detail.instance-id` and `detail.instance-action`.
- `time` must be RFC3339.

A test decodes the injector's output with Karpenter's own structs. A second test runs the real Karpenter parser on it. Both pass.

## What happened on the live stack

The run used a spot NodePool, a 45-second grace period and `-any-market`.

| Time (UTC) | Event |
|---|---|
| 21:56:22.209 | `simctl` starts and posts the warning |
| 21:56:22.339 | Karpenter logs `initiating delete from interruption message` (`spot_interrupted`, `CordonAndDrain`) |
| 21:56:22.351 | Karpenter taints the node |
| 21:56:25.595 | The replacement NodeClaim is initialized, and the evicted pod is rescheduled onto it |
| 21:56:28 | `node-registrar` removes the old Node because the instance terminated |
| 21:56:32.846 | Karpenter deletes the old NodeClaim |
| about 21:57:07 | `simctl`'s grace period ends |

Karpenter terminated the instance itself during the drain, about 35 seconds before the grace period ended. The garbage collector played no part. The full record is `docs/s0/criterion4.md` in the repository.

## Limits

- The run used a 45-second grace period. The real warning gives two minutes.
- It did not check that Karpenter marked the spot offering unavailable.
- It did not cover rebalance recommendations, scheduled changes or state-change events.
- The automated gate assertion for this criterion does not exist yet. See [[Roadmap and Status]].
