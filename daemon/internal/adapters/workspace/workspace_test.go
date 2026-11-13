package workspace

import (
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

// hermetic is the environment the tests run git with: no user or system configuration.
var hermetic = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=HarmFlow", "-c",
		"user.email=harmflow@example.invalid"}, args...)...)
	cmd.Env = hermetic
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeOld writes a file whose modification time is an hour before now.
func writeOld(t *testing.T, path string) {
	t.Helper()
	write(t, path, "old\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// repo makes a repository at dir whose one commit holds files.
func repo(t *testing.T, dir string, files ...string) {
	t.Helper()
	git(t, filepath.Dir(dir), "init", "-q", "-b", "main", dir)
	for _, f := range files {
		write(t, filepath.Join(dir, f), f+"\n")
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
}

// A repository directly under a plain workspace, there when the attempt started: what git status reports, untracked
// files included and ignored ones not, and what the attempt's commits changed since its HEAD then. The workspace's own
// files are scanned: one written since the mark is listed, one older is not.
func TestChangesOfARepositoryUnderAPlainWorkspace(t *testing.T) {
	ws := t.TempDir()
	wt := filepath.Join(ws, "repo")
	repo(t, wt, "edited.txt", "gone.txt", "kept.txt")
	write(t, filepath.Join(wt, ".gitignore"), "*.log\n")
	git(t, wt, "add", ".gitignore")
	git(t, wt, "commit", "-q", "-m", "ignore logs")
	writeOld(t, filepath.Join(ws, "old.txt"))
	r := &Reader{Env: hermetic}
	mark := r.Mark(t.Context(), ws)
	if mark.Heads["repo"] == "" || len(mark.Heads) != 1 {
		t.Fatalf("Mark heads = %v, want the one of repo", mark.Heads)
	}

	write(t, filepath.Join(wt, "committed.txt"), "new\n")
	git(t, wt, "add", "committed.txt")
	git(t, wt, "commit", "-q", "-m", "add")
	write(t, filepath.Join(wt, "edited.txt"), "changed\n")
	if err := os.Remove(filepath.Join(wt, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(wt, "dir", "untracked.txt"), "wip\n")
	write(t, filepath.Join(wt, "debug.log"), "ignored\n")
	write(t, filepath.Join(ws, "notes", "shell.txt"), "from a shell\n")

	c := r.Changes(t.Context(), ws, mark)
	want := []domain.CheckpointFile{
		{Path: "notes/shell.txt", Status: domain.FileModified},
		{Path: "repo/committed.txt", Status: domain.FileAdded},
		{Path: "repo/dir/untracked.txt", Status: domain.FileUntracked},
		{Path: "repo/edited.txt", Status: domain.FileModified},
		{Path: "repo/gone.txt", Status: domain.FileDeleted},
	}
	if !slices.Equal(c.Files, want) || !c.Scanned || c.ScanLimited || c.More != 0 {
		t.Errorf("Changes = %+v, want the scanned files %+v", c, want)
	}
}

// A workspace that is itself a repository is read with Git alone, and a repository under it by its own files, not as
// the one untracked folder the root reports.
func TestChangesOfAWorkspaceThatIsARepository(t *testing.T) {
	ws := t.TempDir()
	repo(t, ws, "root.txt")
	r := &Reader{Env: hermetic}
	mark := r.Mark(t.Context(), ws)
	repo(t, filepath.Join(ws, "nested"), "base.txt")
	write(t, filepath.Join(ws, "nested", "wip.txt"), "wip\n")
	write(t, filepath.Join(ws, "root.txt"), "changed\n")

	c := r.Changes(t.Context(), ws, mark)
	// nested appeared after the mark: its starting point is the commit its HEAD started at, so base.txt is not listed.
	want := []domain.CheckpointFile{
		{Path: "nested/wip.txt", Status: domain.FileUntracked},
		{Path: "root.txt", Status: domain.FileModified},
	}
	if !slices.Equal(c.Files, want) || c.Scanned {
		t.Errorf("Changes = %+v, want the files %+v read with Git alone", c, want)
	}
}

// commitAt is the commit HEAD names in the repository at dir.
func commitAt(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	cmd.Env = hermetic
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

// harmflow-app#308: each repository at the workspace's root or directly under it is named with the branch it has
// checked out and its HEAD commit: a worktree on its work branch, one with a detached HEAD, which names no branch, and
// the root. A repository with no commit yet, and a plain folder, are left out.
func TestHeadsOfTheWorkspaceRepositories(t *testing.T) {
	ws := t.TempDir()
	repo(t, ws, "root.txt")
	origin := t.TempDir()
	repo(t, filepath.Join(origin, "origin"), "base.txt")
	git(t, filepath.Join(origin, "origin"), "worktree", "add", "-q", "-b", "fix/5-parser", filepath.Join(ws, "r-5"), "main")
	write(t, filepath.Join(ws, "r-5", "fix.txt"), "fix\n")
	git(t, filepath.Join(ws, "r-5"), "add", "fix.txt")
	git(t, filepath.Join(ws, "r-5"), "commit", "-q", "-m", "fix")
	git(t, filepath.Join(origin, "origin"), "worktree", "add", "-q", "--detach", filepath.Join(ws, "detached"), "main")
	git(t, ws, "init", "-q", "-b", "main", filepath.Join(ws, "empty"))
	write(t, filepath.Join(ws, "notes", "plain.txt"), "plain\n")

	got := (&Reader{Env: hermetic}).Heads(t.Context(), ws)
	want := []ports.RepositoryHead{
		{Path: ".", Branch: "main", Commit: commitAt(t, ws)},
		{Path: "detached", Commit: commitAt(t, filepath.Join(ws, "detached"))},
		{Path: "r-5", Branch: "fix/5-parser", Commit: commitAt(t, filepath.Join(ws, "r-5"))},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Heads = %+v, want %+v", got, want)
	}
	if got[2].Commit == got[1].Commit {
		t.Errorf("Heads gives r-5 the commit %s it started at, not its own", got[2].Commit)
	}
}

// harmflow-app#1028: the daemon's CODEX_HOME names a Codex installation, whose environment reaches no other process, so
// every git the reader runs starts without it, whether it comes in the reader's environment or, with none, in the
// daemon's own.
func TestGitRunsWithoutCodexHome(t *testing.T) {
	for _, c := range []struct {
		name    string
		fromEnv bool
	}{{"the reader's environment", false}, {"the daemon's own environment", true}} {
		t.Run(c.name, func(t *testing.T) {
			bin, envLog, codexHome := t.TempDir(), filepath.Join(t.TempDir(), "env.log"), t.TempDir()
			// A stand-in git that appends its arguments and its CODEX_HOME, "unset" when none, to $ENVLOG and fails, so
			// the reader goes on to each of its other commands.
			write(t, filepath.Join(bin, "git"), "#!/bin/sh\nprintf '%s|%s\\n' \"$*\" \"${CODEX_HOME-unset}\" >> \"$ENVLOG\"\nexit 1\n")
			if err := os.Chmod(filepath.Join(bin, "git"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODEX_HOME", codexHome)
			r := &Reader{PathEnv: bin}
			if c.fromEnv {
				t.Setenv("ENVLOG", envLog)
			} else {
				r.Env = []string{"PATH=/usr/bin:/bin", "ENVLOG=" + envLog, "CODEX_HOME=" + codexHome}
			}
			ws := t.TempDir()
			if err := os.Mkdir(filepath.Join(ws, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}

			mark := r.Mark(t.Context(), ws)
			r.Changes(t.Context(), ws, mark)
			r.Heads(t.Context(), ws)
			data, err := os.ReadFile(envLog)
			if err != nil {
				t.Fatalf("os.ReadFile(%s) = %v, want the runs of git", envLog, err)
			}
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			for _, l := range lines {
				if args, home, _ := strings.Cut(l, "|"); home != "unset" {
					t.Errorf("git %q ran with CODEX_HOME %q, want none", args, home)
				}
			}
			if len(lines) < 3 {
				t.Errorf("git ran %d times, %q; want Mark, Changes, and Heads to run it", len(lines), lines)
			}
		})
	}
}
