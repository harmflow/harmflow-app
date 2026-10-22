package claudecode

import (
	"bufio"
	"context"
	"math"
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
	// FS-005 FR-008: the init line names the model the session runs, which the attempt records (D-105).
	if e := events[0]; e.Kind != ports.HarnessStarted || e.Model != "claude-haiku-4-5-20251001" || e.Effort != "" {
		t.Errorf("start: %+v", e)
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
		t.Errorf("got %s (%s), want quota_exhausted", end.Class, end.Error.Message)
	}
}

// FS-012 FR-005: the CLI's own error stays as it printed it, with no key; the daemon's text stands in, with one, when
// the CLI printed nothing, and for a timeout.
func TestFinishKeysOnlyTheDaemonsText(t *testing.T) {
	p := &parser{tools: map[string]toolUse{}}
	if end := p.Finish(2, "Error: boom", false, false); end.Error.Key != "" || end.Error.Message != "Error: boom" {
		t.Errorf("the CLI's error: %+v", end.Error)
	}
	if end := p.Finish(2, "", false, false); end.Error.Key != "harness_exit_code" || end.Error.Params["code"] != "2" ||
		end.Error.Message != "Claude Code exited with code 2" {
		t.Errorf("no output: %+v", end.Error)
	}
	if end := p.Finish(-1, "", true, false); end.Class != domain.ClassTimeout || end.Error.Key != "execution_timed_out" {
		t.Errorf("timeout: %+v", end)
	}
}

// FS-009 FR-005: the CLI's error is redacted before the 2000-character tail cuts it; cut first, a secret would keep
// part of itself, too little to match its pattern.
func TestFinishRedactsBeforeTheTail(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	end := (&parser{tools: map[string]toolUse{}}).Finish(2, "Error: "+token+" "+strings.Repeat("y", 1969), false, false)
	if strings.Contains(end.Error.Message, strings.Repeat("a", 20)) || !strings.Contains(end.Error.Message, "[REDACTED]") {
		t.Errorf("the error keeps part of the token: %q", end.Error.Message)
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

// The Costs tab (harmflow-app#39), on a real Claude Code 2.1.239 run: the messages count once each while it runs,
// and the result line's modelUsage settles the output and the cost the CLI printed.
func TestUsageFromTheResultLine(t *testing.T) {
	p, events := replay(t, "usage.jsonl")
	var live []*domain.Usage
	for _, e := range events {
		if e.Kind == ports.HarnessUsage {
			live = append(live, e.Usage)
		}
	}
	// Three messages, each repeated on two or three lines: one update per message.
	if len(live) != 3 {
		t.Fatalf("%d live updates, want 3", len(live))
	}
	last := live[2]
	want := domain.Tokens{Input: 25, CacheWriteLong: 12752, CacheRead: 65425}
	if last.Status != domain.UsagePartial || len(last.Models) != 1 || last.Models[0].Tokens != want || last.CostUSD != nil {
		t.Errorf("live usage: %+v", last)
	}

	u := p.Finish(0, "", false, false).Usage
	want.Output = 866
	if u.Status != domain.UsageReported || u.ModelSource != domain.ModelFromHarness || len(u.Models) != 1 {
		t.Fatalf("final usage: %+v", u)
	}
	m := u.Models[0]
	if m.Model != "claude-haiku-4-5-20251001" || m.Tokens != want {
		t.Errorf("model usage: %+v", m)
	}
	if u.CostSource != domain.CostReported || u.CostUSD == nil || *u.CostUSD != 0.0364015 || u.PricesAsOf != Prices.AsOf {
		t.Errorf("cost: %+v", u)
	}
	// The price table reproduces the CLI's figure: it gives the split by token class.
	if m.Price == nil || math.Abs(m.Price.Cost(m.Tokens)-0.0364015) > 1e-9 {
		t.Errorf("the table's estimate for %+v is not the CLI's $0.0364015", m)
	}
}

// A cancelled attempt has no result line: its input and cache counts are partial, without output or cost.
func TestUsageOfACancelledAttempt(t *testing.T) {
	p, _ := replay(t, "cancelled.jsonl")
	u := p.Finish(143, "", false, true).Usage
	if u.Status != domain.UsagePartial || len(u.Models) != 1 || u.CostUSD != nil || u.CostSource != "" {
		t.Fatalf("usage: %+v", u)
	}
	if got := u.Models[0].Tokens; got != (domain.Tokens{Input: 9, CacheWriteLong: 11511, CacheRead: 14129}) {
		t.Errorf("tokens: %+v", got)
	}
	// A CLI that printed no usage at all reports none, which is not zero.
	q, _ := replay(t, "success.jsonl")
	if u := q.Finish(0, "", false, false).Usage; u.Status != domain.UsageNotReported || len(u.Models) != 0 {
		t.Errorf("no usage: %+v", u)
	}
	// An error's result line that counts nothing does not erase what the messages counted.
	p.Line([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"limit","total_cost_usd":0,` +
		`"usage":{"input_tokens":0,"output_tokens":0},"modelUsage":{}}`))
	if u := p.Finish(1, "", false, false).Usage; u.Status != domain.UsagePartial || u.Models[0].Tokens.CacheRead != 14129 {
		t.Errorf("an empty result line: %+v", u)
	}
}

// modelUsage keys each model by its full id; their cache writes are split by the result line's lifetimes, and a model
// the table has no price for keeps the cost the CLI reported for it.
func TestUsageAcrossModels(t *testing.T) {
	defer func(old domain.PriceTable) { Prices = old }(Prices)
	Prices = domain.PriceTable{AsOf: "2026-01-02", Prices: map[string]domain.Price{
		"claude-main-1": {Input: 3, CacheWriteShort: 3.75, CacheWriteLong: 6, CacheRead: 0.3, Output: 15}}}
	p := &parser{tools: map[string]toolUse{}}
	for _, l := range []string{
		`{"type":"system","subtype":"init","session_id":"s","model":"claude-main-1-20260101"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"ok","total_cost_usd":0.5,
		  "usage":{"input_tokens":10,"cache_creation_input_tokens":4000,"cache_read_input_tokens":0,"output_tokens":10,
		    "cache_creation":{"ephemeral_5m_input_tokens":1000,"ephemeral_1h_input_tokens":3000}},
		  "modelUsage":{
		    "claude-aux-1":{"inputTokens":900,"outputTokens":40,"cacheReadInputTokens":0,"cacheCreationInputTokens":400,"costUSD":0.01},
		    "claude-main-1-20260101":{"inputTokens":10,"outputTokens":500,"cacheReadInputTokens":2000,"cacheCreationInputTokens":4000,"costUSD":0.49}}}`,
	} {
		p.Line([]byte(strings.ReplaceAll(l, "\n", "")))
	}
	u := p.Finish(0, "", false, false).Usage
	if len(u.Models) != 2 || u.Models[0].Model != "claude-main-1-20260101" || u.Models[1].Model != "claude-aux-1" {
		t.Fatalf("the session's model comes first: %+v", u.Models)
	}
	main, aux := u.Models[0], u.Models[1]
	if main.Tokens.CacheWriteLong != 3000 || main.Tokens.CacheWriteShort != 1000 || aux.Tokens.CacheWriteLong != 300 ||
		aux.Tokens.CacheWriteShort != 100 {
		t.Errorf("cache writes by lifetime: %+v %+v", main.Tokens, aux.Tokens)
	}
	if main.Price == nil || aux.Price != nil || aux.CostUSD == nil || *aux.CostUSD != 0.01 {
		t.Errorf("prices: %+v %+v", main, aux)
	}
	if u.CostSource != domain.CostReported || *u.CostUSD != 0.5 {
		t.Errorf("the CLI's total is the attempt's cost: %+v", u)
	}
}

// standIn writes a stand-in for the claude CLI whose --version and `auth status` run the given shell commands, and
// whose --help lists every flag the adapter needs.
func standIn(t *testing.T, version, auth string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/bash
case "$1" in
  --version) ` + version + ` ;;
  --help) echo "-p, --print  --output-format <format>  --permission-mode <mode>  --allowedTools <tools...>  --append-system-prompt <prompt>" ;;
  auth) ` + auth + ` ;;
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
	version, signedIn := `echo "2.1.239 (Claude Code)"`, `echo '{"loggedIn": true}'`
	cases := []struct {
		name, version, auth                         string
		wantVersion, wantSupport, wantAuth, wantKey string
		wantReadiness                               string
	}{
		{"signed in", version, signedIn, "2.1.239", "supported", "valid", "", "ready"},
		{"no version number", `echo "Claude Code (dev build)"`, signedIn, "", "unknown", "valid", "", "ready"},
		// The shell ends itself as the probe's timeout would end it, with no 20-second wait.
		{"sign-in probe killed", version, `kill -KILL $$`, "2.1.239", "supported", "unknown",
			"authentication_unverified", "ready"},
		{"sign-in probe fails", version, `echo "Error: could not reach the credential store" >&2; exit 1`, "2.1.239",
			"supported", "unknown", "authentication_unverified", "ready"},
		// What the CLI prints when it is signed out, exiting 1.
		{"signed out", version, `echo '{"loggedIn": false, "authMethod": "none"}'; exit 1`, "2.1.239", "supported",
			"invalid", "claude_not_signed_in", "not_ready"},
		{"signed out in text", version, `echo "Not logged in" >&2; exit 1`, "2.1.239", "supported", "invalid",
			"claude_not_signed_in", "not_ready"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info := (&Adapter{Home: t.TempDir()}).Detect(context.Background(), standIn(t, c.version, c.auth))
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
