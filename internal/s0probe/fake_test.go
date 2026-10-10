package s0probe

import (
	"encoding/json"
	"fmt"
	"io"
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
)

// fakeAWS is a small, stateful stand-in for Floci behind the gateway. By default it answers the way Floci was
// observed to (realistic fields, the public AL2023 SSM parameter, a seeded network). Tests make it deficient
// on purpose: drop a field from every answer, rewrite one operation's body, or force an HTTP status.
type fakeAWS struct {
	t *testing.T

	mu    sync.Mutex
	calls []string // "service:Op" in arrival order; eks calls also appear as "eks-route:METHOD /path"

	// knobs
	drop        []string                       // element/key names removed from every response body
	rewrite     map[string]func(string) string // "service:Op" -> body rewrite
	fail        map[string]int                 // "service:Op" -> forced HTTP status
	cold        int                            // the first N ec2 requests answer 503 (cold start)
	pendingFor  int                            // DescribeInstances reports "pending" this many times before "running"
	noFleetTags bool                           // CreateFleet ignores TagSpecification
	noSubnets   bool
	noTagFilter bool // DescribeLaunchTemplates ignores nothing: a tag filter matches no template
	noAMIImage  bool // SSM serves the parameter but EC2 has no such image
	noAMIParam  bool
	eksExists   bool // the cluster already exists: POST /clusters answers 409
	eksActiveAt int  // GET /clusters/{name} answers ACTIVE from this poll on (1 = immediately); 0 = never
	ssmPuts     []string

	// state
	ami       string
	roles     map[string]bool
	profiles  map[string][]string
	queues    map[string][]string // name -> messages
	inflight  map[string]string   // receipt -> queue
	lts       map[string]map[string]string
	instances map[string]*fakeInstance
	nextID    int
	eksGets   int
	eksPosts  int
	cluster   string
	params    map[string]string
	typeSet   map[string]string // instance type -> on-demand price
}

type fakeInstance struct {
	id, typ, az, lifecycle string
	tags                   map[string]string
	state                  string
	describes              int
}

func newFake(t *testing.T) (*fakeAWS, *httptest.Server) {
	f := &fakeAWS{
		t: t, ami: "ami-0abcdef1234567891", eksActiveAt: 1,
		roles: map[string]bool{}, profiles: map[string][]string{}, queues: map[string][]string{}, inflight: map[string]string{},
		lts: map[string]map[string]string{}, instances: map[string]*fakeInstance{}, params: map[string]string{},
		rewrite: map[string]func(string) string{}, fail: map[string]int{}, cluster: "meridian-g0",
		typeSet: map[string]string{"m5.large": "0.096", "c5.large": "0.085", "t3.medium": "0.0416"},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

// probeCfg is a Config that talks to the fake quickly.
func (f *fakeAWS) cfg(url string) Config {
	return Config{
		Endpoint: url, Region: "us-west-2", Access: "AKIAFAKEFAKE", Secret: "fake/secret+key", Cluster: f.cluster,
		AMIParam:  "/aws/service/eks/optimized-ami/1.35/amazon-linux-2023/x86_64/standard/recommended/image_id",
		QueueName: "meridian-g0-interruption", EKSCreate: true,
		Wait: 2 * time.Second, Retries: 4, Backoff: time.Millisecond, PollInterval: time.Millisecond,
	}
}

func (f *fakeAWS) called(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == op {
			n++
		}
	}
	return n
}

func scopeService(auth string) string {
	i := strings.Index(auth, "Credential=")
	if i < 0 {
		return ""
	}
	parts := strings.Split(strings.SplitN(auth[i+len("Credential="):], ",", 2)[0], "/")
	if len(parts) < 5 {
		return ""
	}
	return parts[3]
}

var targetPrefix = map[string]string{"sqs": "AmazonSQS.", "ssm": "AmazonSSM.", "pricing": "AWSPriceListService."}

func (f *fakeAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	svc := scopeService(r.Header.Get("Authorization"))
	if svc == "" {
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}
	var op string
	var form url.Values
	switch {
	case r.Header.Get("X-Amz-Target") != "":
		target := r.Header.Get("X-Amz-Target")
		pre := targetPrefix[svc]
		if pre == "" || !strings.HasPrefix(target, pre) {
			http.Error(w, "bad X-Amz-Target "+target+" for "+svc, http.StatusBadRequest)
			return
		}
		op = strings.TrimPrefix(target, pre)
	case svc == "eks":
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/clusters":
			op = "CreateCluster"
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/clusters/") && strings.Count(r.URL.Path, "/") == 2:
			op = "DescribeCluster"
		default:
			http.Error(w, "the gateway does not route "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
			return
		}
	default:
		form, _ = url.ParseQuery(string(body))
		op = form.Get("Action")
	}
	key := svc + ":" + op

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	if svc == "ec2" && f.cold > 0 {
		f.cold--
		http.Error(w, "ec2 is still starting", http.StatusServiceUnavailable)
		return
	}
	if st, ok := f.fail[key]; ok {
		http.Error(w, `<ErrorResponse><Error><Code>InternalFailure</Code><Message>forced failure of `+key+`</Message></Error></ErrorResponse>`, st)
		return
	}
	status, ctype, out := f.dispatch(svc, op, form, body, r)
	if fn, ok := f.rewrite[key]; ok {
		out = fn(out)
	}
	out = f.dropFields(out, ctype)
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(out))
}

// dropFields removes every element (XML) or key (JSON) named in f.drop.
func (f *fakeAWS) dropFields(out, ctype string) string {
	for _, name := range f.drop {
		if strings.Contains(ctype, "json") {
			var v any
			if json.Unmarshal([]byte(out), &v) == nil {
				dropKey(v, name)
				b, _ := json.Marshal(v)
				out = string(b)
			}
			continue
		}
		out = regexp.MustCompile(`(?s)<`+regexp.QuoteMeta(name)+`>.*?</`+regexp.QuoteMeta(name)+`>|<`+regexp.QuoteMeta(name)+`/>`).ReplaceAllString(out, "")
	}
	return out
}

func dropKey(v any, name string) {
	switch x := v.(type) {
	case map[string]any:
		delete(x, name)
		for _, c := range x {
			dropKey(c, name)
		}
	case []any:
		for _, c := range x {
			dropKey(c, name)
		}
	}
}

const (
	xmlT  = "text/xml"
	jsonT = "application/x-amz-json-1.1"
)

func (f *fakeAWS) newID(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s%017x", prefix, f.nextID)
}

func jsonOut(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func awsErr(code, msg string) (int, string, string) {
	return http.StatusBadRequest, jsonT, jsonOut(map[string]string{"__type": code, "message": msg})
}

func xmlErr(code string) (int, string, string) {
	return http.StatusBadRequest, xmlT, `<ErrorResponse><Error><Code>` + code + `</Code></Error></ErrorResponse>`
}

func ok(root, inner string) (int, string, string) {
	return 200, xmlT, `<` + root + `Response xmlns="urn:fake"><` + root + `Result>` + inner + `</` + root + `Result><requestId>r</requestId></` + root + `Response>`
}

func okPlain(root, inner string) (int, string, string) {
	return 200, xmlT, `<` + root + ` xmlns="urn:fake">` + inner + `<requestId>r</requestId></` + root + `>`
}

// filters reads Filter.N.Name / Filter.N.Value.M into name -> values.
func filters(form url.Values) map[string][]string {
	out := map[string][]string{}
	for n := 1; ; n++ {
		name := form.Get(fmt.Sprintf("Filter.%d.Name", n))
		if name == "" {
			return out
		}
		for m := 1; ; m++ {
			v := form.Get(fmt.Sprintf("Filter.%d.Value.%d", n, m))
			if v == "" {
				break
			}
			out[name] = append(out[name], v)
		}
	}
}

var tagKeyRE = regexp.MustCompile(`^TagSpecification\.\d+\.Tag\.(\d+)\.Key$`)

func specTags(form url.Values) map[string]string {
	tags := map[string]string{}
	for k, v := range form {
		if m := tagKeyRE.FindStringSubmatch(k); m != nil {
			tags[v[0]] = form.Get(strings.TrimSuffix(k, "Key") + "Value")
		}
	}
	return tags
}

func (f *fakeAWS) dispatch(svc, op string, form url.Values, body []byte, r *http.Request) (int, string, string) {
	switch svc {
	case "sts":
		return ok("GetCallerIdentity", `<Account>000000000000</Account><Arn>arn:aws:iam::000000000000:root</Arn>`)
	case "ssm":
		return f.ssm(op, body)
	case "iam":
		return f.iam(op, form)
	case "sqs":
		return f.sqs(op, body)
	case "eks":
		return f.eks(op, r.URL.Path)
	case "pricing":
		return f.pricing(body)
	case "ec2":
		return f.ec2(op, form)
	}
	return awsErr("UnknownService", svc)
}

func (f *fakeAWS) ssm(op string, body []byte) (int, string, string) {
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	name, _ := req["Name"].(string)
	switch op {
	case "PutParameter":
		f.ssmPuts = append(f.ssmPuts, name)
		if strings.HasPrefix(name, "/aws") || strings.HasPrefix(name, "/ssm") {
			return awsErr("ValidationException", `can't be prefixed with "aws" or "ssm"`)
		}
		f.params[name] = req["Value"].(string)
		return 200, jsonT, `{"Version":1,"Tier":"Standard"}`
	case "GetParameter":
		if strings.HasPrefix(name, "/aws/service/eks/optimized-ami/") && strings.HasSuffix(name, "/image_id") && !f.noAMIParam {
			return 200, jsonT, jsonOut(map[string]any{"Parameter": map[string]any{"Name": name, "Type": "String", "Value": f.ami, "Version": 1}})
		}
		if v, ok := f.params[name]; ok {
			return 200, jsonT, jsonOut(map[string]any{"Parameter": map[string]any{"Name": name, "Type": "String", "Value": v, "Version": 1}})
		}
		return awsErr("ParameterNotFound", name)
	}
	return awsErr("UnknownOperation", op)
}

func (f *fakeAWS) iam(op string, form url.Values) (int, string, string) {
	role, prof := form.Get("RoleName"), form.Get("InstanceProfileName")
	switch op {
	case "CreateRole":
		f.roles[role] = true
		return ok("CreateRole", `<Role><RoleName>`+role+`</RoleName><Arn>arn:aws:iam::000000000000:role/`+role+`</Arn></Role>`)
	case "GetRole":
		if !f.roles[role] {
			return xmlErr("NoSuchEntity")
		}
		return ok("GetRole", `<Role><RoleName>`+role+`</RoleName><Arn>arn:aws:iam::000000000000:role/`+role+`</Arn></Role>`)
	case "CreateInstanceProfile":
		f.profiles[prof] = nil
		return ok("CreateInstanceProfile", `<InstanceProfile><InstanceProfileName>`+prof+`</InstanceProfileName></InstanceProfile>`)
	case "AddRoleToInstanceProfile":
		f.profiles[prof] = append(f.profiles[prof], role)
		return okPlain("AddRoleToInstanceProfileResponse", "")
	case "GetInstanceProfile":
		roles, found := f.profiles[prof]
		if !found {
			return xmlErr("NoSuchEntity")
		}
		var rs string
		for _, r := range roles {
			rs += `<member><RoleName>` + r + `</RoleName></member>`
		}
		return ok("GetInstanceProfile", `<InstanceProfile><InstanceProfileName>`+prof+`</InstanceProfileName><Arn>arn:aws:iam::000000000000:instance-profile/`+prof+`</Arn><Roles>`+rs+`</Roles></InstanceProfile>`)
	case "ListInstanceProfiles":
		var names []string
		for n := range f.profiles {
			names = append(names, n)
		}
		sort.Strings(names)
		var ms string
		for _, n := range names {
			ms += `<member><InstanceProfileName>` + n + `</InstanceProfileName></member>`
		}
		return ok("ListInstanceProfiles", `<InstanceProfiles>`+ms+`</InstanceProfiles>`)
	case "TagInstanceProfile":
		return okPlain("TagInstanceProfileResponse", "")
	case "RemoveRoleFromInstanceProfile":
		f.profiles[prof] = nil
		return okPlain("RemoveRoleFromInstanceProfileResponse", "")
	case "DeleteInstanceProfile":
		delete(f.profiles, prof)
		return okPlain("DeleteInstanceProfileResponse", "")
	}
	return xmlErr("UnknownOperation")
}

func (f *fakeAWS) sqs(op string, body []byte) (int, string, string) {
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	name, _ := req["QueueName"].(string)
	qurl, _ := req["QueueUrl"].(string)
	q := strings.TrimPrefix(qurl, "http://floci:4566/000000000000/")
	switch op {
	case "CreateQueue":
		if _, ok := f.queues[name]; !ok {
			f.queues[name] = nil
		}
		return 200, jsonT, jsonOut(map[string]string{"QueueUrl": "http://floci:4566/000000000000/" + name})
	case "GetQueueUrl":
		if _, ok := f.queues[name]; !ok {
			return awsErr("QueueDoesNotExist", name)
		}
		return 200, jsonT, jsonOut(map[string]string{"QueueUrl": "http://floci:4566/000000000000/" + name})
	case "SendMessage":
		f.queues[q] = append(f.queues[q], req["MessageBody"].(string))
		return 200, jsonT, `{"MessageId":"m1","MD5OfMessageBody":"x"}`
	case "ReceiveMessage":
		if len(f.queues[q]) == 0 {
			return 200, jsonT, `{}`
		}
		msg := f.queues[q][0]
		f.queues[q] = f.queues[q][1:]
		f.inflight["rh-1"] = q
		return 200, jsonT, jsonOut(map[string]any{"Messages": []map[string]string{{"MessageId": "m1", "ReceiptHandle": "rh-1", "Body": msg}}})
	case "DeleteMessage":
		if _, ok := f.inflight[req["ReceiptHandle"].(string)]; !ok {
			return awsErr("ReceiptHandleIsInvalid", "unknown handle")
		}
		return 200, jsonT, `{}`
	}
	return awsErr("UnknownOperation", op)
}

func (f *fakeAWS) eks(op, path string) (int, string, string) {
	cluster := func(status string) string {
		return jsonOut(map[string]any{"cluster": map[string]any{
			"name": f.cluster, "arn": "arn:aws:eks:us-west-2:000000000000:cluster/" + f.cluster, "status": status, "version": "1.35",
			"endpoint": "https://" + f.cluster + ".eks.local", "kubernetesNetworkConfig": map[string]any{"serviceIpv4Cidr": "10.100.0.0/16"},
		}})
	}
	switch op {
	case "CreateCluster":
		f.eksPosts++
		if f.eksExists {
			return 409, jsonT, `{"__type":"ResourceInUseException","message":"Cluster already exists with name: ` + f.cluster + `"}`
		}
		f.eksExists = true
		return 200, jsonT, cluster("CREATING")
	case "DescribeCluster":
		if path != "/clusters/"+f.cluster || !f.eksExists {
			return 404, jsonT, `{"__type":"ResourceNotFoundException","message":"No cluster found for name: ` + strings.TrimPrefix(path, "/clusters/") + `"}`
		}
		f.eksGets++
		if f.eksActiveAt > 0 && f.eksGets >= f.eksActiveAt {
			return 200, jsonT, cluster("ACTIVE")
		}
		return 200, jsonT, cluster("CREATING")
	}
	return awsErr("UnknownOperation", op)
}

func (f *fakeAWS) pricing(body []byte) (int, string, string) {
	var req struct{ Filters []map[string]string }
	_ = json.Unmarshal(body, &req)
	want := ""
	for _, fl := range req.Filters {
		if fl["Field"] == "instanceType" {
			want = fl["Value"]
		}
	}
	var docs []string
	var names []string
	for t := range f.typeSet {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		if want == "" || want == t {
			docs = append(docs, priceDoc(t, f.typeSet[t]))
		}
	}
	return 200, jsonT, jsonOut(map[string]any{"FormatVersion": "aws_v1", "PriceList": docs})
}

func (f *fakeAWS) ec2(op string, form url.Values) (int, string, string) {
	switch op {
	case "DescribeAvailabilityZones":
		var s string
		for _, z := range []string{"a", "b", "c"} {
			s += `<item><zoneName>us-west-2` + z + `</zoneName><zoneId>usw2-az` + strconv.Itoa(len(s)%7+1) + `</zoneId><zoneState>available</zoneState></item>`
		}
		return okPlain("DescribeAvailabilityZonesResponse", `<availabilityZoneInfo>`+s+`</availabilityZoneInfo>`)
	case "DescribeSubnets":
		if f.noSubnets {
			return okPlain("DescribeSubnetsResponse", `<subnetSet/>`)
		}
		return okPlain("DescribeSubnetsResponse", `<subnetSet>
<item><subnetId>subnet-aaa</subnetId><availabilityZone>us-west-2a</availabilityZone><vpcId>vpc-1</vpcId></item>
<item><subnetId>subnet-bbb</subnetId><availabilityZone>us-west-2b</availabilityZone><vpcId>vpc-1</vpcId></item></subnetSet>`)
	case "DescribeSecurityGroups":
		return okPlain("DescribeSecurityGroupsResponse", `<securityGroupInfo><item><groupId>sg-111</groupId></item></securityGroupInfo>`)
	case "DescribeImages":
		if f.noAMIImage || form.Get("ImageId.1") != f.ami {
			return okPlain("DescribeImagesResponse", `<imagesSet/>`)
		}
		return okPlain("DescribeImagesResponse", `<imagesSet><item><imageId>`+f.ami+`</imageId><architecture>x86_64</architecture><creationDate>2026-09-01T00:00:00.000Z</creationDate><rootDeviceName>/dev/xvda</rootDeviceName></item></imagesSet>`)
	case "DescribeInstanceTypes":
		item := func(t string) string {
			return `<item><instanceType>` + t + `</instanceType><vCpuInfo><defaultVCpus>2</defaultVCpus></vCpuInfo><memoryInfo><sizeInMiB>8192</sizeInMiB></memoryInfo>` +
				`<processorInfo><supportedArchitectures><item>x86_64</item></supportedArchitectures></processorInfo>` +
				`<networkInfo><maximumNetworkInterfaces>3</maximumNetworkInterfaces><ipv4AddressesPerInterface>10</ipv4AddressesPerInterface></networkInfo>` +
				`<supportedUsageClasses><item>on-demand</item><item>spot</item></supportedUsageClasses></item>`
		}
		if t := form.Get("InstanceType.1"); t != "" {
			return okPlain("DescribeInstanceTypesResponse", `<instanceTypeSet>`+item(t)+`</instanceTypeSet>`)
		}
		return okPlain("DescribeInstanceTypesResponse", `<instanceTypeSet>`+item("m5.large")+item("c5.large")+item("t3.medium")+`</instanceTypeSet>`)
	case "DescribeInstanceTypeOfferings":
		az := filters(form)["location"]
		loc := "us-west-2a"
		if len(az) > 0 {
			loc = az[0]
		}
		return okPlain("DescribeInstanceTypeOfferingsResponse", `<instanceTypeOfferingSet><item><instanceType>m5.large</instanceType><location>`+loc+`</location></item><item><instanceType>c5.large</instanceType><location>`+loc+`</location></item></instanceTypeOfferingSet>`)
	case "DescribeSpotPriceHistory":
		return okPlain("DescribeSpotPriceHistoryResponse", `<spotPriceHistorySet><item><instanceType>m5.large</instanceType><availabilityZone>us-west-2a</availabilityZone><spotPrice>0.0312</spotPrice><productDescription>Linux/UNIX</productDescription></item></spotPriceHistorySet>`)
	case "CreateLaunchTemplate":
		name := form.Get("LaunchTemplateName")
		f.lts[name] = specTagsFor(form, "launch-template")
		return okPlain("CreateLaunchTemplateResponse", `<launchTemplate><launchTemplateId>lt-0123456789</launchTemplateId><launchTemplateName>`+name+`</launchTemplateName></launchTemplate>`)
	case "DescribeLaunchTemplates":
		var s string
		fl := filters(form)
		for name, tags := range f.lts {
			if n := form.Get("LaunchTemplateName.1"); n != "" && n != name {
				continue
			}
			if v := fl["tag:karpenter.k8s.aws/cluster"]; len(v) > 0 && (f.noTagFilter || tags["karpenter.k8s.aws/cluster"] != v[0]) {
				continue
			}
			s += `<item><launchTemplateName>` + name + `</launchTemplateName></item>`
		}
		return okPlain("DescribeLaunchTemplatesResponse", `<launchTemplates>`+s+`</launchTemplates>`)
	case "DeleteLaunchTemplate":
		delete(f.lts, form.Get("LaunchTemplateName"))
		return okPlain("DeleteLaunchTemplateResponse", `<launchTemplate><launchTemplateName>`+form.Get("LaunchTemplateName")+`</launchTemplateName></launchTemplate>`)
	case "CreateFleet":
		if _, ok := f.lts[form.Get("LaunchTemplateConfigs.1.LaunchTemplateSpecification.LaunchTemplateName")]; !ok {
			return xmlErr("InvalidLaunchTemplateName.NotFoundException")
		}
		in := &fakeInstance{id: f.newID("i-"), typ: form.Get("LaunchTemplateConfigs.1.Overrides.1.InstanceType"), az: form.Get("LaunchTemplateConfigs.1.Overrides.1.AvailabilityZone"),
			lifecycle: "spot", tags: map[string]string{}, state: "pending"}
		if !f.noFleetTags {
			in.tags = specTags(form)
		}
		f.instances[in.id] = in
		return okPlain("CreateFleetResponse", `<fleetId>fleet-1</fleetId><errorSet/><fleetInstanceSet><item><instanceIds><item>`+in.id+`</item></instanceIds><instanceType>`+in.typ+`</instanceType><lifecycle>spot</lifecycle></item></fleetInstanceSet>`)
	case "RunInstances":
		in := &fakeInstance{id: f.newID("i-"), typ: form.Get("InstanceType"), az: "us-west-2a", tags: map[string]string{}, state: "pending"}
		f.instances[in.id] = in
		return okPlain("RunInstancesResponse", `<instancesSet><item><instanceId>`+in.id+`</instanceId></item></instancesSet>`)
	case "DescribeInstances":
		return f.describeInstances(form)
	case "DescribeInstanceStatus": // real shape: instanceStatusSet/item with state, instanceStatus and systemStatus
		var set string
		for _, id := range sortedKeys(f.instances) {
			if want := form.Get("InstanceId.1"); want != "" && want != id {
				continue
			}
			in := f.instances[id]
			set += `<item><instanceId>` + id + `</instanceId><availabilityZone>` + in.az + `</availabilityZone>` +
				`<instanceState><name>` + in.state + `</name></instanceState>` +
				`<instanceStatus><status>ok</status></instanceStatus><systemStatus><status>ok</status></systemStatus></item>`
		}
		return okPlain("DescribeInstanceStatusResponse", `<instanceStatusSet>`+set+`</instanceStatusSet>`)
	case "CreateTags":
		for n := 1; form.Get(fmt.Sprintf("ResourceId.%d", n)) != ""; n++ {
			if in := f.instances[form.Get(fmt.Sprintf("ResourceId.%d", n))]; in != nil {
				for m := 1; form.Get(fmt.Sprintf("Tag.%d.Key", m)) != ""; m++ {
					in.tags[form.Get(fmt.Sprintf("Tag.%d.Key", m))] = form.Get(fmt.Sprintf("Tag.%d.Value", m))
				}
			}
		}
		return okPlain("CreateTagsResponse", `<return>true</return>`)
	case "TerminateInstances":
		var s string
		for n := 1; form.Get(fmt.Sprintf("InstanceId.%d", n)) != ""; n++ {
			in := f.instances[form.Get(fmt.Sprintf("InstanceId.%d", n))]
			if in == nil {
				return xmlErr("InvalidInstanceID.NotFound")
			}
			in.state = "shutting-down"
			s += `<item><instanceId>` + in.id + `</instanceId><currentState><name>shutting-down</name></currentState></item>`
		}
		return okPlain("TerminateInstancesResponse", `<instancesSet>`+s+`</instancesSet>`)
	}
	return xmlErr("InvalidAction")
}

func specTagsFor(form url.Values, _ string) map[string]string { return specTags(form) }

func (f *fakeAWS) describeInstances(form url.Values) (int, string, string) {
	fl := filters(form)
	var ids []string
	for id := range f.instances {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var s string
	for _, id := range ids {
		in := f.instances[id]
		if want := form.Get("InstanceId.1"); want != "" && want != id {
			continue
		}
		in.describes++
		switch {
		case in.state == "shutting-down" && in.describes > 0 && in.describes%2 == 0:
			in.state = "terminated"
		case in.state == "pending" && in.describes > f.pendingFor:
			in.state = "running"
		}
		if k := fl["tag-key"]; len(k) > 0 {
			if _, has := in.tags[k[0]]; !has {
				continue
			}
		}
		if st := fl["instance-state-name"]; len(st) > 0 {
			match := false
			for _, x := range st {
				match = match || x == in.state
			}
			if !match {
				continue
			}
		}
		var tags string
		var keys []string
		for k := range in.tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			tags += `<item><key>` + k + `</key><value>` + in.tags[k] + `</value></item>`
		}
		lifecycle := ""
		if in.lifecycle != "" {
			lifecycle = `<instanceLifecycle>` + in.lifecycle + `</instanceLifecycle>`
		}
		s += `<item><instancesSet><item><instanceId>` + id + `</instanceId><instanceState><name>` + in.state + `</name></instanceState>` +
			`<privateIpAddress>10.0.1.7</privateIpAddress><placement><availabilityZone>` + in.az + `</availabilityZone></placement>` +
			`<instanceType>` + in.typ + `</instanceType>` + lifecycle + `<tagSet>` + tags + `</tagSet></item></instancesSet></item>`
	}
	return okPlain("DescribeInstancesResponse", `<reservationSet>`+s+`</reservationSet>`)
}

// sortedKeys returns the instance ids in a stable order.
func sortedKeys(m map[string]*fakeInstance) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
