package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
// themselves are kept with no limit (D-073). A pass waits for one already running: beside it, it would skip a log the
// other had just deleted, since a missing log is aged by its execution's end, and return before that log's record
// says it expired.
func (a *App) ApplyRetention() {
	a.retentionMu.Lock()
	defer a.retentionMu.Unlock()
	a.mu.Lock()
	days := a.settings.LogRetentionDays
	var candidates []*domain.Execution
	for _, x := range a.executions {
		if x.Status.IsTerminal() && !x.LogExpired && x.EndedAt != nil {
			candidates = append(candidates, x)
		}
	}
	a.mu.Unlock()
	cutoff := a.now().Add(-time.Duration(days) * 24 * time.Hour)
	for _, x := range candidates {
		mod, ok := a.d.Events.LogModTime(x.ID)
		if !ok {
			mod = *x.EndedAt
		}
		if mod.After(cutoff) {
			continue
		}
		if err := a.d.Events.DeleteLog(x.ID); err != nil {
			a.log.Printf("deleting the log of execution %s: %v", x.ID, err)
		}
		if err := os.RemoveAll(filepath.Join(a.d.Paths.Runs, x.ID)); err != nil {
			a.log.Printf("deleting the directory of execution %s: %v", x.ID, err)
		}
		a.mu.Lock()
		x.LogExpired = true
		a.mu.Unlock()
		a.persistExecution(x)
	}
}

type KeptPath struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type CleanResult struct {
	Removed []string   `json:"removed"`
	Kept    []KeptPath `json:"kept"`
}

// CleanWorkspaces removes the worktrees no longer needed (FS-006 FR-016, D-073). A worktree is tied to its work item
// through the path harmflow-github-worktree §2 gives it, <workspace>/<repo-name>-<issue-number>, or the worktreePath a
// report named; it is removed only when that item reads closed in the ledger, or when the item's last execution was
// cancelled, failed (a timeout included), or interrupted and its log has expired. Every other worktree is kept: those
// of running tasks, of completed and needs_attention items, and any HarmFlow cannot tie to a work item, whether or
// not its task still exists.
func (a *App) CleanWorkspaces() CleanResult {
	res := CleanResult{Removed: []string{}, Kept: []KeptPath{}}
	taskDirs, err := os.ReadDir(a.d.Paths.Workspace)
	if err != nil {
		return res
	}
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
	a.mu.Unlock()
	for _, td := range taskDirs {
		if !td.IsDir() {
			continue
		}
		taskDir := filepath.Join(a.d.Paths.Workspace, td.Name())
		if running[td.Name()] {
			res.Kept = append(res.Kept, KeptPath{taskDir, "the task is running"})
			continue
		}
		entries, _ := os.ReadDir(taskDir) // a directory that cannot be read has nothing this pass can clean
		for _, en := range entries {
			if !en.IsDir() {
				continue
			}
			p := filepath.Join(taskDir, en.Name())
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
		// A deleted task's directory goes once nothing is left in it; os.Remove never removes a non-empty one.
		if !exists[td.Name()] && os.Remove(taskDir) == nil {
			res.Removed = append(res.Removed, taskDir)
		}
	}
	return res
}

// worktreeFate is what the clean-up does with one worktree, and why it keeps it.
type worktreeFate struct {
	remove bool
	reason string
}

// worktreeFatesLocked decides, from the ledger, the fate of each worktree HarmFlow can tie to a work item, keyed by
// worktreeKey. The path harmflow-github-worktree §2 derives from an item decides first; a worktreePath a report named
// only adds a path no item derives. Where two claims meet, keeping wins.
func (a *App) worktreeFatesLocked() map[string]worktreeFate {
	fates := map[string]worktreeFate{}
	claim := func(m map[string]worktreeFate, p string, f worktreeFate) {
		k := worktreeKey(p)
		if old, ok := m[k]; ok && !old.remove {
			return
		}
		m[k] = f
	}
	for taskID, items := range a.ledger {
		for key, item := range items {
			name, ok := worktreeName(key)
			if !ok {
				continue
			}
			x := a.executions[item.LastExecutionID]
			dir := filepath.Join(a.d.Paths.Workspace, taskID)
			if x != nil && x.WorkspacePath != "" {
				dir = x.WorkspacePath
			}
			claim(fates, filepath.Join(dir, name), fateOf(item, x))
		}
	}
	reported := map[string]worktreeFate{}
	for _, x := range a.executions {
		for _, w := range x.WorkItems {
			if w.WorktreePath == "" {
				continue
			}
			f := worktreeFate{reason: "HarmFlow has no record of its work item"}
			if item := a.ledger[x.TaskID][w.Key]; item != nil {
				f = fateOf(item, a.executions[item.LastExecutionID])
			}
			claim(reported, w.WorktreePath, f)
		}
	}
	for k, f := range reported {
		if _, ok := fates[k]; !ok {
			fates[k] = f
		}
	}
	return fates
}

// fateOf decides a work item's worktree from the item's status and from its last execution, x, nil when HarmFlow has
// no record of it.
func fateOf(item *domain.WorkItem, x *domain.Execution) worktreeFate {
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

// worktreeName is the directory harmflow-github-worktree §2 gives a work item's worktree, <repo-name>-<issue-number>;
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
