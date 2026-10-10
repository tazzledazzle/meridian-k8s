package ops

import (
	"strings"
	"testing"
)

func TestValidateRejectsDuplicateUnknownProtocolAndBadLabels(t *testing.T) {
	good := Op{Service: "ec2", Name: "CreateFleet", Protocol: "query", Backend: "ec2-sim", Fidelity: "behavior-inferred"}
	cases := map[string]List{
		"duplicate":        {Ops: []Op{good, good}},
		"unknown protocol": {Ops: []Op{{Service: "ec2", Name: "X", Protocol: "soap", Backend: "ec2-sim", Fidelity: "behavior-inferred"}}},
		"bad backend name": {Ops: []Op{{Service: "ec2", Name: "X", Protocol: "query", Backend: "Not A Name", Fidelity: "behavior-inferred"}}},
		"unknown fidelity": {Ops: []Op{{Service: "ec2", Name: "X", Protocol: "query", Backend: "ec2-sim", Fidelity: "perfect"}}},
	}
	for name, l := range cases {
		if err := l.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := (List{Ops: []Op{good}}).Validate(); err != nil {
		t.Errorf("good list rejected: %v", err)
	}
}

func TestCoupledOperationsMustShareABackend(t *testing.T) {
	mk := func(name, backend string) Op {
		return Op{Service: "ec2", Name: name, Protocol: "query", Backend: backend, Fidelity: "behavior-inferred"}
	}
	split := List{Ops: []Op{mk("CreateFleet", "floci"), mk("DescribeInstances", "ec2-sim")}}
	if err := split.Validate(); err == nil || !strings.Contains(err.Error(), "share instance state") {
		t.Fatalf("split lifecycle should be rejected, got %v", err)
	}
	// Reference data may live elsewhere.
	ok := List{Ops: []Op{mk("CreateFleet", "floci"), mk("DescribeInstances", "floci"), mk("DescribeSpotPriceHistory", "ec2-sim"), mk("DescribeInstanceTypes", "ec2-sim")}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("reference data on another backend should be accepted, got %v", err)
	}
}

func TestFindAndBackends(t *testing.T) {
	l := List{Ops: []Op{
		{Service: "ec2", Name: "CreateFleet", Protocol: "query", Backend: "floci", Fidelity: "behavior-inferred"},
		{Service: "ec2", Name: "DescribeSpotPriceHistory", Protocol: "query", Backend: "ec2-sim", Fidelity: "behavior-inferred"},
		{Service: "ssm", Name: "GetParameter", Protocol: "json", Backend: "floci", Fidelity: "behavior-inferred"},
	}}
	if op, ok := l.Find("ec2", "DescribeSpotPriceHistory"); !ok || op.Backend != "ec2-sim" {
		t.Fatalf("Find = %+v %v", op, ok)
	}
	if _, ok := l.Find("ec2", "Nope"); ok {
		t.Fatal("unexpected hit")
	}
	if got := l.Backends(); len(got) != 2 || got[0] != "ec2-sim" || got[1] != "floci" {
		t.Fatalf("Backends = %v", got)
	}
}

func TestHasAndForService(t *testing.T) {
	l := List{Ops: []Op{
		{Service: "ec2", Name: "CreateFleet", Protocol: "query", Backend: "ec2-sim", Fidelity: "behavior-inferred"},
		{Service: "ssm", Name: "GetParameter", Protocol: "json", Backend: "floci", Fidelity: "behavior-inferred"},
	}}
	if !l.Has("ec2", "CreateFleet") || l.Has("ec2", "RunInstances") {
		t.Fatal("Has gave the wrong answer")
	}
	if got := l.ForService("ssm"); len(got) != 1 || got[0].Name != "GetParameter" {
		t.Fatalf("ForService(ssm) = %+v", got)
	}
}

// karpenterIAMPolicy is the set of AWS actions in Karpenter's published IAM
// policy. It is the best public indicator of the call surface; Task 7's trace
// is the authority and may add to ops.yaml.
var karpenterIAMPolicy = [][2]string{
	{"ssm", "GetParameter"},
	{"ec2", "DescribeImages"}, {"ec2", "RunInstances"}, {"ec2", "DescribeLaunchTemplates"},
	{"ec2", "DescribeInstances"}, {"ec2", "DescribeInstanceTypes"}, {"ec2", "DescribeInstanceTypeOfferings"},
	{"ec2", "DescribeAvailabilityZones"}, {"ec2", "DeleteLaunchTemplate"}, {"ec2", "CreateTags"},
	{"ec2", "CreateLaunchTemplate"}, {"ec2", "CreateFleet"}, {"ec2", "DescribeSpotPriceHistory"},
	{"ec2", "TerminateInstances"}, {"ec2", "DescribeSubnets"}, {"ec2", "DescribeSecurityGroups"},
	{"pricing", "GetProducts"},
	{"eks", "DescribeCluster"},
	{"iam", "GetInstanceProfile"}, {"iam", "CreateInstanceProfile"}, {"iam", "DeleteInstanceProfile"},
	{"iam", "AddRoleToInstanceProfile"}, {"iam", "RemoveRoleFromInstanceProfile"}, {"iam", "TagInstanceProfile"},
	{"iam", "ListInstanceProfiles"},
	{"sqs", "GetQueueUrl"}, {"sqs", "ReceiveMessage"}, {"sqs", "DeleteMessage"},
	{"sts", "GetCallerIdentity"},
}

// platformOps are calls the platform's own tooling makes (seeding, the spot
// injector, the S0 probe, node-registrar), not Karpenter. They must be in
// ops.yaml too, because the gateway rejects anything that is not listed.
var platformOps = [][2]string{
	{"ssm", "PutParameter"}, {"iam", "CreateRole"}, {"iam", "GetRole"},
	{"sqs", "CreateQueue"}, {"sqs", "SendMessage"}, {"eks", "CreateCluster"},
}

func TestRepoOpsFileCoversKarpenterIAMPolicyAndIsValid(t *testing.T) {
	l, err := Load("../../ops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, sn := range append(append([][2]string{}, karpenterIAMPolicy...), platformOps...) {
		if !l.Has(sn[0], sn[1]) {
			t.Errorf("ops.yaml is missing %s:%s", sn[0], sn[1])
		}
	}
}
