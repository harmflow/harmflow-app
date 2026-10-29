package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

func (a *App) maintenanceLoop(ctx context.Context) {
	defer a.wg.Done()
	a.ApplyRetention()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.ApplyRetention()
		}
	}
}

// ApplyRetention deletes the logs and execution directories older than the retention period. The execution records
// themselves are kept with no limit. A pass waits for one already running: beside it, it would skip a log the
// other had just deleted, since a missing log is aged by its execution's end, and return before that log's record
// says it expired.
func (a *App) ApplyRetention() {
	a.retentionMu.Lock()
	defer a.retentionMu.Unlock()
	// Each log expired saves its record, which writes the whole index: held, the index is written once, at the end.
	release := a.d.Executions.HoldIndex()
	defer release()
	type candidate struct {
		id      string
		endedAt time.Time
		held    *domain.Execution // the record held whole, nil for an execution held as its summary
	}
	a.mu.Lock()
	days := a.settings.LogRetentionDays
	var candidates []candidate
	for _, x := range a.executions {
		if x.Status.IsTerminal() && !x.LogExpired && x.EndedAt != nil {
			candidates = append(candidates, candidate{x.ID, *x.EndedAt, x})
		}
	}
	for _, s := range a.ended {
		if !s.LogExpired && s.EndedAt != nil {
			candidates = append(candidates, candidate{s.ID, *s.EndedAt, nil})
		}
	}
	a.mu.Unlock()
	cutoff := a.now().Add(-time.Duration(days) * 24 * time.Hour)
	for _, c := range candidates {
		mod, ok := a.d.Events.LogModTime(c.id)
		if !ok {
			mod = c.endedAt
		}
		if mod.After(cutoff) {
			continue
		}
		if err := a.d.Events.DeleteLog(c.id); err != nil {
			a.log.Printf("deleting the log of execution %s: %v", c.id, err)
		}
		if err := os.RemoveAll(filepath.Join(a.d.Paths.Runs, c.id)); err != nil {
			a.log.Printf("deleting the directory of execution %s: %v", c.id, err)
		}
		x := c.held
		if x == nil {
			var err error
			if x, err = a.d.Executions.Load(c.id); err != nil {
				a.log.Printf("reading execution %s to record that its log expired: %v", c.id, err)
				continue
			}
			normalize(x)
		}
		a.mu.Lock()
		x.LogExpired = true
		x.Changed()
		a.mu.Unlock()
		a.persistExecution(x)
	}
}

// KeptPath is a directory CleanWorkspaces left in place, and why.
type KeptPath struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// CleanResult is what CleanWorkspaces removed and what it kept.
type CleanResult struct {
	Removed []string   `json:"removed"`
	Kept    []KeptPath `json:"kept"`
}

// CleanWorkspaces removes the worktrees no longer needed, in the daemon's workspace and in each custom workspace an
// execution recorded. A worktree is tied to its work item through the path hf-github-worktree §2 gives it,
// <workspace>/<repo-name>-<issue-number>, or the worktreePath a report named; it is removed only when that item reads
// closed in the ledger, or when the item's last execution was cancelled, failed (a timeout included), or interrupted
// and its log has expired. Every other worktree is kept: those of running tasks, of completed and needs_attention
// items, and any HarmFlow cannot tie to a work item, whether or not its task still exists. A custom workspace is the
// user's folder, shared by every task that names it: its entries are read as worktrees, one level deep, and the folder
// itself is never removed.
func (a *App) CleanWorkspaces() CleanResult {
	res := CleanResult{Removed: []string{}, Kept: []KeptPath{}}
	a.mu.Lock()
	running := map[string]bool{}
	for id := range a.running {
		running[id] = true
	}
	exists := map[string]bool{}
	for id := range a.tasks {
		exists[id] = true
	}
	fates := a.worktreeFatesLocked()
	custom := a.customWorkspacesLocked()
	a.mu.Unlock()
	taskDirs, _ := os.ReadDir(a.d.Paths.Workspace) // a workspace that cannot be read has nothing this pass can clean
	for _, td := range taskDirs {
		if !td.IsDir() {
			continue
		}
		taskDir := filepath.Join(a.d.Paths.Workspace, td.Name())
		if running[td.Name()] {
			res.Kept = append(res.Kept, KeptPath{taskDir, "the task is running"})
			continue
		}
		res.clean(taskDir, fates)
		// A deleted task's directory goes once nothing is left in it; os.Remove never removes a non-empty one.
		if !exists[td.Name()] && os.Remove(taskDir) == nil {
			res.Removed = append(res.Removed, taskDir)
		}
	}
	for _, dir := range custom {
		res.clean(dir, fates)
	}
	return res
}

// clean removes each worktree directly in the workspace dir whose fate is removal, and lists every other directory
// there as kept, with its reason.
func (res *CleanResult) clean(dir string, fates map[string]worktreeFate) {
	entries, _ := os.ReadDir(dir) // a directory that cannot be read has nothing this pass can clean
	for _, en := range entries {
		if !en.IsDir() {
			continue
		}
		p := filepath.Join(dir, en.Name())
		fate, known := fates[worktreeKey(p)]
		switch {
		case !known:
			res.Kept = append(res.Kept, KeptPath{p, "HarmFlow has no record of its work item"})
		case !fate.remove:
			res.Kept = append(res.Kept, KeptPath{p, fate.reason})
		case !isWorktree(p):
			res.Kept = append(res.Kept, KeptPath{p, "not a Git worktree HarmFlow created"})
		default:
			removeWorktree(p)
			if os.RemoveAll(p) == nil {
				res.Removed = append(res.Removed, p)
			}
		}
	}
}

// customWorkspacesLocked lists, sorted and once each, the workspaces outside the daemon's own that executions
// recorded: the custom paths their tasks named. A script's working directory is not one; the worktree skill never runs
// there, and it is the user's home folder by default.
func (a *App) customWorkspacesLocked() []string {
	own := filepath.Clean(a.d.Paths.Workspace)
	seen := map[string]bool{}
	var dirs []string
	a.eachSummaryLocked(func(x *domain.ExecutionSummary) {
		if x.WorkspacePath == "" || x.Script {
			return
		}
		p := filepath.Clean(x.WorkspacePath)
		if filepath.Dir(p) == own || seen[worktreeKey(p)] {
			return
		}
		seen[worktreeKey(p)] = true
		dirs = append(dirs, p)
	})
	slices.Sort(dirs)
	return dirs
}

// worktreeFate is what the clean-up does with one worktree, and why it keeps it.
type worktreeFate struct {
	remove bool
	reason string
}

// worktreeFatesLocked decides, from the ledger, the fate of each worktree HarmFlow can tie to a work item, keyed by
// worktreeKey. The path hf-github-worktree §2 derives from an item decides first; a worktreePath a report named
// only adds a path no item derives. Where two claims meet, keeping wins. A running task keeps its items' worktrees,
// wherever its workspace is: its session may be reusing one.
func (a *App) worktreeFatesLocked() map[string]worktreeFate {
	fates := map[string]worktreeFate{}
	claim := func(m map[string]worktreeFate, p string, f worktreeFate) {
		k := worktreeKey(p)
		if old, ok := m[k]; ok && !old.remove {
			return
		}
		m[k] = f
	}
	fateIn := func(taskID string, item *domain.WorkItem) worktreeFate {
		if _, ok := a.running[taskID]; ok {
			return worktreeFate{reason: "the task is running"}
		}
		x, _ := a.summaryLocked(item.LastExecutionID)
		return fateOf(item, x)
	}
	for taskID, items := range a.ledger {
		for key, item := range items {
			name, ok := worktreeName(key)
			if !ok {
				continue
			}
			dir := filepath.Join(a.d.Paths.Workspace, taskID)
			if x, ok := a.summaryLocked(item.LastExecutionID); ok && x.WorkspacePath != "" {
				dir = x.WorkspacePath
			}
			claim(fates, filepath.Join(dir, name), fateIn(taskID, item))
		}
	}
	reported := map[string]worktreeFate{}
	a.eachSummaryLocked(func(x *domain.ExecutionSummary) {
		for _, w := range x.WorkItems {
			if w.WorktreePath == "" {
				continue
			}
			f := worktreeFate{reason: "HarmFlow has no record of its work item"}
			if item := a.ledger[x.TaskID][w.Key]; item != nil {
				f = fateIn(x.TaskID, item)
			}
			claim(reported, w.WorktreePath, f)
		}
	})
	for k, f := range reported {
		if _, ok := fates[k]; !ok {
			fates[k] = f
		}
	}
	return fates
}

// fateOf decides a work item's worktree from the item's status and from its last execution, x, nil when HarmFlow has
// no record of it.
func fateOf(item *domain.WorkItem, x *domain.ExecutionSummary) worktreeFate {
	switch item.Status {
	case domain.WorkItemClosed:
		return worktreeFate{remove: true}
	case domain.WorkItemCompleted:
		return worktreeFate{reason: "its draft pull request is still in progress"}
	case domain.WorkItemNeedsAttention:
		return worktreeFate{reason: "its work item needs attention"}
	}
	if x == nil {
		return worktreeFate{reason: "HarmFlow has no record of its execution"}
	}
	switch {
	case x.Status == domain.StatusFailed || x.Status == domain.StatusCancelled || x.Status == domain.StatusInterrupted:
		if x.LogExpired {
			return worktreeFate{remove: true}
		}
		return worktreeFate{reason: "kept for diagnosis until its execution's log expires"}
	case !x.Status.IsTerminal():
		return worktreeFate{reason: "its execution has not ended"}
	}
	return worktreeFate{reason: "its work item has not ended"}
}

// githubItemKey is a GitHub work item's key, OWNER/REPO#N.
var githubItemKey = regexp.MustCompile(`^[\w.-]+/([\w.-]+)#([1-9][0-9]*)$`)

// worktreeName is the directory hf-github-worktree §2 gives a work item's worktree, <repo-name>-<issue-number>;
// false for a key that names no GitHub issue.
func worktreeName(key string) (string, bool) {
	m := githubItemKey.FindStringSubmatch(key)
	if m == nil {
		return "", false
	}
	return m[1] + "-" + m[2], true
}

// worktreeKey compares worktree paths as the default macOS volume and GitHub do, ignoring case: a key that spells
// the repository's name in another case still names the same worktree.
func worktreeKey(p string) string {
	return strings.ToLower(filepath.Clean(p))
}

// isWorktree reports a linked Git worktree: its .git is a file pointing at the main repository.
func isWorktree(p string) bool {
	st, err := os.Lstat(filepath.Join(p, ".git"))
	return err == nil && st.Mode().IsRegular()
}

func removeWorktree(p string) {
	data, err := os.ReadFile(filepath.Join(p, ".git"))
	if err != nil {
		return
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
	// <repo>/.git/worktrees/<name> → <repo>
	repo := filepath.Dir(filepath.Dir(filepath.Dir(gitdir)))
	// git's answer changes nothing: the caller removes the directory itself, and prune drops a record left stale.
	_ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", p).Run()
	_ = exec.Command("git", "-C", repo, "worktree", "prune").Run()
}
