package domain

import (
	"regexp"
	"sort"
)

// UsageStatus is how complete an attempt's token counts are (harmflow-app#39).
type UsageStatus string

const (
	// UsageReported is the harness's own final count.
	UsageReported UsageStatus = "reported"
	// UsagePartial is the count so far, without the harness's final line: output tokens are unknown, and there is no
	// cost. A running attempt is partial, and so is one that ended before its harness reported.
	UsagePartial UsageStatus = "partial"
	// UsageNotReported is an attempt whose harness reported no usage; it is never shown as zero.
	UsageNotReported UsageStatus = "not_reported"
)

// CostSource says where an attempt's cost comes from.
type CostSource string

const (
	CostReported  CostSource = "reported"  // the harness printed it
	CostEstimated CostSource = "estimated" // the tokens at the adapter's price table
	CostUnpriced  CostSource = "unpriced"  // the model is unknown, or the table has no price for it
)

// Where the model that prices an attempt was named.
const (
	ModelFromHarness = "harness" // the harness's own output
	ModelFromTask    = "task"    // the task's harness profile
	ModelFromConfig  = "config"  // the harness's configuration, for a task left at the default model
)

// Tokens counts an attempt's tokens by class. Input excludes the tokens written to and read from the cache. Cache
// writes are split by lifetime, which prices them: CacheWriteShort at the provider's default (Anthropic 5 minutes,
// OpenAI 30 minutes), CacheWriteLong at its extended one (Anthropic 1 hour, OpenAI 12 hours).
type Tokens struct {
	Input           int64 `json:"input"`
	CacheWriteShort int64 `json:"cacheWriteShort"`
	CacheWriteLong  int64 `json:"cacheWriteLong"`
	CacheRead       int64 `json:"cacheRead"`
	Output          int64 `json:"output"`
}

// Add returns the sum of t and o, class by class.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{Input: t.Input + o.Input, CacheWriteShort: t.CacheWriteShort + o.CacheWriteShort,
		CacheWriteLong: t.CacheWriteLong + o.CacheWriteLong, CacheRead: t.CacheRead + o.CacheRead, Output: t.Output + o.Output}
}

// Price is a model's API list price by token class, in USD per million tokens.
type Price struct {
	Input           float64 `json:"input"`
	CacheWriteShort float64 `json:"cacheWriteShort"`
	CacheWriteLong  float64 `json:"cacheWriteLong"`
	CacheRead       float64 `json:"cacheRead"`
	Output          float64 `json:"output"`
}

// Cost is what the tokens cost at this price, in USD.
func (p Price) Cost(t Tokens) float64 {
	return (float64(t.Input)*p.Input + float64(t.CacheWriteShort)*p.CacheWriteShort + float64(t.CacheWriteLong)*p.CacheWriteLong +
		float64(t.CacheRead)*p.CacheRead + float64(t.Output)*p.Output) / 1e6
}

// PriceTable is an adapter's dated table of API list prices by model id. It is updated with the adapter's tested
// version, and an attempt keeps the prices it ended with, so a later table never reprices history.
type PriceTable struct {
	AsOf   string // the date the prices were read, YYYY-MM-DD
	Prices map[string]Price
}

var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// Lookup finds a model's price by its id, or by the id without a release date (claude-haiku-4-5-20251001).
func (t PriceTable) Lookup(model string) (Price, bool) {
	if p, ok := t.Prices[model]; ok {
		return p, true
	}
	p, ok := t.Prices[dateSuffix.ReplaceAllString(model, "")]
	return p, ok
}

// ModelUsage is one model's share of an attempt. Model is empty when nothing names it.
type ModelUsage struct {
	Model   string   `json:"model,omitempty"`
	Tokens  Tokens   `json:"tokens"`
	Price   *Price   `json:"price,omitempty"`
	CostUSD *float64 `json:"costUsd,omitempty"`
}

// Usage is an attempt's token use as its harness reported it, and its cost, stamped when the attempt ended. The daemon
// records numbers and enums only; the desktop words them.
type Usage struct {
	Status      UsageStatus  `json:"status"`
	ModelSource string       `json:"modelSource,omitempty"`
	Models      []ModelUsage `json:"models"`
	CostSource  CostSource   `json:"costSource,omitempty"`
	CostUSD     *float64     `json:"costUsd,omitempty"`
	PricesAsOf  string       `json:"pricesAsOf,omitempty"`
}

// NotReported is the usage of an attempt whose harness reported none.
func NotReported() *Usage {
	return &Usage{Status: UsageNotReported, Models: []ModelUsage{}}
}

// Total is the attempt's tokens across its models.
func (u *Usage) Total() Tokens {
	var t Tokens
	for _, m := range u.Models {
		t = t.Add(m.Tokens)
	}
	return t
}

// ApplyPrices stamps the table's price on each model it knows and settles the attempt's cost: the one the harness
// reported, else the sum of every model's estimate. Only a reported usage is priced — partial counts lack the output
// tokens — and a usage with a model the table cannot price is unpriced, so its cost is never understated.
func (u *Usage) ApplyPrices(t PriceTable) {
	if u.Status != UsageReported {
		return
	}
	var sum float64
	priced := true
	for i := range u.Models {
		m := &u.Models[i]
		if p, ok := t.Lookup(m.Model); ok && m.Model != "" {
			m.Price = &p
			u.PricesAsOf = t.AsOf
			if m.CostUSD == nil {
				c := p.Cost(m.Tokens)
				m.CostUSD = &c
			}
		}
		if m.CostUSD == nil {
			priced = false
		} else {
			sum += *m.CostUSD
		}
	}
	switch {
	case u.CostSource == CostReported && u.CostUSD != nil:
	case priced && len(u.Models) > 0:
		u.CostSource, u.CostUSD = CostEstimated, &sum
	default:
		u.CostSource, u.CostUSD = CostUnpriced, nil
	}
}

// SortModels orders the models by id, with first, when given, ahead of the rest.
func (u *Usage) SortModels(first string) {
	sort.SliceStable(u.Models, func(i, j int) bool {
		a, b := u.Models[i].Model, u.Models[j].Model
		if (a == first) != (b == first) {
			return a == first
		}
		return a < b
	})
}
