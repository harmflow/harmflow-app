package claudecode

import (
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// Each quota ending of quota-endings.md that no other test replays, as stream-json prints it in its release line. The
// adapter classifies every ending the study records: TestParseQuota replays the rejected plan window of 2.1.45 on
// (quota.jsonl), and TestParseQuotaTextWithNoRateLimitEvent the 2.0.0 spending cap and the 5-hour limit's text alone.
func TestQuotaEndings(t *testing.T) {
	for _, file := range []string{
		"quota-2.0-session.jsonl",         // 2.0.0: no rate_limit_event, the result text alone
		"quota-2.0-legacy.jsonl",          // 2.0.0's fallback when no rate-limit report was read
		"quota-2.0.77.jsonl",              // 2.0.77 and 2.1.x before 2.1.45: "You've hit your … limit"
		"quota-2.0.77-extra-usage.jsonl",  // 2.0.77: "You're out of extra usage", with no rate_limit_event
		"quota-five-hour.jsonl",           // 2.1.312 on: "5-hour limit", read from the rejected window
		"quota-monthly-spend-limit.jsonl", // 2.1.329: "You've hit your monthly spend limit.", the text alone
		"quota-usage-credits.jsonl",       // 2.1.329: "You're out of usage credits", the text alone
	} {
		t.Run(file, func(t *testing.T) {
			p, _ := replay(t, file)
			if end := p.Finish(1, "", false, false); end.Class != domain.ClassQuotaExhausted {
				t.Errorf("Finish(1, \"\", false, false).Class = %s (%s), want quota_exhausted", end.Class, end.Error.Message)
			}
		})
	}
}
