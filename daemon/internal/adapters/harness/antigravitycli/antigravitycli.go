// Package antigravitycli is the HarnessRunner adapter of Google's Antigravity CLI. It uses the installed CLI and its
// own sign-in as they are: `agy` in its headless mode, the prompt on stdin as stream-json, with stream-json output, and
// the permission profile translated to rules for that session alone, every setting it cannot enforce reported as not
// enforced.
package antigravitycli

import (
	"context"
	"encoding/json"
	"maps"
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
	ID = domain.HarnessAntigravityCLI
	// Name is the harness's display name.
	Name = "Antigravity CLI"
	// binary is the executable's name, which identifies the CLI together with a version-shaped --version output.
	binary = "agy"
	// KeychainService and KeychainAccount name the macOS Keychain item where the CLI keeps its own sign-in. Only the
	// item's presence is checked: the daemon never reads its secret.
	KeychainService = "gemini"
	// KeychainAccount is the account of the CLI's Keychain item; see KeychainService.
	KeychainAccount = "antigravity"
	// updateVariable turns off the CLI's self-update, so a run never replaces the executable the daemon identified.
	updateVariable = "AGY_CLI_DISABLE_AUTO_UPDATE"
)

// credentialVariables are the environment variables through which the CLI would run on a key or on Google Cloud
// credentials rather than its own sign-in. They are removed from every probe and run, never read, so that only the
// CLI's own sign-in counts.
var credentialVariables = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GEMINI_BASE_URL", "AGY_ADC_AUTH",
	"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_QUOTA_PROJECT", "GOOGLE_CLOUD_LOCATION",
	"GOOGLE_GENAI_USE_VERTEXAI"}

// Features the adapter needs from the CLI; a version whose help lacks one is not ready.
var requiredFlags = []string{"--input-format", "--output-format", "--mode", "--settings", "--model"}

// optionalFlags are the features a run uses when the CLI has them.
var optionalFlags = []string{"--add-dir", "--effort"}

// efforts are the reasoning efforts the CLI's --effort accepts; which a model takes is the CLI's to say.
var efforts = []string{domain.DefaultEffort, "low", "medium", "high", "xhigh", "max"}

// modelName is a model as the CLI's `agy models` names it.
var modelName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Adapter is the HarnessAdapter of the Antigravity CLI. It looks for the CLI on PathEnv, then in KnownLocations, and
// runs its probes with Env, without the credential variables. SignedIn tells whether the CLI's own sign-in is there;
// nil leaves the sign-in unknown, for the first run to prove.
type Adapter struct {
	Home    string // the user's home directory
	PathEnv string
	Env     []string
	// KnownLocations are searched after PATH; nil is the usual install locations, an empty list none.
	KnownLocations []string
	// SignedIn reports whether the CLI's own sign-in is stored, without reading it; an error leaves it unknown.
	SignedIn func() (bool, error)
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
// CODEX_HOME of a Codex installation and without the credential variables. It is a copy, never nil.
func (a *Adapter) env() []string { return runEnv(ports.WithoutCodexHome(a.Env)) }

// runEnv is env without the credential variables and with the CLI's self-update off, in a copy.
func runEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(credentialVariables, name) && name != updateVariable {
			out = append(out, kv)
		}
	}
	return append(out, updateVariable+"=true")
}

func (a *Adapter) knownLocations() []string {
	if a.KnownLocations != nil {
		return a.KnownLocations
	}
	return []string{
		filepath.Join(a.Home, ".gemini", "bin", binary), // Google's installer
		"/opt/homebrew/bin/" + binary,
		"/usr/local/bin/" + binary,
		filepath.Join(a.Home, ".local", "bin", binary),
	}
}

// versionRe is a version-shaped --version output, as the CLI prints it: a bare dotted number, optionally with a
// pre-release suffix, and nothing else on the line.
var versionRe = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)(-[0-9A-Za-z.-]+)?$`)

// identifies reports whether a --version output is version-shaped, which with the executable's name identifies the
// CLI: its --version names no harness.
func identifies(out string) bool { return versionRe.MatchString(strings.TrimSpace(out)) }

// Identify implements ports.HarnessIdentifier: path is an executable file named agy whose --version output is a version
// number.
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

// help is the CLI's --help, which it prints to stderr, and whether it was read in full before the timeout.
func (a *Adapter) help(ctx context.Context, path string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, defaultHelpTimeout)
	defer cancel()
	out, errOut, err := procutil.Output(ctx, defaultHelpTimeout, a.env(), path, "--help")
	return out + errOut, err == nil || ctx.Err() == nil
}

// Detect implements ports.HarnessAdapter: it finds the CLI, the path set by hand when there is one, then checks its
// version, the flags HarmFlow needs, and its own sign-in, and says how ready it is. A key or Google Cloud credentials
// in the environment are neither read nor counted, and no sign-in is started for a signed-out CLI.
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
		info.SetDiagnostic(domain.T("antigravity_not_found", "agy was not found on PATH or in the usual install locations"))
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
	help, read := a.help(ctx, info.Path)
	for _, f := range append(slices.Clone(requiredFlags), optionalFlags...) {
		info.Features[f] = strings.Contains(help, f)
	}
	info.FeaturesUnknown = !read
	if info.FeaturesUnknown {
		info.SetDiagnostic(domain.T("harness_help_timed_out",
			"the {{harness}} {{command}} check timed out, so the flags it has are unknown; the first attempt proves them",
			"harness", Name, "command", "--help"))
	}
	// Only 1.5.3 and 1.5.15 have been run against: every version runs, with the untested warning.
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
	if info.HasFeature("--effort") {
		info.Efforts = slices.Clone(efforts)
	}
	info.Models = append(info.Models, ports.ModelInfo{ID: domain.DefaultModel, Efforts: info.Efforts})
	info.CapabilitySource = "declared"
	info.EnforceableControls = []string{"permission_rules", "workspace_files"}

	// Only the CLI's own sign-in, in its own store, reads as signed in; the store is asked whether it holds it, never
	// for it.
	if a.SignedIn != nil {
		if ok, err := a.SignedIn(); err == nil {
			info.Authentication = map[bool]string{true: "valid", false: "invalid"}[ok]
		}
	}
	if info.Readiness == "not_ready" {
		return info
	}
	switch info.Authentication {
	case "valid":
		info.Readiness = "ready"
	case "invalid":
		info.Readiness = "not_ready"
		info.SetDiagnostic(domain.T("antigravity_not_signed_in",
			"the Antigravity CLI is not signed in; run `agy` in a terminal and sign in with a personal Google account"))
	default:
		// The sign-in could not be checked; the first execution proves it rather than blocking on an unknown.
		info.Readiness = "ready"
		if !info.FeaturesUnknown {
			info.SetDiagnostic(domain.T("authentication_unverified", "authentication could not be verified"))
		}
	}
	return info
}

// ValidateProfile implements ports.HarnessAdapter: a model is default or a name as `agy models` lists them, which the
// CLI itself checks when it runs, and an effort is default or one the CLI's --effort accepts.
func (a *Adapter) ValidateProfile(_ ports.HarnessInfo, model, effort string) error {
	if model != "" && model != domain.DefaultModel && !modelName.MatchString(model) {
		return domain.TextErr("antigravity_model_unknown",
			"the Antigravity CLI does not offer the model \"{{model}}\"; use default or a model `agy models` lists", "model", model)
	}
	if effort != "" && !slices.Contains(efforts, effort) {
		return domain.TextErr("antigravity_effort_invalid",
			"the Antigravity CLI does not accept the effort \"{{effort}}\"; it accepts {{efforts}}", "effort", effort,
			"efforts", strings.Join(efforts, ", "))
	}
	return nil
}

// UserItem implements ports.HarnessAdapter: the user's agents and skills are not read from the Antigravity CLI's
// configuration, so none is found.
func (a *Adapter) UserItem(context.Context, ports.HarnessInfo, string, string, string, []string) (ports.ResolvedItem, bool, []ports.DiscoveryError, error) {
	return ports.ResolvedItem{}, false, nil, nil
}

// UserItems implements ports.HarnessAdapter: none, as UserItem.
func (a *Adapter) UserItems(context.Context, ports.HarnessInfo, string, string, []string) ([]ports.ResolvedItem, []ports.DiscoveryError, error) {
	return []ports.ResolvedItem{}, nil, nil
}

// unenforced is the restriction named name, which the Antigravity CLI does not enforce, with the setting the warning
// names.
func unenforced(name, setting string) ports.Restriction {
	return ports.Restriction{Name: name, Enforced: false, Text: domain.T("restriction_antigravity_unenforced",
		"the Antigravity CLI does not enforce {{setting}}; it runs with a warning", "setting", setting)}
}

// sessionSettings are the CLI's settings for one run alone, passed with --settings and never saved: the permission
// rules the profile becomes, and file tools kept to the workspace and its added folders.
type sessionSettings struct {
	AllowNonWorkspaceAccess bool             `json:"allowNonWorkspaceAccess"`
	Permissions             sessionRuleLists `json:"permissions"`
}

type sessionRuleLists struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// commandRule is the CLI's rule for a command that starts with program.
func commandRule(program string) string { return "command(" + program + ")" }

// Prepare implements ports.HarnessAdapter: a headless `agy` run reading stream-json from stdin and writing stream-json,
// the prompt and HarmFlow's context in one user message, the model and effort asked for, the task's extra folders added
// to the workspace, and the permission profile translated to rules for this run alone. With no one to approve, the CLI
// refuses any action no rule allows: it never writes a file outside the workspace and its added folders, given by their
// real paths, and it starts a command only where a rule allows it. Commands are not sandboxed, so what a command may
// reach is reported as not enforced.
func (a *Adapter) Prepare(info ports.HarnessInfo, req ports.RunRequest) (ports.Invocation, ports.OutputParser, error) {
	args := []string{"--input-format", "stream-json", "--output-format", "stream-json", "--mode", "accept-edits"}
	if req.Model != "" && req.Model != domain.DefaultModel {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" && req.Effort != domain.DefaultEffort && info.HasFeature("--effort") {
		args = append(args, "--effort", req.Effort)
	}
	perm := req.Permissions
	restrictions := []ports.Restriction{}
	settings := sessionSettings{Permissions: sessionRuleLists{Allow: []string{}, Deny: []string{}}}
	dirs := append([]string{}, req.AdditionalDirs...)
	commands := false
	switch perm.Profile {
	case domain.ProfileReadOnly:
		// Denied, not only left unallowed: the CLI adds the user's own allow rules to the session's, and a denial wins.
		settings.Permissions.Deny = append(settings.Permissions.Deny, "write_file(*)", commandRule("*"))
	case domain.ProfileCustom:
		commands = true
		if c := perm.Custom; c != nil {
			dirs = append(dirs, c.WritablePaths...)
			if len(c.AllowedPrograms) > 0 {
				for _, p := range c.AllowedPrograms {
					settings.Permissions.Allow = append(settings.Permissions.Allow, commandRule(p))
				}
			} else {
				settings.Permissions.Allow = append(settings.Permissions.Allow, commandRule("*"))
			}
			for _, p := range c.DeniedPrograms {
				settings.Permissions.Deny = append(settings.Permissions.Deny, commandRule(p))
			}
			if len(c.AllowedTools) > 0 || len(c.DeniedTools) > 0 {
				restrictions = append(restrictions, unenforced("tool_allowlist", "the custom tool lists"))
			}
			if len(c.ReadablePaths) > 0 {
				restrictions = append(restrictions, unenforced("readablePaths", "the custom read paths"))
			}
		} else {
			settings.Permissions.Allow = append(settings.Permissions.Allow, commandRule("*"))
		}
		if perm.RestrictToWorkspace != nil && *perm.RestrictToWorkspace {
			restrictions = append(restrictions, unenforced("restrictToWorkspace", "restrictToWorkspace for the commands it runs"))
		}
	default: // workspace_write
		commands = true
		settings.Permissions.Allow = append(settings.Permissions.Allow, commandRule("*"))
		if perm.RestrictToWorkspace == nil || *perm.RestrictToWorkspace {
			restrictions = append(restrictions, unenforced("restrictToWorkspace", "restrictToWorkspace for the commands it runs"))
		}
	}
	if perm.Network == domain.NetworkDeny {
		settings.Permissions.Deny = append(settings.Permissions.Deny, "read_url(*)", "execute_url(*)")
		if commands {
			restrictions = append(restrictions, unenforced("network", "network: deny for the commands it runs"))
		}
	}
	if req.ExecutionDir != "" {
		dirs = append(dirs, req.ExecutionDir)
	}
	if info.HasFeature("--add-dir") {
		work := resolved(req.WorkDir)
		for _, d := range uniq(mapSlice(dirs, resolved)) {
			if d != work {
				args = append(args, "--add-dir", d)
			}
		}
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return ports.Invocation{}, nil, err
	}
	args = append(args, "--settings", string(encoded))
	prompt := req.Prompt
	if req.Context != "" {
		prompt = req.Context + "\n\n" + req.Prompt
	}
	message, err := json.Marshal(userMessage{Event: "user", Message: userContent{Content: prompt}})
	if err != nil {
		return ports.Invocation{}, nil, err
	}
	inv := ports.Invocation{Path: info.Path, Args: args, Env: runEnv(req.Env), Dir: req.WorkDir,
		Stdin: string(message) + "\n", Restrictions: restrictions}
	return inv, &parser{texts: map[int]*strings.Builder{}, tools: map[int]bool{}}, nil
}

// userMessage is the one stream-json line a run reads from stdin: the user's turn.
type userMessage struct {
	Event   string      `json:"event"`
	Message userContent `json:"message"`
}

type userContent struct {
	Content string `json:"content"`
}

// streamLine is one line of the CLI's stream-json output: init, step_update, or result, the payload under the key the
// event names.
type streamLine struct {
	Event          string      `json:"event"`
	ConversationID string      `json:"conversation_id"`
	Init           *initInfo   `json:"init"`
	StepUpdate     *stepUpdate `json:"step_update"`
	Result         *result     `json:"result"`
}

type initInfo struct {
	Model string `json:"model"`
}

type stepUpdate struct {
	ConversationID string    `json:"conversation_id"`
	StepIndex      int       `json:"step_index"`
	State          string    `json:"state"`
	StepType       string    `json:"step_type"`
	ToolName       string    `json:"tool_name"`
	TextDelta      string    `json:"text_delta"`
	ToolInfo       *toolInfo `json:"tool_info"`
}

type toolInfo struct {
	Name       string          `json:"name"`
	Parameters json.RawMessage `json:"parameters"`
	Output     string          `json:"output"`
	Error      *stepError      `json:"error"`
}

type stepError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type result struct {
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	Response       string `json:"response"`
	Error          string `json:"error"`
}

// parser turns one attempt's stream-json lines into the normalized events the other harnesses produce.
type parser struct {
	sessionID string
	texts     map[int]*strings.Builder // the agent's responses being written, by step
	lastText  string
	tools     map[int]bool // the tool steps already reported as started
	result    *result
}

// flushText ends the agent's response at step, as one message.
func (p *parser) flushText(step int) []ports.HarnessEvent {
	b, ok := p.texts[step]
	if !ok {
		return nil
	}
	delete(p.texts, step)
	t := strings.TrimSpace(b.String())
	if t == "" {
		return nil
	}
	p.lastText = t
	return []ports.HarnessEvent{{Kind: ports.HarnessMessage, Message: t}}
}

// flushAll ends every response still being written, in step order.
func (p *parser) flushAll() []ports.HarnessEvent {
	var evs []ports.HarnessEvent
	for _, s := range slices.Sorted(maps.Keys(p.texts)) {
		evs = append(evs, p.flushText(s)...)
	}
	return evs
}

// Line implements ports.OutputParser.
func (p *parser) Line(line []byte) []ports.HarnessEvent {
	var sl streamLine
	if json.Unmarshal(line, &sl) != nil {
		return nil
	}
	switch sl.Event {
	case "init":
		p.sessionID = sl.ConversationID
		ev := ports.HarnessEvent{Kind: ports.HarnessStarted, SessionID: sl.ConversationID}
		if sl.Init != nil {
			ev.Model = sl.Init.Model
		}
		return []ports.HarnessEvent{ev}
	case "step_update":
		if sl.StepUpdate == nil {
			return nil
		}
		return p.step(*sl.StepUpdate)
	case "result":
		if sl.Result == nil {
			return nil
		}
		evs := p.flushAll()
		r := *sl.Result
		p.result = &r
		if p.sessionID == "" {
			p.sessionID = r.ConversationID
		}
		if p.lastText == "" {
			p.lastText = strings.TrimSpace(r.Response)
		}
		return evs
	}
	return nil
}

// step turns one step's update into events: an agent's response is one message once its step is done, and a tool
// step starts once and completes, or fails, once.
func (p *parser) step(su stepUpdate) []ports.HarnessEvent {
	if p.sessionID == "" {
		p.sessionID = su.ConversationID
	}
	switch su.StepType {
	case "agent_response":
		b, ok := p.texts[su.StepIndex]
		if !ok {
			b = &strings.Builder{}
			p.texts[su.StepIndex] = b
		}
		b.WriteString(su.TextDelta)
		if su.State == "ACTIVE" {
			return nil
		}
		return p.flushText(su.StepIndex)
	case "tool":
		id := strconv.Itoa(su.StepIndex)
		tool := su.ToolName
		var evs []ports.HarnessEvent
		if !p.tools[su.StepIndex] {
			p.tools[su.StepIndex] = true
			evs = append(p.flushAll(), startedEvent(id, tool, su.ToolInfo))
		}
		if su.State == "ACTIVE" {
			return evs
		}
		done := ports.HarnessEvent{Kind: ports.HarnessToolDone, ToolID: id, Tool: tool, IsError: su.State == "ERROR"}
		if ti := su.ToolInfo; ti != nil {
			done.Output = classify.Truncate(ti.Output, 2000)
			if ti.Error != nil {
				done.IsError = true
				if done.Output == "" {
					done.Output = classify.Truncate(ti.Error.Message, 2000)
				}
			}
		}
		return append(evs, done)
	}
	return nil
}

// startedEvent is a tool step's start, with the command it runs or the file it touches when its parameters name one.
func startedEvent(id, tool string, ti *toolInfo) ports.HarnessEvent {
	ev := ports.HarnessEvent{Kind: ports.HarnessToolStarted, ToolID: id, Tool: tool}
	if ti == nil {
		return ev
	}
	var params map[string]any
	if json.Unmarshal(ti.Parameters, &params) == nil {
		if c, ok := params["CommandLine"].(string); ok {
			ev.Command = c
		}
		for _, k := range []string{"Path", "AbsolutePath", "TargetFile"} {
			if f, ok := params[k].(string); ok && ev.Path == "" {
				ev.Path = f
			}
		}
	}
	return ev
}

// signedOutTexts are the CLI's own words that its sign-in is missing or expired.
const signedOutTexts = `not logged into Antigravity|authentication required|Sign in once with the Antigravity CLI|failed to retrieve token|invalid_grant|token has been expired|unauthenticated`

var signedOut = regexp.MustCompile(`(?i)(` + signedOutTexts + `)`)

// unusableTexts are the CLI's own words that it will not run although it is signed in: the signed-in account is not
// eligible for Antigravity. Signing in again with the same account changes nothing.
const unusableTexts = `not eligible for Antigravity|Eligibility check failed`

var unusable = regexp.MustCompile(`(?i)(` + unusableTexts + `)`)

// deniedText is the CLI's notice that headless mode refused an action no rule allowed, after which the run ends
// without its answer.
var deniedText = regexp.MustCompile(`(?i)headless mode cannot prompt for, so it was auto-denied`)

// patterns match the CLI's own error texts; quota is checked before rate limits, as its texts mention limits too. Its
// quota and rate-limit texts have not been seen from the real CLI, so they are the usual Google API ones.
var patterns = classify.Patterns{
	Quota:       regexp.MustCompile(`(?i)(quota exceeded|exceeded your current quota|exhausted your (weekly |daily )?quota|quota (has been )?(reached|exhausted)|usage limit reached|out of credits)`),
	RateLimit:   regexp.MustCompile(`(?i)(rate[ _-]?limit|too many requests|resource_exhausted|status 429|code"?:? ?429)`),
	Unavailable: regexp.MustCompile(`(?i)(` + signedOutTexts + `|` + unusableTexts + `)`),
	Network:     regexp.MustCompile(`(?i)(connection refused|no such host|dial tcp|i/o timeout|network is unreachable|tls handshake timeout|connection reset)`),
}

// Finish implements ports.OutputParser: a run that exited 0 with a successful result and an answer completed; any other
// is classified from the CLI's result and its stderr. A run whose action headless mode refused ends with no answer, and
// is a task error naming the permission; an account the CLI will not serve is an unavailable harness, not a sign-out.
func (p *parser) Finish(exitCode int, stderrTail string, timedOut, cancelled bool) domain.AttemptEnd {
	p.flushAll()
	end := domain.AttemptEnd{ExitCode: exitCode, SessionID: p.sessionID, FinalText: p.lastText, Usage: domain.NotReported()}
	switch {
	case cancelled:
		end.Class, end.Error = domain.ClassCancelled, domain.T("execution_cancelled", "cancelled")
		return end
	case timedOut:
		end.Class, end.Error = domain.ClassTimeout, domain.T("execution_timed_out", "the execution timeout elapsed")
		return end
	}
	stderr := strings.TrimSpace(stderrTail)
	succeeded := p.result != nil && p.result.Status == "SUCCESS"
	if exitCode == 0 && succeeded && !(p.lastText == "" && deniedText.MatchString(stderr)) {
		return end
	}
	var parts []string
	if p.result != nil && p.result.Error != "" {
		parts = append(parts, p.result.Error)
	}
	if stderr != "" && (len(parts) == 0 || !strings.Contains(stderr, parts[0])) {
		parts = append(parts, stderr)
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	switch {
	case text != "":
		end.Error = domain.Text{Message: classify.Tail(eventlog.Redact(text), 2000)}
	case p.result != nil && p.result.Status != "":
		end.Error = domain.T("harness_ended_with_error", "the harness ended with an error")
	default:
		end.Error = domain.T("harness_exit_code", "{{harness}} exited with code {{code}}", "harness", Name,
			"code", strconv.Itoa(exitCode))
	}
	end.Class = patterns.Text(text)
	if unusable.MatchString(text) {
		end.Class = domain.ClassHarnessUnavailable
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

// resolved is path with its symbolic links followed, or path as it is when it does not resolve: the CLI checks a file
// tool's target by its real path, so an added folder given through a link, as macOS's /var is, would refuse every write
// into it.
func resolved(path string) string {
	if path == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

func mapSlice(list []string, f func(string) string) []string {
	var out []string
	for _, s := range list {
		out = append(out, f(s))
	}
	return out
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
