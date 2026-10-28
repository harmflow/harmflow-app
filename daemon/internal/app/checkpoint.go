package app

import (
	"context"
	"fmt"
	"path/filepath"
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
	r.a.mu.Unlock()
	prev.checkpoint = c
	r.save(true)
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
