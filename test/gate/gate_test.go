//go:build gate

// Package gate holds the Karpenter proof gate (G0): eight criteria that must pass from a clean checkout with
// `make gate-karpenter`. The tests run on the host and reach the isolated stack only through docker; nothing
// here talks to a real AWS endpoint. Not validated against real AWS.
package gate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/gatecheck"
	"github.com/tazzledazzle/meridian/internal/trace"
)

// state carries facts from one criterion to the next (the criteria run in order and share one cluster).
type state struct {
	odNode     node
	odInstance string
}

func TestGate(t *testing.T) {
	r := newRig(t)
	s := &state{}

	t.Run("C1_karpenter_starts", func(t *testing.T) {
		eventually(t, 3*time.Minute, "the Karpenter deployment to be available", func() (bool, string) {
			var d struct {
				Status struct {
					AvailableReplicas int `json:"availableReplicas"`
				} `json:"status"`
			}
			if err := r.kcJSON(&d, "-n", "kube-system", "get", "deploy/karpenter"); err != nil {
				return false, err.Error()
			}
			return d.Status.AvailableReplicas >= 1, "no available replica yet"
		})
		var pods struct {
			Items []struct {
				Status struct {
					ContainerStatuses []struct {
						RestartCount int `json:"restartCount"`
					} `json:"containerStatuses"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := r.kcJSON(&pods, "-n", "kube-system", "get", "pods", "-l", "app.kubernetes.io/name=karpenter"); err != nil {
			t.Fatal(err)
		}
		if len(pods.Items) == 0 {
			t.Fatal("no Karpenter pod found")
		}
		for _, p := range pods.Items {
			for _, c := range p.Status.ContainerStatuses {
				if c.RestartCount > 0 {
					t.Errorf("Karpenter restarted %d times: it could not get what it needs from the simulated AWS", c.RestartCount)
				}
			}
		}
		logs, err := r.kc("-n", "kube-system", "logs", "deploy/karpenter", "--tail=-1")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logs, "panic:") {
			t.Errorf("Karpenter panicked:\n%s", logs)
		}
	})

	t.Run("C2_pending_pod_becomes_a_running_node", func(t *testing.T) {
		r.scale(t, "inflate-ondemand", 1)
		var providerID string
		eventually(t, 3*time.Minute, "a NodeClaim with a provider ID (CreateFleet succeeded)", func() (bool, string) {
			var l struct {
				Items []struct {
					Status struct {
						ProviderID string `json:"providerID"`
					} `json:"status"`
				} `json:"items"`
			}
			if err := r.kcJSON(&l, "get", "nodeclaims", "-l", "karpenter.sh/nodepool=ondemand"); err != nil {
				return false, err.Error()
			}
			if len(l.Items) == 0 || l.Items[0].Status.ProviderID == "" {
				return false, "no NodeClaim with a provider ID yet"
			}
			providerID = l.Items[0].Status.ProviderID
			return true, ""
		})
		s.odInstance = providerID[strings.LastIndex(providerID, "/")+1:]
		eventually(t, 2*time.Minute, "the EC2 instance to be running", func() (bool, string) {
			st, err := r.instanceState(s.odInstance)
			if err != nil {
				return false, err.Error()
			}
			return st == "running", "instance is " + st
		})
		eventually(t, 3*time.Minute, "a Ready Node for that instance and the pod running on it", func() (bool, string) {
			nodes, err := r.nodesOf("ondemand")
			if err != nil {
				return false, err.Error()
			}
			for _, n := range nodes {
				if n.instanceID() == s.odInstance && n.ready() {
					ok, err := r.runningPodOn("inflate-ondemand", n.Metadata.Name)
					if err != nil {
						return false, err.Error()
					}
					if ok {
						s.odNode = n
						return true, ""
					}
					return false, "node is Ready but the pod is not Running on it yet"
				}
			}
			return false, "no Ready node with providerID for " + s.odInstance
		})
	})

	t.Run("C5_cheaper_offering_chosen_from_pricing_data", func(t *testing.T) {
		if s.odNode.Metadata.Name == "" {
			t.Skip("C2 did not produce a node")
		}
		out, err := r.tool("s0probe", "-endpoint", gateway, "-region", region, "-prices", "t3.medium,m5.large")
		if err != nil {
			t.Fatal(err)
		}
		var prices map[string]float64
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &prices); err != nil {
			t.Fatalf("prices output %q: %v", out, err)
		}
		if prices["t3.medium"] == prices["m5.large"] || prices["t3.medium"] == 0 || prices["m5.large"] == 0 {
			t.Fatalf("prices %v: the price data cannot distinguish the two types, so this criterion would prove nothing", prices)
		}
		want := "t3.medium"
		if prices["m5.large"] < prices["t3.medium"] {
			want = "m5.large"
		}
		if got := s.odNode.Metadata.Labels["node.kubernetes.io/instance-type"]; got != want {
			t.Errorf("Karpenter launched %s but the cheapest allowed type is %s (prices %v)", got, want, prices)
		}
	})

	t.Run("C6_ami_alias_and_instance_profile", func(t *testing.T) {
		var nc struct {
			Status struct {
				AMIs []struct {
					ID string `json:"id"`
				} `json:"amis"`
				InstanceProfile string      `json:"instanceProfile"`
				Conditions      []condition `json:"conditions"`
			} `json:"status"`
		}
		if err := r.kcJSON(&nc, "get", "ec2nodeclass", "default"); err != nil {
			t.Fatal(err)
		}
		ready := false
		for _, c := range nc.Status.Conditions {
			ready = ready || (c.Type == "Ready" && c.Status == "True")
		}
		if !ready {
			t.Errorf("EC2NodeClass is not Ready: %+v", nc.Status.Conditions)
		}
		b, err := os.ReadFile(filepath.Join(r.root, ".ami-id"))
		if err != nil {
			t.Fatalf("no .ami-id (written by the seed script): %v", err)
		}
		want := strings.TrimSpace(string(b))
		if len(nc.Status.AMIs) == 0 || nc.Status.AMIs[0].ID != want {
			t.Errorf("status.amis = %+v, want the seeded AMI %s (resolved through the SSM alias)", nc.Status.AMIs, want)
		}
		if nc.Status.InstanceProfile == "" {
			t.Fatal("EC2NodeClass has no status.instanceProfile")
		}
		out, err := r.awsQuery("iam", "2010-05-08", "GetInstanceProfile", "InstanceProfileName="+nc.Status.InstanceProfile)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "KarpenterNodeRole-meridian-g0") {
			t.Errorf("instance profile %s does not carry the node role:\n%.400s", nc.Status.InstanceProfile, out)
		}
	})

	t.Run("C3_consolidation_terminates_instance_and_deletes_node", func(t *testing.T) {
		if s.odNode.Metadata.Name == "" {
			t.Skip("C2 did not produce a node")
		}
		r.scale(t, "inflate-ondemand", 0)
		eventually(t, 4*time.Minute, "the empty node to be deleted", func() (bool, string) {
			nodes, err := r.nodesOf("ondemand")
			if err != nil {
				return false, err.Error()
			}
			for _, n := range nodes {
				if n.Metadata.Name == s.odNode.Metadata.Name {
					return false, "node " + n.Metadata.Name + " still exists"
				}
			}
			return true, ""
		})
		eventually(t, 2*time.Minute, "the EC2 instance to be terminated", func() (bool, string) {
			st, err := r.instanceState(s.odInstance)
			if err != nil {
				return false, err.Error()
			}
			return st == "shutting-down" || st == "terminated", "instance is " + st
		})
	})

	t.Run("C4_spot_interruption_cordons_drains_and_replaces", func(t *testing.T) {
		r.scale(t, "inflate-spot", 1)
		var first node
		eventually(t, 4*time.Minute, "a Ready spot node with the pod running", func() (bool, string) {
			nodes, err := r.nodesOf("spot")
			if err != nil {
				return false, err.Error()
			}
			for _, n := range nodes {
				if n.ready() {
					if ok, _ := r.runningPodOn("inflate-spot", n.Metadata.Name); ok {
						first = n
						return true, ""
					}
				}
			}
			return false, "no Ready spot node with a Running pod yet"
		})
		// The NodeClaim behind that Node: the handler's log lines name the claim, not the node.
		var claims struct {
			Items []struct {
				Metadata meta `json:"metadata"`
				Status   struct {
					ProviderID string `json:"providerID"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := r.kcJSON(&claims, "get", "nodeclaims", "-l", "karpenter.sh/nodepool=spot"); err != nil {
			t.Fatal(err)
		}
		claim := ""
		for _, c := range claims.Items {
			if c.Status.ProviderID == first.Spec.ProviderID {
				claim = c.Metadata.Name
			}
		}
		if claim == "" {
			t.Fatalf("no NodeClaim has provider ID %s", first.Spec.ProviderID)
		}
		// Floci omits instanceLifecycle=spot from DescribeInstances (docs/s0/criterion4.md), so simctl needs -any-market.
		out, err := r.tool("simctl", "-gateway", gateway, "-queue", queue, "interrupt", "-grace", "10s", "-any-market", first.instanceID())
		if err != nil {
			t.Fatalf("simctl interrupt failed: %v\n%s", err, out)
		}
		eventually(t, 5*time.Minute, "a replacement node running the pod, and the old instance gone", func() (bool, string) {
			nodes, err := r.nodesOf("spot")
			if err != nil {
				return false, err.Error()
			}
			for _, n := range nodes {
				if n.instanceID() != first.instanceID() && n.ready() {
					if ok, _ := r.runningPodOn("inflate-spot", n.Metadata.Name); ok {
						st, err := r.instanceState(first.instanceID())
						if err != nil {
							return false, err.Error()
						}
						return st == "shutting-down" || st == "terminated", "old instance is " + st
					}
				}
			}
			return false, "no replacement node with a Running pod yet"
		})
		// A6: the replacement alone does not prove the warning was handled; Karpenter's log must show the handler
		// received it and cordoned and drained the node before the claim (and its instance) was deleted.
		logs, err := r.kc("-n", "kube-system", "logs", "deploy/karpenter", "--tail=-1")
		if err != nil {
			t.Fatal(err)
		}
		res, err := gatecheck.CheckInterruption(logs, claim)
		if err != nil {
			t.Fatalf("A6: %v", err)
		}
		if res.InstanceID != first.instanceID() {
			t.Errorf("A6: Karpenter deleted instance %s, the interrupted one was %s", res.InstanceID, first.instanceID())
		}
		t.Logf("handled by the interruption handler: node %s, instance %s, offering marked unavailable: %v", res.Node, res.InstanceID, res.OfferingUnavailable)
		r.scale(t, "inflate-spot", 0)
	})

	t.Run("C7_no_route_to_the_internet", func(t *testing.T) {
		// A5: the network is internal, every gate container is on it and only it, and nothing publishes a port or
		// mounts the Docker socket (A1).
		netJSON, ctrJSON, err := r.gateContainers()
		if err != nil {
			t.Fatal(err)
		}
		if err := gatecheck.CheckNetwork(network, netJSON, ctrJSON); err != nil {
			t.Error(err)
		}
		if out, err := r.sh(60*time.Second, "./scripts/check-no-sock.sh", "deploy/compose.gate.yaml"); err != nil {
			t.Errorf("A1: %v\n%s", err, out)
		}
		// From every vantage point a public AWS endpoint, and a raw public IP (so a DNS failure alone cannot pass),
		// must be unreachable, while the gateway must answer (so a broken probe cannot pass either).
		for _, v := range vantages {
			if ok, detail, err := v.run(r, "http://aws-gateway:4566"); err != nil || !ok {
				t.Errorf("%s: control failed, the probe cannot reach the gateway (%v): %s", v.name, err, detail)
				continue
			}
			for _, target := range []string{"https://ec2.us-west-2.amazonaws.com", "https://1.1.1.1", "https://8.8.8.8"} {
				ok, detail, err := v.run(r, target)
				switch {
				case err != nil:
					t.Errorf("%s: probing %s failed to run: %v", v.name, target, err)
				case ok:
					t.Errorf("%s reached %s: %s", v.name, target, detail)
				}
			}
		}
	})

	t.Run("C8_call_surface_matches_the_golden_file", func(t *testing.T) {
		time.Sleep(12 * time.Second) // the gateway flushes its recording every 10 seconds
		goldenPath := filepath.Join(r.root, "trace", "golden", "karpenter-calls.golden")
		got, err := trace.ReadGolden(filepath.Join(r.root, "trace", "gate", "calls.golden"))
		if err != nil {
			t.Fatal(err)
		}
		want, err := trace.ReadGolden(goldenPath)
		if err != nil {
			t.Fatalf("no golden file yet: review trace/gate/calls.golden and run `make golden-update` (%v)", err)
		}
		added, removed := trace.Diff(want, got)
		if len(added)+len(removed) > 0 {
			t.Errorf("Karpenter's AWS call surface changed.\n  now calls (not in golden): %v\n  no longer calls: %v\nReview the diff, then `make golden-update`.", added, removed)
		}
		raw, err := os.ReadFile(filepath.Join(r.root, "trace", "gate", "calls.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rec struct {
			Unsupported []string `json:"unsupported"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		if len(rec.Unsupported) > 0 {
			t.Errorf("Karpenter called operations that ops.yaml does not list: %v", rec.Unsupported)
		}
	})
}
