package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

func (a *App) maintenanceLoop() {
	defer a.wg.Done()
	a.ApplyRetention()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-t.C:
			a.ApplyRetention()
		}
	}
}

// ApplyRetention deletes the logs and execution directories older than the retention period. The execution records
// themselves are kept with no limit (D-073).
func (a *App) ApplyRetention() {
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
		_ = a.d.Events.DeleteLog(x.ID)
		_ = os.RemoveAll(filepath.Join(a.d.Paths.Runs, x.ID))
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

// CleanWorkspaces removes the worktrees no longer needed (D-073): it keeps those of running tasks, of a draft pull
// request still in progress, and of a failed or cancelled execution whose log has not expired.
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
	keep := map[string]string{}
	for _, x := range a.executions {
		for _, w := range x.WorkItems {
			if w.WorktreePath == "" {
				continue
			}
			item := a.ledger[x.TaskID][w.Key]
			switch {
			case item != nil && item.Status == domain.WorkItemInProgress:
				keep[filepath.Clean(w.WorktreePath)] = "its draft pull request is still in progress"
			case (x.Status == domain.StatusFailed || x.Status == domain.StatusCancelled ||
				x.Status == domain.StatusInterrupted) && !x.LogExpired:
				keep[filepath.Clean(w.WorktreePath)] = "kept for diagnosis until its execution's log expires"
			}
		}
	}
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
		if !exists[td.Name()] {
			removeWorktrees(taskDir)
			if os.RemoveAll(taskDir) == nil {
				res.Removed = append(res.Removed, taskDir)
			}
			continue
		}
		entries, _ := os.ReadDir(taskDir)
		for _, en := range entries {
			if !en.IsDir() {
				continue
			}
			p := filepath.Join(taskDir, en.Name())
			if reason, ok := keep[filepath.Clean(p)]; ok {
				res.Kept = append(res.Kept, KeptPath{p, reason})
				continue
			}
			if !isWorktree(p) {
				res.Kept = append(res.Kept, KeptPath{p, "not a Git worktree HarmFlow created"})
				continue
			}
			removeWorktree(p)
			if os.RemoveAll(p) == nil {
				res.Removed = append(res.Removed, p)
			}
		}
	}
	return res
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
	_ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", p).Run()
	_ = exec.Command("git", "-C", repo, "worktree", "prune").Run()
}

func removeWorktrees(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		p := filepath.Join(dir, en.Name())
		if en.IsDir() && isWorktree(p) {
			removeWorktree(p)
		}
	}
}
