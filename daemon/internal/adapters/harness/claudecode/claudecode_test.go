package claudecode

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
	p := &parser{tools: map[string]toolUse{}}
	var events []ports.HarnessEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		events = append(events, p.Line(sc.Bytes())...)
	}
	return p, events
}

func TestParseSuccess(t *testing.T) {
	p, events := replay(t, "success.jsonl")
	kinds := map[ports.HarnessEventKind]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	if kinds[ports.HarnessStarted] != 1 || kinds[ports.HarnessToolStarted] != 2 || kinds[ports.HarnessToolDone] != 2 ||
		kinds[ports.HarnessFileChanged] != 1 || kinds[ports.HarnessMessage] != 1 {
		t.Fatalf("unexpected events: %v", kinds)
	}
	if events[1].Command != "go test ./..." || events[1].Message != "Run the tests" {
		t.Errorf("tool start: %+v", events[1])
	}
	end := p.Finish(0, "", false, false)
	if end.Class != domain.ClassNone || end.FinalText != "Done: the draft PR is open." || end.SessionID == "" {
		t.Errorf("end: %+v", end)
	}
}

// FS-005 FR-004: a rejected plan window is a quota exhaustion, from the stream's own signal.
func TestParseQuota(t *testing.T) {
	p, _ := replay(t, "quota.jsonl")
	if end := p.Finish(1, "", false, false); end.Class != domain.ClassQuotaExhausted {
		t.Errorf("got %s (%s), want quota_exhausted", end.Class, end.Message)
	}
}

func TestParseAuthAndOthers(t *testing.T) {
	p, _ := replay(t, "auth.jsonl")
	if end := p.Finish(1, "", false, false); end.Class != domain.ClassHarnessUnavailable {
		t.Errorf("auth: got %s", end.Class)
	}
	q := &parser{tools: map[string]toolUse{}}
	if end := q.Finish(1, "Error: the tests in issue #429 failed", false, false); end.Class != domain.ClassTaskError {
		t.Errorf("an issue number was taken for a rate limit: %s", end.Class)
	}
	if end := q.Finish(1, "API Error: 529 overloaded_error", false, false); end.Class != domain.ClassRateLimited {
		t.Errorf("overloaded: got %s", end.Class)
	}
	managed := "Your organization requires remote managed settings to load, but they could not be loaded. Run `claude auth login` to re-authenticate"
	if end := q.Finish(1, managed, false, false); end.Class != domain.ClassHarnessUnavailable {
		t.Errorf("managed settings: got %s", end.Class)
	}
	if end := q.Finish(143, "", false, true); end.Class != domain.ClassCancelled {
		t.Errorf("cancel: got %s", end.Class)
	}
	if end := q.Finish(143, "", true, false); end.Class != domain.ClassTimeout {
		t.Errorf("timeout: got %s", end.Class)
	}
}

// D-068: each profile is translated to Claude Code's own flags, and what they cannot confine is named.
func TestPrepareTranslatesPermissions(t *testing.T) {
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{"--agent": true, "--no-session-persistence": true}}
	perm := domain.TaskDocument{}.Effective().Spec.Permissions
	exec := t.TempDir()

	ro := *perm
	ro.Profile = domain.ProfileReadOnly
	inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: ro, ExecutionDir: exec, Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(inv.Args, " ")
	if !strings.Contains(args, "--permission-mode dontAsk") || strings.Contains(args, "Bash") {
		t.Errorf("read_only args: %s", args)
	}
	for _, r := range inv.Restrictions {
		if !r.Enforced {
			t.Errorf("read_only should be fully enforced, %s is not", r.Name)
		}
	}
	if inv.Stdin != "hi" {
		t.Error("the prompt goes through stdin")
	}

	ww := *perm
	ww.Network = domain.NetworkDeny
	inv, _, _ = a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: ww, ExecutionDir: exec, AdditionalDirs: []string{"/repo"}})
	args = strings.Join(inv.Args, " ")
	if !strings.Contains(args, "--permission-mode acceptEdits") || !strings.Contains(args, "--add-dir /repo") ||
		!strings.Contains(args, "--disallowedTools WebFetch,WebSearch") {
		t.Errorf("workspace_write args: %s", args)
	}
	unenforced := map[string]bool{}
	for _, r := range inv.Restrictions {
		if !r.Enforced {
			unenforced[r.Name] = true
		}
	}
	if !unenforced["restrictToWorkspace"] || !unenforced["network"] {
		t.Errorf("the shell's freedom must be warned: %v", inv.Restrictions)
	}
}

// D-092: built-in skills are delivered as a plugin of the execution; built-in agents are defined for the session.
func TestPrepareDeliversBuiltins(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "skills", "harmflow-x")
	_ = os.MkdirAll(skill, 0o700)
	_ = os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: harmflow-x\ndescription: d\n---\nbody"), 0o600)
	agent := filepath.Join(root, "agents", "harmflow-a.md")
	_ = os.MkdirAll(filepath.Dir(agent), 0o700)
	_ = os.WriteFile(agent, []byte("---\nname: harmflow-a\ndescription: an agent\n---\nYou implement."), 0o600)
	exec := t.TempDir()
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{"--agent": true}}
	inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", ExecutionDir: exec,
		Permissions: *domain.TaskDocument{}.Effective().Spec.Permissions,
		Skills:      []ports.ResolvedItem{{Name: "harmflow-x", Kind: "skill", Builtin: true, Path: skill}},
		Agents:      []ports.ResolvedItem{{Name: "harmflow-a", Kind: "agent", Builtin: true, Path: agent}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(exec, "claude-plugin", "skills", "harmflow-x", "SKILL.md")); err != nil {
		t.Errorf("skill not delivered: %v", err)
	}
	args := strings.Join(inv.Args, " ")
	if !strings.Contains(args, "--plugin-dir "+filepath.Join(exec, "claude-plugin")) || !strings.Contains(args, "--agent harmflow-a") ||
		!strings.Contains(args, `"prompt":"You implement."`) {
		t.Errorf("args: %s", args)
	}
	if _, err := os.Stat(filepath.Join(a.Home, ".claude")); err == nil {
		t.Error("the user's configuration was touched")
	}
}

func TestValidateProfile(t *testing.T) {
	a := &Adapter{}
	info := ports.HarnessInfo{Detected: true, Version: "2.1.239", Efforts: []string{"default", "low", "high"},
		Features: map[string]bool{"--effort": true}}
	if err := a.ValidateProfile(info, "sonnet", "high"); err != nil {
		t.Error(err)
	}
	if err := a.ValidateProfile(info, "gpt-5", "default"); err == nil {
		t.Error("a model of another vendor was accepted")
	}
	if err := a.ValidateProfile(info, "default", "ultra"); err == nil {
		t.Error("an unsupported effort was accepted silently")
	}
}
