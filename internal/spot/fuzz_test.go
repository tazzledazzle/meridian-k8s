package spot

import (
	"encoding/json"
	"testing"
	"time"
)

// FuzzEvent: any region and instance id must never panic and must yield JSON
// Karpenter's struct decodes and routes to the spot parser. Inputs that pass
// validation must round-trip exactly.
func FuzzEvent(f *testing.F) {
	f.Add("us-west-2", "i-0123456789abcdef0")
	f.Add("", "")
	f.Add(`"}{`, "i-\x00\xff")
	f.Add("us/west", "i-abc&Action=TerminateInstances")
	f.Fuzz(func(t *testing.T, region, id string) {
		b := Event(region, id, time.Unix(0, 0))
		var m karpSpotMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("Karpenter's struct cannot decode %q: %v", b, err)
		}
		if k := (karpParserKey{m.Version, m.Source, m.DetailType}); k != spotRegistryKey {
			t.Fatalf("not routed to the spot parser: %+v", k)
		}
		if ValidateInstanceID(id) == nil && (m.Detail.InstanceID != id || len(m.Resources) != 1) {
			t.Fatalf("valid id %q did not round-trip: %+v", id, m)
		}
		in := &Injector{Endpoint: "http://127.0.0.1:1", Region: region, QueueName: "q", Access: "AK", Secret: "SK"}
		if in.Validate() == nil && m.Region != region {
			t.Fatalf("valid region %q did not round-trip: %q", region, m.Region)
		}
	})
}
