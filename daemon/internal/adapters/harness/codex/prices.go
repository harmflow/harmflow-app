package codex

import "github.com/harmflow/harmflow-app/daemon/internal/domain"

// Prices are OpenAI's API list prices for the models Codex runs, in USD per million tokens at the standard tier for
// prompts up to 272K tokens, as read on AsOf from https://developers.openai.com/api/docs/pricing and the models' pages.
// CacheWriteShort is the 30-minute write, CacheWriteLong the 12-hour one; a model released before GPT-5.6 bills no
// cache write, so its writes are priced as input. Update them with TestedUpTo, in the same release step.
//
// A request over 272K tokens costs more, which the attempt's totals cannot tell, so an estimate is a lower bound
// there. gpt-reserve and codex-auto-review have no published price and stay unpriced.
var Prices = domain.PriceTable{AsOf: "2026-10-22", Prices: map[string]domain.Price{
	"gpt-6.1-sol":   {Input: 2, CacheWriteShort: 2.50, CacheWriteLong: 5, CacheRead: 0.10, Output: 10},
	"gpt-6-sol":     {Input: 2, CacheWriteShort: 2.50, CacheWriteLong: 5, CacheRead: 0.20, Output: 10},
	"gpt-6-luna":    {Input: 0.10, CacheWriteShort: 0.125, CacheWriteLong: 0.25, CacheRead: 0.01, Output: 0.50},
	"gpt-5.6-sol":   {Input: 4, CacheWriteShort: 5, CacheWriteLong: 10, CacheRead: 0.40, Output: 20}, // promotional, at least to 2026-11-21
	"gpt-5.6-terra": {Input: 2, CacheWriteShort: 2.50, CacheWriteLong: 5, CacheRead: 0.20, Output: 12},
	"gpt-5.6-luna":  {Input: 0.20, CacheWriteShort: 0.25, CacheWriteLong: 0.50, CacheRead: 0.02, Output: 1.20},

	"gpt-5.3-codex":      {Input: 1.75, CacheWriteShort: 1.75, CacheWriteLong: 1.75, CacheRead: 0.175, Output: 14},
	"gpt-5.2-codex":      {Input: 1.75, CacheWriteShort: 1.75, CacheWriteLong: 1.75, CacheRead: 0.175, Output: 14},
	"gpt-5.1-codex-max":  {Input: 1.25, CacheWriteShort: 1.25, CacheWriteLong: 1.25, CacheRead: 0.125, Output: 10},
	"gpt-5.1-codex":      {Input: 1.25, CacheWriteShort: 1.25, CacheWriteLong: 1.25, CacheRead: 0.125, Output: 10},
	"gpt-5.1-codex-mini": {Input: 0.25, CacheWriteShort: 0.25, CacheWriteLong: 0.25, CacheRead: 0.025, Output: 2},
	"gpt-5-codex":        {Input: 1.25, CacheWriteShort: 1.25, CacheWriteLong: 1.25, CacheRead: 0.125, Output: 10},
	"codex-mini-latest":  {Input: 1.50, CacheWriteShort: 1.50, CacheWriteLong: 1.50, CacheRead: 0.375, Output: 6},
}}
