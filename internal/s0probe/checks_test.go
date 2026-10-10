package s0probe

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/ops"
)

// probe runs every check against the fake and returns the results keyed by check name.
func probe(t *testing.T, f *fakeAWS, url string, mut func(*Config)) ([]Result, map[string]Result) {
	t.Helper()
	cfg := f.cfg(url)
	if mut != nil {
		mut(&cfg)
	}
	rs := Run(context.Background(), cfg, Checks())
	m := map[string]Result{}
	for _, r := range rs {
		m[r.Name] = r
	}
	return rs, m
}

func opOf(check string) string { return strings.Fields(check)[0] }

func TestAgainstARealisticFlociEverythingPasses(t *testing.T) {
	f, srv := newFake(t)
	rs, _ := probe(t, f, srv.URL, nil)
	for _, r := range rs {
		if r.Status != "pass" {
			t.Errorf("%s: %s: %s", r.Name, r.Status, r.Detail)
		}
	}
	if len(rs) != len(Checks()) {
		t.Fatalf("%d results for %d checks", len(rs), len(Checks()))
	}
	// Live finding 1: the AMI parameter is read, never written, and nothing is written under /aws/.
	for _, p := range f.ssmPuts {
		if strings.HasPrefix(p, "/aws") {
			t.Errorf("probe wrote SSM parameter %q", p)
		}
	}
	if len(f.ssmPuts) == 0 {
		t.Error("expected the ssm:PutParameter round trip under a probe-owned path")
	}
	// The probe cleans up: no live instance, template or instance profile is left behind.
	for id, in := range f.instances {
		if in.state == "running" || in.state == "pending" {
			t.Errorf("instance %s left %s", id, in.state)
		}
	}
	if len(f.lts) != 0 || len(f.profiles) != 0 {
		t.Errorf("leftovers: launch templates %v, instance profiles %v", f.lts, f.profiles)
	}
}

func TestEveryRequestIsAnOperationInOpsYaml(t *testing.T) {
	l, err := ops.Load("../../ops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, srv := newFake(t)
	probe(t, f, srv.URL, nil)
	for _, c := range f.calls {
		svc, op, _ := strings.Cut(c, ":")
		if !l.Has(svc, op) {
			t.Errorf("the probe sent %s, which the gateway would refuse (not in ops.yaml)", c)
		}
	}
}

// Every operation in ops.yaml has at least one check, so a decision never rests on an unprobed operation.
func TestEveryOpsYamlOperationIsProbed(t *testing.T) {
	l, err := ops.Load("../../ops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	probed := map[string]bool{}
	for _, c := range Checks() {
		probed[opOf(c.Name)] = true
	}
	for _, o := range l.Ops {
		if !probed[o.Service+":"+o.Name] {
			t.Errorf("%s:%s is in ops.yaml but no check probes it", o.Service, o.Name)
		}
	}
}

// ---- amendment A2: CreateFleet first among the coupled EC2 checks ----

func TestCreateFleetComesFirstAmongCoupledEC2Checks(t *testing.T) {
	prereq := map[string]bool{"ec2:DescribeSubnets": true, "ec2:DescribeSecurityGroups": true, "ec2:DescribeImages": true, "ec2:CreateLaunchTemplate": true}
	coupled := map[string]bool{}
	for _, c := range ops.Coupled {
		coupled[c] = true
	}
	fleet := -1
	var all []Check
	for _, c := range Checks() {
		if c.Group == "ec2" {
			all = append(all, c)
		}
	}
	for i, c := range all {
		if opOf(c.Name) == "ec2:CreateFleet" {
			fleet = i
		}
	}
	if fleet < 0 {
		t.Fatal("no ec2:CreateFleet check")
	}
	for i, c := range all {
		op := opOf(c.Name)
		switch {
		case i < fleet:
			// Before the fleet call only its prerequisites may run (network, image, launch template).
			if !prereq[op] {
				t.Errorf("%s runs before ec2:CreateFleet but is not one of its prerequisites", c.Name)
			}
			if c.GatedBy != "" {
				t.Errorf("prerequisite %s must not be gated", c.Name)
			}
		case i == fleet:
			if c.GatedBy != "" {
				t.Errorf("ec2:CreateFleet must not be gated")
			}
		case coupled[op]:
			if c.GatedBy != "ec2:CreateFleet" {
				t.Errorf("coupled check %s must be gated by ec2:CreateFleet, GatedBy = %q", c.Name, c.GatedBy)
			}
		default:
			if c.GatedBy != "" {
				t.Errorf("reference-data check %s must still run when CreateFleet fails, GatedBy = %q", c.Name, c.GatedBy)
			}
		}
	}
	// Every coupled operation has a check, so the skip really covers the whole coupled matrix.
	have := map[string]bool{}
	for _, c := range all {
		have[opOf(c.Name)] = true
	}
	for _, c := range ops.Coupled {
		if !have[c] {
			t.Errorf("coupled operation %s has no check", c)
		}
	}
}

func TestCreateFleetFailureSkipsTheCoupledMatrixButNotReferenceData(t *testing.T) {
	f, srv := newFake(t)
	f.fail["ec2:CreateFleet"] = 500
	rs, m := probe(t, f, srv.URL, nil)

	if r := m["ec2:CreateFleet"]; r.Status != "fail" || !strings.Contains(r.Detail, "forced failure") {
		t.Fatalf("CreateFleet = %+v", r)
	}
	gatedSkipped := 0
	for _, c := range Checks() {
		if c.GatedBy == "" {
			continue
		}
		r := m[c.Name]
		if r.Status != "skip" || !strings.Contains(r.Detail, "ec2:CreateFleet failed") || !strings.Contains(r.Detail, "fallback") {
			t.Errorf("%s = %s %q, want skip naming ec2:CreateFleet and the fallback", c.Name, r.Status, r.Detail)
		}
		gatedSkipped++
	}
	if gatedSkipped < 5 {
		t.Fatalf("only %d gated checks", gatedSkipped)
	}
	for _, name := range []string{"ec2:DescribeAvailabilityZones", "ec2:DescribeInstanceTypes", "ec2:DescribeInstanceTypeOfferings", "ec2:DescribeSpotPriceHistory", "pricing:GetProducts", "sqs:CreateQueue"} {
		if m[name].Status != "pass" {
			t.Errorf("%s should still run and pass: %+v", name, m[name])
		}
	}
	// The skipped coupled operations were never sent to the backend.
	for _, op := range []string{"ec2:RunInstances", "ec2:DescribeInstances", "ec2:CreateTags", "ec2:TerminateInstances", "ec2:DescribeLaunchTemplates", "ec2:DeleteLaunchTemplate"} {
		if n := f.called(op); n != 0 {
			t.Errorf("%s was sent %d times after CreateFleet failed", op, n)
		}
	}
	_ = rs
}

// The plan's version indexed an empty subnet list and would have panicked here.
func TestMissingNetworkSkipsCreateFleetInsteadOfCrashing(t *testing.T) {
	f, srv := newFake(t)
	f.noSubnets = true
	_, m := probe(t, f, srv.URL, nil)
	if r := m["ec2:DescribeSubnets (discovery tag)"]; r.Status != "fail" || !strings.Contains(r.Detail, "karpenter.sh/discovery=meridian-g0") {
		t.Fatalf("subnets = %+v", r)
	}
	if r := m["ec2:CreateFleet"]; r.Status != "skip" {
		t.Fatalf("CreateFleet = %+v", r)
	}
	if r := m["ec2:RunInstances"]; r.Status != "skip" {
		t.Fatalf("RunInstances = %+v", r)
	}
	if f.called("ec2:CreateFleet") != 0 {
		t.Fatal("CreateFleet was sent without a subnet")
	}
}

// ---- every check can fail, and says which field is missing ----

func TestEachCheckFailsNamingTheMissingField(t *testing.T) {
	cases := []struct {
		name  string
		check string
		want  string
		set   func(f *fakeAWS)
	}{
		{"no account", "sts:GetCallerIdentity", "Account", func(f *fakeAWS) { f.drop = []string{"Account"} }},
		{"no vcpus", "ec2:DescribeInstanceTypes", "vCpuInfo.defaultVCpus", func(f *fakeAWS) { f.drop = []string{"defaultVCpus"} }},
		{"no memory", "ec2:DescribeInstanceTypes", "memoryInfo.sizeInMiB", func(f *fakeAWS) { f.drop = []string{"sizeInMiB"} }},
		{"no ENI count", "ec2:DescribeInstanceTypes", "networkInfo.maximumNetworkInterfaces", func(f *fakeAWS) { f.drop = []string{"maximumNetworkInterfaces"} }},
		{"no IPs per ENI", "ec2:DescribeInstanceTypes", "networkInfo.ipv4AddressesPerInterface", func(f *fakeAWS) { f.drop = []string{"ipv4AddressesPerInterface"} }},
		{"no architectures", "ec2:DescribeInstanceTypes", "processorInfo.supportedArchitectures", func(f *fakeAWS) { f.drop = []string{"supportedArchitectures"} }},
		{"no spot usage class", "ec2:DescribeInstanceTypes", "supportedUsageClasses", func(f *fakeAWS) {
			f.rewrite["ec2:DescribeInstanceTypes"] = func(s string) string { return strings.ReplaceAll(s, "<item>spot</item>", "") }
		}},
		{"no zone ids", "ec2:DescribeAvailabilityZones", "zoneId", func(f *fakeAWS) { f.drop = []string{"zoneId"} }},
		{"one zone", "ec2:DescribeAvailabilityZones", "only 1 zones", func(f *fakeAWS) {
			f.rewrite["ec2:DescribeAvailabilityZones"] = func(s string) string {
				i := strings.Index(s, "</item>")
				return s[:i+len("</item>")] + "</availabilityZoneInfo></DescribeAvailabilityZonesResponse>"
			}
		}},
		{"no subnets", "ec2:DescribeSubnets (discovery tag)", "karpenter.sh/discovery", func(f *fakeAWS) { f.noSubnets = true }},
		{"no security group", "ec2:DescribeSecurityGroups (discovery tag)", "karpenter.sh/discovery", func(f *fakeAWS) { f.drop = []string{"groupId"} }},
		{"image without creation date", "ec2:DescribeImages", "creationDate", func(f *fakeAWS) { f.drop = []string{"creationDate"} }},
		{"image without architecture", "ec2:DescribeImages", "architecture", func(f *fakeAWS) { f.drop = []string{"architecture"} }},
		{"offering missing", "ec2:DescribeInstanceTypeOfferings", "not offered", func(f *fakeAWS) {
			f.rewrite["ec2:DescribeInstanceTypeOfferings"] = func(s string) string { return strings.ReplaceAll(s, "m5.large", "m5.xlarge") }
		}},
		{"no spot prices", "ec2:DescribeSpotPriceHistory", "no spot prices", func(f *fakeAWS) { f.drop = []string{"spotPrice"} }},
		{"template without id", "ec2:CreateLaunchTemplate", "launchTemplateId", func(f *fakeAWS) { f.drop = []string{"launchTemplateId"} }},
		{"fleet without instances", "ec2:CreateFleet", "no instances launched", func(f *fakeAWS) { f.drop = []string{"instanceIds"} }},
		{"fleet without id", "ec2:CreateFleet", "fleetId", func(f *fakeAWS) { f.drop = []string{"fleetId"} }},
		{"instance without private IP", "ec2:DescribeInstances (running, tagged)", "privateIpAddress", func(f *fakeAWS) { f.drop = []string{"privateIpAddress"} }},
		{"instance without zone", "ec2:DescribeInstances (running, tagged)", "placement.availabilityZone", func(f *fakeAWS) {
			f.rewrite["ec2:DescribeInstances"] = func(s string) string {
				return strings.ReplaceAll(s, "<placement><availabilityZone>us-west-2a</availabilityZone></placement>", "")
			}
		}},
		{"fleet tags not applied", "ec2:DescribeInstances (running, tagged)", "TagSpecification tags were not applied", func(f *fakeAWS) { f.noFleetTags = true }},
		{"instance never runs", "ec2:DescribeInstances (running, tagged)", "never reached running", func(f *fakeAWS) { f.pendingFor = 1 << 30 }},
		{"tag not visible", "ec2:CreateTags", "not visible", func(f *fakeAWS) {
			f.rewrite["ec2:DescribeInstances"] = func(s string) string { return strings.ReplaceAll(s, "s0probe", "other") }
		}},
		{"run instances without id", "ec2:RunInstances", "RunInstances returned 0 instances", func(f *fakeAWS) {
			f.rewrite["ec2:RunInstances"] = func(s string) string { return strings.ReplaceAll(s, "instanceId>", "instanceIdX>") }
		}},
		{"terminate does not progress", "ec2:TerminateInstances", "current states", func(f *fakeAWS) {
			f.rewrite["ec2:TerminateInstances"] = func(s string) string { return strings.ReplaceAll(s, "shutting-down", "running") }
		}},
		{"template not found by tag", "ec2:DescribeLaunchTemplates", "tag filter did not return the template", func(f *fakeAWS) { f.noTagFilter = true }},
		{"role not attached", "iam:GetInstanceProfile", "attached roles", func(f *fakeAWS) { f.drop = []string{"Roles"} }},
		{"profile without ARN", "iam:GetInstanceProfile", "InstanceProfile.Arn", func(f *fakeAWS) {
			f.rewrite["iam:GetInstanceProfile"] = func(s string) string {
				i, j := strings.Index(s, "<Arn>"), strings.Index(s, "</Arn>")
				return s[:i] + s[j+len("</Arn>"):]
			}
		}},
		{"role without ARN", "iam:GetRole", "Role.Arn", func(f *fakeAWS) {
			f.rewrite["iam:GetRole"] = func(s string) string { return strings.ReplaceAll(s, "Arn>", "ArnX>") }
		}},
		{"profile not listed", "iam:ListInstanceProfiles", "not listed", func(f *fakeAWS) {
			f.rewrite["iam:ListInstanceProfiles"] = func(s string) string { return strings.ReplaceAll(s, "InstanceProfileName", "Other") }
		}},
		{"queue without URL", "sqs:CreateQueue", "QueueUrl", func(f *fakeAWS) { f.drop = []string{"QueueUrl"} }},
		{"receive without handle", "sqs:ReceiveMessage", "ReceiptHandle", func(f *fakeAWS) { f.drop = []string{"ReceiptHandle"} }},
		{"receive never delivers", "sqs:ReceiveMessage", "not ready", func(f *fakeAWS) {
			f.rewrite["sqs:ReceiveMessage"] = func(string) string { return `{}` } // the message is accepted, then never shows up
		}},
		{"cluster without CIDR", "eks:DescribeCluster", "serviceIpv4Cidr", func(f *fakeAWS) { f.drop = []string{"serviceIpv4Cidr"} }},
		{"cluster without endpoint", "eks:DescribeCluster", "cluster.endpoint", func(f *fakeAWS) { f.drop = []string{"endpoint"} }},
		{"cluster without version", "eks:DescribeCluster", "cluster.version", func(f *fakeAWS) { f.drop = []string{"version"} }},
		{"cluster never active", "eks:DescribeCluster", "CREATING", func(f *fakeAWS) { f.eksActiveAt = 0 }},
		{"pricing with zero price", "pricing:GetProducts", "pricePerUnit.USD", func(f *fakeAWS) {
			f.rewrite["pricing:GetProducts"] = func(s string) string { return strings.ReplaceAll(s, "0.096", "0") }
		}},
		{"pricing without the type", "pricing:GetProducts", "no on-demand price", func(f *fakeAWS) { f.typeSet = map[string]string{"c5.large": "0.085"} }},
		{"pricing doc for another type", "pricing:GetProducts", "attributes.instanceType", func(f *fakeAWS) {
			f.rewrite["pricing:GetProducts"] = func(s string) string {
				return strings.ReplaceAll(s, `\"instanceType\":\"m5.large\"`, `\"instanceType\":\"zz\"`)
			}
		}},
		{"forced 500 on an iam call", "iam:CreateRole", "HTTP 500", func(f *fakeAWS) { f.fail["iam:CreateRole"] = 500 }},
		{"forced 400 on pricing", "pricing:GetProducts", "HTTP 400", func(f *fakeAWS) { f.fail["pricing:GetProducts"] = 400 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFake(t)
			tc.set(f)
			_, m := probe(t, f, srv.URL, func(c *Config) { c.Wait = 150 * time.Millisecond })
			r, found := m[tc.check]
			if !found {
				t.Fatalf("no result named %q", tc.check)
			}
			if r.Status != "fail" || !strings.Contains(r.Detail, tc.want) {
				t.Fatalf("%s = %s %q, want fail mentioning %q", tc.check, r.Status, r.Detail, tc.want)
			}
		})
	}
}

// ---- live finding 1: the SSM check reads the AMI parameter; DescribeImages must find that image ----

func TestAMIParameterIsReadAndTheImageMustExist(t *testing.T) {
	f, srv := newFake(t)
	f.noAMIImage = true
	_, m := probe(t, f, srv.URL, nil)
	if m["ssm:GetParameter (AMI alias path)"].Status != "pass" {
		t.Fatalf("ssm = %+v", m["ssm:GetParameter (AMI alias path)"])
	}
	r := m["ec2:DescribeImages"]
	if r.Status != "fail" || !strings.Contains(r.Detail, f.ami) {
		t.Fatalf("DescribeImages = %+v, want a failure naming %s", r, f.ami)
	}
	if r := m["ec2:CreateFleet"]; r.Status == "pass" && f.noAMIImage {
		// The launch template names an AMI EC2 does not have; the fleet may not be treated as eligible.
		t.Logf("note: fleet passed with an unknown image: %+v", r)
	}
}

func TestMissingAMIParameterFailsSSMAndSkipsTheImageCheck(t *testing.T) {
	f, srv := newFake(t)
	f.noAMIParam = true
	_, m := probe(t, f, srv.URL, nil)
	if r := m["ssm:GetParameter (AMI alias path)"]; r.Status != "fail" || !strings.Contains(r.Detail, "ParameterNotFound") {
		t.Fatalf("ssm = %+v", r)
	}
	if r := m["ec2:DescribeImages"]; r.Status != "skip" {
		t.Fatalf("DescribeImages = %+v", r)
	}
	for _, p := range f.ssmPuts {
		if strings.HasPrefix(p, "/aws") {
			t.Fatalf("wrote %s", p)
		}
	}
}

func TestAMIParameterValueMustBeAnAMIID(t *testing.T) {
	f, srv := newFake(t)
	f.ami = "not-an-ami"
	_, m := probe(t, f, srv.URL, nil)
	if r := m["ssm:GetParameter (AMI alias path)"]; r.Status != "fail" || !strings.Contains(r.Detail, "not an AMI ID") {
		t.Fatalf("ssm = %+v", r)
	}
}

// ---- live finding 3: EKS through the gateway's two routes ----

func TestEKSCreatesThenWaitsForActive(t *testing.T) {
	f, srv := newFake(t)
	f.eksActiveAt = 4 // CREATING for three polls
	_, m := probe(t, f, srv.URL, nil)
	if m["eks:CreateCluster"].Status != "pass" || m["eks:DescribeCluster"].Status != "pass" {
		t.Fatalf("eks = %+v / %+v", m["eks:CreateCluster"], m["eks:DescribeCluster"])
	}
	if f.eksPosts != 1 || f.eksGets < 4 {
		t.Fatalf("posts=%d gets=%d", f.eksPosts, f.eksGets)
	}
}

func TestEKSAlreadyExistsIsOK(t *testing.T) {
	f, srv := newFake(t)
	f.eksExists = true
	_, m := probe(t, f, srv.URL, nil)
	if r := m["eks:CreateCluster"]; r.Status != "pass" || !strings.Contains(r.Detail, "already exists") {
		t.Fatalf("CreateCluster = %+v", r)
	}
	if m["eks:DescribeCluster"].Status != "pass" {
		t.Fatalf("DescribeCluster = %+v", m["eks:DescribeCluster"])
	}
}

// Without -eks-create the create operation was not exercised, which must not read as a pass.
func TestEKSCreateNotRequestedIsReportedAsNotProbed(t *testing.T) {
	f, srv := newFake(t)
	f.eksExists = true
	_, m := probe(t, f, srv.URL, func(c *Config) { c.EKSCreate = false })
	if r := m["eks:CreateCluster"]; r.Status != "skip" || !strings.Contains(r.Detail, "-eks-create") {
		t.Fatalf("CreateCluster = %+v", r)
	}
	if f.eksPosts != 0 {
		t.Fatalf("POST /clusters sent %d times", f.eksPosts)
	}
	if m["eks:DescribeCluster"].Status != "pass" {
		t.Fatalf("DescribeCluster = %+v", m["eks:DescribeCluster"])
	}
}

func TestEKSMissingClusterFailsWithTheBackendMessage(t *testing.T) {
	f, srv := newFake(t)
	_, m := probe(t, f, srv.URL, func(c *Config) { c.EKSCreate = false })
	if r := m["eks:DescribeCluster"]; r.Status != "fail" || !strings.Contains(r.Detail, "No cluster found") {
		t.Fatalf("DescribeCluster = %+v", r)
	}
}

// ---- live finding 2: EC2 answers a few seconds after STS on a cold start ----

func TestColdStartIsRetriedWithinTheBound(t *testing.T) {
	f, srv := newFake(t)
	f.cold = 3 // the first three EC2 requests get 503
	rs, _ := probe(t, f, srv.URL, nil)
	for _, r := range rs {
		if r.Status != "pass" {
			t.Errorf("%s: %s: %s", r.Name, r.Status, r.Detail)
		}
	}
}

func TestColdStartBeyondTheBoundFailsClearly(t *testing.T) {
	f, srv := newFake(t)
	f.cold = 1000
	_, m := probe(t, f, srv.URL, nil)
	r := m["ec2:DescribeSubnets (discovery tag)"]
	if r.Status != "fail" || !strings.Contains(r.Detail, "503") || !strings.Contains(r.Detail, "5 attempts") {
		t.Fatalf("subnets = %+v", r)
	}
	if m["sts:GetCallerIdentity"].Status != "pass" {
		t.Fatal("STS is not affected by the EC2 cold start")
	}
}

// ---- amendment A4: what the probe checks is listed in one place ----

func TestRequiredFieldsHasAnEntryForEveryCheckAndNoStaleOnes(t *testing.T) {
	names := map[string]bool{}
	for _, c := range Checks() {
		names[c.Name] = true
		if len(RequiredFields[c.Name]) == 0 {
			t.Errorf("check %q has no entry in RequiredFields", c.Name)
		}
	}
	for k := range RequiredFields {
		if !names[k] {
			t.Errorf("RequiredFields has an entry for %q, which is not a check", k)
		}
	}
}

func TestRequiredFieldsAreNamedLikeTheTraceShowsThem(t *testing.T) {
	// Spot check a few: the lead compares these paths with the -record trace of real Karpenter calls.
	want := map[string]string{
		"ec2:DescribeInstanceTypes":         "instanceTypeSet.item.networkInfo.maximumNetworkInterfaces",
		"eks:DescribeCluster":               "cluster.kubernetesNetworkConfig.serviceIpv4Cidr",
		"ec2:DescribeImages":                "imagesSet.item.creationDate",
		"pricing:GetProducts":               "PriceList[].terms.OnDemand.*.priceDimensions.*.pricePerUnit.USD",
		"ssm:GetParameter (AMI alias path)": "Parameter.Value",
	}
	for check, field := range want {
		found := false
		for _, f := range RequiredFields[check] {
			found = found || f == field
		}
		if !found {
			t.Errorf("RequiredFields[%q] = %v, want it to contain %q", check, RequiredFields[check], field)
		}
	}
}

func TestReportsStateEligibilityAndNeverClaimProof(t *testing.T) {
	f, srv := newFake(t)
	rs, _ := probe(t, f, srv.URL, nil)
	md := Markdown("S0", rs)
	low := strings.ToLower(md)
	for _, word := range []string{"proven", "verified", "proves", "guarantee"} {
		if strings.Contains(low, word) {
			t.Errorf("report uses %q", word)
		}
	}
	for _, must := range []string{"eligible", "Karpenter", "oracle", "Fields each check requires", "| ec2:DescribeInstanceTypes |"} {
		if !strings.Contains(md, must) {
			t.Errorf("report lacks %q", must)
		}
	}
	js, err := JSON(rs)
	if err != nil || !strings.Contains(string(js), `"required_fields"`) || !strings.Contains(string(js), "serviceIpv4Cidr") {
		t.Fatalf("json: %v", err)
	}
	// Neither artifact may carry the credentials.
	cfg := f.cfg(srv.URL)
	for _, out := range []string{md, string(js)} {
		if strings.Contains(out, cfg.Secret) || strings.Contains(out, cfg.Access) {
			t.Fatal("report contains a credential")
		}
	}
}

func TestReportCallsOutEC2AsTheSwingFactorWhenCreateFleetFails(t *testing.T) {
	f, srv := newFake(t)
	f.fail["ec2:CreateFleet"] = 500
	rs, _ := probe(t, f, srv.URL, nil)
	md := Markdown("S0", rs)
	if !strings.Contains(md, "swing factor") || !strings.Contains(md, "fallback") {
		t.Fatalf("report should name EC2 as the swing factor and ask for the fallback:\n%s", md)
	}
	ok := Markdown("S0", []Result{{Name: "ec2:CreateFleet", Group: "ec2", Status: "pass"}})
	if strings.Contains(ok, "swing factor") {
		t.Fatal("the swing-factor note belongs only to a failed CreateFleet")
	}
}

func TestSkippedChecksAreNotPassesInTheSummary(t *testing.T) {
	f, srv := newFake(t)
	f.fail["ec2:CreateFleet"] = 500
	rs, _ := probe(t, f, srv.URL, nil)
	s := Summarize(rs)
	if s.Fail < 1 || s.Skip < 5 {
		t.Fatalf("summary = %+v", s)
	}
	found := false
	for _, g := range FailedGroups(rs) {
		found = found || g == "ec2"
	}
	if !found {
		t.Fatal("ec2 must be a failed group")
	}
}
