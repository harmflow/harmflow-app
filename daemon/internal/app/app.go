// Package app holds the daemon's use cases: tasks, scheduling, executions, failover, recovery, notifications, and
// settings. It orchestrates only; what a task does lives in its prompt, agents, and skills.
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

// Error returns the en-US message.
func (e *Error) Error() string { return e.Message }

// The codes of an Error, which the API answers with an HTTP status: a malformed request, a missing task or execution,
// a conflict with the current state, a task that fails validation, a disabled task asked to run, and running executions
// that a stop must confirm.
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

// Paths are the directories of one installation.
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

// NewPaths lays the directories out under home, the installation's directory; userHome is the user's home directory,
// and builtins the directory of the built-in agents and skills.
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

// Deps is what New wires the use cases to: the installation's paths, the build, the ports, and the intervals tests
// shorten. A zero clock, wake monitor, location, interval, operating system, or logger takes its default.
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
	// Workspace reads the files an attempt changed in its workspace, for the checkpoint before a switch; without one, the
	// checkpoint is not taken.
	Workspace ports.WorkspaceReader
	// HarnessEnv is the environment harness processes, and script tasks' programs, start with.
	HarnessEnv []string
	// OS is this machine's operating system, as a script task names it; the running one when empty.
	OS string
	// TickInterval is how often the scheduler looks for due firings.
	TickInterval time.Duration
	// TimeoutUnit is what a task's timeoutMinutes counts, a minute when zero; tests shorten it, since a timeout is at
	// least one minute.
	TimeoutUnit time.Duration
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

// App holds the daemon's use cases and their in-memory state: the tasks, executions, schedule, ledger, harness
// inventory, notifications, and settings. New builds it; Start loads its state and starts its loops, and Stop ends
// them.
type App struct {
	d         Deps
	log       *log.Logger
	startedAt time.Time

	mu            sync.Mutex
	tasks         map[string]*taskEntry
	templates     []*Template // the built-in templates, sorted by identifier, read at start
	executions    map[string]*domain.Execution
	running       map[string]*runHandle // by task id
	inventory     map[string]ports.HarnessInfo
	manualPaths   map[string]string
	settings      Settings
	sched         map[string]*scheduleState
	firings       map[string][]firingRecord // by task id
	ledger        map[string]map[string]*domain.WorkItem
	ledgerRepair  bool // the ledger was saved before ledgerVersion, and repairLedger has not mended it yet
	notifications []Notification
	accepting     bool
	stopping      bool
	adapters      map[string]ports.HarnessAdapter
	// quotas holds each harness's last quota reading, by harness id, and quotaReads runs one read of a harness at a time.
	quotas     map[string]quotaReading
	quotaReads map[string]*sync.Mutex
	// unavailableUntil is when the daemon last became available again, at its start or when the machine woke: a firing
	// scheduled before it and still due came due while the daemon or the machine was down, and is a misfire however
	// late it is.
	unavailableUntil time.Time

	// tickMu runs one Tick at a time, whole.
	tickMu sync.Mutex
	// retentionMu runs one retention pass at a time, whole.
	retentionMu sync.Mutex
	// records orders the saves of each execution record, by execution id, so the last copy taken of a record is the last
	// written; a save waits only for the saves of its own record. Guarded by mu, it holds a record while a save holds or
	// waits for it.
	records map[string]*recordLock

	stopOnce sync.Once
	// stopLoops cancels the context Start runs the loops under, and stopDetection the one of the harness detection it
	// launches; both are nil until Start.
	stopLoops     context.CancelFunc
	stopDetection context.CancelFunc
	wg            sync.WaitGroup
	// detection counts the harness detections Start launched, which Stop waits for.
	detection sync.WaitGroup
	// announceMu orders harness checks before daemon.stopping: a detection holds it for reading while it decides to
	// announce and announces, and Stop holds it for writing to announce the stop, so a detection an API request or an
	// execution started either announces first or sees the daemon stopping and announces nothing.
	announceMu sync.RWMutex
	// ShutdownRequested is closed when the API asks the daemon to stop.
	ShutdownRequested chan struct{}
	shutdownOnce      sync.Once
}

// New builds the use cases over d, with its defaults filled in, and creates the directories they write to; it loads
// nothing, which Start does.
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
	if d.TimeoutUnit == 0 {
		d.TimeoutUnit = time.Minute
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
		adapters: map[string]ports.HarnessAdapter{}, quotas: map[string]quotaReading{}, quotaReads: map[string]*sync.Mutex{},
		records: map[string]*recordLock{}, ShutdownRequested: make(chan struct{}),
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
// watcher, and maintenance. The loops run until ctx ends or Stop is called, whichever comes first.
func (a *App) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	detectCtx, stopDetection := context.WithCancel(ctx)
	a.mu.Lock()
	a.stopLoops, a.stopDetection = cancel, stopDetection
	a.mu.Unlock()
	if err := a.loadSettings(); err != nil {
		return err
	}
	a.loadNotifications()
	a.loadLedger()
	if _, err := a.d.State.Load("harnesses", &a.manualPaths); err != nil {
		a.log.Printf("loading the harness paths set by hand; detection finds them instead: %v", err)
	}
	if err := a.loadTasks(ctx); err != nil {
		return err
	}
	a.loadTemplates()
	a.loadSchedule()
	if err := a.recover(ctx); err != nil {
		return err
	}
	a.repairLedger() // after recovery, so that every execution its items name has ended
	a.refreshHarnessesAsync(detectCtx)
	a.emit(domain.Event{Type: domain.EventDaemonStarted, Payload: map[string]any{"version": a.d.Version, "pid": os.Getpid()}}.Says(
		domain.T("daemon_started", "HarmFlow daemon {{version}} started", "version", a.d.Version)))
	a.mu.Lock()
	a.unavailableUntil = a.now() // the daemon was down until now
	a.mu.Unlock()
	a.wg.Add(3)
	go a.schedulerLoop(ctx)
	go a.watchLoop(ctx)
	go a.maintenanceLoop(ctx)
	a.startQueued()
	return nil
}

// Stop ends the loops and the harness detection Start launched, and interrupts running executions: a stopped daemon
// never continues them. The detection ends before the daemon announces that it stops, so no harness check is
// recorded after that.
func (a *App) Stop(reason domain.Text) {
	a.stopOnce.Do(func() {
		a.mu.Lock()
		a.accepting = false
		a.stopping = true
		stopLoops, stopDetection := a.stopLoops, a.stopDetection
		var cancels []context.CancelFunc
		var dones []chan struct{}
		for _, h := range a.running {
			if h.cancel != nil {
				cancels = append(cancels, h.cancel)
			}
			dones = append(dones, h.done)
		}
		a.mu.Unlock()
		if stopDetection != nil {
			stopDetection()
		}
		a.detection.Wait()
		a.announceMu.Lock()
		a.emit(domain.Event{Type: domain.EventDaemonStopping}.Says(reason))
		a.announceMu.Unlock()
		if stopLoops != nil {
			stopLoops()
		}
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
		// No execution or loop asks a harness anything more: the processes harnesses keep between calls end.
		for _, id := range sortedKeys(a.adapters) {
			if c, ok := a.adapters[id].(ports.HarnessCloser); ok {
				c.Close()
			}
		}
	})
}

// RequestShutdown asks main to stop the daemon; with running executions it needs force.
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

// Version is the daemon's version.
func (a *App) Version() string { return a.d.Version }

// Commit is the short commit the daemon was built from, as Deps.Commit gives it.
func (a *App) Commit() string { return a.d.Commit }

// BuiltAt is when the daemon was built; zero without a build time.
func (a *App) BuiltAt() time.Time { return a.d.BuiltAt }

// StartedAt is when New built the use cases, by their clock.
func (a *App) StartedAt() time.Time { return a.startedAt }

// Paths are the installation's directories.
func (a *App) Paths() Paths { return a.d.Paths }

// AcceptingFirings reports whether the daemon still starts executions: false once it is stopping or was asked to stop.
func (a *App) AcceptingFirings() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.accepting
}

// Subscribe streams the events after the id after, the buffered ones then the live ones, and reports whether the buffer
// no longer reaches back to that id; the function it returns ends the subscription.
func (a *App) Subscribe(after int64) (<-chan domain.Event, bool, func()) {
	return a.d.Events.Subscribe(after)
}

// ExecutionEvents returns the stored events of an execution after the id after, at most limit when it is positive, and
// whether retention deleted its log; an unknown execution is CodeNotFound.
func (a *App) ExecutionEvents(id string, after int64, limit int) ([]domain.Event, bool, error) {
	a.mu.Lock()
	_, ok := a.executions[id]
	a.mu.Unlock()
	if !ok {
		return nil, false, newError(CodeNotFound, domain.T("execution_not_found", "no execution {{execution}}", "execution", id))
	}
	return a.d.Events.Replay(id, after, limit)
}

// persistExecution saves a copy of the record taken under a.mu, since the executor may change it at the same time, and
// returns the revision of that copy and whether it was written. It waits only for the saves of the same record, so a
// firing or a cancellation never waits for another execution's save; the caller must not hold a.mu.
func (a *App) persistExecution(x *domain.Execution) (uint64, bool) {
	defer a.lockRecord(x.ID)()
	a.mu.Lock()
	data, err := json.Marshal(x)
	revision := x.Revision()
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
		return 0, false
	}
	return revision, true
}

// recordLock orders the saves of one execution record; users counts the saves that hold or wait for it, guarded by the
// App's mutex.
type recordLock struct {
	mu    sync.Mutex
	users int
}

// lockRecord waits for the saves of execution id already under way and returns the function that ends this one. The
// last save of a record to end removes its lock, so the locks never outnumber the saves under way.
func (a *App) lockRecord(id string) (unlock func()) {
	a.mu.Lock()
	l := a.records[id]
	if l == nil {
		l = &recordLock{}
		a.records[id] = l
	}
	l.users++
	a.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		a.mu.Lock()
		if l.users--; l.users == 0 {
			delete(a.records, id)
		}
		a.mu.Unlock()
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
