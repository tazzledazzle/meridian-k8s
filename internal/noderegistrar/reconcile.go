package noderegistrar

import (
	"context"
	"fmt"
	"sort"
)

// Result summarizes one reconcile pass.
type Result struct {
	Created []string          // node names created
	Deleted []string          // node names deleted
	Skipped map[string]string // instance ID -> why no node was made
}

// Reconciler makes the set of managed Nodes match the set of running instances.
type Reconciler struct {
	Fleet    Fleet
	Cluster  Cluster
	Backends map[string]Backend // keyed by the meridian.io/node-backend tag value
	Default  string             // backend for instances without the tag
	Logf     func(format string, args ...any)
}

func (r *Reconciler) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Reconcile runs one pass. If the fleet cannot be listed it changes nothing:
// deleting Nodes because EC2 was briefly unreachable would be much worse than
// leaving them for one more interval.
func (r *Reconciler) Reconcile(ctx context.Context) (Result, error) {
	res := Result{Skipped: map[string]string{}}
	instances, err := r.Fleet.Instances(ctx)
	if err != nil {
		return res, fmt.Errorf("list instances: %w", err)
	}
	nodes, err := r.Cluster.ManagedNodes(ctx)
	if err != nil {
		return res, fmt.Errorf("list nodes: %w", err)
	}
	have := map[string]Node{} // by instance ID
	for _, n := range nodes {
		have[n.Labels[InstanceIDKey]] = n
	}

	seen := map[string]bool{}
	for _, in := range instances {
		seen[in.ID] = true
		node, exists := have[in.ID]
		switch in.State {
		case "running":
			if exists {
				continue
			}
			name := in.Tags[BackendKey]
			if name == "" {
				name = r.Default
			}
			b, ok := r.Backends[name]
			if !ok {
				// Never fall back to another backend silently: a pool that asked
				// for real nodes must not quietly get simulated ones.
				res.Skipped[in.ID] = fmt.Sprintf("node backend %q is not available in this build", name)
				r.logf("skip %s: %s", in.ID, res.Skipped[in.ID])
				continue
			}
			n, err := b.Node(in)
			if err != nil {
				res.Skipped[in.ID] = err.Error()
				r.logf("skip %s: %v", in.ID, err)
				continue
			}
			if err := r.Cluster.Create(ctx, n); err != nil {
				return res, fmt.Errorf("create node %s: %w", n.Name, err)
			}
			res.Created = append(res.Created, n.Name)
			r.logf("created node %s for %s", n.Name, in.ID)
		case "shutting-down", "terminated":
			if exists {
				if err := r.Cluster.Delete(ctx, node.Name); err != nil {
					return res, fmt.Errorf("delete node %s: %w", node.Name, err)
				}
				res.Deleted = append(res.Deleted, node.Name)
				r.logf("deleted node %s (instance %s %s)", node.Name, in.ID, in.State)
			}
		}
		// pending: the instance is still booting; a Node appears when it runs.
	}

	// Nodes whose instance is no longer listed at all (for example ec2-sim restarted).
	for id, n := range have {
		if seen[id] {
			continue
		}
		if err := r.Cluster.Delete(ctx, n.Name); err != nil {
			return res, fmt.Errorf("delete orphan node %s: %w", n.Name, err)
		}
		res.Deleted = append(res.Deleted, n.Name)
		r.logf("deleted orphan node %s (instance %s is gone)", n.Name, id)
	}
	sort.Strings(res.Created)
	sort.Strings(res.Deleted)
	return res, nil
}
