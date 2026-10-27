package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// The shape of `claude -p "/usage"` as Claude Code 2.1.309 prints it, around its plan windows.
const usageReport = `You are currently using your subscription to power your Claude Code usage

Current session: 100% used · resets Oct 27 at 1:39pm (Europe/Lisbon)
Current week (all models): 20% used · resets Oct 28 at 6pm (Europe/Lisbon)

What's contributing to your limits usage?
Last 24h · 720 requests · 16 sessions
  96%  Start fresh when you switch tasks · run /clear
`

// quotaStandIn is a CLI that answers `-p /usage` with the script's output, and any other call with nothing.
func quotaStandIn(t *testing.T, script string) ports.HarnessInfo {
	t.Helper()
	path := filepath.Join(tempDir(t), "claude")
	body := "#!/bin/sh\n[ \"$1 $2\" = \"-p /usage\" ] || exit 2\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return ports.HarnessInfo{ID: ID, Detected: true, Path: path, Readiness: "ready"}
}

// The usage report gives one window per plan window it prints, its reset in the CLI's own words; a report that names
// none, or a command that fails, is a quota that cannot be read.
func TestReadQuota(t *testing.T) {
	a := &Adapter{Home: tempDir(t)}
	q, err := a.ReadQuota(t.Context(), quotaStandIn(t, "cat <<'EOF'\n"+usageReport+"EOF"))
	if err != nil {
		t.Fatalf("ReadQuota = %v", err)
	}
	want := []domain.QuotaWindow{
		{Name: "Current session", UsedPercent: 100, Resets: "Oct 27 at 1:39pm (Europe/Lisbon)"},
		{Name: "Current week (all models)", UsedPercent: 20, Resets: "Oct 28 at 6pm (Europe/Lisbon)"},
	}
	if len(q.Windows) != len(want) || q.Windows[0] != want[0] || q.Windows[1] != want[1] {
		t.Errorf("ReadQuota windows = %+v, want %+v", q.Windows, want)
	}
	if w, exhausted := q.Exhausted(); !exhausted || w.Name != "Current session" {
		t.Errorf("Exhausted() = %+v, %v, want the session window", w, exhausted)
	}

	for _, c := range []struct{ name, script, key string }{
		{"a report of another shape", "echo 'Usage: nothing to show'", "quota_report_unrecognized"},
		{"a command that fails", "echo 'Error: not logged in' >&2; exit 1", "command_failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := a.ReadQuota(t.Context(), quotaStandIn(t, c.script))
			if got := domain.TextOf(err); err == nil || got.Key != c.key {
				t.Errorf("ReadQuota = %v (%s), want %s", err, got.Key, c.key)
			}
		})
	}
}

// A recorded stream of a session whose plan window ran out: the CLI rejects the call on the window, yet goes on with
// extra usage and finishes. Its rate-limit report says so, and is the harness's quota reading.
func TestParseExtraUsage(t *testing.T) {
	p, events := replay(t, "extra-usage.jsonl")
	var quotas []*domain.Quota
	for _, e := range events {
		if e.Kind == ports.HarnessQuota {
			quotas = append(quotas, e.Quota)
		}
	}
	if len(quotas) != 1 || quotas[0] == nil {
		t.Fatalf("quota events %+v, want one", quotas)
	}
	q := quotas[0]
	resets := time.Unix(1792719600, 0)
	if !q.UsingExtraUsage || len(q.Windows) != 1 || q.Windows[0].Name != "five_hour" || q.Windows[0].UsedPercent != 100 ||
		!q.Windows[0].ResetsAt.Equal(resets) || q.Limit == nil || q.Limit.Name != "five_hour" {
		t.Errorf("quota %+v, want extra usage on the five_hour window at 100%%, rejected, resetting at %v", q, resets)
	}
	// Left to run, the session ends well: only the daemon, told of the extra usage, ends it.
	if end := p.Finish(0, "", false, false); end.Class != domain.ClassNone || !strings.Contains(end.FinalText, "answer") {
		t.Errorf("end %+v, want success", end)
	}

	// A report within the plan says so too, with no extra usage and nothing exhausted.
	_, events = replay(t, "success.jsonl")
	for _, e := range events {
		if e.Kind == ports.HarnessQuota {
			if _, exhausted := e.Quota.Exhausted(); exhausted || e.Quota.UsingExtraUsage {
				t.Errorf("quota %+v within the plan reads exhausted or on extra usage", e.Quota)
			}
		}
	}
}
