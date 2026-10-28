// Package workspace reads which files an attempt changed in its execution's workspace, from the workspace itself: Git
// for each repository at the workspace's root or directly under it, where the built-in skills create a work item's
// worktree, and a scan of modification times for the rest. It lists paths with their status and reads no file's
// content.
package workspace

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// gitTimeout bounds each git command, so a repository that hangs holds a switch up for that long at most.
const gitTimeout = 20 * time.Second

// Reader is the WorkspaceReader. It finds git on PathEnv, then in the usual Homebrew and system locations, and runs it
// with Env. Without git, every repository's files are scanned as the rest are.
type Reader struct {
	PathEnv string
	Env     []string
}

var _ ports.WorkspaceReader = (*Reader)(nil)

// Mark implements ports.WorkspaceReader: the machine's time, and the HEAD of each repository that has one.
func (r *Reader) Mark(ctx context.Context, workspace string) ports.WorkspaceMark {
	m := ports.WorkspaceMark{At: time.Now(), Heads: map[string]string{}}
	git := r.git()
	if git == "" {
		return m
	}
	for _, rel := range repositories(workspace) {
		out, _, err := procutil.Output(ctx, gitTimeout, r.env(), git, "-C", filepath.Join(workspace, rel), "rev-parse",
			"--verify", "--quiet", "HEAD")
		if head := strings.TrimSpace(out); err == nil && head != "" {
			m.Heads[rel] = head
		}
	}
	return m
}

// Changes implements ports.WorkspaceReader. In a repository, a changed file is one git status reports, untracked ones
// included and ignored ones not, or one a commit changed since the repository's starting point: its HEAD at the mark,
// or, for a repository that appeared since, such as a worktree the attempt created, the oldest commit its HEAD's
// reflog holds. The rest of the workspace is scanned for files modified since the mark, which sees neither a deleted
// file nor one whose modification time was kept.
func (r *Reader) Changes(ctx context.Context, workspace string, mark ports.WorkspaceMark) *domain.Checkpoint {
	var files, root []domain.CheckpointFile
	read := map[string]bool{}
	if git := r.git(); git != "" {
		for _, rel := range repositories(workspace) {
			start, ok := mark.Heads[rel]
			if !ok {
				start = r.createdFrom(ctx, git, filepath.Join(workspace, rel))
			}
			changed, ok := r.repository(ctx, git, filepath.Join(workspace, rel), start)
			if !ok {
				continue // not readable as a repository: the scan covers it
			}
			read[rel] = true
			for _, f := range changed {
				if rel == "." {
					root = append(root, f)
					continue
				}
				f.Path = rel + "/" + f.Path
				files = append(files, f)
			}
		}
	}
	// The root's status reports a repository under it as one folder; that repository's own files stand in for it.
	for _, f := range root {
		if !under(f.Path, read) {
			files = append(files, f)
		}
	}
	if read["."] {
		return domain.NewCheckpoint(files)
	}
	scanned, limited := scan(workspace, mark.At, read)
	c := domain.NewCheckpoint(append(files, scanned...))
	c.Scanned, c.ScanLimited = true, limited
	return c
}

func (r *Reader) git() string {
	found := procutil.LookPath("git", r.PathEnv, []string{"/opt/homebrew/bin/git", "/usr/local/bin/git", "/usr/bin/git"})
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

// env is Env with Git's optional locks off, so reading a repository never takes its index lock from a process the
// attempt left running.
func (r *Reader) env() []string {
	base := r.Env
	if base == nil {
		base = os.Environ()
	}
	return append(append([]string{}, base...), "GIT_OPTIONAL_LOCKS=0")
}

// createdFrom is the oldest commit dir's HEAD reflog holds, the commit a worktree or a clone started at; empty without
// a reflog.
func (r *Reader) createdFrom(ctx context.Context, git, dir string) string {
	out, _, err := procutil.Output(ctx, gitTimeout, r.env(), git, "-C", dir, "reflog", "show", "--format=%H", "HEAD", "--")
	if err != nil {
		return ""
	}
	lines := strings.Fields(out)
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

// repository lists the files changed in the repository at dir since start, with their status relative to start; ok is
// false when git cannot read dir. A start git does not know, or an empty one, leaves only what git status reports.
func (r *Reader) repository(ctx context.Context, git, dir, start string) ([]domain.CheckpointFile, bool) {
	out, _, err := procutil.Output(ctx, gitTimeout, r.env(), git, "-C", dir, "status", "--porcelain=v1", "-z",
		"--untracked-files=all", "--no-renames")
	if err != nil {
		return nil, false
	}
	status := map[string]string{}
	var order []string
	set := func(path, s string) {
		if _, ok := status[path]; !ok {
			order = append(order, path)
		}
		status[path] = s
	}
	if start != "" {
		diff, _, err := procutil.Output(ctx, gitTimeout, r.env(), git, "-C", dir, "diff", "--name-status", "-z",
			"--no-renames", start, "HEAD", "--")
		if err == nil {
			fields := strings.Split(diff, "\x00")
			for i := 0; i+1 < len(fields); i += 2 {
				set(fields[i+1], commitStatus(fields[i]))
			}
		}
	}
	for entry := range strings.SplitSeq(out, "\x00") {
		if len(entry) < 4 {
			continue
		}
		path, s := entry[3:], worktreeStatus(entry[:2])
		switch {
		case s == "":
			continue
		case status[path] == domain.FileAdded && s == domain.FileModified:
			continue // added since the start, whatever changed it after
		case status[path] == domain.FileAdded && s == domain.FileDeleted:
			delete(status, path) // added since the start and gone again
			continue
		}
		set(path, s)
	}
	files := []domain.CheckpointFile{}
	for _, p := range order {
		if s, ok := status[p]; ok {
			files = append(files, domain.CheckpointFile{Path: p, Status: s})
		}
	}
	return files, true
}

// commitStatus reads a status letter of git diff --name-status.
func commitStatus(letter string) string {
	switch letter {
	case "A":
		return domain.FileAdded
	case "D":
		return domain.FileDeleted
	}
	return domain.FileModified
}

// worktreeStatus reads the two status letters of git status --porcelain; empty for a file that changed only to be
// gone again: added to the index, then deleted.
func worktreeStatus(xy string) string {
	switch {
	case xy == "??":
		return domain.FileUntracked
	case xy[0] == 'A' && xy[1] == 'D':
		return ""
	case xy[0] == 'D' || xy[1] == 'D':
		return domain.FileDeleted
	case xy[0] == 'A':
		return domain.FileAdded
	}
	return domain.FileModified
}

// repositories are the Git repositories of the workspace: its root, as ".", and each folder directly under it, each
// when it holds a .git of its own, a folder or a linked worktree's file.
func repositories(workspace string) []string {
	var out []string
	if _, err := os.Lstat(filepath.Join(workspace, ".git")); err == nil {
		out = append(out, ".")
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ".git" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(workspace, e.Name(), ".git")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

// under reports whether path, relative to the workspace, is a repository of repos other than the root, or a path in
// one.
func under(path string, repos map[string]bool) bool {
	first, _, _ := strings.Cut(strings.TrimSuffix(path, "/"), "/")
	return first != "." && repos[first]
}

// scan lists the regular files under workspace modified in the second since started or later, outside every .git and
// the repositories skip holds, visiting domain.CheckpointScanLimit entries at most; limited is set when it stopped
// there. Each is modified, since a scan cannot tell an added file from a changed one. The second is whole because
// some file systems keep modification times to the second, and the time a file system stamps can lag the clock.
func scan(workspace string, started time.Time, skip map[string]bool) (files []domain.CheckpointFile, limited bool) {
	since := started.Truncate(time.Second)
	visited := 0
	_ = filepath.WalkDir(workspace, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == workspace {
			return nil // what cannot be read is not listed
		}
		if visited++; visited > domain.CheckpointScanLimit {
			limited = true
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.Name() == ".git" || d.IsDir() && skip[rel] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil && !info.ModTime().Before(since) {
			files = append(files, domain.CheckpointFile{Path: rel, Status: domain.FileModified})
		}
		return nil
	})
	return files, limited
}
