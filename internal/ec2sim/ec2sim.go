// Package ec2sim answers only ec2:DescribeInstanceTypes, from internal/catalog.
//
// It exists because Floci reports maximumNetworkInterfaces only inside
// networkInfo.networkCards, while Karpenter reads it at networkInfo level to
// derive pod density. Everything else EC2 stays on Floci. It is reference data,
// not a simulator: no state, and any other action is an EC2 InvalidAction.
package ec2sim

import (
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/tazzledazzle/meridian/internal/awsapi"
	"github.com/tazzledazzle/meridian/internal/catalog"
)

// Namespace is the EC2 API namespace SDKs expect on the response root.
const Namespace = "http://ec2.amazonaws.com/doc/2016-11-15/"

// item is one InstanceTypeInfo; field names are the EC2 wire names.
type item struct {
	InstanceType      string    `xml:"instanceType"`
	CurrentGeneration bool      `xml:"currentGeneration"`
	UsageClasses      []string  `xml:"supportedUsageClasses>item"`
	Virtualization    []string  `xml:"supportedVirtualizationTypes>item"`
	Hypervisor        string    `xml:"hypervisor"`
	BareMetal         bool      `xml:"bareMetal"`
	Burstable         bool      `xml:"burstablePerformanceSupported"`
	Processor         processor `xml:"processorInfo"`
	VCPU              vcpu      `xml:"vCpuInfo"`
	Memory            memory    `xml:"memoryInfo"`
	Network           network   `xml:"networkInfo"`
	EBS               ebs       `xml:"ebsInfo"`
	InstanceStorage   bool      `xml:"instanceStorageSupported"`
}

type processor struct {
	Architectures []string `xml:"supportedArchitectures>item"`
}

type vcpu struct {
	DefaultVCPUs  int `xml:"defaultVCpus"`
	DefaultCores  int `xml:"defaultCores"`
	DefaultThread int `xml:"defaultThreadsPerCore"`
}

type memory struct {
	SizeInMiB int `xml:"sizeInMiB"`
}

type card struct {
	Index int `xml:"networkCardIndex"`
	Max   int `xml:"maximumNetworkInterfaces"`
}

type network struct {
	MaxENIs     int    `xml:"maximumNetworkInterfaces"` // the field Karpenter needs at this level
	IPv4PerENI  int    `xml:"ipv4AddressesPerInterface"`
	IPv6PerENI  int    `xml:"ipv6AddressesPerInterface"`
	IPv6        bool   `xml:"ipv6Supported"`
	EFA         bool   `xml:"efaSupported"`
	Cards       []card `xml:"networkCards>item"`
	DefaultCard int    `xml:"defaultNetworkCardIndex"`
}

type ebs struct {
	Optimized string `xml:"ebsOptimizedSupport"`
}

type response struct {
	XMLName xml.Name `xml:"DescribeInstanceTypesResponse"`
	XMLNS   string   `xml:"xmlns,attr"`
	ReqID   string   `xml:"requestId"`
	Items   []item   `xml:"instanceTypeSet>item"`
	// NextToken is omitted on the last page.
	NextToken string `xml:"nextToken,omitempty"`
}

// toItem renders a catalog type the way EC2 would describe a plain nitro type.
func toItem(t catalog.Type) item {
	threads := 2 // x86 types are hyperthreaded; Graviton cores are not
	if t.Arch == "arm64" {
		threads = 1
	}
	return item{
		InstanceType:      t.Name,
		CurrentGeneration: true,
		UsageClasses:      []string{"on-demand", "spot"},
		Virtualization:    []string{"hvm"},
		Hypervisor:        "nitro",
		Burstable:         burstable(t),
		Processor:         processor{Architectures: []string{t.Arch}},
		VCPU:              vcpu{DefaultVCPUs: t.VCPU, DefaultCores: t.VCPU / threads, DefaultThread: threads},
		Memory:            memory{SizeInMiB: t.MemoryMiB},
		Network: network{
			MaxENIs: t.MaxENIs, IPv4PerENI: t.IPv4PerENI, IPv6PerENI: t.IPv4PerENI, IPv6: true,
			Cards: []card{{Index: 0, Max: t.MaxENIs}},
		},
		EBS: ebs{Optimized: "default"},
	}
}

// maxBody bounds the request; DescribeInstanceTypes requests are tiny.
const maxBody = 1 << 20

// handler holds the types served, sorted by name; nothing else is ever offered.
type handler struct{ types []catalog.Type }

// New returns the handler serving exactly the given types (sorted by name, as the catalog is).
func New(types []catalog.Type) http.Handler {
	cp := append([]catalog.Type(nil), types...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Name < cp[j].Name })
	return &handler{types: cp}
}

func (h *handler) has(name string) bool {
	i := sort.Search(len(h.types), func(i int) bool { return h.types[i].Name >= name })
	return i < len(h.types) && h.types[i].Name == name
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code, msg string) { awsapi.WriteError(w, awsapi.StyleEC2, status, code, msg) }
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		fail(http.StatusMethodNotAllowed, "InvalidAction", "only POST and GET are supported")
		return
	}
	body, err := awsapi.ReadBody(r, maxBody)
	if err != nil {
		fail(http.StatusRequestEntityTooLarge, "InvalidParameterValue", "request body too large")
		return
	}
	form, err := awsapi.Form(r, body)
	if err != nil {
		fail(http.StatusBadRequest, "MalformedQueryString", "the request is not a valid query string")
		return
	}
	switch action := form.Get("Action"); action {
	case "":
		fail(http.StatusBadRequest, "MissingAction", "no action was supplied")
	case "DescribeInstanceTypes":
		h.describe(w, awsapi.Nest(form), fail)
	default: // ec2-sim is reference data only; a misrouted call must be obvious
		fail(http.StatusBadRequest, "InvalidAction", "The action "+action+" is not valid for this web service (ec2-sim serves only DescribeInstanceTypes).")
	}
}

func (h *handler) describe(w http.ResponseWriter, q *awsapi.Node, fail func(int, string, string)) {
	// InstanceType.N narrows the set; every name must exist in the catalog.
	var wanted map[string]bool
	if q.Has("InstanceType") {
		wanted = map[string]bool{}
		var unknown []string
		for _, n := range q.Get("InstanceType").Strings() {
			if !h.has(n) { // the served set, not the global catalog: never offer more than we were given
				unknown = append(unknown, n)
			}
			wanted[n] = true
		}
		if len(unknown) > 0 {
			fail(http.StatusBadRequest, "InvalidInstanceType", "The following supplied instance types do not exist: ["+strings.Join(unknown, ",")+"]")
			return
		}
	}
	filters, err := parseFilters(q)
	if err != nil {
		fail(http.StatusBadRequest, "InvalidParameterValue", err.Error())
		return
	}
	limit, after, err := parsePaging(q, wanted != nil)
	if err != nil {
		fail(http.StatusBadRequest, "InvalidParameterValue", err.Error())
		return
	}
	resp := response{XMLNS: Namespace, ReqID: awsapi.NewRequestID()}
	for _, t := range h.types {
		if t.Name <= after || (wanted != nil && !wanted[t.Name]) || !matches(t, filters) {
			continue
		}
		if limit > 0 && len(resp.Items) == limit { // one more match exists: hand out a token
			resp.NextToken = encodeToken(resp.Items[limit-1].InstanceType)
			break
		}
		resp.Items = append(resp.Items, toItem(t))
	}
	w.Header().Set("X-Amzn-Requestid", resp.ReqID) // header and <requestId> carry the same id, as in real EC2
	awsapi.WriteXML(w, http.StatusOK, resp)
}

// tokenPrefix marks tokens this service issued; the rest is the last type name returned.
const tokenPrefix = "ec2sim:v1:"

func encodeToken(last string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(tokenPrefix + last))
}

// parsePaging reads MaxResults (5..100, as EC2 enforces) and NextToken. limit 0 means unpaged.
func parsePaging(q *awsapi.Node, haveTypes bool) (limit int, after string, err error) {
	if q.Has("MaxResults") {
		limit, err = strconv.Atoi(q.Str("MaxResults"))
		if err != nil || limit < 5 || limit > 100 {
			return 0, "", errors.New("Value for parameter MaxResults must be an integer between 5 and 100")
		}
		if haveTypes { // EC2 refuses to page an explicit list
			return 0, "", errors.New("MaxResults cannot be combined with InstanceType")
		}
	}
	if q.Has("NextToken") {
		raw, derr := base64.RawURLEncoding.DecodeString(q.Str("NextToken"))
		if limit == 0 || derr != nil || !strings.HasPrefix(string(raw), tokenPrefix) {
			return 0, "", errors.New("The specified NextToken is not valid")
		}
		after = strings.TrimPrefix(string(raw), tokenPrefix)
	}
	return limit, after, nil
}

// filter is one Filter.N: a name and its OR-ed value patterns.
type filter struct {
	name   string
	values []string
}

func parseFilters(q *awsapi.Node) ([]filter, error) {
	var out []filter
	for _, f := range q.Get("Filter").List() {
		name := f.Str("Name")
		if name == "" {
			return nil, errors.New("a filter has no Name")
		}
		if _, ok := attrs[name]; !ok {
			return nil, fmt.Errorf("The filter '%s' is invalid", name)
		}
		vals := f.Get("Value").Strings()
		if len(vals) == 0 {
			return nil, fmt.Errorf("The filter '%s' has no values", name)
		}
		out = append(out, filter{name, vals})
	}
	return out, nil
}

// attrs maps each supported filter name to the values a type has for it.
var attrs = map[string]func(catalog.Type) []string{
	"instance-type":                         func(t catalog.Type) []string { return []string{t.Name} },
	"current-generation":                    func(catalog.Type) []string { return []string{"true"} },
	"supported-usage-class":                 func(catalog.Type) []string { return []string{"on-demand", "spot"} },
	"supported-virtualization-type":         func(catalog.Type) []string { return []string{"hvm"} },
	"hypervisor":                            func(catalog.Type) []string { return []string{"nitro"} },
	"bare-metal":                            func(catalog.Type) []string { return []string{"false"} },
	"processor-info.supported-architecture": func(t catalog.Type) []string { return []string{t.Arch} },
	"burstable-performance-supported":       func(t catalog.Type) []string { return []string{strconv.FormatBool(burstable(t))} },
	"vcpu-info.default-vcpus":               func(t catalog.Type) []string { return []string{strconv.Itoa(t.VCPU)} },
	"memory-info.size-in-mib":               func(t catalog.Type) []string { return []string{strconv.Itoa(t.MemoryMiB)} },
}

// matches applies EC2 semantics: filters AND together, a filter's values OR, and * and ? are wildcards.
func matches(t catalog.Type, fs []filter) bool {
	for _, f := range fs {
		ok := false
		for _, have := range attrs[f.name](t) {
			for _, pat := range f.values {
				if glob(pat, have) {
					ok = true
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// glob matches s against a pattern with * (any run) and ? (any one byte); iterative, so no blow-up on hostile patterns.
func glob(pat, s string) bool {
	p, i, star, mark := 0, 0, -1, 0
	for i < len(s) {
		switch {
		case p < len(pat) && pat[p] == '*': // test the wildcard first: a literal '*' in s must not consume it
			star, mark = p, i
			p++
		case p < len(pat) && (pat[p] == '?' || pat[p] == s[i]):
			p++
			i++
		case star >= 0:
			p, mark = star+1, mark+1
			i = mark
		default:
			return false
		}
	}
	for p < len(pat) && pat[p] == '*' {
		p++
	}
	return p == len(pat)
}

func burstable(t catalog.Type) bool { return strings.HasPrefix(t.Name, "t") }
