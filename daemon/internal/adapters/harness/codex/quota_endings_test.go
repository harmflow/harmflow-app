package codex

import (
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// Each quota ending of quota-endings.md that no other test replays, as the app server ends a turn on it: an error that
// will not retry, then a failed turn/completed with the same message. TestParseQuota replays quota.jsonl and the
// workspace-credit, spend-cap, and "Quota exceeded" endings. The releases before 0.43.0, whose output the adapter
// cannot read, are below MinVersion and never run (harmflow-app#1122).
func TestQuotaEndings(t *testing.T) {
	for _, file := range []string{"quota-plus.jsonl", "quota-model-limit.jsonl", "quota-no-plan.jsonl"} {
		t.Run(file, func(t *testing.T) {
			p, _ := replay(t, file)
			if end := p.Finish(1, "", false, false); end.Class != domain.ClassQuotaExhausted {
				t.Errorf("Finish(1, \"\", false, false).Class = %s (%s), want quota_exhausted", end.Class, end.Error.Message)
			}
		})
	}
}
