// Package domain holds HarmFlow's entities, invariants, and transitions. It imports nothing specific to a harness,
// GitHub, Tauri, or an operating system (ADR-001).
package domain

import "strings"

// APIVersion and KindTask are the apiVersion and kind of every task file (D-070).
const (
	APIVersion = "harmflow/v1alpha1"
	KindTask   = "Task"
)

// Task types (D-060). The behavior of each lives in its prompt, agents, and skills (D-064), except a script task's,
// which is its program: it runs instead of a harness session (FS-013).
const (
	TypeGitHubIssueImplementation = "github-issue-implementation"
	TypeGeneral                   = "general"
	TypeScriptTask                = "script"
)

// Operating systems a script task may declare (FS-013 FR-014).
const (
	SystemMacOS   = "macos"
	SystemLinux   = "linux"
	SystemWindows = "windows"
)

// Harness providers of the MVP (D-017).
const (
	HarnessClaudeCode = "claude-code"
	HarnessCodex      = "codex"
)

// DefaultModel and DefaultEffort leave the harness's own default in place.
const (
	DefaultModel  = "default"
	DefaultEffort = "default"
)

// The closed-set values of a task's fields, as contracts/schemas/task.schema.json names them: a schedule's missed-run
// policy, a task's overlapping runs, a permissions profile and network, what a fallback fails over on, and an issue
// queue's ordering and resume strategy.
const (
	MissedRunOnResume = "run_on_resume"
	MissedRunSkip     = "skip"

	OverlapQueue = "queue"
	OverlapSkip  = "skip"

	ProfileReadOnly       = "read_only"
	ProfileWorkspaceWrite = "workspace_write"
	ProfileCustom         = "custom"

	NetworkAllow = "allow"
	NetworkDeny  = "deny"

	FallbackOnQuota       = "quota_exhausted"
	FallbackOnUnavailable = "harness_unavailable"

	OrderingOldestFirst = "oldest_first"
	OrderingNewestFirst = "newest_first"

	ResumeFirst     = "resume_first"
	ResumeNewFirst  = "new_first"
	ResumeAlternate = "alternate"
)

// TaskDocument is one task file: apiVersion, kind, metadata, and spec (D-070). Optional scalars are pointers so a
// document keeps only what its author set; Effective fills the defaults in.
type TaskDocument struct {
	APIVersion string       `yaml:"apiVersion" json:"apiVersion"`
	Kind       string       `yaml:"kind" json:"kind"`
	Metadata   TaskMetadata `yaml:"metadata" json:"metadata"`
	Spec       TaskSpec     `yaml:"spec" json:"spec"`
}

// TaskMetadata names a task: its identifier, its display name, and an optional description.
type TaskMetadata struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// TaskSpec is a task's spec block. A script task has no prompt and no harness, so both are left out of its file.
type TaskSpec struct {
	Enabled                   *bool                      `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Type                      string                     `yaml:"type" json:"type"`
	Prompt                    string                     `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	Agents                    []string                   `yaml:"agents,omitempty" json:"agents,omitempty"`
	Skills                    []string                   `yaml:"skills,omitempty" json:"skills,omitempty"`
	Workspace                 *Workspace                 `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Schedule                  *Schedule                  `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	Concurrency               *Concurrency               `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	Harness                   HarnessSelection           `yaml:"harness,omitempty" json:"harness,omitzero"`
	Permissions               *Permissions               `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Execution                 *ExecutionLimits           `yaml:"execution,omitempty" json:"execution,omitempty"`
	Notifications             *Notifications             `yaml:"notifications,omitempty" json:"notifications,omitempty"`
	GitHubIssueImplementation *GitHubIssueImplementation `yaml:"githubIssueImplementation,omitempty" json:"githubIssueImplementation,omitempty"`
	Script                    *ScriptSpec                `yaml:"script,omitempty" json:"script,omitempty"`
}

// ScriptSpec is the program a script task runs: Command, an argument list started with no shell, or Inline, a script
// run through its interpreter (FS-013 FR-003, FR-004, FR-014).
type ScriptSpec struct {
	Command          []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Inline           *InlineScript     `yaml:"inline,omitempty" json:"inline,omitempty"`
	WorkingDirectory string            `yaml:"workingDirectory,omitempty" json:"workingDirectory,omitempty"`
	Environment      map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	Systems          []string          `yaml:"systems,omitempty" json:"systems,omitempty"`
}

// InlineScript is a script's source and the argument list of its interpreter; the daemon appends the path of the file
// it writes the source to.
type InlineScript struct {
	Interpreter []string `yaml:"interpreter" json:"interpreter"`
	Source      string   `yaml:"source" json:"source"`
}

// Workspace is where the harness session works; Path may use ${NAME} references and ~ (FS-001 FR-007).
type Workspace struct {
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
}

// Schedule is when a task fires: a 5-field cron expression or an interval, never both, in Timezone or the system's time
// zone (D-071). A task with no schedule runs only by hand.
type Schedule struct {
	Cron      string     `yaml:"cron,omitempty" json:"cron,omitempty"`
	Interval  string     `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timezone  string     `yaml:"timezone,omitempty" json:"timezone,omitempty"`
	MissedRun *MissedRun `yaml:"missedRun,omitempty" json:"missedRun,omitempty"`
}

// MissedRun is what becomes of the firings missed while the daemon or the machine was unavailable: one catch-up when
// it resumes, or none (D-071).
type MissedRun struct {
	Policy string `yaml:"policy,omitempty" json:"policy,omitempty"`
}

// Concurrency is whether a firing that arrives while the task runs waits in the queue or is skipped (D-072).
type Concurrency struct {
	OverlappingRuns string `yaml:"overlappingRuns,omitempty" json:"overlappingRuns,omitempty"`
}

// HarnessProfile is one harness a task may run with: its provider, and the model and effort to ask it for, where
// DefaultModel and DefaultEffort leave the harness's own.
type HarnessProfile struct {
	Provider string `yaml:"provider" json:"provider"`
	Model    string `yaml:"model,omitempty" json:"model,omitempty"`
	Effort   string `yaml:"effort,omitempty" json:"effort,omitempty"`
}

// HarnessSelection is the harness profile a task prefers and the policy for failing over from it (D-067).
type HarnessSelection struct {
	Preferred HarnessProfile  `yaml:"preferred" json:"preferred"`
	Fallback  *FallbackPolicy `yaml:"fallback,omitempty" json:"fallback,omitempty"`
}

// FallbackPolicy is when an execution fails over to the next candidate profile, and how often: On lists what triggers a
// failover, and MaxSwitches caps the switches of one execution (D-067).
type FallbackPolicy struct {
	On          ChosenList       `yaml:"on,omitempty" json:"on,omitzero"`
	Candidates  []HarnessProfile `yaml:"candidates,omitempty" json:"candidates,omitempty"`
	MaxSwitches *int             `yaml:"maxSwitches,omitempty" json:"maxSwitches,omitempty"`
}

// Permissions is what the harness may do, translated to each harness's native controls (D-068): a profile, whether its
// writes stay in the workspace, and its network access; Custom holds the lists of the custom profile.
type Permissions struct {
	Profile             string             `yaml:"profile,omitempty" json:"profile,omitempty"`
	RestrictToWorkspace *bool              `yaml:"restrictToWorkspace,omitempty" json:"restrictToWorkspace,omitempty"`
	Network             string             `yaml:"network,omitempty" json:"network,omitempty"`
	Custom              *CustomPermissions `yaml:"custom,omitempty" json:"custom,omitempty"`
}

// CustomPermissions is the tools a custom permissions profile allows and denies, and the paths it lets the harness
// write.
type CustomPermissions struct {
	AllowedTools  []string `yaml:"allowedTools,omitempty" json:"allowedTools,omitempty"`
	DeniedTools   []string `yaml:"deniedTools,omitempty" json:"deniedTools,omitempty"`
	WritablePaths []string `yaml:"writablePaths,omitempty" json:"writablePaths,omitempty"`
}

// ExecutionLimits bounds an execution: a timeout shared by all its attempts, the grace a process gets to end once it is
// asked to stop, and the retries after a rate limit.
type ExecutionLimits struct {
	TimeoutMinutes     *int       `yaml:"timeoutMinutes,omitempty" json:"timeoutMinutes,omitempty"`
	CancelGraceSeconds *int       `yaml:"cancelGraceSeconds,omitempty" json:"cancelGraceSeconds,omitempty"`
	RateLimit          *RateLimit `yaml:"rateLimit,omitempty" json:"rateLimit,omitempty"`
}

// RateLimit is how an attempt that hit a rate limit is retried on the same harness: up to MaxRetries times, waiting
// from InitialWaitSeconds, doubled each retry, up to MaxWaitSeconds (D-082).
type RateLimit struct {
	MaxRetries         *int `yaml:"maxRetries,omitempty" json:"maxRetries,omitempty"`
	InitialWaitSeconds *int `yaml:"initialWaitSeconds,omitempty" json:"initialWaitSeconds,omitempty"`
	MaxWaitSeconds     *int `yaml:"maxWaitSeconds,omitempty" json:"maxWaitSeconds,omitempty"`
}

// Notifications is how a task tells its user about its executions; native notifications are the only kind.
type Notifications struct {
	Native *NativeNotifications `yaml:"native,omitempty" json:"native,omitempty"`
}

// NativeNotifications is whether a task posts the operating system's notifications, and for which events; Events left
// out takes DefaultNotifiedEvents (FS-011 FR-002).
type NativeNotifications struct {
	Enabled *bool      `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Events  ChosenList `yaml:"events,omitempty" json:"events,omitzero"`
}

// ChosenList is a list whose empty value is a choice: a task that chose none says `[]`, and only a list left out (nil)
// takes the default. The file and the API keep `[]`, since yaml.v3's omitempty and encoding/json's omitzero ask
// IsZero, which is true for nil alone.
type ChosenList []string

// IsZero reports whether the list was left out, as yaml.v3's omitempty and encoding/json's omitzero ask.
func (l ChosenList) IsZero() bool { return l == nil }

// RepositoryTarget is a GitHub repository a task works on: its owner/repo name, the path of its local clone, and the
// branch its work starts from, DefaultBaseBranch when left out.
type RepositoryTarget struct {
	Name       string `yaml:"name" json:"name"`
	LocalPath  string `yaml:"localPath" json:"localPath"`
	BaseBranch string `yaml:"baseBranch,omitempty" json:"baseBranch,omitempty"`
}

// GitHubIssueImplementation is the parameters of the github-issue-implementation type: its repositories, its issue
// queue, and its delivery (FS-006, FS-007). Which issues are eligible is written in the prompt (D-093).
type GitHubIssueImplementation struct {
	Repositories []RepositoryTarget `yaml:"repositories" json:"repositories"`
	Queue        *IssueQueue        `yaml:"queue,omitempty" json:"queue,omitempty"`
	Delivery     *Delivery          `yaml:"delivery,omitempty" json:"delivery,omitempty"`
}

// IssueQueue is how a github-issue-implementation task picks its work items each time it fires: the order issues are
// read in, how many are discovered and worked, whether work to resume comes before new issues, and how often one item
// is resumed or retried before it needs a human.
type IssueQueue struct {
	Ordering              string `yaml:"ordering,omitempty" json:"ordering,omitempty"`
	MaxDiscoveredPerCycle *int   `yaml:"maxDiscoveredPerCycle,omitempty" json:"maxDiscoveredPerCycle,omitempty"`
	MaxItemsPerCycle      *int   `yaml:"maxItemsPerCycle,omitempty" json:"maxItemsPerCycle,omitempty"`
	ResumeStrategy        string `yaml:"resumeStrategy,omitempty" json:"resumeStrategy,omitempty"`
	MaxResumeAttempts     *int   `yaml:"maxResumeAttempts,omitempty" json:"maxResumeAttempts,omitempty"`
	MaxFailedRetries      *int   `yaml:"maxFailedRetries,omitempty" json:"maxFailedRetries,omitempty"`
}

// Delivery is how a work item is delivered: how many times the session tries to repair failing tests.
type Delivery struct {
	TestRepairAttempts *int `yaml:"testRepairAttempts,omitempty" json:"testRepairAttempts,omitempty"`
}

// DefaultNotifiedEvents are the events a task notifies when it leaves its events out; an empty list notifies none
// (FS-011 FR-002).
var DefaultNotifiedEvents = []string{
	EventExecutionCompleted, EventExecutionFailed, EventExecutionNeedsAttention, EventPullRequestCreated,
	EventHarnessNoAlternative,
}

// Defaults of D-082 (FS-001 FR-012).
const (
	DefaultTimeoutMinutes        = 60
	DefaultCancelGraceSeconds    = 30
	DefaultRateLimitMaxRetries   = 5
	DefaultRateLimitInitialWait  = 60
	DefaultRateLimitMaxWait      = 900
	DefaultMaxSwitches           = 1
	DefaultMaxDiscoveredPerCycle = 10
	DefaultMaxItemsPerCycle      = 1
	DefaultMaxResumeAttempts     = 3
	DefaultMaxFailedRetries      = 3
	DefaultTestRepairAttempts    = 2
	DefaultBaseBranch            = "main"
)

// Effective returns a copy of the document with every default applied, so no optional field is nil.
func (d TaskDocument) Effective() TaskDocument {
	e := d.clone()
	s := &e.Spec
	if s.Enabled == nil {
		s.Enabled = ptr(true)
	}
	if s.Agents == nil {
		s.Agents = []string{}
	}
	if s.Skills == nil {
		s.Skills = []string{}
	}
	if s.Workspace == nil {
		s.Workspace = &Workspace{}
	}
	if s.Schedule != nil {
		if s.Schedule.MissedRun == nil {
			s.Schedule.MissedRun = &MissedRun{}
		}
		if s.Schedule.MissedRun.Policy == "" {
			s.Schedule.MissedRun.Policy = MissedRunOnResume
		}
	}
	if s.Concurrency == nil {
		s.Concurrency = &Concurrency{}
	}
	if s.Concurrency.OverlappingRuns == "" {
		s.Concurrency.OverlappingRuns = OverlapQueue
	}
	if s.Type == TypeScriptTask {
		// No harness: the block stays empty, with no harness default filled in, and HarnessFallback reads nil.
		if s.Script == nil {
			s.Script = &ScriptSpec{}
		}
		if s.Script.Environment == nil {
			s.Script.Environment = map[string]string{}
		}
		if s.Script.Systems == nil {
			s.Script.Systems = []string{}
		}
	} else {
		s.Harness.Preferred = s.Harness.Preferred.withDefaults()
		if s.Harness.Fallback == nil {
			s.Harness.Fallback = &FallbackPolicy{}
		}
		if s.Harness.Fallback.On == nil {
			s.Harness.Fallback.On = []string{FallbackOnQuota, FallbackOnUnavailable}
		}
		if s.Harness.Fallback.Candidates == nil {
			s.Harness.Fallback.Candidates = []HarnessProfile{}
		}
		for i := range s.Harness.Fallback.Candidates {
			s.Harness.Fallback.Candidates[i] = s.Harness.Fallback.Candidates[i].withDefaults()
		}
		if s.Harness.Fallback.MaxSwitches == nil {
			s.Harness.Fallback.MaxSwitches = ptr(DefaultMaxSwitches)
		}
	}
	if s.Permissions == nil {
		s.Permissions = &Permissions{}
	}
	if s.Permissions.Profile == "" {
		s.Permissions.Profile = ProfileWorkspaceWrite
	}
	if s.Permissions.RestrictToWorkspace == nil {
		s.Permissions.RestrictToWorkspace = ptr(true)
	}
	if s.Permissions.Network == "" {
		s.Permissions.Network = NetworkAllow
	}
	if s.Permissions.Custom == nil {
		s.Permissions.Custom = &CustomPermissions{}
	}
	if s.Execution == nil {
		s.Execution = &ExecutionLimits{}
	}
	setDefault(&s.Execution.TimeoutMinutes, DefaultTimeoutMinutes)
	setDefault(&s.Execution.CancelGraceSeconds, DefaultCancelGraceSeconds)
	if s.Execution.RateLimit == nil {
		s.Execution.RateLimit = &RateLimit{}
	}
	setDefault(&s.Execution.RateLimit.MaxRetries, DefaultRateLimitMaxRetries)
	setDefault(&s.Execution.RateLimit.InitialWaitSeconds, DefaultRateLimitInitialWait)
	setDefault(&s.Execution.RateLimit.MaxWaitSeconds, DefaultRateLimitMaxWait)
	if s.Notifications == nil {
		s.Notifications = &Notifications{}
	}
	if s.Notifications.Native == nil {
		s.Notifications.Native = &NativeNotifications{}
	}
	if s.Notifications.Native.Enabled == nil {
		s.Notifications.Native.Enabled = ptr(true)
	}
	if s.Notifications.Native.Events == nil {
		s.Notifications.Native.Events = append([]string(nil), DefaultNotifiedEvents...)
	}
	if g := s.GitHubIssueImplementation; g != nil {
		for i := range g.Repositories {
			if g.Repositories[i].BaseBranch == "" {
				g.Repositories[i].BaseBranch = DefaultBaseBranch
			}
		}
		if g.Queue == nil {
			g.Queue = &IssueQueue{}
		}
		if g.Queue.Ordering == "" {
			g.Queue.Ordering = OrderingOldestFirst
		}
		if g.Queue.ResumeStrategy == "" {
			g.Queue.ResumeStrategy = ResumeFirst
		}
		setDefault(&g.Queue.MaxDiscoveredPerCycle, DefaultMaxDiscoveredPerCycle)
		setDefault(&g.Queue.MaxItemsPerCycle, DefaultMaxItemsPerCycle)
		setDefault(&g.Queue.MaxResumeAttempts, DefaultMaxResumeAttempts)
		setDefault(&g.Queue.MaxFailedRetries, DefaultMaxFailedRetries)
		if g.Delivery == nil {
			g.Delivery = &Delivery{}
		}
		setDefault(&g.Delivery.TestRepairAttempts, DefaultTestRepairAttempts)
	}
	return e
}

func (p HarnessProfile) withDefaults() HarnessProfile {
	if p.Model == "" {
		p.Model = DefaultModel
	}
	if p.Effort == "" {
		p.Effort = DefaultEffort
	}
	return p
}

// Profiles returns the preferred profile followed by the fallback candidates, defaults applied; a script task has none.
func (d TaskDocument) Profiles() []HarnessProfile {
	if d.IsScript() {
		return nil
	}
	e := d.Effective()
	return append([]HarnessProfile{e.Spec.Harness.Preferred}, e.Spec.Harness.Fallback.Candidates...)
}

// IsScript reports whether the task runs a program instead of a harness session (FS-013).
func (d TaskDocument) IsScript() bool {
	return d.Spec.Type == TypeScriptTask
}

// RunsOn reports whether the task may run on a system: a script task only on those it declares, when it declares any
// (FS-013 FR-014); every other task everywhere.
func (d TaskDocument) RunsOn(system string) bool {
	if !d.IsScript() || d.Spec.Script == nil || len(d.Spec.Script.Systems) == 0 {
		return true
	}
	for _, s := range d.Spec.Script.Systems {
		if s == system {
			return true
		}
	}
	return false
}

// ProgramName is the program a script task starts, for display: the first element of its command, or its interpreter.
func (d TaskDocument) ProgramName() string {
	s := d.Spec.Script
	switch {
	case s == nil:
		return ""
	case len(s.Command) > 0:
		return s.Command[0]
	case s.Inline != nil && len(s.Inline.Interpreter) > 0:
		return s.Inline.Interpreter[0]
	}
	return ""
}

// IsEnabled reads the enabled state with its default.
func (d TaskDocument) IsEnabled() bool {
	return d.Spec.Enabled == nil || *d.Spec.Enabled
}

// ScheduleRule returns the schedule expression for display, empty for a manual-only task.
func (d TaskDocument) ScheduleRule() string {
	if d.Spec.Schedule == nil {
		return ""
	}
	if d.Spec.Schedule.Cron != "" {
		return d.Spec.Schedule.Cron
	}
	return d.Spec.Schedule.Interval
}

// Repositories lists the repository targets of a type that takes them.
func (d TaskDocument) Repositories() []RepositoryTarget {
	if d.Spec.GitHubIssueImplementation == nil {
		return nil
	}
	return d.Spec.GitHubIssueImplementation.Repositories
}

// TypeParameters returns the block of the task's type, or nil for a type with none.
func (d TaskDocument) TypeParameters() any {
	switch d.Spec.Type {
	case TypeGitHubIssueImplementation:
		return d.Effective().Spec.GitHubIssueImplementation
	case TypeScriptTask:
		return d.Effective().Spec.Script
	default:
		return nil
	}
}

// ExpandPath replaces ${NAME} references and a leading ~ in a task path (FS-001 FR-007).
func ExpandPath(path, home string, vars map[string]string, env func(string) string) (string, error) {
	out := path
	if strings.HasPrefix(out, "~/") || out == "~" {
		out = home + strings.TrimPrefix(out, "~")
	}
	var missing []string
	var b strings.Builder
	for i := 0; i < len(out); i++ {
		if out[i] == '$' && i+1 < len(out) && out[i+1] == '{' {
			end := strings.IndexByte(out[i:], '}')
			if end < 0 {
				return "", TextErr("path_variable_unterminated", `unterminated variable reference in "{{path}}"`, "path", path)
			}
			name := out[i+2 : i+end]
			val, ok := vars[name]
			if !ok && env != nil {
				val = env(name)
				ok = val != ""
			}
			if name == "HOME" && !ok {
				val, ok = home, true
			}
			if !ok {
				missing = append(missing, name)
			}
			b.WriteString(val)
			i += end
			continue
		}
		b.WriteByte(out[i])
	}
	if len(missing) > 0 {
		return "", TextErr("path_variable_undefined", `undefined path variable {{names}} in "{{path}}"; define it in Settings`,
			"names", strings.Join(missing, ", "), "path", path)
	}
	return b.String(), nil
}

func (d TaskDocument) clone() TaskDocument {
	c := d
	s := &c.Spec
	s.Enabled = clonePtr(s.Enabled)
	s.Agents = cloneSlice(s.Agents)
	s.Skills = cloneSlice(s.Skills)
	if s.Workspace != nil {
		w := *s.Workspace
		s.Workspace = &w
	}
	if s.Schedule != nil {
		sc := *s.Schedule
		if sc.MissedRun != nil {
			m := *sc.MissedRun
			sc.MissedRun = &m
		}
		s.Schedule = &sc
	}
	if s.Concurrency != nil {
		cc := *s.Concurrency
		s.Concurrency = &cc
	}
	if f := s.Harness.Fallback; f != nil {
		fc := *f
		fc.On = cloneSlice(f.On)
		fc.Candidates = cloneSlice(f.Candidates)
		fc.MaxSwitches = clonePtr(f.MaxSwitches)
		s.Harness.Fallback = &fc
	}
	if p := s.Permissions; p != nil {
		pc := *p
		pc.RestrictToWorkspace = clonePtr(p.RestrictToWorkspace)
		if p.Custom != nil {
			cu := CustomPermissions{
				AllowedTools:  cloneSlice(p.Custom.AllowedTools),
				DeniedTools:   cloneSlice(p.Custom.DeniedTools),
				WritablePaths: cloneSlice(p.Custom.WritablePaths),
			}
			pc.Custom = &cu
		}
		s.Permissions = &pc
	}
	if x := s.Execution; x != nil {
		xc := ExecutionLimits{TimeoutMinutes: clonePtr(x.TimeoutMinutes), CancelGraceSeconds: clonePtr(x.CancelGraceSeconds)}
		if x.RateLimit != nil {
			xc.RateLimit = &RateLimit{
				MaxRetries:         clonePtr(x.RateLimit.MaxRetries),
				InitialWaitSeconds: clonePtr(x.RateLimit.InitialWaitSeconds),
				MaxWaitSeconds:     clonePtr(x.RateLimit.MaxWaitSeconds),
			}
		}
		s.Execution = &xc
	}
	if n := s.Notifications; n != nil {
		nc := Notifications{}
		if n.Native != nil {
			nc.Native = &NativeNotifications{Enabled: clonePtr(n.Native.Enabled), Events: cloneSlice(n.Native.Events)}
		}
		s.Notifications = &nc
	}
	if g := s.GitHubIssueImplementation; g != nil {
		gc := GitHubIssueImplementation{Repositories: cloneSlice(g.Repositories)}
		if g.Queue != nil {
			q := *g.Queue
			q.MaxDiscoveredPerCycle = clonePtr(g.Queue.MaxDiscoveredPerCycle)
			q.MaxItemsPerCycle = clonePtr(g.Queue.MaxItemsPerCycle)
			q.MaxResumeAttempts = clonePtr(g.Queue.MaxResumeAttempts)
			q.MaxFailedRetries = clonePtr(g.Queue.MaxFailedRetries)
			gc.Queue = &q
		}
		if g.Delivery != nil {
			gc.Delivery = &Delivery{TestRepairAttempts: clonePtr(g.Delivery.TestRepairAttempts)}
		}
		s.GitHubIssueImplementation = &gc
	}
	if sc := s.Script; sc != nil {
		cc := ScriptSpec{Command: cloneSlice(sc.Command), WorkingDirectory: sc.WorkingDirectory, Systems: cloneSlice(sc.Systems)}
		if sc.Inline != nil {
			cc.Inline = &InlineScript{Interpreter: cloneSlice(sc.Inline.Interpreter), Source: sc.Inline.Source}
		}
		if sc.Environment != nil {
			cc.Environment = make(map[string]string, len(sc.Environment))
			for k, v := range sc.Environment {
				cc.Environment[k] = v
			}
		}
		s.Script = &cc
	}
	return c
}

func ptr[T any](v T) *T { return &v }

func setDefault(p **int, v int) {
	if *p == nil {
		*p = ptr(v)
	}
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// cloneSlice keeps nil apart from an empty list: nil is a list left out, which Effective fills in, and an empty one is
// a choice it keeps.
func cloneSlice[T any](s []T) []T {
	if s == nil {
		return nil
	}
	return append(make([]T, 0, len(s)), s...)
}
