// Package gatecheck holds the pure assertions behind the Karpenter proof gate (test/gate): they take captured
// text or JSON and return an error, so they are unit-tested without Docker. The gate feeds them live data.
package gatecheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Interruption is what CheckInterruption learned from a handled spot warning.
type Interruption struct {
	Node                string // the Node Karpenter cordoned and drained
	InstanceID          string // from the provider-id on the "deleted nodeclaim" line
	OfferingUnavailable bool   // Karpenter also marked the spot offering unavailable (a debug line)
}

type logLine struct {
	Message     string `json:"message"`
	MessageKind string `json:"messageKind"`
	Action      string `json:"action"`
	ProviderID  string `json:"provider-id"`
	NodeClaim   struct {
		Name string `json:"name"`
	} `json:"NodeClaim"`
	Node struct {
		Name string `json:"name"`
	} `json:"Node"`
}

// CheckInterruption proves, from Karpenter's own log, that the interruption handler (not the garbage collector)
// removed the NodeClaim (amendment A6). For the named NodeClaim the log must contain, in this order:
//
//	"initiating delete from interruption message" (kind spot_interrupted, action CordonAndDrain)
//	"tainted node"  ->  "deleted node"  ->  "deleted nodeclaim"
//
// and no "garbage collecting" line for it. Karpenter terminates the instance itself between the last two, so this
// order is the proof that the warning was handled before the instance went away. Lines that are not JSON are skipped.
func CheckInterruption(logs, nodeClaim string) (Interruption, error) {
	var res Interruption
	if nodeClaim == "" {
		return res, errors.New("no NodeClaim name given")
	}
	var lines []logLine
	for _, raw := range strings.Split(logs, "\n") {
		var l logLine
		if json.Unmarshal([]byte(raw), &l) == nil && l.Message != "" {
			lines = append(lines, l)
		}
	}
	find := func(from int, match func(logLine) bool) int {
		for i := from; i < len(lines); i++ {
			if lines[i].NodeClaim.Name == nodeClaim && match(lines[i]) {
				return i
			}
		}
		return -1
	}
	msg := func(m string) func(logLine) bool { return func(l logLine) bool { return l.Message == m } }

	for i, l := range lines {
		if l.NodeClaim.Name == nodeClaim && strings.Contains(l.Message, "garbage collecting") {
			return res, fmt.Errorf("log line %d: the garbage collector removed %s; the interruption handler must do it", i+1, nodeClaim)
		}
	}
	got := find(0, func(l logLine) bool {
		return l.Message == "initiating delete from interruption message" && l.MessageKind == "spot_interrupted" && l.Action == "CordonAndDrain"
	})
	if got < 0 {
		return res, fmt.Errorf("no interruption message (kind spot_interrupted, action CordonAndDrain) for NodeClaim %s in the Karpenter log", nodeClaim)
	}
	res.Node = lines[got].Node.Name
	res.OfferingUnavailable = find(0, func(l logLine) bool {
		return l.Message == "removing offering from offerings" && l.MessageKind == "spot_interrupted"
	}) >= 0

	prev := got
	for _, step := range []string{"tainted node", "deleted node", "deleted nodeclaim"} {
		i := find(prev+1, msg(step))
		if i < 0 {
			return res, fmt.Errorf("no %q for NodeClaim %s after the interruption message (the handler must cordon, drain and delete in order)", step, nodeClaim)
		}
		if step == "deleted nodeclaim" {
			res.InstanceID = lines[i].ProviderID[strings.LastIndex(lines[i].ProviderID, "/")+1:]
			if res.InstanceID == "" {
				return res, fmt.Errorf("the %q line for NodeClaim %s has no provider-id, so the terminated instance cannot be identified", step, nodeClaim)
			}
		}
		prev = i
	}
	return res, nil
}
