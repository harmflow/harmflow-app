package claudecode

import "github.com/harmflow/harmflow-app/daemon/internal/domain"

// Prices are Anthropic's API list prices for the Claude models, in USD per million tokens at the standard tier, as read
// on AsOf from https://platform.claude.com/docs/en/about-claude/pricing. CacheWriteShort is the 5-minute write,
// CacheWriteLong the 1-hour one. Update them with TestedUpTo, in the same release step.
//
// The cost of a Claude Code attempt is the one the CLI reports; these give its split by token class, which differs
// from it when a request was billed apart from the standard tier (fast mode, US-only inference). Haiku 5.5 is left
// out: it is priced by each request's prompt length, which an attempt's totals cannot tell, so its attempts keep the
// CLI's cost without a split.
var Prices = domain.PriceTable{AsOf: "2026-10-22", Prices: map[string]domain.Price{
	"claude-fable-5-1":  {Input: 10, CacheWriteShort: 12.50, CacheWriteLong: 20, CacheRead: 0.25, Output: 50},
	"claude-mythos-5-1": {Input: 10, CacheWriteShort: 12.50, CacheWriteLong: 20, CacheRead: 0.25, Output: 50},
	"claude-fable-5":    {Input: 10, CacheWriteShort: 12.50, CacheWriteLong: 20, CacheRead: 1, Output: 50},
	"claude-mythos-5":   {Input: 10, CacheWriteShort: 12.50, CacheWriteLong: 20, CacheRead: 1, Output: 50},
	"claude-opus-5-5":   {Input: 4, CacheWriteShort: 5, CacheWriteLong: 8, CacheRead: 0.20, Output: 20},
	"claude-opus-5":     {Input: 5, CacheWriteShort: 6.25, CacheWriteLong: 10, CacheRead: 0.50, Output: 25},
	"claude-opus-4-8":   {Input: 5, CacheWriteShort: 6.25, CacheWriteLong: 10, CacheRead: 0.50, Output: 25},
	"claude-opus-4-7":   {Input: 5, CacheWriteShort: 6.25, CacheWriteLong: 10, CacheRead: 0.50, Output: 25},
	"claude-opus-4-6":   {Input: 5, CacheWriteShort: 6.25, CacheWriteLong: 10, CacheRead: 0.50, Output: 25},
	"claude-opus-4-5":   {Input: 5, CacheWriteShort: 6.25, CacheWriteLong: 10, CacheRead: 0.50, Output: 25},
	"claude-sonnet-5-5": {Input: 2, CacheWriteShort: 2.50, CacheWriteLong: 4, CacheRead: 0.10, Output: 10},
	"claude-sonnet-5":   {Input: 2, CacheWriteShort: 2.50, CacheWriteLong: 4, CacheRead: 0.20, Output: 10},
	"claude-sonnet-4-6": {Input: 3, CacheWriteShort: 3.75, CacheWriteLong: 6, CacheRead: 0.30, Output: 15},
	"claude-sonnet-4-5": {Input: 3, CacheWriteShort: 3.75, CacheWriteLong: 6, CacheRead: 0.30, Output: 15},
	"claude-haiku-4-5":  {Input: 1, CacheWriteShort: 1.25, CacheWriteLong: 2, CacheRead: 0.10, Output: 5},
}}
