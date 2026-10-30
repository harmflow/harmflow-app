// Package claudecode is the HarnessRunner adapter of the Claude Code CLI. It uses the installed CLI and its login
// as they are: `claude -p` with stream-json output, built-in skills delivered per execution as a session-only
// plugin (--plugin-dir), and the permission profile translated to Claude Code's own controls.
package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	ID = domain.HarnessClaudeCode
	// Name is the harness's display name.
	Name = "Claude Code"
	// MinVersion is the oldest release line HarmFlow supports; an older CLI is not ready.
	MinVersion = "2.0"
	// TestedMinor is the newest release line HarmFlow was tested with: every patch in it runs as supported, and a newer
	// line runs with a warning.
	TestedMinor = "2.1"
)

// Features the adapter needs from the CLI; a version without them is not ready.
var requiredFlags = []string{"--print", "--output-format", "--permission-mode", "--allowedTools", "--append-system-prompt"}

var declaredModels = []string{domain.DefaultModel, "fable", "opus", "sonnet", "haiku"}

// Adapter is the HarnessAdapter of the Claude Code CLI. It looks for the CLI on PathEnv, then in KnownLocations, and
// runs its probes with Env; the user's agents are read from the configuration directory under Home, or
// CLAUDE_CONFIG_DIR, and the skills a task can cite are those Claude Code reports it loads for the task's workspace.
type Adapter struct {
	Home    string // the user's home directory
	PathEnv string
	Env     []string
	// KnownLocations are searched after PATH; nil is the usual install locations, an empty list none.
	KnownLocations []string

	mu               sync.Mutex
	skills           map[string][]ports.ResolvedItem // by CLI path, workspace, and added folders, kept until ReloadUserItems
	helpTimeout      time.Duration                   // the --help check; zero is defaultHelpTimeout
	skillListTimeout time.Duration                   // the skill list's session; zero is defaultSkillListTimeout
}

// defaultHelpTimeout bounds the --help check; help it could not read by then leaves the flags unknown.
const defaultHelpTimeout = 15 * time.Second

var (
	_ ports.HarnessAdapter         = (*Adapter)(nil)
	_ ports.HarnessIdentifier      = (*Adapter)(nil)
	_ ports.QuotaReader            = (*Adapter)(nil)
	_ ports.ExtraUsageReporter     = (*Adapter)(nil)
	_ ports.WorkspaceLister        = (*Adapter)(nil)
	_ ports.UnverifiedItemsAllower = (*Adapter)(nil)
	_ ports.ItemReloader           = (*Adapter)(nil)
)

// ReportsExtraUsage implements ports.ExtraUsageReporter: every rate-limit report of the stream says whether the session
// is using extra usage.
func (a *Adapter) ReportsExtraUsage() bool { return true }

// ID implements ports.HarnessAdapter: the harness's identifier.
func (a *Adapter) ID() string { return ID }

// Name implements ports.HarnessAdapter: the harness's display name.
func (a *Adapter) Name() string { return Name }

func (a *Adapter) configDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(a.Home, ".claude")
}

func (a *Adapter) knownLocations() []string {
	if a.KnownLocations != nil {
		return a.KnownLocations
	}
	return []string{
		filepath.Join(a.Home, ".claude", "local", "claude"),
		filepath.Join(a.Home, ".local", "bin", "claude"),
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
		filepath.Join(a.Home, ".npm-global", "bin", "claude"),
		filepath.Join(a.Home, ".bun", "bin", "claude"),
	}
}

var versionRe = regexp.MustCompile(`(\d+\.\d+\.\d+)`)
var effortRe = regexp.MustCompile(`--effort <level>[\s\S]*?\(([a-z, ]+)\)`)

// Detect implements ports.HarnessAdapter: it finds the CLI, the path set by hand when there is one, then checks its
// version, the flags HarmFlow needs, its efforts and models, and its sign-in, and says how ready it is.
func (a *Adapter) Detect(ctx context.Context, manualPath string) ports.HarnessInfo {
	info := ports.HarnessInfo{ID: ID, Name: Name, Authentication: "unknown", Readiness: "not_installed",
		Models: []ports.ModelInfo{}, Efforts: []string{}, EnforceableControls: []string{}, Installations: []string{},
		LastCheckedAt: time.Now(), Features: map[string]bool{}}
	found := procutil.LookPath("claude", a.PathEnv, a.knownLocations())
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
		info.SetDiagnostic(domain.T("claude_not_found", "claude was not found on PATH or in the usual install locations"))
		return info
	}
	// Discovery takes the first file that names Claude Code, skipping one that does not.
	out, chosen, err := a.firstIdentified(ctx, candidates)
	info.Installation, info.Path = chosen, chosen
	if manualPath == "" {
		info.PathSource = "known_location"
		if inPath(chosen, a.PathEnv) {
			info.PathSource = "path"
		}
	}
	if real, err := filepath.EvalSymlinks(info.Path); err == nil {
		info.Path = real
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
	// Help read in full that does not list a flag reads as a version that lacks it. Help the check could not read within
	// its limit leaves the flags unknown instead: a slow check does not turn the harness away, and the first attempt
	// proves them.
	helpTimeout := a.helpTimeout
	if helpTimeout == 0 {
		helpTimeout = defaultHelpTimeout
	}
	help, read := procutil.Help(ctx, helpTimeout, a.Env, info.Path, "--help")
	for _, f := range append(requiredFlags, "--plugin-dir", "--effort", "--agents", "--add-dir", "--tools",
		"--disallowedTools", "--no-session-persistence", "--model") {
		info.Features[f] = strings.Contains(help, f)
	}
	info.FeaturesUnknown = !read
	if info.FeaturesUnknown {
		info.SetDiagnostic(domain.T("harness_help_timed_out",
			"the {{harness}} {{command}} check timed out, so the flags it has are unknown; the first attempt proves them",
			"harness", Name, "command", "--help"))
	}
	info.VersionSupport = "supported"
	for _, f := range requiredFlags {
		if !info.HasFeature(f) {
			info.VersionSupport = "below_minimum"
			info.Readiness = "not_ready"
			info.SetDiagnostic(domain.T("harness_feature_missing", "this {{harness}} version lacks {{feature}}, which HarmFlow needs",
				"harness", Name, "feature", f))
		}
	}
	if info.VersionSupport == "supported" {
		switch {
		case info.Version == "":
			// No number to check against the tested range; the help text passed, so it runs with a warning.
			info.VersionSupport = "unknown"
		case compareVersions(releaseLine(info.Version), MinVersion) < 0:
			info.VersionSupport = "below_minimum"
			info.Readiness = "not_ready"
			info.SetDiagnostic(domain.T("harness_version_below_minimum", "{{harness}} {{version}} is older than {{minimum}}, the oldest version line HarmFlow supports",
				"harness", Name, "version", info.Version, "minimum", MinVersion))
		case compareVersions(releaseLine(info.Version), TestedMinor) > 0:
			info.VersionSupport = "untested_newer"
		}
	}
	if m := effortRe.FindStringSubmatch(help); m != nil {
		info.Efforts = append([]string{domain.DefaultEffort}, splitList(m[1])...)
	} else {
		info.Efforts = []string{domain.DefaultEffort}
	}
	for _, m := range declaredModels {
		info.Models = append(info.Models, ports.ModelInfo{ID: m, Efforts: info.Efforts})
	}
	info.CapabilitySource = "reported"
	info.EnforceableControls = []string{"permission_mode", "tool_allowlist", "edit_directories"}

	// Signed out only when the CLI says so, which it does exiting 1; a probe killed at its timeout, or failing
	// otherwise, leaves the sign-in unknown.
	status, statusErr, err := procutil.Output(ctx, 20*time.Second, a.Env, info.Path, "auth", "status")
	var st struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	reported := json.Unmarshal([]byte(strings.TrimSpace(status)), &st) == nil && st.LoggedIn != nil
	switch {
	case reported && !*st.LoggedIn, strings.Contains(strings.ToLower(status+statusErr), "not logged in"):
		info.Authentication = "invalid"
	case err == nil && reported:
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
		info.SetDiagnostic(domain.T("claude_not_signed_in", "Claude Code is not signed in; run `claude` in a terminal and sign in"))
	default:
		// The CLI cannot tell; let the first execution prove it rather than blocking on an unknown. The unknown sign-in
		// shows in the authentication field, so a help check that timed out keeps the diagnostic.
		info.Readiness = "ready"
		if !info.FeaturesUnknown {
			info.SetDiagnostic(domain.T("authentication_unverified", "authentication could not be verified"))
		}
	}
	return info
}

// ValidateProfile implements ports.HarnessAdapter: a model is default, an alias Claude Code offers, or a full claude-*
// name, and an effort other than default is one the detected CLI accepts; with no CLI detected, or help that could not
// be read in time, any effort passes.
func (a *Adapter) ValidateProfile(info ports.HarnessInfo, model, effort string) error {
	if model != "" && model != domain.DefaultModel && !strings.HasPrefix(model, "claude-") && !containsStr(declaredModels, model) {
		return domain.TextErr("claude_model_unknown",
			`Claude Code does not offer the model "{{model}}"; use one of {{models}} or a full claude-* model name`,
			"model", model, "models", strings.Join(declaredModels, ", "))
	}
	if effort != "" && effort != domain.DefaultEffort {
		if !info.Detected || info.FeaturesUnknown {
			return nil
		}
		if !info.Features["--effort"] {
			return domain.TextErr("claude_effort_unsupported",
				"Claude Code {{version}} has no --effort setting; leave the effort as default", "version", info.Version)
		}
		if !containsStr(info.Efforts, effort) {
			return domain.TextErr("claude_effort_invalid",
				`Claude Code {{version}} does not accept the effort "{{effort}}"; it accepts {{efforts}}`,
				"version", info.Version, "effort", effort, "efforts", strings.Join(info.Efforts, ", "))
		}
	}
	return nil
}

// agentDir is the folder the user's agents are read from, Claude Code's configuration directory, whatever the
// workspace.
func (a *Adapter) agentDir() catalog.Dir { return catalog.Dir{Root: a.configDir()} }

// UserItem implements ports.HarnessAdapter: a skill is one Claude Code reports it loads for workspace and the added
// folders, which a CLI that was not detected cannot report; an agent is looked up in the configuration directory.
func (a *Adapter) UserItem(ctx context.Context, info ports.HarnessInfo, kind, name, workspace string, added []string) (ports.ResolvedItem, bool, []ports.DiscoveryError, error) {
	if kind == "skill" {
		skills, err := a.sessionSkills(ctx, info, workspace, added)
		if err != nil {
			return ports.ResolvedItem{}, false, nil, err
		}
		for _, it := range skills {
			if it.Name == name {
				return it, true, nil, nil
			}
		}
		return ports.ResolvedItem{}, false, nil, nil
	}
	it, ok := a.agentDir().Item(kind, name)
	return it, ok, nil, nil
}

// UserItems implements ports.HarnessAdapter: the skills Claude Code reports it loads for workspace and the added
// folders, or the agents of the configuration directory, sorted by name, each name once. A skill list Claude Code
// could not give is the error.
func (a *Adapter) UserItems(ctx context.Context, info ports.HarnessInfo, kind, workspace string, added []string) ([]ports.ResolvedItem, []ports.DiscoveryError, error) {
	if kind == "skill" {
		skills, err := a.sessionSkills(ctx, info, workspace, added)
		return skills, nil, err
	}
	items := a.agentDir().Items(kind)
	slices.SortFunc(items, func(x, y ports.ResolvedItem) int { return strings.Compare(x.Name, y.Name) })
	return items, nil, nil
}

var readTools = []string{"Read", "Glob", "Grep", "Skill", "Task", "TodoWrite"}
var webTools = []string{"WebFetch", "WebSearch"}
var editTools = []string{"Edit", "Write", "MultiEdit", "NotebookEdit"}
var writeTools = append([]string{"Bash"}, editTools...)

// Prepare implements ports.HarnessAdapter: a `claude -p` invocation with stream-json output, the model, effort, and
// context asked for, the built-in skills as a session-only plugin, the built-in agents defined for the session, and
// the permission profile translated to Claude Code's controls, with the restrictions it enforces and those it cannot.
func (a *Adapter) Prepare(info ports.HarnessInfo, req ports.RunRequest) (ports.Invocation, ports.OutputParser, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if info.HasFeature("--no-session-persistence") {
		args = append(args, "--no-session-persistence")
	}
	if req.Model != "" && req.Model != domain.DefaultModel {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" && req.Effort != domain.DefaultEffort {
		args = append(args, "--effort", req.Effort)
	}
	if req.Context != "" {
		args = append(args, "--append-system-prompt", req.Context)
	}

	// Built-in skills: a plugin that exists only for this execution.
	var builtinSkills []ports.ResolvedItem
	for _, s := range req.Skills {
		if s.Builtin {
			builtinSkills = append(builtinSkills, s)
		}
	}
	if len(builtinSkills) > 0 {
		dir := filepath.Join(req.ExecutionDir, "claude-plugin")
		if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o700); err != nil {
			return ports.Invocation{}, nil, err
		}
		manifest := `{"name":"harmflow","version":"0.1.0","description":"HarmFlow built-in agents and skills for one execution"}`
		if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
			return ports.Invocation{}, nil, err
		}
		for _, s := range builtinSkills {
			if err := catalog.CopyTree(s.Path, filepath.Join(dir, "skills", s.Name)); err != nil {
				return ports.Invocation{}, nil, domain.TextErr("skill_delivery_failed", "delivering skill {{name}}: {{detail}}",
					"name", s.Name, "detail", err.Error())
			}
		}
		args = append(args, "--plugin-dir", dir)
	}

	// Agents are subagents only: the session runs as Claude Code's default agent, with the task's prompt. Built-in ones
	// are defined for this session only; a user's agent is loaded from ~/.claude by name.
	defs := map[string]map[string]any{}
	for _, ag := range req.Agents {
		if !ag.Builtin {
			continue
		}
		data, err := os.ReadFile(ag.Path)
		if err != nil {
			return ports.Invocation{}, nil, domain.TextErr("agent_delivery_failed", "delivering agent {{name}}: {{detail}}",
				"name", ag.Name, "detail", err.Error())
		}
		fm, body := catalog.Parse(data)
		def := map[string]any{"description": fm.Description, "prompt": body}
		if tools := fm.ToolList(); len(tools) > 0 {
			def["tools"] = tools
		}
		defs[ag.Name] = def
	}
	if len(defs) > 0 {
		js, _ := json.Marshal(defs) // maps of strings and string slices always marshal
		args = append(args, "--agents", string(js))
	}

	perm := req.Permissions
	restrictions := []ports.Restriction{}
	network := perm.Network != domain.NetworkDeny
	dirs := append([]string{}, req.AdditionalDirs...)
	switch perm.Profile {
	case domain.ProfileReadOnly:
		tools := append([]string{}, readTools...)
		if network {
			tools = append(tools, webTools...)
		}
		args = append(args, "--permission-mode", "dontAsk", "--tools", strings.Join(tools, ","))
		restrictions = append(restrictions, ports.Restriction{Name: "read_only", Enforced: true,
			Text: domain.Text{Message: "no write or shell tool is available to the session"}})
		if !network {
			restrictions = append(restrictions, ports.Restriction{Name: "network", Enforced: true,
				Text: domain.Text{Message: "no web or shell tool is available to the session"}})
		}
	case domain.ProfileCustom:
		allowed := perm.Custom.AllowedTools
		if len(allowed) == 0 {
			allowed = readTools
		}
		mode := "dontAsk"
		for _, t := range allowed {
			if containsStr(writeTools, toolName(t)) {
				mode = "acceptEdits"
			}
		}
		args = append(args, "--permission-mode", mode, "--allowedTools", strings.Join(allowed, ","))
		denied := append([]string{}, perm.Custom.DeniedTools...)
		if !network {
			denied = append(denied, webTools...)
		}
		if len(denied) > 0 {
			args = append(args, "--disallowedTools", strings.Join(denied, ","))
		}
		dirs = append(dirs, perm.Custom.WritablePaths...)
		restrictions = append(restrictions, a.allowlistRestriction(req, append(append([]string{}, allowed...), denied...)))
		restrictions = append(restrictions, shellRestrictions(mode == "acceptEdits" && hasShell(allowed), network,
			*perm.RestrictToWorkspace)...)
	default: // workspace_write
		// The editing tools get no allow rule: a bare one allows them on any path, and acceptEdits alone approves their
		// edits in the working directory and the added directories.
		tools := append(append([]string{}, readTools...), "Bash")
		if network {
			tools = append(tools, webTools...)
		} else {
			args = append(args, "--disallowedTools", strings.Join(webTools, ","))
		}
		args = append(args, "--permission-mode", "acceptEdits", "--allowedTools", strings.Join(tools, ","))
		restrictions = append(restrictions, a.editRestriction(req))
		restrictions = append(restrictions, shellRestrictions(true, network, *perm.RestrictToWorkspace)...)
	}
	if req.ExecutionDir != "" {
		dirs = append(dirs, req.ExecutionDir)
	}
	for _, d := range uniq(dirs) {
		if d != req.WorkDir {
			args = append(args, "--add-dir", d)
		}
	}
	inv := ports.Invocation{Path: info.Path, Args: args, Env: req.Env, Dir: req.WorkDir, Stdin: req.Prompt,
		Restrictions: restrictions}
	return inv, &parser{tools: map[string]toolUse{}}, nil
}

// settingsSources are the Claude Code settings files HarmFlow reads for allow rules, in the order a warning names them:
// the user's, in the configuration directory, then the working directory's project and local ones. Managed settings
// are not read.
var settingsSources = []struct{ name, file string }{
	{"user", "settings.json"},
	{"project", filepath.Join(".claude", "settings.json")},
	{"local", filepath.Join(".claude", "settings.local.json")},
}

// wideningSources names, in the order of settingsSources, the settings sources that widen what the task allows: those
// with an allow rule widens reports, and those whose file exists but cannot be read or parsed, since it cannot be
// checked. Validation reads only the user's settings; the workspace's own are read when each execution starts.
func (a *Adapter) wideningSources(req ports.RunRequest, widens func(source, rule string) bool) []string {
	var out []string
	for _, s := range settingsSources {
		path := filepath.Join(req.WorkDir, s.file)
		switch {
		case s.name == "user":
			path = filepath.Join(a.configDir(), s.file)
		case req.PolicyCheck || req.WorkDir == "":
			continue
		}
		rules, err := allowRules(path)
		if err != nil || slices.ContainsFunc(rules, func(r string) bool { return widens(s.name, r) }) {
			out = append(out, s.name)
		}
	}
	return out
}

// allowlistRestriction says whether the custom profile's tool list is enforced. Claude Code merges the allow rules of
// every settings source it loads into the list it is given, so an allow rule that covered, the task's allowed and
// denied tools, does not hold widens the list. A rule is covered only when it is identical to an entry.
func (a *Adapter) allowlistRestriction(req ports.RunRequest, covered []string) ports.Restriction {
	widening := a.wideningSources(req, func(_, r string) bool { return !slices.Contains(covered, r) })
	if len(widening) == 0 {
		return ports.Restriction{Name: "tool_allowlist", Enforced: true,
			Text: domain.Text{Message: "Claude Code allows the listed tools, and the commands it judges read-only"}}
	}
	return ports.Restriction{Name: "tool_allowlist", Enforced: false,
		Text: domain.T("restriction_claude_allow_rules",
			"Claude Code adds the allow rules of its {{where}} to the task's tool list, and they allow more than the list or could not be read; managed settings are not checked",
			"where", settingsPhrase(widening), "sources", strings.Join(widening, "_"))}
}

// editRestriction says whether workspace_write's file edits are limited to the workspace and the added directories.
// acceptEdits limits them so, but Claude Code adds the allow rules of every settings source it loads, and an editing
// rule that may reach outside those directories lifts the limit. In doubt, a rule reaches outside: only one whose path
// lies within the working directory or an added directory stays inside, and a bare rule does not. The directories are
// compared resolved, as the rules' paths are, so a link on either side does not decide.
func (a *Adapter) editRestriction(req ports.RunRequest) ports.Restriction {
	var roots []string
	for _, d := range append([]string{req.ExecutionDir, req.WorkDir}, req.AdditionalDirs...) {
		if real, err := domain.ResolvePath(d); err == nil {
			roots = append(roots, real)
		}
	}
	widening := a.wideningSources(req, func(source, r string) bool {
		return containsStr(editTools, toolName(r)) && !a.editRuleInside(r, source != "user", roots)
	})
	if len(widening) == 0 {
		return ports.Restriction{Name: "edit_directories", Enforced: true,
			Text: domain.Text{Message: "file edits are limited to the workspace and the added directories"}}
	}
	return ports.Restriction{Name: "edit_directories", Enforced: false,
		Text: domain.T("restriction_claude_edit_rules",
			"Claude Code adds the allow rules of its {{where}} to the task's permissions, and they allow file edits outside the workspace and the added directories or could not be read; managed settings are not checked",
			"where", settingsPhrase(widening), "sources", strings.Join(widening, "_"))}
}

// editRuleInside reports whether an editing rule's path stays within the working directory or one of roots, read in
// Claude Code's forms: //path from the filesystem root, ~/path from the home folder, and any other path from the
// working directory. A /path is read from the settings file's folder or the project root, as versions differ, so it
// stays inside only in the working directory's own settings, as a path from that directory. A //path or ~/path is
// resolved before it is compared with roots, which are resolved too: through the longest part of it that exists, so a
// link to a root stays inside, and a path that cannot be resolved reaches outside. A bare rule, Tool(*), another user's
// ~, and a path that climbs out with .. reach outside.
func (a *Adapter) editRuleInside(rule string, workspaceSettings bool, roots []string) bool {
	pattern, ok := strings.CutPrefix(rule, toolName(rule)+"(")
	if !ok || !strings.HasSuffix(pattern, ")") {
		return false
	}
	pattern = strings.TrimSuffix(pattern, ")")
	base := ""
	switch {
	case pattern == "" || pattern == "*":
		return false
	case strings.HasPrefix(pattern, "//"):
		base, pattern = "/", pattern[2:]
	case strings.HasPrefix(pattern, "~/"):
		base, pattern = a.Home, pattern[2:]
	case strings.HasPrefix(pattern, "~"):
		return false
	case strings.HasPrefix(pattern, "/"):
		if !workspaceSettings {
			return false
		}
		pattern = pattern[1:]
	}
	dir, ok := fixedDir(pattern)
	if !ok {
		return false
	}
	if base == "" {
		return filepath.IsLocal(filepath.Join(".", dir))
	}
	real, err := domain.ResolvePath(filepath.Join(base, dir))
	return err == nil && within(real, roots)
}

// fixedDir is the part of a path pattern before its first segment with a wildcard, which every path it matches lies
// in. ok is false when a segment after the wildcard climbs with .., out of that part.
func fixedDir(pattern string) (dir string, ok bool) {
	segments := strings.Split(pattern, "/")
	for i, s := range segments {
		if strings.ContainsAny(s, `*?[{\`) {
			return strings.Join(segments[:i], "/"), !slices.Contains(segments[i:], "..")
		}
	}
	return pattern, true
}

// within reports whether path is one of dirs or lies below one.
func within(path string, dirs []string) bool {
	for _, d := range dirs {
		if rel, err := filepath.Rel(d, path); d != "" && err == nil && filepath.IsLocal(rel) {
			return true
		}
	}
	return false
}

// allowRules are the permissions.allow rules of a Claude Code settings file; a missing file has none.
func allowRules(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return settings.Permissions.Allow, nil
}

// settingsPhrase names settings sources in en-US: "user settings", "user and local settings", "user, project, and
// local settings".
func settingsPhrase(sources []string) string {
	switch len(sources) {
	case 1:
		return sources[0] + " settings"
	case 2:
		return sources[0] + " and " + sources[1] + " settings"
	}
	return strings.Join(sources[:len(sources)-1], ", ") + ", and " + sources[len(sources)-1] + " settings"
}

// shellRestrictions names what the shell tool can do that Claude Code's permission mode does not confine.
func shellRestrictions(shell, network, restrict bool) []ports.Restriction {
	if !shell {
		return nil
	}
	var out []ports.Restriction
	if restrict {
		out = append(out, ports.Restriction{Name: "restrictToWorkspace", Enforced: false,
			Text: domain.T("restriction_claude_shell_workspace",
				"Claude Code does not confine shell commands to the workspace; only its file-edit tools are confined")})
	}
	if !network {
		out = append(out, ports.Restriction{Name: "network", Enforced: false,
			Text: domain.T("restriction_claude_shell_network",
				"Claude Code does not block network access from shell commands; only its web tools are removed")})
	}
	return out
}

func hasShell(tools []string) bool {
	for _, t := range tools {
		if toolName(t) == "Bash" {
			return true
		}
	}
	return false
}

func toolName(rule string) string {
	if i := strings.IndexByte(rule, '('); i >= 0 {
		return rule[:i]
	}
	return rule
}

type toolUse struct {
	name, path string
}

type parser struct {
	tools      map[string]toolUse
	sessionID  string
	model      string // the init line's model, the session's main one
	result     *resultLine
	rateStatus string
	rateType   string
	overage    bool // the last rate-limit report said the session is using extra usage
	resetsAt   int64
	lastText   string
	messages   map[string]messageUsage // the latest usage of each assistant message, by message id
	prices     domain.PriceTable       // what prices the usage; Prices when it holds none
	order      []string
}

var _ ports.OutputParser = (*parser)(nil)

type resultLine struct {
	IsError      bool
	Subtype      string
	Result       string
	TotalCostUSD *float64
	Usage        *apiUsage
	ModelUsage   map[string]modelUsage
}

// apiUsage is the API's usage of one message, or of the session on the result line.
type apiUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreation            *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// tokens are the usage's input and cache counts. Cache writes the CLI does not split by lifetime are counted as
// 5-minute writes, the API's default. The output is left out: a message's output count is provisional.
func (u apiUsage) tokens() domain.Tokens {
	t := domain.Tokens{Input: u.InputTokens, CacheRead: u.CacheReadInputTokens}
	if u.CacheCreation != nil {
		t.CacheWriteLong = u.CacheCreation.Ephemeral1h
	}
	t.CacheWriteShort = max(0, u.CacheCreationInputTokens-t.CacheWriteLong)
	return t
}

// modelUsage is the result line's complete count for one model, calls the messages never show included.
type modelUsage struct {
	InputTokens              int64    `json:"inputTokens"`
	OutputTokens             int64    `json:"outputTokens"`
	CacheReadInputTokens     int64    `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64    `json:"cacheCreationInputTokens"`
	CostUSD                  *float64 `json:"costUSD"`
}

type messageUsage struct {
	model  string
	tokens domain.Tokens
}

type streamLine struct {
	Type         string                `json:"type"`
	Subtype      string                `json:"subtype"`
	SessionID    string                `json:"session_id"`
	Model        string                `json:"model"`
	Message      json.RawMessage       `json:"message"`
	IsError      bool                  `json:"is_error"`
	Result       string                `json:"result"`
	TotalCostUSD *float64              `json:"total_cost_usd"`
	Usage        *apiUsage             `json:"usage"`
	ModelUsage   map[string]modelUsage `json:"modelUsage"`
	RateLimit    *rateLimitInfo        `json:"rate_limit_info"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (p *parser) Line(line []byte) []ports.HarnessEvent {
	var sl streamLine
	if len(line) == 0 {
		return nil
	}
	if err := json.Unmarshal(line, &sl); err != nil {
		return []ports.HarnessEvent{{Kind: ports.HarnessWarning, Message: classify.Truncate(string(line), 2000)}}
	}
	switch sl.Type {
	case "system":
		if sl.Subtype == "init" {
			p.sessionID = sl.SessionID
			p.model = sl.Model
			return []ports.HarnessEvent{{Kind: ports.HarnessStarted, SessionID: sl.SessionID, Model: sl.Model}}
		}
	case "assistant":
		var msg struct {
			ID      string         `json:"id"`
			Model   string         `json:"model"`
			Usage   *apiUsage      `json:"usage"`
			Content []contentBlock `json:"content"`
		}
		// A message of another shape leaves msg empty, and the line yields no event.
		_ = json.Unmarshal(sl.Message, &msg)
		var out []ports.HarnessEvent
		if p.countMessage(msg.ID, msg.Model, msg.Usage) {
			out = append(out, ports.HarnessEvent{Kind: ports.HarnessUsage, Usage: p.liveUsage()})
		}
		for _, c := range msg.Content {
			switch c.Type {
			case "text":
				if strings.TrimSpace(c.Text) != "" {
					p.lastText = c.Text
					out = append(out, ports.HarnessEvent{Kind: ports.HarnessMessage, Message: c.Text})
				}
			case "tool_use":
				var in struct {
					Command     string `json:"command"`
					Description string `json:"description"`
					FilePath    string `json:"file_path"`
					NotebookP   string `json:"notebook_path"`
					Pattern     string `json:"pattern"`
					Skill       string `json:"skill"`
					Prompt      string `json:"prompt"`
				}
				_ = json.Unmarshal(c.Input, &in) // an input of another shape leaves the fields empty
				path := in.FilePath
				if path == "" {
					path = in.NotebookP
				}
				p.tools[c.ID] = toolUse{name: c.Name, path: path}
				desc := in.Description
				if desc == "" {
					desc = firstNonEmpty(in.FilePath, in.Pattern, in.Skill, classify.Truncate(in.Prompt, 200))
				}
				out = append(out, ports.HarnessEvent{Kind: ports.HarnessToolStarted, ToolID: c.ID, Tool: c.Name,
					Command: in.Command, Path: path, Message: desc})
			}
		}
		return out
	case "user":
		var msg struct {
			Content []contentBlock `json:"content"`
		}
		_ = json.Unmarshal(sl.Message, &msg) // as above: another shape yields no event
		var out []ports.HarnessEvent
		for _, c := range msg.Content {
			if c.Type != "tool_result" {
				continue
			}
			use := p.tools[c.ToolUseID]
			out = append(out, ports.HarnessEvent{Kind: ports.HarnessToolDone, ToolID: c.ToolUseID, Tool: use.name,
				IsError: c.IsError, Output: classify.Truncate(resultText(c.Content), 4000)})
			if !c.IsError && use.path != "" && containsStr([]string{"Edit", "Write", "MultiEdit", "NotebookEdit"}, use.name) {
				out = append(out, ports.HarnessEvent{Kind: ports.HarnessFileChanged, Path: use.path})
			}
		}
		return out
	case "rate_limit_event":
		if sl.RateLimit != nil {
			p.rateStatus = sl.RateLimit.Status
			p.rateType = sl.RateLimit.RateLimitType
			p.overage = sl.RateLimit.IsUsingOverage
			p.resetsAt = sl.RateLimit.ResetsAt
			out := []ports.HarnessEvent{{Kind: ports.HarnessQuota, Quota: sl.RateLimit.quota()}}
			if sl.RateLimit.Status != "allowed" && sl.RateLimit.Status != "allowed_warning" {
				out = append(out, ports.HarnessEvent{Kind: ports.HarnessRateLimit, Message: fmt.Sprintf("%s limit: %s",
					sl.RateLimit.RateLimitType, sl.RateLimit.Status)})
			}
			return out
		}
	case "result":
		p.result = &resultLine{IsError: sl.IsError, Subtype: sl.Subtype, Result: sl.Result, TotalCostUSD: sl.TotalCostUSD,
			Usage: sl.Usage, ModelUsage: sl.ModelUsage}
		if sl.SessionID != "" {
			p.sessionID = sl.SessionID
		}
	}
	return nil
}

// countMessage keeps a message's usage once: the CLI repeats it on every content-block line of the message, so
// summing per line would count its input and cache tokens two or three times. It reports whether the counts changed.
func (p *parser) countMessage(id, model string, u *apiUsage) bool {
	if id == "" || u == nil {
		return false
	}
	if p.messages == nil {
		p.messages = map[string]messageUsage{}
	}
	next := messageUsage{model: firstNonEmpty(model, p.model), tokens: u.tokens()}
	prev, seen := p.messages[id]
	if seen && prev == next {
		return false
	}
	if !seen {
		p.order = append(p.order, id)
	}
	p.messages[id] = next
	return true
}

// liveUsage is the input and cache tokens of the messages so far, by model: partial until the result line settles
// the output, or not reported when no message carried usage.
func (p *parser) liveUsage() *domain.Usage {
	if len(p.order) == 0 {
		return domain.NotReported()
	}
	u := &domain.Usage{Status: domain.UsagePartial, ModelSource: domain.ModelFromHarness, Models: []domain.ModelUsage{}}
	index := map[string]int{}
	for _, id := range p.order {
		m := p.messages[id]
		i, ok := index[m.model]
		if !ok {
			i = len(u.Models)
			index[m.model] = i
			u.Models = append(u.Models, domain.ModelUsage{Model: m.model})
		}
		u.Models[i].Tokens = u.Models[i].Tokens.Add(m.tokens)
	}
	u.SortModels(p.model)
	return u
}

// usage is the attempt's final count: the result line's modelUsage, keyed by the full model id, with the cost the CLI
// reported. A result line that counts nothing, as an error before any call prints, is no final count: the messages'
// partial counts stand, or none.
func (p *parser) usage() *domain.Usage {
	r := p.result
	if r == nil || (len(r.ModelUsage) == 0 && (r.Usage == nil || r.Usage.tokens() == (domain.Tokens{}) && r.Usage.OutputTokens == 0)) {
		return p.liveUsage()
	}
	u := &domain.Usage{Status: domain.UsageReported, ModelSource: domain.ModelFromHarness, Models: []domain.ModelUsage{}}
	if len(r.ModelUsage) > 0 {
		long := p.longCacheShare()
		for id, m := range r.ModelUsage {
			w1h := int64(math.Round(float64(m.CacheCreationInputTokens) * long))
			u.Models = append(u.Models, domain.ModelUsage{Model: id, CostUSD: m.CostUSD, Tokens: domain.Tokens{
				Input: m.InputTokens, CacheWriteShort: m.CacheCreationInputTokens - w1h, CacheWriteLong: w1h,
				CacheRead: m.CacheReadInputTokens, Output: m.OutputTokens}})
		}
	} else {
		t := r.Usage.tokens()
		t.Output = r.Usage.OutputTokens
		u.Models = append(u.Models, domain.ModelUsage{Model: p.model, Tokens: t})
	}
	u.SortModels(p.model)
	if r.TotalCostUSD != nil {
		u.CostSource, u.CostUSD = domain.CostReported, r.TotalCostUSD
	}
	u.ApplyPrices(p.priceTable())
	return u
}

// longCacheShare is the share of cache writes at the 1-hour lifetime. modelUsage does not split them, so each model's
// writes are apportioned by the result line's split, or the messages' when it has none.
func (p *parser) longCacheShare() float64 {
	var w5, w1 int64
	if u := p.result.Usage; u != nil && u.CacheCreation != nil {
		w5, w1 = u.CacheCreation.Ephemeral5m, u.CacheCreation.Ephemeral1h
	}
	if w5+w1 == 0 {
		for _, m := range p.messages {
			w5, w1 = w5+m.tokens.CacheWriteShort, w1+m.tokens.CacheWriteLong
		}
	}
	if w5+w1 == 0 {
		return 0
	}
	return float64(w1) / float64(w5+w1)
}

func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

// patterns match the CLI's own texts, not words a task's error can hold too: a bare quota or "limit reached", or the
// "not logged into" of another tool, ends the attempt as a task error (harmflow-app#213).
var patterns = classify.Patterns{
	Quota:       regexp.MustCompile(`(?i)(usage limit|you(’|')ve hit your ([\w’']+ ){0,3}limit|out of (extra )?usage|credit balance (is )?too low|(weekly|session|5-hour|opus|sonnet)( weekly)? limit (has been )?reached)`),
	RateLimit:   regexp.MustCompile(`(?i)(rate[ _-]?limit|too many requests|overloaded_error|api error:? (429|529))`),
	Unavailable: regexp.MustCompile(`(?i)(invalid api key|please run /login|\bnot logged in\b|authentication_error|oauth token (has )?expired|api error:? 401|claude auth login|re-authenticate|managed settings .*could not be loaded)`),
	Network:     regexp.MustCompile(`(?i)(ECONNREFUSED|ENOTFOUND|ETIMEDOUT|ECONNRESET|network error|getaddrinfo|fetch failed|connection error)`),
}

func (p *parser) Finish(exitCode int, stderrTail string, timedOut, cancelled bool) domain.AttemptEnd {
	end := domain.AttemptEnd{ExitCode: exitCode, SessionID: p.sessionID, FinalText: p.lastText, Usage: p.usage()}
	switch {
	case cancelled:
		end.Class, end.Error = domain.ClassCancelled, domain.T("execution_cancelled", "cancelled")
		return end
	case timedOut:
		end.Class, end.Error = domain.ClassTimeout, domain.T("execution_timed_out", "the execution timeout elapsed")
		return end
	}
	if p.result != nil && !p.result.IsError && exitCode == 0 {
		if p.result.Result != "" {
			end.FinalText = p.result.Result
		}
		return end
	}
	text := strings.TrimSpace(stderrTail)
	if p.result != nil && p.result.Result != "" {
		text = p.result.Result + "\n" + text
	}
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
	if p.rateStatus == "rejected" {
		// The stream itself said the limit rejected the session: a plan window is a quota, anything else a rate limit. A
		// plan window rejected while the session goes on with extra usage is no quota exhausted, so the session's error
		// keeps the class its text gives; the runner ends the attempt of a task that allows no extra usage as a quota.
		switch {
		case !planWindow(p.rateType):
			end.Class = domain.ClassRateLimited
		case !p.overage:
			end.Class = domain.ClassQuotaExhausted
		}
	}
	if p.resetsAt > 0 && end.Class == domain.ClassRateLimited {
		if d := time.Until(time.Unix(p.resetsAt, 0)); d > 0 {
			end.RetryAfter = d
		}
	}
	return end
}

func inPath(p, pathEnv string) bool {
	dir := filepath.Dir(p)
	for _, d := range strings.Split(pathEnv, ":") {
		if d == dir {
			return true
		}
	}
	return false
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func uniq(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// releaseLine keeps the major and minor of a dotted version, the level that the supported range is written in.
func releaseLine(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, ".")
}

// compareVersions compares dotted numeric versions.
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

// identifies reports whether the output of --version names Claude Code, as "2.1.239 (Claude Code)" does.
func identifies(out string) bool { return strings.Contains(out, "Claude Code") }

// Identify checks that path is an executable file whose --version output names Claude Code.
func (a *Adapter) Identify(ctx context.Context, path string) error {
	_, err := procutil.IdentifyHarness(ctx, procutil.VersionTimeout, a.Env, path, Name, "claude", identifies)
	return err
}

// firstIdentified returns the first candidate that names Claude Code, with its --version output, or the first
// candidate and why it failed when none does.
func (a *Adapter) firstIdentified(ctx context.Context, candidates []string) (string, string, error) {
	var first error
	for _, c := range candidates {
		out, err := procutil.IdentifyHarness(ctx, procutil.VersionTimeout, a.Env, c, Name, "claude", identifies)
		if err == nil {
			return out, c, nil
		}
		if first == nil {
			first = err
		}
	}
	return "", candidates[0], first
}
