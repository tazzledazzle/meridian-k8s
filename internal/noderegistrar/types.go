// Package noderegistrar turns simulated EC2 instances into Kubernetes Nodes.
// On real AWS the kubelet on a new instance registers itself; here nothing
// boots, so this controller plays the kubelet's part: when ec2-sim reports an
// instance running it creates the Node, and when the instance is gone it
// deletes the Node. It talks to EC2 through the gateway and to Kubernetes
// through its REST API, so it needs no client library.
package noderegistrar

import "context"

// Label and annotation keys the registrar owns.
const (
	ManagedByKey   = "meridian.io/managed-by"
	ManagedByValue = "node-registrar"
	InstanceIDKey  = "meridian.io/instance-id"
	BackendKey     = "meridian.io/node-backend" // also the EC2 tag that picks the backend per instance
)

// Instance is what the registrar needs to know about one EC2 instance.
type Instance struct {
	ID         string
	State      string // pending | running | shutting-down | terminated
	Type       string
	AZ         string
	PrivateDNS string
	PrivateIP  string
	Tags       map[string]string
}

// ProviderID is the Node.spec.providerID Karpenter matches NodeClaims against.
func (i Instance) ProviderID() string { return "aws:///" + i.AZ + "/" + i.ID }

// Taint mirrors a Kubernetes taint.
type Taint struct {
	Key, Value, Effect string
}

// Node is the registrar's view of a Kubernetes Node.
type Node struct {
	Name        string
	ProviderID  string
	Labels      map[string]string
	Annotations map[string]string
	Taints      []Taint
	Capacity    map[string]string
	Allocatable map[string]string
}

// Fleet lists the instances that belong to this cluster.
type Fleet interface {
	Instances(ctx context.Context) ([]Instance, error)
}

// Cluster reads and writes the Nodes the registrar manages. Nodes it did not
// create (the k3d server node, anything else) are never listed or deleted.
type Cluster interface {
	ManagedNodes(ctx context.Context) ([]Node, error)
	Create(ctx context.Context, n Node) error
	Delete(ctx context.Context, name string) error
}

// Backend builds the Node for an instance.
type Backend interface {
	Node(i Instance) (Node, error)
}
