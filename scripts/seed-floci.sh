#!/usr/bin/env bash
# Seeds the data Karpenter expects directly into Floci: the node IAM role, the SQS
# interruption queue, and a VPC with two tagged subnets and a tagged security group; it checks the AMI exists.
# Seeding talks to Floci itself (not the gateway) because these setup calls are not part of the supported
# operation list; they are what a real account would already contain.
set -euo pipefail
cd "${BUILD_WORKSPACE_DIRECTORY:-$(dirname "$0")/..}"   # bazel run: the real workspace, not the runfiles tree
set -a; . ./.env; . ./.env.images; set +a
NET="${MERIDIAN_NET:-meridian}"   # the gate sets meridian-gate
export AWS_REGION=us-west-2
FLOCI="http://floci:4566"
CLUSTER=meridian-g0
AMI=ami-0a1b2c3d4e5f60718   # fallback ID, used only if image lookups are served by ec2-sim

# Runs awscall on the Compose network, where "floci" resolves. bin/linux/awscall is mounted read-only.
call() { docker run --rm --network "$NET" -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_REGION \
  -v "$PWD/bin/linux/awscall:/awscall:ro" --entrypoint /awscall "${STATIC_IMAGE}" -endpoint "$FLOCI" "$@"; }

# Kubernetes minor from the pinned k3s version, e.g. v1.32.4+k3s1 -> 1.32. Karpenter's alias "al2023@latest"
# resolves to an SSM path containing this minor.
MINOR=$(echo "$K3S_VERSION" | sed -E 's/^v?([0-9]+\.[0-9]+).*/\1/')

# 0. Wait until Floci answers: it reports healthy a moment before every service is ready, and a seed call made in
# that window fails (observed on the first run).
for i in $(seq 1 30); do
  call -service sts -version 2011-06-15 -action GetCallerIdentity >/dev/null 2>&1 && break
  [ "$i" = 30 ] && { echo "floci did not answer within 60s"; exit 1; }
  sleep 2
done

# 1. The AMI. S0 finding: Floci already serves the public AL2023 SSM parameter for this Kubernetes minor (it refuses
# writes under /aws/ like real AWS does) and ships a matching AMI, so there is nothing to register or write. Read the
# parameter and check EC2 can describe that exact image; this is idempotent. If S0 moved image lookups to ec2-sim,
# the fixed fallback ID is used instead.
if [ "$(python3 scripts/profiles.py --backend-of ec2:DescribeImages ops.yaml)" != "ec2-sim" ]; then
  AMI=$(call -service ssm -target AmazonSSM.GetParameter \
    -body "{\"Name\":\"/aws/service/eks/optimized-ami/${MINOR}/amazon-linux-2023/x86_64/standard/recommended/image_id\"}" \
    | sed -n 's/.*"Value":"\([^"]*\)".*/\1/p' | head -1)
  [ -n "$AMI" ] || { echo "FAIL: floci has no AL2023 SSM parameter for Kubernetes ${MINOR}"; exit 1; }
  # EC2 comes up a few seconds after STS on a cold start (observed), so retry before calling it a finding.
  for i in $(seq 1 30); do
    call -service ec2 -action DescribeImages "ImageId.1=${AMI}" 2>/dev/null | grep -q "<imageId>${AMI}</imageId>" && break
    [ "$i" = 30 ] && { echo "FAIL: ssm points at ${AMI} but ec2 DescribeImages cannot find it after 60s (S0 finding)"; exit 1; }
    sleep 2
  done
fi
echo "$AMI" > .ami-id

# 2. IAM: the node role (Karpenter creates the instance profile itself and attaches this role to it).
TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
call -service iam -version 2010-05-08 -action CreateRole RoleName=KarpenterNodeRole-${CLUSTER} \
  "AssumeRolePolicyDocument=${TRUST}" >/dev/null 2>&1 || true   # already exists on a re-run

# 3. SQS: the interruption queue.
call -service sqs -target AmazonSQS.CreateQueue -json-version 1.0 \
  -body "{\"QueueName\":\"${CLUSTER}-interruption\"}" >/dev/null

# 4. EC2 network: one VPC, two subnets in different zones, one security group, all tagged for Karpenter discovery.
# Re-runs create duplicates in Floci, so skip when the discovery-tagged subnets already exist.
if [ "$(python3 scripts/profiles.py --backend-of ec2:DescribeSubnets ops.yaml)" != "ec2-sim" ] && \
   ! call -service ec2 -action DescribeSubnets "Filter.1.Name=tag:karpenter.sh/discovery" "Filter.1.Value.1=${CLUSTER}" | grep -q '<subnetId>'; then
  VPC=$(call -service ec2 -action CreateVpc CidrBlock=10.0.0.0/16 | sed -n 's:.*<vpcId>\(.*\)</vpcId>.*:\1:p' | head -1)
  for spec in "10.0.1.0/24 ${AWS_REGION}a" "10.0.2.0/24 ${AWS_REGION}b"; do
    set -- $spec
    SUBNET=$(call -service ec2 -action CreateSubnet VpcId="$VPC" CidrBlock="$1" AvailabilityZone="$2" | sed -n 's:.*<subnetId>\(.*\)</subnetId>.*:\1:p' | head -1)
    call -service ec2 -action CreateTags "ResourceId.1=$SUBNET" Tag.1.Key=karpenter.sh/discovery "Tag.1.Value=${CLUSTER}" >/dev/null
  done
  SG=$(call -service ec2 -action CreateSecurityGroup GroupName=${CLUSTER}-nodes GroupDescription=nodes VpcId="$VPC" | sed -n 's:.*<groupId>\(.*\)</groupId>.*:\1:p' | head -1)
  call -service ec2 -action CreateTags "ResourceId.1=$SG" Tag.1.Key=karpenter.sh/discovery "Tag.1.Value=${CLUSTER}" >/dev/null
fi

# 6. The EKS cluster record. Karpenter reads its version and service CIDR. The probe's EKS check creates it
# (a 409 means it already exists) and waits until it is ACTIVE.
if [ -x bin/linux/s0probe ]; then
  docker run --rm --network "$NET" -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
    -v "$PWD/bin/linux/s0probe:/s0probe:ro" --entrypoint /s0probe "${STATIC_IMAGE}" \
    -endpoint "$FLOCI" -region us-west-2 -cluster "$CLUSTER" -groups eks -eks-create -wait 120s >/dev/null || \
    echo "note: creating the EKS cluster in Floci failed; S0 must record whether its EKS mock mode is needed"
else
  echo "note: bin/linux/s0probe is not built yet (Task 8 adds it); the probe creates the EKS cluster itself"
fi

echo "seeded floci: ami ${AMI} (k8s ${MINOR}), role KarpenterNodeRole-${CLUSTER}, queue ${CLUSTER}-interruption, network"
