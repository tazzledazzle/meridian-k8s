I'll start with the brainstorming workflow to shape this into a coherent, incremental project.

Where should this platform actually run, given you want it to be credible on GitHub without a runaway AWS bill?

As close to 1 and 2, but instead of using real EKS, generalize a 1:1 parity cluster service that's home rolled or custom implement. Any AWS components that are defined should have either their open source equivalent implemented with an adapter between them and the code to "simulate" real AWS or creating the necessary component to "simulate" the real thing

Thought for 12s

What should the home-rolled 'EKS-parity' cluster service be?

Facade + simulated EC2 fleet for Karpenter
Thought for 6s
What language should the home-rolled services (EKS facade, EC2 simulator, platform CLI) use?
Go core + Python/Bash tooling
Thought for 4s
Which CI/CD and GitOps stack should the repo use as its primary pipeline?
GitHub Actions + Argo CD + Terraform (Recommended)
Thought for 11s
Which build sequencing do you want?
B: Walking skeleton, then deepen (Recommended)
Thought for 13s
Does the Architecture section look right?
Looks good
Thought for 14s
Mapping posting requirements to milestones and flagging two gaps
Does the Milestones and coverage section look right?
All mentioned tools integrated with facades for any AWS surface that normally would be used
Thought for 19s
Expanding roadmap with ECS facade and CI/CD parity milestones
Does the revised Milestones and coverage section look right?
Looks good
Thought for 12s
Does the Repo layout, fidelity, errors, and testing section look right?
Looks good

The design is approved, so I'm writing the spec now.

Self-review turned up one vague acceptance criterion in M4 ("a stated SLO"). I'm making it concrete.

Understood. I'm adding a time-boxed spike to the spec first, with a fallback to a custom component if no open-source alternative fits. The stress-test runs after that.

The spec now includes spike S1. Next I'm running the adversarial stress-test on it.

I'm using the stress-test skill to interrogate this design. `bd` isn't installed here and the workspace isn't a git repo, so I'm skipping the bead and the commit restore point. The spec is the file I'm editing inline (Mode A).

**Decision tree mapped: 10 branches**
1. Karpenter against a simulated EC2 (load-bearing assumption)
2. Terraform's real `aws` provider against the facades
3. Build vs. reuse for `eks-facade` (extend Moto?)
4. Istio multi-cluster across two k3d clusters on one Docker host
5. Total resource budget (one machine, and CI runners)
6. Security and risk: Docker socket exposure and credentials
7. Parity claims: what "1:1" can and can't prove without real AWS
8. Build effort and maintainability: hand-written protocols vs. generated from service models
9. Version pinning and drift (Karpenter, Istio, Argo, k3d)
10. Fallbacks when a milestone fails

---

## Branch 1: Karpenter against a simulated EC2

**The risk.** The whole plan assumes Karpenter's real AWS provider can run against `ec2-sim`. That provider calls far more than EC2: SSM parameters (AMI lookup), the pricing API, IAM instance profiles, SQS (interruption queue), `eks:DescribeCluster`, STS, launch templates, and fleet calls. After a "launch", Karpenter also waits for a Node to register, and a KWOK node isn't a kubelet bootstrapping the way an EC2 instance is. So `ec2-sim` needs a custom controller that creates the Node when an instance enters `running`. None of this is proven, and it's the centerpiece of the repo. The spec puts the spike at M2, after M0 and M1 have already been built on the assumption that it works.

**My recommendation.**
1. Move a 2-day feasibility probe into M0, before M1. Run real Karpenter against a recording proxy in front of Moto, with the AWS SDK's global endpoint override, and capture the exact set of calls it makes. That list becomes the `ec2-sim` scope and tells you early whether the plan is viable.
2. Write the fallback into the spec now: if the probe shows the surface is too large, switch to Karpenter's upstream KWOK cloud provider (no EC2 API needed) and keep `ec2-sim` as a smaller pricing, spot-event and fault-injection component. The Karpenter demos would then still work, but the "1:1 EC2 parity" claim would be dropped.

What's your alternative for Branch 1? For example: keep the spike at M2 as written, make the KWOK-provider fallback the primary path, drop the 1:1 EC2 parity goal, or something else. Tell me what you'd rather do and why, and I'll fold it into the spec.

I've asked for your alternative on Branch 1 and I'm waiting for your reply. Once you answer, I'll update the spec for this branch and re-check it with you. After that I'll continue through the remaining nine branches one at a time.