// Package codex is the HarnessRunner adapter of the OpenAI Codex CLI: `codex exec --json`, with the permission
// profile translated to Codex's own sandbox modes (D-068). Codex has no session plugin mechanism, so built-in
// agents and skills are delivered as files of the execution and named in the session's instructions (D-092).
package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/catalog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/classify"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

const (
	ID         = domain.HarnessCodex
	Name       = "Codex"
	TestedUpTo = "0.163.0"
)

var requiredFlags = []string{"--json", "--sandbox", "--cd", "--skip-git-repo-check"}

type Adapter struct {
	Home    string
	PathEnv string
	Env     []string
}

func (a *Adapter) ID() string   { return ID }
func (a *Adapter) Name() string { return Name }

func (a *Adapter) codexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(a.Home, ".codex")
}

func (a *Adapter) knownLocations() []string {
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

func (a *Adapter) Detect(ctx context.Context, manualPath string) ports.HarnessInfo {
	info := ports.HarnessInfo{ID: ID, Name: Name, Authentication: "unknown", Readiness: "not_installed",
		Models: []ports.ModelInfo{}, Efforts: []string{}, EnforceableControls: []string{}, Installations: []string{},
		LastCheckedAt: time.Now(), Features: map[string]bool{}}
	found := procutil.LookPath("codex", a.PathEnv, a.knownLocations())
	info.Installations = append(info.Installations, found...)
	switch {
	case manualPath != "":
		info.Path, info.PathSource = manualPath, "manual"
		if st, err := os.Stat(manualPath); err != nil || st.IsDir() {
			info.Diagnostic = "the path set by hand does not exist or is not a file"
			return info
		}
	case len(found) > 0:
		info.Path, info.PathSource = found[0], "known_location"
		for _, d := range strings.Split(a.PathEnv, ":") {
			if d == filepath.Dir(found[0]) {
				info.PathSource = "path"
			}
		}
	default:
		info.Diagnostic = "codex was not found on PATH, in Codex.app, or in the usual install locations"
		return info
	}
	info.Detected = true
	info.Readiness = "installed"
	out, _, err := procutil.Output(ctx, 15*time.Second, a.Env, info.Path, "--version")
	if err != nil {
		info.Diagnostic = "codex --version failed: " + err.Error()
		return info
	}
	if m := versionRe.FindStringSubmatch(out); m != nil {
		info.Version = m[1]
		info.Readiness = "version_identified"
	}
	help, _, _ := procutil.Output(ctx, 15*time.Second, a.Env, info.Path, "exec", "--help")
	for _, f := range append(requiredFlags, "--add-dir", "--ephemeral", "--model", "--color") {
		info.Features[f] = strings.Contains(help, f)
	}
	info.VersionSupport = "supported"
	for _, f := range requiredFlags {
		if !info.Features[f] {
			info.VersionSupport = "below_minimum"
			info.Readiness = "not_ready"
			info.Diagnostic = "this Codex version lacks exec " + f + ", which HarmFlow needs"
		}
	}
	if info.VersionSupport == "supported" && compareVersions(info.Version, TestedUpTo) > 0 {
		info.VersionSupport = "untested_newer"
	}
	info.Models, info.Efforts, info.CapabilitySource = a.models()
	info.EnforceableControls = []string{"sandbox_read_only", "sandbox_workspace_write", "network_access", "writable_roots"}

	_, _, err = procutil.Output(ctx, 20*time.Second, a.Env, info.Path, "login", "status")
	if err == nil {
		info.Authentication = "valid"
	} else if _, ok := err.(interface{ ExitCode() int }); ok {
		info.Authentication = "invalid"
	}
	if info.Readiness == "not_ready" {
		return info
	}
	if info.Authentication == "invalid" {
		info.Readiness = "not_ready"
		info.Diagnostic = "Codex is not signed in; run `codex login` in a terminal"
		return info
	}
	info.Readiness = "ready"
	return info
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
		return fmt.Errorf("Codex %s does not offer the model %q; it offers %s", info.Version, model, strings.Join(ids, ", "))
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
	return fmt.Errorf("Codex model %q does not accept the effort %q; it accepts %s", chosen.ID, effort, strings.Join(efforts, ", "))
}

func (a *Adapter) userDir() catalog.Dir { return catalog.Dir{Root: a.codexHome()} }

func (a *Adapter) UserItem(kind, name string) (ports.ResolvedItem, bool) {
	return a.userDir().Item(kind, name)
}
func (a *Adapter) UserItems(kind string) []ports.ResolvedItem { return a.userDir().Items(kind) }

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
			ports.Restriction{Name: "read_only", Enforced: true, Message: "Codex's read-only sandbox blocks every write"},
			ports.Restriction{Name: "network", Enforced: true, Message: "Codex's read-only sandbox blocks network access"})
	default:
		args = append(args, "--sandbox", "workspace-write",
			"-c", fmt.Sprintf("sandbox_workspace_write.network_access=%t", network))
		restrictions = append(restrictions, ports.Restriction{Name: "restrictToWorkspace", Enforced: true,
			Message: "Codex's workspace-write sandbox confines writes to the workspace and the added directories"})
		if !network {
			restrictions = append(restrictions, ports.Restriction{Name: "network", Enforced: true,
				Message: "Codex's sandbox blocks network access"})
		}
		if perm.Profile == domain.ProfileCustom {
			dirs = append(dirs, perm.Custom.WritablePaths...)
			if len(perm.Custom.AllowedTools) > 0 || len(perm.Custom.DeniedTools) > 0 {
				restrictions = append(restrictions, ports.Restriction{Name: "tool_allowlist", Enforced: false,
					Message: "Codex has no per-tool allow or deny list; the custom tool lists are not enforced"})
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

	prompt, err := a.instructions(req)
	if err != nil {
		return ports.Invocation{}, nil, err
	}
	inv := ports.Invocation{Path: info.Path, Args: args, Env: req.Env, Dir: req.WorkDir, Stdin: prompt,
		Restrictions: restrictions}
	return inv, &parser{}, nil
}

// instructions composes the session's input: HarmFlow's context, the agent's role, the skills to use, and the task's
// prompt. Codex reads a skill by opening its SKILL.md, the way it loads its own skills.
func (a *Adapter) instructions(req ports.RunRequest) (string, error) {
	var b strings.Builder
	if req.Context != "" {
		b.WriteString(req.Context)
		b.WriteString("\n\n")
	}
	if len(req.Agents) > 0 {
		main := req.Agents[0]
		data, err := os.ReadFile(main.Path)
		if err != nil {
			return "", fmt.Errorf("delivering agent %s: %w", main.Name, err)
		}
		_, body := catalog.Parse(data)
		fmt.Fprintf(&b, "## Your role: agent %s\n\n%s\n\n", main.Name, strings.TrimSpace(body))
		for _, other := range req.Agents[1:] {
			fmt.Fprintf(&b, "Another agent is available as instructions you may follow when its role fits: %s at %s\n",
				other.Name, other.Path)
		}
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
	return b.String(), nil
}

type parser struct {
	sessionID string
	completed bool
	failure   string
	lastText  string
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
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     *item  `json:"item"`
	Message  string `json:"message"`
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
		return []ports.HarnessEvent{{Kind: ports.HarnessWarning, Message: classify.Truncate(string(raw), 2000)}}
	}
	switch l.Type {
	case "thread.started":
		p.sessionID = l.ThreadID
		return []ports.HarnessEvent{{Kind: ports.HarnessStarted, SessionID: l.ThreadID}}
	case "turn.completed":
		p.completed = true
	case "turn.failed":
		if l.Error != nil {
			p.failure = l.Error.Message
		} else {
			p.failure = "the turn failed"
		}
	case "error":
		p.failure = firstNonEmpty(l.Message, p.failure)
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
	RateLimit:   regexp.MustCompile(`(?i)(rate[ _-]?limit|too many requests|status 429|\b429\b)`),
	Unavailable: regexp.MustCompile(`(?i)(not logged in|please log in|codex login|authentication failed|invalid api key|401 unauthorized|refresh token|token (has )?expired)`),
	Network:     regexp.MustCompile(`(?i)(stream disconnected|error sending request|connection (refused|reset)|dns error|timed out connecting|network)`),
}

func (p *parser) Finish(exitCode int, stderrTail string, timedOut, cancelled bool) domain.AttemptEnd {
	end := domain.AttemptEnd{ExitCode: exitCode, SessionID: p.sessionID, FinalText: p.lastText}
	switch {
	case cancelled:
		end.Class, end.Message = domain.ClassCancelled, "cancelled"
		return end
	case timedOut:
		end.Class, end.Message = domain.ClassTimeout, "the execution timeout elapsed"
		return end
	case p.completed && p.failure == "" && exitCode == 0:
		return end
	}
	text := strings.TrimSpace(p.failure + "\n" + lastLines(stderrTail, 20))
	if text == "" {
		text = fmt.Sprintf("Codex exited with code %d", exitCode)
	}
	end.Message = classify.Tail(text, 2000)
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
