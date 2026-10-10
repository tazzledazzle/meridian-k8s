package s0probe

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// priceFilters is the filter set Karpenter sends to Pricing GetProducts for Linux on-demand prices.
func priceFilters(region string, extra ...map[string]string) []map[string]string {
	f := []map[string]string{
		{"Type": "TERM_MATCH", "Field": "regionCode", "Value": region},
		{"Type": "TERM_MATCH", "Field": "tenancy", "Value": "Shared"},
		{"Type": "TERM_MATCH", "Field": "preInstalledSw", "Value": "NA"},
		{"Type": "TERM_MATCH", "Field": "operatingSystem", "Value": "Linux"},
		{"Type": "TERM_MATCH", "Field": "capacitystatus", "Value": "Used"},
		{"Type": "TERM_MATCH", "Field": "marketoption", "Value": "OnDemand"},
	}
	return append(f, extra...)
}

// listProducts pages through GetProducts and returns every PriceList entry (each a JSON document in a string).
func listProducts(ctx context.Context, e *Env, filters []map[string]string) ([]string, error) {
	var all []string
	token := ""
	for page := 0; page < 50; page++ { // bounded: a backend that never stops paging must not hang the probe
		body := map[string]any{"ServiceCode": "AmazonEC2", "FormatVersion": "aws_v1", "Filters": filters, "MaxResults": 100}
		if token != "" {
			body["NextToken"] = token
		}
		m, err := e.JSONCall(ctx, "pricing", "AWSPriceListService.GetProducts", "1.1", body)
		if err != nil {
			return nil, err
		}
		pl, _ := m["PriceList"].([]any)
		for _, p := range pl {
			s, ok := p.(string)
			if !ok {
				return nil, fmt.Errorf("PriceList entries must be JSON strings, got %T", p)
			}
			all = append(all, s)
		}
		token, _ = m["NextToken"].(string)
		if token == "" {
			return all, nil
		}
	}
	return nil, fmt.Errorf("pagination did not finish")
}

// parsePrice reads the instance type and the on-demand USD price from one PriceList document, using the paths
// Karpenter reads: product.attributes.instanceType and terms.OnDemand.*.priceDimensions.*.pricePerUnit.USD.
func parsePrice(raw string) (instanceType string, usd float64, err error) {
	var doc struct {
		Product struct {
			Attributes map[string]string `json:"attributes"`
		} `json:"product"`
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct {
					PricePerUnit map[string]string `json:"pricePerUnit"`
				} `json:"priceDimensions"`
			} `json:"OnDemand"`
		} `json:"terms"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", 0, fmt.Errorf("PriceList entry is not JSON: %w", err)
	}
	for _, t := range doc.Terms.OnDemand {
		for _, d := range t.PriceDimensions {
			usd, _ = strconv.ParseFloat(d.PricePerUnit["USD"], 64)
		}
	}
	return doc.Product.Attributes["instanceType"], usd, nil
}

// OnDemandPrices returns USD per hour for each instance type, asked of the backend exactly as Karpenter asks.
// The gate uses it to work out which type Karpenter should have chosen.
func OnDemandPrices(ctx context.Context, cfg Config, types []string) (map[string]float64, error) {
	e := &Env{Cfg: &cfg, State: map[string]string{}, Lists: map[string][]string{}}
	out := map[string]float64{}
	for _, t := range types {
		docs, err := listProducts(ctx, e, priceFilters(cfg.Region, map[string]string{"Type": "TERM_MATCH", "Field": "instanceType", "Value": t}))
		if err != nil {
			return nil, err
		}
		if len(docs) == 0 {
			return nil, fmt.Errorf("no on-demand price for %s", t)
		}
		typ, usd, err := parsePrice(docs[0])
		if err != nil {
			return nil, err
		}
		if typ != t || usd <= 0 {
			return nil, fmt.Errorf("price document for %s says type %q price %v", t, typ, usd)
		}
		out[t] = usd
	}
	return out, nil
}
