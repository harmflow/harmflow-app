package codex

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

func replay(t *testing.T, file string) (*parser, []ports.HarnessEvent) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := &parser{}
	var events []ports.HarnessEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		events = append(events, p.Line(sc.Bytes())...)
	}
	return p, events
}

// standIn writes a stand-in for the codex CLI whose --version and `login status` run the given shell commands, and
// whose `exec --help` lists every flag the adapter needs.
func standIn(t *testing.T, version, login string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/bash
case "$1" in
  --version) ` + version + ` ;;
  login) ` + login + ` ;;
  exec) echo "Usage: codex exec  --json  --sandbox <MODE>  --cd <DIR>  --skip-git-repo-check" ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// FS-004 FR-006, FR-008 (harmflow-app#71): what the probes could not establish reads unknown, never a known state. A
// sign-in is invalid only when the CLI says it is signed out; a probe that is killed, as one is at its timeout, or that
// fails otherwise leaves the harness ready with a diagnostic, and the first execution proves the sign-in.
func TestDetectReadsOnlyWhatTheProbesEstablish(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	version, signedIn := `echo "codex-cli 0.163.0"`, `echo "Logged in using ChatGPT" >&2`
	cases := []struct {
		name, version, login                        string
		wantVersion, wantSupport, wantAuth, wantKey string
		wantReadiness                               string
	}{
		{"signed in", version, signedIn, "0.163.0", "supported", "valid", "", "ready"},
		{"no version number", `echo "codex-cli (dev build)"`, signedIn, "", "unknown", "valid", "", "ready"},
		// The shell ends itself as the probe's timeout would end it, with no 20-second wait.
		{"sign-in probe killed", version, `kill -KILL $$`, "0.163.0", "supported", "unknown",
			"authentication_unverified", "ready"},
		{"sign-in probe fails", version, `echo "Error checking login status: permission denied" >&2; exit 1`, "0.163.0",
			"supported", "unknown", "authentication_unverified", "ready"},
		// What the CLI prints when it is signed out, exiting 1.
		{"signed out", version, `echo "Not logged in" >&2; exit 1`, "0.163.0", "supported", "invalid", "not_signed_in",
			"not_ready"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info := (&Adapter{Home: t.TempDir()}).Detect(context.Background(), standIn(t, c.version, c.login))
			if info.Version != c.wantVersion || info.VersionSupport != c.wantSupport || info.Authentication != c.wantAuth ||
				info.DiagnosticKey != c.wantKey || info.Readiness != c.wantReadiness {
				t.Errorf("version %q %s, authentication %s, diagnostic %q (%s), readiness %s; want version %q %s, "+
					"authentication %s, diagnostic %q, readiness %s", info.Version, info.VersionSupport, info.Authentication,
					info.DiagnosticKey, info.Diagnostic, info.Readiness, c.wantVersion, c.wantSupport, c.wantAuth, c.wantKey,
					c.wantReadiness)
			}
		})
	}
}

func TestParseSuccess(t *testing.T) {
	p, events := replay(t, "success.jsonl")
	var started, done, changed, msgs, warnings int
	for _, e := range events {
		switch e.Kind {
		case ports.HarnessToolStarted:
			started++
			if e.Command != "echo hello" {
				t.Errorf("the shell wrapper was not removed: %q", e.Command)
			}
		case ports.HarnessToolDone:
			done++
		case ports.HarnessFileChanged:
			changed++
		case ports.HarnessMessage:
			msgs++
		case ports.HarnessWarning:
			warnings++
		}
	}
	if started != 1 || done != 1 || changed != 1 || msgs != 1 || warnings != 1 {
		t.Errorf("events: started %d done %d changed %d messages %d warnings %d", started, done, changed, msgs, warnings)
	}
	// thread.started names the thread only: the attempt's model and effort read not reported (D-105).
	if e := events[0]; e.Kind != ports.HarnessStarted || e.SessionID == "" || e.Model != "" || e.Effort != "" {
		t.Errorf("start: %+v", e)
	}
	// A configuration notice reported as an error item does not fail the turn.
	if end := p.Finish(0, "Reading additional input from stdin...", false, false); end.Class != domain.ClassNone {
		t.Errorf("end: %+v", end)
	}
}

// FS-009 FR-005: the CLI's error is redacted before the 2000-character tail cuts it; cut first, a secret would keep
// part of itself, too little to match its pattern.
func TestFinishRedactsBeforeTheTail(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	end := (&parser{}).Finish(2, "Error: "+token+" "+strings.Repeat("y", 1969), false, false)
	if strings.Contains(end.Error.Message, strings.Repeat("a", 20)) || !strings.Contains(end.Error.Message, "[REDACTED]") {
		t.Errorf("the error keeps part of the token: %q", end.Error.Message)
	}
}

func TestParseQuota(t *testing.T) {
	p, _ := replay(t, "quota.jsonl")
	if end := p.Finish(1, "", false, false); end.Class != domain.ClassQuotaExhausted {
		t.Errorf("got %s: %s", end.Class, end.Error.Message)
	}
}

// A 429 is a rate limit only as an HTTP status; elsewhere it is part of the task's own failure.
func TestParseRateLimit(t *testing.T) {
	cases := []struct {
		text string
		want domain.ErrorClass
	}{
		{"thread 'main' panicked at src/client.rs:429:13", domain.ClassTaskError},
		{"could not push: the pull request for issue #429 already exists", domain.ClassTaskError},
		{"tests failed: 429 passed, 3 failed", domain.ClassTaskError},
		{"unexpected status 429", domain.ClassRateLimited},
		{"429 Too Many Requests", domain.ClassRateLimited},
	}
	for _, c := range cases {
		p := &parser{}
		if end := p.Finish(1, c.text, false, false); end.Class != c.want {
			t.Errorf("%q: got %s, want %s", c.text, end.Class, c.want)
		}
	}
}

// D-068: Codex's sandbox enforces the workspace and the network; per-tool lists it cannot enforce are named.
func TestPrepareSandbox(t *testing.T) {
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/codex", Features: map[string]bool{"--add-dir": true, "--ephemeral": true}}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	perm.Network = domain.NetworkDeny
	inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, ExecutionDir: "/x",
		AdditionalDirs: []string{"/repo"}, Prompt: "do it", Model: "gpt-x", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(inv.Args, " ")
	for _, want := range []string{"exec --json", "--sandbox workspace-write", "sandbox_workspace_write.network_access=false",
		"--add-dir /repo", "--add-dir /x", "--model gpt-x", `model_reasoning_effort="high"`, "--cd /w"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	for _, r := range inv.Restrictions {
		if !r.Enforced {
			t.Errorf("%s should be enforced by the sandbox", r.Name)
		}
	}
	if !strings.HasSuffix(inv.Stdin, "do it\n") || inv.Args[len(inv.Args)-1] != "-" {
		t.Error("the prompt goes through stdin")
	}
	perm.Profile = domain.ProfileCustom
	perm.Custom = &domain.CustomPermissions{AllowedTools: []string{"Read"}}
	inv, _, _ = a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm})
	found := false
	for _, r := range inv.Restrictions {
		if r.Name == "tool_allowlist" && !r.Enforced {
			found = true
		}
	}
	if !found {
		t.Error("a custom tool list Codex cannot enforce must be warned")
	}
}

// FS-004 FR-004, AC-005: a task that sets no model or effort, or sets default, passes Codex no model and no reasoning
// effort, so those of Codex's own configuration apply. TestPrepareSandbox shows a set model and effort reaching it.
func TestPrepareLeavesTheDefaultsToCodex(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/codex", Features: map[string]bool{"--model": true}}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	for _, c := range []struct{ name, model, effort string }{
		{"default", domain.DefaultModel, domain.DefaultEffort},
		{"unset", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, Prompt: "do it",
				Model: c.model, Effort: c.effort})
			if err != nil {
				t.Fatal(err)
			}
			for _, arg := range inv.Args {
				if arg == "--model" || arg == "-m" || strings.HasPrefix(arg, "model=") ||
					strings.HasPrefix(arg, "model_reasoning_effort=") {
					t.Errorf("%q was passed: %q", arg, inv.Args)
				}
			}
		})
	}
}

// D-092: built-ins are read from copies in the execution's directory, never from the app; the user's own items stay put.
func TestPrepareDeliversBuiltins(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "skills", "harmflow-x")
	_ = os.MkdirAll(filepath.Join(skill, "scripts"), 0o700)
	_ = os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: harmflow-x\ndescription: d\n---\nbody"), 0o600)
	_ = os.WriteFile(filepath.Join(skill, "scripts", "run.sh"), []byte("echo hi"), 0o700)
	main := filepath.Join(root, "agents", "harmflow-a.md")
	other := filepath.Join(root, "agents", "harmflow-b.md")
	_ = os.MkdirAll(filepath.Dir(main), 0o700)
	_ = os.WriteFile(main, []byte("---\nname: harmflow-a\ndescription: an agent\n---\nYou implement."), 0o600)
	_ = os.WriteFile(other, []byte("---\nname: harmflow-b\ndescription: another\n---\nYou review."), 0o600)
	userSkill := filepath.Join(t.TempDir(), "skills", "mine")
	exec := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/codex", Features: map[string]bool{"--add-dir": true}}
	inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", ExecutionDir: exec, Prompt: "do it",
		Permissions: *domain.TaskDocument{}.Effective().Spec.Permissions,
		Skills: []ports.ResolvedItem{{Name: "harmflow-x", Kind: "skill", Builtin: true, Path: skill},
			{Name: "mine", Kind: "skill", Path: userSkill}},
		Agents: []ports.ResolvedItem{{Name: "harmflow-a", Kind: "agent", Builtin: true, Path: main},
			{Name: "harmflow-b", Kind: "agent", Builtin: true, Path: other}}})
	if err != nil {
		t.Fatal(err)
	}
	delivered := filepath.Join(exec, "codex-builtins")
	for _, f := range []string{"skills/harmflow-x/SKILL.md", "skills/harmflow-x/scripts/run.sh", "agents/harmflow-b.md"} {
		if _, err := os.Stat(filepath.Join(delivered, f)); err != nil {
			t.Errorf("%s not delivered: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(delivered, "agents", "harmflow-a.md")); err == nil {
		t.Error("the main agent is inlined in the instructions; it needs no copy")
	}
	for _, want := range []string{"You implement.", filepath.Join(delivered, "skills", "harmflow-x", "SKILL.md"),
		filepath.Join(delivered, "agents", "harmflow-b.md"), filepath.Join(userSkill, "SKILL.md")} {
		if !strings.Contains(inv.Stdin, want) {
			t.Errorf("the instructions lack %q:\n%s", want, inv.Stdin)
		}
	}
	if strings.Contains(inv.Stdin, root) {
		t.Errorf("the instructions name a built-in at its source:\n%s", inv.Stdin)
	}
	if entries, _ := os.ReadDir(a.Home); len(entries) > 0 {
		t.Error("the user's home was touched")
	}
}

// The Costs tab (harmflow-app#39), on a real Codex 0.163.0 run: one turn.completed carries the attempt's usage, its
// input_tokens include the cached ones, and the cost is estimated from the table when the attempt ends.
func TestUsageFromTurnCompleted(t *testing.T) {
	defer func(old domain.PriceTable) { Prices = old }(Prices)
	price := domain.Price{Input: 1.25, CacheRead: 0.125, Output: 10}
	Prices = domain.PriceTable{AsOf: "2026-01-02", Prices: map[string]domain.Price{"gpt-test": price}}

	p, events := replay(t, "usage.jsonl")
	p.model, p.modelSource = "gpt-test", domain.ModelFromConfig
	var live []*domain.Usage
	for _, e := range events {
		if e.Kind == ports.HarnessUsage {
			live = append(live, e.Usage)
		}
	}
	want := domain.Tokens{Input: 160035 - 135680, CacheRead: 135680, Output: 1194}
	if len(live) != 1 || live[0].Models[0].Tokens != want || live[0].CostUSD != nil {
		t.Fatalf("live usage: %+v", live)
	}
	u := p.Finish(0, "", false, false).Usage
	if u.Status != domain.UsageReported || u.ModelSource != domain.ModelFromConfig || u.Models[0].Model != "gpt-test" {
		t.Fatalf("usage: %+v", u)
	}
	if u.CostSource != domain.CostEstimated || u.CostUSD == nil || *u.CostUSD != price.Cost(want) || u.PricesAsOf != "2026-01-02" {
		t.Errorf("cost: %+v", u)
	}

	// No model named anywhere: the tokens, without a cost.
	p.model, p.modelSource = "", ""
	if u := p.Finish(0, "", false, false).Usage; u.CostSource != domain.CostUnpriced || u.CostUSD != nil || u.Models[0].Tokens != want {
		t.Errorf("unpriced: %+v", u)
	}
	// Cache writes, which GPT-5.6 and later bill, are counted out of the input that includes them.
	w := &parser{model: "gpt-test", modelSource: domain.ModelFromTask}
	w.Line([]byte(`{"type":"turn.completed","usage":{"input_tokens":5000,"cached_input_tokens":3000,"cache_write_input_tokens":1500,"output_tokens":20}}`))
	if got := w.Finish(0, "", false, false).Usage.Models[0].Tokens; got != (domain.Tokens{Input: 500, CacheWriteShort: 1500, CacheRead: 3000, Output: 20}) {
		t.Errorf("cache writes: %+v", got)
	}
	// A turn that failed reported no usage.
	q, _ := replay(t, "quota.jsonl")
	if u := q.Finish(1, "", false, false).Usage; u.Status != domain.UsageNotReported {
		t.Errorf("quota: %+v", u)
	}
}

// A task left at the default model is priced as the model Codex's configuration sets, and says so.
func TestPrepareNamesTheModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/codex", Features: map[string]bool{}}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	model := func(m string) (string, string) {
		t.Helper()
		_, out, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, Model: m})
		if err != nil {
			t.Fatal(err)
		}
		p := out.(*parser)
		return p.model, p.modelSource
	}
	if m, src := model("default"); m != "" || src != "" {
		t.Errorf("no configuration: %q from %q", m, src)
	}
	_ = os.WriteFile(filepath.Join(home, "config.toml"), []byte("# mine\nmodel = \"gpt-conf\" # chosen\n\n[profiles.fast]\nmodel = \"gpt-fast\"\n"), 0o600)
	if m, src := model("default"); m != "gpt-conf" || src != domain.ModelFromConfig {
		t.Errorf("configured: %q from %q", m, src)
	}
	if m, src := model("gpt-task"); m != "gpt-task" || src != domain.ModelFromTask {
		t.Errorf("the task's: %q from %q", m, src)
	}
	_ = os.WriteFile(filepath.Join(home, "config.toml"), []byte("[profiles.fast]\nmodel = 'gpt-fast'\n"), 0o600)
	if m, _ := model(""); m != "" {
		t.Errorf("a profile's model was read as the default: %q", m)
	}
}

func TestModelsFromCache(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".codex"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"models":[
		{"slug":"m-1","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},
		{"slug":"m-2","supported_reasoning_levels":[{"effort":"low"}]}]}`), 0o600)
	t.Setenv("CODEX_HOME", "")
	a := &Adapter{Home: home}
	models, efforts, src := a.models()
	if src != "reported" || len(models) != 3 || strings.Join(efforts, ",") != "default,low,high" {
		t.Fatalf("models %v efforts %v source %s", models, efforts, src)
	}
	info := ports.HarnessInfo{Detected: true, CapabilitySource: src, Models: models, Efforts: efforts}
	if err := a.ValidateProfile(info, "m-2", "high"); err == nil {
		t.Error("m-2 does not accept high")
	}
	if err := a.ValidateProfile(info, "m-1", "high"); err != nil {
		t.Error(err)
	}
}

// FS-004 FR-005: with no readable models cache, Codex's capabilities are only declared, so a model or effort other
// than default cannot be checked and says so; with the cache, it is checked as before and nothing warns.
func TestProfileWarningWithoutTheModelsCache(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Detected: true, Version: "0.163.0"}
	info.Models, info.Efforts, info.CapabilitySource = a.models()
	if info.CapabilitySource != "declared" {
		t.Fatalf("source %q without the cache", info.CapabilitySource)
	}
	for _, p := range [][2]string{{"no-such-model", "ultra"}, {"no-such-model", "default"}, {"default", "ultra"}} {
		if err := a.ValidateProfile(info, p[0], p[1]); err != nil {
			t.Errorf("%v refused unchecked: %v", p, err)
		}
		if w, ok := a.ProfileWarning(info, p[0], p[1]); !ok || w.Key != "codex_profile_unchecked" ||
			!strings.Contains(w.Message, "Codex") {
			t.Errorf("%v saved with no warning: %+v", p, w)
		}
	}
	for _, p := range [][2]string{{"default", "default"}, {"", ""}} {
		if w, ok := a.ProfileWarning(info, p[0], p[1]); ok {
			t.Errorf("the default profile %v warns: %+v", p, w)
		}
	}
	if w, ok := a.ProfileWarning(ports.HarnessInfo{}, "no-such-model", "ultra"); ok {
		t.Errorf("a Codex not detected warns here too: %+v", w)
	}

	_ = os.WriteFile(filepath.Join(codexHome, "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-test","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`), 0o600)
	info.Models, info.Efforts, info.CapabilitySource = a.models()
	if w, ok := a.ProfileWarning(info, "no-such-model", "ultra"); ok {
		t.Errorf("a checked profile warns: %+v", w)
	}
	if err := a.ValidateProfile(info, "no-such-model", "default"); err == nil ||
		domain.TextOf(err).Key != "codex_model_unknown" {
		t.Errorf("unknown model: %v", err)
	}
	if err := a.ValidateProfile(info, "gpt-test", "ultra"); err == nil || domain.TextOf(err).Key != "codex_effort_invalid" {
		t.Errorf("invalid effort: %v", err)
	}
}
