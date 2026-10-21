// Package claudecode is the HarnessRunner adapter of the Claude Code CLI. It uses the installed CLI and its login
// as they are (D-018, D-019): `claude -p` with stream-json output, built-in skills delivered per execution as a
// session-only plugin (--plugin-dir), and the permission profile translated to Claude Code's own controls (D-068).
package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/catalog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/classify"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

const (
	ID   = domain.HarnessClaudeCode
	Name = "Claude Code"
	// TestedUpTo is the newest version this adapter was tested with; newer versions run with a warning (D-078).
	TestedUpTo = "2.1.239"
)

// Features the adapter needs from the CLI; a version without them is not ready (D-078).
var requiredFlags = []string{"--print", "--output-format", "--permission-mode", "--allowedTools", "--append-system-prompt"}

var declaredModels = []string{domain.DefaultModel, "fable", "opus", "sonnet", "haiku"}

type Adapter struct {
	Home    string // the user's home directory
	PathEnv string
	Env     []string
}

func (a *Adapter) ID() string   { return ID }
func (a *Adapter) Name() string { return Name }

func (a *Adapter) configDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(a.Home, ".claude")
}

func (a *Adapter) knownLocations() []string {
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

func (a *Adapter) Detect(ctx context.Context, manualPath string) ports.HarnessInfo {
	info := ports.HarnessInfo{ID: ID, Name: Name, Authentication: "unknown", Readiness: "not_installed",
		Models: []ports.ModelInfo{}, Efforts: []string{}, EnforceableControls: []string{}, Installations: []string{},
		LastCheckedAt: time.Now(), Features: map[string]bool{}}
	found := procutil.LookPath("claude", a.PathEnv, a.knownLocations())
	info.Installations = append(info.Installations, found...)
	switch {
	case manualPath != "":
		info.Path, info.PathSource = manualPath, "manual"
		if st, err := os.Stat(manualPath); err != nil || st.IsDir() {
			info.SetDiagnostic(domain.T("harness_manual_path_invalid", "the path set by hand does not exist or is not a file"))
			return info
		}
	case len(found) > 0:
		info.Path = found[0]
		info.PathSource = "known_location"
		if inPath(found[0], a.PathEnv) {
			info.PathSource = "path"
		}
	default:
		info.SetDiagnostic(domain.T("claude_not_found", "claude was not found on PATH or in the usual install locations"))
		return info
	}
	if real, err := filepath.EvalSymlinks(info.Path); err == nil {
		info.Path = real
	}
	info.Detected = true
	info.Readiness = "installed"
	out, _, err := procutil.Output(ctx, 15*time.Second, a.Env, info.Path, "--version")
	if err != nil {
		info.SetDiagnostic(domain.T("command_failed", "{{command}} failed: {{detail}}", "command", "claude --version",
			"detail", err.Error()))
		return info
	}
	if m := versionRe.FindStringSubmatch(out); m != nil {
		info.Version = m[1]
		info.Readiness = "version_identified"
	}
	help, _, _ := procutil.Output(ctx, 15*time.Second, a.Env, info.Path, "--help")
	for _, f := range append(requiredFlags, "--plugin-dir", "--effort", "--agents", "--agent", "--add-dir", "--tools",
		"--disallowedTools", "--no-session-persistence", "--model") {
		info.Features[f] = strings.Contains(help, f)
	}
	info.VersionSupport = "supported"
	for _, f := range requiredFlags {
		if !info.Features[f] {
			info.VersionSupport = "below_minimum"
			info.Readiness = "not_ready"
			info.SetDiagnostic(domain.T("harness_feature_missing", "this {{harness}} version lacks {{feature}}, which HarmFlow needs",
				"harness", Name, "feature", f))
		}
	}
	if info.VersionSupport == "supported" && compareVersions(info.Version, TestedUpTo) > 0 {
		info.VersionSupport = "untested_newer"
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

	status, _, err := procutil.Output(ctx, 20*time.Second, a.Env, info.Path, "auth", "status")
	var st struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if err == nil && json.Unmarshal([]byte(strings.TrimSpace(status)), &st) == nil && st.LoggedIn != nil {
		if *st.LoggedIn {
			info.Authentication = "valid"
		} else {
			info.Authentication = "invalid"
		}
	} else if err != nil && strings.Contains(strings.ToLower(status), "not logged in") {
		info.Authentication = "invalid"
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
		// The CLI cannot tell; let the first execution prove it rather than blocking on an unknown.
		info.Readiness = "ready"
		info.SetDiagnostic(domain.T("authentication_unverified", "authentication could not be verified"))
	}
	return info
}

func (a *Adapter) ValidateProfile(info ports.HarnessInfo, model, effort string) error {
	if model != "" && model != domain.DefaultModel && !strings.HasPrefix(model, "claude-") && !containsStr(declaredModels, model) {
		return domain.TextErr("claude_model_unknown",
			`Claude Code does not offer the model "{{model}}"; use one of {{models}} or a full claude-* model name`,
			"model", model, "models", strings.Join(declaredModels, ", "))
	}
	if effort != "" && effort != domain.DefaultEffort {
		if !info.Detected {
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

func (a *Adapter) UserItem(kind, name string) (ports.ResolvedItem, bool) {
	return catalog.Dir{Root: a.configDir()}.Item(kind, name)
}

func (a *Adapter) UserItems(kind string) []ports.ResolvedItem {
	return catalog.Dir{Root: a.configDir()}.Items(kind)
}

var readTools = []string{"Read", "Glob", "Grep", "Skill", "Task", "TodoWrite"}
var webTools = []string{"WebFetch", "WebSearch"}
var writeTools = []string{"Bash", "Edit", "Write", "MultiEdit", "NotebookEdit"}

func (a *Adapter) Prepare(info ports.HarnessInfo, req ports.RunRequest) (ports.Invocation, ports.OutputParser, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if info.Features["--no-session-persistence"] {
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

	// Agents: built-in ones are defined for this session only; a user's agent is loaded from ~/.claude by name.
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
		js, _ := json.Marshal(defs)
		args = append(args, "--agents", string(js))
	}
	if len(req.Agents) > 0 && info.Features["--agent"] {
		args = append(args, "--agent", req.Agents[0].Name)
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
		restrictions = append(restrictions, ports.Restriction{Name: "tool_allowlist", Enforced: true,
			Text: domain.Text{Message: "Claude Code allows only the listed tools"}})
		restrictions = append(restrictions, shellRestrictions(mode == "acceptEdits" && hasShell(allowed), network,
			*perm.RestrictToWorkspace)...)
	default: // workspace_write
		tools := append(append([]string{}, readTools...), writeTools...)
		if network {
			tools = append(tools, webTools...)
		} else {
			args = append(args, "--disallowedTools", strings.Join(webTools, ","))
		}
		args = append(args, "--permission-mode", "acceptEdits", "--allowedTools", strings.Join(tools, ","))
		restrictions = append(restrictions, ports.Restriction{Name: "edit_directories", Enforced: true,
			Text: domain.Text{Message: "file edits are limited to the workspace and the added directories"}})
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
	result     *resultLine
	rateStatus string
	rateType   string
	resetsAt   int64
	lastText   string
}

type resultLine struct {
	IsError bool   `json:"is_error"`
	Subtype string `json:"subtype"`
	Result  string `json:"result"`
}

type streamLine struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Model     string          `json:"model"`
	Message   json.RawMessage `json:"message"`
	IsError   bool            `json:"is_error"`
	Result    string          `json:"result"`
	RateLimit *struct {
		Status        string `json:"status"`
		ResetsAt      int64  `json:"resetsAt"`
		RateLimitType string `json:"rateLimitType"`
		OverageStatus string `json:"overageStatus"`
	} `json:"rate_limit_info"`
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
			return []ports.HarnessEvent{{Kind: ports.HarnessStarted, SessionID: sl.SessionID, Model: sl.Model}}
		}
	case "assistant":
		var msg struct {
			Content []contentBlock `json:"content"`
		}
		_ = json.Unmarshal(sl.Message, &msg)
		var out []ports.HarnessEvent
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
				_ = json.Unmarshal(c.Input, &in)
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
		_ = json.Unmarshal(sl.Message, &msg)
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
			p.resetsAt = sl.RateLimit.ResetsAt
			if sl.RateLimit.Status != "allowed" && sl.RateLimit.Status != "allowed_warning" {
				return []ports.HarnessEvent{{Kind: ports.HarnessRateLimit, Message: fmt.Sprintf("%s limit: %s",
					sl.RateLimit.RateLimitType, sl.RateLimit.Status)}}
			}
		}
	case "result":
		p.result = &resultLine{IsError: sl.IsError, Subtype: sl.Subtype, Result: sl.Result}
		if sl.SessionID != "" {
			p.sessionID = sl.SessionID
		}
	}
	return nil
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

var patterns = classify.Patterns{
	Quota:       regexp.MustCompile(`(?i)(usage limit|limit (has been )?reached|out of (extra )?usage|quota|credit balance is too low|weekly limit|session limit)`),
	RateLimit:   regexp.MustCompile(`(?i)(rate[ _-]?limit|too many requests|overloaded_error|api error:? (429|529))`),
	Unavailable: regexp.MustCompile(`(?i)(invalid api key|please run /login|not logged in|authentication_error|oauth token (has )?expired|api error:? 401|claude auth login|re-authenticate|managed settings .*could not be loaded)`),
	Network:     regexp.MustCompile(`(?i)(ECONNREFUSED|ENOTFOUND|ETIMEDOUT|ECONNRESET|network error|getaddrinfo|fetch failed|connection error)`),
}

func (p *parser) Finish(exitCode int, stderrTail string, timedOut, cancelled bool) domain.AttemptEnd {
	end := domain.AttemptEnd{ExitCode: exitCode, SessionID: p.sessionID, FinalText: p.lastText}
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
		end.Error = domain.Text{Message: classify.Tail(text, 2000)}
	}
	end.Class = patterns.Text(text)
	if p.rateStatus == "rejected" {
		// The stream itself said the limit rejected the session: a plan window is a quota, anything else a rate limit.
		if strings.Contains(p.rateType, "hour") || strings.Contains(p.rateType, "day") || strings.Contains(p.rateType, "week") {
			end.Class = domain.ClassQuotaExhausted
		} else {
			end.Class = domain.ClassRateLimited
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

// compareVersions compares dotted numeric versions.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscanf(pa[i], "%d", &x)
		}
		if i < len(pb) {
			fmt.Sscanf(pb[i], "%d", &y)
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
