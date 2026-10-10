// Package fakeec2 is a deliberately tiny EC2 stand-in for unit tests of EC2
// clients (the spot injector, simctl, node-registrar's EC2 reader). It answers
// DescribeInstances and TerminateInstances with the response shape AWS
// documents, and nothing else. It is not a simulator and is never deployed:
// the real answers come from Floci or, where S0 requires it, from ec2-sim.
package fakeec2

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type instance struct {
	id, typ, az, lifecycle string
	tags                   map[string]string
	launched               time.Time
	terminated             bool
}

// Fake holds instances in memory; its clock decides when pending becomes running.
type Fake struct {
	mu         sync.Mutex
	now        func() time.Time
	pendingFor time.Duration
	seq        int
	items      map[string]*instance
}

// New returns a Fake whose instances stay pending for pendingFor.
func New(now func() time.Time, pendingFor time.Duration) *Fake {
	return &Fake{now: now, pendingFor: pendingFor, items: map[string]*instance{}}
}

// Launch adds an instance and returns its ID. lifecycle is "spot" or "".
func (f *Fake) Launch(typ, az, lifecycle string, tags map[string]string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("i-%017x", f.seq)
	f.items[id] = &instance{id: id, typ: typ, az: az, lifecycle: lifecycle, tags: tags, launched: f.now()}
	return id
}

// State returns pending, running or shutting-down.
func (f *Fake) State(id string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.items[id]
	if !ok {
		return "", false
	}
	return f.stateLocked(i), true
}

func (f *Fake) stateLocked(i *instance) string {
	switch {
	case i.terminated:
		return "shutting-down"
	case f.now().Sub(i.launched) < f.pendingFor:
		return "pending"
	default:
		return "running"
	}
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	q, _ := url.ParseQuery(string(b))
	f.mu.Lock()
	defer f.mu.Unlock()
	switch q.Get("Action") {
	case "DescribeInstances":
		f.describe(w, q)
	case "TerminateInstances":
		var sb strings.Builder
		for _, id := range listParam(q, "InstanceId") {
			if i, ok := f.items[id]; ok {
				i.terminated = true
				fmt.Fprintf(&sb, "<item><instanceId>%s</instanceId></item>", id)
			}
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, "<TerminateInstancesResponse><instancesSet>%s</instancesSet></TerminateInstancesResponse>", sb.String())
	default:
		http.Error(w, "<Response><Errors><Error><Code>InvalidAction</Code></Error></Errors></Response>", http.StatusBadRequest)
	}
}

// listParam collects Name.1, Name.2, ... in order.
func listParam(q url.Values, name string) []string {
	var out []string
	for n := 1; ; n++ {
		v := q.Get(fmt.Sprintf("%s.%d", name, n))
		if v == "" {
			return out
		}
		out = append(out, v)
	}
}

func (f *Fake) describe(w http.ResponseWriter, q url.Values) {
	ids := listParam(q, "InstanceId")
	tagKey := ""
	if q.Get("Filter.1.Name") == "tag-key" {
		tagKey = q.Get("Filter.1.Value.1")
	}
	keys := make([]string, 0, len(f.items))
	for id := range f.items {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, id := range keys {
		i := f.items[id]
		if len(ids) > 0 && !contains(ids, id) {
			continue
		}
		if _, ok := i.tags[tagKey]; tagKey != "" && !ok {
			continue
		}
		lifecycle := ""
		if i.lifecycle != "" {
			lifecycle = "<instanceLifecycle>" + i.lifecycle + "</instanceLifecycle>"
		}
		var tags strings.Builder
		for k, v := range i.tags {
			fmt.Fprintf(&tags, "<item><key>%s</key><value>%s</value></item>", k, v)
		}
		fmt.Fprintf(&sb, `<item><instancesSet><item><instanceId>%s</instanceId><instanceState><name>%s</name></instanceState>`+
			`<instanceType>%s</instanceType><placement><availabilityZone>%s</availabilityZone></placement>`+
			`<privateDnsName>ip-10-0-0-%d.us-west-2.compute.internal</privateDnsName><privateIpAddress>10.0.0.%d</privateIpAddress>`+
			`%s<tagSet>%s</tagSet></item></instancesSet></item>`,
			id, f.stateLocked(i), i.typ, i.az, len(id), len(id), lifecycle, tags.String())
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, "<DescribeInstancesResponse><reservationSet>%s</reservationSet></DescribeInstancesResponse>", sb.String())
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
