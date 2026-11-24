// Package codex is the HarnessRunner adapter of the OpenAI Codex CLI: its app server, with the permission profile
// translated to Codex's own sandbox modes, and the approvals it asks for relayed to the owner. Codex has no session
// plugin mechanism, so built-in agents and skills are delivered as files of the execution and named in the session's
// instructions.
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
	ID = domain.HarnessCodex
	// Name is the harness's display name.
	Name = "Codex"
	// TestedUpTo is the newest version HarmFlow was tested with; a newer one runs as untested.
	TestedUpTo = "0.163.0"
)

// Adapter is the HarnessAdapter of the Codex CLI. It looks for the CLI on PathEnv, then in KnownLocations, and runs its
// probes with Env, each within ProbeTimeout. Each Codex home it finds, `.codex` under Home and the one the daemon's
// CODEX_HOME names, is an installation of its own: its sign-in, models cache, configuration, and agents are read from
// that home, and its probes and attempts run with it as CODEX_HOME. The user's skills are the ones Codex's app server
// lists, from one server per home that it keeps running until Close.
type Adapter struct {
	Home    string
	PathEnv string
	Env     []string
	// KnownLocations are searched after PATH; nil is the usual install locations, an empty list none.
	KnownLocations []string
	// ClientVersion is the version HarmFlow names itself with to the app server.
	ClientVersion string
	// ProbeTimeout limits each probe of the CLI: its --version, its help texts, and its sign-in status. Zero is each
	// probe's own limit, 15 seconds, or 20 for the sign-in.
	ProbeTimeout time.Duration

	mu          sync.Mutex
	servers     map[string]*appServer // by the home each runs with
	reload      bool                  // the next skills/list of each home not in reloaded carries forceReload
	reloaded    map[string]bool       // the servers (serverKey) whose skills/list carried forceReload since ReloadUserItems
	listTimeout time.Duration         // a call to the app server; zero is defaultListTimeout
	helpTimeout time.Duration         // the top-level --help check; zero is the probe's own limit
	signInStep  time.Duration         // the wait for `codex login` to reach its browser's step; zero is defaultSignInStep
	entryHome   string                // the one home of an adapter ProbeEntry made; empty for the daemon's own
}

var (
	_ ports.HarnessAdapter     = (*Adapter)(nil)
	_ ports.HarnessIdentifier  = (*Adapter)(nil)
	_ ports.ProfileWarner      = (*Adapter)(nil)
	_ ports.ItemReloader       = (*Adapter)(nil)
	_ ports.HarnessCloser      = (*Adapter)(nil)
	_ ports.InstallationLister = (*Adapter)(nil)
	_ ports.QuotaReader        = (*Adapter)(nil)
	_ ports.SessionKeeper      = (*Adapter)(nil)
	_ ports.EntryProber        = (*Adapter)(nil)
)

// ID implements ports.HarnessAdapter: the harness's identifier.
func (a *Adapter) ID() string { return ID }

// Name implements ports.HarnessAdapter: the harness's display name.
func (a *Adapter) Name() string { return Name }

// EntryExecutable implements ports.EntryProber: name as an absolute path to an executable file, or the first of that
// name on PathEnv, where detection looks for the CLI.
func (a *Adapter) EntryExecutable(name string) string {
	return procutil.FindExecutable(name, a.PathEnv)
}

// ProbeEntry implements ports.EntryProber: the binary at path read as Detect reads a path set by hand, every probe run
// with assignments added to Env, as the installation of the CODEX_HOME they set, or of the home in use when they set
// none. The app servers it starts for the probes end with it.
func (a *Adapter) ProbeEntry(ctx context.Context, path string, assignments []string) ports.HarnessInfo {
	home := a.codexHome()
	if h, ok := ports.AssignmentValue(assignments, "CODEX_HOME"); ok && h != "" {
		home = filepath.Clean(h)
	}
	entry := &Adapter{Home: a.Home, PathEnv: a.PathEnv, Env: ports.WithAssignments(a.Env, assignments),
		KnownLocations: []string{}, ClientVersion: a.ClientVersion, ProbeTimeout: a.ProbeTimeout,
		listTimeout: a.listTimeout, helpTimeout: a.helpTimeout, entryHome: home}
	defer entry.Close()
	info := entry.Detect(ctx, path)
	info.Homes, info.Installations, info.EntryEnv = nil, []string{path}, slices.Clone(assignments)
	return info
}

// codexHome is the home of the installation in use: the one the daemon's CODEX_HOME names, or `.codex` under Home. An
// adapter ProbeEntry made for a custom entry has the entry's home alone.
func (a *Adapter) codexHome() string {
	if a.entryHome != "" {
		return a.entryHome
	}
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(a.Home, ".codex")
}

// homes are the Codex homes detection lists, each once: `.codex` under Home, then the one the daemon's CODEX_HOME names
// when it is another. `.codex` is left out only while it is no folder and CODEX_HOME names another home, so a Mac whose
// one home is elsewhere lists no empty one.
func (a *Adapter) homes() []string {
	if a.entryHome != "" {
		return []string{a.entryHome}
	}
	def := filepath.Join(a.Home, ".codex")
	env := os.Getenv("CODEX_HOME")
	if env == "" || sameDir(env, def) {
		return []string{def}
	}
	if st, err := os.Stat(def); err == nil && st.IsDir() {
		return []string{def, filepath.Clean(env)}
	}
	return []string{filepath.Clean(env)}
}

// sameDir reports whether two paths name one folder, once cleaned and, where they exist, with their links resolved.
func sameDir(a, b string) bool {
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	return resolve(a) == resolve(b)
}

// installationID is what a task names the installation of a home by: the home's path, written from `~` when it is
// under Home, so a task file reads the same on another Mac with the same folders.
func (a *Adapter) installationID(home string) string {
	if a.Home != "" {
		if rel, err := filepath.Rel(a.Home, home); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.Clean(home)
}

// homeOf is the home an installation runs with: its own, or the home in use for one detection did not give.
func (a *Adapter) homeOf(info ports.HarnessInfo) string {
	if info.Home != "" {
		return info.Home
	}
	return a.codexHome()
}

// envWith is env, or the daemon's own environment when env is nil, with CODEX_HOME set to home, and to nothing else.
func envWith(env []string, home string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "CODEX_HOME=") {
			out = append(out, kv)
		}
	}
	return append(out, "CODEX_HOME="+home)
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
// version, the app server every attempt runs on, the models and efforts of its models cache, and its sign-in, and says
// how ready it is.
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
	// Every attempt runs on the app server, which also lists the user's skills, so a Codex without one cannot run. Codex
	// 0.163.0 marks the command experimental, though its methods are in its stable schema; the top-level help lists it
	// either way. Help read in full that does not list the command reads as a Codex without it, below the minimum and
	// not ready. Help the check could not read within its limit leaves it unknown instead: a slow check does not turn the
	// harness away, and the first call to the app server proves it.
	helpTimeout := a.helpTimeout
	if helpTimeout == 0 {
		helpTimeout = a.probeTimeout(15 * time.Second)
	}
	top, topRead := procutil.Help(ctx, helpTimeout, a.Env, info.Path, "--help")
	info.Features["app-server"] = strings.Contains(top, "app-server")
	if !topRead && !info.Features["app-server"] {
		info.UnknownFeatures = map[string]bool{"app-server": true}
		info.SetDiagnostic(domain.T("codex_app_server_help_timed_out",
			"the {{harness}} --help check timed out, so whether it has an app server is unknown; the first call to the app server proves it",
			"harness", Name))
	}
	helpTimedOut := info.UnknownFeatures["app-server"]
	info.VersionSupport = "supported"
	if !info.Features["app-server"] && !helpTimedOut {
		info.VersionSupport = "below_minimum"
		info.Readiness = "not_ready"
		info.SetDiagnostic(domain.T("codex_app_server_missing", "Codex {{version}} has no app server", "version", info.Version))
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
	info.EnforceableControls = []string{"sandbox_read_only", "sandbox_workspace_write", "network_access", "writable_roots"}

	// Each home is an installation of the binary, read by probes that run with it alone: the one in use is the
	// harness's own reading, and every one is listed in Homes.
	inUse := a.codexHome()
	var homes []ports.HarnessInfo
	used := 0
	for i, home := range a.homes() {
		if sameDir(home, inUse) {
			used = i
		}
		homes = append(homes, a.probeHome(ctx, info, home, helpTimedOut))
	}
	info = homes[used]
	info.Homes = homes
	return info
}

// probeHome is what the binary info describes reads as the installation of one Codex home: its models and efforts, its
// sign-in, its managed requirements, and its account, each probe run with home as CODEX_HOME.
func (a *Adapter) probeHome(ctx context.Context, info ports.HarnessInfo, home string, helpTimedOut bool) ports.HarnessInfo {
	info.Home, info.InstallationID = home, a.installationID(home)
	info.Models, info.Efforts, info.CapabilitySource = a.models(home)

	// Signed out only when the CLI says so, which it does exiting 1; a probe killed at its timeout, or failing
	// otherwise, leaves the sign-in unknown.
	status, statusErr, err := procutil.Output(ctx, a.probeTimeout(20*time.Second), envWith(a.Env, home), info.Path,
		"login", "status")
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
		// The CLI cannot tell; let the first execution prove it rather than blocking on an unknown. The unknown sign-in
		// shows in the authentication field, so a help check that timed out keeps the diagnostic.
		info.Readiness = "ready"
		if !helpTimedOut {
			info.SetDiagnostic(domain.T("authentication_unverified", "authentication could not be verified"))
		}
	}
	if info.Ready() {
		// Requirements that cannot be read leave the harness as it would be with none. An unknown sign-in still shows in
		// the authentication field, so a disallowed setting takes the diagnostic, and it does over a help check that
		// timed out too: a setting Codex will not run with outlasts a slow check, which the next one may read.
		if read, err := a.readRequirements(ctx, info); err == nil {
			if t, found := read.disallowed(); found {
				info.SetDiagnostic(t)
			}
			info.ManagedRequirements = read.managed()
		}
		// The account is shown, never checked: a plan that cannot be read stops no run, and leaves the diagnostic and the
		// readiness as they are.
		info.Account = a.account(ctx, info)
	}
	return info
}

// account is how the account Codex runs under signs in, and its plan, as account/read answers; a read that fails
// leaves both unknown, with its error as the plan's reason.
func (a *Adapter) account(ctx context.Context, info ports.HarnessInfo) *ports.HarnessAccount {
	acct := &ports.HarnessAccount{SignInMethod: "unknown"}
	read, err := a.readAccount(ctx, info)
	switch {
	case err != nil:
		acct.SetPlanUnknown(domain.TextOf(err))
	case read.Account == nil:
		acct.SetPlanUnknown(domain.T("codex_account_unnamed", "Codex's app server names no account"))
	default:
		switch read.Account.Type {
		case "chatgpt":
			acct.SignInMethod = "chatgpt"
		case "apiKey":
			acct.SignInMethod = "api_key"
		}
		switch read.Account.PlanType {
		case "":
			// A sign-in with an API key has no plan.
			if acct.SignInMethod != "api_key" {
				acct.SetPlanUnknown(domain.T("codex_plan_unknown", "Codex reports the account's plan as unknown"))
			}
		case "unknown":
			acct.SetPlanUnknown(domain.T("codex_plan_unknown", "Codex reports the account's plan as unknown"))
		default:
			acct.Plan = read.Account.PlanType
		}
	}
	return acct
}

// managed is what the requirements allow: none managed when Codex reports none.
func (r configRequirements) managed() *ports.ManagedRequirements {
	if r.Requirements == nil {
		return &ports.ManagedRequirements{}
	}
	return &ports.ManagedRequirements{
		Managed:            true,
		ApprovalPolicies:   r.Requirements.AllowedApprovalPolicies,
		ApprovalsReviewers: r.Requirements.AllowedApprovalsReviewers,
		PermissionProfiles: r.Requirements.AllowedPermissionProfiles,
		SandboxModes:       r.Requirements.AllowedSandboxModes,
	}
}

// The settings Prepare runs Codex with: an approval policy under which no command waits for an approval, and the
// sandbox modes it maps the permission profiles to.
const (
	approvalPolicy        = "never"
	sandboxReadOnly       = "read-only"
	sandboxWorkspaceWrite = "workspace-write"
)

var sandboxModes = []string{sandboxReadOnly, sandboxWorkspaceWrite}

// disallowed says which of the settings HarmFlow runs Codex with the requirements do not allow, and what they allow
// instead; found is false when they allow every one.
func (r configRequirements) disallowed() (t domain.Text, found bool) {
	if r.Requirements == nil {
		return domain.Text{}, false
	}
	var settings, allowed []string
	check := func(name string, list []string, wanted ...string) {
		if list == nil {
			return
		}
		var missing []string
		for _, w := range wanted {
			if !slices.Contains(list, w) {
				missing = append(missing, w)
			}
		}
		if len(missing) > 0 {
			settings = append(settings, name+" = "+strings.Join(missing, ", "))
			allowed = append(allowed, name+" = "+strings.Join(list, ", "))
		}
	}
	check("approval_policy", r.Requirements.AllowedApprovalPolicies, approvalPolicy)
	check("sandbox_mode", r.Requirements.AllowedSandboxModes, sandboxModes...)
	if len(settings) == 0 {
		return domain.Text{}, false
	}
	return domain.T("codex_requirements_disallow",
		"Codex's managed requirements do not allow {{settings}}, which HarmFlow runs it with, so Codex falls back to what they allow: {{allowed}}",
		"settings", strings.Join(settings, "; "), "allowed", strings.Join(allowed, "; ")), true
}

// probeTimeout is the limit of a probe whose own limit is def: ProbeTimeout, when it is set.
func (a *Adapter) probeTimeout(def time.Duration) time.Duration {
	if a.ProbeTimeout == 0 {
		return def
	}
	return a.ProbeTimeout
}

// identifies reports whether the output of --version names Codex, as "codex-cli 0.163.0" does.
func identifies(out string) bool { return strings.Contains(strings.ToLower(out), "codex") }

// Identify checks that path is an executable file whose --version output names Codex.
func (a *Adapter) Identify(ctx context.Context, path string) error {
	_, err := procutil.IdentifyHarness(ctx, a.probeTimeout(procutil.VersionTimeout), a.Env, path, Name, "codex", identifies)
	return err
}

// firstIdentified returns the first candidate that names Codex, with its --version output, or the first candidate and
// why it failed when none does.
func (a *Adapter) firstIdentified(ctx context.Context, candidates []string) (string, string, error) {
	var first error
	for _, c := range candidates {
		out, err := procutil.IdentifyHarness(ctx, a.probeTimeout(procutil.VersionTimeout), a.Env, c, Name, "codex", identifies)
		if err == nil {
			return out, c, nil
		}
		if first == nil {
			first = err
		}
	}
	return "", candidates[0], first
}

// models reads the model list the CLI itself caches in home, with the efforts each accepts.
func (a *Adapter) models(home string) ([]ports.ModelInfo, []string, string) {
	defaults := []string{domain.DefaultEffort}
	models := []ports.ModelInfo{{ID: domain.DefaultModel, Efforts: defaults}}
	data, err := os.ReadFile(filepath.Join(home, "models_cache.json"))
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

func (a *Adapter) userDir(info ports.HarnessInfo) catalog.Dir {
	return catalog.Dir{Root: a.homeOf(info)}
}

// UserItem implements ports.HarnessAdapter: a skill is the first of that name in the installed Codex's skill list, an
// agent is looked up in the installation's Codex home. The list is the installation's, so the workspace and the added
// folders change nothing.
func (a *Adapter) UserItem(ctx context.Context, info ports.HarnessInfo, kind, name, _ string, _ []string) (ports.ResolvedItem, bool, []ports.DiscoveryError, error) {
	if kind != "skill" {
		it, ok := a.userDir(info).Item(kind, name)
		return it, ok, nil, nil
	}
	items, problems, err := a.skills(ctx, info)
	if err != nil {
		return ports.ResolvedItem{}, false, nil, err
	}
	for _, it := range items {
		if it.Name == name {
			return it, true, problems, nil
		}
	}
	return ports.ResolvedItem{}, false, problems, nil
}

// UserItems implements ports.HarnessAdapter: the skills of the installed Codex's skill list, or the agents of the
// installation's Codex home; the workspace and the added folders change nothing.
func (a *Adapter) UserItems(ctx context.Context, info ports.HarnessInfo, kind, _ string, _ []string) ([]ports.ResolvedItem, []ports.DiscoveryError, error) {
	if kind != "skill" {
		return a.userDir(info).Items(kind), nil, nil
	}
	return a.skills(ctx, info)
}

// KeepsSessions implements ports.SessionKeeper: run without --ephemeral, Codex keeps the session under its home's
// sessions folder.
func (a *Adapter) KeepsSessions() bool { return true }

// sessionsDir is the folder where Codex keeps its sessions: under the CODEX_HOME the attempt's environment sets, or
// home.
func sessionsDir(env []string, home string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "CODEX_HOME="); ok && v != "" {
			home = v
		}
	}
	return filepath.Join(home, "sessions")
}

// Prepare implements ports.HarnessAdapter: a `codex app-server` that the attempt's Conversation speaks to, starting a
// thread with the model asked for and the permission profile translated to a sandbox, then one turn with the effort
// asked for and the instructions, which name the built-ins as copied into the execution's directory. It reports the
// restrictions the sandbox enforces and those it cannot. The thread is ephemeral unless the request keeps it. The
// installation's home is the process's CODEX_HOME, in place of any the request's environment carries. An installation
// detection did not give runs with the home in use when the request's environment has no CODEX_HOME, as one
// ports.SessionEnv gives has none. A custom entry's assignments are added to the process's environment, and its
// command line's arguments come before app-server.
func (a *Adapter) Prepare(info ports.HarnessInfo, req ports.RunRequest) (ports.Invocation, ports.OutputParser, error) {
	if info.Detected && !info.Features["app-server"] && !info.UnknownFeatures["app-server"] && !req.PolicyCheck {
		return ports.Invocation{}, nil, domain.TextErr("codex_app_server_missing", "Codex {{version}} has no app server",
			"version", info.Version)
	}
	home := a.homeOf(info)
	if len(info.EntryEnv) > 0 {
		req.Env = ports.WithAssignments(req.Env, info.EntryEnv)
	}
	switch {
	case info.Home != "":
		req.Env = envWith(req.Env, info.Home)
	case req.Env != nil && !slices.ContainsFunc(req.Env, func(kv string) bool { return strings.HasPrefix(kv, "CODEX_HOME=") }):
		req.Env = envWith(req.Env, home)
	}
	var restrictions []ports.Restriction
	// The thread's settings, which a configuration of the user's cannot widen: they are the thread's own parameters, and
	// its config overrides the keys a user's config.toml would set.
	thread := map[string]any{"cwd": req.WorkDir, "approvalPolicy": approvalPolicy, "ephemeral": !req.KeepSession}
	config := map[string]any{}
	turn := map[string]any{}
	if req.Model != "" && req.Model != domain.DefaultModel {
		thread["model"] = req.Model
	}
	if req.Effort != "" && req.Effort != domain.DefaultEffort {
		turn["effort"] = req.Effort
	}

	perm := req.Permissions
	network := perm.Network != domain.NetworkDeny
	dirs := append([]string{}, req.AdditionalDirs...)
	switch perm.Profile {
	case domain.ProfileReadOnly:
		thread["sandbox"] = sandboxReadOnly
		restrictions = append(restrictions, readOnlyRestriction(info.ManagedRequirements),
			ports.Restriction{Name: "network", Enforced: true,
				Text: domain.Text{Message: "Codex's read-only sandbox blocks network access"}})
	default:
		thread["sandbox"] = sandboxWorkspaceWrite
		config["sandbox_workspace_write.network_access"] = network
		restrictions = append(restrictions, ports.Restriction{Name: "restrictToWorkspace", Enforced: true,
			Text: domain.Text{
				Message: "Codex's workspace-write sandbox confines writes to the workspace and the added directories"}})
		if !network {
			restrictions = append(restrictions, ports.Restriction{Name: "network", Enforced: true,
				Text: domain.Text{Message: "Codex's sandbox blocks network access"}})
		}
		// The sandbox keeps the .git folder of a writable root read-only, so a session could neither fetch nor add a
		// worktree in a repository's local path (harmflow-app#1128). Under workspace_write, the .git folder of each
		// repository's local path is a writable root of its own; a custom profile's writable paths stay as they are.
		if perm.Profile == domain.ProfileWorkspaceWrite {
			for _, d := range req.AdditionalDirs {
				if d != "" {
					dirs = append(dirs, filepath.Join(d, ".git"))
				}
			}
		}
		if perm.Profile == domain.ProfileCustom {
			dirs = append(dirs, perm.Custom.WritablePaths...)
			if len(perm.Custom.AllowedTools) > 0 || len(perm.Custom.DeniedTools) > 0 {
				restrictions = append(restrictions, ports.Restriction{Name: "tool_allowlist", Enforced: false,
					Text: domain.T("restriction_codex_tool_lists",
						"Codex has no per-tool allow or deny list; the custom tool lists are not enforced")})
			}
			// HarmFlow applies no control of Codex's own to the programs its shell starts, so the lists are accepted and
			// reported, never passed on.
			if perm.Custom.HasProgramLists() {
				restrictions = append(restrictions, ports.Restriction{Name: "program_lists", Enforced: false,
					Text: domain.T("restriction_codex_program_lists",
						"Codex's sandbox does not limit the programs its shell starts; the custom program lists are not enforced")})
			}
			// The read paths are never writable roots, where the sandbox would let the session write.
			if len(perm.Custom.ReadablePaths) > 0 {
				restrictions = append(restrictions, ports.Restriction{Name: "readablePaths", Enforced: false,
					Text: domain.T("restriction_codex_read_paths",
						"Codex's sandbox lets the session read any file; the custom read paths are not enforced")})
			}
		}
	}
	if req.ExecutionDir != "" {
		dirs = append(dirs, req.ExecutionDir)
	}
	seen := map[string]bool{req.WorkDir: true}
	var roots []string
	for _, d := range dirs {
		if d != "" && !seen[d] {
			seen[d] = true
			roots = append(roots, d)
		}
	}
	// The read-only sandbox writes nowhere, so its writable roots stay empty, as `--add-dir` left them.
	if len(roots) > 0 && perm.Profile != domain.ProfileReadOnly {
		config["sandbox_workspace_write.writable_roots"] = roots
	}
	if len(config) > 0 {
		thread["config"] = config
	}

	req, err := deliverBuiltins(req)
	if err != nil {
		return ports.Invocation{}, nil, err
	}
	turn["input"] = []map[string]any{{"type": "text", "text": instructions(req)}}
	args := append(slices.Clone(info.EntryArgs), "app-server", "--listen", "stdio://")
	inv := ports.Invocation{Path: info.Path, Args: args, Env: req.Env, Dir: req.WorkDir,
		Interactive: true, Restrictions: restrictions}
	p := &parser{model: req.Model, modelSource: domain.ModelFromTask, thread: thread, turn: turn,
		clientVersion: a.ClientVersion}
	if req.KeepSession {
		p.sessionDir = sessionsDir(req.Env, home)
	}
	if req.Model == "" || req.Model == domain.DefaultModel {
		// A task left at the default runs the model its configuration sets, which the thread's start then names.
		p.model, p.modelSource = configModel(home), domain.ModelFromConfig
		if p.model == "" {
			p.modelSource = ""
		}
	}
	return inv, p, nil
}

// readOnlyRestriction is the read_only profile's restriction: enforced only when Codex's managed requirements, as
// read, allow the read-only sandbox and approvals off. Under requirements that allowed only on-request approvals, a
// real read_only run had Codex's change to a file of its workspace approved, and only the one outside it declined.
// Codex applies its requirements whether or not they could be read, so unread ones leave the profile not enforced.
func readOnlyRestriction(m *ports.ManagedRequirements) ports.Restriction {
	leftOut := func(list []string, v string) bool { return list != nil && !slices.Contains(list, v) }
	if m == nil || leftOut(m.ApprovalPolicies, approvalPolicy) || leftOut(m.SandboxModes, sandboxReadOnly) {
		return ports.Restriction{Name: "read_only", Enforced: false,
			Text: domain.T("restriction_codex_read_only",
				"Codex's managed requirements do not allow its read-only sandbox with approvals off, or could not be read, so Codex may approve a change to a file of the workspace; the session may change files in its workspace")}
	}
	return ports.Restriction{Name: "read_only", Enforced: true,
		Text: domain.Text{Message: "Codex's read-only sandbox refuses every write, with approvals off"}}
}

var configModelRe = regexp.MustCompile(`^\s*model\s*=\s*(?:"([^"]*)"|'([^']*)')`)

// configModel is the top-level `model` of home's config.toml, the model Codex runs when none is asked for; the keys of
// a table, such as a profile's, are not read.
func configModel(home string) string {
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
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

// signedOutTexts are the CLI's own words that it is signed out, which its browser sign-in mends. The other texts of
// Unavailable are not: an API key that failed is not one a browser sign-in mends, and HarmFlow signs in with none.
const signedOutTexts = `\bnot logged in\b|please log in|codex login|refresh token|token (has )?expired`

var signedOut = regexp.MustCompile(`(?i)(` + signedOutTexts + `)`)

// patterns read the CLI's error as a class. A workspace out of credits, a spend cap reached, and "Quota exceeded", as
// Codex 0.163.0 and 0.171.0 end a turn on them, are an exhausted quota as much as a usage limit is. Each is read in
// Codex's own sentence, so the system's "Disk quota exceeded" in a write's error stays a task error.
var patterns = classify.Patterns{
	Quota:       regexp.MustCompile(`(?i)(usage limit|insufficient_quota|exceeded your current quota|purchase more credits|plan limit|workspace is out of credits|hit your spend cap|\bquota exceeded\. check your plan)`),
	RateLimit:   regexp.MustCompile(`(?i)(rate[ _-]?limit|too many requests|status 429)`),
	Unavailable: regexp.MustCompile(`(?i)(` + signedOutTexts + `|authentication failed|invalid api key|401 unauthorized)`),
	Network:     regexp.MustCompile(`(?i)(stream disconnected|error sending request|connection (refused|reset)|dns error|timed out connecting)`),
}

// networkWaitRe is the notice of a Codex that cannot reach its provider and waits for the network, as Codex 0.163.0
// prints it: "Reconnecting... waiting for network (Connection failed: error sending request)". Its numbered retries,
// "Reconnecting... 2/5", are not a wait: Codex exits once they run out.
var networkWaitRe = regexp.MustCompile(`(?i)^reconnecting\b.*\bwaiting for network\b`)

// overrideRe is Codex's notice that its managed requirements override the approval policy or the sandbox HarmFlow set,
// as Codex 0.163.0 prints it: "Configured value for `approval_policy` is disallowed by requirements; falling back to
// required value OnRequest. Details: …". Codex names the sandbox `sandbox_mode`, or `permission_profile` where it
// derives one from the sandbox.
var overrideRe = regexp.MustCompile("^Configured value for `(approval_policy|sandbox_mode|permission_profile)` is disallowed by requirements")

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
	p.mu.Lock()
	defer p.mu.Unlock()
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
	case strings.TrimSpace(p.failure) == "" && p.ending.Key != "":
		// The turn ended without completing and Codex gave no error of its own: the daemon's text says how it ended.
		end.Error = p.ending
		end.Class = patterns.Text(p.ending.Message)
		return end
	}
	// The app server logs to stderr what its turn does not depend on, such as an MCP server that would not start: the
	// turn's own error, when it gave one, is the cause, and the log's tail only stands in for none.
	text := strings.TrimSpace(p.failure)
	if text == "" {
		text = lastLines(stderrTail, 20)
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
	end.SignedOut = end.Class == domain.ClassHarnessUnavailable && signedOut.MatchString(text)
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
