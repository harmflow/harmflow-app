package harmflowd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// exists reports whether path is there, failing the test on any error but its absence.
func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

// write creates path with content, and the directories above it.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A module's session gets a directory of its own under the sessions folder, apart from the workspaces, which Remove
// deletes once; the session directories a crash left are removed at the start, and a task's workspace is not.
func TestASessionDirIsCreatedAndRemoved(t *testing.T) {
	home := isolate(t)
	leftover := filepath.Join(home, "sessions", "session-left", "note.txt")
	write(t, leftover, "left by a crash")
	workspace := filepath.Join(home, "workspace", "a-task", "file.txt")
	write(t, workspace, "a task's work")
	var d *Daemon
	startAt(t, home, keep(&d))

	if exists(t, filepath.Dir(leftover)) {
		t.Errorf("after the start, %s is still there, want it removed", filepath.Dir(leftover))
	}
	if !exists(t, workspace) {
		t.Errorf("after the start, %s is gone, want a workspace kept", workspace)
	}
	p := d.Paths()
	if want := filepath.Join(home, "sessions"); p.Sessions != want {
		t.Errorf("Daemon.Paths().Sessions = %q, want %q", p.Sessions, want)
	}
	s, err := d.NewSessionDir()
	if err != nil {
		t.Fatalf("NewSessionDir() = %v", err)
	}
	if filepath.Dir(s.Path()) != p.Sessions {
		t.Errorf("NewSessionDir().Path() = %q, want a directory of %s", s.Path(), p.Sessions)
	}
	info, err := os.Stat(s.Path())
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("os.Stat(%q) = %v, %v, want a directory only the user can read", s.Path(), info, err)
	}
	other, err := d.NewSessionDir()
	if err != nil {
		t.Fatalf("NewSessionDir() a second time = %v", err)
	}
	if other.Path() == s.Path() {
		t.Errorf("two NewSessionDir() gave the same path %q, want one each", s.Path())
	}
	write(t, filepath.Join(s.Path(), "turn.txt"), "a turn")
	if err := s.Remove(); err != nil {
		t.Errorf("SessionDir.Remove() = %v", err)
	}
	if exists(t, s.Path()) {
		t.Errorf("after Remove(), %s is still there", s.Path())
	}
	if err := s.Remove(); err != nil {
		t.Errorf("SessionDir.Remove() a second time = %v, want nil", err)
	}
	if !exists(t, other.Path()) {
		t.Errorf("removing one session directory removed %s, another's", other.Path())
	}
}

// The daemon's stop removes every session directory still open.
func TestTheDaemonsStopRemovesTheOpenSessionDirs(t *testing.T) {
	var path string
	// Registered before start's, this cleanup runs after the daemon has stopped.
	t.Cleanup(func() {
		if path == "" {
			return
		}
		if exists(t, path) {
			t.Errorf("after the daemon stopped, %s is still there, want it removed", path)
		}
	})
	var d *Daemon
	start(t, keep(&d))
	s, err := d.NewSessionDir()
	if err != nil {
		t.Fatalf("NewSessionDir() = %v", err)
	}
	path = s.Path()
}

// A module's agents and skills are copied into the session directory, as the daemon delivers its own built-ins, with
// nothing written to the user's harness configuration.
func TestASessionDirDeliversAModulesAgentsAndSkills(t *testing.T) {
	var d *Daemon
	start(t, keep(&d))
	root := t.TempDir()
	write(t, filepath.Join(root, "agents", "mod-guide.md"), "---\nname: mod-guide\ndescription: Guides the user\n---\nGuide.\n")
	write(t, filepath.Join(root, "skills", "mod-check", "SKILL.md"), "---\nname: mod-check\ndescription: Checks a draft\n---\nCheck.\n")
	write(t, filepath.Join(root, "skills", "mod-check", "scripts", "run.sh"), "echo run\n")
	s, err := d.NewSessionDir()
	if err != nil {
		t.Fatalf("NewSessionDir() = %v", err)
	}
	got, err := s.Deliver(root)
	if err != nil {
		t.Fatalf("Deliver(%q) = %v", root, err)
	}
	want := []DeliveredItem{
		{Name: "mod-guide", Kind: "agent", Path: filepath.Join(s.Path(), "agents", "mod-guide.md"), Description: "Guides the user"},
		{Name: "mod-check", Kind: "skill", Path: filepath.Join(s.Path(), "skills", "mod-check"), Description: "Checks a draft"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Deliver(%q) = %+v, want %+v", root, got, want)
	}
	for _, f := range []string{want[0].Path, filepath.Join(want[1].Path, "SKILL.md"), filepath.Join(want[1].Path, "scripts", "run.sh")} {
		if !exists(t, f) {
			t.Errorf("after Deliver, %s is missing", f)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{".claude", ".codex"} {
		if exists(t, filepath.Join(home, config)) {
			t.Errorf("after Deliver, %s exists in the user's home, want the harness configuration untouched", config)
		}
	}
	if got, err := s.Deliver(t.TempDir()); err != nil || len(got) != 0 {
		t.Errorf("Deliver of a directory with no agent or skill = %v, %v, want nothing delivered", got, err)
	}
}

// With nothing registered, the daemon opens no session directory.
func TestNothingRegisteredOpensNoSessionDir(t *testing.T) {
	r := start(t)
	if path := filepath.Join(r.home, "sessions"); exists(t, path) {
		t.Errorf("with nothing registered, %s exists, want no session directory", path)
	}
}
