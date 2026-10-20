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
