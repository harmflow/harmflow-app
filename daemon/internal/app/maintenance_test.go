package app

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// workspaces builds the state CleanWorkspaces reads: real Git worktrees under the workspace, and the executions and
// ledger entries the executor would have left, written through the ledger's own functions.
type workspaces struct {
	*env
	repo     string
	branches int
}

func newWorkspaces(t *testing.T) *workspaces {
	t.Helper()
	w := &workspaces{env: newEnv(t), repo: t.TempDir()}
	w.git("-C", w.repo, "init", "-q")
	w.git("-C", w.repo, "commit", "-q", "--allow-empty", "-m", "init")
	return w
}

// git runs Git with neither the user's nor the system's configuration, under a placeholder identity.
func (w *workspaces) git(args ...string) string {
	w.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=HarmFlow Test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// worktree adds a linked worktree on its own branch at <workspace>/<task>/<name>, as hf-github-worktree §2 does.
func (w *workspaces) worktree(task, name string) string {
	w.t.Helper()
	return w.worktreeIn(filepath.Join(w.app.d.Paths.Workspace, task), name)
}

// worktreeIn adds a linked worktree on its own branch at <dir>/<name>, as hf-github-worktree §2 does in a task's
// custom workspace dir.
func (w *workspaces) worktreeIn(dir, name string) string {
	w.t.Helper()
	w.branches++
	p := filepath.Join(dir, name)
	w.git("-C", w.repo, "worktree", "add", "-q", "-b", fmt.Sprintf("harmflow/%d-test", w.branches), p)
	return p
}

// ended records an execution of task that ended with status at the current time, in the daemon's workspace.
func (w *workspaces) ended(task string, status domain.ExecutionStatus, report ...domain.WorkItemResult) *domain.Execution {
	return w.endedIn(task, filepath.Join(w.app.d.Paths.Workspace, task), status, report...)
}

// endedIn is ended for an execution whose workspace was dir, as prepare records a custom path the task named.
func (w *workspaces) endedIn(task, dir string, status domain.ExecutionStatus, report ...domain.WorkItemResult) *domain.Execution {
	now := w.clock.Now()
	x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: task}}, domain.TriggerManual, nil, now)
	x.Status = status
	x.EndedAt = &now
	x.WorkspacePath = dir
	x.WorkItems = append([]domain.WorkItemResult{}, report...)
	w.app.mu.Lock()
	w.app.addExecutionLocked(x)
	w.app.mu.Unlock()
	return x
}

var defaultLimits = domain.WorkItemLimits{MaxResumeAttempts: domain.DefaultMaxResumeAttempts,
	MaxFailedRetries: domain.DefaultMaxFailedRetries}

// selected records what the work_item.selected progress line leaves in the ledger: the item, in progress. It returns
// what the ledger held for the item before.
func (w *workspaces) selected(task, key string, x *domain.Execution) selection {
	w.app.mu.Lock()
	x.WorkItem = key
	w.app.mu.Unlock()
	return w.app.trackWorkItem(task, key, "Fix it", "", x.ID)
}

// reported records what a report.json naming the item with outcome leaves in the ledger.
func (w *workspaces) reported(task, key, outcome string, x *domain.Execution) {
	before := w.selected(task, key, x)
	w.app.applyWorkItem(task, domain.WorkItemResult{Key: key, Outcome: outcome}, x.ID, defaultLimits, &before)
}

// expireLogs lets the retention period elapse and runs the retention pass, as the maintenance loop does.
func (w *workspaces) expireLogs() {
	w.clock.Advance(31 * 24 * time.Hour)
	w.app.ApplyRetention()
}

func (w *workspaces) present(res CleanResult, p, reason string) {
	w.t.Helper()
	if _, err := os.Stat(p); err != nil {
		w.t.Errorf("%s was removed: %v", p, err)
	}
	if slices.Contains(res.Removed, p) {
		w.t.Errorf("%s is listed as removed", p)
	}
	for _, k := range res.Kept {
		if k.Path == p {
			if !strings.Contains(k.Reason, reason) {
				w.t.Errorf("%s kept because %q, want %q", p, k.Reason, reason)
			}
			return
		}
	}
	w.t.Errorf("%s is not listed as kept: %+v", p, res.Kept)
}

func (w *workspaces) gone(res CleanResult, p string) {
	w.t.Helper()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		w.t.Errorf("%s is still there (%v)", p, err)
	}
	if !slices.Contains(res.Removed, p) {
		w.t.Errorf("%s is not listed as removed: %v", p, res.Removed)
	}
	// Git lists the worktree by its resolved path, which on macOS starts with /private where p starts with /var.
	tail := filepath.Join(filepath.Base(filepath.Dir(p)), filepath.Base(p))
	if list := w.git("-C", w.repo, "worktree", "list", "--porcelain"); strings.Contains(list, tail+"\n") {
		w.t.Errorf("Git still lists the worktree %s:\n%s", p, list)
	}
}

// listed reports whether res names p as removed or kept.
func listed(res CleanResult, p string) bool {
	return slices.Contains(res.Removed, p) || slices.ContainsFunc(res.Kept, func(k KeptPath) bool { return k.Path == p })
}

// A cancelled execution leaves no report, so its worktree is tied to it through the ledger's work item and the path the
// worktree skill gives it, and kept until the execution's log expires.
func TestCleanKeepsACancelledWorktreeUntilItsLogExpires(t *testing.T) {
	w := newWorkspaces(t)
	w.create("fixer", "")
	p := w.worktree("fixer", "r-7")
	x := w.ended("fixer", domain.StatusCancelled)
	w.selected("fixer", "o/r#7", x)

	w.present(w.app.CleanWorkspaces(), p, "until its execution's log expires")

	w.expireLogs()
	w.gone(w.app.CleanWorkspaces(), p)
}

// Deleting a task removes nothing by itself; a failed execution's worktree is kept while the execution's log is, and
// goes with it. The empty directory of the deleted task goes last.
func TestCleanKeepsAFailedWorktreeOfADeletedTaskWhileItsLogIsKept(t *testing.T) {
	w := newWorkspaces(t)
	w.create("gone", "")
	p := w.worktree("gone", "r-8")
	timedOut := w.ended("gone", domain.StatusFailed) // a timeout ends failed, before any report is read
	w.selected("gone", "o/r#8", timedOut)
	w.app.failWorkItem("gone", "o/r#8", timedOut.ID, "", defaultLimits)
	if _, err := w.app.DeleteTask("gone"); err != nil {
		t.Fatal(err)
	}

	res := w.app.CleanWorkspaces()
	w.present(res, p, "until its execution's log expires")
	if slices.Contains(res.Removed, filepath.Dir(p)) {
		t.Errorf("the deleted task's workspace was removed: %v", res.Removed)
	}

	w.expireLogs()
	res = w.app.CleanWorkspaces()
	w.gone(res, p)
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) || !slices.Contains(res.Removed, filepath.Dir(p)) {
		t.Errorf("the deleted task's empty workspace is still there (%v): %v", err, res.Removed)
	}
}

// A completed item's draft pull request is still open, and a needs_attention item waits for a human; both keep their
// worktrees, even once their executions' logs expire.
func TestCleanKeepsCompletedAndNeedsAttentionWorktrees(t *testing.T) {
	w := newWorkspaces(t)
	w.create("drafts", "")
	draft := w.worktree("drafts", "r-1")
	w.reported("drafts", "o/r#1", "completed", w.ended("drafts", domain.StatusCompleted))
	stuck := w.worktree("drafts", "r-2")
	w.reported("drafts", "o/r#2", "needs_attention", w.ended("drafts", domain.StatusNeedsAttention))
	// Past its retry limit after a failure, an item needs attention although its last execution failed.
	stalled := w.worktree("drafts", "r-3")
	failed := w.ended("drafts", domain.StatusFailed)
	w.selected("drafts", "o/r#3", failed)
	for range defaultLimits.MaxFailedRetries + 1 {
		w.app.failWorkItem("drafts", "o/r#3", failed.ID, "", defaultLimits)
	}

	check := func(res CleanResult) {
		t.Helper()
		w.present(res, draft, "draft pull request is still in progress")
		w.present(res, stuck, "needs attention")
		w.present(res, stalled, "needs attention")
	}
	check(w.app.CleanWorkspaces())
	w.expireLogs()
	check(w.app.CleanWorkspaces())
}

// A closed item's worktree is removed, at the skill's path in any case, or at the path its report named; the ledger's
// closed status wins over an earlier failed execution that reported the same path.
func TestCleanRemovesTheWorktreeOfAClosedItem(t *testing.T) {
	w := newWorkspaces(t)
	w.create("merged", "")
	derived := w.worktree("merged", "r-4")
	earlier := w.ended("merged", domain.StatusFailed, domain.WorkItemResult{Key: "o/r#4", WorktreePath: derived})
	w.selected("merged", "o/r#4", earlier)
	w.reported("merged", "o/r#4", "closed", w.ended("merged", domain.StatusCompleted))

	otherCase := w.worktree("merged", "mixed-5")
	w.reported("merged", "O/Mixed#5", "closed", w.ended("merged", domain.StatusCompleted))

	named := w.worktree("merged", "custom-6")
	w.reported("merged", "o/r#6", "closed", w.ended("merged", domain.StatusCompleted,
		domain.WorkItemResult{Key: "o/r#6", WorktreePath: named, Outcome: "closed"}))

	res := w.app.CleanWorkspaces()
	for _, p := range []string{derived, otherCase, named} {
		w.gone(res, p)
	}
	if len(res.Removed) != 3 {
		t.Errorf("removed %v, want the three closed items' worktrees", res.Removed)
	}
}

// An execution that finds no eligible work still reports the items whose worktrees hf-github-worktree §3 removed, with
// outcome closed and no work_item.selected line. The ledger reads them closed, so the clean-up agrees with the session:
// it removes such a worktree had the session left it, and keeps the draft's and the failed item's.
func TestCleanAgreesWithANoWorkExecutionThatClosedAnItem(t *testing.T) {
	w := newWorkspaces(t)
	w.create("idle", "")
	merged := w.worktree("idle", "r-1")
	w.reported("idle", "o/r#1", "completed", w.ended("idle", domain.StatusCompleted))
	draft := w.worktree("idle", "r-2")
	w.reported("idle", "o/r#2", "completed", w.ended("idle", domain.StatusCompleted))
	failed := w.worktree("idle", "r-3")
	x := w.ended("idle", domain.StatusFailed)
	w.selected("idle", "o/r#3", x)
	w.app.failWorkItem("idle", "o/r#3", x.ID, "", defaultLimits)

	w.claude.setScript(func(req ports.RunRequest) string {
		report := fmt.Sprintf(`{"outcome":"no_work","summary":"nothing eligible","workItems":[{"key":"o/r#1",`+
			`"repository":"o/r","worktreePath":"%s","outcome":"closed"}]}`, merged)
		return fmt.Sprintf(`printf '%%s' '%s' > %s/report.json; echo 'END ok'`, report, req.ExecutionDir)
	})
	run, err := w.app.RunTask("idle")
	if err != nil || run.Outcome != "started" {
		t.Fatalf("RunTask(idle) = %+v, %v, want started", run, err)
	}
	w.wait(run.ExecutionID, domain.StatusNoWork)
	// The task stops running a moment after its execution ends, and a running task keeps every worktree.
	w.poll(10*time.Second, func() bool {
		w.app.mu.Lock()
		defer w.app.mu.Unlock()
		_, running := w.app.running["idle"]
		return !running
	})

	got := map[string]domain.WorkItemStatus{}
	w.app.mu.Lock()
	for _, items := range w.app.ledger {
		for _, item := range items {
			got[item.Key] = item.Status
		}
	}
	w.app.mu.Unlock()
	want := map[string]domain.WorkItemStatus{"o/r#1": domain.WorkItemClosed, "o/r#2": domain.WorkItemCompleted,
		"o/r#3": domain.WorkItemFailed}
	if !maps.Equal(got, want) {
		t.Errorf("the ledger after the no_work execution = %v, want %v", got, want)
	}
	res := w.app.CleanWorkspaces()
	w.gone(res, merged)
	w.present(res, draft, "draft pull request is still in progress")
	w.present(res, failed, "until its execution's log expires")
}

// The clean-up never removes what it has no record of: a worktree no work item or report names, in a task's workspace
// or in one no task or ledger entry knows.
func TestCleanKeepsWorktreesItCannotAttribute(t *testing.T) {
	w := newWorkspaces(t)
	w.create("known", "")
	w.reported("known", "o/r#9", "closed", w.ended("known", domain.StatusCompleted))
	stray := w.worktree("known", "r-10")
	handMade := w.worktree("known", "scratch")
	orphan := w.worktree("unknown-task", "r-9")
	expired := w.ended("known", domain.StatusCancelled) // no work item was ever selected
	w.expireLogs()
	if !expired.LogExpired {
		t.Fatal("the retention pass did not expire the log")
	}

	res := w.app.CleanWorkspaces()
	for _, p := range []string{stray, handMade, orphan} {
		w.present(res, p, "no record of its work item")
	}
	if len(res.Removed) != 0 {
		t.Errorf("removed %v", res.Removed)
	}
}

// A task's custom workspace holds its worktrees directly, with no task folder; the clean-up judges them there as it
// does in the daemon's workspace.
func TestCleanRemovesEndedWorktreesInACustomWorkspace(t *testing.T) {
	w := newWorkspaces(t)
	w.create("custom", "")
	root := t.TempDir()
	closed := w.worktreeIn(root, "r-1")
	w.reported("custom", "o/r#1", "closed", w.endedIn("custom", root, domain.StatusCompleted))
	cancelled := w.worktreeIn(root, "r-2")
	w.selected("custom", "o/r#2", w.endedIn("custom", root, domain.StatusCancelled))
	failed := w.worktreeIn(root, "r-3")
	f := w.endedIn("custom", root, domain.StatusFailed)
	w.selected("custom", "o/r#3", f)
	w.app.failWorkItem("custom", "o/r#3", f.ID, "", defaultLimits)
	draft := w.worktreeIn(root, "r-4")
	w.reported("custom", "o/r#4", "completed", w.endedIn("custom", root, domain.StatusCompleted))

	res := w.app.CleanWorkspaces()
	w.gone(res, closed)
	w.present(res, cancelled, "until its execution's log expires")
	w.present(res, failed, "until its execution's log expires")
	w.present(res, draft, "draft pull request is still in progress")

	w.expireLogs()
	res = w.app.CleanWorkspaces()
	w.gone(res, cancelled)
	w.gone(res, failed)
	w.present(res, draft, "draft pull request is still in progress")
	if listed(res, root) {
		t.Errorf("the custom workspace %s is listed: %+v", root, res)
	}
}

// A custom workspace is the user's folder: the clean-up never removes it, not even empty once its task is deleted.
func TestCleanKeepsTheCustomWorkspaceOfADeletedTask(t *testing.T) {
	w := newWorkspaces(t)
	w.create("gone", "")
	root := t.TempDir()
	p := w.worktreeIn(root, "r-5")
	w.reported("gone", "o/r#5", "closed", w.endedIn("gone", root, domain.StatusCompleted))
	if _, err := w.app.DeleteTask("gone"); err != nil {
		t.Fatal(err)
	}

	res := w.app.CleanWorkspaces()
	w.gone(res, p)
	if _, err := os.Stat(root); err != nil || slices.Contains(res.Removed, root) {
		t.Errorf("the custom workspace %s was removed (%v): %v", root, err, res.Removed)
	}
}

// In a custom workspace the clean-up removes nothing it cannot tie to a work item, and reads its entries one level
// deep: a directory there is never taken for a task's folder of worktrees.
func TestCleanKeepsWhatItCannotAttributeInACustomWorkspace(t *testing.T) {
	w := newWorkspaces(t)
	w.create("custom", "")
	root := t.TempDir()
	w.endedIn("custom", root, domain.StatusCancelled) // no work item was ever selected
	stray := w.worktreeIn(root, "r-6")
	own := w.worktreeIn(root, "scratch")
	notes := filepath.Join(root, "notes")
	nested := w.worktreeIn(notes, "r-7")
	w.reported("custom", "o/r#7", "closed", w.endedIn("custom", root, domain.StatusCompleted))
	w.expireLogs()

	res := w.app.CleanWorkspaces()
	for _, p := range []string{stray, own, notes} {
		w.present(res, p, "no record of its work item")
	}
	if _, err := os.Stat(nested); err != nil || listed(res, nested) {
		t.Errorf("the clean-up read %s, inside a directory of the workspace (%v): %+v", nested, err, res)
	}
	if len(res.Removed) != 0 {
		t.Errorf("removed %v", res.Removed)
	}
}

// Tasks that share a custom workspace each have their worktrees judged by their own work items, and a running task
// keeps its own.
func TestCleanJudgesEachWorktreeOfASharedCustomWorkspaceByItsItem(t *testing.T) {
	w := newWorkspaces(t)
	root := t.TempDir()
	for _, task := range []string{"merger", "drafter", "busy"} {
		w.create(task, "")
	}
	closed := w.worktreeIn(root, "r-8")
	w.reported("merger", "o/r#8", "closed", w.endedIn("merger", root, domain.StatusCompleted))
	draft := w.worktreeIn(root, "r-9")
	w.reported("drafter", "o/r#9", "completed", w.endedIn("drafter", root, domain.StatusCompleted))
	inUse := w.worktreeIn(root, "r-10")
	w.reported("busy", "o/r#10", "closed", w.endedIn("busy", root, domain.StatusCompleted))
	done := make(chan struct{})
	close(done)
	w.app.mu.Lock()
	w.app.running["busy"] = &runHandle{done: done}
	w.app.mu.Unlock()
	t.Cleanup(func() {
		w.app.mu.Lock()
		delete(w.app.running, "busy")
		w.app.mu.Unlock()
	})

	res := w.app.CleanWorkspaces()
	w.gone(res, closed)
	w.present(res, draft, "draft pull request is still in progress")
	w.present(res, inUse, "the task is running")
}

// A script's working directory is not a workspace: the worktree skill never runs there, so the clean-up never reads it.
func TestCleanSkipsAScriptsWorkingDirectory(t *testing.T) {
	w := newWorkspaces(t)
	wd := t.TempDir()
	sub := filepath.Join(wd, "project")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	x := w.endedIn("script", wd, domain.StatusCompleted)
	w.app.mu.Lock()
	x.Script = &domain.ScriptRun{WorkingDirectory: wd}
	w.app.mu.Unlock()

	if res := w.app.CleanWorkspaces(); listed(res, sub) {
		t.Errorf("CleanWorkspaces() read the script's working directory: %+v", res)
	}
}
