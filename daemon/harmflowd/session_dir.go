package harmflowd

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/catalog"
)

// SessionDir is the working directory of one session a registration holds outside any task: a directory of its own
// under Paths.Sessions, which is no workspace. The daemon removes it when the registration calls Remove, and when the
// daemon stops; one left by a crash is removed at the next start.
type SessionDir struct {
	path   string
	owner  *sessionDirs
	remove sync.Once
	err    error
}

// DeliveredItem is an agent or a skill that Deliver copied into a session directory.
type DeliveredItem struct {
	// Name is the agent's or the skill's name: its file name without .md, or its folder's name.
	Name string
	// Kind is "agent" or "skill".
	Kind string
	// Path is the copy: the agent's Markdown file, or the skill's folder, which holds its SKILL.md.
	Path string
	// Description is the first line of the description its front matter declares.
	Description string
}

// NewSessionDir creates a directory for one session, only the user can read, under Paths.Sessions.
func (d *Daemon) NewSessionDir() (*SessionDir, error) {
	return d.sessions.create()
}

// Path is the session directory's absolute path.
func (s *SessionDir) Path() string { return s.path }

// Remove deletes the session directory and all it holds, once: a second call, or the daemon's stop after it, returns
// what the first did.
func (s *SessionDir) Remove() error {
	s.remove.Do(func() {
		s.err = os.RemoveAll(s.path)
		s.owner.forget(s)
	})
	return s.err
}

// Deliver copies the agents (agents/*.md) and the skills (skills/*/SKILL.md) under root, a directory a module ships,
// into the session directory's agents/ and skills/, as the daemon copies its own built-ins for an execution: copies
// the session reads, so a change to root meanwhile does not reach it, and nothing written to the user's harness
// configuration. It returns what it delivered, sorted by kind then name; a root that holds none delivers nothing. It
// only stages the copies: handing them to a harness is the session's part.
func (s *SessionDir) Deliver(root string) ([]DeliveredItem, error) {
	src := catalog.Dir{Root: root}
	var out []DeliveredItem
	for _, kind := range []string{"agent", "skill"} {
		for _, it := range src.Items(kind) {
			dst := filepath.Join(s.path, "skills", it.Name)
			if kind == "agent" {
				dst = filepath.Join(s.path, "agents", it.Name+".md")
			}
			if err := catalog.CopyTree(it.Path, dst); err != nil {
				return out, fmt.Errorf("delivering %s %s: %w", kind, it.Name, err)
			}
			out = append(out, DeliveredItem{Name: it.Name, Kind: kind, Path: dst, Description: it.Description})
		}
	}
	return out, nil
}

// sessionsDir is Paths.Sessions of the installation whose data directory is home: a folder of its own, beside the
// workspaces rather than in them.
func sessionsDir(home string) string { return filepath.Join(home, "sessions") }

// sessionDirs are the session directories open under root.
type sessionDirs struct {
	root string
	mu   sync.Mutex
	open map[*SessionDir]bool
}

func (o *sessionDirs) create() (*SessionDir, error) {
	if err := os.MkdirAll(o.root, 0o700); err != nil {
		return nil, err
	}
	path, err := os.MkdirTemp(o.root, "session-")
	if err != nil {
		return nil, err
	}
	s := &SessionDir{path: path, owner: o}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.open == nil {
		o.open = map[*SessionDir]bool{}
	}
	o.open[s] = true
	return s, nil
}

func (o *sessionDirs) forget(s *SessionDir) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.open, s)
}

// removeAll removes every session directory still open, as the daemon stops, and logs what it cannot remove: the next
// start removes it.
func (o *sessionDirs) removeAll(logger *log.Logger) {
	o.mu.Lock()
	var open []*SessionDir
	for s := range o.open {
		open = append(open, s)
	}
	o.mu.Unlock()
	var errs []error
	for _, s := range open {
		errs = append(errs, s.Remove())
	}
	if err := errors.Join(errs...); err != nil {
		logger.Printf("removing the session directories as the daemon stops: %v", err)
	}
}
