// Package domain holds HarmFlow's entities, invariants, and transitions. It imports nothing specific to a harness,
// GitHub, Tauri, or an operating system (ADR-0001).
package domain

import "strings"

const (
	APIVersion = "harmflow/v1alpha1"
	KindTask   = "Task"
)

// Task types (D-060). The behavior of each lives in its prompt, agents, and skills (D-064).
const (
	TypeGitHubIssueImplementation = "github-issue-implementation"
	TypeGeneral                   = "general"
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

type TaskMetadata struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

type TaskSpec struct {
	Enabled                   *bool                      `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Type                      string                     `yaml:"type" json:"type"`
	Prompt                    string                     `yaml:"prompt" json:"prompt"`
	Agents                    []string                   `yaml:"agents,omitempty" json:"agents,omitempty"`
	Skills                    []string                   `yaml:"skills,omitempty" json:"skills,omitempty"`
	Workspace                 *Workspace                 `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Schedule                  *Schedule                  `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	Concurrency               *Concurrency               `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	Harness                   HarnessSelection           `yaml:"harness" json:"harness"`
	Permissions               *Permissions               `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Execution                 *ExecutionLimits           `yaml:"execution,omitempty" json:"execution,omitempty"`
	Notifications             *Notifications             `yaml:"notifications,omitempty" json:"notifications,omitempty"`
	GitHubIssueImplementation *GitHubIssueImplementation `yaml:"githubIssueImplementation,omitempty" json:"githubIssueImplementation,omitempty"`
}

type Workspace struct {
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
}

type Schedule struct {
	Cron      string     `yaml:"cron,omitempty" json:"cron,omitempty"`
	Interval  string     `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timezone  string     `yaml:"timezone,omitempty" json:"timezone,omitempty"`
	MissedRun *MissedRun `yaml:"missedRun,omitempty" json:"missedRun,omitempty"`
}

type MissedRun struct {
	Policy string `yaml:"policy,omitempty" json:"policy,omitempty"`
}

type Concurrency struct {
	OverlappingRuns string `yaml:"overlappingRuns,omitempty" json:"overlappingRuns,omitempty"`
}

type HarnessProfile struct {
	Provider string `yaml:"provider" json:"provider"`
	Model    string `yaml:"model,omitempty" json:"model,omitempty"`
	Effort   string `yaml:"effort,omitempty" json:"effort,omitempty"`
}

type HarnessSelection struct {
	Preferred HarnessProfile  `yaml:"preferred" json:"preferred"`
	Fallback  *FallbackPolicy `yaml:"fallback,omitempty" json:"fallback,omitempty"`
}

type FallbackPolicy struct {
	On          []string         `yaml:"on,omitempty" json:"on,omitempty"`
	Candidates  []HarnessProfile `yaml:"candidates,omitempty" json:"candidates,omitempty"`
	MaxSwitches *int             `yaml:"maxSwitches,omitempty" json:"maxSwitches,omitempty"`
}

type Permissions struct {
	Profile             string             `yaml:"profile,omitempty" json:"profile,omitempty"`
	RestrictToWorkspace *bool              `yaml:"restrictToWorkspace,omitempty" json:"restrictToWorkspace,omitempty"`
	Network             string             `yaml:"network,omitempty" json:"network,omitempty"`
	Custom              *CustomPermissions `yaml:"custom,omitempty" json:"custom,omitempty"`
}

type CustomPermissions struct {
	AllowedTools  []string `yaml:"allowedTools,omitempty" json:"allowedTools,omitempty"`
	DeniedTools   []string `yaml:"deniedTools,omitempty" json:"deniedTools,omitempty"`
	WritablePaths []string `yaml:"writablePaths,omitempty" json:"writablePaths,omitempty"`
}

type ExecutionLimits struct {
	TimeoutMinutes     *int       `yaml:"timeoutMinutes,omitempty" json:"timeoutMinutes,omitempty"`
	CancelGraceSeconds *int       `yaml:"cancelGraceSeconds,omitempty" json:"cancelGraceSeconds,omitempty"`
	RateLimit          *RateLimit `yaml:"rateLimit,omitempty" json:"rateLimit,omitempty"`
}

type RateLimit struct {
	MaxRetries         *int `yaml:"maxRetries,omitempty" json:"maxRetries,omitempty"`
	InitialWaitSeconds *int `yaml:"initialWaitSeconds,omitempty" json:"initialWaitSeconds,omitempty"`
	MaxWaitSeconds     *int `yaml:"maxWaitSeconds,omitempty" json:"maxWaitSeconds,omitempty"`
}

type Notifications struct {
	Native *NativeNotifications `yaml:"native,omitempty" json:"native,omitempty"`
}

type NativeNotifications struct {
	Enabled *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Events  []string `yaml:"events,omitempty" json:"events,omitempty"`
}

type RepositoryTarget struct {
	Name       string `yaml:"name" json:"name"`
	LocalPath  string `yaml:"localPath" json:"localPath"`
	BaseBranch string `yaml:"baseBranch,omitempty" json:"baseBranch,omitempty"`
}

type GitHubIssueImplementation struct {
	Repositories []RepositoryTarget `yaml:"repositories" json:"repositories"`
	Queue        *IssueQueue        `yaml:"queue,omitempty" json:"queue,omitempty"`
	Delivery     *Delivery          `yaml:"delivery,omitempty" json:"delivery,omitempty"`
}

type IssueQueue struct {
	Ordering              string `yaml:"ordering,omitempty" json:"ordering,omitempty"`
	MaxDiscoveredPerCycle *int   `yaml:"maxDiscoveredPerCycle,omitempty" json:"maxDiscoveredPerCycle,omitempty"`
	MaxItemsPerCycle      *int   `yaml:"maxItemsPerCycle,omitempty" json:"maxItemsPerCycle,omitempty"`
	ResumeStrategy        string `yaml:"resumeStrategy,omitempty" json:"resumeStrategy,omitempty"`
	MaxResumeAttempts     *int   `yaml:"maxResumeAttempts,omitempty" json:"maxResumeAttempts,omitempty"`
	MaxFailedRetries      *int   `yaml:"maxFailedRetries,omitempty" json:"maxFailedRetries,omitempty"`
}

type Delivery struct {
	TestRepairAttempts *int `yaml:"testRepairAttempts,omitempty" json:"testRepairAttempts,omitempty"`
}

// DefaultNotifiedEvents are the events a task notifies when it chooses none (FS-011 FR-002).
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

// Profiles returns the preferred profile followed by the fallback candidates, defaults applied.
func (d TaskDocument) Profiles() []HarnessProfile {
	e := d.Effective()
	return append([]HarnessProfile{e.Spec.Harness.Preferred}, e.Spec.Harness.Fallback.Candidates...)
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

func cloneSlice[T any](s []T) []T {
	if s == nil {
		return nil
	}
	return append([]T(nil), s...)
}
