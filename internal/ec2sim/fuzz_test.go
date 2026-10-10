package ec2sim

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tazzledazzle/meridian/internal/catalog"
)

// FuzzDescribeInstanceTypes feeds arbitrary form bodies to the handler: it must never panic, must answer with
// well-formed XML, and a 200 must contain only catalog types, each at most once, in catalog order.
func FuzzDescribeInstanceTypes(f *testing.F) {
	for _, s := range []string{
		"Action=DescribeInstanceTypes",
		"Action=DescribeInstanceTypes&InstanceType.1=m5.large&InstanceType.1=c5.large",
		"Action=DescribeInstanceTypes&Filter.1.Name=instance-type&Filter.1.Value.1=*",
		"Action=DescribeInstanceTypes&Filter.99999999999.Name=hypervisor&Filter.99999999999.Value.1=nitro",
		"Action=DescribeInstanceTypes&MaxResults=5&NextToken=ZWMyc2ltOnYxOm01Lmxhcmdl",
		"Action=DescribeInstanceTypes&MaxResults=-1",
		"Action=DescribeInstanceTypes&Filter.1.Name=instance-type&Filter.1.Value.1=" + strings.Repeat("*a", 200),
		"Action=RunInstances", "Action=", "%zz", "InstanceType..=x&Action=DescribeInstanceTypes", "",
	} {
		f.Add(s)
	}
	h := New(catalog.Types)
	order := map[string]int{}
	for i, c := range catalog.Types {
		order[c.Name] = i
	}
	f.Fuzz(func(t *testing.T, body string) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 && rec.Code != 400 {
			t.Fatalf("status %d for %q", rec.Code, body)
		}
		var any struct {
			XMLName xml.Name
		}
		if err := xml.Unmarshal(rec.Body.Bytes(), &any); err != nil {
			t.Fatalf("not well-formed XML for %q: %v", body, err)
		}
		if rec.Code != 200 {
			return
		}
		var r respXML
		if err := xml.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		last := -1
		for _, it := range r.Items {
			i, ok := order[it.Type]
			if !ok || i <= last {
				t.Fatalf("type %q out of catalog or out of order/duplicated for %q", it.Type, body)
			}
			last = i
		}
	})
}
