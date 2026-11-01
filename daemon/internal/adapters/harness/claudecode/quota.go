package claudecode

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// quotaTimeout bounds `claude -p "/usage"`, which answered in about 3.6 seconds.
const quotaTimeout = 20 * time.Second

// usageLineRe reads one plan window of the usage report, as "Current session: 4% used · resets Oct 27 at 1:39pm
// (America/Sao_Paulo)" or "Current week (all models): 20% used".
var usageLineRe = regexp.MustCompile(`(?m)^[ \t]*(Current [^:\n]+):[ \t]*(\d+(?:\.\d+)?)%[ \t]+used(?:[ \t]*·[ \t]*resets[ \t]+([^\n]*?))?[ \t]*$`)

// ReadQuota implements ports.QuotaReader with `claude -p "/usage"`, which runs no turn and makes no model call: the
// plan windows it prints, each reset in the CLI's own words.
func (a *Adapter) ReadQuota(ctx context.Context, info ports.HarnessInfo) (domain.Quota, error) {
	out, _, err := procutil.Output(ctx, quotaTimeout, a.withToken(a.Env), info.Path, "-p", "/usage")
	if err != nil {
		return domain.Quota{}, domain.TextErr("command_failed", "{{command}} failed: {{detail}}",
			"command", `claude -p "/usage"`, "detail", err.Error())
	}
	q := parseUsageReport(out)
	if len(q.Windows) == 0 {
		return domain.Quota{}, domain.TextErr("quota_report_unrecognized",
			"{{harness}}'s usage report names no plan window HarmFlow can read", "harness", Name)
	}
	return q, nil
}

// parseUsageReport reads the plan windows of the usage report; a line of another shape is skipped.
func parseUsageReport(out string) domain.Quota {
	var q domain.Quota
	for _, m := range usageLineRe.FindAllStringSubmatch(out, -1) {
		used, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		q.Windows = append(q.Windows, domain.QuotaWindow{Name: m[1], UsedPercent: used, Resets: m[3]})
	}
	return q
}

// rateLimitInfo is the rate_limit_info of a rate_limit_event, which the CLI sends with each API call. A rejected status
// with isUsingOverage set is a plan window used up and the session going on with extra usage.
type rateLimitInfo struct {
	Status         string                   `json:"status"`
	ResetsAt       int64                    `json:"resetsAt"`
	RateLimitType  string                   `json:"rateLimitType"`
	OverageStatus  string                   `json:"overageStatus"`
	IsUsingOverage bool                     `json:"isUsingOverage"`
	UnifiedWindows map[string]unifiedWindow `json:"unifiedWindows"`
}

// unifiedWindow is one plan window of a rate-limit report: its use as a fraction of 1, and when it resets.
type unifiedWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    int64    `json:"resetsAt"`
}

// quota is the reading the report gives: its windows, the limit it rejected the call on, and whether the session is
// spending extra usage.
func (r rateLimitInfo) quota() *domain.Quota {
	q := &domain.Quota{UsingExtraUsage: r.IsUsingOverage}
	for _, name := range slices.Sorted(maps.Keys(r.UnifiedWindows)) {
		w := r.UnifiedWindows[name]
		if w.Utilization == nil {
			continue
		}
		q.Windows = append(q.Windows, domain.QuotaWindow{Name: name, UsedPercent: *w.Utilization * 100,
			ResetsAt: unixTime(w.ResetsAt)})
	}
	// A call rejected on a plan window is a limit reached; one rejected on anything else is a temporary rate limit, which
	// retries on the same harness, as Finish classifies it.
	if r.Status == "rejected" && planWindow(r.RateLimitType) {
		q.Limit = &domain.QuotaWindow{Name: r.RateLimitType, ResetsAt: unixTime(r.ResetsAt)}
	}
	return q
}

// planWindow reports whether a rate limit's type names a plan window, such as five_hour or seven_day.
func planWindow(rateLimitType string) bool {
	return strings.Contains(rateLimitType, "hour") || strings.Contains(rateLimitType, "day") ||
		strings.Contains(rateLimitType, "week")
}

// unixTime is a time in seconds since the epoch; zero when not reported.
func unixTime(s int64) time.Time {
	if s <= 0 {
		return time.Time{}
	}
	return time.Unix(s, 0)
}
