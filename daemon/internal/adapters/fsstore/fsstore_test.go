package fsstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
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
	// An edit that started from the first revision must not overwrite the second.
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

// An interrupted write leaves the complete old file; temporary files are not listed as tasks.
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

// A save killed with SIGKILL leaves the complete old file or the complete new one, on the real file system. A child
// process saves a new version through Tasks.Save and holds at a point of the write until the parent kills it there, so
// the kill never depends on timing. A new Tasks on the same directory then reads what it left.
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

// endedExecution is the record of an execution of task that ended with status at created plus a minute.
func endedExecution(task string, status domain.ExecutionStatus, created time.Time) *domain.Execution {
	x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: task}}, domain.TriggerManual, nil,
		created)
	ended := created.Add(time.Minute)
	x.Status, x.EndedAt, x.Summary = status, &ended, "Done for "+task
	return x
}

// summaryIDs lists the ids and statuses Summaries returns, in its order.
func summaryIDs(t *testing.T, repo *Executions) []string {
	t.Helper()
	list, err := repo.Summaries()
	if err != nil {
		t.Fatalf("Summaries() = %v", err)
	}
	var out []string
	for _, s := range list {
		out = append(out, s.ID+" "+string(s.Status))
	}
	return out
}

// The index is derived from the records: a repository that never wrote it, or whose index was deleted or garbled,
// rebuilds it from them, and the next repository summarizes the records from the index alone.
func TestExecutionIndexRebuiltWhenMissing(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewExecutions(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a, b := endedExecution("one", domain.StatusCompleted, now), endedExecution("two", domain.StatusFailed, now)
	for _, x := range []*domain.Execution{a, b} {
		if err := repo.Save(x); err != nil {
			t.Fatal(err)
		}
	}
	index := filepath.Join(dir, IndexFile)
	if _, err := os.Stat(index); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an index was written before Summaries read the records: %v", err)
	}
	want := summaryIDs(t, repo)
	if len(want) != 2 {
		t.Fatalf("Summaries() = %v, want both records", want)
	}
	for name, damage := range map[string]func() error{
		"missing":    func() error { return os.Remove(index) },
		"unreadable": func() error { return os.WriteFile(index, []byte("{not json"), 0o600) },
		"of another version": func() error {
			return os.WriteFile(index, []byte(`{"schemaVersion":"0","executions":[]}`), 0o600)
		},
	} {
		if err := damage(); err != nil {
			t.Fatal(err)
		}
		fresh, _ := NewExecutions(dir)
		if got := summaryIDs(t, fresh); !slices.Equal(got, want) {
			t.Errorf("with the index %s, Summaries() = %v, want %v", name, got, want)
		}
		if _, err := os.Stat(index); err != nil {
			t.Errorf("with the index %s, it was not written again: %v", name, err)
		}
	}

	// With a current index, a record is read from it, not from its file: a record replaced by an unreadable file of the
	// same size and time still reads as the index has it.
	path := filepath.Join(dir, a.ID+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), int(info.Size())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	fresh, _ := NewExecutions(dir)
	if got := summaryIDs(t, fresh); !slices.Equal(got, want) {
		t.Errorf("with a current index, Summaries() = %v, want %v from the index", got, want)
	}
}

// An index behind the records — a record added, changed, or removed by a save it did not see — is caught up from
// the records themselves, found by their size and modification time.
func TestExecutionIndexCatchesUpWithTheRecords(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewExecutions(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a, b := endedExecution("one", domain.StatusCompleted, now), endedExecution("two", domain.StatusCompleted, now)
	for _, x := range []*domain.Execution{a, b} {
		if err := repo.Save(x); err != nil {
			t.Fatal(err)
		}
	}
	summaryIDs(t, repo) // writes the index

	// Another repository saves behind this one's back, as a daemon that stopped before writing its index would leave.
	behind, _ := NewExecutions(dir)
	c := endedExecution("three", domain.StatusNoWork, now)
	b.Status = domain.StatusNeedsAttention
	for _, x := range []*domain.Execution{b, c} {
		if err := behind.Save(x); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(dir, a.ID+".json")); err != nil {
		t.Fatal(err)
	}
	want := []string{b.ID + " needs_attention", c.ID + " no_work"}
	slices.Sort(want)
	fresh, _ := NewExecutions(dir)
	if got := summaryIDs(t, fresh); !slices.Equal(got, want) {
		t.Errorf("Summaries() behind the records = %v, want %v", got, want)
	}
	// What it caught up with is in the index now.
	data, err := os.ReadFile(filepath.Join(dir, IndexFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc indexDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var indexed []string
	for _, en := range doc.Executions {
		indexed = append(indexed, en.Summary.ID+" "+string(en.Summary.Status))
	}
	if !slices.Equal(indexed, want) {
		t.Errorf("the index holds %v, want %v", indexed, want)
	}
}

// While the index is held, a save of an execution that ended leaves the index file as it is, and the last release
// writes it once, with every save made meanwhile. A release that follows no save writes nothing.
func TestExecutionIndexHeldUntilReleased(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewExecutions(dir)
	if err != nil {
		t.Fatal(err)
	}
	summaryIDs(t, repo) // writes the empty index
	index := filepath.Join(dir, IndexFile)
	indexed := func() []string {
		t.Helper()
		data, err := os.ReadFile(index)
		if err != nil {
			t.Fatal(err)
		}
		var doc indexDocument
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, en := range doc.Executions {
			ids = append(ids, en.Summary.ID)
		}
		return ids
	}
	var want []string
	save := func(task string) {
		t.Helper()
		x := endedExecution(task, domain.StatusCompleted, time.Now())
		if err := repo.Save(x); err != nil {
			t.Fatal(err)
		}
		want = append(want, x.ID)
		slices.Sort(want)
	}
	outer, inner := repo.HoldIndex(), repo.HoldIndex()
	save("one")
	save("two")
	if got := indexed(); len(got) != 0 {
		t.Fatalf("while held, the index file holds %v, want it as it was", got)
	}
	inner()
	if got := indexed(); len(got) != 0 {
		t.Fatalf("with a hold left, the index file holds %v, want it as it was", got)
	}
	outer()
	if got := indexed(); !slices.Equal(got, want) {
		t.Errorf("after the last release, the index file holds %v, want %v", got, want)
	}
	save("three")
	if got := indexed(); !slices.Equal(got, want) {
		t.Errorf("after the release, a save left the index file holding %v, want %v", got, want)
	}

	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	repo.HoldIndex()()
	if _, err := os.Stat(index); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a release that followed no save wrote the index: %v", err)
	}
}

// A save keeps the index current for an execution that ended, and Load reads the full record.
func TestExecutionSaveKeepsTheIndex(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewExecutions(dir)
	if err != nil {
		t.Fatal(err)
	}
	summaryIDs(t, repo)
	x := endedExecution("one", domain.StatusCompleted, time.Now())
	x.LastOperation = "opened draft PR o/r#2"
	if err := repo.Save(x); err != nil {
		t.Fatal(err)
	}
	// A record whose file no longer reads would be read again; this one is read from the index.
	path := filepath.Join(dir, x.ID+".json")
	info, _ := os.Stat(path)
	_ = os.WriteFile(path, bytes.Repeat([]byte(" "), int(info.Size())), 0o600)
	_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	fresh, _ := NewExecutions(dir)
	list, err := fresh.Summaries()
	if err != nil || len(list) != 1 || list[0].LastOperation != x.LastOperation || list[0].Summary != x.Summary {
		t.Fatalf("Summaries() = %+v, %v; want the saved record's summary", list, err)
	}

	y := endedExecution("two", domain.StatusFailed, time.Now())
	if err := repo.Save(y); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Load(y.ID)
	if err != nil || got.ID != y.ID || got.Status != y.Status || got.Summary != y.Summary {
		t.Errorf("Load(%q) = %+v, %v; want the saved record", y.ID, got, err)
	}
	for _, id := range []string{"exec-missing", "index", "../" + y.ID, ""} {
		if _, err := repo.Load(id); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("Load(%q) = %v, want ErrNotFound", id, err)
		}
	}
}
