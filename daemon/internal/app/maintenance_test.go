package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
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
	w.branches++
	p := filepath.Join(w.app.d.Paths.Workspace, task, name)
	w.git("-C", w.repo, "worktree", "add", "-q", "-b", fmt.Sprintf("harmflow/%d-test", w.branches), p)
	return p
}

// ended records an execution of task that ended with status at the current time.
func (w *workspaces) ended(task string, status domain.ExecutionStatus, report ...domain.WorkItemResult) *domain.Execution {
	now := w.clock.Now()
	x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: task}}, domain.TriggerManual, nil, now)
	x.Status = status
	x.EndedAt = &now
	x.WorkspacePath = filepath.Join(w.app.d.Paths.Workspace, task)
	x.WorkItems = append([]domain.WorkItemResult{}, report...)
	w.app.mu.Lock()
	w.app.executions[x.ID] = x
	w.app.mu.Unlock()
	return x
}

var defaultLimits = domain.WorkItemLimits{MaxResumeAttempts: domain.DefaultMaxResumeAttempts,
	MaxFailedRetries: domain.DefaultMaxFailedRetries}

// selected records what the work_item.selected progress line leaves in the ledger: the item, in progress.
func (w *workspaces) selected(task, key string, x *domain.Execution) {
	w.app.mu.Lock()
	x.WorkItem = key
	w.app.mu.Unlock()
	w.app.trackWorkItem(task, key, "Fix it", "", x.ID)
}

// reported records what a report.json naming the item with outcome leaves in the ledger.
func (w *workspaces) reported(task, key, outcome string, x *domain.Execution) {
	w.selected(task, key, x)
	w.app.applyWorkItem(task, domain.WorkItemResult{Key: key, Outcome: outcome}, x.ID, defaultLimits)
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
	rel, _ := filepath.Rel(w.app.d.Paths.Workspace, p)
	if list := w.git("-C", w.repo, "worktree", "list", "--porcelain"); strings.Contains(list, rel) {
		w.t.Errorf("Git still lists the worktree %s:\n%s", rel, list)
	}
}

// FS-006 FR-016: a cancelled execution leaves no report, so its worktree is tied to it through the ledger's work item
// and the path the worktree skill gives it, and kept until the execution's log expires.
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

// FS-006 FR-016: deleting a task removes nothing by itself; a failed execution's worktree is kept while the
// execution's log is, and goes with it. The empty directory of the deleted task goes last.
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

// FS-006 FR-016: a completed item's draft pull request is still open, and a needs_attention item waits for a human;
// both keep their worktrees, even once their executions' logs expire.
func TestCleanKeepsCompletedAndNeedsAttentionWorktrees(t *testing.T) {
	w := newWorkspaces(t)
	w.create("drafts", "")
	draft := w.worktree("drafts", "r-1")
	w.reported("drafts", "o/r#1", "completed", w.ended("drafts", domain.StatusCompleted))
	stuck := w.worktree("drafts", "r-2")
	w.reported("drafts", "o/r#2", "needs_attention", w.ended("drafts", domain.StatusNeedsAttention))
	// Past its resume limit after a failure, an item needs attention although its last execution failed.
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

// FS-006 FR-016, FS-010 AC-013: a closed item's worktree is removed, at the skill's path in any case, or at the path
// its report named; the ledger's closed status wins over an earlier failed execution that reported the same path.
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

// FS-006 FR-016: the clean-up never removes what it has no record of: a worktree no work item or report names, in a
// task's workspace or in one no task or ledger entry knows.
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
