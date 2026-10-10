package noderegistrar

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

// KWOK builds simulated nodes that the KWOK controller keeps "Ready". They
// simulate capacity, not execution: no containers run on them.
type KWOK struct{}

// Taints every KWOK node carries. The first is what a kubelet started by
// Karpenter's user data registers with; Karpenter removes it once it has
// matched the Node to its NodeClaim. The second is KWOK's conventional taint,
// so a pod only lands here if it explicitly tolerates simulated nodes.
var kwokTaints = []Taint{
	{Key: "karpenter.sh/unregistered", Value: "", Effect: "NoExecute"},
	{Key: "kwok.x-k8s.io/node", Value: "fake", Effect: "NoSchedule"},
}

// Node implements Backend.
func (KWOK) Node(i Instance) (Node, error) {
	t, ok := catalog.Lookup(i.Type)
	if !ok {
		return Node{}, fmt.Errorf("instance %s has unknown type %q", i.ID, i.Type)
	}
	// Not every EC2 backend fills in privateDnsName; derive the name AWS would
	// give from the private IP so the Node name is stable either way.
	name := i.PrivateDNS
	if name == "" && i.PrivateIP != "" {
		name = "ip-" + strings.ReplaceAll(i.PrivateIP, ".", "-") + "." + catalog.Region + ".compute.internal"
	}
	if name == "" || i.AZ == "" {
		return Node{}, fmt.Errorf("instance %s has no private DNS name, private IP or zone yet", i.ID)
	}
	// The ENI-limited pod count EKS and Karpenter both use: ENIs x (IPs per ENI - 1) + 2.
	pods := t.MaxENIs*(t.IPv4PerENI-1) + 2
	res := map[string]string{
		"cpu":               strconv.Itoa(t.VCPU),
		"memory":            strconv.Itoa(t.MemoryMiB) + "Mi",
		"pods":              strconv.Itoa(pods),
		"ephemeral-storage": "20Gi", // matches the 20 GiB root volume DescribeImages reports
	}
	return Node{
		Name:       name,
		ProviderID: i.ProviderID(),
		Labels: map[string]string{
			"kubernetes.io/hostname":           name,
			"kubernetes.io/os":                 "linux",
			"kubernetes.io/arch":               goArch(t.Arch),
			"node.kubernetes.io/instance-type": t.Name,
			"topology.kubernetes.io/zone":      i.AZ,
			"topology.kubernetes.io/region":    catalog.Region,
			"type":                             "kwok", // the label KWOK's own examples select on
			BackendKey:                         "kwok",
			ManagedByKey:                       ManagedByValue,
			InstanceIDKey:                      i.ID,
		},
		Annotations: map[string]string{
			"kwok.x-k8s.io/node":           "fake", // opts the node in to the KWOK controller
			"node.alpha.kubernetes.io/ttl": "0",
		},
		Taints:      append([]Taint(nil), kwokTaints...),
		Capacity:    res,
		Allocatable: res,
	}, nil
}

// goArch maps EC2 architecture names to the values Kubernetes uses.
func goArch(ec2Arch string) string {
	if ec2Arch == "arm64" {
		return "arm64"
	}
	return "amd64"
}
