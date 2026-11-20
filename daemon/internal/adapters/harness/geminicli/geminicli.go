// Package geminicli is the HarnessRunner adapter of Google's Gemini CLI. It uses the installed CLI and its own sign-in
// as they are: `gemini` in its non-interactive mode, the prompt on stdin, with stream-json output, and the permission
// profile translated to the CLI's approval mode, every setting it cannot enforce reported as not enforced.
package geminicli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/classify"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

const (
	// ID is the harness's identifier, as a task's provider names it.
	ID = domain.HarnessGeminiCLI
	// Name is the harness's display name.
	Name = "Gemini CLI"
	// binary is the executable's name, which identifies the CLI together with a version-shaped --version output.
	binary = "gemini"
	// homeVariable moves the folder that holds the CLI's own .gemini folder, its settings and its sign-in.
	homeVariable = "GEMINI_CLI_HOME"
	// credentialsFile is the file, in the CLI's own .gemini folder, where its own Google sign-in is kept. Only its
	// presence is checked: the daemon never reads it.
	credentialsFile = "oauth_creds.json"
)

// apiKeyVariables are the environment variables through which the CLI would sign in with a key rather than its own
// sign-in. They are removed from every probe and run, never read, so that only the CLI's own sign-in counts.
var apiKeyVariables = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS",
	"GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_GENAI_USE_GCA"}

// Features the adapter needs from the CLI; a version whose help lacks one is not ready.
var requiredFlags = []string{"--output-format", "--approval-mode", "--model"}

var declaredModels = []string{domain.DefaultModel, "gemini-2.5-pro", "gemini-2.5-flash"}

// Adapter is the HarnessAdapter of the Gemini CLI. It looks for the CLI on PathEnv, then in KnownLocations, and runs
// its probes with Env, without the API-key variables. Its sign-in is read from the .gemini folder under Home, or under
// GEMINI_CLI_HOME.
type Adapter struct {
	Home    string // the user's home directory
	PathEnv string
	Env     []string
	// KnownLocations are searched after PATH; nil is the usual install locations, an empty list none.
	KnownLocations []string
}

// defaultHelpTimeout bounds the --help check; help it could not read by then leaves the flags unknown.
const defaultHelpTimeout = 15 * time.Second

var (
	_ ports.HarnessAdapter    = (*Adapter)(nil)
	_ ports.HarnessIdentifier = (*Adapter)(nil)
)

// ID implements ports.HarnessAdapter: the harness's identifier.
func (a *Adapter) ID() string { return ID }

// Name implements ports.HarnessAdapter: the harness's display name.
func (a *Adapter) Name() string { return Name }

// env is the environment the CLI's probes and runs start with: Env, or the daemon's own when Env is nil, without the
// CODEX_HOME of a Codex installation and without the API-key variables. It is a copy, never nil.
func (a *Adapter) env() []string { return withoutKeys(ports.WithoutCodexHome(a.Env)) }

// withoutKeys is env without the API-key variables, in a copy.
func withoutKeys(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(apiKeyVariables, name) {
			out = append(out, kv)
		}
	}
	return out
}

// configDir is the CLI's own .gemini folder: under GEMINI_CLI_HOME when the environment sets it, else under Home.
func (a *Adapter) configDir() string {
	env := a.Env
	if env == nil {
		env = os.Environ()
	}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, homeVariable+"="); ok && v != "" {
			return filepath.Join(v, ".gemini")
		}
	}
	return filepath.Join(a.Home, ".gemini")
}

func (a *Adapter) knownLocations() []string {
	if a.KnownLocations != nil {
		return a.KnownLocations
	}
	return []string{
		"/opt/homebrew/bin/gemini",
		"/usr/local/bin/gemini",
		filepath.Join(a.Home, ".local", "bin", "gemini"),
		filepath.Join(a.Home, ".npm-global", "bin", "gemini"),
		filepath.Join(a.Home, ".bun", "bin", "gemini"),
	}
}

// versionRe is a version-shaped --version output, as the CLI prints it: a bare dotted number, optionally with a
// pre-release suffix, and nothing else on the line.
var versionRe = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)(-[0-9A-Za-z.-]+)?$`)

// identifies reports whether a --version output is version-shaped, which with the executable's name identifies the
// CLI: its --version names no harness.
func identifies(out string) bool { return versionRe.MatchString(strings.TrimSpace(out)) }

// Identify implements ports.HarnessIdentifier: path is an executable file named gemini whose --version output is a
// version number.
func (a *Adapter) Identify(ctx context.Context, path string) error {
	if filepath.Base(path) != binary {
		return domain.TextErr("harness_not_identified",
			"{{path}} is not {{harness}}: its --version output does not name it", "path", path, "harness", Name)
	}
	_, err := procutil.IdentifyHarness(ctx, procutil.VersionTimeout, a.env(), path, Name, binary, identifies)
	return err
}

// firstIdentified returns the first candidate that is the CLI, with its --version output, or the first candidate and
// why it failed when none is.
func (a *Adapter) firstIdentified(ctx context.Context, candidates []string) (string, string, error) {
	var first error
	for _, c := range candidates {
		err := a.Identify(ctx, c)
		if err == nil {
			out, _, _ := procutil.Output(ctx, procutil.VersionTimeout, a.env(), c, "--version")
			return out, c, nil
		}
		if first == nil {
			first = err
		}
	}
	return "", candidates[0], first
}

// Detect implements ports.HarnessAdapter: it finds the CLI, the path set by hand when there is one, then checks its
// version, the flags HarmFlow needs, and its own sign-in, and says how ready it is. An API key in the environment is
// neither read nor counted, and no sign-in is started for a signed-out CLI.
func (a *Adapter) Detect(ctx context.Context, manualPath string) ports.HarnessInfo {
	info := ports.HarnessInfo{ID: ID, Name: Name, Authentication: "unknown", Readiness: "not_installed",
		Models: []ports.ModelInfo{}, Efforts: []string{}, EnforceableControls: []string{}, Installations: []string{},
		LastCheckedAt: time.Now(), Features: map[string]bool{}}
	found := procutil.LookPath(binary, a.PathEnv, a.knownLocations())
	info.Installations = append(info.Installations, found...)
	candidates := found
	switch {
	case manualPath != "":
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
		info.SetDiagnostic(domain.T("gemini_not_found", "gemini was not found on PATH or in the usual install locations"))
		return info
	}
	out, chosen, err := a.firstIdentified(ctx, candidates)
	info.Installation, info.Path = chosen, chosen
	if manualPath == "" {
		info.PathSource = "known_location"
		if inPath(chosen, a.PathEnv) {
			info.PathSource = "path"
		}
	}
	info.Detected = true
	info.Readiness = "installed"
	if err != nil {
		info.SetDiagnostic(domain.TextOf(err))
		return info
	}
	if m := versionRe.FindStringSubmatch(strings.TrimSpace(out)); m != nil {
		info.Version = m[1]
		info.Readiness = "version_identified"
	}
	help, read := procutil.Help(ctx, defaultHelpTimeout, a.env(), info.Path, "--help")
	for _, f := range append(requiredFlags, "--include-directories") {
		info.Features[f] = strings.Contains(help, f)
	}
	info.FeaturesUnknown = !read
	if info.FeaturesUnknown {
		info.SetDiagnostic(domain.T("harness_help_timed_out",
			"the {{harness}} {{command}} check timed out, so the flags it has are unknown; the first attempt proves them",
			"harness", Name, "command", "--help"))
	}
	// No release has been tested against the real CLI yet: every version runs, with the untested warning.
	info.VersionSupport = "untested_newer"
	if info.Version == "" {
		info.VersionSupport = "unknown"
	}
	for _, f := range requiredFlags {
		if !info.HasFeature(f) {
			info.VersionSupport = "below_minimum"
			info.Readiness = "not_ready"
			info.SetDiagnostic(domain.T("harness_feature_missing", "this {{harness}} version lacks {{feature}}, which HarmFlow needs",
				"harness", Name, "feature", f))
		}
	}
	info.Efforts = []string{domain.DefaultEffort}
	for _, m := range declaredModels {
		info.Models = append(info.Models, ports.ModelInfo{ID: m, Efforts: info.Efforts})
	}
	info.CapabilitySource = "declared"
	info.EnforceableControls = []string{"approval_mode"}

	// Only the CLI's own sign-in, in its own store, reads as signed in; the store's presence alone is checked.
	if st, err := os.Stat(filepath.Join(a.configDir(), credentialsFile)); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
		info.Authentication = "valid"
	} else {
		info.Authentication = "invalid"
	}
	if info.Readiness == "not_ready" {
		return info
	}
	if info.Authentication == "valid" {
		info.Readiness = "ready"
		return info
	}
	info.Readiness = "not_ready"
	info.SetDiagnostic(domain.T("gemini_not_signed_in", "the Gemini CLI is not signed in; run `gemini` in a terminal and sign in with Google"))
	return info
}

// ValidateProfile implements ports.HarnessAdapter: a model is default, one the adapter declares, or a full gemini-*
// name; the CLI has no effort setting, so only default passes.
func (a *Adapter) ValidateProfile(_ ports.HarnessInfo, model, effort string) error {
	if model != "" && model != domain.DefaultModel && !strings.HasPrefix(model, "gemini-") {
		return domain.TextErr("gemini_model_unknown",
			`the Gemini CLI does not offer the model "{{model}}"; use default or a full gemini-* model name`, "model", model)
	}
	if effort != "" && effort != domain.DefaultEffort {
		return domain.TextErr("gemini_effort_unsupported", "the Gemini CLI has no effort setting; leave the effort as default")
	}
	return nil
}

// UserItem implements ports.HarnessAdapter: the user's agents and skills are not read from the Gemini CLI's
// configuration, so none is found.
func (a *Adapter) UserItem(context.Context, ports.HarnessInfo, string, string, string, []string) (ports.ResolvedItem, bool, []ports.DiscoveryError, error) {
	return ports.ResolvedItem{}, false, nil, nil
}

// UserItems implements ports.HarnessAdapter: none, as UserItem.
func (a *Adapter) UserItems(context.Context, ports.HarnessInfo, string, string, []string) ([]ports.ResolvedItem, []ports.DiscoveryError, error) {
	return []ports.ResolvedItem{}, nil, nil
}

// unenforced is the restriction named name, which the Gemini CLI does not enforce, with the setting the warning names.
func unenforced(name, setting string) ports.Restriction {
	return ports.Restriction{Name: name, Enforced: false, Text: domain.T("restriction_gemini_unenforced",
		"the Gemini CLI does not enforce {{setting}}; it runs with a warning", "setting", setting)}
}

// Prepare implements ports.HarnessAdapter: a non-interactive `gemini` run with stream-json output, the prompt and
// HarmFlow's context on stdin, the model asked for, the task's extra folders included, and the permission profile
// translated to the CLI's approval mode. The CLI's sandbox is not turned on, so the workspace limits, the network
// setting, and a custom profile's lists are reported as not enforced.
func (a *Adapter) Prepare(info ports.HarnessInfo, req ports.RunRequest) (ports.Invocation, ports.OutputParser, error) {
	args := []string{"--output-format", "stream-json"}
	if req.Model != "" && req.Model != domain.DefaultModel {
		args = append(args, "--model", req.Model)
	}
	perm := req.Permissions
	restrictions := []ports.Restriction{}
	network := perm.Network != domain.NetworkDeny
	dirs := append([]string{}, req.AdditionalDirs...)
	switch perm.Profile {
	case domain.ProfileReadOnly:
		// The default approval mode asks before a tool that writes or runs a command, and with no one to answer, the
		// non-interactive run refuses it. Unverified against the real CLI, so it is reported as not enforced.
		args = append(args, "--approval-mode", "default")
		restrictions = append(restrictions, unenforced("read_only", "read_only"))
	case domain.ProfileCustom:
		args = append(args, "--approval-mode", "yolo")
		if c := perm.Custom; c != nil {
			dirs = append(dirs, c.WritablePaths...)
			if len(c.AllowedTools) > 0 || len(c.DeniedTools) > 0 {
				restrictions = append(restrictions, unenforced("tool_allowlist", "the custom tool lists"))
			}
			if c.HasProgramLists() {
				restrictions = append(restrictions, unenforced("program_lists", "the custom program lists"))
			}
			if len(c.ReadablePaths) > 0 {
				restrictions = append(restrictions, unenforced("readablePaths", "the custom read paths"))
			}
		}
		if perm.RestrictToWorkspace != nil && *perm.RestrictToWorkspace {
			restrictions = append(restrictions, unenforced("restrictToWorkspace", "restrictToWorkspace"))
		}
	default: // workspace_write
		args = append(args, "--approval-mode", "yolo")
		if perm.RestrictToWorkspace == nil || *perm.RestrictToWorkspace {
			restrictions = append(restrictions, unenforced("restrictToWorkspace", "restrictToWorkspace"))
		}
	}
	if !network {
		restrictions = append(restrictions, unenforced("network", "network: deny"))
	}
	if req.ExecutionDir != "" {
		dirs = append(dirs, req.ExecutionDir)
	}
	var include []string
	for _, d := range uniq(dirs) {
		if d != req.WorkDir {
			include = append(include, d)
		}
	}
	if len(include) > 0 && info.HasFeature("--include-directories") {
		args = append(args, "--include-directories", strings.Join(include, ","))
	}
	stdin := req.Prompt
	if req.Context != "" {
		stdin = req.Context + "\n\n" + req.Prompt
	}
	inv := ports.Invocation{Path: info.Path, Args: args, Env: withoutKeys(req.Env), Dir: req.WorkDir, Stdin: stdin,
		Restrictions: restrictions}
	return inv, &parser{tools: map[string]string{}}, nil
}

// streamLine is one line of the CLI's stream-json output: init, message, tool_use, tool_result, error, or result.
type streamLine struct {
	Type       string          `json:"type"`
	SessionID  string          `json:"session_id"`
	Model      string          `json:"model"`
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	Delta      bool            `json:"delta"`
	ToolName   string          `json:"tool_name"`
	ToolID     string          `json:"tool_id"`
	Parameters json.RawMessage `json:"parameters"`
	Status     string          `json:"status"`
	Output     string          `json:"output"`
	Severity   string          `json:"severity"`
	Message    string          `json:"message"`
	Error      *streamError    `json:"error"`
}

type streamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// parser turns one attempt's stream-json lines into the normalized events the other harnesses produce.
type parser struct {
	sessionID string
	text      strings.Builder // the assistant's current message, built from its chunks
	lastText  string
	tools     map[string]string // tool names by id
	result    *streamLine
	errors    []string
}

func (p *parser) flushText() []ports.HarnessEvent {
	t := strings.TrimSpace(p.text.String())
	p.text.Reset()
	if t == "" {
		return nil
	}
	p.lastText = t
	return []ports.HarnessEvent{{Kind: ports.HarnessMessage, Message: t}}
}

// Line implements ports.OutputParser.
func (p *parser) Line(line []byte) []ports.HarnessEvent {
	var sl streamLine
	if json.Unmarshal(line, &sl) != nil {
		return nil
	}
	switch sl.Type {
	case "init":
		p.sessionID = sl.SessionID
		return []ports.HarnessEvent{{Kind: ports.HarnessStarted, SessionID: sl.SessionID, Model: sl.Model}}
	case "message":
		if sl.Role != "assistant" {
			return nil
		}
		if sl.Delta {
			p.text.WriteString(sl.Content)
			return nil
		}
		evs := p.flushText()
		p.text.WriteString(sl.Content)
		return append(evs, p.flushText()...)
	case "tool_use":
		evs := p.flushText()
		p.tools[sl.ToolID] = sl.ToolName
		ev := ports.HarnessEvent{Kind: ports.HarnessToolStarted, ToolID: sl.ToolID, Tool: sl.ToolName}
		var params map[string]any
		if json.Unmarshal(sl.Parameters, &params) == nil {
			if c, ok := params["command"].(string); ok {
				ev.Command = c
			}
			if f, ok := params["file_path"].(string); ok {
				ev.Path = f
			}
		}
		return append(evs, ev)
	case "tool_result":
		ev := ports.HarnessEvent{Kind: ports.HarnessToolDone, ToolID: sl.ToolID, Tool: p.tools[sl.ToolID],
			IsError: sl.Status == "error", Output: classify.Truncate(sl.Output, 2000)}
		if sl.Error != nil && ev.Output == "" {
			ev.Output = classify.Truncate(sl.Error.Message, 2000)
		}
		return []ports.HarnessEvent{ev}
	case "error":
		if sl.Severity == "error" {
			p.errors = append(p.errors, sl.Message)
		}
		return []ports.HarnessEvent{{Kind: ports.HarnessWarning, Message: classify.Truncate(sl.Message, 2000)}}
	case "result":
		evs := p.flushText()
		r := sl
		p.result = &r
		return evs
	}
	return nil
}

// signedOutTexts are the CLI's own words that its sign-in is missing or expired.
const signedOutTexts = `failed to sign in|login required|please (sign|log) ?in|not authenticated|authentication (failed|required)|invalid_grant|token has been expired|unauthenticated`

var signedOut = regexp.MustCompile(`(?i)(` + signedOutTexts + `)`)

// authExitCode is the CLI's exit code for a fatal authentication error.
const authExitCode = 41

// patterns match the CLI's own error texts; quota is checked before rate limits, as its texts mention limits too.
var patterns = classify.Patterns{
	Quota:       regexp.MustCompile(`(?i)(quota exceeded|exceeded your current quota|exhausted your (daily )?quota|daily (request )?limit|usage limit reached)`),
	RateLimit:   regexp.MustCompile(`(?i)(rate[ _-]?limit|too many requests|resource_exhausted|status 429|code"?:? ?429)`),
	Unavailable: regexp.MustCompile(`(?i)(` + signedOutTexts + `)`),
	Network:     regexp.MustCompile(`(?i)(ECONNREFUSED|ENOTFOUND|ETIMEDOUT|ECONNRESET|EAI_AGAIN|network error|getaddrinfo|fetch failed|connection error)`),
}

// Finish implements ports.OutputParser: a run that exited 0 with a successful result completed; any other is classified
// from the CLI's result, its error lines, and its stderr, an exit code of 41 reading as signed out.
func (p *parser) Finish(exitCode int, stderrTail string, timedOut, cancelled bool) domain.AttemptEnd {
	p.flushText()
	end := domain.AttemptEnd{ExitCode: exitCode, SessionID: p.sessionID, FinalText: p.lastText, Usage: domain.NotReported()}
	switch {
	case cancelled:
		end.Class, end.Error = domain.ClassCancelled, domain.T("execution_cancelled", "cancelled")
		return end
	case timedOut:
		end.Class, end.Error = domain.ClassTimeout, domain.T("execution_timed_out", "the execution timeout elapsed")
		return end
	}
	if exitCode == 0 && (p.result == nil || p.result.Status != "error") {
		return end
	}
	parts := append([]string{}, p.errors...)
	if p.result != nil && p.result.Error != nil {
		parts = append(parts, p.result.Error.Message)
	}
	if t := strings.TrimSpace(stderrTail); t != "" {
		parts = append(parts, t)
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		end.Error = domain.T("harness_exit_code", "{{harness}} exited with code {{code}}", "harness", Name,
			"code", strconv.Itoa(exitCode))
	} else {
		end.Error = domain.Text{Message: classify.Tail(eventlog.Redact(text), 2000)}
	}
	end.Class = patterns.Text(text)
	if exitCode == authExitCode {
		end.Class = domain.ClassHarnessUnavailable
		end.SignedOut = true
		return end
	}
	end.SignedOut = end.Class == domain.ClassHarnessUnavailable && signedOut.MatchString(text)
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
