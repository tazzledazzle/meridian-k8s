package catalog

import "testing"

func TestLookupAndSortOrder(t *testing.T) {
	if _, ok := Lookup("m5.large"); !ok {
		t.Fatal("m5.large missing")
	}
	if _, ok := Lookup("nope.large"); ok {
		t.Fatal("unexpected type")
	}
	for i := 1; i < len(Types); i++ {
		if Types[i-1].Name >= Types[i].Name {
			t.Fatalf("Types not sorted at %s", Types[i].Name)
		}
	}
}

func TestSpotPriceIsBelowOnDemandAndVariesByZone(t *testing.T) {
	m5, _ := Lookup("m5.large")
	a, b := SpotPrice(m5, "us-west-2a"), SpotPrice(m5, "us-west-2b")
	if a >= m5.OnDemand || b >= m5.OnDemand {
		t.Fatalf("spot %v/%v should be below on-demand %v", a, b, m5.OnDemand)
	}
	if a == b {
		t.Fatal("zones should differ in spot price")
	}
	if SpotPrice(m5, "us-west-2a") != a {
		t.Fatal("spot price must be deterministic")
	}
}

// HasAZ and Region are not covered by the plan's tests; cover them too.
func TestHasAZAndRegion(t *testing.T) {
	if Region != "us-west-2" {
		t.Fatalf("Region = %s", Region)
	}
	if !HasAZ("us-west-2c") || HasAZ("us-east-1a") {
		t.Fatal("HasAZ wrong")
	}
	for _, z := range AZs {
		if z.Name[:len(Region)] != Region || z.ID == "" {
			t.Fatalf("bad AZ %+v", z)
		}
	}
}
