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
	"sync/atomic"
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
	// GitHub reads GitHub's count of the gh login's hourly limits, for the sidebar's GitHub gauges; without one, both are
	// left out.
	GitHub ports.GitHubRateLimitReader
	// Workspace reads the files an attempt changed in its workspace, for the checkpoint before a switch; without one, the
	// checkpoint is not taken.
	Workspace ports.WorkspaceReader
	// HarnessEnv is the environment harness processes, and script tasks' programs, start with.
	HarnessEnv []string
	// OS is this machine's operating system, as a script task names it; the running one when empty.
	OS string
	// Identify reads a script task's started program's process identity, which its record keeps for crash recovery;
	// procutil's reading when nil.
	Identify func(ctx context.Context, pid int) (domain.ProcessIdentity, error)
	// TickInterval is how often the scheduler looks for due firings.
	TickInterval time.Duration
	// TimeoutUnit is what a task's timeoutMinutes counts, a minute when zero; tests shorten it, since a timeout is at
	// least one minute.
	TimeoutUnit time.Duration
	// MisfireAfter is how late a firing may be before it counts as missed though no unavailability was seen: the clock
	// jumped, or the daemon could not run.
	MisfireAfter time.Duration
	// HarnessTokens gives the credential store item of a harness's long-lived token; nil when the daemon keeps its
	// secrets in no credential store, so it keeps no token.
	HarnessTokens func(harness string) ports.SecretStore
	// SignInTimeout is how long a harness's sign-in waits for the user to approve it in the browser; ten minutes when
	// zero.
	SignInTimeout time.Duration
	// StopDetectionWait is how long Stop waits for the harness detection to end before it goes on without it, and
	// StopExecutionsWait how long it waits for the cancelled executions to end, all of them under the one deadline;
	// DefaultStopDetectionWait and DefaultStopExecutionsWait when zero.
	StopDetectionWait  time.Duration
	StopExecutionsWait time.Duration
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
	// loadedRevision is the revision loadTasks loaded at start, which checkTasks validates again; empty once the file
	// changed, or the daemon wrote it, since.
	loadedRevision string
}

type runHandle struct {
	cancel          context.CancelFunc
	cancelRequested bool
	done            chan struct{}
	// releaseIndex ends the hold of the index file the terminal save was made under, which writes the index; nil
	// before that save. Only the execution's own goroutine uses it.
	releaseIndex func()
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
	templates     []*Template                         // the built-in templates, sorted by identifier, read at start
	executions    map[string]*domain.Execution        // with the four below, the executions history.go describes
	ended         map[string]*domain.ExecutionSummary // by execution id
	byCreated     []placed
	byEnd         []placed
	latest        map[string]placed     // by task id
	running       map[string]*runHandle // by task id
	inventory     map[string]ports.HarnessInfo
	manualPaths   map[string]string
	settings      Settings
	sched         map[string]*scheduleState
	firings       map[string][]firingRecord // by task id
	ledger        map[string]map[string]*domain.WorkItem
	applied       map[string][]string // by execution id, the items whose result the ledger took, as ledgerFile.Applied
	ledgerRepair  bool                // the ledger was saved before ledgerVersion, and repairLedger has not mended it yet
	archive       ledgerArchive
	notifications []Notification
	accepting     bool
	stopping      bool
	// drain is the draining stop under way, nil while none was asked for; accepting is false while it is set.
	drain    *drainState
	adapters map[string]ports.HarnessAdapter
	// quotas holds each harness's last quota reading, by harness id, and quotaReads runs one read of a harness at a time.
	// quotaFailures holds the failed reads in a row of each harness that has them, under the same key.
	quotas        map[string]quotaReading
	quotaFailures map[string]quotaFailure
	quotaReads    map[string]*sync.Mutex
	// signIns holds each harness's sign-in, by harness id; a.wg counts the ones that run.
	signIns map[string]*signInState
	// tokens holds the record of each harness's long-lived token, by harness id; the token is in the credential store.
	tokens map[string]*tokenRecord
	// unavailableUntil is when the daemon last became available again, at its start or when the machine woke: a firing
	// scheduled before it and still due came due while the daemon or the machine was down, and is a misfire however
	// late it is.
	unavailableUntil time.Time

	// tickMu runs one Tick at a time, whole.
	tickMu sync.Mutex
	// retentionMu runs one retention pass at a time, whole.
	retentionMu sync.Mutex
	// ledgerMu orders the ledger's saves: a save holds it from its snapshot to the end of its write, so the ledger is
	// written in the order its snapshots are taken. It is taken before mu, which stays free during the write; no caller
	// of saveLedger holds mu.
	ledgerMu sync.Mutex
	// schedMu orders the scheduler state's saves as ledgerMu orders the ledger's: a save holds it from its snapshot to
	// the end of its write, so the state is written in the order its snapshots are taken. It is taken before mu, which
	// stays free during the write; no caller of saveSchedule holds mu.
	schedMu sync.Mutex
	// records orders the saves of each execution record, by execution id, so the last copy taken of a record is the last
	// written; a save waits only for the saves of its own record. Guarded by mu, it holds a record while a save holds or
	// waits for it.
	records map[string]*recordLock
	// noticeMu orders the saves of the notices as ledgerMu orders the ledger's: a save holds it from its snapshot to the
	// end of its write. It is taken before mu; no caller of saveNotifications holds mu.
	noticeMu sync.Mutex
	// noticeSaves counts the saves of the notices keepNotice started, which run off the caller's path; Stop waits for
	// them.
	noticeSaves sync.WaitGroup
	// noticesInPlace is set, under mu, once Stop waits for noticeSaves: an execution that outlived the stop's wait then
	// saves its notices on its own path, never adding to noticeSaves while Stop waits for it.
	noticesInPlace bool

	stopOnce sync.Once
	// stopLoops cancels the context Start runs the loops under, and stopDetection the one of the harness detection it
	// launches; both are nil until Start.
	stopLoops     context.CancelFunc
	stopDetection context.CancelFunc
	// wg counts the loops, the drain's watcher, and the sign-ins; runs counts the executions, which Stop waits for only
	// up to Deps.StopExecutionsWait.
	wg   sync.WaitGroup
	runs sync.WaitGroup
	// detection counts the harness detections Start launched, and the loop that runs Detect again's, which Stop waits
	// for.
	detection sync.WaitGroup
	// redetect holds the pass of detection RedetectHarnesses asked for, which redetectLoop has not started yet: one at
	// most, so requests made while a pass runs ask for a single pass after it.
	redetect chan struct{}
	// announceMu orders harness checks before daemon.stopping: a detection holds it for reading while it decides to
	// announce and announces, and Stop holds it for writing to announce the stop, so a detection an API request or an
	// execution started either announces first or sees the daemon stopping and announces nothing.
	announceMu sync.RWMutex
	// ShutdownRequested is closed when the API asks the daemon to stop, or a drain has ended; shutdownReason says which.
	// Neither takes mu, so a stop is asked for at once however long another holder keeps it.
	ShutdownRequested chan struct{}
	shutdownOnce      sync.Once
	shutdownReason    atomic.Pointer[domain.Text]
	// stopForced is set by a forced stop, which RequestShutdown records without mu: from then on the daemon accepts no
	// work, and every execution it runs counts as cancelled by the user (acceptingLocked, cancelRequestedLocked).
	stopForced atomic.Bool
	// stopBegun is closed when Stop begins, before it takes mu: every running execution cancels itself on it, and the
	// drain's watcher returns.
	stopBegun chan struct{}
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
	if d.StopDetectionWait == 0 {
		d.StopDetectionWait = DefaultStopDetectionWait
	}
	if d.StopExecutionsWait == 0 {
		d.StopExecutionsWait = DefaultStopExecutionsWait
	}
	if d.OS == "" {
		d.OS = currentOS()
	}
	if d.Identify == nil {
		d.Identify = identifyProcess
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
		ended: map[string]*domain.ExecutionSummary{}, latest: map[string]placed{},
		inventory: map[string]ports.HarnessInfo{}, manualPaths: map[string]string{}, sched: map[string]*scheduleState{},
		firings: map[string][]firingRecord{}, ledger: map[string]map[string]*domain.WorkItem{},
		applied: map[string][]string{}, accepting: true,
		adapters: map[string]ports.HarnessAdapter{}, quotas: map[string]quotaReading{},
		quotaFailures: map[string]quotaFailure{}, quotaReads: map[string]*sync.Mutex{},
		signIns: map[string]*signInState{}, tokens: map[string]*tokenRecord{}, records: map[string]*recordLock{},
		redetect: make(chan struct{}, 1), ShutdownRequested: make(chan struct{}), stopBegun: make(chan struct{}),
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

// Start loads state, recovers interrupted executions, and starts the scheduler, the task-file watcher, and maintenance.
// It waits for no harness: the task files load without the checks that ask one, and the harnesses are detected, then
// the task files checked again with them, in the background. The loops run until ctx ends or Stop is called, whichever
// comes first.
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
	a.repairLedger()                  // after recovery, so that every execution its items name has ended
	a.loadHarnessTokens()             // before detection, which checks each harness with its token
	a.detectThenCheckTasks(detectCtx) // the task files were loaded without asking a harness
	a.noticeTokenExpiry()
	a.detection.Go(func() { a.redetectLoop(detectCtx) })
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

// DefaultStopDetectionWait is how long Stop waits for the harness detection to end, and DefaultStopExecutionsWait how
// long for the cancelled executions to end, when Deps sets neither.
const (
	DefaultStopDetectionWait  = 5 * time.Second
	DefaultStopExecutionsWait = 45 * time.Second
)

// Stop ends the loops, the harness detection Start launched, and the sign-ins that run, and interrupts running
// executions: a stopped daemon never continues them. Its first step, before it takes any lock, closes stopBegun, which
// cancels every running execution (see execute), so nothing the stop waits on holds their cancels up. It then waits for
// the detection, at most Deps.StopDetectionWait, before it announces that the daemon stops: a detection that ends in
// time records no harness check after that, and one still running when the wait ends records none either, since the
// daemon is stopping (see announceMu). The executions are awaited together, all of them within
// Deps.StopExecutionsWait; one still running then is left to the recovery of the next start, which ends it interrupted.
func (a *App) Stop(reason domain.Text) {
	a.stopOnce.Do(func() {
		close(a.stopBegun)
		a.mu.Lock()
		a.accepting = false
		a.stopping = true
		a.stopSignInsLocked()
		stopLoops, stopDetection := a.stopLoops, a.stopDetection
		a.mu.Unlock()
		if stopDetection != nil {
			stopDetection()
		}
		if !waitAtMost(&a.detection, a.d.StopDetectionWait) {
			a.log.Printf("stopping: harness detection still running after %s; stopping without it", a.d.StopDetectionWait)
		}
		a.announceMu.Lock()
		a.emit(domain.Event{Type: domain.EventDaemonStopping}.Says(reason))
		a.announceMu.Unlock()
		if stopLoops != nil {
			stopLoops()
		}
		if !waitAtMost(&a.runs, a.d.StopExecutionsWait) {
			a.log.Printf("stopping: executions still running after %s; stopping without them", a.d.StopExecutionsWait)
		}
		a.wg.Wait()
		// The executions and loops that ended kept their last notices; those saves end before the daemon does. An
		// execution still running saves its own in place from now on (see keepNotice).
		a.mu.Lock()
		a.noticesInPlace = true
		a.mu.Unlock()
		a.noticeSaves.Wait()
		// No execution or loop asks a harness anything more: the processes harnesses keep between calls end.
		for _, id := range sortedKeys(a.adapters) {
			if c, ok := a.adapters[id].(ports.HarnessCloser); ok {
				c.Close()
			}
		}
	})
}

// waitAtMost waits for wg, at most d, and reports whether wg ended in time. The goroutine that waits for wg ends when
// wg does, which Stop has asked for by then: it cancelled what wg counts.
func waitAtMost(wg *sync.WaitGroup, d time.Duration) bool {
	ended := make(chan struct{})
	go func() {
		wg.Wait()
		close(ended)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ended:
		return true
	case <-timer.C:
		return false
	}
}

// RequestShutdown asks main to stop the daemon; with running executions it needs force. A forced stop also ends a
// drain under way at once. A forced stop takes no lock, so it returns at once however long another holder keeps mu: it
// needs no count of the executions, and stopForced marks them cancelled. A stop without force counts them under mu.
func (a *App) RequestShutdown(force bool) error {
	if force {
		a.stopForced.Store(true)
		a.requestStop(stopRequested())
		return nil
	}
	a.mu.Lock()
	n := 0
	for _, x := range a.executions {
		if x.Status.IsActive() {
			n++
		}
	}
	if n > 0 {
		a.mu.Unlock()
		return newError(CodeExecutionsRunning, domain.T("executions_running",
			"{{count}} executions are running; confirm to stop the daemon and cancel them", "count", strconv.Itoa(n)))
	}
	for _, h := range a.running {
		h.cancelRequested = true
	}
	a.accepting = false
	a.mu.Unlock()
	a.requestStop(stopRequested())
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
	return a.acceptingLocked()
}

// acceptingLocked reports whether the daemon still starts work: accepting, unless a forced stop was asked for, which
// RequestShutdown records without mu. The caller holds mu.
func (a *App) acceptingLocked() bool {
	return a.accepting && !a.stopForced.Load()
}

// cancelRequestedLocked reports whether h's execution was asked to cancel: by the user, or by a forced stop of the
// daemon. The caller holds mu.
func (a *App) cancelRequestedLocked(h *runHandle) bool {
	return h.cancelRequested || a.stopForced.Load()
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
	ok := a.knownLocked(id)
	a.mu.Unlock()
	if !ok {
		return nil, false, executionNotFound(id)
	}
	return a.d.Events.Replay(id, after, limit)
}

// persistExecution saves a copy of the record taken under a.mu, since the executor may change it at the same time, and
// returns the revision of that copy and whether it was written. It waits only for the saves of the same record, so a
// firing or a cancellation never waits for another execution's save; the caller must not hold a.mu. Once the record of
// an execution that ended is saved, the daemon holds it as its summary.
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
	if snapshot.Status.IsTerminal() {
		a.mu.Lock()
		a.keepEndedLocked(x, snapshot.Summarize())
		a.mu.Unlock()
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
