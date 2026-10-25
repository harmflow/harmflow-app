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

// Executions is the ExecutionRepository: one JSON record per execution, kept with no time limit.
type Executions struct {
	dir string
}

var _ ports.ExecutionRepository = (*Executions)(nil)

// NewExecutions returns the repository of the execution records in dir, creating the directory when it is missing.
func NewExecutions(dir string) (*Executions, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Executions{dir: dir}, nil
}

// Save implements ports.ExecutionRepository: it writes the record, indented, as <id>.json.
func (e *Executions) Save(x *domain.Execution) error {
	data, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(filepath.Join(e.dir, x.ID+".json"), append(data, '\n'), 0o600)
}

// LoadAll implements ports.ExecutionRepository: every record of the directory, by file name. A file it cannot read
// or decode, or a record with no id, is left out.
func (e *Executions) LoadAll() ([]*domain.Execution, error) {
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return nil, err
	}
	var out []*domain.Execution
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".json" || strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(e.dir, ent.Name()))
		if err != nil {
			continue
		}
		var x domain.Execution
		if err := json.Unmarshal(data, &x); err != nil || x.ID == "" {
			continue
		}
		out = append(out, &x)
	}
	return out, nil
}
