package ec2sim

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

// names lists the instance types of a response.
func names(r respXML) []string {
	var out []string
	for _, it := range r.Items {
		out = append(out, it.Type)
	}
	return out
}

func allNames() []string {
	var out []string
	for _, c := range catalog.Types {
		out = append(out, c.Name)
	}
	return out
}

// errXML is the EC2 error envelope.
type errXML struct {
	XMLName xml.Name `xml:"Response"`
	Code    string   `xml:"Errors>Error>Code"`
	Message string   `xml:"Errors>Error>Message"`
	ReqID   string   `xml:"RequestID"`
}

func wantError(t *testing.T, code int, body string, wantStatus int, wantCode string) errXML {
	t.Helper()
	var e errXML
	if err := xml.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("error body is not EC2 XML: %v\n%s", err, body)
	}
	if code != wantStatus || e.Code != wantCode || e.ReqID == "" {
		t.Fatalf("got %d %q (req %q), want %d %q\n%s", code, e.Code, e.ReqID, wantStatus, wantCode, body)
	}
	return e
}

func TestInstanceTypeParameterSelectsTypesInCatalogOrderOnce(t *testing.T) {
	code, body := call(t, New(catalog.Types), "DescribeInstanceTypes",
		"InstanceType.1", "t3.medium", "InstanceType.2", "c5.large", "InstanceType.3", "t3.medium")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := strings.Join(names(parse(t, body)), ","); got != "c5.large,t3.medium" {
		t.Errorf("got %s", got)
	}
}

func TestUnknownInstanceTypeIsInvalidInstanceType(t *testing.T) {
	code, body := call(t, New(catalog.Types), "DescribeInstanceTypes", "InstanceType.1", "m5.large", "InstanceType.2", "m6gd.large")
	e := wantError(t, code, body, 400, "InvalidInstanceType")
	if !strings.Contains(e.Message, "m6gd.large") || strings.Contains(e.Message, "m5.large") {
		t.Errorf("message should name only the unknown type: %q", e.Message)
	}
}

func TestFilters(t *testing.T) {
	h := New(catalog.Types)
	all := strings.Join(allNames(), ",")
	cases := []struct {
		name string
		kv   []string
		want string
	}{
		{"arch arm64", []string{"Filter.1.Name", "processor-info.supported-architecture", "Filter.1.Value.1", "arm64"}, "m6g.large,m6g.xlarge"},
		{"instance-type wildcard", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "t3.*"}, "t3.large,t3.medium"},
		{"values are OR", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "t3.medium", "Filter.1.Value.2", "r5.large"}, "r5.large,t3.medium"},
		{"filters are AND", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "m*", "Filter.2.Name", "processor-info.supported-architecture", "Filter.2.Value.1", "arm64"}, "m6g.large,m6g.xlarge"},
		{"current generation", []string{"Filter.1.Name", "current-generation", "Filter.1.Value.1", "true"}, all},
		{"not current generation", []string{"Filter.1.Name", "current-generation", "Filter.1.Value.1", "false"}, ""},
		{"usage class spot", []string{"Filter.1.Name", "supported-usage-class", "Filter.1.Value.1", "spot"}, all},
		{"usage class unknown", []string{"Filter.1.Name", "supported-usage-class", "Filter.1.Value.1", "capacity-block"}, ""},
		{"virtualization hvm", []string{"Filter.1.Name", "supported-virtualization-type", "Filter.1.Value.1", "hvm"}, all},
		{"virtualization paravirtual", []string{"Filter.1.Name", "supported-virtualization-type", "Filter.1.Value.1", "paravirtual"}, ""},
		{"hypervisor nitro", []string{"Filter.1.Name", "hypervisor", "Filter.1.Value.1", "nitro"}, all},
		{"hypervisor xen", []string{"Filter.1.Name", "hypervisor", "Filter.1.Value.1", "xen"}, ""},
		{"bare metal", []string{"Filter.1.Name", "bare-metal", "Filter.1.Value.1", "true"}, ""},
		{"burstable", []string{"Filter.1.Name", "burstable-performance-supported", "Filter.1.Value.1", "true"}, "t3.large,t3.medium"},
		{"vcpus", []string{"Filter.1.Name", "vcpu-info.default-vcpus", "Filter.1.Value.1", "8"}, "c5.2xlarge,m5.2xlarge"},
		{"memory", []string{"Filter.1.Name", "memory-info.size-in-mib", "Filter.1.Value.1", "4096"}, "c5.large,t3.medium"},
		{"filter and InstanceType combine", []string{"InstanceType.1", "m6g.large", "InstanceType.2", "m5.large", "Filter.1.Name", "processor-info.supported-architecture", "Filter.1.Value.1", "arm64"}, "m6g.large"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := call(t, h, "DescribeInstanceTypes", c.kv...)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			if got := strings.Join(names(parse(t, body)), ","); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestBadFiltersAreInvalidParameterValue(t *testing.T) {
	h := New(catalog.Types)
	for name, kv := range map[string][]string{
		"unknown filter name": {"Filter.1.Name", "nope", "Filter.1.Value.1", "x"},
		"missing name":        {"Filter.1.Value.1", "x"},
		"no values":           {"Filter.1.Name", "instance-type"},
	} {
		t.Run(name, func(t *testing.T) {
			code, body := call(t, h, "DescribeInstanceTypes", kv...)
			wantError(t, code, body, 400, "InvalidParameterValue")
		})
	}
}
