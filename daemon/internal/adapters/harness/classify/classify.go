// Package classify tells apart the ways a harness attempt can end, from text the CLI actually printed. It errs
// toward ClassTaskError: a failover is triggered only by reliable evidence (FS-005 FR-004).
package classify

import (
	"regexp"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// Patterns are one harness's expressions for the error texts of each class: quota exhaustion, a rate limit, an
// unavailable harness, and a network failure. A nil expression matches nothing.
type Patterns struct {
	Quota       *regexp.Regexp
	RateLimit   *regexp.Regexp
	Unavailable *regexp.Regexp
	Network     *regexp.Regexp
}

// Text classifies an error text; quota is checked before rate limits because quota messages mention limits too.
func (p Patterns) Text(s string) domain.ErrorClass {
	switch {
	case s == "":
		return domain.ClassTaskError
	case p.Quota != nil && p.Quota.MatchString(s):
		return domain.ClassQuotaExhausted
	case p.RateLimit != nil && p.RateLimit.MatchString(s):
		return domain.ClassRateLimited
	case p.Unavailable != nil && p.Unavailable.MatchString(s):
		return domain.ClassHarnessUnavailable
	case p.Network != nil && p.Network.MatchString(s):
		return domain.ClassNetwork
	}
	return domain.ClassTaskError
}

// Truncate shortens a text for an event, keeping its start.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return s[:cut] + "…"
}

// Tail keeps the end of a text.
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && (s[start]&0xC0) == 0x80 {
		start++
	}
	return "…" + s[start:]
}
