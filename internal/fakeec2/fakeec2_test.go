package fakeec2

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func post(t *testing.T, srv *httptest.Server, form url.Values) string {
	t.Helper()
	resp, err := srv.Client().PostForm(srv.URL, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

func TestStatesFollowTheClockAndTerminateIsVisible(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	f := New(func() time.Time { return now }, 3*time.Second)
	id := f.Launch("m5.large", "us-west-2a", "spot", map[string]string{"k": "v"})
	if s, _ := f.State(id); s != "pending" {
		t.Fatalf("state = %s, want pending", s)
	}
	now = now.Add(5 * time.Second)
	if s, _ := f.State(id); s != "running" {
		t.Fatalf("state = %s, want running", s)
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	out := post(t, srv, url.Values{"Action": {"DescribeInstances"}, "InstanceId.1": {id}})
	for _, want := range []string{"<instanceId>" + id + "</instanceId>", "<name>running</name>", "<instanceLifecycle>spot</instanceLifecycle>", "<key>k</key>"} {
		if !strings.Contains(out, want) {
			t.Errorf("DescribeInstances missing %s in %s", want, out)
		}
	}
	post(t, srv, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {id}})
	if s, _ := f.State(id); s != "shutting-down" {
		t.Fatalf("state = %s, want shutting-down", s)
	}
}

func TestTagKeyFilterAndUnknownIDs(t *testing.T) {
	f := New(time.Now, 0)
	mine := f.Launch("m5.large", "us-west-2a", "", map[string]string{"cluster": "owned"})
	f.Launch("c5.large", "us-west-2a", "", nil)
	srv := httptest.NewServer(f)
	defer srv.Close()
	out := post(t, srv, url.Values{"Action": {"DescribeInstances"}, "Filter.1.Name": {"tag-key"}, "Filter.1.Value.1": {"cluster"}})
	if strings.Count(out, "<instanceId>") != 1 || !strings.Contains(out, mine) {
		t.Fatalf("filter result = %s", out)
	}
	if out := post(t, srv, url.Values{"Action": {"DescribeInstances"}, "InstanceId.1": {"i-nope"}}); strings.Contains(out, "<instanceId>") {
		t.Fatalf("unknown ID returned instances: %s", out)
	}
}

func TestUnknownActionIsRejected(t *testing.T) {
	srv := httptest.NewServer(New(time.Now, 0))
	defer srv.Close()
	if out := post(t, srv, url.Values{"Action": {"RunInstances"}}); !strings.Contains(out, "InvalidAction") {
		t.Fatalf("out = %s", out)
	}
}
