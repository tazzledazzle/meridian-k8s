package ec2sim_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/awscall"
	"github.com/tazzledazzle/meridian/internal/catalog"
	"github.com/tazzledazzle/meridian/internal/ec2sim"
	"github.com/tazzledazzle/meridian/internal/gateway"
	"github.com/tazzledazzle/meridian/internal/ops"
	"github.com/tazzledazzle/meridian/internal/s0probe"
)

const (
	access = "AKIAFAKEFAKE"
	secret = "fake/secret+key"
)

func probeCfg(endpoint string) s0probe.Config {
	return s0probe.Config{
		Endpoint: endpoint, Region: catalog.Region, Access: access, Secret: secret, Cluster: "meridian-g0",
		Groups: []string{"ec2"}, Wait: time.Second, Retries: 1, Backoff: time.Millisecond, PollInterval: time.Millisecond,
	}
}

func result(t *testing.T, rs []s0probe.Result, name string) s0probe.Result {
	t.Helper()
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("probe produced no %s result", name)
	return s0probe.Result{}
}

// The real S0 probe check, pointed straight at the handler, must pass.
func TestS0ProbeDescribeInstanceTypesPassesAgainstTheHandler(t *testing.T) {
	srv := httptest.NewServer(ec2sim.New(catalog.Types))
	t.Cleanup(srv.Close)
	// The handler has no SigV4 of its own (it trusts the gateway), so the signed probe request is accepted as is.
	r := result(t, s0probe.Run(t.Context(), probeCfg(srv.URL), s0probe.Checks()), "ec2:DescribeInstanceTypes")
	if r.Status != "pass" {
		t.Fatalf("probe = %s: %s", r.Status, r.Detail)
	}
	if want := "12 types listed"; r.Detail != want {
		t.Errorf("detail = %q, want %q", r.Detail, want)
	}
}

// hits records which backend served which action.
type hits struct {
	mu    sync.Mutex
	byBak map[string][]string
}

func (h *hits) wrap(name string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		form, _ := parseAction(string(b))
		h.mu.Lock()
		h.byBak[name] = append(h.byBak[name], form)
		h.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func parseAction(body string) (string, error) {
	for _, kv := range strings.Split(body, "&") {
		if v, ok := strings.CutPrefix(kv, "Action="); ok {
			return v, nil
		}
	}
	return "", nil
}

// opsWithEC2Sim copies the real ops.yaml with only the DescribeInstanceTypes line switched to ec2-sim.
func opsWithEC2Sim(t *testing.T) ops.List {
	t.Helper()
	raw, err := os.ReadFile("../../ops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	old := "name: DescribeInstanceTypes, protocol: query, backend: floci"
	if strings.Count(string(raw), old) != 1 {
		t.Fatalf("ops.yaml no longer has exactly one %q line", old)
	}
	p := filepath.Join(t.TempDir(), "ops.yaml")
	if err := os.WriteFile(p, []byte(strings.Replace(string(raw), old, "name: DescribeInstanceTypes, protocol: query, backend: ec2-sim", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := ops.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := list.Validate(); err != nil {
		t.Fatal(err)
	}
	return list
}

func gatewayWithEC2Sim(t *testing.T) (url string, h *hits) {
	t.Helper()
	h = &hits{byBak: map[string][]string{}}
	floci := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, "<floci/>")
	})
	gw := gateway.New(gateway.Config{
		Ops:    opsWithEC2Sim(t),
		Lookup: func(k string) (string, bool) { return secret, k == access },
		Backends: map[string]http.Handler{
			"ec2-sim": h.wrap("ec2-sim", ec2sim.New(catalog.Types)),
			"floci":   h.wrap("floci", floci),
		},
		Logf: t.Logf,
	})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	return srv.URL, h
}

func TestGatewayRoutesOnlyDescribeInstanceTypesToEC2Sim(t *testing.T) {
	url, h := gatewayWithEC2Sim(t)
	send := func(action string, params ...string) (int, string) {
		req, _, err := awscall.BuildQuery(url, "ec2", catalog.Region, action, "2016-11-15", params, access, secret, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, body := send("DescribeInstanceTypes", "InstanceType.1=m5.large")
	if code != 200 || !strings.Contains(body, "<networkInfo><maximumNetworkInterfaces>3</maximumNetworkInterfaces>") {
		t.Fatalf("DescribeInstanceTypes via gateway = %d %s", code, body)
	}
	for _, a := range []string{"DescribeInstances", "CreateFleet", "DescribeInstanceTypeOfferings", "DescribeSpotPriceHistory"} {
		if code, body := send(a); code != 200 || body != "<floci/>" {
			t.Errorf("%s = %d %s, want the floci stub", a, code, body)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if got := strings.Join(h.byBak["ec2-sim"], ","); got != "DescribeInstanceTypes" {
		t.Errorf("ec2-sim saw %q", got)
	}
	if got := strings.Join(h.byBak["floci"], ","); got != "DescribeInstances,CreateFleet,DescribeInstanceTypeOfferings,DescribeSpotPriceHistory" {
		t.Errorf("floci saw %q", got)
	}
}

// The probe, through the real gateway and the switched ops.yaml, passes its DescribeInstanceTypes check.
func TestS0ProbeThroughGatewayPassesDescribeInstanceTypes(t *testing.T) {
	url, _ := gatewayWithEC2Sim(t)
	r := result(t, s0probe.Run(t.Context(), probeCfg(url), s0probe.Checks()), "ec2:DescribeInstanceTypes")
	if r.Status != "pass" {
		t.Fatalf("probe = %s: %s", r.Status, r.Detail)
	}
}

// Pod density as the registrar computes it equals what Karpenter derives from the wire fields.
func TestPodDensityFromWireMatchesRegistrar(t *testing.T) {
	srv := httptest.NewServer(ec2sim.New(catalog.Types))
	t.Cleanup(srv.Close)
	for _, c := range catalog.Types {
		req, _, err := awscall.BuildQuery(srv.URL, "ec2", catalog.Region, "DescribeInstanceTypes", "2016-11-15", []string{"InstanceType.1=" + c.Name}, access, secret, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		n, err := s0probe.ParseXML(b)
		if err != nil {
			t.Fatal(err)
		}
		var eni, ip int
		for _, f := range []struct {
			path string
			dst  *int
		}{{"instanceTypeSet/item/networkInfo/maximumNetworkInterfaces", &eni}, {"instanceTypeSet/item/networkInfo/ipv4AddressesPerInterface", &ip}} {
			s := n.Text(f.path)
			for _, ch := range s {
				*f.dst = *f.dst*10 + int(ch-'0')
			}
		}
		if got, want := eni*(ip-1)+2, c.MaxENIs*(c.IPv4PerENI-1)+2; got != want {
			t.Errorf("%s: wire pod density %d, registrar %d", c.Name, got, want)
		}
	}
}
