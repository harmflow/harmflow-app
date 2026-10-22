package domain

import "testing"

func f(v float64) *float64 { return &v }

// harmflow-app#39: only a final count is priced; a model the table cannot price leaves the attempt unpriced rather than
// understated, and a cost the harness reported is kept as it is.
func TestApplyPrices(t *testing.T) {
	table := PriceTable{AsOf: "2026-01-02", Prices: map[string]Price{
		"m-1": {Input: 1, CacheWriteShort: 1.25, CacheWriteLong: 2, CacheRead: 0.1, Output: 5}}}
	tokens := Tokens{Input: 1_000_000, CacheWriteShort: 1_000_000, CacheWriteLong: 1_000_000, CacheRead: 1_000_000, Output: 1_000_000}

	u := Usage{Status: UsageReported, Models: []ModelUsage{{Model: "m-1-20260101", Tokens: tokens}}}
	u.ApplyPrices(table)
	if u.CostSource != CostEstimated || u.CostUSD == nil || *u.CostUSD != 9.35 || u.PricesAsOf != "2026-01-02" ||
		u.Models[0].Price == nil {
		t.Errorf("estimated: %+v", u)
	}

	partial := Usage{Status: UsagePartial, Models: []ModelUsage{{Model: "m-1", Tokens: tokens}}}
	partial.ApplyPrices(table)
	if partial.CostSource != "" || partial.CostUSD != nil || partial.Models[0].Price != nil {
		t.Errorf("a partial count was priced: %+v", partial)
	}

	mixed := Usage{Status: UsageReported, Models: []ModelUsage{{Model: "m-1", Tokens: tokens}, {Model: "m-2", Tokens: tokens}}}
	mixed.ApplyPrices(table)
	if mixed.CostSource != CostUnpriced || mixed.CostUSD != nil || mixed.Models[0].CostUSD == nil {
		t.Errorf("one model without a price: %+v", mixed)
	}

	unnamed := Usage{Status: UsageReported, Models: []ModelUsage{{Tokens: tokens}}}
	unnamed.ApplyPrices(table)
	if unnamed.CostSource != CostUnpriced || unnamed.PricesAsOf != "" {
		t.Errorf("no model: %+v", unnamed)
	}

	reported := Usage{Status: UsageReported, CostSource: CostReported, CostUSD: f(0.42),
		Models: []ModelUsage{{Model: "m-2", Tokens: tokens, CostUSD: f(0.42)}}}
	reported.ApplyPrices(table)
	if reported.CostSource != CostReported || *reported.CostUSD != 0.42 || reported.Models[0].Price != nil {
		t.Errorf("reported: %+v", reported)
	}
}
