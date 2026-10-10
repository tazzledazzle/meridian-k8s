package s0probe

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	ec2Version           = "2016-11-15"
	iamVersion           = "2010-05-08"
	stsVersion           = "2011-06-15"
	probeType            = "m5.large"
	unfilteredPricePages = 20                // pages read for the informational count of priced types
	fleetCheck           = "ec2:CreateFleet" // the gate for the coupled EC2 matrix (amendment A2)
)

var versionRE = regexp.MustCompile(`^\d+\.\d+`)

// need reports a failed expectation as an error naming the missing field, so
// the S0 report says exactly what the backend left out.
func need(ok bool, format string, args ...any) error {
	if ok {
		return nil
	}
	return fmt.Errorf(format, args...)
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func tagFilter(cluster string) []string {
	return []string{"Filter.1.Name=tag:karpenter.sh/discovery", "Filter.1.Value.1=" + cluster}
}

// RequiredFields lists, per check, the response fields the check requires. It is the one place to compare the
// probe against the -record trace of real Karpenter calls (amendment A4): if Karpenter reads a field that is
// not here, the probe can pass an operation that Karpenter then rejects. Paths are dotted; "[]" marks a list
// and "*" a map with arbitrary keys. A test keeps this table and Checks() in step.
var RequiredFields = map[string][]string{
	"sts:GetCallerIdentity":                      {"GetCallerIdentityResult.Account"},
	"ssm:PutParameter":                           {"HTTP 200 for a name outside /aws/"},
	"ssm:GetParameter":                           {"Parameter.Value"},
	"ssm:GetParameter (AMI alias path)":          {"Parameter.Value"},
	"iam:CreateRole":                             {"HTTP 200"},
	"iam:GetRole":                                {"GetRoleResult.Role.RoleName", "GetRoleResult.Role.Arn"},
	"iam:CreateInstanceProfile":                  {"HTTP 200 (accepts Tags.member.N)"},
	"iam:AddRoleToInstanceProfile":               {"HTTP 200"},
	"iam:GetInstanceProfile":                     {"GetInstanceProfileResult.InstanceProfile.Arn", "GetInstanceProfileResult.InstanceProfile.Roles.member.RoleName"},
	"iam:ListInstanceProfiles":                   {"ListInstanceProfilesResult.InstanceProfiles.member.InstanceProfileName"},
	"iam:TagInstanceProfile":                     {"HTTP 200"},
	"sqs:CreateQueue":                            {"QueueUrl"},
	"sqs:GetQueueUrl":                            {"QueueUrl (equals the CreateQueue url)"},
	"sqs:SendMessage":                            {"HTTP 200"},
	"sqs:ReceiveMessage":                         {"Messages[].Body", "Messages[].ReceiptHandle"},
	"sqs:DeleteMessage":                          {"HTTP 200"},
	"eks:CreateCluster":                          {"HTTP 200/201, or 409 ResourceInUse when the cluster exists"},
	"eks:DescribeCluster":                        {"cluster.name", "cluster.status (ACTIVE)", "cluster.version", "cluster.endpoint", "cluster.kubernetesNetworkConfig.serviceIpv4Cidr"},
	"ec2:DescribeSubnets (discovery tag)":        {"subnetSet.item.subnetId", "subnetSet.item.availabilityZone", "filter tag:karpenter.sh/discovery"},
	"ec2:DescribeSecurityGroups (discovery tag)": {"securityGroupInfo.item.groupId", "filter tag:karpenter.sh/discovery"},
	"ec2:DescribeImages":                         {"imagesSet.item.imageId", "imagesSet.item.architecture", "imagesSet.item.creationDate", "imagesSet.item.rootDeviceName"},
	"ec2:CreateLaunchTemplate":                   {"launchTemplate.launchTemplateId"},
	"ec2:CreateFleet": {"fleetId", "fleetInstanceSet.item.instanceIds.item", "fleetInstanceSet.item.instanceType",
		"errorSet.item.errorCode (absent)"},
	"ec2:DescribeLaunchTemplates": {"launchTemplates.item.launchTemplateName (by name)", "filter tag:karpenter.k8s.aws/cluster"},
	"ec2:DescribeInstances (running, tagged)": {"reservationSet.item.instancesSet.item.instanceId", "reservationSet.item.instancesSet.item.instanceState.name (running)",
		"reservationSet.item.instancesSet.item.privateIpAddress", "reservationSet.item.instancesSet.item.placement.availabilityZone",
		"reservationSet.item.instancesSet.item.instanceType", "reservationSet.item.instancesSet.item.tagSet.item.key/value (fleet tags)", "filter tag-key"},
	"ec2:CreateTags":                {"HTTP 200", "tagSet.item.key/value visible in DescribeInstances"},
	"ec2:DescribeInstanceStatus":    {"instanceStatusSet.item.instanceId", "instanceStatusSet.item.instanceState.name (running)"}, // Karpenter 1.14.1 instance-status controller; status fields are reported, not required
	"ec2:RunInstances":              {"instancesSet.item.instanceId"},
	"ec2:TerminateInstances":        {"instancesSet.item.currentState.name", "DescribeInstances instanceState.name reaches terminated"},
	"ec2:DeleteLaunchTemplate":      {"HTTP 200"},
	"ec2:DescribeAvailabilityZones": {"availabilityZoneInfo.item.zoneName", "availabilityZoneInfo.item.zoneId"},
	"ec2:DescribeInstanceTypes": {"instanceTypeSet.item.vCpuInfo.defaultVCpus", "instanceTypeSet.item.memoryInfo.sizeInMiB",
		"instanceTypeSet.item.processorInfo.supportedArchitectures.item", "instanceTypeSet.item.networkInfo.maximumNetworkInterfaces",
		"instanceTypeSet.item.networkInfo.ipv4AddressesPerInterface", "instanceTypeSet.item.supportedUsageClasses.item (on-demand, spot)"},
	"ec2:DescribeInstanceTypeOfferings": {"instanceTypeOfferingSet.item.instanceType", "filter location"},
	"ec2:DescribeSpotPriceHistory":      {"spotPriceHistorySet.item.spotPrice", "spotPriceHistorySet.item.availabilityZone"},
	"pricing:GetProducts": {"PriceList[].product.attributes.instanceType",
		"PriceList[].terms.OnDemand.*.priceDimensions.*.pricePerUnit.USD"},
	"iam:RemoveRoleFromInstanceProfile": {"HTTP 200"},
	"iam:DeleteInstanceProfile":         {"HTTP 200"},
}

// Checks returns the probe, in dependency order. Within ec2, the prerequisites of a fleet (network, image,
// launch template) run first, then ec2:CreateFleet, then the coupled checks that need an instance (gated on
// the fleet), then the reference-data checks, which run whatever happened to the fleet (amendment A2).
func Checks() []Check {
	return []Check{
		// ---- STS ----
		{Name: "sts:GetCallerIdentity", Group: "sts", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "sts", stsVersion, "GetCallerIdentity")
			if err != nil {
				return "", err
			}
			acct := n.Text("GetCallerIdentityResult/Account")
			return "account " + acct, need(acct != "", "no Account in GetCallerIdentityResult")
		}},

		// ---- SSM ----
		// The round trip uses a path of our own; Floci (like AWS) refuses writes under /aws/, so the probe never writes there.
		{Name: "ssm:PutParameter", Group: "ssm", Run: func(ctx context.Context, e *Env) (string, error) {
			name := "/s0probe/" + e.Suffix
			_, err := e.JSONCall(ctx, "ssm", "AmazonSSM.PutParameter", "1.1",
				map[string]any{"Name": name, "Value": "hello", "Type": "String", "Overwrite": true})
			if err == nil {
				e.State["ssmName"] = name
			}
			return name, err
		}},
		{Name: "ssm:GetParameter", Group: "ssm", Needs: []string{"ssmName"}, Run: func(ctx context.Context, e *Env) (string, error) {
			m, err := e.JSONCall(ctx, "ssm", "AmazonSSM.GetParameter", "1.1", map[string]any{"Name": e.State["ssmName"]})
			if err != nil {
				return "", err
			}
			return "value round-trips", need(GetString(m, "Parameter.Value") == "hello", "Parameter.Value = %q, want hello", GetString(m, "Parameter.Value"))
		}},
		// Karpenter resolves the AMI alias by reading this public parameter. Floci serves it already, so read it and let
		// ec2:DescribeImages confirm the image exists.
		{Name: "ssm:GetParameter (AMI alias path)", Group: "ssm", Run: func(ctx context.Context, e *Env) (string, error) {
			m, err := e.JSONCall(ctx, "ssm", "AmazonSSM.GetParameter", "1.1", map[string]any{"Name": e.Cfg.AMIParam})
			if err != nil {
				return "", fmt.Errorf("%w (reading %s)", err, e.Cfg.AMIParam)
			}
			ami := GetString(m, "Parameter.Value")
			if err := need(strings.HasPrefix(ami, "ami-"), "parameter value %q is not an AMI ID (ami-...)", ami); err != nil {
				return "", err
			}
			e.State["ami"] = ami
			return ami, nil
		}},

		// ---- IAM instance profile ----
		{Name: "iam:CreateRole", Group: "iam", Run: func(ctx context.Context, e *Env) (string, error) {
			role := "s0probe-role-" + e.Suffix
			trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
			_, err := e.Query(ctx, "iam", iamVersion, "CreateRole", "RoleName="+role, "AssumeRolePolicyDocument="+trust)
			if err == nil {
				e.State["role"] = role
			}
			return role, err
		}},
		{Name: "iam:GetRole", Group: "iam", Needs: []string{"role"}, Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "iam", iamVersion, "GetRole", "RoleName="+e.State["role"])
			if err != nil {
				return "", err
			}
			return n.Text("GetRoleResult/Role/Arn"), firstErr(
				need(n.Text("GetRoleResult/Role/RoleName") == e.State["role"], "Role.RoleName = %q, want %s", n.Text("GetRoleResult/Role/RoleName"), e.State["role"]),
				need(n.Text("GetRoleResult/Role/Arn") != "", "no Role.Arn"))
		}},
		{Name: "iam:CreateInstanceProfile", Group: "iam", Run: func(ctx context.Context, e *Env) (string, error) {
			prof := "s0probe-profile-" + e.Suffix
			_, err := e.Query(ctx, "iam", iamVersion, "CreateInstanceProfile", "InstanceProfileName="+prof,
				"Tags.member.1.Key=karpenter.sh/managed-by", "Tags.member.1.Value=s0probe")
			if err == nil {
				e.State["profile"] = prof
			}
			return prof, err
		}},
		{Name: "iam:AddRoleToInstanceProfile", Group: "iam", Needs: []string{"role", "profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.Query(ctx, "iam", iamVersion, "AddRoleToInstanceProfile", "InstanceProfileName="+e.State["profile"], "RoleName="+e.State["role"])
			return "", err
		}},
		{Name: "iam:GetInstanceProfile", Group: "iam", Needs: []string{"role", "profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "iam", iamVersion, "GetInstanceProfile", "InstanceProfileName="+e.State["profile"])
			if err != nil {
				return "", err
			}
			roles := n.Texts("GetInstanceProfileResult/InstanceProfile/Roles/member/RoleName")
			return "roles " + strings.Join(roles, ","), firstErr(
				need(n.Text("GetInstanceProfileResult/InstanceProfile/Arn") != "", "no InstanceProfile.Arn"),
				need(len(roles) == 1 && roles[0] == e.State["role"], "attached roles = %v, want [%s]", roles, e.State["role"]))
		}},
		{Name: "iam:ListInstanceProfiles", Group: "iam", Needs: []string{"profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "iam", iamVersion, "ListInstanceProfiles")
			if err != nil {
				return "", err
			}
			names := n.Texts("ListInstanceProfilesResult/InstanceProfiles/member/InstanceProfileName")
			return fmt.Sprintf("%d profiles", len(names)), need(contains(names, e.State["profile"]), "created profile %s not listed (got %v)", e.State["profile"], names)
		}},
		{Name: "iam:TagInstanceProfile", Group: "iam", Needs: []string{"profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.Query(ctx, "iam", iamVersion, "TagInstanceProfile", "InstanceProfileName="+e.State["profile"],
				"Tags.member.1.Key=karpenter.k8s.aws/ec2nodeclass", "Tags.member.1.Value=default")
			return "", err
		}},

		// ---- SQS (JSON protocol) ----
		{Name: "sqs:CreateQueue", Group: "sqs", Run: func(ctx context.Context, e *Env) (string, error) {
			name := "s0probe-" + e.Suffix
			m, err := e.JSONCall(ctx, "sqs", "AmazonSQS.CreateQueue", "1.0", map[string]any{"QueueName": name})
			if err != nil {
				return "", err
			}
			e.State["queueName"] = name
			e.State["queueURL"] = GetString(m, "QueueUrl")
			return e.State["queueURL"], need(e.State["queueURL"] != "", "no QueueUrl")
		}},
		{Name: "sqs:GetQueueUrl", Group: "sqs", Needs: []string{"queueName", "queueURL"}, Run: func(ctx context.Context, e *Env) (string, error) {
			m, err := e.JSONCall(ctx, "sqs", "AmazonSQS.GetQueueUrl", "1.0", map[string]any{"QueueName": e.State["queueName"]})
			if err != nil {
				return "", err
			}
			return GetString(m, "QueueUrl"), need(GetString(m, "QueueUrl") == e.State["queueURL"], "GetQueueUrl %q != CreateQueue %q", GetString(m, "QueueUrl"), e.State["queueURL"])
		}},
		{Name: "sqs:SendMessage", Group: "sqs", Needs: []string{"queueURL"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.JSONCall(ctx, "sqs", "AmazonSQS.SendMessage", "1.0",
				map[string]any{"QueueUrl": e.State["queueURL"], "MessageBody": `{"detail-type":"probe"}`})
			return "", err
		}},
		{Name: "sqs:ReceiveMessage", Group: "sqs", Needs: []string{"queueURL"}, Run: func(ctx context.Context, e *Env) (string, error) {
			var handle string
			err := e.poll(ctx, func() (bool, error) {
				m, err := e.JSONCall(ctx, "sqs", "AmazonSQS.ReceiveMessage", "1.0",
					map[string]any{"QueueUrl": e.State["queueURL"], "MaxNumberOfMessages": 1, "WaitTimeSeconds": 1, "VisibilityTimeout": 30})
				if err != nil {
					return false, err
				}
				msgs, _ := m["Messages"].([]any)
				if len(msgs) == 0 {
					return false, nil
				}
				msg, _ := msgs[0].(map[string]any)
				if msg["Body"] != `{"detail-type":"probe"}` {
					return false, fmt.Errorf("message body = %v", msg["Body"])
				}
				handle, _ = msg["ReceiptHandle"].(string)
				return true, nil
			})
			if err != nil {
				return "", fmt.Errorf("message sent by SendMessage was not received: %w", err)
			}
			e.State["receipt"] = handle
			return "body round-trips", need(handle != "", "no Messages[].ReceiptHandle")
		}},
		{Name: "sqs:DeleteMessage", Group: "sqs", Needs: []string{"queueURL", "receipt"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.JSONCall(ctx, "sqs", "AmazonSQS.DeleteMessage", "1.0",
				map[string]any{"QueueUrl": e.State["queueURL"], "ReceiptHandle": e.State["receipt"]})
			return "", err
		}},

		// ---- EKS (REST-JSON; the gateway routes POST /clusters and GET /clusters/{name}) ----
		{Name: "eks:CreateCluster", Group: "eks", Run: func(ctx context.Context, e *Env) (string, error) {
			if !e.Cfg.EKSCreate {
				// Not exercised, so it must not read as a pass.
				return "", notProbed("eks:CreateCluster was not called (-eks-create not set; the cluster is expected to exist)")
			}
			_, err := e.REST(ctx, "eks", http.MethodPost, "/clusters", map[string]any{
				"name": e.Cfg.Cluster, "roleArn": "arn:aws:iam::123456789012:role/s0probe-cluster",
				"resourcesVpcConfig": map[string]any{"subnetIds": []string{}},
			})
			if err != nil && (strings.Contains(err.Error(), "HTTP 409") || strings.Contains(err.Error(), "ResourceInUse")) {
				return "already exists (409); creation itself was not exercised", nil
			}
			return e.Cfg.Cluster, err
		}},
		{Name: "eks:DescribeCluster", Group: "eks", Run: func(ctx context.Context, e *Env) (string, error) {
			var m map[string]any
			var status string
			err := e.poll(ctx, func() (bool, error) {
				var err error
				m, err = e.REST(ctx, "eks", http.MethodGet, "/clusters/"+e.Cfg.Cluster, nil)
				if err != nil {
					return false, err
				}
				if status = GetString(m, "cluster.status"); status == "" {
					return false, fmt.Errorf("no cluster.status in the DescribeCluster answer")
				}
				return status == "ACTIVE", nil
			})
			if err != nil {
				return "", fmt.Errorf("cluster %s (last status %q): %w", e.Cfg.Cluster, status, err)
			}
			v := GetString(m, "cluster.version")
			return "version " + v, firstErr(
				need(GetString(m, "cluster.name") == e.Cfg.Cluster, "cluster.name = %q, want %s", GetString(m, "cluster.name"), e.Cfg.Cluster),
				need(versionRE.MatchString(v), "cluster.version = %q", v),
				need(GetString(m, "cluster.endpoint") != "", "no cluster.endpoint"),
				need(GetString(m, "cluster.kubernetesNetworkConfig.serviceIpv4Cidr") != "", "no cluster.kubernetesNetworkConfig.serviceIpv4Cidr (Karpenter reads it for the cluster DNS address)"))
		}},

		// ---- EC2: prerequisites of a fleet (network, image, launch template) ----
		{Name: "ec2:DescribeSubnets (discovery tag)", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeSubnets", tagFilter(e.Cfg.Cluster)...)
			if err != nil {
				return "", err
			}
			var good, incomplete []string
			for _, it := range n.All("subnetSet/item") {
				id, az := it.Text("subnetId"), it.Text("availabilityZone")
				if id == "" || az == "" {
					incomplete = append(incomplete, fmt.Sprintf("%q/%q", id, az))
					continue
				}
				good = append(good, id+"|"+az)
			}
			e.Lists["subnets"] = good
			if len(good) > 0 { // CreateFleet needs at least one usable subnet; the check below still wants two
				e.State["subnets"] = strconv.Itoa(len(good))
			}
			return strconv.Itoa(len(good)) + " subnets", firstErr(
				need(len(incomplete) == 0, "subnets without subnetId or availabilityZone: %v", incomplete),
				need(len(good) >= 2, "found %d tagged subnets with zones; seed at least 2 (karpenter.sh/discovery=%s)", len(good), e.Cfg.Cluster))
		}},
		{Name: "ec2:DescribeSecurityGroups (discovery tag)", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeSecurityGroups", tagFilter(e.Cfg.Cluster)...)
			if err != nil {
				return "", err
			}
			var ids []string
			for _, id := range n.Texts("securityGroupInfo/item/groupId") {
				if id != "" {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				e.State["sg"] = ids[0]
			}
			return strings.Join(ids, ","), need(len(ids) >= 1, "no security group (groupId) tagged karpenter.sh/discovery=%s", e.Cfg.Cluster)
		}},
		{Name: "ec2:DescribeImages", Group: "ec2", Needs: []string{"ami"}, Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeImages", "ImageId.1="+e.State["ami"])
			if err != nil {
				return "", err
			}
			it := n.All("imagesSet/item")
			if len(it) != 1 {
				return "", fmt.Errorf("DescribeImages returned %d images for %s, the image the SSM parameter points at", len(it), e.State["ami"])
			}
			return it[0].Text("architecture"), firstErr(
				need(it[0].Text("imageId") == e.State["ami"], "imageId = %q, want %s", it[0].Text("imageId"), e.State["ami"]),
				need(it[0].Text("architecture") != "", "no architecture"),
				need(it[0].Text("creationDate") != "", "no creationDate (Karpenter sorts AMIs by it)"),
				need(it[0].Text("rootDeviceName") != "" || it[0].Text("blockDeviceMapping/item/deviceName") != "", "no rootDeviceName"))
		}},
		{Name: "ec2:CreateLaunchTemplate", Group: "ec2", Needs: []string{"ami"}, Run: func(ctx context.Context, e *Env) (string, error) {
			name := "s0probe-" + e.Suffix
			// A representative subset of what Karpenter puts in a launch template.
			params := []string{
				"LaunchTemplateName=" + name,
				"LaunchTemplateData.ImageId=" + e.State["ami"],
				"LaunchTemplateData.UserData=" + base64.StdEncoding.EncodeToString([]byte("#!/bin/bash\ntrue\n")),
				"LaunchTemplateData.BlockDeviceMapping.1.DeviceName=/dev/xvda",
				"LaunchTemplateData.BlockDeviceMapping.1.Ebs.VolumeSize=20",
				"LaunchTemplateData.BlockDeviceMapping.1.Ebs.VolumeType=gp3",
				"LaunchTemplateData.MetadataOptions.HttpTokens=required",
				"LaunchTemplateData.MetadataOptions.HttpPutResponseHopLimit=1",
				"LaunchTemplateData.Monitoring.Enabled=false",
				"TagSpecification.1.ResourceType=launch-template",
				"TagSpecification.1.Tag.1.Key=karpenter.k8s.aws/cluster", "TagSpecification.1.Tag.1.Value=" + e.Cfg.Cluster,
			}
			if p := e.State["profile"]; p != "" { // only when the IAM checks created one
				params = append(params, "LaunchTemplateData.IamInstanceProfile.Name="+p)
			}
			if sg := e.State["sg"]; sg != "" {
				params = append(params, "LaunchTemplateData.SecurityGroupId.1="+sg)
			}
			n, err := e.Query(ctx, "ec2", ec2Version, "CreateLaunchTemplate", params...)
			if err != nil {
				return "", err
			}
			id := n.Text("launchTemplate/launchTemplateId")
			if id != "" {
				e.State["lt"], e.State["ltID"] = name, id
			}
			return id, need(id != "", "no launchTemplate.launchTemplateId")
		}},

		// ---- EC2: the swing factor. If this fails, the coupled matrix below is skipped. ----
		{Name: fleetCheck, Group: "ec2", Needs: []string{"lt", "subnets"}, Run: func(ctx context.Context, e *Env) (string, error) {
			subs := e.Lists["subnets"] // every entry has an id and a zone (DescribeSubnets filtered the rest)
			s1 := strings.SplitN(subs[0], "|", 2)
			s2 := s1
			if len(subs) > 1 {
				s2 = strings.SplitN(subs[1], "|", 2)
			}
			n, err := e.Query(ctx, "ec2", ec2Version, "CreateFleet",
				"Type=instant",
				"TargetCapacitySpecification.TotalTargetCapacity=1",
				"TargetCapacitySpecification.DefaultTargetCapacityType=spot",
				"SpotOptions.AllocationStrategy=price-capacity-optimized",
				"LaunchTemplateConfigs.1.LaunchTemplateSpecification.LaunchTemplateName="+e.State["lt"],
				"LaunchTemplateConfigs.1.LaunchTemplateSpecification.Version=$Latest",
				"LaunchTemplateConfigs.1.Overrides.1.InstanceType="+probeType,
				"LaunchTemplateConfigs.1.Overrides.1.SubnetId="+s1[0],
				"LaunchTemplateConfigs.1.Overrides.1.AvailabilityZone="+s1[1],
				"LaunchTemplateConfigs.1.Overrides.1.Priority=1",
				"LaunchTemplateConfigs.1.Overrides.2.InstanceType=c5.large",
				"LaunchTemplateConfigs.1.Overrides.2.SubnetId="+s2[0],
				"LaunchTemplateConfigs.1.Overrides.2.AvailabilityZone="+s2[1],
				"LaunchTemplateConfigs.1.Overrides.2.Priority=2",
				"TagSpecification.1.ResourceType=instance",
				"TagSpecification.1.Tag.1.Key=kubernetes.io/cluster/"+e.Cfg.Cluster, "TagSpecification.1.Tag.1.Value=owned",
				"TagSpecification.1.Tag.2.Key=karpenter.sh/nodepool", "TagSpecification.1.Tag.2.Value=default",
				"TagSpecification.1.Tag.3.Key=karpenter.sh/nodeclaim", "TagSpecification.1.Tag.3.Value=default-"+e.Suffix)
			if err != nil {
				return "", err
			}
			var ids []string
			for _, id := range n.Texts("fleetInstanceSet/item/instanceIds/item") {
				if id != "" {
					ids = append(ids, id)
				}
			}
			if len(ids) == 0 {
				return "", fmt.Errorf("no instances launched (fleetInstanceSet.item.instanceIds empty); errors: %v %v", n.Texts("errorSet/item/errorCode"), n.Texts("errorSet/item/errorMessage"))
			}
			e.State["instance"] = ids[0] // set before the field checks so the cleanup still finds the instance
			return ids[0] + " (" + n.Text("fleetInstanceSet/item/instanceType") + ")", firstErr(
				need(n.Text("fleetId") != "", "no fleetId"),
				need(len(n.Texts("errorSet/item/errorCode")) == 0, "unexpected fleet errors %v", n.Texts("errorSet/item/errorCode")))
		}},

		// ---- EC2: coupled checks that need the instance (skipped when the fleet call fails) ----
		{Name: "ec2:DescribeLaunchTemplates", Group: "ec2", GatedBy: fleetCheck, Needs: []string{"lt"}, Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeLaunchTemplates", "LaunchTemplateName.1="+e.State["lt"])
			if err != nil {
				return "", err
			}
			names := n.Texts("launchTemplates/item/launchTemplateName")
			if err := need(len(names) == 1 && names[0] == e.State["lt"], "describe by name returned %v, want [%s]", names, e.State["lt"]); err != nil {
				return "", err
			}
			// Karpenter also lists by tag filter when cleaning up.
			f, err := e.Query(ctx, "ec2", ec2Version, "DescribeLaunchTemplates",
				"Filter.1.Name=tag:karpenter.k8s.aws/cluster", "Filter.1.Value.1="+e.Cfg.Cluster)
			if err != nil {
				return "", fmt.Errorf("filter by tag: %w", err)
			}
			got := f.Texts("launchTemplates/item/launchTemplateName")
			return "by name and by tag", need(contains(got, e.State["lt"]), "tag filter did not return the template (got %v)", got)
		}},
		{Name: "ec2:DescribeInstances (running, tagged)", Group: "ec2", GatedBy: fleetCheck, Needs: []string{"instance"}, Run: func(ctx context.Context, e *Env) (string, error) {
			var it *Node
			state := "not listed"
			err := e.poll(ctx, func() (bool, error) {
				n, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstances", "InstanceId.1="+e.State["instance"])
				if err != nil {
					return false, err
				}
				for _, cand := range n.All("reservationSet/item/instancesSet/item") {
					if cand.Text("instanceId") == e.State["instance"] {
						it, state = cand, cand.Text("instanceState/name")
						return state == "running", nil
					}
				}
				return false, nil
			})
			if err != nil {
				return "", fmt.Errorf("instance %s never reached running (last state %q): %w", e.State["instance"], state, err)
			}
			tags := map[string]string{}
			for _, t := range it.All("tagSet/item") {
				tags[t.Text("key")] = t.Text("value")
			}
			// Karpenter finds its instances by tag filter, so the filter must return the instance too.
			f, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstances",
				"Filter.1.Name=tag-key", "Filter.1.Value.1=kubernetes.io/cluster/"+e.Cfg.Cluster,
				"Filter.2.Name=instance-state-name", "Filter.2.Value.1=pending", "Filter.2.Value.2=running")
			if err != nil {
				return "", fmt.Errorf("filter by tag-key: %w", err)
			}
			if !contains(f.Texts("reservationSet/item/instancesSet/item/instanceId"), e.State["instance"]) {
				return "", fmt.Errorf("instance %s is not returned by the tag-key filter kubernetes.io/cluster/%s: fleet TagSpecification tags were not applied (tags seen: %v)", e.State["instance"], e.Cfg.Cluster, tags)
			}
			note := ""
			if it.Text("instanceLifecycle") != "spot" {
				note = "; NOTE: no instanceLifecycle=spot (spot-only checks need simctl -any-market)"
			}
			return "running at " + it.Text("privateIpAddress") + note, firstErr(
				need(it.Text("privateIpAddress") != "", "no privateIpAddress (node-registrar names the Node from it)"),
				need(it.Text("placement/availabilityZone") != "", "no placement.availabilityZone (providerID needs it)"),
				need(it.Text("instanceType") != "", "no instanceType"),
				need(tags["karpenter.sh/nodepool"] == "default" && tags["kubernetes.io/cluster/"+e.Cfg.Cluster] == "owned",
					"fleet TagSpecification tags were not applied to the instance: %v", tags))
		}},
		// Karpenter 1.14.1's instance-status controller calls this (seen in the Task 8 trace). It reads the state of
		// instances CreateFleet made, so it is coupled to them and gated behind CreateFleet.
		{Name: "ec2:DescribeInstanceStatus", Group: "ec2", GatedBy: fleetCheck, Needs: []string{"instance"}, Run: func(ctx context.Context, e *Env) (string, error) {
			id := e.State["instance"]
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstanceStatus", "InstanceId.1="+id, "IncludeAllInstances=true")
			if err != nil {
				return "", err
			}
			for _, st := range n.All("instanceStatusSet/item") {
				if st.Text("instanceId") != id {
					continue
				}
				state := st.Text("instanceState/name")
				if err := need(state != "", "instanceStatusSet/item/instanceState/name is missing for %s", id); err != nil {
					return "", err
				}
				return fmt.Sprintf("state %s, instance status %q, system status %q", state, st.Text("instanceStatus/status"), st.Text("systemStatus/status")), nil
			}
			return "", fmt.Errorf("instance %s is not listed by DescribeInstanceStatus (instanceStatusSet/item/instanceId)", id)
		}},
		{Name: "ec2:CreateTags", Group: "ec2", GatedBy: fleetCheck, Needs: []string{"instance"}, Run: func(ctx context.Context, e *Env) (string, error) {
			if _, err := e.Query(ctx, "ec2", ec2Version, "CreateTags", "ResourceId.1="+e.State["instance"], "Tag.1.Key=Name", "Tag.1.Value=s0probe"); err != nil {
				return "", err
			}
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstances", "InstanceId.1="+e.State["instance"])
			if err != nil {
				return "", err
			}
			found := false
			for _, t := range n.All("reservationSet/item/instancesSet/item/tagSet/item") {
				found = found || (t.Text("key") == "Name" && t.Text("value") == "s0probe")
			}
			return "tag visible", need(found, "tag added by CreateTags is not visible in DescribeInstances")
		}},
		{Name: "ec2:RunInstances", Group: "ec2", GatedBy: fleetCheck, Needs: []string{"ami", "subnets"}, Run: func(ctx context.Context, e *Env) (string, error) {
			s := strings.SplitN(e.Lists["subnets"][0], "|", 2)
			n, err := e.Query(ctx, "ec2", ec2Version, "RunInstances", "ImageId="+e.State["ami"], "InstanceType=t3.medium",
				"MinCount=1", "MaxCount=1", "SubnetId="+s[0])
			if err != nil {
				return "", err
			}
			ids := n.Texts("instancesSet/item/instanceId")
			if len(ids) != 1 || ids[0] == "" {
				return "", fmt.Errorf("RunInstances returned %d instances (instancesSet.item.instanceId)", len(ids))
			}
			e.State["runInstance"] = ids[0]
			return ids[0], nil
		}},
		// Cleanup: when CreateFleet launched an instance but then failed a field check, the instance is still terminated
		// and the template deleted (the gate is lifted, the fleet failure stays in its own row).
		{Name: "ec2:TerminateInstances", Group: "ec2", GatedBy: fleetCheck, Cleanup: true, Needs: []string{"instance"}, Run: func(ctx context.Context, e *Env) (string, error) {
			ids := []string{e.State["instance"]}
			if e.State["runInstance"] != "" {
				ids = append(ids, e.State["runInstance"])
			}
			params := make([]string, len(ids))
			for i, id := range ids {
				params[i] = fmt.Sprintf("InstanceId.%d=%s", i+1, id)
			}
			n, err := e.Query(ctx, "ec2", ec2Version, "TerminateInstances", params...)
			if err != nil {
				return "", err
			}
			cur := n.Texts("instancesSet/item/currentState/name")
			ok := len(cur) == len(ids)
			for _, c := range cur {
				ok = ok && (c == "shutting-down" || c == "terminated")
			}
			if err := need(ok, "current states = %v, want shutting-down or terminated for %d instances", cur, len(ids)); err != nil {
				return "", err
			}
			// Karpenter waits for "terminated"; make sure the instance gets there.
			last := ""
			err = e.poll(ctx, func() (bool, error) {
				d, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstances", "InstanceId.1="+e.State["instance"])
				if err != nil {
					return false, err
				}
				last = d.Text("reservationSet/item/instancesSet/item/instanceState/name")
				return last == "terminated", nil
			})
			if err != nil {
				return "", fmt.Errorf("instance %s never reached terminated (last state %q): %w", e.State["instance"], last, err)
			}
			return strings.Join(cur, ","), nil
		}},
		{Name: "ec2:DeleteLaunchTemplate", Group: "ec2", GatedBy: fleetCheck, Cleanup: true, Needs: []string{"lt"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.Query(ctx, "ec2", ec2Version, "DeleteLaunchTemplate", "LaunchTemplateName="+e.State["lt"])
			return "", err
		}},

		// ---- EC2 reference data: holds no per-instance state, so it runs whatever happened to the fleet ----
		{Name: "ec2:DescribeAvailabilityZones", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeAvailabilityZones")
			if err != nil {
				return "", err
			}
			names, ids := n.Texts("availabilityZoneInfo/item/zoneName"), n.Texts("availabilityZoneInfo/item/zoneId")
			e.Lists["azs"] = names
			return strings.Join(names, ","), firstErr(need(len(names) >= 2, "only %d zones", len(names)), need(len(ids) == len(names), "zoneId missing on some zones (%d ids for %d zones)", len(ids), len(names)))
		}},
		{Name: "ec2:DescribeInstanceTypes", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstanceTypes", "InstanceType.1="+probeType)
			if err != nil {
				return "", err
			}
			it := n.All("instanceTypeSet/item")
			if len(it) != 1 {
				return "", fmt.Errorf("%d results for %s", len(it), probeType)
			}
			t := it[0]
			usage := strings.Join(t.Texts("supportedUsageClasses/item"), ",")
			if err := firstErr(
				need(t.Text("vCpuInfo/defaultVCpus") != "", "no vCpuInfo.defaultVCpus"),
				need(t.Text("memoryInfo/sizeInMiB") != "", "no memoryInfo.sizeInMiB"),
				need(len(t.Texts("processorInfo/supportedArchitectures/item")) > 0, "no processorInfo.supportedArchitectures"),
				need(t.Text("networkInfo/maximumNetworkInterfaces") != "", "no networkInfo.maximumNetworkInterfaces (pod density is computed from it)"),
				need(t.Text("networkInfo/ipv4AddressesPerInterface") != "", "no networkInfo.ipv4AddressesPerInterface"),
				need(strings.Contains(usage, "spot") && strings.Contains(usage, "on-demand"), "supportedUsageClasses = %q, want on-demand and spot", usage)); err != nil {
				return "", err
			}
			all, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstanceTypes")
			if err != nil {
				return "", fmt.Errorf("unfiltered listing: %w", err)
			}
			e.State["typeCount"] = strconv.Itoa(len(all.All("instanceTypeSet/item")))
			return e.State["typeCount"] + " types listed", nil
		}},
		{Name: "ec2:DescribeInstanceTypeOfferings", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			az := e.Cfg.Region + "a"
			if len(e.Lists["azs"]) > 0 {
				az = e.Lists["azs"][0]
			}
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeInstanceTypeOfferings", "LocationType=availability-zone",
				"Filter.1.Name=location", "Filter.1.Value.1="+az)
			if err != nil {
				return "", err
			}
			types := n.Texts("instanceTypeOfferingSet/item/instanceType")
			return fmt.Sprintf("%d offerings in %s", len(types), az), need(contains(types, probeType), "%s not offered in %s", probeType, az)
		}},
		{Name: "ec2:DescribeSpotPriceHistory", Group: "ec2", Run: func(ctx context.Context, e *Env) (string, error) {
			n, err := e.Query(ctx, "ec2", ec2Version, "DescribeSpotPriceHistory", "InstanceType.1="+probeType,
				"ProductDescription.1=Linux/UNIX", "StartTime="+e.now().UTC().Format(time.RFC3339))
			if err != nil {
				return "", err
			}
			prices := n.Texts("spotPriceHistorySet/item/spotPrice")
			if len(prices) == 0 {
				return "", fmt.Errorf("no spot prices for %s (spotPriceHistorySet.item.spotPrice)", probeType)
			}
			p, perr := strconv.ParseFloat(prices[0], 64)
			return fmt.Sprintf("%d prices, first %s", len(prices), prices[0]), firstErr(perr,
				need(p > 0, "non-positive spot price %q", prices[0]),
				need(n.Text("spotPriceHistorySet/item/availabilityZone") != "", "no availabilityZone on price entries"))
		}},

		// ---- Pricing ----
		{Name: "pricing:GetProducts", Group: "pricing", Run: func(ctx context.Context, e *Env) (string, error) {
			one, err := listProducts(ctx, e, priceFilters(e.Cfg.Region, map[string]string{"Type": "TERM_MATCH", "Field": "instanceType", "Value": probeType}))
			if err != nil {
				return "", err
			}
			if len(one) == 0 {
				return "", fmt.Errorf("no on-demand price for %s in %s (the backend's price data does not cover this region or type)", probeType, e.Cfg.Region)
			}
			typ, price, err := parsePrice(one[0])
			if err != nil {
				return "", err
			}
			// The listing only feeds a count in the detail, so it is bounded and a long listing is not a failure.
			all, truncated, err := listProductsMax(ctx, e, priceFilters(e.Cfg.Region), unfilteredPricePages)
			if err != nil {
				return "", fmt.Errorf("unfiltered-by-type listing: %w", err)
			}
			detail := fmt.Sprintf("%s $%v/h; %d priced types", probeType, price, len(all))
			if truncated {
				detail = fmt.Sprintf("%s $%v/h; at least %d priced types (truncated at %d pages)", probeType, price, len(all), unfilteredPricePages)
			}
			if tc := e.State["typeCount"]; tc != "" {
				detail += " of " + tc + " listed by EC2"
			}
			return detail, firstErr(
				need(typ == probeType, "attributes.instanceType = %q, want %s", typ, probeType),
				need(price > 0, "no terms.OnDemand.*.priceDimensions.*.pricePerUnit.USD"))
		}},

		// ---- IAM cleanup (also operations Karpenter calls) ----
		{Name: "iam:RemoveRoleFromInstanceProfile", Group: "iam", Needs: []string{"role", "profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.Query(ctx, "iam", iamVersion, "RemoveRoleFromInstanceProfile", "InstanceProfileName="+e.State["profile"], "RoleName="+e.State["role"])
			return "", err
		}},
		{Name: "iam:DeleteInstanceProfile", Group: "iam", Needs: []string{"profile"}, Run: func(ctx context.Context, e *Env) (string, error) {
			_, err := e.Query(ctx, "iam", iamVersion, "DeleteInstanceProfile", "InstanceProfileName="+e.State["profile"])
			return "", err
		}},
	}
}
