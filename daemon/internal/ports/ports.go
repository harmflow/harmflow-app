// Package ports declares the output ports the use cases depend on. Every vendor,
// tool, and operating-system detail lives behind one of them, in an adapter.
package ports

import (
	"context"
	"io"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// Clock is injectable so the scheduler can be tested without waiting.
type Clock interface {
	Now() time.Time
}

// WakeMonitor tells the scheduler when the machine last woke from sleep or hibernation, so a firing due while it was
// suspended follows the missed-run policy however soon it wakes. Tests replace it, as they do Clock.
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

// TaskRepository holds task YAML files; the daemon is their only writer.
type TaskRepository interface {
	List() ([]TaskFile, error)
	Get(id string) (TaskFile, error)
	// Save writes atomically; expectedRevision, when set, must match the file on disk or ErrConflict is returned.
	Save(id string, content []byte, expectedRevision string) (TaskFile, error)
	Delete(id string) error
	Dir() string
}

// StateStore persists operational state as versioned JSON, written atomically.
type StateStore interface {
	Load(name string, v any) (bool, error)
	Save(name string, v any) error
}

// ExecutionRepository keeps execution records with no time limit.
type ExecutionRepository interface {
	Save(x *domain.Execution) error
	LoadAll() ([]*domain.Execution, error)
}

// EventLog writes one JSONL file per execution and publishes every event live.
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

// SecretStore keeps the local API token in the operating system's credential store.
type SecretStore interface {
	Get() (string, error)
	Set(token string) error
	Kind() string
}

// HostServiceManager enables and disables the daemon's start with the user's session.
type HostServiceManager interface {
	Enabled() bool
	Enable(executable string, args []string, env map[string]string) error
	Disable() error
	Mechanism() string
	Location() string
}

// HarnessInfo is what detection found.
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

// SetDiagnostic records what is wrong with the harness; Diagnostic is the en-US text.
func (h *HarnessInfo) SetDiagnostic(t domain.Text) {
	h.Diagnostic, h.DiagnosticKey, h.DiagnosticParams = t.Message, t.Key, t.Params
}

// DiagnosticText is the text SetDiagnostic recorded.
func (h HarnessInfo) DiagnosticText() domain.Text {
	return domain.Text{Key: h.DiagnosticKey, Params: h.DiagnosticParams, Message: h.Diagnostic}
}

// ModelInfo is a model a harness accepts, with the reasoning efforts it accepts for that model.
type ModelInfo struct {
	ID      string   `json:"id"`
	Efforts []string `json:"efforts,omitempty"`
}

// Ready reports whether an execution may start on the harness.
func (h HarnessInfo) Ready() bool { return h.Readiness == "ready" }

// Resolved agent or skill, delivered to the harness.
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
	// PolicyCheck marks a request that validation makes only to learn the restrictions: no attempt starts, and WorkDir
	// stands in for a working directory, so nothing in it describes the task.
	PolicyCheck bool
}

// Restriction is one declared restriction and whether the harness enforces it. Text says how; the user sees
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
	// ExecutionID is the value of HARMFLOW_EXECUTION_ID in Env. Ending the process also ends every process that still
	// carries it, wherever it moved; empty, only the process's own tree is ended.
	ExecutionID string
}

// HarnessEventKind is a normalized observation of the harness's structured output.
type HarnessEventKind string

// The kinds of a HarnessEvent: the session started, the agent wrote a message, a tool call started or completed, a file
// changed, the harness printed a warning or a rate-limit notice, it began to wait for the network, or it reported its
// token use.
const (
	HarnessStarted     HarnessEventKind = "started"
	HarnessMessage     HarnessEventKind = "message"
	HarnessToolStarted HarnessEventKind = "tool_started"
	HarnessToolDone    HarnessEventKind = "tool_completed"
	HarnessFileChanged HarnessEventKind = "file_changed"
	HarnessWarning     HarnessEventKind = "warning"
	HarnessRateLimit   HarnessEventKind = "rate_limit"
	// HarnessNetworkWait is the first notice of a stretch in which the harness cannot reach its provider and waits for
	// the network; its Message is the harness's own notice.
	HarnessNetworkWait HarnessEventKind = "network_wait"
	// HarnessUsage carries the attempt's token use so far, each time the harness reports more.
	HarnessUsage HarnessEventKind = "usage"
	// HarnessQuota carries the quota the harness reported while it runs, extra usage included.
	HarnessQuota HarnessEventKind = "quota"
)

// HarnessEvent is one observation an OutputParser read from the harness's output; which fields it carries depends on
// its Kind.
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
	Quota  *domain.Quota
}

// OutputParser turns one attempt's stdout lines into events and classifies its end.
type OutputParser interface {
	Line(line []byte) []HarnessEvent
	Finish(exitCode int, stderrTail string, timedOut, cancelled bool) domain.AttemptEnd
}

// HarnessAdapter is the HarnessRunner port of one harness: detect, declare capabilities, run
// non-interactively, capture events, and recognize quota failure. Cancellation is the executor's, by process group.
type HarnessAdapter interface {
	ID() string
	Name() string
	Detect(ctx context.Context, manualPath string) HarnessInfo
	ValidateProfile(info HarnessInfo, model, effort string) error
	Prepare(info HarnessInfo, req RunRequest) (Invocation, OutputParser, error)
	// UserItem looks up an agent or skill in the user's harness configuration, as the harness finds it for workspace, a
	// folder that is never empty. An error says the harness's list could not be read, which is not the item missing;
	// the discovery errors are what the harness reported it could not load on the way.
	UserItem(ctx context.Context, info HarnessInfo, kind, name, workspace string) (ResolvedItem, bool, []DiscoveryError, error)
	// UserItems lists the agents or skills of the user's harness configuration for workspace, each name once, with the
	// discovery errors the harness reported; an error says the list could not be read.
	UserItems(ctx context.Context, info HarnessInfo, kind, workspace string) ([]ResolvedItem, []DiscoveryError, error)
}

// DiscoveryError is what a harness reported it could not load while it listed the user's items, such as a SKILL.md
// whose front matter it rejects: the file, and the harness's own message.
type DiscoveryError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ItemReloader is a HarnessAdapter whose harness caches the user's items: ReloadUserItems makes its next list read them
// from disk again, as an explicit refresh asks.
type ItemReloader interface {
	ReloadUserItems()
}

// WorkspaceLister is a HarnessAdapter that finds the user's items of a kind in its harness's own list for a workspace,
// not in a folder of its configuration: ListsPerWorkspace says which kinds.
type WorkspaceLister interface {
	ListsPerWorkspace(kind string) bool
}

// WorkspaceFolderReader is a HarnessAdapter that finds the user's items of a kind in a folder of the workspace as well as
// in a folder of its configuration, as its harness loads them: ReadsWorkspace says which kinds.
type WorkspaceFolderReader interface {
	ReadsWorkspace(kind string) bool
}

// PluginLister is a HarnessAdapter that finds a plugin's items of a kind, named `<plugin>:<name>`, in its harness's list
// of the plugins it has enabled for a workspace, not in a folder: ListsPlugins says which kinds.
type PluginLister interface {
	ListsPlugins(kind string) bool
}

// HarnessCloser is a HarnessAdapter that keeps a process of its harness running between calls; Close ends it.
type HarnessCloser interface {
	Close()
}

// HarnessIdentifier is a HarnessAdapter that tells whether a file is its harness: an executable file whose --version
// output names it.
type HarnessIdentifier interface {
	Identify(ctx context.Context, path string) error
}

// ProfileWarner is a HarnessAdapter whose capabilities cannot always check a model and effort: ProfileWarning says so,
// and the save keeps them with that warning.
type ProfileWarner interface {
	ProfileWarning(info HarnessInfo, model, effort string) (domain.Text, bool)
}

// QuotaReader is a HarnessAdapter that reads its harness's quota without a model call. An error says the quota could
// not be read: the command failed, timed out, or printed what the adapter does not understand.
type QuotaReader interface {
	ReadQuota(ctx context.Context, info HarnessInfo) (domain.Quota, error)
}

// ExtraUsageReporter is a HarnessAdapter whose running harness reports when it turns to extra usage, in a HarnessQuota
// event, so the attempt can be ended then. Any other harness can be checked only before its attempt starts.
type ExtraUsageReporter interface {
	ReportsExtraUsage() bool
}

// SystemInfo reads the gauges of the sidebar.
type SystemInfo interface {
	Snapshot(dir string) map[string]any
}

// ToolProber reports git and gh.
type ToolProber interface {
	Probe(ctx context.Context) []ToolInfo
}

// WorkspaceReader reads, from an execution's workspace itself, which files an attempt changed there, whatever wrote
// them: the checkpoint taken before a switch.
type WorkspaceReader interface {
	// Mark is where a later Changes measures from, taken as an attempt starts: the time, and the HEAD of each Git
	// repository at the workspace's root or directly under it.
	Mark(ctx context.Context, workspace string) WorkspaceMark
	// Changes lists the files changed in the workspace since mark.
	Changes(ctx context.Context, workspace string, mark WorkspaceMark) *domain.Checkpoint
}

// WorkspaceMark is what a WorkspaceReader measures the changes of an attempt from: when the attempt started, by the
// machine's own clock, which file modification times follow, and the HEAD commit of each Git repository then, by its
// path relative to the workspace, "." for the workspace's root.
type WorkspaceMark struct {
	At    time.Time
	Heads map[string]string
}

// ToolInfo is what a ToolProber found of one tool, git or gh: where it is, its version, and its authentication.
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

// SetDiagnostic records what is wrong with the tool; Diagnostic is the en-US text.
func (t *ToolInfo) SetDiagnostic(text domain.Text) {
	t.Diagnostic, t.DiagnosticKey, t.DiagnosticParams = text.Message, text.Key, text.Params
}
