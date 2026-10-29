package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// markWorkspace takes what the checkpoint after the attempt about to start measures from, when a switch can follow it
// and the daemon has a reader of the workspace; nil otherwise, and the attempt gets no checkpoint.
func (r *run) markWorkspace(ctx context.Context, canSwitch bool) *ports.WorkspaceMark {
	if !canSwitch || r.a.d.Workspace == nil {
		return nil
	}
	m := r.a.d.Workspace.Mark(ctx, r.work)
	return &m
}

// readCheckpoint reads the files changed in the workspace since mark; nil when the reader gives nothing.
func (r *run) readCheckpoint(ctx context.Context, mark ports.WorkspaceMark) *domain.Checkpoint {
	c := r.a.d.Workspace.Changes(ctx, r.work, mark)
	if c == nil {
		return nil
	}
	// The session's progress and report, when the execution directory is inside the workspace, are not its work.
	if rel, err := filepath.Rel(r.work, r.dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		dir := filepath.ToSlash(rel)
		kept := c.Files[:0]
		for _, f := range c.Files {
			if f.Path != dir && !strings.HasPrefix(f.Path, dir+"/") {
				kept = append(kept, f)
			}
		}
		c.Files = kept
	}
	return c
}

// keepCheckpoint saves c on the record of the attempt prev numbers before the switch goes on, so it is there before
// the next attempt starts, whose context names its files through prev.
func (r *run) keepCheckpoint(c *domain.Checkpoint, prev *previousAttempt) {
	r.a.mu.Lock()
	for i := range r.x.Attempts {
		if r.x.Attempts[i].Number == prev.Number {
			r.x.Attempts[i].Checkpoint = c
		}
	}
	r.x.Changed()
	r.a.mu.Unlock()
	prev.checkpoint = c
	r.save(true)
}

// attemptWorkItem is one work item an attempt worked, as the next attempt's context.json gives it: the branch the
// attempt reported, the commit that branch is at where the workspace has it checked out, the last test result the
// attempt reported, and the checkpoint's files in that branch's repository folder. A value the attempt never reported,
// or that the workspace could not give, is left out.
type attemptWorkItem struct {
	Key          string                  `json:"key"`
	Repository   string                  `json:"repository,omitempty"`
	Branch       string                  `json:"branch,omitempty"`
	HeadCommit   string                  `json:"headCommit,omitempty"`
	Tests        *attemptTests           `json:"tests,omitempty"`
	ChangedFiles []domain.CheckpointFile `json:"changedFiles,omitzero"`
}

// attemptTests is the last test result an attempt reported for a work item: passed, failed, or not_run, and the
// summary it gave. The test output itself stays in the execution's log.
type attemptTests struct {
	Status  string `json:"status"`
	Summary string `json:"summary,omitempty"`
}

// testStatus reports whether s is a status a test result reports.
func testStatus(s string) bool {
	return s == "passed" || s == "failed" || s == "not_run"
}

// attemptItem is the current attempt's entry for the work item key, added when the attempt had none, with repository
// when it is given; nil without a key. The caller holds a.mu.
func (r *run) attemptItem(key, repository string) *attemptWorkItem {
	if key == "" {
		return nil
	}
	i := slices.IndexFunc(r.items, func(it attemptWorkItem) bool { return it.Key == key })
	if i < 0 {
		r.items = append(r.items, attemptWorkItem{Key: key})
		i = len(r.items) - 1
	}
	if repository != "" {
		r.items[i].Repository = repository
	}
	return &r.items[i]
}

// continuedItems are the work items an attempt starts with: the execution's current one, which a continuing session
// works without selecting it again, with its repository and the branch an earlier attempt reported for it.
func (r *run) continuedItems(prev []previousAttempt) []attemptWorkItem {
	if r.x.WorkItem == "" {
		return nil
	}
	it := attemptWorkItem{Key: r.x.WorkItem}
	for _, p := range slices.Backward(prev) {
		if i := slices.IndexFunc(p.WorkItems, func(w attemptWorkItem) bool { return w.Key == it.Key }); i >= 0 {
			it.Repository, it.Branch = p.WorkItems[i].Repository, p.WorkItems[i].Branch
			break
		}
	}
	return []attemptWorkItem{it}
}

// placeWorkItems finds, for each work item of prev with a branch, the repository folder of the workspace that has the
// branch checked out: the item gets that folder's HEAD commit, and the files the checkpoint listed in it. An item
// whose branch no folder has checked out gets neither.
func (r *run) placeWorkItems(ctx context.Context, prev *previousAttempt) {
	if !slices.ContainsFunc(prev.WorkItems, func(it attemptWorkItem) bool { return it.Branch != "" }) {
		return
	}
	heads := r.a.d.Workspace.Heads(ctx, r.work)
	for i := range prev.WorkItems {
		it := &prev.WorkItems[i]
		h := slices.IndexFunc(heads, func(h ports.RepositoryHead) bool { return it.Branch != "" && h.Branch == it.Branch })
		if h < 0 {
			continue
		}
		it.HeadCommit = heads[h].Commit
		if prev.checkpoint != nil {
			it.ChangedFiles = filesIn(prev.checkpoint, heads[h].Path, heads)
		}
	}
}

// filesIn is the checkpoint's files in the repository folder dir, relative to the workspace. The workspace's root,
// ".", holds the files in no other repository folder of heads.
func filesIn(c *domain.Checkpoint, dir string, heads []ports.RepositoryHead) []domain.CheckpointFile {
	files := []domain.CheckpointFile{}
	for _, f := range c.Files {
		first, _, _ := strings.Cut(f.Path, "/")
		in := first == dir && f.Path != dir
		if dir == "." {
			in = !slices.ContainsFunc(heads, func(h ports.RepositoryHead) bool { return h.Path == first })
		}
		if in {
			files = append(files, f)
		}
	}
	return files
}

// checkpointText says, for the next session, which files the previous attempt left changed, and what the reading
// could not see.
func checkpointText(c *domain.Checkpoint) string {
	var b strings.Builder
	if len(c.Files) == 0 {
		b.WriteString("No file it changed was found in the workspace.")
	} else {
		names := make([]string, len(c.Files))
		for i, f := range c.Files {
			names[i] = fmt.Sprintf("%s (%s)", f.Path, f.Status)
		}
		fmt.Fprintf(&b, "The files it left changed in the workspace, read from the workspace itself: %s",
			strings.Join(names, ", "))
		if c.More > 0 {
			fmt.Fprintf(&b, ", and %d more", c.More)
		}
		b.WriteString(".")
	}
	if c.Scanned {
		b.WriteString(" Outside a Git repository, a changed file was found by its modification time, so a file it deleted," +
			" or one whose modification time was kept, is not listed.")
	}
	if c.ScanLimited {
		fmt.Fprintf(&b, " The scan stopped at its limit of %d files and folders, so files it did not reach may have"+
			" changed too.", domain.CheckpointScanLimit)
	}
	return b.String()
}
