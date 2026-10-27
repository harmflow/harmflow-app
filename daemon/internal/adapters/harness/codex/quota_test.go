package codex

import (
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// Codex's quota is what account/rateLimits/read answers, as Codex 0.163.0 shapes it: its plan windows, of which some
// plans have none, the limit it reports reached, and the account's spend control. An answer without rateLimits is one
// the adapter does not understand.
func TestReadQuota(t *testing.T) {
	cases := []struct {
		name, result string
		want         domain.Quota
		exhausted    string
	}{
		{"windows in use", `{"rateLimits":{"primary":{"usedPercent":42,"windowDurationMins":300,"resetsAt":1792719600},` +
			`"secondary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":null},"planType":"plus"}}`,
			domain.Quota{Windows: []domain.QuotaWindow{{Name: "primary", UsedPercent: 42, ResetsAt: time.Unix(1792719600, 0)},
				{Name: "secondary", UsedPercent: 100}}}, "secondary"},
		{"no window, spend control reached", `{"rateLimits":{"primary":null,"secondary":null,` +
			`"credits":{"hasCredits":true,"unlimited":false,"balance":"10"},"spendControlReached":true,"planType":"business"}}`,
			domain.Quota{SpendControlReached: true}, domain.SpendControl},
		{"a limit reported reached", `{"rateLimits":{"primary":{"usedPercent":12},` +
			`"rateLimitReachedType":"workspace_member_usage_limit_reached"}}`,
			domain.Quota{Windows: []domain.QuotaWindow{{Name: "primary", UsedPercent: 12}},
				Limit: &domain.QuotaWindow{Name: "workspace_member_usage_limit_reached"}}, "workspace_member_usage_limit_reached"},
		{"no window and nothing reached", `{"rateLimits":{"primary":null,"secondary":null,"spendControlReached":null}}`,
			domain.Quota{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, received := appServerStandIn(t, answer(c.result))
			a := appServerAdapter(t)
			q, err := a.ReadQuota(t.Context(), appServerInfo(path))
			if err != nil {
				t.Fatalf("ReadQuota = %v, want no error", err)
			}
			if !sameQuota(q, c.want) {
				t.Errorf("ReadQuota = %+v, want %+v", q, c.want)
			}
			w, exhausted := q.Exhausted()
			if exhausted != (c.exhausted != "") || w.Name != c.exhausted {
				t.Errorf("Exhausted() = %+v, %v, want %q", w, exhausted, c.exhausted)
			}
			if got := requests(t, received)["account/rateLimits/read"]; len(got) != 1 {
				t.Errorf("the app server got %d account/rateLimits/read requests, want 1", len(got))
			}
		})
	}

	path, _ := appServerStandIn(t, answer(`{"data":[]}`))
	_, err := appServerAdapter(t).ReadQuota(t.Context(), appServerInfo(path))
	if got := domain.TextOf(err); err == nil || got.Key != "quota_report_unrecognized" {
		t.Errorf("ReadQuota of an answer without rateLimits = %v (%s), want quota_report_unrecognized", err, got.Key)
	}
}

func sameQuota(a, b domain.Quota) bool {
	if len(a.Windows) != len(b.Windows) || a.SpendControlReached != b.SpendControlReached ||
		a.UsingExtraUsage != b.UsingExtraUsage || (a.Limit == nil) != (b.Limit == nil) {
		return false
	}
	for i := range a.Windows {
		if a.Windows[i].Name != b.Windows[i].Name || a.Windows[i].UsedPercent != b.Windows[i].UsedPercent ||
			!a.Windows[i].ResetsAt.Equal(b.Windows[i].ResetsAt) || a.Windows[i].Resets != b.Windows[i].Resets {
			return false
		}
	}
	return a.Limit == nil || *a.Limit == *b.Limit
}
