package ec2sim

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

const ec2NS = "http://ec2.amazonaws.com/doc/2016-11-15/"

// itemXML mirrors the InstanceTypeInfo fields Karpenter reads.
type itemXML struct {
	Type       string   `xml:"instanceType"`
	CurrentGen bool     `xml:"currentGeneration"`
	Usage      []string `xml:"supportedUsageClasses>item"`
	Virt       []string `xml:"supportedVirtualizationTypes>item"`
	Hypervisor string   `xml:"hypervisor"`
	BareMetal  bool     `xml:"bareMetal"`
	Burstable  bool     `xml:"burstablePerformanceSupported"`
	Arch       []string `xml:"processorInfo>supportedArchitectures>item"`
	VCPUs      int      `xml:"vCpuInfo>defaultVCpus"`
	Cores      int      `xml:"vCpuInfo>defaultCores"`
	Threads    int      `xml:"vCpuInfo>defaultThreadsPerCore"`
	MemMiB     int      `xml:"memoryInfo>sizeInMiB"`
	MaxENIs    int      `xml:"networkInfo>maximumNetworkInterfaces"`
	IPv4       int      `xml:"networkInfo>ipv4AddressesPerInterface"`
	IPv6       int      `xml:"networkInfo>ipv6AddressesPerInterface"`
	IPv6OK     bool     `xml:"networkInfo>ipv6Supported"`
	EFA        bool     `xml:"networkInfo>efaSupported"`
	CardIdx    int      `xml:"networkInfo>defaultNetworkCardIndex"`
	Cards      []struct {
		Index int `xml:"networkCardIndex"`
		Max   int `xml:"maximumNetworkInterfaces"`
	} `xml:"networkInfo>networkCards>item"`
	EBSOpt  string    `xml:"ebsInfo>ebsOptimizedSupport"`
	Storage bool      `xml:"instanceStorageSupported"`
	GPU     *struct{} `xml:"gpuInfo"`
	Infer   *struct{} `xml:"inferenceAcceleratorInfo"`
}

type respXML struct {
	XMLName xml.Name  `xml:"DescribeInstanceTypesResponse"`
	ReqID   string    `xml:"requestId"`
	Items   []itemXML `xml:"instanceTypeSet>item"`
	Next    string    `xml:"nextToken"`
}

// call posts an EC2 query request (alternating key, value args) and returns status and body.
func call(t *testing.T, h http.Handler, action string, kv ...string) (int, string) {
	t.Helper()
	form := url.Values{"Action": {action}, "Version": {"2016-11-15"}}
	for i := 0; i+1 < len(kv); i += 2 {
		form.Add(kv[i], kv[i+1])
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func parse(t *testing.T, body string) respXML {
	t.Helper()
	var r respXML
	if err := xml.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("response is not well-formed XML: %v\n%s", err, body)
	}
	return r
}

func TestListsEveryCatalogTypeOnceInCatalogOrder(t *testing.T) {
	h := New(catalog.Types)
	code, body := call(t, h, "DescribeInstanceTypes")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, `xmlns="`+ec2NS+`"`) {
		t.Errorf("missing EC2 namespace: %s", body)
	}
	r := parse(t, body)
	if len(r.Items) != len(catalog.Types) {
		t.Fatalf("got %d types, want %d", len(r.Items), len(catalog.Types))
	}
	for i, it := range r.Items {
		if it.Type != catalog.Types[i].Name {
			t.Errorf("item %d = %s, want %s", i, it.Type, catalog.Types[i].Name)
		}
	}
	if r.ReqID == "" {
		t.Error("no requestId")
	}
	if r.Next != "" {
		t.Errorf("unexpected nextToken %q without MaxResults", r.Next)
	}
}

func TestEachTypeCarriesKarpenterFieldsFromCatalog(t *testing.T) {
	_, body := call(t, New(catalog.Types), "DescribeInstanceTypes")
	for i, it := range parse(t, body).Items {
		c := catalog.Types[i]
		if it.VCPUs != c.VCPU || it.MemMiB != c.MemoryMiB || len(it.Arch) != 1 || it.Arch[0] != c.Arch {
			t.Errorf("%s: cpu/mem/arch = %d/%d/%v", c.Name, it.VCPUs, it.MemMiB, it.Arch)
		}
		// networkInfo level, not only inside networkCards: this is the whole point of the service.
		if it.MaxENIs != c.MaxENIs || it.IPv4 != c.IPv4PerENI || it.IPv6 != c.IPv4PerENI || !it.IPv6OK {
			t.Errorf("%s: networkInfo = %d/%d/%d", c.Name, it.MaxENIs, it.IPv4, it.IPv6)
		}
		if len(it.Cards) != 1 || it.Cards[0].Max != c.MaxENIs || it.CardIdx != 0 {
			t.Errorf("%s: networkCards = %+v", c.Name, it.Cards)
		}
		if got := it.MaxENIs*(it.IPv4-1) + 2; got != c.MaxENIs*(c.IPv4PerENI-1)+2 {
			t.Errorf("%s: pod density mismatch %d", c.Name, got)
		}
		if strings.Join(it.Usage, ",") != "on-demand,spot" || strings.Join(it.Virt, ",") != "hvm" {
			t.Errorf("%s: usage %v virt %v", c.Name, it.Usage, it.Virt)
		}
		if !it.CurrentGen || it.Hypervisor != "nitro" || it.BareMetal || it.EFA || it.Storage || it.EBSOpt == "" {
			t.Errorf("%s: flags %+v", c.Name, it)
		}
		if it.Cores*it.Threads != it.VCPUs || it.Threads != 1 && it.Threads != 2 {
			t.Errorf("%s: cores %d threads %d vcpus %d", c.Name, it.Cores, it.Threads, it.VCPUs)
		}
		if it.Burstable != strings.HasPrefix(c.Name, "t") {
			t.Errorf("%s: burstable = %v", c.Name, it.Burstable)
		}
		if it.GPU != nil || it.Infer != nil {
			t.Errorf("%s: unexpected accelerator info", c.Name)
		}
	}
}
