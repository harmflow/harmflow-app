package app

import (
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// selectTwo starts a session's script: it selects o/r#1, then o/r#2, and waits until the daemon has logged both, so
// that what the script does next comes after the two selections.
func selectTwo(logs string) string {
	return `P="$HARMFLOW_EXECUTION_DIR/progress.jsonl"; L="` + logs + `/$HARMFLOW_EXECUTION_ID.jsonl"
printf '%s\n' '{"type":"work_item.selected","workItem":"o/r#1","message":"Select the first"}' \
  '{"type":"work_item.selected","workItem":"o/r#2","message":"Select the second"}' >> "$P"
until grep -qF 'Select the second' "$L"; do sleep 0.05; done
`
}

// withReport ends a session's script: it writes report as report.json and exits as a finished session.
func withReport(report string) string {
	return `printf '%s' '` + report + `' > "$HARMFLOW_EXECUTION_DIR/report.json"` + "\necho 'END ok'"
}

// ledgerItem is the task's work item key as the queue shows it, nil when the ledger holds none.
func ledgerItem(e *env, task, key string) *domain.WorkItem {
	for _, w := range e.app.Queue().WorkItems {
		if w.TaskID == task && w.Key == key {
			return &w
		}
	}
	return nil
}

// runTask runs task once with script and waits until its execution has ended as want and freed the task, so that what
// the end left in the ledger is there.
func runTask(e *env, task string, want domain.ExecutionStatus, script string) ExecutionView {
	e.t.Helper()
	e.claude.setScript(func(ports.RunRequest) string { return script })
	r, err := e.app.RunTask(task)
	if err != nil || r.Outcome != "started" {
		e.t.Fatalf("RunTask(%q) = %+v, %v; want started", task, r, err)
	}
	x := e.wait(r.ExecutionID, want)
	e.idle(task)
	return x
}

// harmflow-app#312: a skipped result changes no status. An item the session selected goes back to the status it had
// before, and one the ledger did not hold gets no entry, whether the session selected it or not, so none of them waits
// in the queue as work to resume.
func TestSkippedItemKeepsItsStatus(t *testing.T) {
	e := newEnv(t)
	e.create("skipper", "")
	runTask(e, "skipper", domain.StatusCompleted,
		withReport(`{"outcome":"completed","workItems":[{"key":"o/r#1","outcome":"completed"}]}`))

	runTask(e, "skipper", domain.StatusCompleted, selectTwo(e.app.d.Paths.Logs)+withReport(`{"outcome":"completed",`+
		`"workItems":[{"key":"o/r#1","outcome":"skipped"},{"key":"o/r#2","outcome":"skipped"},`+
		`{"key":"o/r#3","outcome":"skipped"}]}`))

	if w := ledgerItem(e, "skipper", "o/r#1"); w == nil || w.Status != domain.WorkItemCompleted || w.Failures != 0 {
		t.Errorf("o/r#1, completed, then selected and skipped: %+v; want completed, with no failure", w)
	}
	for _, key := range []string{"o/r#2", "o/r#3"} {
		if w := ledgerItem(e, "skipper", key); w != nil {
			t.Errorf("%s, skipped and never held before: %+v; want no entry", key, w)
		}
	}
}

// harmflow-app#312: an item the session selected and left without a result counts as an operational failure whatever
// ended the execution, every item it selected included, not only the last; an item with a result counts none.
func TestUnreportedItemCountsAFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report string // the session's report.json; none when empty
		want   domain.ExecutionStatus
	}{
		{"completed", `{"outcome":"completed","workItems":[{"key":"o/r#1","outcome":"completed"}]}`, domain.StatusCompleted},
		{"no work", `{"outcome":"no_work","workItems":[{"key":"o/r#1","outcome":"completed"}]}`, domain.StatusNoWork},
		{"needs attention", `{"outcome":"needs_attention","workItems":[{"key":"o/r#1","outcome":"completed"}]}`,
			domain.StatusNeedsAttention},
		{"failed", `{"outcome":"failed","workItems":[{"key":"o/r#1","outcome":"completed"}]}`, domain.StatusFailed},
		{"no report", "", domain.StatusCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.create("dropper", "")
			script := selectTwo(e.app.d.Paths.Logs) + "echo 'END ok'"
			if tc.report != "" {
				script = selectTwo(e.app.d.Paths.Logs) + withReport(tc.report)
			}
			x := runTask(e, "dropper", tc.want, script)

			first, second := ledgerItem(e, "dropper", "o/r#1"), ledgerItem(e, "dropper", "o/r#2")
			if tc.report == "" {
				if first == nil || first.Status != domain.WorkItemFailed || first.Failures != 1 {
					t.Errorf("o/r#1, selected first, with no report: %+v; want failed, with one failure", first)
				}
			} else if first == nil || first.Status != domain.WorkItemCompleted || first.Failures != 0 {
				t.Errorf("o/r#1, reported completed: %+v; want completed, with no failure", first)
			}
			if second == nil || second.Status != domain.WorkItemFailed || second.Failures != 1 ||
				second.LastExecutionID != x.ID {
				t.Errorf("o/r#2, selected and left without a result: %+v; want failed, with one failure, by %s", second,
					x.ID)
			}
		})
	}
}

// harmflow-app#312: a cancellation counts nothing against the items its session selected, which stay in progress.
func TestCancelledSelectionCountsNoFailure(t *testing.T) {
	e := newEnv(t)
	e.create("cancelled", "")
	e.claude.setScript(func(ports.RunRequest) string { return selectTwo(e.app.d.Paths.Logs) + "exec sleep 30" })
	r, err := e.app.RunTask("cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if !e.poll(10*time.Second, func() bool { return ledgerItem(e, "cancelled", "o/r#2") != nil }) {
		t.Fatal("the session never selected o/r#2")
	}
	if err := e.app.CancelExecution(r.ExecutionID); err != nil {
		t.Fatal(err)
	}
	e.wait(r.ExecutionID, domain.StatusCancelled)
	e.idle("cancelled")

	for _, key := range []string{"o/r#1", "o/r#2"} {
		if w := ledgerItem(e, "cancelled", key); w == nil || w.Status != domain.WorkItemInProgress || w.Failures != 0 {
			t.Errorf("%s, selected by a cancelled execution: %+v; want in progress, with no failure", key, w)
		}
	}
}

// harmflow-app#312 and #440: an ignored report loses its work items' results, so the execution needs attention, with
// the reason it was ignored, and every item its session selected counts a failure.
func TestIgnoredReportNeedsAttention(t *testing.T) {
	for _, tc := range []struct {
		name, report, key string
	}{
		{"not following its schema", `{"outcome":"completed","workItems":[{"key":"o/r#1","outcome":"completed",` +
			`"pullRequest":"https://github.com/o/r/pull/9"}]}`, "report_schema_invalid"},
		{"not JSON", `{"outcome":`, "report_not_json"},
		// The schema bounds an issue number only from below, and no int holds this one.
		{"not fitting its fields", `{"outcome":"completed","workItems":[{"key":"o/r#1","outcome":"completed",` +
			`"issueNumber":100000000000000000000}]}`, "report_not_decodable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.create("ignored", "")
			x := runTask(e, "ignored", domain.StatusNeedsAttention, selectTwo(e.app.d.Paths.Logs)+withReport(tc.report))

			if x.ErrorKey != tc.key || x.ErrorMessage == "" {
				t.Errorf("error of the execution: %q %q; want the key %s and its text", x.ErrorKey, x.ErrorMessage, tc.key)
			}
			warned := false
			for _, w := range x.Warnings {
				warned = warned || w.Key == tc.key
			}
			if !warned {
				t.Errorf("warnings %+v; want one with the key %s", x.Warnings, tc.key)
			}
			for _, key := range []string{"o/r#1", "o/r#2"} {
				if w := ledgerItem(e, "ignored", key); w == nil || w.Status != domain.WorkItemFailed || w.Failures != 1 {
					t.Errorf("%s, selected, its result ignored: %+v; want failed, with one failure", key, w)
				}
			}
		})
	}
}

// harmflow-app#312: a ledger saved before the daemon kept skipped and unreported items out of progress is repaired on
// the first start that loads it, and only on that one. An item in progress whose last execution reported it skipped,
// with nothing counted, leaves the ledger; one that execution gave no result counts a failure. An item whose execution
// was cancelled, whose tests failed, that has a failure counted, or whose execution's record is gone stays as it is.
func TestOldLedgerIsRepairedOnce(t *testing.T) {
	e := newEnv(t)
	ended := func(status domain.ExecutionStatus, report ...domain.WorkItemResult) string {
		now := e.clock.Now()
		x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: "old"}}, domain.TriggerManual, nil,
			now)
		x.Status, x.EndedAt, x.WorkItems = status, &now, report
		e.app.persistExecution(x)
		return x.ID
	}
	skipped := ended(domain.StatusCompleted, domain.WorkItemResult{Key: "o/r#1", Outcome: "skipped"},
		domain.WorkItemResult{Key: "o/r#6", Outcome: "skipped"})
	unreported := ended(domain.StatusCompleted)
	cancelled := ended(domain.StatusCancelled)
	e.app.Stop(domain.Text{Message: "upgrade"})

	inProgress := func(key, execution string) *domain.WorkItem {
		return &domain.WorkItem{TaskID: "old", Key: key, Status: domain.WorkItemInProgress, LastExecutionID: execution}
	}
	old := map[string]*domain.WorkItem{
		"o/r#1": inProgress("o/r#1", skipped),
		"o/r#2": inProgress("o/r#2", unreported),
		"o/r#3": inProgress("o/r#3", cancelled),
		"o/r#4": inProgress("o/r#4", "20261020-gone"),
		"o/r#5": inProgress("o/r#5", unreported),
		"o/r#6": inProgress("o/r#6", skipped),
	}
	old["o/r#5"].TestsStatus = "failed"
	old["o/r#6"].Failures = 1
	state, err := fsstore.NewState(e.paths.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save("workitems", ledgerFile{SchemaVersion: "1",
		Tasks: map[string]map[string]*domain.WorkItem{"old": old}}); err != nil {
		t.Fatal(err)
	}

	e = e.restart(t)
	check := func(when string) {
		t.Helper()
		if w := ledgerItem(e, "old", "o/r#1"); w != nil {
			t.Errorf("%s: o/r#1, skipped with nothing counted: %+v; want no entry", when, w)
		}
		if w := ledgerItem(e, "old", "o/r#2"); w == nil || w.Status != domain.WorkItemFailed || w.Failures != 1 {
			t.Errorf("%s: o/r#2, without a result: %+v; want failed, with one failure", when, w)
		}
		for _, key := range []string{"o/r#3", "o/r#4", "o/r#5", "o/r#6"} {
			if w := ledgerItem(e, "old", key); w == nil || w.Status != domain.WorkItemInProgress ||
				w.Failures != old[key].Failures {
				t.Errorf("%s: %s: %+v; want in progress, as it was", when, key, w)
			}
		}
	}
	check("after the upgrade")
	var saved ledgerFile
	if ok, err := state.Load("workitems", &saved); err != nil || !ok || saved.SchemaVersion != ledgerVersion {
		t.Errorf("saved ledger: version %q, %v, %v; want %s", saved.SchemaVersion, ok, err, ledgerVersion)
	}

	// A ledger already at the new version is never repaired: an item it holds in progress, which the repair would count
	// a failure against, stays as it is.
	e.app.Stop(domain.Text{Message: "restart"})
	saved.Tasks["old"]["o/r#7"] = inProgress("o/r#7", unreported)
	if err := state.Save("workitems", saved); err != nil {
		t.Fatal(err)
	}
	e = e.restart(t)
	check("after a second start")
	if w := ledgerItem(e, "old", "o/r#7"); w == nil || w.Status != domain.WorkItemInProgress || w.Failures != 0 {
		t.Errorf("o/r#7, in progress in a ledger at version %s: %+v; want in progress, with no failure", ledgerVersion, w)
	}
}
