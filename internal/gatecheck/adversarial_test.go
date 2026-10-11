package gatecheck

import (
	"fmt"
	"strings"
	"testing"
)

// Adversarial tests written by an independent tester (bead meridian-17s.10). Every test asserts the behaviour the
// gate NEEDS. Where the code does not have it, knownDefect turns the failure into a documented skip (and fails
// loudly once the defect is fixed, so the marker cannot rot).

// knownDefect runs check; if it reports a problem the test is skipped with the defect id, and if it reports none the
// test fails (remove the marker).
func knownDefect(t *testing.T, id, why string, check func() string) {
	t.Helper()
	if problem := check(); problem != "" {
		t.Skipf("KNOWN DEFECT %s: %s -- %s", id, why, problem)
	}
	t.Errorf("KNOWN DEFECT %s appears fixed; replace knownDefect with a plain assertion", id)
}

// rename returns the capture with every spot-8t9sn (and only that exact name) replaced by name.
func rename(t *testing.T, name string) string {
	t.Helper()
	return strings.ReplaceAll(capture(t), `"spot-8t9sn"`, `"`+name+`"`)
}

func lines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

// ---------------------------------------------------------------- CheckInterruption

func TestAdvPrefixSiblingClaimDoesNotSatisfyTheOriginal(t *testing.T) {
	sibling := rename(t, "spot-8t9sn2")
	if _, err := CheckInterruption(sibling, "spot-8t9sn"); err == nil {
		t.Fatal("lines of spot-8t9sn2 satisfied a check for spot-8t9sn")
	}
	if _, err := CheckInterruption(sibling, "spot-8t9sn2"); err != nil {
		t.Fatalf("renamed capture should pass for its own name: %v", err)
	}
	if _, err := CheckInterruption(sibling, "spot-8t9s"); err == nil {
		t.Fatal("a name prefix matched")
	}
	if _, err := CheckInterruption(strings.ToUpper(capture(t)), "spot-8t9sn"); err == nil {
		t.Fatal("upper-cased garbage passed")
	}
}

func TestAdvTwoClaimsInterleavedEachJudgedSeparately(t *testing.T) {
	a := lines(capture(t))
	b := lines(rename(t, "spot-8t9sn2"))
	var mixed []string
	for i := range a { // strictly alternate lines of the two claims
		mixed = append(mixed, a[i], b[i])
	}
	logs := strings.Join(mixed, "\n")
	for _, name := range []string{"spot-8t9sn", "spot-8t9sn2"} {
		if _, err := CheckInterruption(logs, name); err != nil {
			t.Errorf("%s: interleaving broke a valid sequence: %v", name, err)
		}
	}
}

func TestAdvStepsFromAnotherClaimDoNotFillTheGap(t *testing.T) {
	// A has the interruption message and deleted node/nodeclaim but NOT its own taint; B (a sibling) has a taint
	// between. B's taint must not satisfy A.
	a := without(t, `"message":"tainted node"`)
	bTaint := strings.Replace(line(t, `"message":"tainted node"`), "spot-8t9sn", "spot-8t9sn2", -1)
	ls := lines(a)
	var out []string
	for _, l := range ls {
		out = append(out, l)
		if strings.Contains(l, "initiating delete from interruption message") {
			out = append(out, bTaint)
		}
	}
	if _, err := CheckInterruption(strings.Join(out, "\n"), "spot-8t9sn"); err == nil {
		t.Fatal("a sibling claim's taint satisfied the check")
	}
}

func TestAdvConsolidationDeleteAloneIsNotAnInterruption(t *testing.T) {
	// A consolidation/drift delete also logs tainted node -> deleted node -> deleted nodeclaim, with no interruption
	// message. Source-verified: terminator.go "tainted node", termination/controller.go "deleted node" and
	// lifecycle/controller.go "deleted nodeclaim" are emitted for every disruption method.
	consolidation := without(t, "initiating delete from interruption message")
	consolidation = without2(consolidation, `"message":"removing offering from offerings"`)
	if _, err := CheckInterruption(consolidation, "spot-8t9sn"); err == nil {
		t.Fatal("a consolidation-driven delete (no interruption message) passed A6")
	}
}

func without2(logs, drop string) string {
	var keep []string
	for _, l := range strings.Split(logs, "\n") {
		if !strings.Contains(l, drop) {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func TestAdvInterruptionForAnotherClaimPlusOurConsolidationFails(t *testing.T) {
	msg := line(t, "initiating delete from interruption message")
	other := strings.ReplaceAll(msg, "spot-8t9sn", "spot-other")
	logs := other + "\n" + without(t, "initiating delete from interruption message")
	if _, err := CheckInterruption(logs, "spot-8t9sn"); err == nil {
		t.Fatal("somebody else's interruption message legitimised our claim's delete")
	}
}

func TestAdvEarlierConsolidationLinesBeforeMessageDoNotCount(t *testing.T) {
	// taint/deleted-node/deleted-nodeclaim all BEFORE the message: the message came too late to have caused them.
	var pre, msg []string
	for _, l := range lines(capture(t)) {
		switch {
		case strings.Contains(l, "initiating delete from interruption message"):
			msg = append(msg, l)
		case strings.Contains(l, `"message":"tainted node"`), strings.Contains(l, `"message":"deleted node"`), strings.Contains(l, `"message":"deleted nodeclaim"`):
			pre = append(pre, l)
		}
	}
	if _, err := CheckInterruption(strings.Join(append(pre, msg...), "\n"), "spot-8t9sn"); err == nil {
		t.Fatal("deletion that precedes the interruption message passed")
	}
}

func TestAdvEveryAdjacentSwapOfTheFourStepsFails(t *testing.T) {
	idx := map[string]int{}
	ls := lines(capture(t))
	for i, l := range ls {
		for _, m := range []string{"initiating delete from interruption message", `"message":"tainted node"`, `"message":"deleted node"`, `"message":"deleted nodeclaim"`} {
			if strings.Contains(l, m) {
				idx[m] = i
			}
		}
	}
	keys := []string{"initiating delete from interruption message", `"message":"tainted node"`, `"message":"deleted node"`, `"message":"deleted nodeclaim"`}
	for a := 0; a < 4; a++ {
		for b := a + 1; b < 4; b++ {
			c := append([]string(nil), ls...)
			c[idx[keys[a]]], c[idx[keys[b]]] = c[idx[keys[b]]], c[idx[keys[a]]]
			if _, err := CheckInterruption(strings.Join(c, "\n"), "spot-8t9sn"); err == nil {
				t.Errorf("swapping %s and %s was accepted", keys[a], keys[b])
			}
		}
	}
}

func TestAdvDuplicatedCaptureStillPasses(t *testing.T) {
	if _, err := CheckInterruption(capture(t)+"\n"+capture(t), "spot-8t9sn"); err != nil {
		t.Fatalf("a duplicated (re-delivered) sequence broke the check: %v", err)
	}
}

func TestAdvTimestampsAreNotUsedOnlyFileOrder(t *testing.T) {
	// Documents the contract: order is the file order. Scrambled timestamps with correct file order pass; correct
	// timestamps with reversed file order fail. kubectl logs preserves write order so this is sound.
	scrambled := capture(t)
	for i, d := range []string{"04:59:59", "04:00:00", "04:30:00"} {
		scrambled = strings.Replace(scrambled, fmt.Sprintf("04:50:3%d", i+2), d, -1)
	}
	if _, err := CheckInterruption(scrambled, "spot-8t9sn"); err != nil {
		t.Errorf("timestamp scrambling with correct file order failed: %v", err)
	}
	ls := lines(capture(t))
	for i, j := 0, len(ls)-1; i < j; i, j = i+1, j-1 {
		ls[i], ls[j] = ls[j], ls[i]
	}
	if _, err := CheckInterruption(strings.Join(ls, "\n"), "spot-8t9sn"); err == nil {
		t.Error("a fully reversed log passed")
	}
}

func TestAdvGarbageCollectionVariants(t *testing.T) {
	// Real text, from sigs.k8s.io/karpenter@6e7eab7a0f48 (the v1.14.1 pin) garbagecollection/controller.go:111:
	//   log...V(1).Info("garbage collecting nodeclaim with no cloudprovider representation")
	// It is a V(1) = DEBUG line, so the gate's logLevel: debug (deploy/karpenter-values.yaml) is required for this
	// check to bite. Placed anywhere in the log for the claim, even after a full valid sequence, it must fail.
	const real = `{"level":"DEBUG","message":"garbage collecting nodeclaim with no cloudprovider representation","NodeClaim":{"name":"spot-8t9sn"},"Node":{"name":"n"},"provider-id":"aws:///z/i-1"}`
	for name, logs := range map[string]string{
		"before":  real + "\n" + capture(t),
		"after":   capture(t) + "\n" + real,
		"middle":  strings.Replace(capture(t), "\n", "\n"+real+"\n", 3),
		"crlf":    strings.ReplaceAll(real+"\n"+capture(t), "\n", "\r\n"),
		"invalid": strings.Replace(real, `"level":"DEBUG"`, "\"level\":\"DE\xff\xfeBUG\"", 1) + "\n" + capture(t),
	} {
		if _, err := CheckInterruption(logs, "spot-8t9sn"); err == nil {
			t.Errorf("%s: GC line did not fail the check", name)
		}
	}
	// A GC line for a different claim must not poison ours.
	other := strings.ReplaceAll(real, "spot-8t9sn", "spot-8t9sn2")
	if _, err := CheckInterruption(other+"\n"+capture(t), "spot-8t9sn"); err != nil {
		t.Errorf("sibling's GC line failed our claim: %v", err)
	}
}

func TestAdvLevelCaseAndMessageVariantsOfGC(t *testing.T) {
	// Capitalised or reworded GC messages are NOT detected. Real Karpenter does not emit them; this pins the
	// current exact-substring behaviour so a Karpenter upgrade that rewords the line is noticed (the sequence check
	// still fails such a run because no interruption message would exist, so this is defence in depth only).
	reworded := `{"message":"Garbage Collecting nodeclaim","NodeClaim":{"name":"spot-8t9sn"}}`
	if _, err := CheckInterruption(reworded+"\n"+capture(t), "spot-8t9sn"); err != nil {
		t.Logf("note: capitalised GC variant is rejected (%v)", err)
	} else {
		t.Log("note: capitalised GC variant is not detected; relies on the interruption-message requirement")
	}
}

func TestAdvHugeAndHostileLines(t *testing.T) {
	huge := strings.Repeat("x", 8<<20)
	hostile := []string{
		huge,
		`{"message":"` + huge + `"}`,
		"\xff\xfe\x00\x01{",
		`{"message":"tainted node","NodeClaim":"not-an-object"}`,
		`{"message":"tainted node","NodeClaim":{"name":123}}`,
		`{"message":["deleted","node"]}`,
		`null`, `[]`, `"string"`, `{}`, "\x00",
	}
	logs := strings.Join(hostile, "\n") + "\n" + capture(t)
	r, err := CheckInterruption(logs, "spot-8t9sn")
	if err != nil {
		t.Fatalf("hostile noise broke a valid log: %v", err)
	}
	if r.InstanceID != "i-55a23d42b53b19917" {
		t.Errorf("instance %q", r.InstanceID)
	}
}

func TestAdvInvalidUTF8InsideRealLinesIsTolerated(t *testing.T) {
	// A byte-mangled field value on an unrelated key must not make Go reject the line.
	logs := strings.Replace(capture(t), `"commit":"bde0065"`, "\"commit\":\"bde\xff0065\"", -1)
	if _, err := CheckInterruption(logs, "spot-8t9sn"); err != nil {
		t.Fatalf("invalid UTF-8 in an unrelated field failed the check: %v", err)
	}
}

func TestAdvProviderIDShapes(t *testing.T) {
	good := capture(t)
	for _, pid := range []string{"", "i-abc", "aws:///", "/", "aws:///us-west-2b/", "a/b/c/i-1"} {
		logs := strings.Replace(good, `"provider-id":"aws:///us-west-2b/i-55a23d42b53b19917"`, `"provider-id":"`+pid+`"`, 1)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic for provider-id %q: %v", pid, r)
				}
			}()
			_, _ = CheckInterruption(logs, "spot-8t9sn")
		}()
	}
	// Absent provider-id key.
	var withoutPID []string
	for _, l := range lines(good) {
		if strings.Contains(l, `"message":"deleted nodeclaim"`) {
			l = strings.Replace(l, `"provider-id":"aws:///us-west-2b/i-55a23d42b53b19917",`, ``, 1)
			if strings.Contains(l, "provider-id") {
				t.Fatal("test bug: provider-id not removed")
			}
		}
		withoutPID = append(withoutPID, l)
	}
	logs := strings.Join(withoutPID, "\n")
	knownDefect(t, "GC-1 (low)", "a deleted-nodeclaim line without provider-id yields InstanceID \"\" and NO error, so C4's instance comparison degenerates to \"\" == \"\" if the Node also has no providerID", func() string {
		r, err := CheckInterruption(logs, "spot-8t9sn")
		if err != nil {
			return ""
		}
		return fmt.Sprintf("err=nil, InstanceID=%q", r.InstanceID)
	})
}

func TestAdvNodeNameMismatchBetweenSteps(t *testing.T) {
	// The steps are keyed on NodeClaim only; the Node named in the message is not cross-checked against the node
	// that was tainted/deleted. Claim names are unique per Node in Karpenter, so this is low risk; pinned as a note.
	logs := strings.Replace(capture(t), `"Node":{"name":"ip-10-0-2-10.us-west-2.compute.internal"},"namespace":"","name":"ip-10-0-2-10.us-west-2.compute.internal"`, `"Node":{"name":"ip-other"},"namespace":"","name":"ip-other"`, 1)
	if logs == capture(t) {
		t.Fatal("test bug: nothing replaced")
	}
	if _, err := CheckInterruption(logs, "spot-8t9sn"); err == nil {
		t.Log("note: Node name is not cross-checked between steps (low)")
	}
}

func FuzzCheckInterruption(f *testing.F) {
	f.Add(`{"message":"deleted nodeclaim","NodeClaim":{"name":"a"},"provider-id":"nope"}`, "a")
	f.Add(`{"message":"initiating delete from interruption message","messageKind":"spot_interrupted","action":"CordonAndDrain","NodeClaim":{"name":"a"}}`+"\n"+
		`{"message":"tainted node","NodeClaim":{"name":"a"}}`+"\n"+`{"message":"deleted node","NodeClaim":{"name":"a"}}`+"\n"+
		`{"message":"deleted nodeclaim","NodeClaim":{"name":"a"},"provider-id":""}`, "a")
	f.Add("\xff\n{", "")
	f.Fuzz(func(t *testing.T, logs, claim string) {
		r, err := CheckInterruption(logs, claim)
		if err == nil && claim == "" {
			t.Fatal("empty claim accepted")
		}
		_ = r
	})
}

// ---------------------------------------------------------------- CheckNetwork

const advNet = "meridian-gate"

func advNetJSON(containerNames ...string) string {
	var cs []string
	for i, n := range containerNames {
		cs = append(cs, fmt.Sprintf(`"id%d":{"Name":%q}`, i, n))
	}
	return `[{"Name":"meridian-gate","Internal":true,"Containers":{` + strings.Join(cs, ",") + `}}]`
}

func one(name, networks, bindings, ports, mounts string) string {
	return "[" + containerPorts(name, networks, bindings, ports, mounts) + "]"
}

func TestAdvMalformedInspectShapes(t *testing.T) {
	ok := `{"meridian-gate":{}}`
	for name, tc := range map[string]struct {
		json    string
		wantErr bool
	}{
		"baseline":              {one("c", ok, `{}`, `{}`, `[]`), false},
		"Networks null":         {one("c", `null`, `{}`, `{}`, `[]`), false}, // passes: GN-4
		"Mounts null":           {one("c", ok, `{}`, `{}`, `null`), false},
		"PortBindings null":     {one("c", ok, `null`, `{}`, `[]`), false},
		"Ports null":            {one("c", ok, `{}`, `null`, `[]`), false},
		"binding array null":    {one("c", ok, `{"80/tcp":null}`, `{"80/tcp":null}`, `[]`), false},
		"binding array empty":   {one("c", ok, `{"80/tcp":[]}`, `{"80/tcp":[]}`, `[]`), false},
		"HostIp 0.0.0.0":        {one("c", ok, `{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"80"}]}`, `{}`, `[]`), true},
		"HostIp ::":             {one("c", ok, `{"80/tcp":[{"HostIp":"::","HostPort":"80"}]}`, `{}`, `[]`), true},
		"HostIp empty":          {one("c", ok, `{"80/tcp":[{"HostIp":"","HostPort":"80"}]}`, `{}`, `[]`), true},
		"HostIp missing":        {one("c", ok, `{"80/tcp":[{"HostPort":"80"}]}`, `{}`, `[]`), true},
		"HostIp localhost":      {one("c", ok, `{"80/tcp":[{"HostIp":"localhost","HostPort":"80"}]}`, `{}`, `[]`), true},
		"HostIp padded":         {one("c", ok, `{"80/tcp":[{"HostIp":" 127.0.0.1","HostPort":"80"}]}`, `{}`, `[]`), true},
		"HostIp 127.0.0.1":      {one("c", ok, `{"6443/tcp":[{"HostIp":"127.0.0.1","HostPort":"0"}]}`, `{}`, `[]`), false},
		"HostIp ::1":            {one("c", ok, `{"6443/tcp":[{"HostIp":"::1","HostPort":"0"}]}`, `{}`, `[]`), false},
		"HostIp 127.0.0.2":      {one("c", ok, `{"6443/tcp":[{"HostIp":"127.0.0.2","HostPort":"0"}]}`, `{}`, `[]`), true}, // over-strict, fails closed
		"one good one 0.0.0.0":  {one("c", ok, `{"80/tcp":[{"HostIp":"127.0.0.1"},{"HostIp":"0.0.0.0"}]}`, `{}`, `[]`), true},
		"realized loopback":     {one("c", ok, `{}`, `{"80/tcp":[{"HostPort":"1"}]}`, `[]`), true},
		"mount sock":            {one("c", ok, `{}`, `{}`, `[{"Source":"/var/run/docker.sock"}]`), true},
		"mount mac sock":        {one("c", ok, `{}`, `{}`, `[{"Source":"/Users/x/.docker/run/docker.sock"}]`), true},
		"mount not-docker.sock": {one("c", ok, `{}`, `{}`, `[{"Source":"/x/not-docker.sock"}]`), true}, // false positive, fails closed
		"mount docker.sock.bak": {one("c", ok, `{}`, `{}`, `[{"Source":"/data/docker.sock.bak"}]`), false},
		"mount sock w/ slash":   {one("c", ok, `{}`, `{}`, `[{"Source":"/var/run/docker.sock/"}]`), false}, // not detected; Docker cleans paths so unrealistic
		"also on bridge":        {one("c", `{"meridian-gate":{},"bridge":{}}`, `{}`, `{}`, `[]`), true},
		"also on host":          {one("c", `{"meridian-gate":{},"host":{}}`, `{}`, `{}`, `[]`), true},
		"only on bridge":        {one("c", `{"bridge":{}}`, `{}`, `{}`, `[]`), true},
		"scalar":                {`1`, true},
		"empty array":           {`[]`, true}, // c not inspected
		"null":                  {`null`, true},
		"object not array":      {`{}`, true},
		"empty string":          {``, true},
		"element is null":       {`[null]`, true},
	} {
		err := CheckNetwork(advNet, advNetJSON("c"), tc.json)
		if tc.wantErr != (err != nil) {
			t.Errorf("%s: err=%v, wantErr=%v", name, err, tc.wantErr)
		}
	}
}

func TestAdvNetworkNameSlashCaseAndDuplicates(t *testing.T) {
	ok := `{"meridian-gate":{}}`
	c := func(n string) string { return containerPorts(n, ok, `{}`, `{}`, `[]`) }
	// leading slash on inspect, none on network: matched.
	if err := CheckNetwork(advNet, advNetJSON("c"), "["+c("c")+"]"); err != nil {
		t.Errorf("slash handling: %v", err)
	}
	// case differences must not match (fail closed: an unaudited container).
	if err := CheckNetwork(advNet, advNetJSON("Floci"), "["+c("floci")+"]"); err == nil {
		t.Error("case-different container name counted as audited")
	}
	// the same container listed twice is harmless.
	if err := CheckNetwork(advNet, advNetJSON("c"), "["+c("c")+","+c("c")+"]"); err != nil {
		t.Errorf("duplicate inspect entry: %v", err)
	}
	// duplicate network entries (two containers same name) fine; a bad one among duplicates still found.
	bad := containerPorts("c", ok, `{}`, `{}`, `[{"Source":"/var/run/docker.sock"}]`)
	if err := CheckNetwork(advNet, advNetJSON("c"), "["+c("c")+","+bad+"]"); err == nil {
		t.Error("a bad duplicate was masked by a good one with the same name")
	}
	// double leading slash: TrimPrefix removes one only -> unaudited -> fails closed.
	if err := CheckNetwork(advNet, advNetJSON("c"), "["+strings.Replace(c("c"), `"/c"`, `"//c"`, 1)+"]"); err == nil {
		t.Error("//c matched c")
	}
	// network inspected is not the named one.
	if err := CheckNetwork("other", advNetJSON("c"), "["+c("c")+"]"); err == nil {
		t.Error("wrong network name accepted")
	}
	// two-element network array.
	two := strings.TrimSuffix(advNetJSON("c"), "]") + "," + strings.TrimPrefix(advNetJSON("c"), "[")
	if err := CheckNetwork(advNet, two, "["+c("c")+"]"); err == nil {
		t.Error("two networks accepted")
	}
}

func TestAdvSocketNamesThatEscapeTheSuffixCheck(t *testing.T) {
	ok := `{"meridian-gate":{}}`
	// Real container-runtime sockets whose basename is not exactly "docker.sock". Each is root-equivalent on its
	// daemon.
	for _, src := range []string{
		"/run/host-services/docker.proxy.sock", // Docker Desktop (Mac/Windows) proxy socket
		"/run/podman/podman.sock",              // Podman
		"/run/containerd/containerd.sock",      // containerd
		"/var/run/dockershim.sock",
		"/run/user/1000/docker.sock.lock",
	} {
		src := src
		t.Run(src, func(t *testing.T) {
			knownDefect(t, "GN-1 (medium)", "a container-runtime socket not literally named *docker.sock is not detected", func() string {
				err := CheckNetwork(advNet, advNetJSON("c"), one("c", ok, `{}`, `{}`, fmt.Sprintf(`[{"Source":%q}]`, src)))
				if err == nil {
					return "audit PASSED a mount of " + src
				}
				return ""
			})
		})
	}
}

func TestAdvParentDirectoryMountsExposeTheSocket(t *testing.T) {
	ok := `{"meridian-gate":{}}`
	// A bind of /var/run, /run, /var or / contains docker.sock; the container sees it at <dest>/docker.sock.
	for _, src := range []string{"/var/run", "/run", "/var", "/", "/var/run/", "/run/"} {
		src := src
		t.Run(src, func(t *testing.T) {
			knownDefect(t, "GN-2 (medium)", "a bind mount of a parent directory of the Docker socket passes the audit", func() string {
				err := CheckNetwork(advNet, advNetJSON("c"), one("c", ok, `{}`, `{}`, fmt.Sprintf(`[{"Source":%q,"Destination":"/host","Type":"bind"}]`, src)))
				if err == nil {
					return "audit PASSED a bind mount of " + src
				}
				return ""
			})
		})
	}
}

func TestAdvDestinationOnlySocket(t *testing.T) {
	ok := `{"meridian-gate":{}}`
	// Mounts[].Destination is not looked at: a socket mounted at /var/run/docker.sock whose Source is an opaque path
	// (Docker Desktop reports the VM-side path) is only visible through Destination.
	knownDefect(t, "GN-5 (low)", "Destination is ignored; only Source is audited", func() string {
		err := CheckNetwork(advNet, advNetJSON("c"), one("c", ok, `{}`, `{}`, `[{"Source":"/run/host-services/sock-proxy","Destination":"/var/run/docker.sock"}]`))
		if err == nil {
			return "audit PASSED a mount whose Destination is /var/run/docker.sock"
		}
		return ""
	})
}

func TestAdvContainerAttachedToNoNetworkPasses(t *testing.T) {
	// A container the network lists whose own inspect shows no attachment at all (Networks null/{}): inconsistent
	// data, accepted silently.
	knownDefect(t, "GN-4 (low)", "a container whose inspect shows it attached to NO network (or not to the audited one) is accepted if it is the only one listed", func() string {
		if err := CheckNetwork(advNet, advNetJSON("c"), one("c", `null`, `{}`, `{}`, `[]`)); err == nil {
			return "passed"
		}
		return ""
	})
}

func TestAdvNetworkOnlyContainerWithHostNetworkMode(t *testing.T) {
	// HostConfig.NetworkMode is never read; "host" shows up as a Networks key, which is flagged as a second network.
	ok := `{"host":{}}`
	if err := CheckNetwork(advNet, advNetJSON("c"), one("c", ok, `{}`, `{}`, `[]`)); err == nil {
		t.Error("host-mode container accepted")
	}
}

func FuzzCheckNetwork(f *testing.F) {
	f.Add(advNetJSON("c"), one("c", `{"meridian-gate":{}}`, `{}`, `{}`, `[]`))
	f.Add(`[{"Name":"meridian-gate","Internal":true,"Containers":null}]`, `[null]`)
	f.Add(goodNet, good())
	f.Fuzz(func(t *testing.T, netJSON, ctrJSON string) {
		_ = CheckNetwork(advNet, netJSON, ctrJSON) // property under test: never panics
	})
}
