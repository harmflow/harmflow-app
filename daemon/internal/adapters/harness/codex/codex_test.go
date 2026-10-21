package codex

import (
	"bufio"
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
	// A configuration notice reported as an error item does not fail the turn.
	if end := p.Finish(0, "Reading additional input from stdin...", false, false); end.Class != domain.ClassNone {
		t.Errorf("end: %+v", end)
	}
}

func TestParseQuota(t *testing.T) {
	p, _ := replay(t, "quota.jsonl")
	if end := p.Finish(1, "", false, false); end.Class != domain.ClassQuotaExhausted {
		t.Errorf("got %s: %s", end.Class, end.Message)
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
