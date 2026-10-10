# S0 contract probe: floci

35 passed, 2 failed, 0 skipped.

| Check | Group | Status | Detail |
|---|---|---|---|
| sts:GetCallerIdentity | sts | pass | account 000000000000 |
| ssm:PutParameter | ssm | pass | /s0probe/8b14783e |
| ssm:GetParameter | ssm | pass | value round-trips |
| ssm:GetParameter (AMI alias path) | ssm | pass | ami-0abcdef1234567891 |
| iam:CreateRole | iam | pass | s0probe-role-8b14783e |
| iam:GetRole | iam | pass | arn:aws:iam::000000000000:role/s0probe-role-8b14783e |
| iam:CreateInstanceProfile | iam | pass | s0probe-profile-8b14783e |
| iam:AddRoleToInstanceProfile | iam | pass |  |
| iam:GetInstanceProfile | iam | pass | roles s0probe-role-8b14783e |
| iam:ListInstanceProfiles | iam | pass | 2 profiles |
| iam:TagInstanceProfile | iam | pass |  |
| sqs:CreateQueue | sqs | pass | http://floci:4566/000000000000/s0probe-8b14783e |
| sqs:GetQueueUrl | sqs | pass | http://floci:4566/000000000000/s0probe-8b14783e |
| sqs:SendMessage | sqs | pass |  |
| sqs:ReceiveMessage | sqs | pass | body round-trips |
| sqs:DeleteMessage | sqs | pass |  |
| eks:CreateCluster | eks | pass | already exists (409); creation itself was not exercised |
| eks:DescribeCluster | eks | pass | version 1.35 |
| ec2:DescribeSubnets (discovery tag) | ec2 | pass | 2 subnets |
| ec2:DescribeSecurityGroups (discovery tag) | ec2 | pass | sg-d7105a0a40f66f50f |
| ec2:DescribeImages | ec2 | pass | x86_64 |
| ec2:CreateLaunchTemplate | ec2 | pass | lt-48139b19103936947 |
| ec2:CreateFleet | ec2 | pass | i-d28f3b5ea2b2975d6 (m5.large) |
| ec2:DescribeLaunchTemplates | ec2 | pass | by name and by tag |
| ec2:DescribeInstances (running, tagged) | ec2 | pass | running at 10.0.1.15; NOTE: no instanceLifecycle=spot (spot-only checks need simctl -any-market) |
| ec2:DescribeInstanceStatus | ec2 | pass | state running, instance status "ok", system status "ok" |
| ec2:CreateTags | ec2 | pass | tag visible |
| ec2:RunInstances | ec2 | pass | i-98d58f39d5284276e |
| ec2:TerminateInstances | ec2 | pass | shutting-down,shutting-down |
| ec2:DeleteLaunchTemplate | ec2 | pass |  |
| ec2:DescribeAvailabilityZones | ec2 | pass | us-west-2a,us-west-2b,us-west-2c |
| ec2:DescribeInstanceTypes | ec2 | fail | no networkInfo.maximumNetworkInterfaces (pod density is computed from it) |
| ec2:DescribeInstanceTypeOfferings | ec2 | pass | 19 offerings in us-west-2a |
| ec2:DescribeSpotPriceHistory | ec2 | pass | 3 prices, first 0.012800 |
| pricing:GetProducts | pricing | fail | no on-demand price for m5.large in us-west-2 (the backend's price data does not cover this region or type) |
| iam:RemoveRoleFromInstanceProfile | iam | pass |  |
| iam:DeleteInstanceProfile | iam | pass |  |

Groups needing a custom backend or a recorded gap: ec2, pricing

## How to read this report

A pass means the backend is eligible to serve the operation: it answered with every field the probe reads. It does not mean Karpenter accepts the answers. Karpenter itself (criteria 1 to 3, run by hand before the approval stop) is the oracle, and it can move an operation the probe passed to the next backend. A skip counts as not passing.

## Fields each check requires

| Check | Required fields |
|---|---|
| sts:GetCallerIdentity | GetCallerIdentityResult.Account |
| ssm:PutParameter | HTTP 200 for a name outside /aws/ |
| ssm:GetParameter | Parameter.Value |
| ssm:GetParameter (AMI alias path) | Parameter.Value |
| iam:CreateRole | HTTP 200 |
| iam:GetRole | GetRoleResult.Role.RoleName, GetRoleResult.Role.Arn |
| iam:CreateInstanceProfile | HTTP 200 (accepts Tags.member.N) |
| iam:AddRoleToInstanceProfile | HTTP 200 |
| iam:GetInstanceProfile | GetInstanceProfileResult.InstanceProfile.Arn, GetInstanceProfileResult.InstanceProfile.Roles.member.RoleName |
| iam:ListInstanceProfiles | ListInstanceProfilesResult.InstanceProfiles.member.InstanceProfileName |
| iam:TagInstanceProfile | HTTP 200 |
| sqs:CreateQueue | QueueUrl |
| sqs:GetQueueUrl | QueueUrl (equals the CreateQueue url) |
| sqs:SendMessage | HTTP 200 |
| sqs:ReceiveMessage | Messages[].Body, Messages[].ReceiptHandle |
| sqs:DeleteMessage | HTTP 200 |
| eks:CreateCluster | HTTP 200/201, or 409 ResourceInUse when the cluster exists |
| eks:DescribeCluster | cluster.name, cluster.status (ACTIVE), cluster.version, cluster.endpoint, cluster.kubernetesNetworkConfig.serviceIpv4Cidr |
| ec2:DescribeSubnets (discovery tag) | subnetSet.item.subnetId, subnetSet.item.availabilityZone, filter tag:karpenter.sh/discovery |
| ec2:DescribeSecurityGroups (discovery tag) | securityGroupInfo.item.groupId, filter tag:karpenter.sh/discovery |
| ec2:DescribeImages | imagesSet.item.imageId, imagesSet.item.architecture, imagesSet.item.creationDate, imagesSet.item.rootDeviceName |
| ec2:CreateLaunchTemplate | launchTemplate.launchTemplateId |
| ec2:CreateFleet | fleetId, fleetInstanceSet.item.instanceIds.item, fleetInstanceSet.item.instanceType, errorSet.item.errorCode (absent) |
| ec2:DescribeLaunchTemplates | launchTemplates.item.launchTemplateName (by name), filter tag:karpenter.k8s.aws/cluster |
| ec2:DescribeInstances (running, tagged) | reservationSet.item.instancesSet.item.instanceId, reservationSet.item.instancesSet.item.instanceState.name (running), reservationSet.item.instancesSet.item.privateIpAddress, reservationSet.item.instancesSet.item.placement.availabilityZone, reservationSet.item.instancesSet.item.instanceType, reservationSet.item.instancesSet.item.tagSet.item.key/value (fleet tags), filter tag-key |
| ec2:DescribeInstanceStatus | instanceStatusSet.item.instanceId, instanceStatusSet.item.instanceState.name (running) |
| ec2:CreateTags | HTTP 200, tagSet.item.key/value visible in DescribeInstances |
| ec2:RunInstances | instancesSet.item.instanceId |
| ec2:TerminateInstances | instancesSet.item.currentState.name, DescribeInstances instanceState.name reaches terminated |
| ec2:DeleteLaunchTemplate | HTTP 200 |
| ec2:DescribeAvailabilityZones | availabilityZoneInfo.item.zoneName, availabilityZoneInfo.item.zoneId |
| ec2:DescribeInstanceTypes | instanceTypeSet.item.vCpuInfo.defaultVCpus, instanceTypeSet.item.memoryInfo.sizeInMiB, instanceTypeSet.item.processorInfo.supportedArchitectures.item, instanceTypeSet.item.networkInfo.maximumNetworkInterfaces, instanceTypeSet.item.networkInfo.ipv4AddressesPerInterface, instanceTypeSet.item.supportedUsageClasses.item (on-demand, spot) |
| ec2:DescribeInstanceTypeOfferings | instanceTypeOfferingSet.item.instanceType, filter location |
| ec2:DescribeSpotPriceHistory | spotPriceHistorySet.item.spotPrice, spotPriceHistorySet.item.availabilityZone |
| pricing:GetProducts | PriceList[].product.attributes.instanceType, PriceList[].terms.OnDemand.*.priceDimensions.*.pricePerUnit.USD |
| iam:RemoveRoleFromInstanceProfile | HTTP 200 |
| iam:DeleteInstanceProfile | HTTP 200 |
