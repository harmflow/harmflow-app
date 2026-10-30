package app

import (
	"encoding/json"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

// ledgerItem is the task's work item key as the ledger holds it, nil when it holds none.
func ledgerItem(e *env, task, key string) *domain.WorkItem {
	return heldItem(e.app, task, key)
}

// heldItem is a copy of the task's work item key as a's ledger holds it, closed or not, nil when it holds none. The
// queue shows only the items still in play.
func heldItem(a *App, task, key string) *domain.WorkItem {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.ledger[task][key]
	if w == nil {
		return nil
	}
	c := *w
	return &c
}

// queued reports whether the queue lists the task's work item key.
func queued(e *env, task, key string) bool {
	for _, w := range e.app.Queue().WorkItems {
		if w.TaskID == task && w.Key == key {
			return true
		}
	}
	return false
}

// savedItems are the work items of the ledger's file name in the state folder, by task and key.
func savedItems(t *testing.T, e *env, name string) map[string]map[string]*domain.WorkItem {
	t.Helper()
	var f ledgerFile
	if _, err := e.app.d.State.Load(name, &f); err != nil {
		t.Fatalf("Load(%q) = %v", name, err)
	}
	return f.Tasks
}

// harmflow-app#341: the queue leaves out an item as soon as it closes, completed, needing attention, or closed, and
// lists the items still in play. The ledger keeps every item, and context.json still lists the closed ones.
func TestQueueLeavesOutClosedItems(t *testing.T) {
	e := newEnv(t)
	e.create("closer", "")
	x := runTask(e, "closer", domain.StatusCompleted, selectTwo(e.app.d.Paths.Logs)+withReport(`{"outcome":"completed",`+
		`"workItems":[{"key":"o/r#1","outcome":"completed"},{"key":"o/r#3","outcome":"needs_attention"},`+
		`{"key":"o/r#4","outcome":"closed"},{"key":"o/r#5","outcome":"completed","tests":{"status":"failed","summary":"1 failed"}},`+
		`{"key":"o/r#6","outcome":"failed"}]}`))

	for key, want := range map[string]bool{"o/r#1": false, "o/r#2": true, "o/r#3": false, "o/r#4": false,
		"o/r#5": true, "o/r#6": true} {
		if got := queued(e, "closer", key); got != want {
			t.Errorf("Queue() lists %s (%+v): %v, want %v", key, ledgerItem(e, "closer", key), got, want)
		}
		if ledgerItem(e, "closer", key) == nil {
			t.Errorf("the ledger holds no %s; want every item kept", key)
		}
	}

	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	r, err := e.app.RunTask("closer")
	if err != nil {
		t.Fatal(err)
	}
	next := e.wait(r.ExecutionID, domain.StatusCompleted, domain.StatusNoWork, domain.StatusNeedsAttention)
	e.idle("closer")
	data, err := os.ReadFile(filepath.Join(next.ExecutionDir, "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		WorkItems []domain.WorkItem `json:"workItems"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.WorkItems) != 6 {
		t.Errorf("context.json after %s lists %d work items, want all 6, the closed ones included: %+v", x.ID,
			len(c.WorkItems), c.WorkItems)
	}
}

// harmflow-app#341: a closed item moves from the file of the items in play to the archive beside it, and a change to an
// item in play rewrites only the first file. An archived item stays there, and one selected again comes back to the
// first file; after a restart the ledger holds every item, each as it was last saved.
func TestClosedItemsMoveToTheArchive(t *testing.T) {
	e := newEnv(t)
	e.create("archiver", "")
	runTask(e, "archiver", domain.StatusCompleted, selectTwo(e.app.d.Paths.Logs)+withReport(`{"outcome":"completed",`+
		`"workItems":[{"key":"o/r#1","outcome":"completed"},{"key":"o/r#2","outcome":"failed"}]}`))

	open, archive := savedItems(t, e, ledgerName), savedItems(t, e, archiveName)
	if w := archive["archiver"]["o/r#1"]; w == nil || w.Status != domain.WorkItemCompleted {
		t.Errorf("the archive's o/r#1 = %+v, want it completed", w)
	}
	if w := open["archiver"]["o/r#1"]; w != nil {
		t.Errorf("the ledger's file of items in play keeps o/r#1, completed: %+v", w)
	}
	if w := open["archiver"]["o/r#2"]; w == nil || w.Status != domain.WorkItemFailed {
		t.Errorf("the file of items in play: o/r#2 = %+v, want it failed", w)
	}
	if w := archive["archiver"]["o/r#2"]; w != nil {
		t.Errorf("the archive keeps o/r#2, still in play: %+v", w)
	}

	// A change to an item in play leaves the archive as it is on disk.
	archivePath := filepath.Join(e.paths.State, archiveName+".json")
	before, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(archivePath, before.ModTime(), before.ModTime().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.app.failWorkItem("archiver", "o/r#2", "x", "", e.app.taskLimits("archiver"))
	if after, err := os.Stat(archivePath); err != nil || !after.ModTime().Equal(before.ModTime().Add(-time.Hour)) {
		t.Errorf("a change to o/r#2, in play, wrote the archive again (%v)", err)
	}

	// Selected again, o/r#1 is back in play, and its archived copy stays until it closes again.
	e.app.trackWorkItem("archiver", "o/r#1", "", "", "y")
	open, archive = savedItems(t, e, ledgerName), savedItems(t, e, archiveName)
	if w := open["archiver"]["o/r#1"]; w == nil || w.Status != domain.WorkItemInProgress {
		t.Errorf("o/r#1 selected again: the file of items in play holds %+v, want it in progress", w)
	}
	if archive["archiver"]["o/r#1"] == nil {
		t.Error("o/r#1 selected again: its archived copy was deleted")
	}

	e.app.Stop(domain.Text{Message: "restart"})
	e = e.restart(t)
	if w := ledgerItem(e, "archiver", "o/r#1"); w == nil || w.Status != domain.WorkItemInProgress ||
		w.LastExecutionID != "y" {
		t.Errorf("after a restart, o/r#1 = %+v; want in progress, selected by y, the later of its two copies", w)
	}
	if w := ledgerItem(e, "archiver", "o/r#2"); w == nil || w.Status != domain.WorkItemFailed || w.Failures != 2 {
		t.Errorf("after a restart, o/r#2 = %+v; want failed, with two failures", w)
	}
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

// harmflow-app#569: a work_item.selected line whose key is not owner/repo#<number> changes neither the execution's
// record nor the ledger, and the log says it was ignored, with its key, under no work item. A key of that form is
// tracked as before, and a line of another form after it leaves the record naming that one.
func TestSelectionOfAKeyOfAnotherFormIsIgnored(t *testing.T) {
	const ignoredText = "its selection was ignored"
	for _, tc := range []struct {
		name    string
		keys    []string // the keys the session selects, in order; "" writes a line without workItem
		tracked string   // the work item the record and the ledger name, "" for none
		ignored []string // the keys the log says were ignored, in order
		wait    string   // a text the log holds once the daemon has read every line
	}{
		{"another form", []string{"o/r#81 8"}, "", []string{"o/r#81 8"}, ignoredText},
		{"no key", []string{""}, "", []string{""}, ignoredText},
		{"owner/repo#<number>", []string{"o/r#81"}, "o/r#81", nil, "Select o/r#81"},
		{"another form after owner/repo#<number>", []string{"o/r#81", "o/r#81 8"}, "o/r#81", []string{"o/r#81 8"},
			ignoredText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.create("selector", "")
			var lines []string
			for _, key := range tc.keys {
				line := `{"type":"work_item.selected","repository":"o/r","message":"Select ` + key + `"`
				if key != "" {
					line += `,"workItem":"` + key + `"`
				}
				lines = append(lines, line+"}")
			}
			x := runTask(e, "selector", domain.StatusCompleted, `P="$HARMFLOW_EXECUTION_DIR/progress.jsonl"; L="`+
				e.app.d.Paths.Logs+`/$HARMFLOW_EXECUTION_ID.jsonl"
printf '%s\n' '`+strings.Join(lines, `' '`)+`' >> "$P"
until grep -qF '`+tc.wait+`' "$L"; do sleep 0.05; done
echo 'END ok'`)

			if x.WorkItem != tc.tracked {
				t.Errorf("GetExecution(%s).WorkItem = %q, want %q", x.ID, x.WorkItem, tc.tracked)
			}
			var held, want []string
			for _, w := range e.app.Queue().WorkItems {
				if w.TaskID == "selector" {
					held = append(held, w.Key)
				}
			}
			if tc.tracked != "" {
				want = []string{tc.tracked}
			}
			if !slices.Equal(held, want) {
				t.Errorf("ledger holds %q for the task, want %q", held, want)
			}
			if w := ledgerItem(e, "selector", tc.tracked); tc.tracked != "" && (w == nil ||
				w.Status != domain.WorkItemFailed || w.Failures != 1 || w.LastExecutionID != x.ID) {
				t.Errorf("%s, selected and left without a result: %+v; want failed, with one failure, by %s", tc.tracked,
					w, x.ID)
			}

			events, _, err := e.app.ExecutionEvents(x.ID, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			var ignored []string
			for _, ev := range events {
				switch {
				case ev.Type != domain.EventWorkItemSelected:
				case ev.MessageKey == "work_item_key_invalid":
					ignored = append(ignored, ev.Params["workItem"])
					if ev.WorkItemID != "" || ev.Severity != domain.SeverityWarning ||
						!strings.Contains(ev.Message, ignoredText) {
						t.Errorf("ignored selection logged as %+v; want a warning under no work item saying %q", ev,
							ignoredText)
					}
				case ev.WorkItemID != tc.tracked:
					t.Errorf("work_item.selected logged under %q, want %q", ev.WorkItemID, tc.tracked)
				}
			}
			if !slices.Equal(ignored, tc.ignored) {
				t.Errorf("the log says %q were ignored, want %q", ignored, tc.ignored)
			}
		})
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

// harmflow-app#413: a crash counts one failure against every item the session selected and left without a result,
// not only the last one, which the record names. Recovery reads the others from the execution's log; the record names
// the last item before the log does, so a crash between the two still counts it. The record still names the last item
// alone, and an item selected twice counts once.
func TestRecoveryCountsEverySelectedItem(t *testing.T) {
	for _, tc := range []struct {
		name  string
		crash string // the message of the event the daemon dies before appending
	}{
		{"every selection logged", "Work on the second"},
		{"the last selection not yet logged", "Select the second"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.create("crashed", "")
			e.claude.setScript(func(ports.RunRequest) string {
				return selectTwo(e.app.d.Paths.Logs) + `printf '%s\n' ` +
					`'{"type":"work_item.selected","workItem":"o/r#1","message":"Select the first again"}' ` +
					`'{"type":"work_item.selected","workItem":"o/r#2","message":"Select the second again"}' ` +
					`'{"type":"step.started","message":"Work on the second"}' >> "$P"` + "\nexec sleep 30"
			})
			rec, id := e.crashAt("crashed", func(ev domain.Event) bool { return ev.Message == tc.crash })

			x, err := rec.app.GetExecution(id)
			if err != nil {
				t.Fatal(err)
			}
			if x.Status != domain.StatusInterrupted || x.WorkItem != "o/r#2" {
				t.Errorf("GetExecution(%s): %s, work item %q; want interrupted, o/r#2", id, x.Status, x.WorkItem)
			}
			for _, key := range []string{"o/r#1", "o/r#2"} {
				if w := ledgerItem(rec, "crashed", key); w == nil || w.Status != domain.WorkItemFailed || w.Failures != 1 ||
					w.LastExecutionID != id {
					t.Errorf("%s, selected by the crashed session: %+v; want failed, with one failure, by %s", key, w, id)
				}
			}
		})
	}
}

// heldState is a StateStore whose first Save, once it has begun, waits for release before it writes; it records what
// each Save is given, in the order the saves write, and closes second once the second one has written.
type heldState struct {
	entered chan struct{} // closed when the first Save begins
	second  chan struct{}
	release chan struct{}

	mu     sync.Mutex
	calls  int
	writes []ledgerFile
}

func (s *heldState) Load(string, any) (bool, error) { return false, nil }

func (s *heldState) Save(_ string, v any) error {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if n == 1 {
		close(s.entered)
		<-s.release
	}
	s.mu.Lock()
	s.writes = append(s.writes, v.(ledgerFile))
	s.mu.Unlock()
	if n == 2 {
		close(s.second)
	}
	return nil
}

// harmflow-app#585: two saves of the ledger at once write it in the order their snapshots were taken, so the last one
// written holds every change a save took, and a crash after it loses none. The first save is held in its write until
// the second one has written, or for a second while the second waits for it.
func TestLedgerSavesKeepTheirOrder(t *testing.T) {
	state := &heldState{entered: make(chan struct{}), second: make(chan struct{}), release: make(chan struct{})}
	a, err := New(Deps{Paths: NewPaths(t.TempDir(), t.TempDir(), t.TempDir()), State: state,
		Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.itemLocked("t", "o/r#1")
	a.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.saveLedger()
	}()
	<-state.entered
	go func() {
		defer wg.Done()
		a.trackWorkItem("t", "o/r#2", "", "", "x2")
	}()
	select {
	case <-state.second:
	case <-time.After(time.Second):
	}
	close(state.release)
	wg.Wait()

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.writes) != 2 {
		t.Fatalf("Save called %d times; want 2", len(state.writes))
	}
	last := state.writes[1].Tasks["t"]
	if last["o/r#1"] == nil || last["o/r#2"] == nil {
		t.Errorf("last ledger written holds %v; want o/r#1 and o/r#2, the newest snapshot", slices.Sorted(maps.Keys(last)))
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
