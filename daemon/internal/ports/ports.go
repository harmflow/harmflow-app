// Package ports declares the output ports the use cases depend on (architecture/daemon-structure.md). Every vendor,
// tool, and operating-system detail lives behind one of them, in an adapter.
package ports

import (
	"context"
	"io"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// Clock is injectable so the scheduler can be tested without waiting (ADR-001).
type Clock interface {
	Now() time.Time
}

// WakeMonitor tells the scheduler when the machine last woke from sleep or hibernation, so a firing due while it was
// suspended follows the missed-run policy however soon it wakes (FS-002 FR-004). Tests replace it, as they do Clock.
type WakeMonitor interface {
	// LastWake is when the machine was last seen awake after a suspension; zero when none was seen.
	LastWake() time.Time
}

// TaskFile is one task definition as stored: the raw YAML is kept so comments survive edits from the editor.
type TaskFile struct {
	ID       string
	Path     string
	Content  []byte
	Revision string
	ModTime  time.Time
}

// TaskRepository holds task YAML files; the daemon is their only writer (FS-001 FR-004).
type TaskRepository interface {
	List() ([]TaskFile, error)
	Get(id string) (TaskFile, error)
	// Save writes atomically; expectedRevision, when set, must match the file on disk or ErrConflict is returned.
	Save(id string, content []byte, expectedRevision string) (TaskFile, error)
	Delete(id string) error
	Dir() string
}

// StateStore persists operational state as versioned JSON, written atomically (ADR-007).
type StateStore interface {
	Load(name string, v any) (bool, error)
	Save(name string, v any) error
}

// ExecutionRepository keeps execution records with no time limit (D-073).
type ExecutionRepository interface {
	Save(x *domain.Execution) error
	LoadAll() ([]*domain.Execution, error)
}

// EventLog writes one JSONL file per execution and publishes every event live (ADR-004, ADR-007).
type EventLog interface {
	Append(e domain.Event) domain.Event
	Replay(executionID string, after int64, limit int) ([]domain.Event, bool, error)
	Subscribe(after int64) (events <-chan domain.Event, replayGap bool, cancel func())
	RawWriter(executionID string, attempt int) (io.WriteCloser, error)
	// ReadRaw returns the end of an attempt's raw capture, at most limit bytes; a capture never written reads empty.
	ReadRaw(executionID string, attempt int, limit int64) (RawCapture, error)
	DeleteLog(executionID string) error
	LogModTime(executionID string) (time.Time, bool)
}

// RawCapture is the end of one attempt's raw output: Text starts at byte Offset of a capture of Size bytes.
type RawCapture struct {
	Size   int64
	Offset int64
	Text   string
}

// SecretStore keeps the local API token in the operating system's credential store (D-090).
type SecretStore interface {
	Get() (string, error)
	Set(token string) error
	Kind() string
}

// HostServiceManager enables and disables the daemon's start with the user's session (D-008, D-062).
type HostServiceManager interface {
	Enabled() bool
	Enable(executable string, args []string, env map[string]string) error
	Disable() error
	Mechanism() string
	Location() string
}

// HarnessInfo is what detection found (FS-004 FR-006, FR-007).
type HarnessInfo struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	Detected            bool              `json:"detected"`
	Path                string            `json:"path,omitempty"`
	PathSource          string            `json:"pathSource,omitempty"`
	Installations       []string          `json:"installations"`
	Installation        string            `json:"installation,omitempty"` // the entry of Installations in use
	Version             string            `json:"version,omitempty"`
	VersionSupport      string            `json:"versionSupport,omitempty"`
	Authentication      string            `json:"authentication"`
	Readiness           string            `json:"readiness"`
	Models              []ModelInfo       `json:"models"`
	Efforts             []string          `json:"efforts"`
	CapabilitySource    string            `json:"capabilitySource,omitempty"`
	EnforceableControls []string          `json:"enforceableControls"`
	LastCheckedAt       time.Time         `json:"lastCheckedAt"`
	Diagnostic          string            `json:"diagnostic,omitempty"`
	DiagnosticKey       string            `json:"diagnosticKey,omitempty"`
	DiagnosticParams    map[string]string `json:"diagnosticParams,omitempty"`
	Features            map[string]bool   `json:"-"`
}

// SetDiagnostic records what is wrong with the harness; Diagnostic is the en-US text (FS-012 FR-005).
func (h *HarnessInfo) SetDiagnostic(t domain.Text) {
	h.Diagnostic, h.DiagnosticKey, h.DiagnosticParams = t.Message, t.Key, t.Params
}

// DiagnosticText is the text SetDiagnostic recorded.
func (h HarnessInfo) DiagnosticText() domain.Text {
	return domain.Text{Key: h.DiagnosticKey, Params: h.DiagnosticParams, Message: h.Diagnostic}
}

type ModelInfo struct {
	ID      string   `json:"id"`
	Efforts []string `json:"efforts,omitempty"`
}

// Ready reports whether an execution may start on the harness.
func (h HarnessInfo) Ready() bool { return h.Readiness == "ready" }

// Resolved agent or skill, delivered to the harness (D-092).
type ResolvedItem struct {
	Name        string
	Kind        string // agent or skill
	Builtin     bool
	Path        string // file of an agent, folder of a skill
	Description string
}

// RunRequest is everything an adapter needs to start one attempt.
type RunRequest struct {
	ExecutionID    string
	TaskID         string
	Attempt        int
	Prompt         string
	Context        string // HarmFlow's orchestration context, delivered as a system prompt where the harness has one
	Agents         []ResolvedItem
	Skills         []ResolvedItem
	Model          string
	Effort         string
	WorkDir        string
	AdditionalDirs []string
	Permissions    domain.Permissions
	ExecutionDir   string
	Env            []string
}

// Restriction is one declared restriction and whether the harness enforces it (D-068). Text says how; the user sees
// it only for a restriction not enforced, so only those texts carry a key.
type Restriction struct {
	Name     string
	Enforced bool
	Text     domain.Text
}

// Invocation is the process an adapter asks the executor to start.
type Invocation struct {
	Path         string
	Args         []string
	Env          []string
	Dir          string
	Stdin        string
	Restrictions []Restriction
}

// HarnessEventKind is a normalized observation of the harness's structured output.
type HarnessEventKind string

const (
	HarnessStarted     HarnessEventKind = "started"
	HarnessMessage     HarnessEventKind = "message"
	HarnessToolStarted HarnessEventKind = "tool_started"
	HarnessToolDone    HarnessEventKind = "tool_completed"
	HarnessFileChanged HarnessEventKind = "file_changed"
	HarnessWarning     HarnessEventKind = "warning"
	HarnessRateLimit   HarnessEventKind = "rate_limit"
	// HarnessUsage carries the attempt's token use so far, each time the harness reports more.
	HarnessUsage HarnessEventKind = "usage"
)

type HarnessEvent struct {
	Kind      HarnessEventKind
	Message   string
	ToolID    string
	Tool      string
	Command   string
	Path      string
	ExitCode  *int
	IsError   bool
	Output    string
	SessionID string
	// Model and Effort are what the harness says it runs with, on HarnessStarted; empty when it does not say.
	Model  string
	Effort string
	Usage  *domain.Usage
}

// OutputParser turns one attempt's stdout lines into events and classifies its end.
type OutputParser interface {
	Line(line []byte) []HarnessEvent
	Finish(exitCode int, stderrTail string, timedOut, cancelled bool) domain.AttemptEnd
}

// HarnessAdapter is the HarnessRunner port of one harness (ADR-009): detect, declare capabilities, run
// non-interactively, capture events, and recognize quota failure. Cancellation is the executor's, by process group.
type HarnessAdapter interface {
	ID() string
	Name() string
	Detect(ctx context.Context, manualPath string) HarnessInfo
	ValidateProfile(info HarnessInfo, model, effort string) error
	Prepare(info HarnessInfo, req RunRequest) (Invocation, OutputParser, error)
	// UserItem looks up an agent or skill in the user's harness configuration.
	UserItem(kind, name string) (ResolvedItem, bool)
	UserItems(kind string) []ResolvedItem
}

// ProfileWarner is a HarnessAdapter whose capabilities cannot always check a model and effort: ProfileWarning says so,
// and the save keeps them with that warning (FS-004 FR-005).
type ProfileWarner interface {
	ProfileWarning(info HarnessInfo, model, effort string) (domain.Text, bool)
}

// SystemInfo reads the gauges of the sidebar.
type SystemInfo interface {
	Snapshot(dir string) map[string]any
}

// ToolProber reports git and gh.
type ToolProber interface {
	Probe(ctx context.Context) []ToolInfo
}

type ToolInfo struct {
	ID               string            `json:"id"`
	Detected         bool              `json:"detected"`
	Path             string            `json:"path,omitempty"`
	Version          string            `json:"version,omitempty"`
	Authentication   string            `json:"authentication,omitempty"`
	Diagnostic       string            `json:"diagnostic,omitempty"`
	DiagnosticKey    string            `json:"diagnosticKey,omitempty"`
	DiagnosticParams map[string]string `json:"diagnosticParams,omitempty"`
}

// SetDiagnostic records what is wrong with the tool; Diagnostic is the en-US text (FS-012 FR-005).
func (t *ToolInfo) SetDiagnostic(text domain.Text) {
	t.Diagnostic, t.DiagnosticKey, t.DiagnosticParams = text.Message, text.Key, text.Params
}
