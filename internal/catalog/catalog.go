// Package catalog is the platform's fixed region data: availability zones and
// instance types (CPU, memory, ENI limits) with approximate list prices.
// node-registrar reads it to size KWOK nodes. If S0 requires custom services,
// ec2-sim and pricing-facade read it too, so they always agree on what exists
// and what it costs.
package catalog

import "sort"

// Region is the only region the platform serves.
const Region = "us-west-2"

// AZ is one availability zone.
type AZ struct {
	Name string // us-west-2a
	ID   string // usw2-az1 (the zone ID, stable across accounts)
}

// AZs are the zones, in order.
var AZs = []AZ{
	{"us-west-2a", "usw2-az2"},
	{"us-west-2b", "usw2-az1"},
	{"us-west-2c", "usw2-az3"},
}

// Type is one instance type.
type Type struct {
	Name       string
	VCPU       int
	MemoryMiB  int
	Arch       string  // x86_64 | arm64
	MaxENIs    int     // maximum network interfaces
	IPv4PerENI int     // IPv4 addresses per interface
	OnDemand   float64 // USD per hour, approximate Linux list price
}

// Types is sorted by name. Prices are approximate public list prices, used only
// so that cost-based decisions in Karpenter are plausible.
var Types = []Type{
	{"c5.2xlarge", 8, 16384, "x86_64", 4, 15, 0.34},
	{"c5.large", 2, 4096, "x86_64", 3, 10, 0.085},
	{"c5.xlarge", 4, 8192, "x86_64", 4, 15, 0.17},
	{"m5.2xlarge", 8, 32768, "x86_64", 4, 15, 0.384},
	{"m5.large", 2, 8192, "x86_64", 3, 10, 0.096},
	{"m5.xlarge", 4, 16384, "x86_64", 4, 15, 0.192},
	{"m6g.large", 2, 8192, "arm64", 3, 10, 0.077},
	{"m6g.xlarge", 4, 16384, "arm64", 4, 15, 0.154},
	{"r5.large", 2, 16384, "x86_64", 3, 10, 0.126},
	{"r5.xlarge", 4, 32768, "x86_64", 4, 15, 0.252},
	{"t3.large", 2, 8192, "x86_64", 3, 12, 0.0832},
	{"t3.medium", 2, 4096, "x86_64", 3, 6, 0.0416},
}

func init() {
	// Lookup and binary-search callers rely on this order; enforce it once.
	sort.Slice(Types, func(i, j int) bool { return Types[i].Name < Types[j].Name })
}

// Lookup finds an instance type by name.
func Lookup(name string) (Type, bool) {
	for _, t := range Types {
		if t.Name == name {
			return t, true
		}
	}
	return Type{}, false
}

// HasAZ reports whether the zone name exists.
func HasAZ(name string) bool {
	for _, z := range AZs {
		if z.Name == name {
			return true
		}
	}
	return false
}

// SpotPrice is a deterministic spot price: about 30% of on-demand, with a small
// per-zone offset so zones differ and Karpenter has something to choose between.
func SpotPrice(t Type, az string) float64 {
	offset := 0.0
	for i, z := range AZs {
		if z.Name == az {
			offset = float64(i) * 0.01
		}
	}
	p := t.OnDemand * (0.30 + offset)
	// Round to 4 decimals like the real API.
	return float64(int(p*10000+0.5)) / 10000
}
