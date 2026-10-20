package fsstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

func TestTasksRevisionConflict(t *testing.T) {
	repo, err := NewTasks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f, err := repo.Save("a", []byte("one"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Save("a", []byte("two"), f.Revision); err != nil {
		t.Fatalf("save from the current revision: %v", err)
	}
	// An edit that started from the first revision must not overwrite the second (FS-001 FR-005).
	if _, err := repo.Save("a", []byte("three"), f.Revision); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("got %v, want a conflict", err)
	}
	got, _ := repo.Get("a")
	if string(got.Content) != "two" {
		t.Errorf("content %q", got.Content)
	}
	info, _ := os.Stat(got.Path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("task file mode %v", info.Mode().Perm())
	}
}

// FS-001 AC-009: an interrupted write leaves the complete old file; temporary files are not listed as tasks.
func TestAtomicWriteLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	repo, _ := NewTasks(dir)
	_, _ = repo.Save("a", []byte("old"), "")
	_ = os.WriteFile(filepath.Join(dir, ".a.yaml.tmp-123"), []byte("half"), 0o600)
	list, _ := repo.List()
	if len(list) != 1 || string(list[0].Content) != "old" {
		t.Fatalf("list: %+v", list)
	}
}

func TestStateRoundTrip(t *testing.T) {
	s, _ := NewState(t.TempDir())
	if ok, err := s.Load("missing", &struct{}{}); ok || err != nil {
		t.Fatal("a missing document must load as absent")
	}
	in := map[string]int{"x": 1}
	if err := s.Save("doc", in); err != nil {
		t.Fatal(err)
	}
	var out map[string]int
	if ok, err := s.Load("doc", &out); !ok || err != nil || out["x"] != 1 {
		t.Fatalf("got %v %v %v", out, ok, err)
	}
}
