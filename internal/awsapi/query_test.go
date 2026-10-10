package awsapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestNestBuildsTreeAndOrdersListsNumerically(t *testing.T) {
	v := url.Values{}
	v.Set("TargetCapacitySpecification.TotalTargetCapacity", "1")
	v.Set("LaunchTemplateConfigs.1.LaunchTemplateSpecification.LaunchTemplateName", "lt-a")
	v.Set("LaunchTemplateConfigs.1.Overrides.10.InstanceType", "c5.large")
	v.Set("LaunchTemplateConfigs.1.Overrides.2.InstanceType", "m5.large")
	n := Nest(v)

	if got := n.Str("TargetCapacitySpecification", "TotalTargetCapacity"); got != "1" {
		t.Fatalf("capacity = %q", got)
	}
	cfgs := n.Get("LaunchTemplateConfigs").List()
	if len(cfgs) != 1 {
		t.Fatalf("configs = %d", len(cfgs))
	}
	var types []string
	for _, o := range cfgs[0].Get("Overrides").List() {
		types = append(types, o.Str("InstanceType"))
	}
	if !reflect.DeepEqual(types, []string{"m5.large", "c5.large"}) { // index 2 before index 10
		t.Fatalf("override order = %v", types)
	}
	if n.Has("Nope") || !n.Has("TargetCapacitySpecification") {
		t.Fatal("Has gave the wrong answer")
	}
	if n.Str("Nope", "Deeper") != "" {
		t.Fatal("missing paths must yield empty strings, not panic")
	}
}

func TestStringsReturnsLeafListValuesInOrder(t *testing.T) {
	v := url.Values{}
	v.Set("InstanceId.1", "i-1")
	v.Set("InstanceId.2", "i-2")
	if got := Nest(v).Get("InstanceId").Strings(); !reflect.DeepEqual(got, []string{"i-1", "i-2"}) {
		t.Fatalf("got %v", got)
	}
}

func TestReadBodyRestoresBodyAndEnforcesLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Action=DescribeInstances"))
	b, err := ReadBody(r, 1024)
	if err != nil || string(b) != "Action=DescribeInstances" {
		t.Fatalf("b=%q err=%v", b, err)
	}
	again := make([]byte, 64)
	n, _ := r.Body.Read(again)
	if string(again[:n]) != "Action=DescribeInstances" {
		t.Fatalf("body not restored: %q", again[:n])
	}
	big := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 100)))
	if _, err := ReadBody(big, 10); err == nil {
		t.Fatal("expected an error for an oversized body")
	}
}

func TestFormMergesBodyAndURLQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/?Version=2016-11-15", nil)
	v, err := Form(r, []byte("Action=DescribeSubnets&SubnetId.1=subnet-1"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Get("Action") != "DescribeSubnets" || v.Get("Version") != "2016-11-15" || v.Get("SubnetId.1") != "subnet-1" {
		t.Fatalf("v=%v", v)
	}
}
