package spot

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"
)

// The structs below are copied verbatim (fields and json tags) from Karpenter
// v1.14.1: messages.Metadata (pkg/controllers/interruption/messages/types.go)
// and spotinterruption.Message / Detail (messages/spotinterruption/model.go).
type karpMetadata struct {
	Account    string    `json:"account"`
	DetailType string    `json:"detail-type"`
	ID         string    `json:"id"`
	Region     string    `json:"region"`
	Resources  []string  `json:"resources"`
	Source     string    `json:"source"`
	Time       time.Time `json:"time"`
	Version    string    `json:"version"`
}

type karpSpotMessage struct {
	karpMetadata
	Detail karpDetail `json:"detail"`
}

type karpDetail struct {
	InstanceID     string `json:"instance-id"`
	InstanceAction string `json:"instance-action"`
}

// karpParserKey is parserKey from pkg/controllers/interruption/parser.go; the
// registry is a map keyed on it, and spotinterruption.Parser registers
// Version "0", Source "aws.ec2", DetailType "EC2 Spot Instance Interruption Warning".
type karpParserKey struct{ Version, Source, DetailType string }

var spotRegistryKey = karpParserKey{"0", "aws.ec2", "EC2 Spot Instance Interruption Warning"}

const goldenID = "i-0123456789abcdef0"

func loadGolden(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/spot_interruption_warning.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "_comment") // provenance note, not part of the event
	b, _ := json.Marshal(m)
	return b
}

func decode(t *testing.T, b []byte) karpSpotMessage {
	t.Helper()
	var m karpSpotMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Karpenter's struct cannot decode %s: %v", b, err)
	}
	return m
}

func keys(t *testing.T, b []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func TestGoldenSampleDecodesWithKarpentersStructAndRoutes(t *testing.T) {
	g := decode(t, loadGolden(t))
	if got := (karpParserKey{g.Version, g.Source, g.DetailType}); got != spotRegistryKey {
		t.Fatalf("golden key %+v != registry key %+v", got, spotRegistryKey)
	}
	if g.Detail.InstanceID != goldenID || g.Detail.InstanceAction != "terminate" {
		t.Fatalf("golden detail = %+v", g.Detail)
	}
}

func TestEventMatchesTheGoldenSampleFieldForField(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	golden, got := loadGolden(t), Event("us-west-2", goldenID, at)
	g, e := decode(t, golden), decode(t, got)

	// Same top-level and detail key sets: nothing Karpenter reads is missing, nothing foreign is added.
	if !reflect.DeepEqual(keys(t, golden), keys(t, got)) {
		t.Errorf("top-level keys %v != golden %v", keys(t, got), keys(t, golden))
	}
	var gd, ed struct{ Detail json.RawMessage }
	_ = json.Unmarshal(golden, &gd)
	_ = json.Unmarshal(got, &ed)
	if !reflect.DeepEqual(keys(t, ed.Detail), keys(t, gd.Detail)) {
		t.Errorf("detail keys %v != golden %v", keys(t, ed.Detail), keys(t, gd.Detail))
	}

	// Routing: the registry key Karpenter looks the parser up by.
	if k := (karpParserKey{e.Version, e.Source, e.DetailType}); k != spotRegistryKey {
		t.Errorf("Karpenter would not route %+v to the spot parser (%+v)", k, spotRegistryKey)
	}
	// Fields whose value is fixed by the fixture must be identical.
	if e.Version != g.Version || e.Source != g.Source || e.DetailType != g.DetailType ||
		e.Region != g.Region || e.Detail != g.Detail || !reflect.DeepEqual(e.Resources, g.Resources) {
		t.Errorf("event\n %+v\ndiffers from golden\n %+v", e, g)
	}
	// Generated fields only need the right shape and values.
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(e.ID) {
		t.Errorf("id %q is not a UUID", e.ID)
	}
	if !regexp.MustCompile(`^[0-9]{12}$`).MatchString(e.Account) {
		t.Errorf("account %q is not 12 digits", e.Account)
	}
	if !e.Time.Equal(at) {
		t.Errorf("time = %v, want %v", e.Time, at)
	}
}

func TestEventIDsAreUnique(t *testing.T) {
	a, b := decode(t, Event("us-west-2", goldenID, time.Now())), decode(t, Event("us-west-2", goldenID, time.Now()))
	if a.ID == b.ID {
		t.Fatal("two events share an id; SQS consumers may deduplicate on it")
	}
}
