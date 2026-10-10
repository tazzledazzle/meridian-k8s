package noderegistrar

import "testing"

func runningInstance() Instance {
	return Instance{ID: "i-0123", State: "running", Type: "m5.large", AZ: "us-west-2a",
		PrivateDNS: "ip-10-0-1-10.us-west-2.compute.internal", PrivateIP: "10.0.1.10"}
}

func TestKWOKNodeMatchesTheInstance(t *testing.T) {
	n, err := KWOK{}.Node(runningInstance())
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "ip-10-0-1-10.us-west-2.compute.internal" || n.ProviderID != "aws:///us-west-2a/i-0123" {
		t.Fatalf("name/providerID = %s / %s", n.Name, n.ProviderID)
	}
	want := map[string]string{
		"node.kubernetes.io/instance-type": "m5.large", "topology.kubernetes.io/zone": "us-west-2a",
		"kubernetes.io/arch": "amd64", ManagedByKey: ManagedByValue, InstanceIDKey: "i-0123", "type": "kwok",
	}
	for k, v := range want {
		if n.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, n.Labels[k], v)
		}
	}
	if n.Annotations["kwok.x-k8s.io/node"] != "fake" {
		t.Error("the KWOK controller only manages nodes annotated kwok.x-k8s.io/node=fake")
	}
	// m5.large: 3 ENIs x (10 - 1) + 2 = 29 pods.
	if n.Allocatable["pods"] != "29" || n.Allocatable["cpu"] != "2" || n.Allocatable["memory"] != "8192Mi" || n.Allocatable["ephemeral-storage"] != "20Gi" {
		t.Errorf("allocatable = %v", n.Allocatable)
	}
}

func TestKWOKNodeCarriesBothTaints(t *testing.T) {
	n, _ := KWOK{}.Node(runningInstance())
	has := func(key, effect string) bool {
		for _, tn := range n.Taints {
			if tn.Key == key && tn.Effect == effect {
				return true
			}
		}
		return false
	}
	if !has("karpenter.sh/unregistered", "NoExecute") {
		t.Error("missing karpenter.sh/unregistered:NoExecute (what a Karpenter-bootstrapped kubelet registers with)")
	}
	if !has("kwok.x-k8s.io/node", "NoSchedule") {
		t.Error("missing KWOK's conventional taint, so real workloads could land on a simulated node")
	}
}

func TestKWOKRejectsIncompleteOrUnknownInstances(t *testing.T) {
	bad := runningInstance()
	bad.Type = "x9.huge"
	if _, err := (KWOK{}).Node(bad); err == nil {
		t.Error("unknown type must fail")
	}
	bad = runningInstance()
	bad.PrivateDNS, bad.PrivateIP = "", ""
	if _, err := (KWOK{}).Node(bad); err == nil {
		t.Error("an instance with neither a DNS name nor an IP must fail")
	}
}

func TestArm64MapsToTheKubernetesArchLabel(t *testing.T) {
	i := runningInstance()
	i.Type = "m6g.large"
	n, _ := KWOK{}.Node(i)
	if n.Labels["kubernetes.io/arch"] != "arm64" {
		t.Fatalf("arch = %s", n.Labels["kubernetes.io/arch"])
	}
}

func TestNodeNameIsDerivedFromThePrivateIPWhenTheBackendOmitsDNS(t *testing.T) {
	i := runningInstance()
	i.PrivateDNS = ""
	n, err := KWOK{}.Node(i)
	if err != nil || n.Name != "ip-10-0-1-10.us-west-2.compute.internal" || n.Labels["kubernetes.io/hostname"] != n.Name {
		t.Fatalf("name = %q err = %v", n.Name, err)
	}
}
