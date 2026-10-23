package fsstore

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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

// The crash test's child is a re-run of the same test binary, told by these variables to save one task file and hold
// at a write point until it is killed.
const (
	crashPointEnv   = "HARMFLOW_TEST_CRASH_POINT"
	crashDirEnv     = "HARMFLOW_TEST_CRASH_DIR"
	crashIDEnv      = "HARMFLOW_TEST_CRASH_ID"
	crashContentEnv = "HARMFLOW_TEST_CRASH_CONTENT"

	heldMarker = "held at " // the child prints it, then the point, once it holds
)

func taskVersion(name string) []byte {
	return fmt.Appendf(nil, `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: weekly-notes
  name: %s
  description: %s
spec:
  type: general
  prompt: Summarize the week.
  harness:
    preferred:
      provider: claude-code
`, name, strings.Repeat("A week of notes. ", 100))
}

// FS-001 AC-009: a save killed with SIGKILL leaves the complete old file or the complete new one, on the real file
// system. A child process saves a new version through Tasks.Save and holds at a point of the write until the parent
// kills it there, so the kill never depends on timing. A new Tasks on the same directory then reads what it left.
func TestSaveSurvivesAKill(t *testing.T) {
	if holdMidSave() {
		return
	}
	old, next := taskVersion("Weekly notes"), taskVersion("Weekly notes, renamed")
	for _, c := range []struct {
		point WritePoint
		want  []byte
	}{
		{BeforeRename, old},
		{AfterRename, next},
	} {
		t.Run(string(c.point), func(t *testing.T) {
			dir := t.TempDir()
			repo, err := NewTasks(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.Save("weekly-notes", old, ""); err != nil {
				t.Fatal(err)
			}
			killMidSave(t, dir, "weekly-notes", next, c.point)
			temps, _ := filepath.Glob(filepath.Join(dir, ".weekly-notes.yaml.tmp-*"))
			if c.point == BeforeRename {
				// The kill came after the new version was written and synced: the temporary file holds all of it.
				if len(temps) != 1 {
					t.Fatalf("temporary files %v, want the one the kill left", temps)
				}
				if data, _ := os.ReadFile(temps[0]); !bytes.Equal(data, next) {
					t.Errorf("the temporary file holds %d bytes, want the %d of the new version", len(data), len(next))
				}
			} else if len(temps) != 0 {
				t.Errorf("temporary files %v left after the rename", temps)
			}
			fresh, err := NewTasks(dir)
			if err != nil {
				t.Fatal(err)
			}
			list, err := fresh.List()
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0].ID != "weekly-notes" {
				t.Fatalf("listed %d tasks (%+v), want weekly-notes alone", len(list), list)
			}
			if !bytes.Equal(list[0].Content, c.want) {
				t.Errorf("after a kill %s the task file holds:\n%s\nwant:\n%s", c.point, list[0].Content, c.want)
			}
		})
	}
}

// holdMidSave is the child's part, run when the variables name a write point: it saves the new version and holds at
// that point, printing heldMarker, until it is killed. It returns false in any other process.
func holdMidSave() bool {
	point := WritePoint(os.Getenv(crashPointEnv))
	if point == "" {
		return false
	}
	repo, err := NewTasks(os.Getenv(crashDirEnv))
	if err == nil {
		repo.HoldAt = func(p WritePoint) {
			if p != point {
				return
			}
			fmt.Println(heldMarker + string(p))
			// Held until killed. Should the parent die first, its end of stdin closes and the child exits.
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(2)
		}
		_, err = repo.Save(os.Getenv(crashIDEnv), []byte(os.Getenv(crashContentEnv)), "")
	}
	fmt.Fprintf(os.Stderr, "the save returned without holding at %s: %v\n", point, err)
	os.Exit(3)
	return true
}

// killMidSave runs a child that saves content as task id in dir, waits until it holds at point, and kills it there
// with SIGKILL.
func killMidSave(t *testing.T, dir, id string, content []byte, point WritePoint) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	test, _, _ := strings.Cut(t.Name(), "/")
	cmd := exec.Command(exe, "-test.run=^"+test+"$")
	cmd.Env = append(os.Environ(), crashPointEnv+"="+string(point), crashDirEnv+"="+dir, crashIDEnv+"="+id,
		crashContentEnv+"="+string(content))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	held, drained := make(chan bool, 1), make(chan struct{})
	go func() {
		defer close(drained)
		found := false
		for scan := bufio.NewScanner(stdout); scan.Scan(); {
			if !found && scan.Text() == heldMarker+string(point) {
				found = true
				held <- true
			}
		}
		if !found {
			held <- false
		}
	}()
	select {
	case ok := <-held:
		if !ok {
			<-drained
			err := cmd.Wait()
			t.Fatalf("the child ended (%v) without holding at %s:\n%s", err, point, stderr.String())
		}
	case <-time.After(2 * time.Minute): // only a broken child gets here: the kill itself waits for the hold
		_ = cmd.Process.Kill()
		<-drained
		_ = cmd.Wait()
		t.Fatalf("the child never held at %s:\n%s", point, stderr.String())
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	<-drained
	err = cmd.Wait()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("the child ended with %v, want it killed by SIGKILL:\n%s", err, stderr.String())
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
