package pricingfacade_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tazzledazzle/meridian/internal/catalog"
	"github.com/tazzledazzle/meridian/internal/gateway"
	"github.com/tazzledazzle/meridian/internal/ops"
	"github.com/tazzledazzle/meridian/internal/pricingfacade"
	"github.com/tazzledazzle/meridian/internal/s0probe"
)

func cfgFor(url string) s0probe.Config {
	return s0probe.Config{
		Endpoint: url, Region: "us-west-2", Access: "AKIAFAKEFAKE", Secret: "fake/secret+key",
		Wait: 2 * time.Second, Retries: 1, Backoff: time.Millisecond, PollInterval: time.Millisecond,
	}
}

func catalogNames() []string {
	var n []string
	for _, t := range catalog.Types {
		n = append(n, t.Name)
	}
	return n
}

// The real S0 probe check and OnDemandPrices pass against the handler directly. The probe signs requests, but the
// handler (which trusts the gateway) ignores signatures, exactly as in the compose stack.
func TestS0ProbePricingCheckAndOnDemandPricesPassAgainstTheHandler(t *testing.T) {
	srv := httptest.NewServer(pricingfacade.New())
	defer srv.Close()
	cfg := cfgFor(srv.URL)

	var checks []s0probe.Check
	for _, c := range s0probe.Checks() {
		if c.Name == "pricing:GetProducts" {
			checks = append(checks, c)
		}
	}
	if len(checks) != 1 {
		t.Fatalf("found %d pricing checks", len(checks))
	}
	rs := s0probe.Run(context.Background(), cfg, checks)
	if len(rs) != 1 || rs[0].Status != "pass" {
		t.Fatalf("probe result: %+v", rs)
	}
	if !strings.Contains(rs[0].Detail, fmt.Sprintf("%d priced types", len(catalog.Types))) {
		t.Errorf("detail %q does not report every catalog type", rs[0].Detail)
	}

	prices, err := s0probe.OnDemandPrices(context.Background(), cfg, catalogNames())
	if err != nil {
		t.Fatal(err)
	}
	for _, ty := range catalog.Types {
		if prices[ty.Name] != ty.OnDemand {
			t.Errorf("%s = %v, want %v", ty.Name, prices[ty.Name], ty.OnDemand)
		}
	}
	// A type outside the catalog has no price, as the gate expects.
	if _, err := s0probe.OnDemandPrices(context.Background(), cfg, []string{"p9.nonexistent"}); err == nil {
		t.Error("an unknown type must not have a price")
	}
	// The region is a filter: us-east-1 has no data here (the catalog serves one region).
	east := cfg
	east.Region = "us-east-1"
	if _, err := s0probe.OnDemandPrices(context.Background(), east, []string{"m5.large"}); err == nil {
		t.Error("us-east-1 must have no price")
	}
}

// karpenterPrice is a copy of Karpenter's per-entry parsing (pkg/providers/pricing): instance type from
// product.attributes.instanceType, price from the first terms.OnDemand.*.priceDimensions.*.pricePerUnit.USD.
func karpenterPrice(entry string) (string, float64, error) {
	var item struct {
		Product struct {
			Attributes map[string]string `json:"attributes"`
		} `json:"product"`
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct {
					PricePerUnit struct {
						USD string `json:"USD"`
					} `json:"pricePerUnit"`
				} `json:"priceDimensions"`
			} `json:"OnDemand"`
		} `json:"terms"`
	}
	if err := json.Unmarshal([]byte(entry), &item); err != nil {
		return "", 0, err
	}
	for _, term := range item.Terms.OnDemand {
		for _, dim := range term.PriceDimensions {
			p, err := strconv.ParseFloat(dim.PricePerUnit.USD, 64)
			if err != nil {
				return "", 0, err
			}
			return item.Product.Attributes["instanceType"], p, nil
		}
	}
	return "", 0, fmt.Errorf("no on-demand price")
}

func call(t *testing.T, url string, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("X-Amz-Target", "AWSPriceListService.GetProducts")
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	return resp.StatusCode, m
}

func TestKarpenterStyleParsingGetsAFinitePositivePriceForEveryCatalogTypeOnce(t *testing.T) {
	srv := httptest.NewServer(pricingfacade.New())
	defer srv.Close()
	filters := []map[string]string{}
	for _, kv := range [][2]string{{"regionCode", "us-west-2"}, {"tenancy", "Shared"}, {"preInstalledSw", "NA"}, {"operatingSystem", "Linux"}, {"capacitystatus", "Used"}} {
		filters = append(filters, map[string]string{"Type": "TERM_MATCH", "Field": kv[0], "Value": kv[1]})
	}
	for _, pageSize := range []int{1, 3, 100} {
		prices := map[string]float64{}
		token := ""
		for pages := 0; pages < 50; pages++ {
			body := map[string]any{"ServiceCode": "AmazonEC2", "Filters": filters, "MaxResults": pageSize}
			if token != "" {
				body["NextToken"] = token
			}
			code, m := call(t, srv.URL, body)
			if code != 200 {
				t.Fatalf("status %d: %v", code, m)
			}
			for _, e := range m["PriceList"].([]any) {
				typ, p, err := karpenterPrice(e.(string))
				if err != nil {
					t.Fatal(err)
				}
				if _, dup := prices[typ]; dup {
					t.Errorf("pageSize=%d: %s listed twice", pageSize, typ)
				}
				if !(p > 0) || math.IsInf(p, 0) || math.IsNaN(p) {
					t.Errorf("%s has price %v", typ, p)
				}
				prices[typ] = p
			}
			token, _ = m["NextToken"].(string)
			if token == "" {
				break
			}
		}
		if token != "" {
			t.Fatalf("pageSize=%d: never finished", pageSize)
		}
		for _, ty := range catalog.Types {
			if prices[ty.Name] != ty.OnDemand {
				t.Errorf("pageSize=%d: %s = %v, want %v", pageSize, ty.Name, prices[ty.Name], ty.OnDemand)
			}
		}
		if len(prices) != len(catalog.Types) {
			t.Errorf("pageSize=%d: %d types priced, want %d", pageSize, len(prices), len(catalog.Types))
		}
	}
}

// The real gateway with the real ops.yaml, with the single pricing:GetProducts line switched to pricing-facade,
// routes a signed request to the handler; the probe passes through it end to end.
func TestRealGatewayRoutesPricingToTheFacade(t *testing.T) {
	raw, err := os.ReadFile("../../ops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// The real ops.yaml may route GetProducts to floci (before the S0 table is applied) or to pricing-facade
	// (after). Force pricing-facade in the temp copy so the test holds in both states.
	re := regexp.MustCompile(`(\{service: pricing, name: GetProducts, protocol: json, backend: )[a-z][a-z0-9-]*`)
	if len(re.FindAll(raw, -1)) != 1 {
		t.Fatalf("expected exactly one pricing:GetProducts line in ops.yaml")
	}
	switched := string(re.ReplaceAll(raw, []byte("${1}pricing-facade")))
	path := t.TempDir() + "/ops.yaml"
	if err := os.WriteFile(path, []byte(switched), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := ops.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := list.Validate(); err != nil {
		t.Fatal(err)
	}

	var flociHits, facadeHits atomic.Int32
	floci := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { flociHits.Add(1); http.Error(w, "floci", 500) })
	facade := pricingfacade.New()
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { facadeHits.Add(1); facade.ServeHTTP(w, r) })
	cfg := cfgFor("")
	gw := gateway.New(gateway.Config{
		Ops:      list,
		Lookup:   func(k string) (string, bool) { return cfg.Secret, k == cfg.Access },
		Backends: map[string]http.Handler{"floci": floci, "pricing-facade": counted},
		Logf:     func(string, ...any) {},
	})
	srv := httptest.NewServer(gw)
	defer srv.Close()
	cfg.Endpoint = srv.URL

	prices, err := s0probe.OnDemandPrices(context.Background(), cfg, catalogNames())
	if err != nil {
		t.Fatalf("through the gateway: %v", err)
	}
	if len(prices) != len(catalog.Types) {
		t.Fatalf("priced %d types", len(prices))
	}
	if facadeHits.Load() == 0 || flociHits.Load() != 0 {
		t.Fatalf("facade hits=%d floci hits=%d", facadeHits.Load(), flociHits.Load())
	}

	// Unsigned or wrongly signed requests never reach the facade.
	before2 := facadeHits.Load()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"ServiceCode":"AmazonEC2"}`))
	req.Header.Set("X-Amz-Target", "AWSPriceListService.GetProducts")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode < 400 || facadeHits.Load() != before2 {
		t.Fatalf("unsigned request: status %d, facade hits %d -> %d", resp.StatusCode, before2, facadeHits.Load())
	}
}

func TestRefusalsAreLoggedWithoutBodies(t *testing.T) {
	var logs []string
	h := pricingfacade.New()
	h.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ServiceCode":"SECRET-MARKER","MaxResults":0}`))
	req.Header.Set("X-Amz-Target", "AWSPriceListService.GetProducts")
	h.ServeHTTP(rec, req)
	if rec.Code != 400 || len(logs) != 1 || !strings.Contains(logs[0], "InvalidParameterException") || strings.Contains(logs[0], "SECRET-MARKER") {
		t.Fatalf("code=%d logs=%q", rec.Code, logs)
	}
}
