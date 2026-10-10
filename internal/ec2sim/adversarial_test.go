package ec2sim

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/catalog"
	"github.com/tazzledazzle/meridian/internal/noderegistrar"
)

// ---------------------------------------------------------------- helpers

func do(h http.Handler, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func form(kv ...string) string {
	v := url.Values{"Action": {"DescribeInstanceTypes"}, "Version": {"2016-11-15"}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return v.Encode()
}

// wellFormed parses every token strictly; a mismatched or unescaped document fails.
func wellFormed(t *testing.T, b []byte) {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = true
	depth := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("not well-formed XML: %v\n%.300s", err, b)
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if depth != 0 {
		t.Fatalf("unbalanced XML: %.300s", b)
	}
}

var reqIDRe = regexp.MustCompile(`<requestId>[^<]*</requestId>`)

func stripReq(s string) string { return reqIDRe.ReplaceAllString(s, "<requestId/>") }

// ec2ErrorComponents is a verbatim copy of aws-sdk-go-v2/aws/protocol/ec2query.ErrorComponents,
// which is what the SDK's EC2 deserializer uses (the SDK module is not a dependency of this repo).
type ec2ErrorComponents struct {
	Code      string `xml:"Errors>Error>Code"`
	Message   string `xml:"Errors>Error>Message"`
	RequestID string `xml:"RequestID"`
}

func sdkErr(t *testing.T, rec *httptest.ResponseRecorder) ec2ErrorComponents {
	t.Helper()
	var er ec2ErrorComponents
	if err := xml.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&er); err != nil && err != io.EOF {
		t.Fatalf("SDK decoder cannot read error body: %v\n%s", err, rec.Body)
	}
	return er
}

// ---------------------------------------------------------------- 2. Karpenter fidelity

// sdkInstanceTypeInfo is the subset of aws-sdk-go-v2 ec2 types.InstanceTypeInfo, with the SDK's
// pointer fields so that an absent element is distinguishable from zero (aws.ToInt32(nil) == 0
// is exactly how a missing networkInfo.maximumNetworkInterfaces silently yields 2 pods).
type sdkInstanceTypeInfo struct {
	InstanceType         *string  `xml:"instanceType"`
	CurrentGeneration    *bool    `xml:"currentGeneration"`
	SupportedUsageClass  []string `xml:"supportedUsageClasses>item"`
	SupportedVirt        []string `xml:"supportedVirtualizationTypes>item"`
	Hypervisor           *string  `xml:"hypervisor"`
	BareMetal            *bool    `xml:"bareMetal"`
	SupportedArch        []string `xml:"processorInfo>supportedArchitectures>item"`
	DefaultVCpus         *int32   `xml:"vCpuInfo>defaultVCpus"`
	SizeInMiB            *int64   `xml:"memoryInfo>sizeInMiB"`
	MaxENIs              *int32   `xml:"networkInfo>maximumNetworkInterfaces"`
	IPv4PerENI           *int32   `xml:"networkInfo>ipv4AddressesPerInterface"`
	CardMax              []int32  `xml:"networkInfo>networkCards>item>maximumNetworkInterfaces"`
	GPUs                 []string `xml:"gpuInfo>gpus>item>name"`
	InferenceAccelerator []string `xml:"inferenceAcceleratorInfo>accelerators>item>name"`
}

type sdkOutput struct {
	XMLName   xml.Name              `xml:"DescribeInstanceTypesResponse"`
	Items     []sdkInstanceTypeInfo `xml:"instanceTypeSet>item"`
	NextToken *string               `xml:"nextToken"`
}

type karpenterType struct {
	name          string
	vcpu          int64
	memoryBytes   int64
	pods          int64
	archLabel     string
	capacityTypes []string
}

// deriveKarpenter re-implements what pkg/providers/instancetype does with an InstanceTypeInfo.
// It returns ok=false when Karpenter would drop the type.
func deriveKarpenter(i sdkInstanceTypeInfo) (kt karpenterType, ok bool, why string) {
	if i.InstanceType == nil || i.CurrentGeneration == nil || !*i.CurrentGeneration {
		return kt, false, "not current generation"
	}
	if i.BareMetal != nil && *i.BareMetal {
		return kt, false, "bare metal"
	}
	hvm := false
	for _, v := range i.SupportedVirt {
		hvm = hvm || v == "hvm"
	}
	if !hvm {
		return kt, false, "no hvm"
	}
	if i.Hypervisor == nil || *i.Hypervisor != "nitro" {
		return kt, false, "not nitro"
	}
	if i.DefaultVCpus == nil || i.SizeInMiB == nil || i.MaxENIs == nil || i.IPv4PerENI == nil {
		return kt, false, "missing field"
	}
	kt.name = *i.InstanceType
	kt.vcpu = int64(*i.DefaultVCpus)
	kt.memoryBytes = *i.SizeInMiB * 1024 * 1024
	kt.pods = int64(*i.MaxENIs)*(int64(*i.IPv4PerENI)-1) + 2
	for _, a := range i.SupportedArch {
		switch a {
		case "x86_64":
			kt.archLabel = "amd64"
		case "arm64":
			kt.archLabel = "arm64"
		}
	}
	for _, u := range i.SupportedUsageClass {
		if u == "on-demand" || u == "spot" {
			kt.capacityTypes = append(kt.capacityTypes, u)
		}
	}
	return kt, kt.archLabel != "", "no known arch"
}

func TestKarpenterDerivationMatchesNodeRegistrarForEveryType(t *testing.T) {
	h := New(catalog.Types)
	rec := do(h, "POST", "/", form(), nil)
	var out sdkOutput
	if err := xml.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != len(catalog.Types) {
		t.Fatalf("%d items, want %d", len(out.Items), len(catalog.Types))
	}
	for _, info := range out.Items {
		kt, ok, why := deriveKarpenter(info)
		name := "?"
		if info.InstanceType != nil {
			name = *info.InstanceType
		}
		if !ok {
			t.Errorf("%s: Karpenter would drop the type: %s", name, why)
			continue
		}
		if len(info.GPUs) != 0 || len(info.InferenceAccelerator) != 0 {
			t.Errorf("%s: unexpected accelerators", name)
		}
		if strings.Join(kt.capacityTypes, ",") != "on-demand,spot" {
			t.Errorf("%s: capacity types %v", name, kt.capacityTypes)
		}
		var sum int32
		for _, c := range info.CardMax {
			sum += c
		}
		if sum != *info.MaxENIs {
			t.Errorf("%s: sum of networkCards %d != networkInfo %d", name, sum, *info.MaxENIs)
		}
		node, err := noderegistrar.KWOK{}.Node(noderegistrar.Instance{ID: "i-1", Type: name, AZ: "us-west-2a", PrivateIP: "10.0.0.1", State: "running"})
		if err != nil {
			t.Fatal(err)
		}
		if got := node.Allocatable["pods"]; got != strconv.FormatInt(kt.pods, 10) {
			t.Errorf("%s: Karpenter pods %d, registrar allocatable %s", name, kt.pods, got)
		}
		if got := node.Allocatable["cpu"]; got != strconv.FormatInt(kt.vcpu, 10) {
			t.Errorf("%s: Karpenter cpu %d, registrar %s", name, kt.vcpu, got)
		}
		if got := node.Allocatable["memory"]; got != strconv.FormatInt(kt.memoryBytes>>20, 10)+"Mi" {
			t.Errorf("%s: Karpenter memory %d, registrar %s", name, kt.memoryBytes, got)
		}
		if got := node.Labels["kubernetes.io/arch"]; got != kt.archLabel {
			t.Errorf("%s: Karpenter arch %s, registrar label %s", name, kt.archLabel, got)
		}
		if node.Labels["node.kubernetes.io/instance-type"] != name {
			t.Errorf("%s: instance-type label %q", name, node.Labels["node.kubernetes.io/instance-type"])
		}
	}
}

// Karpenter pages the full listing with MaxResults=100 and a set of supported filters; all 12 must come back.
func TestKarpenterStyleFilteredListing(t *testing.T) {
	h := New(catalog.Types)
	rec := do(h, "POST", "/", form(
		"MaxResults", "100",
		"Filter.1.Name", "supported-virtualization-type", "Filter.1.Value.1", "hvm",
		"Filter.2.Name", "processor-info.supported-architecture", "Filter.2.Value.1", "x86_64", "Filter.2.Value.2", "arm64",
		"Filter.3.Name", "supported-usage-class", "Filter.3.Value.1", "on-demand", "Filter.3.Value.2", "spot",
		"Filter.4.Name", "current-generation", "Filter.4.Value.1", "true",
		"Filter.5.Name", "bare-metal", "Filter.5.Value.1", "false",
	), nil)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out sdkOutput
	if err := xml.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != len(catalog.Types) || out.NextToken != nil {
		t.Fatalf("items=%d next=%v", len(out.Items), out.NextToken)
	}
}

// ---------------------------------------------------------------- 3. paging

func synth(n int) []catalog.Type {
	var ts []catalog.Type
	for i := 0; i < n; i++ {
		ts = append(ts, catalog.Type{Name: fmt.Sprintf("x%03d.large", i), VCPU: 2, MemoryMiB: 4096, Arch: "x86_64", MaxENIs: 3, IPv4PerENI: 10})
	}
	return ts
}

func walk(t *testing.T, h http.Handler, size int, extra ...string) (pages [][]string) {
	t.Helper()
	token := ""
	for i := 0; i < 1000; i++ {
		kv := append([]string{"MaxResults", strconv.Itoa(size)}, extra...)
		if token != "" {
			kv = append(kv, "NextToken", token)
		}
		rec := do(h, "POST", "/", form(kv...), nil)
		if rec.Code != 200 {
			t.Fatalf("size %d page %d: %d %s", size, i, rec.Code, rec.Body)
		}
		r := parse(t, rec.Body.String())
		if len(r.Items) == 0 {
			t.Fatalf("size %d page %d: empty page (token=%q)", size, i, r.Next)
		}
		pages = append(pages, names(r))
		if r.Next == "" {
			return pages
		}
		token = r.Next
	}
	t.Fatalf("size %d: did not terminate", size)
	return nil
}

func TestEveryPageSizeWalksEveryTypeExactlyOnce(t *testing.T) {
	for _, n := range []int{12, 100, 101} {
		h := New(synth(n))
		want := make([]string, 0, n)
		for _, c := range synth(n) {
			want = append(want, c.Name)
		}
		for size := 5; size <= 100; size++ {
			pages := walk(t, h, size)
			var flat []string
			for i, p := range pages {
				if len(p) > size || (i < len(pages)-1 && len(p) != size) {
					t.Fatalf("n=%d size=%d page %d has %d", n, size, i, len(p))
				}
				flat = append(flat, p...)
			}
			if strings.Join(flat, ",") != strings.Join(want, ",") {
				t.Fatalf("n=%d size=%d: walk differs from catalog", n, size)
			}
			if wantPages := int(math.Ceil(float64(n) / float64(size))); len(pages) != wantPages {
				t.Fatalf("n=%d size=%d: %d pages, want %d", n, size, len(pages), wantPages)
			}
		}
	}
}

func TestRealCatalogEveryPageSize(t *testing.T) {
	h := New(catalog.Types)
	for size := 5; size <= 100; size++ {
		var flat []string
		for _, p := range walk(t, h, size) {
			flat = append(flat, p...)
		}
		if strings.Join(flat, ",") != strings.Join(allNames(), ",") {
			t.Fatalf("size %d: %v", size, flat)
		}
	}
}

func TestTokenTamperingNeverPanicsAndNeverLeaksOutOfCatalog(t *testing.T) {
	h := New(catalog.Types)
	rec := do(h, "POST", "/", form("MaxResults", "5"), nil)
	good := parse(t, rec.Body.String()).Next
	if good == "" {
		t.Fatal("no first-page token")
	}
	valid := map[string]bool{}
	for _, n := range allNames() {
		valid[n] = true
	}
	tokens := []string{
		good[:len(good)-1], good[:len(good)/2], good + "A", good + "=", good + "==", strings.ToUpper(good), "",
		"!!!", " " + good, good + "\n", base64.StdEncoding.EncodeToString([]byte("ec2sim:v1:m5.large")),
		base64.RawURLEncoding.EncodeToString([]byte("ec2sim:v2:m5.large")),
		base64.RawURLEncoding.EncodeToString([]byte("ec2sim:v1:")),
		base64.RawURLEncoding.EncodeToString([]byte("ec2sim:v1:zzzz")),
		base64.RawURLEncoding.EncodeToString([]byte("ec2sim:v1:\x00\xff<&>")),
		base64.RawURLEncoding.EncodeToString([]byte("eyJOZXh0VG9rZW4iOiJ4In0")),                  // another service's JSON token
		base64.RawURLEncoding.EncodeToString([]byte("ec2sim:v1:" + strings.Repeat("a", 500000))), // big
		strings.Repeat("A", 100000),
	}
	for i := 0; i < len(good); i++ { // flip every position
		for _, c := range []byte{'A', '_', '-'} {
			b := []byte(good)
			b[i] = c
			tokens = append(tokens, string(b))
		}
	}
	for _, tok := range tokens {
		rec := do(h, "POST", "/", form("MaxResults", "5", "NextToken", tok), nil)
		wellFormed(t, rec.Body.Bytes())
		switch rec.Code {
		case 400:
			if c := sdkErr(t, rec).Code; c != "InvalidParameterValue" {
				t.Errorf("token %.20q: code %q", tok, c)
			}
		case 200:
			for _, n := range names(parse(t, rec.Body.String())) {
				if !valid[n] {
					t.Errorf("token %.20q leaked type %q", tok, n)
				}
			}
		default:
			t.Errorf("token %.20q: status %d", tok, rec.Code)
		}
	}
}

func TestTokenFromOneFilterSetCannotBreakAnother(t *testing.T) {
	h := New(catalog.Types)
	tok := parse(t, do(h, "POST", "/", form("MaxResults", "5"), nil).Body.String()).Next
	// resumed with different filters and page size: still well-formed, ordered, after the cursor
	rec := do(h, "POST", "/", form("MaxResults", "7", "NextToken", tok, "Filter.1.Name", "processor-info.supported-architecture", "Filter.1.Value.1", "arm64"), nil)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := strings.Join(names(parse(t, rec.Body.String())), ","); got != "m6g.large,m6g.xlarge" {
		t.Errorf("got %s", got)
	}
}

func TestUnsortedInputIsServedSortedAndPagesCleanly(t *testing.T) { // kills the "no sort" mutant: the catalog is already sorted
	rev := append([]catalog.Type(nil), catalog.Types...)
	sort.Slice(rev, func(i, j int) bool { return rev[i].Name > rev[j].Name })
	h := New(rev)
	if got := strings.Join(names(parse(t, do(h, "POST", "/", form(), nil).Body.String())), ","); got != strings.Join(allNames(), ",") {
		t.Errorf("unsorted input served as %s", got)
	}
	var flat []string
	for _, p := range walk(t, h, 5) {
		flat = append(flat, p...)
	}
	if strings.Join(flat, ",") != strings.Join(allNames(), ",") {
		t.Errorf("paging unsorted input: %v", flat)
	}
	if rec := do(h, "POST", "/", form("InstanceType.1", "t3.medium"), nil); rec.Code != 200 {
		t.Errorf("lookup in unsorted-built handler: %d", rec.Code)
	}
}

func TestHandlerDoesNotAliasCallerSlice(t *testing.T) {
	ts := append([]catalog.Type(nil), catalog.Types...)
	h := New(ts)
	before := do(h, "POST", "/", form(), nil).Body.String()
	for i := range ts {
		ts[i].Name = "mutated"
		ts[i].MaxENIs = 99
	}
	after := do(h, "POST", "/", form(), nil).Body.String()
	if stripReq(before) != stripReq(after) {
		t.Error("handler output changed when the caller mutated its slice")
	}
}

func TestEmptyServedSet(t *testing.T) {
	h := New(nil)
	rec := do(h, "POST", "/", form("MaxResults", "5"), nil)
	if rec.Code != 200 || len(parse(t, rec.Body.String()).Items) != 0 {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	wellFormed(t, rec.Body.Bytes())
}

// ---------------------------------------------------------------- 3. filters

func TestFilterSemanticsAdversarial(t *testing.T) {
	h := New(catalog.Types)
	cases := []struct {
		name string
		kv   []string
		want string // "ERR" means 400 InvalidParameterValue
	}{
		{"star alone", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "*"}, strings.Join(allNames(), ",")},
		{"question mark", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "m5.larg?"}, "m5.large"},
		{"question mark does not match empty", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "m5.large?"}, ""},
		{"inner star", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "m*.xlarge"}, "m5.xlarge,m6g.xlarge"},
		{"empty value matches nothing", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", ""}, ""},
		{"value is case sensitive", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "M5.LARGE"}, ""},
		{"filter name is case sensitive", []string{"Filter.1.Name", "Instance-Type", "Filter.1.Value.1", "*"}, "ERR"},
		{"filter name with trailing space", []string{"Filter.1.Name", "instance-type ", "Filter.1.Value.1", "*"}, "ERR"},
		{"tag filter unsupported", []string{"Filter.1.Name", "tag:Name", "Filter.1.Value.1", "x"}, "ERR"},
		{"same filter twice is AND", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "m*", "Filter.2.Name", "instance-type", "Filter.2.Value.1", "*.large"}, "m5.large,m6g.large"},
		{"disjoint AND is empty", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "m*", "Filter.2.Name", "instance-type", "Filter.2.Value.1", "t*"}, ""},
		{"OR within one filter spans arch", []string{"Filter.1.Name", "processor-info.supported-architecture", "Filter.1.Value.1", "arm64", "Filter.1.Value.2", "x86_64"}, strings.Join(allNames(), ",")},
		{"gap in value indexes", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "t3.medium", "Filter.1.Value.7", "r5.large"}, "r5.large,t3.medium"},
		{"gap in filter indexes", []string{"Filter.3.Name", "instance-type", "Filter.3.Value.1", "t3.medium"}, "t3.medium"},
		{"vcpu one-digit wildcard", []string{"Filter.1.Name", "vcpu-info.default-vcpus", "Filter.1.Value.1", "?"}, strings.Join(allNames(), ",")},
		{"vcpu two-digit wildcard", []string{"Filter.1.Name", "vcpu-info.default-vcpus", "Filter.1.Value.1", "??"}, ""},
		{"numeric filter mismatch", []string{"Filter.1.Name", "vcpu-info.default-vcpus", "Filter.1.Value.1", "08"}, ""},
		{"filter plus unknown InstanceType errors first", []string{"InstanceType.1", "nope.large", "Filter.1.Name", "instance-type", "Filter.1.Value.1", "*"}, "ERR"},
		{"InstanceType excluded by filter", []string{"InstanceType.1", "m5.large", "Filter.1.Name", "instance-type", "Filter.1.Value.1", "t*"}, ""},
		{"value with regex metacharacters", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "m5.large|c5.large"}, ""},
		{"dot is literal", []string{"Filter.1.Name", "instance-type", "Filter.1.Value.1", "m5xlarge"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(h, "POST", "/", form(c.kv...), nil)
			if c.want == "ERR" {
				if rec.Code != 400 || sdkErr(t, rec).Code != "InvalidParameterValue" && sdkErr(t, rec).Code != "InvalidInstanceType" {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				return
			}
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if got := strings.Join(names(parse(t, rec.Body.String())), ","); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestPagingOverExactMultipleOfPageSizeHasNoEmptyTrailingPage(t *testing.T) {
	h := New(synth(10))
	if p := walk(t, h, 5); len(p) != 2 {
		t.Errorf("%d pages", len(p))
	}
	h = New(synth(100))
	if p := walk(t, h, 100); len(p) != 1 {
		t.Errorf("%d pages", len(p))
	}
	// filtered to an exact multiple
	if p := walk(t, New(catalog.Types), 5, "Filter.1.Name", "processor-info.supported-architecture", "Filter.1.Value.1", "x86_64"); len(p) != 2 {
		t.Errorf("%d pages", len(p))
	}
}

func TestInstanceTypeListEdgeCases(t *testing.T) {
	h := New(catalog.Types)
	// duplicates collapse
	rec := do(h, "POST", "/", form("InstanceType.1", "m5.large", "InstanceType.2", "m5.large"), nil)
	if got := strings.Join(names(parse(t, rec.Body.String())), ","); rec.Code != 200 || got != "m5.large" {
		t.Errorf("dups: %d %s", rec.Code, got)
	}
	// index gaps, zero and negative indexes
	for _, k := range []string{"InstanceType.5", "InstanceType.0", "InstanceType.-3", "InstanceType.99999999999"} {
		rec = do(h, "POST", "/", form(k, "m5.large"), nil)
		if got := strings.Join(names(parse(t, rec.Body.String())), ","); rec.Code != 200 || got != "m5.large" {
			t.Errorf("%s: %d %s", k, rec.Code, got)
		}
	}
	// more than 100 entries: real EC2 caps the list; either refuse cleanly or serve, but stay bounded and well-formed
	var kv []string
	for i := 1; i <= 150; i++ {
		kv = append(kv, "InstanceType."+strconv.Itoa(i), "m5.large")
	}
	start := time.Now()
	rec = do(h, "POST", "/", form(kv...), nil)
	wellFormed(t, rec.Body.Bytes())
	if rec.Code != 200 && rec.Code != 400 || time.Since(start) > time.Second {
		t.Errorf("150 entries: %d in %v", rec.Code, time.Since(start))
	}
	t.Logf("InstanceType.N beyond 100 -> %d", rec.Code)
	// empty value
	rec = do(h, "POST", "/", form("InstanceType.1", ""), nil)
	wantError(t, rec.Code, rec.Body.String(), 400, "InvalidInstanceType")
	// a wrong case
	rec = do(h, "POST", "/", form("InstanceType.1", "M5.Large"), nil)
	wantError(t, rec.Code, rec.Body.String(), 400, "InvalidInstanceType")
	// unindexed InstanceType is not a recognised parameter; it must not be treated as "select nothing and succeed with garbage"
	rec = do(h, "POST", "/", "Action=DescribeInstanceTypes&InstanceType=m5.large", nil)
	wellFormed(t, rec.Body.Bytes())
	t.Logf("unindexed InstanceType=m5.large -> %d, %d items", rec.Code, len(parse(t, rec.Body.String()).Items))
}

func TestHugeFilterSetsAreBounded(t *testing.T) {
	h := New(catalog.Types)
	var b strings.Builder
	b.WriteString("Action=DescribeInstanceTypes")
	for i := 1; i <= 4900; i++ { // stays under the stdlib's 10000-parameter ParseQuery cap
		fmt.Fprintf(&b, "&Filter.%d.Name=instance-type&Filter.%d.Value.1=*", i, i)
	}
	start := time.Now()
	rec := do(h, "POST", "/", b.String(), nil)
	wellFormed(t, rec.Body.Bytes())
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("many filters took %v", d)
	}
	t.Logf("%d bytes of filters -> %d in %v", b.Len(), rec.Code, time.Since(start))

	// one filter with a huge number of values
	b.Reset()
	b.WriteString("Action=DescribeInstanceTypes&Filter.1.Name=instance-type")
	for i := 1; i <= 9900; i++ {
		fmt.Fprintf(&b, "&Filter.1.Value.%d=zz%d", i, i)
	}
	start = time.Now()
	rec = do(h, "POST", "/", b.String(), nil)
	wellFormed(t, rec.Body.Bytes())
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("many values took %v", d)
	}
	t.Logf("%d bytes of values -> %d in %v", b.Len(), rec.Code, time.Since(start))

	// hostile glob: no catastrophic backtracking
	pat := strings.Repeat("*a", 20000) + "b"
	start = time.Now()
	rec = do(h, "POST", "/", form("Filter.1.Name", "instance-type", "Filter.1.Value.1", pat), nil)
	if rec.Code != 200 || time.Since(start) > time.Second {
		t.Errorf("hostile glob: %d in %v", rec.Code, time.Since(start))
	}
}

// reference glob via regexp
func refGlob(pat, s string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pat); i++ {
		switch pat[i] {
		case '*':
			b.WriteString("(?s:.*)")
		case '?':
			b.WriteString("(?s:.)")
		default:
			b.WriteString(regexp.QuoteMeta(string(pat[i])))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String()).MatchString(s)
}

// BUG: glob tests the literal-equality case before the '*' case, so a '*' in the pattern that meets a
// literal '*' in the subject is consumed as a literal and never recorded as a wildcard; glob("*", "*0")
// is false. Low severity: no attribute value the service filters on contains '*', so it is unreachable
// with the real catalog, but the matcher is wrong.
func TestBugGlobStarWildcardConsumedByLiteralStar(t *testing.T) {
	for _, c := range [][2]string{{"*", "*0"}, {"*", "**"}, {"a*", "a*b"}, {"*b", "*ab"}} {
		if !glob(c[0], c[1]) {
			t.Errorf("glob(%q,%q) = false, want true", c[0], c[1])
		}
	}
}

func TestGlobAgainstRegexpExhaustive(t *testing.T) {
	alpha := []string{"a", "b", "*", "?"}
	var pats []string
	var gen func(prefix string, n int)
	gen = func(prefix string, n int) {
		pats = append(pats, prefix)
		if n == 0 {
			return
		}
		for _, a := range alpha {
			gen(prefix+a, n-1)
		}
	}
	gen("", 5)
	var strs []string
	var gs func(prefix string, n int)
	gs = func(prefix string, n int) {
		strs = append(strs, prefix)
		if n == 0 {
			return
		}
		for _, a := range []string{"a", "b"} {
			gs(prefix+a, n-1)
		}
	}
	gs("", 6)
	for _, p := range pats {
		for _, s := range strs {
			if glob(p, s) != refGlob(p, s) {
				t.Fatalf("glob(%q,%q) = %v, regexp says %v", p, s, glob(p, s), refGlob(p, s))
			}
		}
	}
}

// ---------------------------------------------------------------- 4. protocol robustness

func TestGetAndPostAndQueryPlacement(t *testing.T) {
	h := New(catalog.Types)
	q := "Action=DescribeInstanceTypes&Version=2016-11-15&InstanceType.1=t3.medium"
	type tc struct {
		name, method, target, body string
		hdr                        map[string]string
	}
	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for _, c := range []tc{
		{"GET query", "GET", "/?" + q, "", nil},
		{"POST body", "POST", "/", q, form},
		{"POST query only", "POST", "/?" + q, "", nil},
		{"POST split query and body", "POST", "/?Action=DescribeInstanceTypes", "InstanceType.1=t3.medium", form},
		{"POST body without content type", "POST", "/", q, nil},
		{"POST json content type with form body", "POST", "/", q, map[string]string{"Content-Type": "application/json"}},
		{"POST charset", "POST", "/", q, map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=utf-8"}},
		{"POST multipart ct", "POST", "/", q, map[string]string{"Content-Type": "multipart/form-data; boundary=x"}},
		{"any path", "POST", "/some/other/path", q, form},
		{"GET with body", "GET", "/?" + q, "ignored=1", nil},
	} {
		rec := do(h, c.method, c.target, c.body, c.hdr)
		if rec.Code != 200 || strings.Join(names(parse(t, rec.Body.String())), ",") != "t3.medium" {
			t.Errorf("%s: %d %.200s", c.name, rec.Code, rec.Body)
		}
	}
}

func TestOtherMethodsAre405WithEC2Error(t *testing.T) {
	h := New(catalog.Types)
	for _, m := range []string{"PUT", "DELETE", "PATCH", "OPTIONS", "HEAD", "TRACE", "CONNECT", "PROPFIND"} {
		rec := do(h, m, "/", "", nil)
		if rec.Code != 405 {
			t.Errorf("%s: %d", m, rec.Code)
		}
		if m != "HEAD" && sdkErr(t, rec).Code != "InvalidAction" {
			t.Errorf("%s: %s", m, rec.Body)
		}
	}
}

func TestDuplicateAndConflictingActions(t *testing.T) {
	h := New(catalog.Types)
	// same answer every time, never a 5xx, and a non-Describe action must never produce a type listing
	for _, body := range []string{
		"Action=DescribeInstanceTypes&Action=RunInstances",
		"Action=RunInstances&Action=DescribeInstanceTypes",
	} {
		first := do(h, "POST", "/", body, nil)
		for i := 0; i < 20; i++ {
			if again := do(h, "POST", "/", body, nil); again.Code != first.Code {
				t.Fatalf("%s: unstable %d vs %d", body, again.Code, first.Code)
			}
		}
		t.Logf("%s -> %d", body, first.Code)
	}
	// body wins over URL, consistently with awsapi.Form which the gateway also uses for routing
	rec := do(h, "POST", "/?Action=RunInstances", "Action=DescribeInstanceTypes", nil)
	if rec.Code != 200 {
		t.Errorf("body Action should win: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "POST", "/?Action=DescribeInstanceTypes", "Action=RunInstances", nil)
	wantError(t, rec.Code, rec.Body.String(), 400, "InvalidAction")
}

func TestVersionIsNotRequiredButGarbageIsHarmless(t *testing.T) {
	h := New(catalog.Types)
	for _, v := range []string{"", "1999-01-01", "../../etc/passwd", "2016-11-15"} {
		body := "Action=DescribeInstanceTypes"
		if v != "" {
			body += "&Version=" + url.QueryEscape(v)
		}
		rec := do(h, "POST", "/", body, nil)
		wellFormed(t, rec.Body.Bytes())
		if rec.Code != 200 && rec.Code != 400 {
			t.Errorf("version %q: %d", v, rec.Code)
		}
		t.Logf("Version=%q -> %d", v, rec.Code)
	}
}

func TestEmptyAndOddBodies(t *testing.T) {
	h := New(catalog.Types)
	for name, body := range map[string]string{
		"empty": "", "whitespace": "   ", "just ampersands": "&&&&", "just equals": "=", "action only key": "Action",
		"json": `{"Action":"DescribeInstanceTypes"}`, "xml": "<x/>", "binary": "\x00\x01\x02\xff\xfe",
		"semicolon sep": "Action=DescribeInstanceTypes;InstanceType.1=m5.large", "percent only": "%", "plus": "Action=Describe+InstanceTypes",
	} {
		rec := do(h, "POST", "/", body, nil)
		wellFormed(t, rec.Body.Bytes())
		if rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if sdkErr(t, rec).Code == "" {
			t.Errorf("%s: SDK decoder found no code in %s", name, rec.Body)
		}
	}
}

func TestBodyLimitBoundary(t *testing.T) {
	h := New(catalog.Types)
	base := "Action=DescribeInstanceTypes&pad="
	for _, tc := range []struct {
		size int
		want int
	}{{1<<20 - 1, 200}, {1 << 20, 200}, {1<<20 + 1, 413}, {4 << 20, 413}} {
		rec := do(h, "POST", "/", base+strings.Repeat("a", tc.size-len(base)), nil)
		if rec.Code != tc.want {
			t.Errorf("body of %d bytes: %d, want %d", tc.size, rec.Code, tc.want)
		}
		if tc.want == 413 {
			if c := sdkErr(t, rec).Code; c == "" {
				t.Errorf("413 has no EC2 error code: %.200s", rec.Body)
			}
		}
	}
}

func TestOversizedBodyOverRealConnectionIsAnswered(t *testing.T) {
	srv := httptest.NewServer(New(catalog.Types))
	defer srv.Close()
	resp, err := http.Post(srv.URL, "application/x-www-form-urlencoded", strings.NewReader("Action=DescribeInstanceTypes&p="+strings.Repeat("a", 3<<20)))
	if err != nil {
		t.Fatalf("client could not read the 413 (server closed early?): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestManyParametersLimit(t *testing.T) {
	h := New(catalog.Types)
	for _, n := range []int{9000, 10001, 20000, 50000} {
		var b strings.Builder
		b.WriteString("Action=DescribeInstanceTypes")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "&p%d=1", i)
		}
		if b.Len() > 1<<20 {
			continue
		}
		rec := do(h, "POST", "/", b.String(), nil)
		wellFormed(t, rec.Body.Bytes())
		if rec.Code != 200 && rec.Code != 400 {
			t.Errorf("%d params: status %d", n, rec.Code)
		}
		if rec.Code == 400 && sdkErr(t, rec).Code != "MalformedQueryString" {
			t.Errorf("%d params: %s", n, rec.Body)
		}
		t.Logf("%d params -> %d", n, rec.Code)
	}
}

func TestInvalidUTF8AndNULNeverBreakTheErrorXML(t *testing.T) {
	h := New(catalog.Types)
	nasty := []string{"%ff%fe", "%00", "a%00b", "%c0%af", "%ed%a0%80", "%e2%80%a8", "<&>\"'", "]]>", "%0b%0c%1f", "☃", "%f0%9f%92%a9"}
	for _, n := range nasty {
		for _, body := range []string{
			"Action=" + n, "Action=DescribeInstanceTypes&InstanceType.1=" + n,
			"Action=DescribeInstanceTypes&Filter.1.Name=" + n + "&Filter.1.Value.1=x",
			"Action=DescribeInstanceTypes&Filter.1.Name=instance-type&Filter.1.Value.1=" + n,
			"Action=DescribeInstanceTypes&MaxResults=5&NextToken=" + n,
			"Action=DescribeInstanceTypes&MaxResults=" + n,
		} {
			rec := do(h, "POST", "/", body, nil)
			wellFormed(t, rec.Body.Bytes())
			if rec.Code == 400 {
				if c := sdkErr(t, rec); c.Code == "" || c.RequestID == "" {
					t.Errorf("%s: %+v", body, c)
				}
			}
			if bytes.IndexByte(rec.Body.Bytes(), 0) >= 0 {
				t.Errorf("%s: NUL byte in response", body)
			}
		}
	}
}

func TestGzipRequestIsRejectedCleanlyNotDecompressed(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(form()))
	_ = zw.Close()
	rec := do(New(catalog.Types), "POST", "/", buf.String(), map[string]string{"Content-Encoding": "gzip", "Content-Type": "application/x-www-form-urlencoded"})
	wellFormed(t, rec.Body.Bytes())
	if rec.Code != 400 {
		t.Errorf("gzip body: %d", rec.Code)
	}
}

func TestHTTP10AndRawRequests(t *testing.T) {
	srv := httptest.NewServer(New(catalog.Types))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	raw := func(req string) string {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.WriteString(c, req)
		b, _ := io.ReadAll(c)
		return string(b)
	}
	body := form("InstanceType.1", "m5.large")
	got := raw(fmt.Sprintf("POST / HTTP/1.0\r\nContent-Length: %d\r\n\r\n%s", len(body), body))
	if !strings.HasPrefix(got, "HTTP/1.0 200") && !strings.HasPrefix(got, "HTTP/1.1 200") || !strings.Contains(got, "<instanceType>m5.large</instanceType>") {
		t.Errorf("HTTP/1.0: %.300s", got)
	}
	got = raw(fmt.Sprintf("POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n%x\r\n%s\r\n0\r\n\r\n", len(body), body))
	if !strings.Contains(got, "200 OK") {
		t.Errorf("chunked: %.300s", got)
	}
	got = raw("GARBAGE\r\n\r\n")
	if !strings.Contains(got, "400") {
		t.Errorf("garbage request line: %.200s", got)
	}
	// 100-continue as sent by some SDKs
	got = raw(fmt.Sprintf("POST / HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\nConnection: close\r\nContent-Length: %d\r\n\r\n%s", len(body), body))
	if !strings.Contains(got, "200 OK") {
		t.Errorf("expect-continue: %.300s", got)
	}
}

func TestErrorShapeAndHeaders(t *testing.T) {
	h := New(catalog.Types)
	for _, body := range []string{"Action=Nope", "", "Action=DescribeInstanceTypes&MaxResults=1", "Action=DescribeInstanceTypes&InstanceType.1=x", "Action=DescribeInstanceTypes&Filter.1.Name=x&Filter.1.Value.1=y"} {
		rec := do(h, "POST", "/", body, nil)
		if rec.Code != 400 {
			t.Fatalf("%q: %d", body, rec.Code)
		}
		if !strings.HasPrefix(rec.Body.String(), "<Response><Errors><Error><Code>") {
			t.Errorf("%q: not the EC2 error envelope: %s", body, rec.Body)
		}
		er := sdkErr(t, rec)
		if er.Code == "" || er.Message == "" || er.RequestID == "" {
			t.Errorf("%q: %+v", body, er)
		}
		if got := rec.Header().Get("X-Amzn-Requestid"); got != er.RequestID {
			t.Errorf("header request id %q != body %q", got, er.RequestID)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/xml") {
			t.Errorf("content type %q", ct)
		}
	}
}

// BUG: the success response's X-Amzn-Requestid header and its <requestId> body element are two different
// random IDs (awsapi.WriteXML mints one for the header, the handler another for the body). Error responses
// agree. A client correlating AWS support/log lines by header vs body would see two IDs.
func TestBugSuccessRequestIDHeaderMatchesBody(t *testing.T) {
	rec := do(New(catalog.Types), "POST", "/", form(), nil)
	if h, b := rec.Header().Get("X-Amzn-Requestid"), parse(t, rec.Body.String()).ReqID; h != b {
		t.Errorf("header request id %q != body requestId %q", h, b)
	}
}

// ---------------------------------------------------------------- 5. XML correctness

func TestXMLNamespaceWellFormedAndDeterministic(t *testing.T) {
	h1, h2 := New(catalog.Types), New(catalog.Types)
	rec := do(h1, "POST", "/", form(), nil)
	wellFormed(t, rec.Body.Bytes())
	var root struct {
		XMLName xml.Name
	}
	_ = xml.Unmarshal(rec.Body.Bytes(), &root)
	if root.XMLName.Space != Namespace || root.XMLName.Local != "DescribeInstanceTypesResponse" {
		t.Errorf("root = %+v", root.XMLName)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/xml;charset=UTF-8" {
		t.Errorf("content-type %q", ct)
	}
	want := stripReq(rec.Body.String())
	for i := 0; i < 50; i++ {
		h := h1
		if i%2 == 1 {
			h = h2
		}
		if got := stripReq(do(h, "POST", "/", form(), nil).Body.String()); got != want {
			t.Fatalf("response bytes differ on call %d", i)
		}
	}
	// request IDs differ between calls
	if parse(t, do(h1, "POST", "/", form(), nil).Body.String()).ReqID == parse(t, do(h1, "POST", "/", form(), nil).Body.String()).ReqID {
		t.Error("request ids repeat")
	}
	// reordering parameters does not change the answer
	a := stripReq(do(h1, "POST", "/", "Action=DescribeInstanceTypes&InstanceType.1=t3.medium&InstanceType.2=c5.large", nil).Body.String())
	b := stripReq(do(h1, "POST", "/", "InstanceType.2=c5.large&InstanceType.1=t3.medium&Action=DescribeInstanceTypes", nil).Body.String())
	c := stripReq(do(h1, "POST", "/", "Action=DescribeInstanceTypes&InstanceType.1=c5.large&InstanceType.2=t3.medium", nil).Body.String())
	if a != b || a != c {
		t.Error("parameter order changed the response")
	}
}

func TestNoXMLDeclarationAndNoNextTokenElementOnLastPage(t *testing.T) {
	rec := do(New(catalog.Types), "POST", "/", form("MaxResults", "100"), nil)
	if strings.Contains(rec.Body.String(), "nextToken") {
		t.Error("nextToken present on the last page")
	}
	if strings.HasPrefix(rec.Body.String(), "<?xml") {
		t.Log("xml declaration present")
	}
}

func TestSpecialCharactersInTypeNamesAreEscaped(t *testing.T) {
	ts := []catalog.Type{{Name: `a<b>&"c'.large`, VCPU: 1, MemoryMiB: 1, Arch: "x86_64", MaxENIs: 1, IPv4PerENI: 2}}
	h := New(ts)
	rec := do(h, "POST", "/", form(), nil)
	wellFormed(t, rec.Body.Bytes())
	if got := names(parse(t, rec.Body.String())); len(got) != 1 || got[0] != ts[0].Name {
		t.Errorf("round trip: %v", got)
	}
	rec = do(h, "POST", "/", form("InstanceType.1", `x<&>"`), nil)
	wellFormed(t, rec.Body.Bytes())
	if e := sdkErr(t, rec); e.Code != "InvalidInstanceType" || !strings.Contains(e.Message, `x<&>"`) {
		t.Errorf("%+v", e)
	}
}

func TestResponseSizeFor100Items(t *testing.T) {
	h := New(synth(100))
	rec := do(h, "POST", "/", form("MaxResults", "100"), nil)
	if got := len(parse(t, rec.Body.String()).Items); got != 100 {
		t.Fatalf("%d items", got)
	}
	if n := rec.Body.Len(); n > 256<<10 {
		t.Errorf("100 items = %d bytes", n)
	} else {
		t.Logf("100 items = %d bytes (%d per item)", n, n/100)
	}
}

// ---------------------------------------------------------------- 6. concurrency and logging

func TestConcurrentMixedRequests(t *testing.T) {
	h := New(synth(60))
	var wg sync.WaitGroup
	errs := make(chan string, 400)
	for g := 0; g < 200; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			size := 5 + g%40
			var got []string
			token := ""
			for {
				kv := []string{"MaxResults", strconv.Itoa(size)}
				if g%3 == 0 {
					kv = append(kv, "Filter.1.Name", "instance-type", "Filter.1.Value.1", "x0[0-4]*")
				}
				if token != "" {
					kv = append(kv, "NextToken", token)
				}
				rec := do(h, "POST", "/", form(kv...), nil)
				if rec.Code != 200 {
					errs <- fmt.Sprintf("g%d: %d", g, rec.Code)
					return
				}
				var r respXML
				if err := xml.Unmarshal(rec.Body.Bytes(), &r); err != nil {
					errs <- err.Error()
					return
				}
				got = append(got, names(r)...)
				if r.Next == "" {
					break
				}
				token = r.Next
			}
			want := 60
			if g%3 == 0 {
				want = 0 // "x0[0-4]*" has no bracket support: literal match, so nothing matches
			}
			if len(got) != want {
				errs <- fmt.Sprintf("g%d: %d items, want %d", g, len(got), want)
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestNoRequestContentIsLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	h := New(catalog.Types)
	do(h, "POST", "/", form("InstanceType.1", "SECRET-MARKER-123"), map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKIASECRETMARKER/x"})
	do(h, "POST", "/", "Action=SECRET-MARKER-456", nil)
	if buf.Len() != 0 {
		t.Errorf("handler wrote to the standard logger: %q", buf.String())
	}
}
