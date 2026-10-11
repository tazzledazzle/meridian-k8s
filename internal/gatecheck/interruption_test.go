package gatecheck

import (
	"os"
	"strings"
	"testing"
)

// The fixture is a real capture of Karpenter 1.14.1 handling a simctl spot warning on the gate stack (2026-10-11).
func capture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/karpenter_interruption.log")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRealCaptureIsHandledBeforeTermination(t *testing.T) {
	r, err := CheckInterruption(capture(t), "spot-8t9sn")
	if err != nil {
		t.Fatalf("real capture rejected: %v", err)
	}
	if r.Node != "ip-10-0-2-10.us-west-2.compute.internal" || r.InstanceID != "i-55a23d42b53b19917" {
		t.Errorf("result = %+v", r)
	}
}

// line returns the first captured line containing all of the substrings.
func line(t *testing.T, subs ...string) string {
	t.Helper()
	for _, l := range strings.Split(capture(t), "\n") {
		ok := true
		for _, s := range subs {
			ok = ok && strings.Contains(l, s)
		}
		if ok {
			return l
		}
	}
	t.Fatalf("no captured line with %v", subs)
	return ""
}

func without(t *testing.T, drop string) string {
	t.Helper()
	var keep []string
	for _, l := range strings.Split(capture(t), "\n") {
		if !strings.Contains(l, drop) {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func TestMissingStepsFail(t *testing.T) {
	for _, step := range []string{"initiating delete from interruption message", "tainted node", "deleted node", "deleted nodeclaim"} {
		if _, err := CheckInterruption(without(t, `"message":"`+step+`"`), "spot-8t9sn"); err == nil {
			t.Errorf("accepted logs without %q", step)
		}
	}
}

func TestGarbageCollectionOnlyRemovalFails(t *testing.T) {
	// A node removed only by the garbage collector never went through the interruption handler (A6).
	logs := without(t, `"message":"initiating delete from interruption message"`) + "\n" +
		`{"level":"INFO","time":"2026-10-11T04:50:40.000Z","message":"garbage collecting nodeclaim with no cloudprovider representation","controller":"nodeclaim.garbagecollection","NodeClaim":{"name":"spot-8t9sn"}}` + "\n"
	_, err := CheckInterruption(logs, "spot-8t9sn")
	if err == nil || !strings.Contains(err.Error(), "interruption") {
		t.Fatalf("err = %v, want a complaint about the missing interruption message", err)
	}
}

func TestGarbageCollectionBeforeTheHandlerFails(t *testing.T) {
	gc := `{"level":"INFO","time":"2026-10-11T04:50:30.000Z","message":"garbage collecting nodeclaim with no cloudprovider representation","NodeClaim":{"name":"spot-8t9sn"}}`
	if _, err := CheckInterruption(gc+"\n"+capture(t), "spot-8t9sn"); err == nil {
		t.Fatal("accepted a claim the garbage collector had already removed")
	}
}

func TestWrongOrderFails(t *testing.T) {
	// Taint before the message: the node was cordoned by something other than the interruption handler.
	msg := line(t, "initiating delete from interruption message")
	taint := line(t, `"message":"tainted node"`)
	swapped := strings.Replace(strings.Replace(capture(t), msg, "@@M", 1), taint, msg, 1)
	swapped = strings.Replace(swapped, "@@M", taint, 1)
	if _, err := CheckInterruption(swapped, "spot-8t9sn"); err == nil {
		t.Fatal("accepted a taint that precedes the interruption message")
	}
}

func TestOtherNodeClaimsDoNotCount(t *testing.T) {
	if _, err := CheckInterruption(capture(t), "spot-zzzzz"); err == nil {
		t.Fatal("accepted logs for a different NodeClaim")
	}
}

func TestWrongKindOrActionFails(t *testing.T) {
	for _, from := range []string{`"messageKind":"spot_interrupted"`, `"action":"CordonAndDrain"`} {
		to := strings.Replace(from, "spot_interrupted", "scheduled_change", 1)
		if strings.Contains(from, "action") {
			to = `"action":"NoAction"`
		}
		if _, err := CheckInterruption(strings.ReplaceAll(capture(t), from, to), "spot-8t9sn"); err == nil {
			t.Errorf("accepted %s replaced by %s", from, to)
		}
	}
}

func TestNonJSONNoiseIsIgnored(t *testing.T) {
	if _, err := CheckInterruption("not json\n\n{broken\n"+capture(t), "spot-8t9sn"); err != nil {
		t.Fatalf("noise broke the check: %v", err)
	}
}

func TestEmptyNameOrLogsFail(t *testing.T) {
	if _, err := CheckInterruption("", "spot-8t9sn"); err == nil {
		t.Error("accepted empty logs")
	}
	if _, err := CheckInterruption(capture(t), ""); err == nil {
		t.Error("accepted an empty NodeClaim name")
	}
}
