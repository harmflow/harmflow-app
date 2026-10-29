// Package fsstore keeps task YAML, operational JSON state, and execution records on the file system, with atomic
// writes: a temporary file in the same directory, synced, then renamed over the target.
package fsstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// WritePoint names a step of WriteAtomic where a crash test holds a write, to kill the process there.
type WritePoint string

const (
	// BeforeRename: the temporary file is written, synced, and closed; the target still holds its old content.
	BeforeRename WritePoint = "before_rename"
	// AfterRename: the temporary file has just replaced the target.
	AfterRename WritePoint = "after_rename"
)

// WriteAtomic replaces path with data so a crash leaves either the complete old file or the complete new one.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	return writeAtomic(path, data, perm, nil)
}

// writeAtomic is WriteAtomic, calling holdAt at each WritePoint when it is set.
func writeAtomic(path string, data []byte, perm os.FileMode, holdAt func(WritePoint)) error {
	hold := func(p WritePoint) {
		if holdAt != nil {
			holdAt(p)
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// On failure the temporary file is closed and removed as far as that goes: the error returned is the one that
	// matters, and a temporary file left behind is never read.
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		cleanup()
		return err
	}
	hold(BeforeRename)
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	hold(AfterRename)
	// Syncing the directory makes the rename durable where the file system supports it; the file is in place whatever
	// the directory sync says.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Revision identifies a file's content; an edit names the revision it started from.
func Revision(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:8])
}

// Tasks is the TaskRepository: one <id>.yaml per task in one directory.
type Tasks struct {
	dir string
	mu  sync.Mutex
	// HoldAt, when set, is called at each WritePoint of a save. Only a crash test sets it, in a child process that it then
	// kills; the daemon leaves it nil.
	HoldAt func(WritePoint)
}

var _ ports.TaskRepository = (*Tasks)(nil)

// NewTasks returns the repository of the task files in dir, creating the directory when it is missing.
func NewTasks(dir string) (*Tasks, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Tasks{dir: dir}, nil
}

// Dir implements ports.TaskRepository: the directory of the task files.
func (t *Tasks) Dir() string { return t.dir }

func (t *Tasks) path(id string) string { return filepath.Join(t.dir, id+".yaml") }

// List implements ports.TaskRepository: every .yaml or .yml file of the directory, sorted by id. A hidden file, a
// directory, and a file it cannot read are left out.
func (t *Tasks) List() ([]ports.TaskFile, error) {
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return nil, err
	}
	var out []ports.TaskFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		ext := filepath.Ext(name)
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		f, err := t.read(filepath.Join(t.dir, name), strings.TrimSuffix(name, ext))
		if err != nil {
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (t *Tasks) read(path, id string) (ports.TaskFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ports.TaskFile{}, ports.ErrNotFound
		}
		return ports.TaskFile{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return ports.TaskFile{}, err
	}
	return ports.TaskFile{ID: id, Path: path, Content: content, Revision: Revision(content), ModTime: info.ModTime()}, nil
}

// Get implements ports.TaskRepository: the task's <id>.yaml, or its <id>.yml when there is no .yaml; ErrNotFound when
// it has neither.
func (t *Tasks) Get(id string) (ports.TaskFile, error) {
	f, err := t.read(t.path(id), id)
	if errors.Is(err, ports.ErrNotFound) {
		if g, err2 := t.read(filepath.Join(t.dir, id+".yml"), id); err2 == nil {
			return g, nil
		}
	}
	return f, err
}

// Save implements ports.TaskRepository, writing over the task's existing file, whatever its extension, or a new
// <id>.yaml. An expected revision of a task that does not exist is ErrNotFound; one that does not match is ErrConflict,
// returned with the file as it is.
func (t *Tasks) Save(id string, content []byte, expectedRevision string) (ports.TaskFile, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	path := t.path(id)
	if cur, err := t.Get(id); err == nil {
		path = cur.Path
		if expectedRevision != "" && cur.Revision != expectedRevision {
			return cur, ports.ErrConflict
		}
	} else if expectedRevision != "" && errors.Is(err, ports.ErrNotFound) {
		return ports.TaskFile{}, ports.ErrNotFound
	}
	if err := writeAtomic(path, content, 0o600, t.HoldAt); err != nil {
		return ports.TaskFile{}, err
	}
	return t.read(path, id)
}

// Delete implements ports.TaskRepository: it removes the task's file, ErrNotFound when it has none.
func (t *Tasks) Delete(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	f, err := t.Get(id)
	if err != nil {
		return err
	}
	return os.Remove(f.Path)
}

// State is the StateStore: one JSON document per name in the state directory.
type State struct {
	dir string
	mu  sync.Mutex
}

var _ ports.StateStore = (*State)(nil)

// NewState returns the store of the JSON documents in dir, creating the directory when it is missing.
func NewState(dir string) (*State, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &State{dir: dir}, nil
}

// Load implements ports.StateStore: it decodes <name>.json into v and reports whether the document exists.
func (s *State) Load(name string, v any) (bool, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, name+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("state %s: %w", name, err)
	}
	return true, nil
}

// Save implements ports.StateStore: it writes v, indented, as <name>.json.
func (s *State) Save(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return WriteAtomic(filepath.Join(s.dir, name+".json"), append(data, '\n'), 0o600)
}

// Executions is the ExecutionRepository: one JSON record per execution, kept with no time limit, and beside them the
// index, IndexFile, which holds the summary of each record with the size and modification time of the file it was read
// from. The records are the source of truth: the index is derived from them, and rebuilt from them where it is
// missing, unreadable, or behind them.
type Executions struct {
	dir string

	mu sync.Mutex
	// index is the summaries Summaries read or rebuilt, by execution id; nil until then. A save made before leaves the
	// index file behind its record, which the next Summaries catches up with.
	index map[string]indexEntry
	// holds counts the HoldIndex calls not yet released. While one is, a save that would write the index file sets
	// pending instead, and the last release writes it.
	holds   int
	pending bool
}

var _ ports.ExecutionRepository = (*Executions)(nil)

// IndexFile is the name of the index of the execution records, in their directory; no execution id is "index".
const IndexFile = "index.json"

// indexVersion is the schemaVersion of the index; an index at any other is rebuilt.
const indexVersion = "1"

type indexDocument struct {
	SchemaVersion string       `json:"schemaVersion"`
	Executions    []indexEntry `json:"executions"`
}

// indexEntry is a record's summary, with the size and modification time of the file it summarizes: a file that no
// longer has both is read again.
type indexEntry struct {
	Size    int64                    `json:"size"`
	ModTime time.Time                `json:"modTime"`
	Summary *domain.ExecutionSummary `json:"summary"`
}

// NewExecutions returns the repository of the execution records in dir, creating the directory when it is missing.
func NewExecutions(dir string) (*Executions, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Executions{dir: dir}, nil
}

func (e *Executions) path(id string) string { return filepath.Join(e.dir, id+".json") }

// Save implements ports.ExecutionRepository: it writes the record, indented, as <id>.json, then its summary into the
// index. The index file is written when the record is of an execution that has ended, or by the release of a
// HoldIndex under way: the daemon reads every other record whole at start, and finds a record the index is behind by
// its size and modification time.
func (e *Executions) Save(x *domain.Execution) error {
	data, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		return err
	}
	path := e.path(x.ID)
	if err := WriteAtomic(path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.index == nil {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		// The record is saved; without its size and time, its entry would only mislead, and Summaries reads it again.
		delete(e.index, x.ID)
		return nil
	}
	e.index[x.ID] = indexEntry{Size: info.Size(), ModTime: info.ModTime(), Summary: x.Summarize()}
	if x.Status.IsTerminal() {
		if e.holds > 0 {
			e.pending = true
		} else {
			e.writeIndexLocked()
		}
	}
	return nil
}

// HoldIndex implements ports.ExecutionRepository. A release left uncalled, as a stop in the middle of a pass leaves,
// loses no history: the next start finds the index behind the records and catches up.
func (e *Executions) HoldIndex() (release func()) {
	e.mu.Lock()
	e.holds++
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.holds--; e.holds == 0 && e.pending {
			e.pending = false
			e.writeIndexLocked()
		}
	}
}

// Load implements ports.ExecutionRepository.
func (e *Executions) Load(id string) (*domain.Execution, error) {
	if id == "" || filepath.Base(id) != id || id+".json" == IndexFile {
		return nil, ports.ErrNotFound
	}
	return readExecution(e.path(id))
}

func readExecution(path string) (*domain.Execution, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var x domain.Execution
	if err := json.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("execution record %s: %w", filepath.Base(path), err)
	}
	if x.ID == "" {
		return nil, fmt.Errorf("execution record %s has no id", filepath.Base(path))
	}
	return &x, nil
}

// Summaries implements ports.ExecutionRepository, sorted by execution id. It reads the index, then lists the records:
// one the index has with the same size and modification time is summarized from the index, and only the others are
// read and decoded. A file it cannot read or decode, or a record with no id, is left out. When the index was missing,
// unreadable, or behind the records, it is written again.
func (e *Executions) Summaries() ([]*domain.ExecutionSummary, error) {
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	old, ok := e.readIndexLocked()
	stale := !ok
	index := map[string]indexEntry{}
	reused := 0
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || filepath.Ext(name) != ".json" || strings.HasPrefix(name, ".") || name == IndexFile {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if en, ok := old[id]; ok && en.Summary != nil && en.Size == info.Size() && en.ModTime.Equal(info.ModTime()) {
			index[id] = en
			reused++
			continue
		}
		stale = true
		x, err := readExecution(filepath.Join(e.dir, name))
		if err != nil {
			continue
		}
		index[id] = indexEntry{Size: info.Size(), ModTime: info.ModTime(), Summary: x.Summarize()}
	}
	if reused != len(old) { // a record the index holds is gone
		stale = true
	}
	e.index = index
	if stale {
		e.writeIndexLocked()
	}
	var out []*domain.ExecutionSummary
	for _, id := range sortedIDs(index) {
		out = append(out, index[id].Summary)
	}
	return out, nil
}

// readIndexLocked returns the index file's entries by execution id, and false when the file is missing, unreadable, or
// of another version, which leaves every record to be read.
func (e *Executions) readIndexLocked() (map[string]indexEntry, bool) {
	data, err := os.ReadFile(filepath.Join(e.dir, IndexFile))
	if err != nil {
		return nil, false
	}
	var doc indexDocument
	if err := json.Unmarshal(data, &doc); err != nil || doc.SchemaVersion != indexVersion {
		return nil, false
	}
	m := map[string]indexEntry{}
	for _, en := range doc.Executions {
		if en.Summary != nil && en.Summary.ID != "" {
			m[en.Summary.ID] = en
		}
	}
	return m, true
}

// writeIndexLocked writes the index atomically. A failure is left unsaid: the records are saved, and the next start
// finds the index behind them and catches up.
func (e *Executions) writeIndexLocked() {
	doc := indexDocument{SchemaVersion: indexVersion, Executions: []indexEntry{}}
	for _, id := range sortedIDs(e.index) {
		doc.Executions = append(doc.Executions, e.index[id])
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return
	}
	_ = WriteAtomic(filepath.Join(e.dir, IndexFile), append(data, '\n'), 0o600)
}

func sortedIDs(m map[string]indexEntry) []string {
	var ids []string
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
