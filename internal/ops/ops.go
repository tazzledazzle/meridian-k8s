// Package ops models ops.yaml, the single list of AWS operations the platform
// supports. Handlers, the gateway and the conformance report all read it.
package ops

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Op is one supported AWS operation.
type Op struct {
	Service  string `yaml:"service"`  // sigv4 service name: ec2, iam, sts, sqs, ssm, pricing, eks
	Name     string `yaml:"name"`     // operation name, e.g. CreateFleet
	Protocol string `yaml:"protocol"` // query | json | restjson
	Backend  string `yaml:"backend"`  // routing key: names a -backend flag, e.g. floci, ec2-sim, pricing-facade, moto
	Fidelity string `yaml:"fidelity"` // shape-validated | behavior-inferred | real-aws-verified
}

// List is the parsed ops.yaml.
type List struct {
	Ops []Op `yaml:"ops"`
}

var (
	protocols = map[string]bool{"query": true, "json": true, "restjson": true}
	backendRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	fidelity  = map[string]bool{"shape-validated": true, "behavior-inferred": true, "real-aws-verified": true}
)

// Coupled lists the operations that must share one backend because they read
// and write the same state: an instance created by CreateFleet has to be
// visible to DescribeInstances, and the subnet, security group and image it
// names have to exist in the backend that launches it. Reference data that
// holds no per-instance state (instance types, offerings, zones, spot prices)
// may be served by a different backend.
var Coupled = []string{
	"ec2:CreateFleet", "ec2:RunInstances", "ec2:TerminateInstances", "ec2:DescribeInstances", "ec2:DescribeInstanceStatus", "ec2:CreateTags",
	"ec2:CreateLaunchTemplate", "ec2:DescribeLaunchTemplates", "ec2:DeleteLaunchTemplate",
	"ec2:DescribeImages", "ec2:DescribeSubnets", "ec2:DescribeSecurityGroups",
}

// Validate reports every problem at once.
func (l List) Validate() error {
	var errs []error
	seen := map[string]bool{}
	for _, o := range l.Ops {
		key := o.Service + ":" + o.Name
		if seen[key] {
			errs = append(errs, fmt.Errorf("%s: duplicate operation", key))
		}
		seen[key] = true
		if !protocols[o.Protocol] {
			errs = append(errs, fmt.Errorf("%s: unknown protocol %q", key, o.Protocol))
		}
		if !backendRE.MatchString(o.Backend) {
			errs = append(errs, fmt.Errorf("%s: backend %q must be a lowercase name like floci or ec2-sim", key, o.Backend))
		}
		if !fidelity[o.Fidelity] {
			errs = append(errs, fmt.Errorf("%s: unknown fidelity label %q", key, o.Fidelity))
		}
	}
	errs = append(errs, l.coupledErrors()...)
	return errors.Join(errs...)
}

// coupledErrors reports operations in Coupled that are routed to different backends.
func (l List) coupledErrors() []error {
	var first, firstKey string
	var errs []error
	for _, key := range Coupled {
		svc, name, _ := strings.Cut(key, ":")
		o, ok := l.Find(svc, name)
		if !ok {
			continue
		}
		if first == "" {
			first, firstKey = o.Backend, key
		} else if o.Backend != first {
			errs = append(errs, fmt.Errorf("%s is routed to %q but %s to %q: these operations share instance state and must use one backend", key, o.Backend, firstKey, first))
		}
	}
	return errs
}

// Find returns the entry for one operation.
func (l List) Find(service, name string) (Op, bool) {
	for _, o := range l.Ops {
		if o.Service == service && o.Name == name {
			return o, true
		}
	}
	return Op{}, false
}

// Has reports whether the operation is supported.
func (l List) Has(service, name string) bool {
	_, ok := l.Find(service, name)
	return ok
}

// Backends returns the distinct backend names the list routes to, sorted. The
// gateway refuses to start if any of them is not configured.
func (l List) Backends() []string {
	seen := map[string]bool{}
	for _, o := range l.Ops {
		seen[o.Backend] = true
	}
	out := make([]string, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// ForService returns every supported operation of one service.
func (l List) ForService(service string) []Op {
	var out []Op
	for _, o := range l.Ops {
		if o.Service == service {
			out = append(out, o)
		}
	}
	return out
}
