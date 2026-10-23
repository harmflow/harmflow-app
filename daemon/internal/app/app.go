// Package app holds the daemon's use cases: tasks, scheduling, executions, failover, recovery, notifications, and
// settings. It orchestrates only; what a task does lives in its prompt, agents, and skills (ADR-013).
package app

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// Error is a use-case failure the API turns into a response; its Text is what the user reads.
type Error struct {
	Code string
	domain.Text
	Validation      *ValidationResult
	CurrentRevision string
}

func (e *Error) Error() string { return e.Message }

const (
	CodeBadRequest        = "bad_request"
	CodeNotFound          = "not_found"
	CodeConflict          = "conflict"
	CodeValidationFailed  = "validation_failed"
	CodeTaskDisabled      = "task_disabled"
	CodeExecutionsRunning = "executions_running"
)

func newError(code string, text domain.Text) *Error {
	return &Error{Code: code, Text: text}
}

// Paths are the directories of one installation (architecture/configuration-layout.md).
type Paths struct {
	Home        string
	Tasks       string
	State       string
	Executions  string
	Logs        string
	Workspace   string
	Runs        string
	Run         string
	Builtins    string
	RuntimeFile string
	UserHome    string
}

func NewPaths(home, userHome, builtins string) Paths {
	return Paths{
		Home:        home,
		Tasks:       filepath.Join(home, "tasks"),
		State:       filepath.Join(home, "state"),
		Executions:  filepath.Join(home, "state", "executions"),
		Logs:        filepath.Join(home, "logs"),
		Workspace:   filepath.Join(home, "workspace"),
		Runs:        filepath.Join(home, "runs"),
		Run:         filepath.Join(home, "run"),
		Builtins:    builtins,
		RuntimeFile: filepath.Join(home, "run", "daemon.json"),
		UserHome:    userHome,
	}
}

type Deps struct {
	Paths      Paths
	Version    string
	Commit     string    // the short commit it was built from, "-dirty" from uncommitted changes; empty without one
	BuiltAt    time.Time // when it was built; zero without a build time
	Clock      ports.Clock
	Wake       ports.WakeMonitor
	Location   *time.Location
	Tasks      ports.TaskRepository
	State      ports.StateStore
	Executions ports.ExecutionRepository
	Events     ports.EventLog
	Validator  *schema.Validator
	Harnesses  []ports.HarnessAdapter
	Service    ports.HostServiceManager
	System     ports.SystemInfo
	Tools      ports.ToolProber
	// HarnessEnv is the environment harness processes, and script tasks' programs, start with.
	HarnessEnv []string
	// OS is this machine's operating system, as a script task names it (FS-013 FR-014); the running one when empty.
	OS string
	// TickInterval is how often the scheduler looks for due firings.
	TickInterval time.Duration
	// MisfireAfter is how late a firing may be before it counts as missed though no unavailability was seen: the clock
	// jumped, or the daemon could not run.
	MisfireAfter time.Duration
	// DaemonExecutable and DaemonArgs are what autostart launches.
	DaemonExecutable string
	DaemonArgs       []string
	DaemonEnv        map[string]string
	Logger           *log.Logger
}

type taskEntry struct {
	file          ports.TaskFile
	doc           domain.TaskDocument // the last valid definition
	invalid       *ValidationResult   // the file on disk fails validation
	warnings      []domain.Warning
	deletePending bool
	selfRevision  string // the revision the daemon itself last wrote
}

type runHandle struct {
	cancel          context.CancelFunc
	cancelRequested bool
	done            chan struct{}
}

type App struct {
	d         Deps
	log       *log.Logger
	startedAt time.Time

	mu            sync.Mutex
	tasks         map[string]*taskEntry
	executions    map[string]*domain.Execution
	running       map[string]*runHandle // by task id
	inventory     map[string]ports.HarnessInfo
	manualPaths   map[string]string
	settings      Settings
	sched         map[string]*scheduleState
	firings       map[string][]firingRecord // by task id
	ledger        map[string]map[string]*domain.WorkItem
	notifications []Notification
	accepting     bool
	stopping      bool
	adapters      map[string]ports.HarnessAdapter
	// unavailableUntil is when the daemon last became available again, at its start or when the machine woke: a firing
	// scheduled before it and still due came due while the daemon or the machine was down, and is a misfire however
	// late it is.
	unavailableUntil time.Time

	// tickMu runs one Tick at a time, whole.
	tickMu sync.Mutex
	// retentionMu runs one retention pass at a time, whole.
	retentionMu sync.Mutex
	// saveMu orders the saves of execution records, so the last copy taken is the last written.
	saveMu sync.Mutex

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
	// ShutdownRequested is closed when the API asks the daemon to stop.
	ShutdownRequested chan struct{}
	shutdownOnce      sync.Once
}

func New(d Deps) (*App, error) {
	if d.Clock == nil {
		d.Clock = realClock{}
	}
	if d.Wake == nil {
		d.Wake = noWake{}
	}
	if d.Location == nil {
		d.Location = time.Local
	}
	if d.TickInterval == 0 {
		d.TickInterval = 5 * time.Second
	}
	if d.MisfireAfter == 0 {
		d.MisfireAfter = 2 * time.Minute
	}
	if d.OS == "" {
		d.OS = currentOS()
	}
	if d.Logger == nil {
		d.Logger = log.New(os.Stderr, "harmflowd ", log.LstdFlags)
	}
	for _, dir := range []string{d.Paths.Workspace, d.Paths.Runs, d.Paths.Run, d.Paths.Logs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	a := &App{
		d: d, log: d.Logger, startedAt: d.Clock.Now(),
		tasks: map[string]*taskEntry{}, executions: map[string]*domain.Execution{}, running: map[string]*runHandle{},
		inventory: map[string]ports.HarnessInfo{}, manualPaths: map[string]string{}, sched: map[string]*scheduleState{},
		firings: map[string][]firingRecord{}, ledger: map[string]map[string]*domain.WorkItem{}, accepting: true,
		adapters: map[string]ports.HarnessAdapter{}, stopCh: make(chan struct{}), ShutdownRequested: make(chan struct{}),
	}
	for _, h := range d.Harnesses {
		a.adapters[h.ID()] = h
	}
	return a, nil
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// noWake sees no suspension; a firing missed while the machine slept then counts as missed only by its lateness.
type noWake struct{}

func (noWake) LastWake() time.Time { return time.Time{} }

func (a *App) now() time.Time { return a.d.Clock.Now() }

// Start loads state, recovers interrupted executions, detects harnesses, and starts the scheduler, the task-file
// watcher, and maintenance.
func (a *App) Start(ctx context.Context) error {
	if err := a.loadSettings(); err != nil {
		return err
	}
	a.loadNotifications()
	a.loadLedger()
	_, _ = a.d.State.Load("harnesses", &a.manualPaths)
	if err := a.loadTasks(); err != nil {
		return err
	}
	a.loadSchedule()
	if err := a.recover(); err != nil {
		return err
	}
	a.refreshHarnessesAsync()
	a.emit(domain.Event{Type: domain.EventDaemonStarted, Payload: map[string]any{"version": a.d.Version, "pid": os.Getpid()}}.Says(
		domain.T("daemon_started", "HarmFlow daemon {{version}} started", "version", a.d.Version)))
	a.mu.Lock()
	a.unavailableUntil = a.now() // the daemon was down until now (D-080)
	a.mu.Unlock()
	a.wg.Add(3)
	go a.schedulerLoop()
	go a.watchLoop()
	go a.maintenanceLoop()
	a.startQueued()
	return nil
}

// Stop ends the loops and interrupts running executions: a stopped daemon never continues them (D-089).
func (a *App) Stop(reason domain.Text) {
	a.stopOnce.Do(func() {
		a.mu.Lock()
		a.accepting = false
		a.stopping = true
		var cancels []context.CancelFunc
		var dones []chan struct{}
		for _, h := range a.running {
			if h.cancel != nil {
				cancels = append(cancels, h.cancel)
			}
			dones = append(dones, h.done)
		}
		a.mu.Unlock()
		a.emit(domain.Event{Type: domain.EventDaemonStopping}.Says(reason))
		close(a.stopCh)
		for _, cancel := range cancels {
			cancel()
		}
		for _, done := range dones {
			select {
			case <-done:
			case <-time.After(45 * time.Second):
			}
		}
		a.wg.Wait()
	})
}

// RequestShutdown asks main to stop the daemon; with running executions it needs force (FS-010 FR-008).
func (a *App) RequestShutdown(force bool) error {
	a.mu.Lock()
	n := 0
	for _, x := range a.executions {
		if x.Status.IsActive() {
			n++
		}
	}
	if n > 0 && !force {
		a.mu.Unlock()
		return newError(CodeExecutionsRunning, domain.T("executions_running",
			"{{count}} executions are running; confirm to stop the daemon and cancel them", "count", strconv.Itoa(n)))
	}
	for _, h := range a.running {
		h.cancelRequested = true
	}
	a.accepting = false
	a.mu.Unlock()
	a.shutdownOnce.Do(func() { close(a.ShutdownRequested) })
	return nil
}

// emit publishes an event through the event log.
func (a *App) emit(e domain.Event) domain.Event {
	return a.emitFor(e, nil)
}

// emitFor is emit for an event of a running execution: its notice follows the definition the execution started with.
func (a *App) emitFor(e domain.Event, started *domain.TaskDocument) domain.Event {
	if e.Timestamp.IsZero() {
		e.Timestamp = a.now()
	}
	stored := a.d.Events.Append(e)
	a.notifyFor(stored, started)
	return stored
}

func (a *App) Version() string      { return a.d.Version }
func (a *App) Commit() string       { return a.d.Commit }
func (a *App) BuiltAt() time.Time   { return a.d.BuiltAt }
func (a *App) StartedAt() time.Time { return a.startedAt }
func (a *App) Paths() Paths         { return a.d.Paths }

func (a *App) AcceptingFirings() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.accepting
}

func (a *App) Subscribe(after int64) (<-chan domain.Event, bool, func()) {
	return a.d.Events.Subscribe(after)
}

func (a *App) ExecutionEvents(id string, after int64, limit int) ([]domain.Event, bool, error) {
	a.mu.Lock()
	_, ok := a.executions[id]
	a.mu.Unlock()
	if !ok {
		return nil, false, newError(CodeNotFound, domain.T("execution_not_found", "no execution {{execution}}", "execution", id))
	}
	return a.d.Events.Replay(id, after, limit)
}

// persistExecution saves a copy of the record taken under a.mu, since the executor may change it at the same time; the
// caller must not hold a.mu.
func (a *App) persistExecution(x *domain.Execution) {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	a.mu.Lock()
	data, err := json.Marshal(x)
	a.mu.Unlock()
	var snapshot domain.Execution
	if err == nil {
		err = json.Unmarshal(data, &snapshot)
	}
	if err == nil {
		err = a.d.Executions.Save(&snapshot)
	}
	if err != nil {
		a.log.Printf("saving execution %s: %v", x.ID, err)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
