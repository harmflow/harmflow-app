// Package codex is the HarnessRunner adapter of the OpenAI Codex CLI: `codex exec --json`, with the permission
// profile translated to Codex's own sandbox modes. Codex has no session plugin mechanism, so built-in agents and
// skills are delivered as files of the execution and named in the session's instructions.
package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/catalog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/classify"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

const (
	// ID is the harness's identifier, as a task's provider names it.
	ID = domain.HarnessCodex
	// Name is the harness's display name.
	Name = "Codex"
	// TestedUpTo is the newest version HarmFlow was tested with; a newer one runs as untested.
	TestedUpTo = "0.163.0"
)

var requiredFlags = []string{"--json", "--sandbox", "--cd", "--skip-git-repo-check"}

// Adapter is the HarnessAdapter of the Codex CLI. It looks for the CLI on PathEnv, then in KnownLocations, and runs its
// probes with Env; the user's agents and skills, its models cache, and its configuration are read from the Codex home
// under Home, or CODEX_HOME.
type Adapter struct {
	Home    string
	PathEnv string
	Env     []string
	// KnownLocations are searched after PATH; nil is the usual install locations, an empty list none.
	KnownLocations []string
}

var (
	_ ports.HarnessAdapter    = (*Adapter)(nil)
	_ ports.HarnessIdentifier = (*Adapter)(nil)
	_ ports.ProfileWarner     = (*Adapter)(nil)
)

// ID implements ports.HarnessAdapter: the harness's identifier.
func (a *Adapter) ID() string { return ID }

// Name implements ports.HarnessAdapter: the harness's display name.
func (a *Adapter) Name() string { return Name }

func (a *Adapter) codexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(a.Home, ".codex")
}

func (a *Adapter) knownLocations() []string {
	if a.KnownLocations != nil {
		return a.KnownLocations
	}
	return []string{
		"/Applications/Codex.app/Contents/Resources/codex-cli/bin/codex",
		filepath.Join(a.Home, "Applications", "Codex.app", "Contents", "Resources", "codex-cli", "bin", "codex"),
		"/opt/homebrew/bin/codex",
		"/usr/local/bin/codex",
		filepath.Join(a.Home, ".local", "bin", "codex"),
		filepath.Join(a.Home, ".npm-global", "bin", "codex"),
		filepath.Join(a.Home, ".bun", "bin", "codex"),
	}
}

var versionRe = regexp.MustCompile(`(\d+\.\d+\.\d+)`)

// Detect implements ports.HarnessAdapter: it finds the CLI, the path set by hand when there is one, then checks its
// version, the flags HarmFlow needs, the models and efforts of its models cache, and its sign-in, and says how ready it
// is.
func (a *Adapter) Detect(ctx context.Context, manualPath string) ports.HarnessInfo {
	info := ports.HarnessInfo{ID: ID, Name: Name, Authentication: "unknown", Readiness: "not_installed",
		Models: []ports.ModelInfo{}, Efforts: []string{}, EnforceableControls: []string{}, Installations: []string{},
		LastCheckedAt: time.Now(), Features: map[string]bool{}}
	found := procutil.LookPath("codex", a.PathEnv, a.knownLocations())
	info.Installations = append(info.Installations, found...)
	candidates := found
	switch {
	case manualPath != "":
		// A path set by hand is offered with the installations found, once.
		if !slices.Contains(found, manualPath) {
			info.Installations = append(info.Installations, manualPath)
		}
		info.Installation, info.Path, info.PathSource = manualPath, manualPath, "manual"
		if !procutil.Executable(manualPath) {
			info.SetDiagnostic(domain.T("harness_manual_path_invalid", "the path set by hand does not exist or is not a file"))
			return info
		}
		candidates = []string{manualPath}
	case len(found) == 0:
		info.SetDiagnostic(domain.T("codex_not_found",
			"codex was not found on PATH, in Codex.app, or in the usual install locations"))
		return info
	}
	// Discovery takes the first file that names Codex, skipping one that does not.
	out, chosen, err := a.firstIdentified(ctx, candidates)
	info.Installation, info.Path = chosen, chosen
	if manualPath == "" {
		info.PathSource = "known_location"
		for _, d := range strings.Split(a.PathEnv, ":") {
			if d == filepath.Dir(chosen) {
				info.PathSource = "path"
			}
		}
	}
	info.Detected = true
	info.Readiness = "installed"
	if err != nil {
		info.SetDiagnostic(domain.TextOf(err))
		return info
	}
	if m := versionRe.FindStringSubmatch(out); m != nil {
		info.Version = m[1]
		info.Readiness = "version_identified"
	}
	// Help that cannot be read lists no flags, so each feature reads as missing.
	help, _, _ := procutil.Output(ctx, 15*time.Second, a.Env, info.Path, "exec", "--help")
	for _, f := range append(requiredFlags, "--add-dir", "--ephemeral", "--model", "--color") {
		info.Features[f] = strings.Contains(help, f)
	}
	info.VersionSupport = "supported"
	for _, f := range requiredFlags {
		if !info.Features[f] {
			info.VersionSupport = "below_minimum"
			info.Readiness = "not_ready"
			info.SetDiagnostic(domain.T("harness_feature_missing", "this {{harness}} version lacks {{feature}}, which HarmFlow needs",
				"harness", Name, "feature", "exec "+f))
		}
	}
	if info.VersionSupport == "supported" {
		switch {
		case info.Version == "":
			// No number to check against the tested range; the help text passed, so it runs with a warning.
			info.VersionSupport = "unknown"
		case compareVersions(info.Version, TestedUpTo) > 0:
			info.VersionSupport = "untested_newer"
		}
	}
	info.Models, info.Efforts, info.CapabilitySource = a.models()
	info.EnforceableControls = []string{"sandbox_read_only", "sandbox_workspace_write", "network_access", "writable_roots"}

	// Signed out only when the CLI says so, which it does exiting 1; a probe killed at its timeout, or failing
	// otherwise, leaves the sign-in unknown.
	status, statusErr, err := procutil.Output(ctx, 20*time.Second, a.Env, info.Path, "login", "status")
	switch {
	case strings.Contains(strings.ToLower(status+statusErr), "not logged in"):
		info.Authentication = "invalid"
	case err == nil:
		info.Authentication = "valid"
	}
	if info.Readiness == "not_ready" {
		return info
	}
	switch info.Authentication {
	case "valid":
		info.Readiness = "ready"
	case "invalid":
		info.Readiness = "not_ready"
		info.SetDiagnostic(domain.T("not_signed_in", "{{name}} is not signed in; run `{{command}}` in a terminal",
			"name", Name, "command", "codex login"))
	default:
		// The CLI cannot tell; let the first execution prove it rather than blocking on an unknown.
		info.Readiness = "ready"
		info.SetDiagnostic(domain.T("authentication_unverified", "authentication could not be verified"))
	}
	return info
}

// identifies reports whether the output of --version names Codex, as "codex-cli 0.163.0" does.
func identifies(out string) bool { return strings.Contains(strings.ToLower(out), "codex") }

// Identify checks that path is an executable file whose --version output names Codex.
func (a *Adapter) Identify(ctx context.Context, path string) error {
	_, err := procutil.IdentifyHarness(ctx, a.Env, path, Name, "codex", identifies)
	return err
}

// firstIdentified returns the first candidate that names Codex, with its --version output, or the first candidate and
// why it failed when none does.
func (a *Adapter) firstIdentified(ctx context.Context, candidates []string) (string, string, error) {
	var first error
	for _, c := range candidates {
		out, err := procutil.IdentifyHarness(ctx, a.Env, c, Name, "codex", identifies)
		if err == nil {
			return out, c, nil
		}
		if first == nil {
			first = err
		}
	}
	return "", candidates[0], first
}

// models reads the model list the CLI itself caches, with the efforts each accepts.
func (a *Adapter) models() ([]ports.ModelInfo, []string, string) {
	defaults := []string{domain.DefaultEffort}
	models := []ports.ModelInfo{{ID: domain.DefaultModel, Efforts: defaults}}
	data, err := os.ReadFile(filepath.Join(a.codexHome(), "models_cache.json"))
	if err != nil {
		return models, defaults, "declared"
	}
	var cache struct {
		Models []struct {
			Slug   string `json:"slug"`
			ID     string `json:"id"`
			Levels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &cache) != nil {
		return models, defaults, "declared"
	}
	all := map[string]bool{}
	for _, m := range cache.Models {
		id := m.Slug
		if id == "" {
			id = m.ID
		}
		if id == "" {
			continue
		}
		efforts := []string{domain.DefaultEffort}
		for _, l := range m.Levels {
			efforts = append(efforts, l.Effort)
			all[l.Effort] = true
		}
		models = append(models, ports.ModelInfo{ID: id, Efforts: efforts})
	}
	union := []string{domain.DefaultEffort}
	order := []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
	for _, e := range order {
		if all[e] {
			union = append(union, e)
			delete(all, e)
		}
	}
	rest := make([]string, 0, len(all))
	for e := range all {
		rest = append(rest, e)
	}
	sort.Strings(rest)
	return models, append(union, rest...), "reported"
}

// ValidateProfile implements ports.HarnessAdapter: when the models cache was read, the model is one it lists, and an
// effort other than default is one that model accepts. Otherwise nothing is checked, and ProfileWarning says so.
func (a *Adapter) ValidateProfile(info ports.HarnessInfo, model, effort string) error {
	if !info.Detected || info.CapabilitySource != "reported" {
		return nil
	}
	var chosen *ports.ModelInfo
	for i := range info.Models {
		if info.Models[i].ID == model || (model == "" && info.Models[i].ID == domain.DefaultModel) {
			chosen = &info.Models[i]
		}
	}
	if chosen == nil {
		ids := make([]string, 0, len(info.Models))
		for _, m := range info.Models {
			ids = append(ids, m.ID)
		}
		return domain.TextErr("codex_model_unknown",
			`Codex {{version}} does not offer the model "{{model}}"; it offers {{models}}`,
			"version", info.Version, "model", model, "models", strings.Join(ids, ", "))
	}
	if effort == "" || effort == domain.DefaultEffort {
		return nil
	}
	efforts := chosen.Efforts
	if chosen.ID == domain.DefaultModel {
		efforts = info.Efforts
	}
	for _, e := range efforts {
		if e == effort {
			return nil
		}
	}
	return domain.TextErr("codex_effort_invalid",
		`Codex model "{{model}}" does not accept the effort "{{effort}}"; it accepts {{efforts}}`,
		"model", chosen.ID, "effort", effort, "efforts", strings.Join(efforts, ", "))
}

// ProfileWarning says a model or effort other than default goes unchecked when Codex has no readable models cache, so
// its capabilities are only declared.
func (a *Adapter) ProfileWarning(info ports.HarnessInfo, model, effort string) (domain.Text, bool) {
	if !info.Detected || info.CapabilitySource == "reported" {
		return domain.Text{}, false
	}
	if (model == "" || model == domain.DefaultModel) && (effort == "" || effort == domain.DefaultEffort) {
		return domain.Text{}, false
	}
	return domain.T("codex_profile_unchecked",
		"Codex's list of models could not be read, so the model and effort were not checked"), true
}

func (a *Adapter) userDir() catalog.Dir { return catalog.Dir{Root: a.codexHome()} }

// UserItem implements ports.HarnessAdapter, looking the item up in the Codex home.
func (a *Adapter) UserItem(kind, name string) (ports.ResolvedItem, bool) {
	return a.userDir().Item(kind, name)
}

// UserItems implements ports.HarnessAdapter: the agents or skills of the Codex home.
func (a *Adapter) UserItems(kind string) []ports.ResolvedItem { return a.userDir().Items(kind) }

// Prepare implements ports.HarnessAdapter: a `codex exec --json` invocation with the model and effort asked for and the
// permission profile translated to a sandbox, with the restrictions it enforces and those it cannot. Its stdin is the
// instructions, which name the built-ins as copied into the execution's directory.
func (a *Adapter) Prepare(info ports.HarnessInfo, req ports.RunRequest) (ports.Invocation, ports.OutputParser, error) {
	args := []string{"exec", "--json", "--skip-git-repo-check", "--cd", req.WorkDir}
	if info.Features["--ephemeral"] {
		args = append(args, "--ephemeral")
	}
	if info.Features["--color"] {
		args = append(args, "--color", "never")
	}
	if req.Model != "" && req.Model != domain.DefaultModel {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" && req.Effort != domain.DefaultEffort {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", req.Effort))
	}
	args = append(args, "-c", `approval_policy="never"`)

	perm := req.Permissions
	network := perm.Network != domain.NetworkDeny
	var restrictions []ports.Restriction
	dirs := append([]string{}, req.AdditionalDirs...)
	switch perm.Profile {
	case domain.ProfileReadOnly:
		args = append(args, "--sandbox", "read-only")
		restrictions = append(restrictions,
			ports.Restriction{Name: "read_only", Enforced: true,
				Text: domain.Text{Message: "Codex's read-only sandbox blocks every write"}},
			ports.Restriction{Name: "network", Enforced: true,
				Text: domain.Text{Message: "Codex's read-only sandbox blocks network access"}})
	default:
		args = append(args, "--sandbox", "workspace-write",
			"-c", fmt.Sprintf("sandbox_workspace_write.network_access=%t", network))
		restrictions = append(restrictions, ports.Restriction{Name: "restrictToWorkspace", Enforced: true,
			Text: domain.Text{
				Message: "Codex's workspace-write sandbox confines writes to the workspace and the added directories"}})
		if !network {
			restrictions = append(restrictions, ports.Restriction{Name: "network", Enforced: true,
				Text: domain.Text{Message: "Codex's sandbox blocks network access"}})
		}
		if perm.Profile == domain.ProfileCustom {
			dirs = append(dirs, perm.Custom.WritablePaths...)
			if len(perm.Custom.AllowedTools) > 0 || len(perm.Custom.DeniedTools) > 0 {
				restrictions = append(restrictions, ports.Restriction{Name: "tool_allowlist", Enforced: false,
					Text: domain.T("restriction_codex_tool_lists",
						"Codex has no per-tool allow or deny list; the custom tool lists are not enforced")})
			}
		}
	}
	if req.ExecutionDir != "" {
		dirs = append(dirs, req.ExecutionDir)
	}
	seen := map[string]bool{req.WorkDir: true}
	for _, d := range dirs {
		if d != "" && !seen[d] && info.Features["--add-dir"] {
			seen[d] = true
			args = append(args, "--add-dir", d)
		}
	}
	args = append(args, "-")

	req, err := deliverBuiltins(req)
	if err != nil {
		return ports.Invocation{}, nil, err
	}
	inv := ports.Invocation{Path: info.Path, Args: args, Env: req.Env, Dir: req.WorkDir, Stdin: instructions(req),
		Restrictions: restrictions}
	p := &parser{model: req.Model, modelSource: domain.ModelFromTask}
	if req.Model == "" || req.Model == domain.DefaultModel {
		// Codex's output never names its model: a task left at the default runs the one its configuration sets.
		p.model, p.modelSource = a.configModel(), domain.ModelFromConfig
		if p.model == "" {
			p.modelSource = ""
		}
	}
	return inv, p, nil
}

var configModelRe = regexp.MustCompile(`^\s*model\s*=\s*(?:"([^"]*)"|'([^']*)')`)

// configModel is the top-level `model` of $CODEX_HOME/config.toml, the model Codex runs when none is asked for; the
// keys of a table, such as a profile's, are not read.
func (a *Adapter) configModel() string {
	data, err := os.ReadFile(filepath.Join(a.codexHome(), "config.toml"))
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "[") {
			return ""
		}
		if m := configModelRe.FindStringSubmatch(l); m != nil {
			return m[1] + m[2]
		}
	}
	return ""
}

// deliverBuiltins copies the built-in skills and agents into the execution's directory, and points the request at the
// copies, so an app update during the execution cannot change what the session reads. The user's own items stay where
// they are.
func deliverBuiltins(req ports.RunRequest) (ports.RunRequest, error) {
	dir := filepath.Join(req.ExecutionDir, "codex-builtins")
	skills := append([]ports.ResolvedItem{}, req.Skills...)
	for i, s := range skills {
		if !s.Builtin {
			continue
		}
		dst := filepath.Join(dir, "skills", s.Name)
		if err := catalog.CopyTree(s.Path, dst); err != nil {
			return req, domain.TextErr("skill_delivery_failed", "delivering skill {{name}}: {{detail}}",
				"name", s.Name, "detail", err.Error())
		}
		skills[i].Path = dst
	}
	agents := append([]ports.ResolvedItem{}, req.Agents...)
	for i := range agents {
		if !agents[i].Builtin {
			continue
		}
		dst := filepath.Join(dir, "agents", agents[i].Name+".md")
		if err := catalog.CopyTree(agents[i].Path, dst); err != nil {
			return req, domain.TextErr("agent_delivery_failed", "delivering agent {{name}}: {{detail}}",
				"name", agents[i].Name, "detail", err.Error())
		}
		agents[i].Path = dst
	}
	req.Skills, req.Agents = skills, agents
	return req, nil
}

// instructions composes the session's input: HarmFlow's context, the agents and skills available, and the task's
// prompt. No agent is inlined as the session's role: the task's prompt carries it. Codex reads a skill by opening its
// SKILL.md, the way it loads its own skills.
func instructions(req ports.RunRequest) string {
	var b strings.Builder
	if req.Context != "" {
		b.WriteString(req.Context)
		b.WriteString("\n\n")
	}
	if len(req.Agents) > 0 {
		b.WriteString("## Agents\n\nThese agents are available as instructions you may follow when their role fits.\n\n")
		for _, ag := range req.Agents {
			fmt.Fprintf(&b, "- %s — %s — %s\n", ag.Name, ag.Description, ag.Path)
		}
		b.WriteString("\n")
	}
	if len(req.Skills) > 0 {
		b.WriteString("## Skills\n\nThese skills apply to this task. Before you act on one, read its SKILL.md in full and follow it.\n\n")
		for _, s := range req.Skills {
			fmt.Fprintf(&b, "- %s — %s — %s\n", s.Name, s.Description, filepath.Join(s.Path, "SKILL.md"))
		}
		b.WriteString("\n")
	}
	b.WriteString("## Task\n\n")
	b.WriteString(req.Prompt)
	b.WriteString("\n")
	return b.String()
}

type parser struct {
	sessionID   string
	completed   bool
	failure     string
	lastText    string
	model       string // the model that prices the attempt, empty when nothing names it
	modelSource string
	reported    bool // a turn reported its usage
	tokens      domain.Tokens
	prices      domain.PriceTable // what prices the usage; Prices when it holds none
	waiting     bool              // the last line Codex printed was a wait for the network
}

var _ ports.OutputParser = (*parser)(nil)

// turnUsage is a turn's usage. As in OpenAI's API, input_tokens include the tokens read from and written to the cache,
// so the plain input is what remains; output_tokens include the reasoning tokens, which bill as output. Codex does not
// say how long its writes live: they are priced at the default, 30-minute lifetime.
type turnUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
}

type item struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Command  string `json:"command"`
	Output   string `json:"aggregated_output"`
	ExitCode *int   `json:"exit_code"`
	Status   string `json:"status"`
	Text     string `json:"text"`
	Message  string `json:"message"`
	Changes  []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
	Server string `json:"server"`
	Tool   string `json:"tool"`
	Query  string `json:"query"`
}

type line struct {
	Type     string     `json:"type"`
	ThreadID string     `json:"thread_id"`
	Item     *item      `json:"item"`
	Message  string     `json:"message"`
	Usage    *turnUsage `json:"usage"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *parser) Line(raw []byte) []ports.HarnessEvent {
	var l line
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		p.waiting = false
		return []ports.HarnessEvent{{Kind: ports.HarnessWarning, Message: classify.Truncate(string(raw), 2000)}}
	}
	// Codex repeats the wait every few seconds and never exits while it lasts: only the first line of a stretch is an
	// event, and any other line ends the stretch.
	wasWaiting := p.waiting
	p.waiting = l.Type == "error" && networkWaitRe.MatchString(l.Message)
	switch l.Type {
	case "thread.started":
		p.sessionID = l.ThreadID
		return []ports.HarnessEvent{{Kind: ports.HarnessStarted, SessionID: l.ThreadID}}
	case "turn.completed":
		p.completed = true
		// One turn per `codex exec`: its usage arrives once, at the end of the attempt.
		if u := l.Usage; u != nil {
			p.reported = true
			p.tokens = p.tokens.Add(domain.Tokens{Input: max(0, u.InputTokens-u.CachedInputTokens-u.CacheWriteInputTokens),
				CacheWriteShort: u.CacheWriteInputTokens, CacheRead: u.CachedInputTokens, Output: u.OutputTokens})
			return []ports.HarnessEvent{{Kind: ports.HarnessUsage, Usage: p.usage(false)}}
		}
	case "turn.failed":
		if l.Error != nil {
			p.failure = l.Error.Message
		} else {
			p.failure = "the turn failed"
		}
	case "error":
		p.failure = firstNonEmpty(l.Message, p.failure)
		if p.waiting && !wasWaiting {
			return []ports.HarnessEvent{{Kind: ports.HarnessNetworkWait, Message: classify.Truncate(l.Message, 2000)}}
		}
	case "item.started", "item.updated", "item.completed":
		if l.Item == nil {
			return nil
		}
		return p.item(l.Type, l.Item)
	}
	return nil
}

func (p *parser) item(kind string, it *item) []ports.HarnessEvent {
	done := kind == "item.completed"
	switch it.Type {
	case "command_execution":
		if kind == "item.started" {
			return []ports.HarnessEvent{{Kind: ports.HarnessToolStarted, ToolID: it.ID, Tool: "shell", Command: unwrapShell(it.Command)}}
		}
		if done {
			isErr := it.Status == "failed" || (it.ExitCode != nil && *it.ExitCode != 0)
			return []ports.HarnessEvent{{Kind: ports.HarnessToolDone, ToolID: it.ID, Tool: "shell", ExitCode: it.ExitCode,
				IsError: isErr, Output: classify.Truncate(it.Output, 4000)}}
		}
	case "file_change":
		if done {
			var out []ports.HarnessEvent
			for _, c := range it.Changes {
				out = append(out, ports.HarnessEvent{Kind: ports.HarnessFileChanged, Path: c.Path, Message: c.Kind})
			}
			return out
		}
	case "agent_message":
		if done && strings.TrimSpace(it.Text) != "" {
			p.lastText = it.Text
			return []ports.HarnessEvent{{Kind: ports.HarnessMessage, Message: it.Text}}
		}
	case "mcp_tool_call", "web_search":
		name := firstNonEmpty(it.Tool, it.Type)
		if kind == "item.started" {
			return []ports.HarnessEvent{{Kind: ports.HarnessToolStarted, ToolID: it.ID, Tool: name, Message: firstNonEmpty(it.Query, it.Server)}}
		}
		if done {
			return []ports.HarnessEvent{{Kind: ports.HarnessToolDone, ToolID: it.ID, Tool: name, IsError: it.Status == "failed"}}
		}
	case "error":
		// Codex reports configuration notices as error items; they do not end the turn.
		if done {
			return []ports.HarnessEvent{{Kind: ports.HarnessWarning, Message: classify.Truncate(it.Message, 2000)}}
		}
	}
	return nil
}

// unwrapShell shows `echo hi` for Codex's `/bin/zsh -lc 'echo hi'`.
func unwrapShell(cmd string) string {
	for _, prefix := range []string{"/bin/zsh -lc ", "/bin/bash -lc ", "bash -lc ", "zsh -lc "} {
		if strings.HasPrefix(cmd, prefix) {
			inner := strings.TrimPrefix(cmd, prefix)
			if len(inner) >= 2 && (inner[0] == '\'' || inner[0] == '"') && inner[len(inner)-1] == inner[0] {
				return inner[1 : len(inner)-1]
			}
			return inner
		}
	}
	return cmd
}

var patterns = classify.Patterns{
	Quota:       regexp.MustCompile(`(?i)(usage limit|insufficient_quota|exceeded your current quota|purchase more credits|plan limit)`),
	RateLimit:   regexp.MustCompile(`(?i)(rate[ _-]?limit|too many requests|status 429)`),
	Unavailable: regexp.MustCompile(`(?i)(\bnot logged in\b|please log in|codex login|authentication failed|invalid api key|401 unauthorized|refresh token|token (has )?expired)`),
	Network:     regexp.MustCompile(`(?i)(stream disconnected|error sending request|connection (refused|reset)|dns error|timed out connecting)`),
}

// networkWaitRe is the notice of a Codex that cannot reach its provider and waits for the network, as Codex 0.163.0
// prints it: "Reconnecting... waiting for network (Connection failed: error sending request)". Its numbered retries,
// "Reconnecting... 2/5", are not a wait: Codex exits once they run out.
var networkWaitRe = regexp.MustCompile(`(?i)^reconnecting\b.*\bwaiting for network\b`)

// usage is the turns' tokens, priced at the table once the attempt ends.
func (p *parser) usage(ended bool) *domain.Usage {
	if !p.reported {
		return domain.NotReported()
	}
	u := &domain.Usage{Status: domain.UsageReported, ModelSource: p.modelSource,
		Models: []domain.ModelUsage{{Model: p.model, Tokens: p.tokens}}}
	if ended {
		u.ApplyPrices(p.priceTable())
	}
	return u
}

func (p *parser) Finish(exitCode int, stderrTail string, timedOut, cancelled bool) domain.AttemptEnd {
	end := domain.AttemptEnd{ExitCode: exitCode, SessionID: p.sessionID, FinalText: p.lastText, Usage: p.usage(true)}
	switch {
	case cancelled:
		end.Class, end.Error = domain.ClassCancelled, domain.T("execution_cancelled", "cancelled")
		return end
	case timedOut && p.waiting:
		// Codex never exits while it waits for the network, so the timeout ends the wait: the network is the cause.
		end.Class, end.Error = domain.ClassNetwork, domain.Text{Message: classify.Tail(eventlog.Redact(p.failure), 2000)}
		return end
	case timedOut:
		end.Class, end.Error = domain.ClassTimeout, domain.T("execution_timed_out", "the execution timeout elapsed")
		return end
	case p.completed && p.failure == "" && exitCode == 0:
		return end
	}
	text := strings.TrimSpace(p.failure + "\n" + lastLines(stderrTail, 20))
	// The CLI's own error stays as it is; the daemon's text stands in when it printed none.
	if text == "" {
		end.Error = domain.T("harness_exit_code", "{{harness}} exited with code {{code}}", "harness", Name,
			"code", strconv.Itoa(exitCode))
		text = end.Error.Message
	} else {
		// Redacted before the tail cuts it, so no secret is cut short of its pattern.
		end.Error = domain.Text{Message: classify.Tail(eventlog.Redact(text), 2000)}
	}
	end.Class = patterns.Text(text)
	return end
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	var keep []string
	for _, l := range lines {
		if strings.HasPrefix(l, "Reading additional input from stdin") || strings.TrimSpace(l) == "" {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n")
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int // a part that is not a number counts as 0
		if i < len(pa) {
			_, _ = fmt.Sscanf(pa[i], "%d", &x)
		}
		if i < len(pb) {
			_, _ = fmt.Sscanf(pb[i], "%d", &y)
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
