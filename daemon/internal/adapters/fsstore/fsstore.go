// Package fsstore keeps task YAML, operational JSON state, and execution records on the file system, with atomic
// writes: a temporary file in the same directory, synced, then renamed over the target (ADR-007, FS-001 FR-007).
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

// WriteAtomic replaces path with data so a crash leaves either the complete old file or the complete new one.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
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
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
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
}

func NewTasks(dir string) (*Tasks, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Tasks{dir: dir}, nil
}

func (t *Tasks) Dir() string { return t.dir }

func (t *Tasks) path(id string) string { return filepath.Join(t.dir, id+".yaml") }

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

func (t *Tasks) Get(id string) (ports.TaskFile, error) {
	f, err := t.read(t.path(id), id)
	if errors.Is(err, ports.ErrNotFound) {
		if g, err2 := t.read(filepath.Join(t.dir, id+".yml"), id); err2 == nil {
			return g, nil
		}
	}
	return f, err
}

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
	if err := WriteAtomic(path, content, 0o600); err != nil {
		return ports.TaskFile{}, err
	}
	return t.read(path, id)
}

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

func NewState(dir string) (*State, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &State{dir: dir}, nil
}

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

func (s *State) Save(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return WriteAtomic(filepath.Join(s.dir, name+".json"), append(data, '\n'), 0o600)
}

// Executions is the ExecutionRepository: one JSON record per execution, kept with no time limit (D-073).
type Executions struct {
	dir string
}

func NewExecutions(dir string) (*Executions, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Executions{dir: dir}, nil
}

func (e *Executions) Save(x *domain.Execution) error {
	data, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(filepath.Join(e.dir, x.ID+".json"), append(data, '\n'), 0o600)
}

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
