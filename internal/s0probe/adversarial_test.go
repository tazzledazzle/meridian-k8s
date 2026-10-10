package s0probe

// Adversarial tests written by an independent tester. They use the existing fakeAWS (fake_test.go) but make it
// deficient or oddly shaped, and they poke the HTTP layer, the XML tree, the report writers and the secret
// redaction. A test named TestBug... documents a defect that is present in the code as tested.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/ops"
)

// ---------------------------------------------------------------------------------------------------------
// helpers

// reqOp returns the service and operation of a request and leaves the body readable.
func reqOp(r *http.Request) (svc, op string) {
	svc = scopeService(r.Header.Get("Authorization"))
	if t := r.Header.Get("X-Amz-Target"); t != "" {
		_, op, _ = strings.Cut(t, ".")
		return
	}
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	if svc == "eks" {
		if r.Method == http.MethodPost {
			return svc, "CreateCluster"
		}
		return svc, "DescribeCluster"
	}
	form, _ := url.ParseQuery(string(b))
	return svc, form.Get("Action")
}

// shaper rewrites a fake's answer on the way out.
type shaper func(svc, op string, r *http.Request, status *int, hdr http.Header, body *string)

func shaped(f *fakeAWS, s shaper) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		svc, op := reqOp(r)
		rec := httptest.NewRecorder()
		f.ServeHTTP(rec, r)
		status, body := rec.Code, rec.Body.String()
		hdr := rec.Header().Clone()
		s(svc, op, r, &status, hdr, &body)
		for k, v := range hdr {
			w.Header()[k] = v
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func runShaped(t *testing.T, f *fakeAWS, s shaper, mut func(*Config)) ([]Result, map[string]Result) {
	t.Helper()
	srv := httptest.NewServer(shaped(f, s))
	t.Cleanup(srv.Close)
	return probe(t, f, srv.URL, mut)
}

func expectAllPass(t *testing.T, rs []Result) {
	t.Helper()
	for _, r := range rs {
		if r.Status != "pass" {
			t.Errorf("%s: %s: %s", r.Name, r.Status, r.Detail)
		}
	}
}

func mustStatus(t *testing.T, m map[string]Result, name, want string) {
	t.Helper()
	r, ok := m[name]
	if !ok {
		t.Fatalf("no result for %q", name)
	}
	if r.Status != want {
		t.Errorf("%s: status %s (%s), want %s", name, r.Status, r.Detail, want)
	}
}

var startTag = regexp.MustCompile(`<[A-Za-z][^>]*[^/]>`)

// decorateXML adds a declaration, a comment, unknown elements with attributes and indentation.
func decorateXML(body string) string {
	loc := startTag.FindStringIndex(body)
	if loc == nil {
		return body
	}
	body = body[:loc[1]] + `<!-- vendor comment --><vendorExtra b="2" a="1"><nested><x/></nested></vendorExtra>` + body[loc[1]:]
	body = strings.ReplaceAll(body, "><", ">\n  <")
	return "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" + body + "\n"
}

func decorateJSON(body string) string {
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil {
		return body
	}
	m["vendorExtra"] = map[string]any{"list": []any{1, "two", nil}}
	b, _ := json.MarshalIndent(m, "", "  ")
	return string(b)
}

// ---------------------------------------------------------------------------------------------------------
// 1a. checks that pass when they should fail (too lenient)

func TestDeficientBackendsFailTheRightCheck(t *testing.T) {
	rewriteAll := func(f *fakeAWS, key string, fn func(string) string) { f.rewrite[key] = fn }
	reStatusName := regexp.MustCompile(`<instanceState><name>[^<]*</name></instanceState>`)
	cases := []struct {
		name  string
		setup func(f *fakeAWS)
		cfg   func(c *Config)
		fails []string
	}{
		{"instance never reaches running", func(f *fakeAWS) { f.pendingFor = 1 << 30 }, func(c *Config) { c.Wait = 100 * time.Millisecond },
			[]string{"ec2:DescribeInstances (running, tagged)"}},
		{"fleet tags not applied", func(f *fakeAWS) { f.noFleetTags = true }, nil, []string{"ec2:DescribeInstances (running, tagged)"}},
		{"status of a missing instance", func(f *fakeAWS) {
			rewriteAll(f, "ec2:DescribeInstanceStatus", func(string) string {
				return `<DescribeInstanceStatusResponse><instanceStatusSet/></DescribeInstanceStatusResponse>`
			})
		}, nil, []string{"ec2:DescribeInstanceStatus"}},
		{"status lists only some other instance", func(f *fakeAWS) {
			rewriteAll(f, "ec2:DescribeInstanceStatus", func(s string) string {
				return regexp.MustCompile(`<instanceId>[^<]*</instanceId>`).ReplaceAllString(s, "<instanceId>i-other</instanceId>")
			})
		}, nil, []string{"ec2:DescribeInstanceStatus"}},
		{"status without a state name", func(f *fakeAWS) {
			rewriteAll(f, "ec2:DescribeInstanceStatus", func(s string) string {
				return reStatusName.ReplaceAllString(s, "<instanceState><name></name></instanceState>")
			})
		}, nil, []string{"ec2:DescribeInstanceStatus"}},
		{"SSM value is not an AMI", func(f *fakeAWS) { f.ami = "not-an-ami" }, nil, []string{"ssm:GetParameter (AMI alias path)"}},
		{"SSM value empty", func(f *fakeAWS) { f.ami = "" }, nil, []string{"ssm:GetParameter (AMI alias path)"}},
		{"SSM value upper case AMI", func(f *fakeAWS) { f.ami = "AMI-0abc" }, nil, []string{"ssm:GetParameter (AMI alias path)"}},
		{"image id of the parameter is unknown to EC2", func(f *fakeAWS) { f.noAMIImage = true }, nil, []string{"ec2:DescribeImages"}},
		{"image without creationDate", func(f *fakeAWS) { f.drop = []string{"creationDate"} }, nil, []string{"ec2:DescribeImages"}},
		{"EKS without serviceIpv4Cidr", func(f *fakeAWS) { f.drop = []string{"serviceIpv4Cidr"} }, nil, []string{"eks:DescribeCluster"}},
		{"EKS never ACTIVE", func(f *fakeAWS) { f.eksActiveAt = 0 }, func(c *Config) { c.Wait = 100 * time.Millisecond }, []string{"eks:DescribeCluster"}},
		{"EKS cluster has another name", func(f *fakeAWS) {
			rewriteAll(f, "eks:DescribeCluster", func(s string) string { return strings.Replace(s, `"name":"meridian-g0"`, `"name":"other"`, 1) })
		}, nil, []string{"eks:DescribeCluster"}},
		{"EKS version not numeric", func(f *fakeAWS) {
			rewriteAll(f, "eks:DescribeCluster", func(s string) string { return strings.Replace(s, `"version":"1.35"`, `"version":"latest"`, 1) })
		}, nil, []string{"eks:DescribeCluster"}},
		{"pricing empty PriceList (wrong region)", func(f *fakeAWS) { f.typeSet = map[string]string{} }, nil, []string{"pricing:GetProducts"}},
		{"pricing document under the wrong JSON path", func(f *fakeAWS) {
			rewriteAll(f, "pricing:GetProducts", func(s string) string { return strings.ReplaceAll(s, "OnDemand", "Reserved") })
		}, nil, []string{"pricing:GetProducts"}},
		{"pricing document in another currency", func(f *fakeAWS) {
			rewriteAll(f, "pricing:GetProducts", func(s string) string { return strings.ReplaceAll(s, "USD", "EUR") })
		}, nil, []string{"pricing:GetProducts"}},
		{"pricing price is zero", func(f *fakeAWS) { f.typeSet["m5.large"] = "0.0000000000" }, nil, []string{"pricing:GetProducts"}},
		{"pricing price is negative", func(f *fakeAWS) { f.typeSet["m5.large"] = "-1" }, nil, []string{"pricing:GetProducts"}},
		{"pricing price is not a number", func(f *fakeAWS) { f.typeSet["m5.large"] = "free" }, nil, []string{"pricing:GetProducts"}},
		{"pricing document for the wrong instance type", func(f *fakeAWS) {
			rewriteAll(f, "pricing:GetProducts", func(s string) string {
				return strings.ReplaceAll(s, `instanceType\":\"m5.large`, `instanceType\":\"m5.xlarge`)
			})
		}, nil, []string{"pricing:GetProducts"}},
		{"pricing entries are not strings", func(f *fakeAWS) {
			rewriteAll(f, "pricing:GetProducts", func(string) string { return `{"PriceList":[{"product":{}}]}` })
		}, nil, []string{"pricing:GetProducts"}},
		{"CreateFleet reports capacity errors and no instance", func(f *fakeAWS) {
			rewriteAll(f, "ec2:CreateFleet", func(string) string {
				return `<CreateFleetResponse><fleetId>f-1</fleetId><errorSet><item><errorCode>InsufficientInstanceCapacity</errorCode><errorMessage>no</errorMessage></item></errorSet><fleetInstanceSet/></CreateFleetResponse>`
			})
		}, nil, []string{"ec2:CreateFleet"}},
		{"CreateFleet returns blank instance ids", func(f *fakeAWS) {
			rewriteAll(f, "ec2:CreateFleet", func(string) string {
				return `<CreateFleetResponse><fleetId>f-1</fleetId><fleetInstanceSet><item><instanceIds><item></item></instanceIds></item></fleetInstanceSet></CreateFleetResponse>`
			})
		}, nil, []string{"ec2:CreateFleet"}},
		{"CreateFleet without fleetId", func(f *fakeAWS) {
			rewriteAll(f, "ec2:CreateFleet", func(s string) string { return strings.Replace(s, "<fleetId>fleet-1</fleetId>", "", 1) })
		}, nil, []string{"ec2:CreateFleet"}},
		{"only one tagged subnet and it has a zone", func(f *fakeAWS) {
			rewriteAll(f, "ec2:DescribeSubnets", func(s string) string {
				i := strings.Index(s, "<item>")
				j := strings.Index(s[i+1:], "<item>")
				k := strings.Index(s, "</subnetSet>")
				return s[:i+1+j] + s[k:]
			})
		}, nil, []string{"ec2:DescribeSubnets (discovery tag)"}},
		{"subnet without availability zone", func(f *fakeAWS) { f.drop = []string{"availabilityZone"} }, nil,
			[]string{"ec2:DescribeSubnets (discovery tag)"}},
		{"no discovery-tagged subnets", func(f *fakeAWS) { f.noSubnets = true }, nil, []string{"ec2:DescribeSubnets (discovery tag)"}},
		{"launch template tag filter matches nothing", func(f *fakeAWS) { f.noTagFilter = true }, nil, []string{"ec2:DescribeLaunchTemplates"}},
		{"TerminateInstances answers running", func(f *fakeAWS) {
			rewriteAll(f, "ec2:TerminateInstances", func(s string) string { return strings.ReplaceAll(s, "shutting-down", "running") })
		}, nil, []string{"ec2:TerminateInstances"}},
		{"TerminateInstances answers for one instance of two", func(f *fakeAWS) {
			rewriteAll(f, "ec2:TerminateInstances", func(s string) string {
				i := strings.Index(s, "</item>")
				return s[:strings.Index(s, "<instancesSet>")] + "<instancesSet>" + s[strings.Index(s, "<item>"):i+len("</item>")] + "</instancesSet></TerminateInstancesResponse>"
			})
		}, nil, []string{"ec2:TerminateInstances"}},
		{"spot price is zero", func(f *fakeAWS) {
			rewriteAll(f, "ec2:DescribeSpotPriceHistory", func(s string) string { return strings.Replace(s, "0.0312", "0", 1) })
		}, nil, []string{"ec2:DescribeSpotPriceHistory"}},
		{"spot price is text", func(f *fakeAWS) {
			rewriteAll(f, "ec2:DescribeSpotPriceHistory", func(s string) string { return strings.Replace(s, "0.0312", "cheap", 1) })
		}, nil, []string{"ec2:DescribeSpotPriceHistory"}},
		{"instance type without usage classes", func(f *fakeAWS) { f.drop = []string{"supportedUsageClasses"} }, nil, []string{"ec2:DescribeInstanceTypes"}},
		{"offerings do not contain the probe type", func(f *fakeAWS) {
			rewriteAll(f, "ec2:DescribeInstanceTypeOfferings", func(s string) string { return strings.ReplaceAll(s, "m5.large", "z9.huge") })
		}, nil, []string{"ec2:DescribeInstanceTypeOfferings"}},
		{"ReceiveMessage never delivers", func(f *fakeAWS) {
			rewriteAll(f, "sqs:ReceiveMessage", func(string) string { return `{}` })
		}, func(c *Config) { c.Wait = 100 * time.Millisecond }, []string{"sqs:ReceiveMessage"}},
		{"ReceiveMessage delivers another body", func(f *fakeAWS) {
			rewriteAll(f, "sqs:ReceiveMessage", func(string) string { return `{"Messages":[{"Body":"other","ReceiptHandle":"r"}]}` })
		}, nil, []string{"sqs:ReceiveMessage"}},
		{"GetQueueUrl answers a different URL", func(f *fakeAWS) {
			rewriteAll(f, "sqs:GetQueueUrl", func(string) string { return `{"QueueUrl":"http://elsewhere/q"}` })
		}, nil, []string{"sqs:GetQueueUrl"}},
		{"GetCallerIdentity without account", func(f *fakeAWS) { f.drop = []string{"Account"} }, nil, []string{"sts:GetCallerIdentity"}},
		{"GetRole returns another role name", func(f *fakeAWS) {
			rewriteAll(f, "iam:GetRole", func(s string) string {
				return regexp.MustCompile(`<RoleName>[^<]*</RoleName>`).ReplaceAllString(s, "<RoleName>x</RoleName>")
			})
		}, nil, []string{"iam:GetRole"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFake(t)
			tc.setup(f)
			_, m := probe(t, f, srv.URL, tc.cfg)
			for _, n := range tc.fails {
				mustStatus(t, m, n, "fail")
			}
		})
	}
}

// HTTP 200 whose body is an AWS error document. AWS itself never does this, but a mock or a proxy may.
// Informational: it lists the checks that read nothing from the answer and so pass on it.
func TestInfoErrorBodyWithHTTP200IsAPassForChecksThatReadNothing(t *testing.T) {
	f, srv := newFake(t)
	_, base := probe(t, f, srv.URL, nil)
	var lenient []string
	for name := range base {
		if base[name].Status != "pass" {
			t.Fatalf("baseline: %s %s", name, base[name].Detail)
		}
	}
	for _, name := range []string{"iam:CreateRole", "iam:AddRoleToInstanceProfile", "iam:TagInstanceProfile", "sqs:SendMessage", "sqs:DeleteMessage",
		"ssm:PutParameter", "eks:CreateCluster", "ec2:DeleteLaunchTemplate", "iam:DeleteInstanceProfile", "iam:RemoveRoleFromInstanceProfile"} {
		f2, _ := newFake(t)
		op := name
		s := func(svc, o string, r *http.Request, status *int, hdr http.Header, body *string) {
			if svc+":"+o == op {
				*status = 200
				if strings.Contains(hdr.Get("Content-Type"), "json") {
					*body = `{"__type":"InternalFailure","message":"boom"}`
				} else {
					*body = `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`
				}
			}
		}
		_, m := runShaped(t, f2, s, nil)
		if m[name].Status == "pass" {
			lenient = append(lenient, name)
		}
	}
	t.Logf("checks that pass on an HTTP 200 error document: %v", lenient)
}

// ---------------------------------------------------------------------------------------------------------
// 1b. checks that fail when they should pass (too strict)

func TestVariantResponseShapesStillPass(t *testing.T) {
	variants := map[string]shaper{
		"extra elements, comments, indentation and an XML declaration": func(svc, op string, r *http.Request, st *int, h http.Header, b *string) {
			if strings.Contains(h.Get("Content-Type"), "json") {
				*b = decorateJSON(*b)
			} else {
				*b = decorateXML(*b)
			}
		},
		"wrong content type everywhere": func(svc, op string, r *http.Request, st *int, h http.Header, b *string) {
			h.Set("Content-Type", "text/plain")
		},
		"no content type at all": func(svc, op string, r *http.Request, st *int, h http.Header, b *string) {
			h.Del("Content-Type")
		},
		"prefixed namespace on every element name": func(svc, op string, r *http.Request, st *int, h http.Header, b *string) {
			if strings.Contains(h.Get("Content-Type"), "json") {
				return
			}
			*b = regexp.MustCompile(`<(/?)([A-Za-z])`).ReplaceAllString(*b, "<${1}ns:${2}")
			*b = strings.Replace(*b, ` xmlns="urn:fake"`, ` xmlns:ns="urn:fake"`, 1)
		},
		"CDATA text and character references": func(svc, op string, r *http.Request, st *int, h http.Header, b *string) {
			if strings.Contains(h.Get("Content-Type"), "json") {
				return
			}
			*b = regexp.MustCompile(`<(instanceId|subnetId|groupId|zoneName)>([^<]*)</`).ReplaceAllString(*b, "<$1><![CDATA[$2]]></")
		},
		"EKS create answered 201": func(svc, op string, r *http.Request, st *int, h http.Header, b *string) {
			if svc == "eks" && op == "CreateCluster" && *st == 200 {
				*st = 201
			}
		},
	}
	for name, s := range variants {
		t.Run(name, func(t *testing.T) {
			f, _ := newFake(t)
			rs, _ := runShaped(t, f, s, nil)
			expectAllPass(t, rs)
		})
	}
}

func TestInstanceWithoutSpotLifecycleStillPasses(t *testing.T) {
	f, srv := newFake(t)
	f.rewrite["ec2:DescribeInstances"] = func(s string) string {
		return regexp.MustCompile(`<instanceLifecycle>[^<]*</instanceLifecycle>`).ReplaceAllString(s, "")
	}
	rs, m := probe(t, f, srv.URL, nil)
	expectAllPass(t, rs)
	if !strings.Contains(m["ec2:DescribeInstances (running, tagged)"].Detail, "NOTE") {
		t.Errorf("a missing instanceLifecycle should be noted: %q", m["ec2:DescribeInstances (running, tagged)"].Detail)
	}
}

func TestPricingPaginationWithNextTokenIsFollowed(t *testing.T) {
	f, _ := newFake(t)
	s := func(svc, op string, r *http.Request, st *int, h http.Header, b *string) {
		if svc != "pricing" {
			return
		}
		var out struct{ PriceList []string }
		_ = json.Unmarshal([]byte(*b), &out)
		rb, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(rb, &req)
		i := 0
		if tok, _ := req["NextToken"].(string); tok != "" {
			fmt.Sscanf(tok, "page-%d", &i)
		}
		page := map[string]any{"FormatVersion": "aws_v1", "PriceList": []string{}}
		if i < len(out.PriceList) {
			page["PriceList"] = out.PriceList[i : i+1]
		}
		if i+1 < len(out.PriceList) {
			page["NextToken"] = fmt.Sprintf("page-%d", i+1)
		}
		bb, _ := json.Marshal(page)
		*b = string(bb)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bb, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(bb))
		shaped(f, func(svc, op string, rr *http.Request, st *int, h http.Header, b *string) {
			rr.Body = io.NopCloser(bytes.NewReader(bb))
			s(svc, op, rr, st, h, b)
		}).ServeHTTP(w, r)
	}))
	defer srv.Close()
	_, m := probe(t, f, srv.URL, nil)
	mustStatus(t, m, "pricing:GetProducts", "pass")
	if d := m["pricing:GetProducts"].Detail; !strings.Contains(d, "3 priced types") {
		t.Errorf("all three pages should be counted, detail = %q", d)
	}
}

// Floci serves one product per page (docs/s0/harness.md finding 9). The unfiltered listing only produces a
// count for the report, yet it fails when the catalogue has more than 50 products.
func TestBugPricingUnfilteredListingFailsBeyond50PagesOfOneProduct(t *testing.T) {
	f, _ := newFake(t)
	for i := 0; i < 80; i++ {
		f.typeSet[fmt.Sprintf("x%d.large", i)] = "0.05"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rb, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(rb))
		shaped(f, func(svc, op string, rr *http.Request, st *int, h http.Header, b *string) {
			if svc != "pricing" {
				return
			}
			var out struct{ PriceList []string }
			_ = json.Unmarshal([]byte(*b), &out)
			var req map[string]any
			_ = json.Unmarshal(rb, &req)
			i := 0
			if tok, _ := req["NextToken"].(string); tok != "" {
				fmt.Sscanf(tok, "page-%d", &i)
			}
			page := map[string]any{"FormatVersion": "aws_v1", "PriceList": out.PriceList[i : i+1]}
			if i+1 < len(out.PriceList) {
				page["NextToken"] = fmt.Sprintf("page-%d", i+1)
			}
			bb, _ := json.Marshal(page)
			*b = string(bb)
		}).ServeHTTP(w, r)
	}))
	defer srv.Close()
	_, m := probe(t, f, srv.URL, nil)
	// BUG: listProducts gives up after 50 pages ("pagination did not finish"), so a backend that pages one product at
	// a time and has more than 50 products fails a check whose only use of the listing is a count in the detail.
	mustStatus(t, m, "pricing:GetProducts", "pass")
}

func TestPricingPagingThatNeverEndsIsBounded(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"PriceList":[],"NextToken":"again"}`))
	}))
	defer srv.Close()
	cfg := fastCfg(srv.URL)
	_, err := OnDemandPrices(context.Background(), *cfg, []string{"m5.large"})
	if err == nil || atomic.LoadInt32(&hits) != 50 {
		t.Fatalf("err = %v, hits = %d, want an error after exactly 50 pages", err, hits)
	}
}

// ---------------------------------------------------------------------------------------------------------
// 2. amendment A2

var gatedCoupled = []string{
	"ec2:DescribeLaunchTemplates", "ec2:DescribeInstances (running, tagged)", "ec2:DescribeInstanceStatus", "ec2:CreateTags",
	"ec2:RunInstances", "ec2:TerminateInstances", "ec2:DeleteLaunchTemplate",
}

func TestA2StructureEveryCoupledOperationIsEitherBeforeCreateFleetUngatedOrAfterItAndGated(t *testing.T) {
	checks := Checks()
	fleetIdx := -1
	for i, c := range checks {
		if c.Name == fleetCheck {
			fleetIdx = i
		}
	}
	if fleetIdx < 0 {
		t.Fatal("no CreateFleet check")
	}
	coupled := map[string]bool{}
	for _, c := range ops.Coupled {
		coupled[c] = true
	}
	seen := map[string]bool{}
	for i, c := range checks {
		op := opOf(c.Name)
		if !coupled[op] || c.Name == fleetCheck {
			if c.GatedBy != "" && c.Name != fleetCheck {
				t.Errorf("%s is not coupled but is gated by %s", c.Name, c.GatedBy)
			}
			continue
		}
		seen[op] = true
		if i < fleetIdx && c.GatedBy != "" {
			t.Errorf("%s runs before CreateFleet but is gated by %s", c.Name, c.GatedBy)
		}
		if i > fleetIdx && c.GatedBy != fleetCheck {
			t.Errorf("%s is coupled and runs after CreateFleet but is not gated by it (GatedBy=%q)", c.Name, c.GatedBy)
		}
	}
	for _, c := range ops.Coupled {
		if c != fleetCheck && !seen[c] {
			t.Errorf("coupled operation %s has no check", c)
		}
	}
	// reference data must not be gated and must come after the gated block or be independent of it
	for _, c := range checks {
		if c.Group == "ec2" && !coupled[opOf(c.Name)] && c.GatedBy != "" {
			t.Errorf("reference-data check %s is gated", c.Name)
		}
	}
}

func TestA2CreateFleetFailureSkipsExactlyTheGatedCoupledChecks(t *testing.T) {
	scenarios := map[string]func(f *fakeAWS){
		"HTTP 400": func(f *fakeAWS) { f.fail["ec2:CreateFleet"] = 400 },
		"HTTP 500": func(f *fakeAWS) { f.fail["ec2:CreateFleet"] = 500 },
		"HTTP 403": func(f *fakeAWS) { f.fail["ec2:CreateFleet"] = 403 },
		"no launch": func(f *fakeAWS) {
			f.rewrite["ec2:CreateFleet"] = func(string) string { return `<CreateFleetResponse><fleetInstanceSet/></CreateFleetResponse>` }
		},
		"garbage 200": func(f *fakeAWS) { f.rewrite["ec2:CreateFleet"] = func(string) string { return `not xml at all` } },
	}
	for name, setup := range scenarios {
		t.Run(name, func(t *testing.T) {
			f, srv := newFake(t)
			setup(f)
			rs, m := probe(t, f, srv.URL, nil)
			mustStatus(t, m, fleetCheck, "fail")
			for _, n := range gatedCoupled {
				mustStatus(t, m, n, "skip")
				if !strings.Contains(m[n].Detail, "ec2:CreateFleet") {
					t.Errorf("%s: the skip reason should name ec2:CreateFleet: %q", n, m[n].Detail)
				}
			}
			for _, n := range []string{"ec2:CreateLaunchTemplate", "ec2:DescribeImages", "ec2:DescribeSubnets (discovery tag)", "ec2:DescribeSecurityGroups (discovery tag)",
				"ec2:DescribeAvailabilityZones", "ec2:DescribeInstanceTypes", "ec2:DescribeInstanceTypeOfferings", "ec2:DescribeSpotPriceHistory",
				"pricing:GetProducts", "sts:GetCallerIdentity", "eks:DescribeCluster", "sqs:ReceiveMessage", "ssm:GetParameter"} {
				mustStatus(t, m, n, "pass")
			}
			for _, op := range []string{"RunInstances", "TerminateInstances", "CreateTags", "DescribeInstances", "DescribeInstanceStatus", "DescribeLaunchTemplates", "DeleteLaunchTemplate"} {
				if n := f.called("ec2:" + op); n != 0 {
					t.Errorf("ec2:%s was sent %d times although CreateFleet failed", op, n)
				}
			}
			// exactly the gated set is skipped inside ec2
			for _, r := range rs {
				if r.Group == "ec2" && r.Status == "skip" && !contains(gatedCoupled, r.Name) {
					t.Errorf("unexpected skip %s", r.Name)
				}
			}
			md := Markdown("t", rs)
			for _, want := range []string{"EC2 is the swing factor", "floci, moto, ec2-sim", "Reference-data checks"} {
				if !strings.Contains(md, want) {
					t.Errorf("report lacks %q", want)
				}
			}
		})
	}
}

func TestA2PrerequisiteFailuresSkipCreateFleetWithoutPanic(t *testing.T) {
	scenarios := map[string]func(f *fakeAWS){
		"no subnets":            func(f *fakeAWS) { f.noSubnets = true },
		"no ami parameter":      func(f *fakeAWS) { f.noAMIParam = true },
		"launch template fails": func(f *fakeAWS) { f.fail["ec2:CreateLaunchTemplate"] = 400 },
		"launch template no id": func(f *fakeAWS) {
			f.rewrite["ec2:CreateLaunchTemplate"] = func(string) string {
				return `<CreateLaunchTemplateResponse><launchTemplate/></CreateLaunchTemplateResponse>`
			}
		},
		"subnets call fails": func(f *fakeAWS) { f.fail["ec2:DescribeSubnets"] = 400 },
	}
	for name, setup := range scenarios {
		t.Run(name, func(t *testing.T) {
			f, srv := newFake(t)
			setup(f)
			rs, m := probe(t, f, srv.URL, nil)
			if m[fleetCheck].Status != "skip" {
				t.Errorf("CreateFleet: %s (%s), want skip", m[fleetCheck].Status, m[fleetCheck].Detail)
			}
			if f.called("ec2:CreateFleet") != 0 {
				t.Error("CreateFleet was sent without its prerequisites")
			}
			for _, n := range gatedCoupled {
				if m[n].Status == "pass" {
					t.Errorf("%s passed although CreateFleet did not run", n)
				}
			}
			for _, r := range rs {
				if strings.Contains(r.Detail, "panic") {
					t.Errorf("%s: %s", r.Name, r.Detail)
				}
			}
			if md := Markdown("t", rs); !strings.Contains(md, "EC2 is the swing factor") {
				t.Error("a skipped CreateFleet must still be named as the swing factor")
			}
			if s := Summarize(rs); s.Pass == len(rs) {
				t.Error("summary says everything passed")
			}
		})
	}
}

func TestA2OneSubnetStillRunsCreateFleet(t *testing.T) {
	f, srv := newFake(t)
	f.rewrite["ec2:DescribeSubnets"] = func(s string) string {
		i := strings.Index(s, "<item>")
		j := strings.Index(s[i+1:], "<item>")
		return s[:i+1+j] + s[strings.Index(s, "</subnetSet>"):]
	}
	_, m := probe(t, f, srv.URL, nil)
	mustStatus(t, m, "ec2:DescribeSubnets (discovery tag)", "fail")
	mustStatus(t, m, fleetCheck, "pass")
}

func TestA2SwingFactorNoteOnlyWhenCreateFleetDidNotPass(t *testing.T) {
	f, srv := newFake(t)
	rs, _ := probe(t, f, srv.URL, nil)
	if strings.Contains(Markdown("t", rs), "swing factor") {
		t.Error("swing factor note on an all-pass report")
	}
	f2, srv2 := newFake(t)
	rs2, _ := probe(t, f2, srv2.URL, func(c *Config) { c.Groups = []string{"pricing"} })
	if strings.Contains(Markdown("t", rs2), "swing factor") {
		t.Error("swing factor note although the ec2 group was not selected")
	}
	// only ec2 selected, no SSM: AMI is unknown, so the launch template and the fleet cannot run
	f3, srv3 := newFake(t)
	rs3, m3 := probe(t, f3, srv3.URL, func(c *Config) { c.Groups = []string{"ec2"} })
	mustStatus(t, m3, fleetCheck, "skip")
	if !strings.Contains(Markdown("t", rs3), "swing factor") {
		t.Error("no swing factor note for a skipped CreateFleet")
	}
}

// BUG candidate: the cleanup of a fleet that launched an instance but failed a later field check is skipped.
func TestBugFleetThatLaunchedButFailedLeavesTheInstanceRunning(t *testing.T) {
	f, srv := newFake(t)
	f.rewrite["ec2:CreateFleet"] = func(s string) string { return strings.Replace(s, "<fleetId>fleet-1</fleetId>", "", 1) }
	_, m := probe(t, f, srv.URL, nil)
	mustStatus(t, m, fleetCheck, "fail")
	// BUG: checks.go sets State["instance"] "so the cleanup still finds the instance", but TerminateInstances is
	// gated behind CreateFleet passing, so the instance the fleet launched is never terminated (nor is the launch
	// template deleted).
	for id, in := range f.instances {
		if in.state == "running" || in.state == "pending" {
			t.Errorf("instance %s launched by a failed CreateFleet was left %s", id, in.state)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// 3. retry policy

type callCase struct {
	name     string
	mutating bool
	call     func(e *Env) error
}

func retryCases() []callCase {
	q := func(action string) func(e *Env) error {
		return func(e *Env) error {
			_, err := e.Query(context.Background(), "ec2", ec2Version, action)
			return err
		}
	}
	j := func(svc, target string) func(e *Env) error {
		return func(e *Env) error {
			_, err := e.JSONCall(context.Background(), svc, target, "1.1", map[string]any{"a": 1})
			return err
		}
	}
	return []callCase{
		{"CreateFleet", true, q("CreateFleet")},
		{"RunInstances", true, q("RunInstances")},
		{"TerminateInstances", true, q("TerminateInstances")},
		{"CreateTags", true, q("CreateTags")},
		{"CreateLaunchTemplate", true, q("CreateLaunchTemplate")},
		{"DeleteLaunchTemplate", true, q("DeleteLaunchTemplate")},
		{"CreateQueue", true, j("sqs", "AmazonSQS.CreateQueue")},
		{"SendMessage", true, j("sqs", "AmazonSQS.SendMessage")},
		{"DeleteMessage", true, j("sqs", "AmazonSQS.DeleteMessage")},
		{"PutParameter", true, j("ssm", "AmazonSSM.PutParameter")},
		{"CreateCluster", true, func(e *Env) error {
			_, err := e.REST(context.Background(), "eks", http.MethodPost, "/clusters", map[string]any{"name": "x"})
			return err
		}},
		{"DescribeInstances", false, q("DescribeInstances")},
		{"DescribeInstanceStatus", false, q("DescribeInstanceStatus")},
		{"DescribeSubnets", false, q("DescribeSubnets")},
		{"GetParameter", false, j("ssm", "AmazonSSM.GetParameter")},
		{"GetQueueUrl", false, j("sqs", "AmazonSQS.GetQueueUrl")},
		{"ReceiveMessage", false, j("sqs", "AmazonSQS.ReceiveMessage")},
		{"GetProducts", false, j("pricing", "AWSPriceListService.GetProducts")},
		{"DescribeCluster", false, func(e *Env) error {
			_, err := e.REST(context.Background(), "eks", http.MethodGet, "/clusters/x", nil)
			return err
		}},
	}
}

func hitServer(status int) (*httptest.Server, *int32) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte("nope"))
	}))
	return srv, &hits
}

func TestRetryPolicyMatrix(t *testing.T) {
	for _, status := range []int{429, 502, 503, 504, 500, 400, 403, 404, 501} {
		for _, c := range retryCases() {
			t.Run(fmt.Sprintf("%d/%s", status, c.name), func(t *testing.T) {
				srv, hits := hitServer(status)
				defer srv.Close()
				cfg := fastCfg(srv.URL) // Retries: 3
				err := c.call(newEnv(cfg))
				if err == nil {
					t.Fatal("expected an error")
				}
				want := int32(1)
				switch status {
				case 429, 502, 503, 504:
					want = 4
				case 500:
					if !c.mutating {
						want = 4
					}
				}
				if got := atomic.LoadInt32(hits); got != want {
					t.Errorf("HTTP %d on %s: %d requests, want %d (err %v)", status, c.name, got, want, err)
				}
			})
		}
	}
}

func TestRetryTransportFailures(t *testing.T) {
	reset := func() (*httptest.Server, *int32) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		}))
		return srv, &hits
	}
	for _, c := range retryCases() {
		t.Run("reset/"+c.name, func(t *testing.T) {
			srv, hits := reset()
			defer srv.Close()
			if err := c.call(newEnv(fastCfg(srv.URL))); err == nil {
				t.Fatal("expected an error")
			}
			want := int32(4)
			if c.mutating {
				want = 1 // the request may have been processed before the connection died
			}
			if got := atomic.LoadInt32(hits); got != want {
				t.Errorf("%d requests, want %d", got, want)
			}
		})
	}
	t.Run("refused retried for every operation", func(t *testing.T) {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		url := "http://" + l.Addr().String()
		l.Close()
		for _, c := range retryCases() {
			err := c.call(newEnv(fastCfg(url)))
			if err == nil || !strings.Contains(err.Error(), "after 4 attempts") {
				t.Errorf("%s: err = %v", c.name, err)
			}
		}
	})
	t.Run("client timeout is retried for reads only", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			time.Sleep(150 * time.Millisecond)
		}))
		defer srv.Close()
		for _, c := range []callCase{retryCases()[0], retryCases()[11]} {
			atomic.StoreInt32(&hits, 0)
			cfg := fastCfg(srv.URL)
			cfg.Client = &http.Client{Timeout: 30 * time.Millisecond}
			_ = c.call(newEnv(cfg))
			want := int32(4)
			if c.mutating {
				want = 1
			}
			if got := atomic.LoadInt32(&hits); got != want {
				t.Errorf("%s after a client timeout: %d requests, want %d", c.name, got, want)
			}
		}
	})
}

func TestRetryBoundsAndBackoffTiming(t *testing.T) {
	t.Run("Retries negative means none", func(t *testing.T) {
		srv, hits := hitServer(503)
		defer srv.Close()
		cfg := fastCfg(srv.URL)
		cfg.Retries = -1
		if err := retryCases()[11].call(newEnv(cfg)); err == nil || atomic.LoadInt32(hits) != 1 {
			t.Fatalf("hits = %d err = %v", atomic.LoadInt32(hits), err)
		}
		cfg.Retries = -100
		atomic.StoreInt32(hits, 0)
		_ = retryCases()[11].call(newEnv(cfg))
		if atomic.LoadInt32(hits) != 1 {
			t.Fatalf("hits = %d", atomic.LoadInt32(hits))
		}
	})
	t.Run("Retries zero means the default of five", func(t *testing.T) {
		srv, hits := hitServer(503)
		defer srv.Close()
		cfg := fastCfg(srv.URL)
		cfg.Retries = 0
		_ = retryCases()[11].call(newEnv(cfg))
		if atomic.LoadInt32(hits) != 6 {
			t.Fatalf("hits = %d, want 6", atomic.LoadInt32(hits))
		}
	})
	t.Run("delays double", func(t *testing.T) {
		var mu sync.Mutex
		var at []time.Time
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			at = append(at, time.Now())
			mu.Unlock()
			w.WriteHeader(503)
		}))
		defer srv.Close()
		cfg := fastCfg(srv.URL)
		cfg.Retries, cfg.Backoff = 3, 40*time.Millisecond
		start := time.Now()
		_ = retryCases()[11].call(newEnv(cfg))
		total := time.Since(start)
		mu.Lock()
		defer mu.Unlock()
		if len(at) != 4 {
			t.Fatalf("%d requests", len(at))
		}
		for i, min := range []time.Duration{40, 80, 160} {
			if gap := at[i+1].Sub(at[i]); gap < min*time.Millisecond-5*time.Millisecond {
				t.Errorf("gap %d = %s, want at least %dms", i, gap, min)
			}
		}
		if total > 3*time.Second {
			t.Errorf("total %s for a 280 ms backoff schedule", total)
		}
	})
	t.Run("an already cancelled context sends nothing", func(t *testing.T) {
		srv, hits := hitServer(200)
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := newEnv(fastCfg(srv.URL)).Query(ctx, "ec2", ec2Version, "DescribeInstances")
		if err == nil || atomic.LoadInt32(hits) != 0 {
			t.Fatalf("err = %v hits = %d", err, atomic.LoadInt32(hits))
		}
	})
	t.Run("cancellation during backoff stops promptly", func(t *testing.T) {
		srv, hits := hitServer(503)
		defer srv.Close()
		cfg := fastCfg(srv.URL)
		cfg.Retries, cfg.Backoff = 100, time.Minute
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := newEnv(cfg).Query(ctx, "ec2", ec2Version, "DescribeInstances")
		if err == nil || time.Since(start) > 2*time.Second || atomic.LoadInt32(hits) > 2 {
			t.Fatalf("err = %v after %s with %d hits", err, time.Since(start), atomic.LoadInt32(hits))
		}
	})
	t.Run("a success after a failure stops retrying", func(t *testing.T) {
		srv, hits := retryServer(1, 429)
		defer srv.Close()
		if _, err := newEnv(fastCfg(srv.URL)).Query(context.Background(), "ec2", ec2Version, "CreateFleet"); err != nil || atomic.LoadInt32(hits) != 2 {
			t.Fatalf("err = %v hits = %d", err, atomic.LoadInt32(hits))
		}
	})
	t.Run("every attempt carries a fresh signature date", func(t *testing.T) {
		var mu sync.Mutex
		var dates []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			dates = append(dates, r.Header.Get("X-Amz-Date"))
			mu.Unlock()
			w.WriteHeader(503)
		}))
		defer srv.Close()
		cfg := fastCfg(srv.URL)
		n := 0
		base := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
		cfg.Now = func() time.Time { n++; return base.Add(time.Duration(n) * time.Second) }
		_ = retryCases()[11].call(newEnv(cfg))
		seen := map[string]bool{}
		for _, d := range dates {
			seen[d] = true
		}
		if len(seen) != len(dates) || len(dates) != 4 {
			t.Errorf("dates %v: attempts should be re-signed", dates)
		}
	})
}

// ---------------------------------------------------------------------------------------------------------
// 4. bounded reads

func TestBoundedReads(t *testing.T) {
	t.Run("exactly 16 MiB is accepted", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pre, post := "<R><!--", "--></R>"
			_, _ = w.Write([]byte(pre))
			_, _ = w.Write(bytes.Repeat([]byte("a"), maxBody-len(pre)-len(post)))
			_, _ = w.Write([]byte(post))
		}))
		defer srv.Close()
		if _, err := newEnv(fastCfg(srv.URL)).Query(context.Background(), "ec2", ec2Version, "DescribeInstances"); err != nil {
			t.Fatalf("a body of exactly %d bytes should be read: %v", maxBody, err)
		}
	})
	t.Run("one byte more is refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pre, post := "<R><!--", "--></R>"
			_, _ = w.Write([]byte(pre))
			_, _ = w.Write(bytes.Repeat([]byte("a"), maxBody+1-len(pre)-len(post)))
			_, _ = w.Write([]byte(post))
		}))
		defer srv.Close()
		_, err := newEnv(fastCfg(srv.URL)).Query(context.Background(), "ec2", ec2Version, "DescribeInstances")
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an endless body is cut off quickly without being buffered", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			chunk := bytes.Repeat([]byte("z"), 1<<20)
			for i := 0; i < 4096; i++ { // 4 GiB if anyone kept reading
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		}))
		defer srv.Close()
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		_, err := newEnv(fastCfg(srv.URL)).Query(context.Background(), "ec2", ec2Version, "DescribeInstances")
		runtime.ReadMemStats(&after)
		if err == nil || time.Since(start) > 10*time.Second {
			t.Fatalf("err = %v after %s", err, time.Since(start))
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 400<<20 {
			t.Errorf("allocated %d MiB reading an endless body", grew>>20)
		}
	})
	t.Run("a gzip bomb is bounded by the decompressed size", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			zero := make([]byte, 1<<20)
			for i := 0; i < 256; i++ {
				if _, err := zw.Write(zero); err != nil {
					return
				}
			}
			zw.Close()
		}))
		defer srv.Close()
		start := time.Now()
		_, err := newEnv(fastCfg(srv.URL)).Query(context.Background(), "ec2", ec2Version, "DescribeInstances")
		if err == nil || time.Since(start) > 10*time.Second {
			t.Fatalf("err = %v after %s", err, time.Since(start))
		}
	})
	t.Run("a trickling server is stopped by the context", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			for i := 0; i < 600; i++ {
				if _, err := w.Write([]byte(" ")); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond)
			}
		}))
		defer srv.Close()
		cfg := fastCfg(srv.URL)
		cfg.Client = &http.Client{} // no client timeout: only the context can stop this
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := newEnv(cfg).Query(ctx, "ec2", ec2Version, "DescribeInstances")
		if err == nil || time.Since(start) > 3*time.Second {
			t.Fatalf("err = %v after %s", err, time.Since(start))
		}
	})
	t.Run("a server that never answers is stopped by the default client timeout configuration", func(t *testing.T) {
		// Cfg.Client == nil builds a client with a 30 s timeout; here only check the redirect policy and that the
		// default client is used at all by pointing at a closed port.
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		url := "http://" + l.Addr().String()
		l.Close()
		cfg := fastCfg(url)
		cfg.Client = nil
		cfg.Retries = -1
		if _, err := newEnv(cfg).Query(context.Background(), "ec2", ec2Version, "DescribeInstances"); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("redirects are not followed and the signed request is not resent", func(t *testing.T) {
		var other int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&other, 1)
			_, _ = w.Write([]byte("<R><Account>1</Account></R>"))
		}))
		defer target.Close()
		for _, code := range []int{301, 302, 303, 307, 308} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/x", code)
			}))
			cfg := fastCfg(srv.URL)
			cfg.Client = nil
			cfg.Retries = -1
			_, err := newEnv(cfg).Query(context.Background(), "ec2", ec2Version, "CreateFleet")
			srv.Close()
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(code)) {
				t.Errorf("redirect %d: err = %v", code, err)
			}
		}
		if n := atomic.LoadInt32(&other); n != 0 {
			t.Fatalf("the redirect target received %d requests", n)
		}
	})
	t.Run("malformed, empty and odd bodies give errors, not panics", func(t *testing.T) {
		bodies := map[string]string{
			"empty": "", "whitespace": "  \n ", "text": "hello", "truncated": "<R><a>1</a", "mismatched": "<a></b>", "two roots": "<a/><b/>x",
			"only a comment": "<!-- x -->", "only a declaration": `<?xml version="1.0"?>`, "html": "<html><body>502 Bad Gateway</body></html>",
			"bad entity": "<a>&bogus;</a>", "nul byte": "<a>\x00</a>", "unclosed cdata": "<a><![CDATA[x</a>",
		}
		for name, body := range bodies {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			n, err := newEnv(fastCfg(srv.URL)).Query(context.Background(), "ec2", ec2Version, "DescribeInstances")
			srv.Close()
			if name == "two roots" || name == "html" {
				continue // parseable; the checks then fail on missing fields
			}
			if err == nil {
				t.Errorf("%s: parsed as %+v", name, n)
			}
		}
		jsons := map[string]string{"empty": "", "array": "[]", "string": `"x"`, "number": "1", "truncated": `{"a":`, "html": "<html>", "trailing garbage": `{"a":1} x`}
		for name, body := range jsons {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			_, err := newEnv(fastCfg(srv.URL)).JSONCall(context.Background(), "ssm", "AmazonSSM.GetParameter", "1.1", map[string]any{})
			srv.Close()
			if err == nil {
				t.Errorf("json %s: no error", name)
			}
		}
	})
	t.Run("a JSON null body does not crash the checks", func(t *testing.T) {
		f, _ := newFake(t)
		s := func(svc, op string, r *http.Request, st *int, h http.Header, b *string) {
			if strings.Contains(h.Get("Content-Type"), "json") {
				*b = "null"
			}
		}
		rs, _ := runShaped(t, f, s, func(c *Config) { c.Wait = 50 * time.Millisecond })
		for _, r := range rs {
			if strings.Contains(r.Detail, "panic") {
				t.Errorf("%s: %s", r.Name, r.Detail)
			}
		}
	})
	t.Run("deeply nested JSON is refused, not recursed", func(t *testing.T) {
		deep := strings.Repeat("[", 3<<20)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(deep)) }))
		defer srv.Close()
		start := time.Now()
		_, err := newEnv(fastCfg(srv.URL)).JSONCall(context.Background(), "ssm", "AmazonSSM.GetParameter", "1.1", map[string]any{})
		if err == nil || time.Since(start) > 5*time.Second {
			t.Fatalf("err = %v after %s", err, time.Since(start))
		}
	})
}

func TestParseXMLHostileDocuments(t *testing.T) {
	t.Run("billion laughs", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><!DOCTYPE lolz [<!ENTITY lol "lol">`)
		prev := "lol"
		for i := 1; i <= 9; i++ {
			name := fmt.Sprintf("lol%d", i)
			b.WriteString(fmt.Sprintf(`<!ENTITY %s "%s">`, name, strings.Repeat("&"+prev+";", 10)))
			prev = name
		}
		b.WriteString(`]><lolz>&lol9;</lolz>`)
		start := time.Now()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		n, err := ParseXML([]byte(b.String()))
		runtime.ReadMemStats(&after)
		if time.Since(start) > 2*time.Second || after.TotalAlloc-before.TotalAlloc > 64<<20 {
			t.Fatalf("took %s and allocated %d MiB", time.Since(start), (after.TotalAlloc-before.TotalAlloc)>>20)
		}
		if err == nil && len(n.Chars) > 1<<20 {
			t.Fatalf("entities were expanded: %d bytes", len(n.Chars))
		}
	})
	t.Run("external entity is not fetched", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
		defer srv.Close()
		_, _ = ParseXML([]byte(`<!DOCTYPE a [<!ENTITY x SYSTEM "` + srv.URL + `/etc/passwd">]><a>&x;</a>`))
		if atomic.LoadInt32(&hits) != 0 {
			t.Fatal("an external entity was fetched")
		}
	})
	t.Run("nesting is bounded", func(t *testing.T) {
		deep := strings.Repeat("<a>", 1<<20)
		start := time.Now()
		if _, err := ParseXML([]byte(deep)); err == nil || time.Since(start) > 3*time.Second {
			t.Fatalf("err = %v after %s", err, time.Since(start))
		}
		ok := strings.Repeat("<a>", 200) + strings.Repeat("</a>", 200)
		if _, err := ParseXML([]byte(ok)); err != nil {
			t.Fatalf("200 levels should parse: %v", err)
		}
	})
	t.Run("16 MiB of siblings stays within a sane memory bound", func(t *testing.T) {
		body := "<r>" + strings.Repeat("<a/>", (maxBody-7)/4) + "</r>"
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		_, err := ParseXML([]byte(body))
		runtime.ReadMemStats(&after)
		t.Logf("parsed %d MiB of <a/> in %s, allocated %d MiB (err %v)", len(body)>>20, time.Since(start), (after.TotalAlloc-before.TotalAlloc)>>20, err)
		if after.TotalAlloc-before.TotalAlloc > 3<<30 {
			t.Errorf("allocated %d MiB for a 16 MiB document", (after.TotalAlloc-before.TotalAlloc)>>20)
		}
	})
	t.Run("dialect", func(t *testing.T) {
		good := map[string]string{
			"namespace prefix":   `<ns:a xmlns:ns="u"><ns:b>x</ns:b></ns:a>`,
			"attributes":         `<a z="1" a="2"><b k="v">x</b></a>`,
			"comments and PI":    `<?xml version="1.0"?><?pi x?><!-- c --><a><!-- d --><b>x</b></a>`,
			"doctype":            `<!DOCTYPE a><a><b>x</b></a>`,
			"utf8 declaration":   `<?xml version="1.0" encoding="UTF-8"?><a><b>x</b></a>`,
			"utf-8 lower":        `<?xml version="1.0" encoding="utf-8"?><a><b>x</b></a>`,
			"crlf":               "<a>\r\n<b>x</b>\r\n</a>",
			"cdata":              `<a><b><![CDATA[x]]></b></a>`,
			"char refs":          `<a><b>&#120;</b></a>`,
			"leading whitespace": "\n\n <a><b>x</b></a>",
		}
		for name, doc := range good {
			n, err := ParseXML([]byte(doc))
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			if n.Text("b") != "x" {
				t.Errorf("%s: b = %q", name, n.Text("b"))
			}
		}
	})
}

// A UTF-8 byte order mark, or an XML declaration with another charset, is legal XML that some servers emit.
func TestParseXMLToleratesBOM(t *testing.T) {
	if _, err := ParseXML([]byte("\xef\xbb\xbf<a><b>x</b></a>")); err != nil {
		t.Errorf("UTF-8 BOM: %v", err)
	}
}

func TestBugParseXMLRejectsLegalDeclaredCharsets(t *testing.T) {
	// BUG (low): the decoder has no CharsetReader, so a declaration of ISO-8859-1 or US-ASCII, both legal and
	// both ASCII-compatible, makes the whole answer unreadable.
	if _, err := ParseXML([]byte(`<?xml version="1.0" encoding="ISO-8859-1"?><a><b>x</b></a>`)); err != nil {
		t.Errorf("declared ISO-8859-1: %v", err)
	}
	if _, err := ParseXML([]byte(`<?xml version="1.0" encoding="US-ASCII"?><a><b>x</b></a>`)); err != nil {
		t.Errorf("declared US-ASCII: %v", err)
	}
}

// BUG candidate: character data of one element is appended token by token (n.Chars += ...), and comments split
// the text into many tokens. That is quadratic in the number of tokens, so a body under the 16 MiB cap can pin a CPU.
func TestBugParseXMLCharDataConcatenationIsQuadratic(t *testing.T) {
	measure := func(tokens int) (time.Duration, uint64) {
		body := []byte("<r>" + strings.Repeat("a<!---->", tokens) + "</r>")
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		_, _ = ParseXML(body)
		d := time.Since(start)
		runtime.ReadMemStats(&after)
		return d, after.TotalAlloc - before.TotalAlloc
	}
	d1, a1 := measure(50_000)
	d2, a2 := measure(200_000)
	ratio := float64(a2) / float64(a1)
	t.Logf("50k tokens (0.4 MB): %s, %d MiB allocated; 200k tokens (1.6 MB): %s, %d MiB allocated; allocation ratio %.1f (linear would be 4, quadratic 16)", d1, a1>>20, d2, a2>>20, ratio)
	// BUG: n.Chars += string(t) copies the text collected so far for every token, so work grows with the square of
	// the body size: a 16 MiB body of this shape would copy about 100 times more than the 1.6 MB one (minutes of CPU).
	if ratio > 9 {
		t.Fatalf("ParseXML allocation grew %.1fx for 4x the input (quadratic)", ratio)
	}
}

// ---------------------------------------------------------------------------------------------------------
// pricing helpers

func TestBugParsePriceIsNondeterministicWithMoreThanOneDimension(t *testing.T) {
	doc := `{"product":{"attributes":{"instanceType":"m5.large"}},"terms":{"OnDemand":{"T":{"priceDimensions":{` +
		`"A":{"pricePerUnit":{"USD":"0.096"}},"B":{"pricePerUnit":{"EUR":"0.08"}}}}}}}`
	seen := map[float64]bool{}
	for i := 0; i < 300; i++ {
		_, usd, err := parsePrice(doc)
		if err != nil {
			t.Fatal(err)
		}
		seen[usd] = true
	}
	// BUG: parsePrice overwrites usd with every dimension it visits (map order is random), so a document with a
	// USD price and a dimension without one is accepted or rejected by chance.
	if len(seen) != 1 {
		t.Fatalf("parsePrice gave %d different answers for one document: %v", len(seen), seen)
	}
}

func TestBugOnDemandPricesAcceptsNaNAndInfinity(t *testing.T) {
	for _, bad := range []string{"NaN", "Inf", "+Inf", "1e999"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := json.Marshal(map[string]any{"PriceList": []string{priceDoc("m5.large", bad)}})
			_, _ = w.Write(b)
		}))
		got, err := OnDemandPrices(context.Background(), *fastCfg(srv.URL), []string{"m5.large"})
		srv.Close()
		// BUG: the test is `usd <= 0`, which NaN satisfies neither way and +Inf passes; the price then cannot be
		// encoded as JSON by cmd/s0probe.
		if err == nil {
			t.Errorf("price %q was accepted as %v", bad, got["m5.large"])
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// 5. secrets

const (
	secAccess = "AKIAZ7Q3EXAMPLEKEYID"
	secSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY=="
)

func secretLeaks(s string) []string {
	var found []string
	for _, v := range []string{secAccess, secSecret, url.QueryEscape(secSecret), url.PathEscape(secSecret), url.QueryEscape(secAccess)} {
		if strings.Contains(s, v) {
			found = append(found, v)
		}
	}
	return found
}

func echoServer(pad int, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.WriteHeader(status)
		fmt.Fprintf(w, "%s%s %s auth=%s body=%s url=%s", strings.Repeat("x", pad), secSecret, secAccess, r.Header.Get("Authorization"), b, r.URL)
	}))
}

func secretCfg(url string) Config {
	return Config{Endpoint: url, Region: "us-west-2", Access: secAccess, Secret: secSecret, Cluster: "meridian-g0", AMIParam: "/aws/service/x/image_id",
		QueueName: "q", EKSCreate: true, Wait: 50 * time.Millisecond, Retries: 1, Backoff: time.Millisecond, PollInterval: time.Millisecond}
}

func TestSecretsNeverReachResultsOrReports(t *testing.T) {
	for _, status := range []int{200, 400, 403, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := echoServer(0, status)
			defer srv.Close()
			rs := Run(context.Background(), secretCfg(srv.URL), Checks())
			md := Markdown("title", rs)
			js, err := JSON(rs)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rs {
				if l := secretLeaks(r.Detail + r.Name); len(l) > 0 {
					t.Errorf("%s detail leaks %v: %q", r.Name, l, r.Detail)
				}
			}
			if l := secretLeaks(md); len(l) > 0 {
				t.Errorf("markdown leaks %v", l)
			}
			if l := secretLeaks(string(js)); len(l) > 0 {
				t.Errorf("json leaks %v", l)
			}
		})
	}
}

func TestSecretEchoedAsAnAMIParameterValueIsRedacted(t *testing.T) {
	f, srv := newFake(t)
	f.ami = "ami-" + secSecret
	cfg := f.cfg(srv.URL)
	cfg.Access, cfg.Secret = secAccess, secSecret
	rs := Run(context.Background(), cfg, Checks())
	md := Markdown("t", rs)
	js, _ := JSON(rs)
	if l := secretLeaks(md + string(js)); len(l) > 0 {
		t.Errorf("leaks %v", l)
	}
}

func TestBugSecretCutByTheDetailTruncationIsNotRedacted(t *testing.T) {
	srv := echoServer(228, 400) // the 240-byte cut falls inside the secret
	defer srv.Close()
	rs := Run(context.Background(), secretCfg(srv.URL), []Check{Checks()[0]})
	d := rs[0].Detail
	// BUG: failure() truncates the backend body to 240 bytes and Run redacts afterwards, so a secret straddling the
	// cut is not recognised and its first characters are written to the report.
	for n := 8; n <= len(secSecret); n++ {
		for i := 0; i+n <= len(secSecret); i++ {
			if strings.Contains(d, secSecret[i:i+n]) {
				t.Fatalf("detail contains %d characters of the secret: %q", n, d)
			}
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// 6. report writers

var tableRow = regexp.MustCompile(`^\| `)

func checkTableRows(md string) []string {
	var rows []string
	in := false
	for _, l := range strings.Split(md, "\n") {
		switch {
		case strings.HasPrefix(l, "| Check | Group | Status | Detail |"):
			in = true
		case in && strings.HasPrefix(l, "|---"):
		case in && tableRow.MatchString(l):
			rows = append(rows, l)
		case in:
			return rows
		}
	}
	return rows
}

func TestMarkdownTableSurvivesAwkwardDetails(t *testing.T) {
	details := []string{"a|b", "a\nb", "a\r\nb", "|", "||||", "\n\n", "line1\nline2|x\n| fake | row | pass | row |", "`code`|`", "<script>x</script>", "é x", strings.Repeat("|", 1000), ""}
	var rs []Result
	for i, d := range details {
		rs = append(rs, Result{Name: "ec2:DescribeSubnets (discovery tag)", Group: "ec2", Status: []string{"pass", "fail", "skip"}[i%3], Detail: d})
	}
	md := Markdown("t | with pipe", rs)
	rows := checkTableRows(md)
	if len(rows) != len(details) {
		t.Fatalf("%d table rows for %d results", len(rows), len(details))
	}
	for i, r := range rows {
		if n := strings.Count(r, "|"); n != 5 {
			t.Errorf("row %d has %d pipes: %q", i, n, r)
		}
	}
}

// BUG candidate: a lone carriage return ends a line in CommonMark and GitHub, so it splits a table row just like a newline.
func TestBugMarkdownDetailWithCarriageReturnSplitsTheRow(t *testing.T) {
	md := Markdown("t", []Result{{Name: "ec2:CreateFleet", Group: "ec2", Status: "fail", Detail: "HTTP 500: first\rsecond line"}})
	if strings.Contains(md, "\r") {
		// BUG: Markdown() flattens "|" and "\n" but not "\r".
		t.Fatalf("a carriage return survived into the table row: %q", checkTableRows(md))
	}
}

func TestJSONReportRoundTripsAndIsSelfDescribing(t *testing.T) {
	rs := []Result{
		{Name: fleetCheck, Group: "ec2", Status: "fail", Detail: "bad \"quote\" \\   <&> \x01 \xff"},
		{Name: "ec2:DescribeInstanceStatus", Group: "ec2", Status: "skip", Detail: "x"},
		{Name: "pricing:GetProducts", Group: "pricing", Status: "pass"},
		{Name: "unknown:Thing", Group: "other", Status: "weird"},
	}
	b, err := JSON(rs)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Summary        Summary
		FailedGroups   []string            `json:"failed_groups"`
		Results        []Result            `json:"results"`
		RequiredFields map[string][]string `json:"required_fields"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 4 || out.Summary.Pass != 1 || out.Summary.Fail != 1 || out.Summary.Skip != 2 {
		t.Errorf("round trip: %+v", out)
	}
	if strings.Join(out.FailedGroups, ",") != "ec2,other" {
		t.Errorf("failed_groups = %v", out.FailedGroups)
	}
	if len(out.RequiredFields[fleetCheck]) == 0 || out.RequiredFields["unknown:Thing"] != nil {
		t.Errorf("required_fields = %v", out.RequiredFields)
	}
	if out2, _ := JSON(nil); !strings.Contains(string(out2), `"failed_groups": []`) {
		t.Errorf("empty report: %s", out2)
	}
}

// ---------------------------------------------------------------------------------------------------------
// Run() itself

func TestRunCancelledMidwayNeverCountsInterruptedChecksAsPass(t *testing.T) {
	f, srv := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	f.rewrite["ssm:PutParameter"] = func(s string) string { once.Do(cancel); return s }
	rs := Run(ctx, f.cfg(srv.URL), Checks())
	if len(rs) != len(Checks()) {
		t.Fatalf("%d results for %d checks", len(rs), len(Checks()))
	}
	sawInterrupted := false
	for _, r := range rs {
		if strings.HasPrefix(r.Detail, "interrupted") {
			sawInterrupted = true
			if r.Status != "skip" {
				t.Errorf("%s: %s", r.Name, r.Status)
			}
		}
	}
	if !sawInterrupted {
		t.Error("no check was reported as interrupted")
	}
	if Summarize(rs).Pass == len(rs) {
		t.Error("an interrupted run reads as all-pass")
	}
}

func TestRunUsesAFreshSuffixPerRunAndNeverWritesUnderAWS(t *testing.T) {
	f, srv := newFake(t)
	probe(t, f, srv.URL, nil)
	probe(t, f, srv.URL, nil)
	seen := map[string]bool{}
	for _, p := range f.ssmPuts {
		if seen[p] {
			t.Errorf("parameter name %s reused across runs", p)
		}
		seen[p] = true
		if !strings.HasPrefix(p, "/s0probe/") {
			t.Errorf("unexpected parameter %s", p)
		}
	}
}

func TestEveryCheckRunsAgainstAHostileBackendWithoutPanicking(t *testing.T) {
	bodies := []string{"", "null", "{}", "[]", "<a/>", "<R><R/></R>", `{"Parameter":5,"cluster":[],"Messages":"x","PriceList":{}}`, strings.Repeat("<a>", 100) + "x"}
	for _, body := range bodies {
		for _, status := range []int{200, 201, 204, 400} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
			}))
			cfg := secretCfg(srv.URL)
			rs := Run(context.Background(), cfg, Checks())
			srv.Close()
			if len(rs) != len(Checks()) {
				t.Fatalf("%d results", len(rs))
			}
			for _, r := range rs {
				if strings.Contains(r.Detail, "panic in check") {
					t.Errorf("status %d body %q: %s: %s", status, body, r.Name, r.Detail)
				}
			}
		}
	}
}
