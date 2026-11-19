package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// stdinRecorder stands in for the app server's stdin: it keeps each line the parser writes, and whether it was closed.
type stdinRecorder struct {
	mu     sync.Mutex
	lines  []string
	closed bool
}

func (s *stdinRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, strings.Split(strings.TrimRight(string(b), "\n"), "\n")...)
	return len(b), nil
}

func (s *stdinRecorder) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// sent is the last message the parser wrote whose method, or whose answer's id, is key.
func (s *stdinRecorder) sent(t *testing.T, key string) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.lines) - 1; i >= 0; i-- {
		var m map[string]any
		if json.Unmarshal([]byte(s.lines[i]), &m) != nil {
			continue
		}
		if m["method"] == key || (m["method"] == nil && fmt.Sprint(m["id"]) == key) {
			return m
		}
	}
	t.Fatalf("sent(%s): the parser wrote %q, want a message whose method or answer's id is %s", key, s.lines, key)
	return nil
}

// begun is a parser that said hello to a stand-in for the app server's stdin.
func begun(t *testing.T, p *parser) *stdinRecorder {
	t.Helper()
	in := &stdinRecorder{}
	if err := p.Begin(in); err != nil {
		t.Fatal(err)
	}
	return in
}

func replay(t *testing.T, file string) (*parser, []ports.HarnessEvent) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := &parser{}
	begun(t, p)
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

// harmflow-app#71: what the probes could not establish reads unknown, never a known state. A sign-in is invalid only
// when the CLI says it is signed out; a probe that is killed, as one is at its timeout, or that fails otherwise leaves
// the harness ready with a diagnostic, and the first execution proves the sign-in.
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

// helpStandIn writes a stand-in for the codex CLI at a supported version whose `exec --help` and `login status` run the
// given shell commands.
func helpStandIn(t *testing.T, help, login string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/bash
case "$1" in
  --version) echo "codex-cli 0.163.0" ;;
  login) ` + login + ` ;;
  exec) ` + help + ` ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// harmflow-app#329: an `exec --help` check that times out leaves Codex ready, with its flags unknown and a diagnostic
// saying the check timed out, which the dashboard shows as a warning; it never names a flag the version lacks. Help
// read in full that lacks a flag still reads as a version that lacks it.
func TestDetectReadsAHelpCheckThatTimesOutAsFlagsUnknown(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	signedIn := `echo "Logged in using ChatGPT" >&2`
	someFlags := `echo "Usage: codex exec  --json  --sandbox <MODE>  --cd <DIR>"`
	allFlags := `echo "Usage: codex exec  --json  --sandbox <MODE>  --cd <DIR>  --skip-git-repo-check"`
	cases := []struct {
		name, help, login          string
		timeout                    time.Duration // the check's limit; zero is its own
		wantUnknown                bool
		wantSupport, wantReadiness string
		wantKey                    string
	}{
		{"read in full", allFlags, signedIn, 0, false, "supported", "ready", ""},
		// exec, so the check's timeout ends the sleep itself, and no child keeps the help's output open.
		{"timed out", `exec sleep 30`, signedIn, time.Second, true, "supported", "ready", "harness_help_timed_out"},
		{"timed out after some flags", someFlags + `; exec sleep 30`, signedIn, time.Second, true, "supported", "ready",
			"harness_help_timed_out"},
		// The sign-in shows unknown in its own field, so the timed-out check keeps the diagnostic.
		{"timed out, sign-in unverified", `exec sleep 30`, `kill -KILL $$`, time.Second, true, "supported", "ready",
			"harness_help_timed_out"},
		{"read in full, lacking a flag", someFlags, signedIn, 0, false, "below_minimum", "not_ready",
			"harness_feature_missing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Adapter{Home: t.TempDir(), helpTimeout: c.timeout}
			info := a.Detect(context.Background(), helpStandIn(t, c.help, c.login))
			if info.FeaturesUnknown != c.wantUnknown || info.VersionSupport != c.wantSupport ||
				info.Readiness != c.wantReadiness || info.DiagnosticKey != c.wantKey {
				t.Errorf("Detect: flags unknown %v, support %s, readiness %s, diagnostic %q (%s); want %v, %s, %s, %q",
					info.FeaturesUnknown, info.VersionSupport, info.Readiness, info.DiagnosticKey, info.Diagnostic,
					c.wantUnknown, c.wantSupport, c.wantReadiness, c.wantKey)
			}
			if c.wantKey == "harness_help_timed_out" && (info.DiagnosticParams["harness"] != Name ||
				info.DiagnosticParams["command"] != "exec --help") {
				t.Errorf("Detect: diagnostic params %v, want the harness %q and the command exec --help",
					info.DiagnosticParams, Name)
			}
		})
	}
}

// harmflow-app#465: with its help unread, Codex runs as if it had an app server, and the first attempt proves it; a
// Codex whose help was read and lists none cannot run, since every attempt goes through the app server.
func TestUnknownFlagsAreTakenAsThere(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	a := &Adapter{Home: t.TempDir()}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	req := ports.RunRequest{WorkDir: "/w", Permissions: perm, AdditionalDirs: []string{"/repo"}, Prompt: "do it"}
	unknown := ports.HarnessInfo{Path: "/bin/codex", Detected: true, Features: map[string]bool{}, FeaturesUnknown: true,
		UnknownFeatures: map[string]bool{"app-server": true}}
	inv, out, err := a.Prepare(unknown, req)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inv.Args, []string{"app-server", "--listen", "stdio://"}) || !inv.Interactive {
		t.Errorf("Prepare = %q (interactive %v), want an interactive app-server", inv.Args, inv.Interactive)
	}
	p := out.(*parser)
	if p.thread["ephemeral"] != true || !slices.Contains(roots(p), "/repo") {
		t.Errorf("Prepare thread %v, want it ephemeral with /repo writable", p.thread)
	}
	missing := ports.HarnessInfo{Path: "/bin/codex", Detected: true, Version: "0.1.0", Features: map[string]bool{}}
	if _, _, err := a.Prepare(missing, req); domain.TextOf(err).Key != "codex_app_server_missing" {
		t.Errorf("Prepare(no app server) = %v, want codex_app_server_missing", err)
	}
}

// roots are the writable roots the thread's config gives the workspace-write sandbox.
func roots(p *parser) []string {
	config, _ := p.thread["config"].(map[string]any)
	r, _ := config["sandbox_workspace_write.writable_roots"].([]string)
	return r
}

// input is the turn's text input: the session's instructions.
func input(p *parser) string {
	in, _ := p.turn["input"].([]map[string]any)
	if len(in) == 0 {
		return ""
	}
	s, _ := in[0]["text"].(string)
	return s
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
	// thread.started names the thread only: the attempt's model and effort read not reported.
	if e := events[0]; e.Kind != ports.HarnessStarted || e.SessionID == "" || e.Model != "" || e.Effort != "" {
		t.Errorf("start: %+v", e)
	}
	// A configuration notice reported as an error item does not fail the turn.
	if end := p.Finish(0, "Reading additional input from stdin...", false, false); end.Class != domain.ClassNone {
		t.Errorf("end: %+v", end)
	}
}

// harmflow-app#442: Codex's notice that its managed requirements override the approval policy HarmFlow set is a
// setting overridden, quoted as Codex wrote it, as one about the sandbox would be; its other notices of that kind, such
// as the Windows sandbox on macOS, stay warnings, and none fails the turn.
func TestParseRequirementsOverride(t *testing.T) {
	p, events := replay(t, "requirements-override.jsonl")
	var kinds []ports.HarnessEventKind
	var overridden, overriddenKey string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
		if e.Kind == ports.HarnessSettingOverridden {
			overridden = e.Message
			if e.Text != nil {
				overriddenKey = e.Text.Key
			}
		}
	}
	// The thread's start names the approval policy its requirements set, which is said once: Codex's own notice of it
	// stays a warning beside it.
	wantKinds := []ports.HarnessEventKind{ports.HarnessStarted, ports.HarnessSettingOverridden, ports.HarnessWarning,
		ports.HarnessWarning, ports.HarnessWarning, ports.HarnessMessage, ports.HarnessUsage}
	if !slices.Equal(kinds, wantKinds) {
		t.Errorf("replay(requirements-override.jsonl) kinds %q, want %q", kinds, wantKinds)
	}
	if !strings.Contains(overridden, "approval_policy on-request") || overriddenKey != "codex_approval_policy_overridden" {
		t.Errorf("replay(requirements-override.jsonl) overridden setting = %q %q, "+
			"want codex_approval_policy_overridden naming approval_policy on-request", overriddenKey, overridden)
	}
	if end := p.Finish(0, "", false, false); end.Class != domain.ClassNone {
		t.Errorf("Finish(0) = %+v, want no failure", end)
	}

	for _, field := range []string{"sandbox_mode", "permission_profile"} {
		notice := "Configured value for `" + field + "` is disallowed by requirements; falling back to required value ReadOnly."
		line := `{"method":"configWarning","params":{"summary":"` + notice + `"}}`
		got := (&parser{}).Line([]byte(line))
		if len(got) != 1 || got[0].Kind != ports.HarnessSettingOverridden || got[0].Message != notice {
			t.Errorf("Line(%s notice) = %+v, want one setting overridden quoting it", field, got)
		}
	}
}

// harmflow-app#441: a command an approval reviewer declined, as Codex 0.163.0 printed it, never ran. Its end is a
// declined call, with no exit code, and neither a success nor a failure.
func TestParseDeclined(t *testing.T) {
	_, events := replay(t, "declined.jsonl")
	var done []ports.HarnessEvent
	for _, e := range events {
		if e.Kind == ports.HarnessToolStarted && e.Command != "python3 /some/dir/outside-the-workspace/script.py" {
			t.Errorf("the shell wrapper was not removed: %q", e.Command)
		}
		if e.Kind == ports.HarnessToolDone {
			done = append(done, e)
		}
	}
	if len(done) != 1 || done[0].ToolID != "item_4" || !done[0].Declined || done[0].IsError || done[0].ExitCode != nil {
		t.Errorf("tool ends %+v, want one for item_4, declined, not an error, with no exit code", done)
	}
}

// The end of each tool call Codex reports: a declined call is neither a success nor a failure, whatever its tool.
func TestParseToolEnd(t *testing.T) {
	cases := []struct {
		name                    string
		item                    string
		wantError, wantDeclined bool
	}{
		{"command succeeded", `{"id":"c","type":"commandExecution","exitCode":0,"status":"completed"}`, false, false},
		{"command exited 1", `{"id":"c","type":"commandExecution","exitCode":1,"status":"failed"}`, true, false},
		{"command failed with no exit", `{"id":"c","type":"commandExecution","exitCode":null,"status":"failed"}`, true,
			false},
		{"command declined", `{"id":"c","type":"commandExecution","exitCode":null,"status":"declined"}`, false, true},
		{"MCP call succeeded", `{"id":"c","type":"mcpToolCall","tool":"search","status":"completed"}`, false, false},
		{"MCP call failed", `{"id":"c","type":"mcpToolCall","tool":"search","status":"failed"}`, true, false},
		{"MCP call declined", `{"id":"c","type":"mcpToolCall","tool":"search","status":"declined"}`, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := `{"method":"item/completed","params":{"item":` + c.item + `}}`
			events := (&parser{}).Line([]byte(line))
			if len(events) != 1 || events[0].Kind != ports.HarnessToolDone || events[0].IsError != c.wantError ||
				events[0].Declined != c.wantDeclined {
				t.Errorf("Line(%s) = %+v, want one tool end with IsError %v and Declined %v", line, events, c.wantError,
					c.wantDeclined)
			}
		})
	}
}

// A file change Codex declined or failed to apply changed nothing, so it names no changed file.
func TestParseFileChangeStatus(t *testing.T) {
	for _, c := range []struct {
		status string
		want   int
	}{{"completed", 1}, {"", 1}, {"declined", 0}, {"failed", 0}} {
		t.Run(c.status, func(t *testing.T) {
			line := `{"method":"item/completed","params":{"item":{"id":"f","type":"fileChange","changes":[{"path":"/work/a.md",` +
				`"kind":{"type":"update"}}],"status":"` + c.status + `"}}}`
			var changed int
			for _, e := range (&parser{}).Line([]byte(line)) {
				if e.Kind == ports.HarnessFileChanged {
					changed++
				}
			}
			if changed != c.want {
				t.Errorf("Line(%s) changed %d files, want %d", line, changed, c.want)
			}
		})
	}
}

// The CLI's error is redacted before the 2000-character tail cuts it; cut first, a secret would keep part of itself,
// too little to match its pattern.
func TestFinishRedactsBeforeTheTail(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	end := (&parser{}).Finish(2, "Error: "+token+" "+strings.Repeat("y", 1969), false, false)
	if strings.Contains(end.Error.Message, strings.Repeat("a", 20)) || !strings.Contains(end.Error.Message, "[REDACTED]") {
		t.Errorf("the error keeps part of the token: %q", end.Error.Message)
	}
}

// A usage limit is an exhausted quota, and so are a workspace out of credits, a spend cap reached, and "Quota
// exceeded", as Codex 0.163.0 and 0.171.0 end a turn on them: each fails over to the alternative harness.
func TestParseQuota(t *testing.T) {
	for _, file := range []string{
		"quota.jsonl",
		"quota-workspace-owner-credits.jsonl",
		"quota-workspace-member-credits.jsonl",
		"quota-workspace-owner-spend-cap.jsonl",
		"quota-workspace-member-spend-cap.jsonl",
		"quota-exceeded.jsonl",
	} {
		t.Run(file, func(t *testing.T) {
			p, _ := replay(t, file)
			if end := p.Finish(1, "", false, false); end.Class != domain.ClassQuotaExhausted {
				t.Errorf("Finish(1, \"\", false, false).Class = %s (%s), want quota_exhausted", end.Class,
					end.Error.Message)
			}
		})
	}
}

// The quota's texts take no text of another class: a rate limit, a sign-in, or a network failure keeps its own, and
// the system's disk quota exceeded, or a task's failure that names credits or a spend cap, stays a task error.
func TestParseQuotaKeepsOtherClasses(t *testing.T) {
	cases := []struct {
		text string
		want domain.ErrorClass
	}{
		{"exceeded retry limit, last status: 429 Too Many Requests", domain.ClassRateLimited},
		{"Rate limit reached for requests. Please try again later.", domain.ClassRateLimited},
		{"Not logged in", domain.ClassHarnessUnavailable},
		{"no Codex credentials were found. Run codex login or provide an API key through a supported auth env var.",
			domain.ClassHarnessUnavailable},
		{"stream disconnected before completion: error sending request", domain.ClassNetwork},
		{"tests failed: the credits screen shows no spend total", domain.ClassTaskError},
		{"failed to write session file: Disk quota exceeded (os error 122)", domain.ClassTaskError},
		{"failed to write session file: Disc quota exceeded (os error 69)", domain.ClassTaskError},
		{"tests failed: want the out of credits banner when the spend cap is reached", domain.ClassTaskError},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			if end := (&parser{}).Finish(1, c.text, false, false); end.Class != c.want {
				t.Errorf("Finish(1, %q, false, false).Class = %s, want %s", c.text, end.Class, c.want)
			}
		})
	}
}

// A signed-out CLI, as Codex 0.163.0 printed it with an empty CODEX_HOME and no API key, is an
// unavailable harness.
func TestParseNotLoggedIn(t *testing.T) {
	p, _ := replay(t, "not-logged-in.jsonl")
	if end := p.Finish(1, "", false, false); end.Class != domain.ClassHarnessUnavailable {
		t.Errorf("Finish(1, \"\", false, false).Class = %s (%s), want harness_unavailable", end.Class, end.Error.Message)
	}
}

// harmflow-app#213: a sign-in is Codex's own only; another tool's, such as gh's, is part of the task's own failure.
func TestParseUnavailable(t *testing.T) {
	cases := []struct {
		text string
		want domain.ErrorClass
	}{
		{"Not logged in", domain.ClassHarnessUnavailable},
		{"no Codex credentials were found. Run codex login or provide an API key through a supported auth env var.", domain.ClassHarnessUnavailable},
		{"gh: You are not logged into any GitHub hosts. Run gh auth login", domain.ClassTaskError},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			p := &parser{}
			if end := p.Finish(1, c.text, false, false); end.Class != c.want {
				t.Errorf("Finish(1, %q, false, false).Class = %s, want %s", c.text, end.Class, c.want)
			}
		})
	}
}

// harmflow-app#212: Codex 0.163.0 waiting for an unreachable provider, as it printed it against a closed local port,
// is a network failure when the attempt ends; a task's error that only mentions the network is the task's own.
func TestParseNetwork(t *testing.T) {
	p, _ := replay(t, "network.jsonl")
	if end := p.Finish(1, "", false, false); end.Class != domain.ClassNetwork {
		t.Errorf("Finish(1, \"\", false, false).Class = %s (%s), want network", end.Class, end.Error.Message)
	}
	text := "tests failed: TestNetworkPolicy expected 2 rules, got 1"
	if end := (&parser{}).Finish(1, text, false, false); end.Class != domain.ClassTaskError {
		t.Errorf("Finish(1, %q, false, false).Class = %s, want task_error", text, end.Class)
	}
}

const (
	networkWait  = `{"method":"error","params":{"willRetry":true,"error":{"message":"Reconnecting... waiting for network (Connection failed: error sending request)"}}}`
	agentMessage = `{"method":"item/completed","params":{"item":{"id":"i-1","type":"agentMessage","text":"Back online."}}}`
)

// feed gives the parser each line, as the executor does, and returns the events they produced.
func feed(p *parser, lines ...string) []ports.HarnessEvent {
	var events []ports.HarnessEvent
	for _, l := range lines {
		events = append(events, p.Line([]byte(l))...)
	}
	return events
}

// harmflow-app#250: Codex never exits while it waits for the network, so a wait is ended by the execution's timeout.
// The attempt is a network failure when a wait was the last thing Codex printed, and a timeout after any other output.
func TestFinishAtTheTimeout(t *testing.T) {
	cases := []struct {
		name  string
		file  string // replayed before lines, when set
		lines []string
		want  domain.ErrorClass
	}{
		{"waiting for the network", "network.jsonl", nil, domain.ClassNetwork},
		{"an agent message after the wait", "network.jsonl", []string{agentMessage}, domain.ClassTimeout},
		{"a line that is not JSON after the wait", "network.jsonl", []string{"panic: unexpected"}, domain.ClassTimeout},
		{"no wait", "", []string{`{"id":2,"result":{"thread":{"id":"t-1"}}}`, `{"method":"turn/started","params":{}}`,
			agentMessage}, domain.ClassTimeout},
		// Numbered retries end on their own, and an error that names the network is not a wait.
		{"a numbered retry", "", []string{`{"method":"error","params":{"willRetry":true,"error":{"message":"Reconnecting... 2/5 (stream disconnected before completion)"}}}`},
			domain.ClassTimeout},
		{"a network error", "", []string{`{"method":"error","params":{"willRetry":false,"error":{"message":"stream disconnected before completion"}}}`},
			domain.ClassTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &parser{}
			begun(t, p)
			if c.file != "" {
				p, _ = replay(t, c.file)
			}
			feed(p, c.lines...)
			end := p.Finish(-1, "", true, false)
			if end.Class != c.want {
				t.Errorf("Finish(-1, \"\", true, false).Class = %s (%s), want %s", end.Class, end.Error.Message, c.want)
			}
			if c.want == domain.ClassNetwork && !strings.Contains(end.Error.Message, "waiting for network") {
				t.Errorf("Finish(-1, \"\", true, false).Error = %q, want Codex's notice", end.Error.Message)
			}
		})
	}
	// A cancellation is one, whatever Codex was doing.
	p, _ := replay(t, "network.jsonl")
	if end := p.Finish(-1, "", true, true); end.Class != domain.ClassCancelled {
		t.Errorf("Finish(-1, \"\", true, true).Class = %s, want cancelled", end.Class)
	}
}

// harmflow-app#250: a stretch of waits for the network is one event, its first notice; a wait after other output is a
// new stretch.
func TestNetworkWaitIsOneEventPerStretch(t *testing.T) {
	count := func(events []ports.HarnessEvent) int {
		n := 0
		for _, e := range events {
			if e.Kind == ports.HarnessNetworkWait {
				n++
				if !strings.HasPrefix(e.Message, "Reconnecting... waiting for network") {
					t.Errorf("the wait's event says %q, want Codex's notice", e.Message)
				}
			}
		}
		return n
	}
	if _, events := replay(t, "network.jsonl"); count(events) != 1 {
		t.Errorf("network.jsonl: %d wait events, want 1: %+v", count(events), events)
	}
	events := feed(&parser{}, networkWait, networkWait, agentMessage, networkWait, networkWait)
	if count(events) != 2 {
		t.Errorf("two stretches: %d wait events, want 2: %+v", count(events), events)
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
		t.Run(c.text, func(t *testing.T) {

			p := &parser{}
			if end := p.Finish(1, c.text, false, false); end.Class != c.want {
				t.Errorf("Finish(1, %q, false, false).Class = %s, want %s", c.text, end.Class, c.want)
			}
		})
	}
}

// Codex's sandbox enforces the workspace and the network; per-tool lists it cannot enforce are named.
func TestPrepareSandbox(t *testing.T) {
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/codex", Features: map[string]bool{"--add-dir": true, "--ephemeral": true}}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	perm.Network = domain.NetworkDeny
	inv, out, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, ExecutionDir: "/x",
		AdditionalDirs: []string{"/repo"}, Prompt: "do it", Model: "gpt-x", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	p := out.(*parser)
	config, _ := p.thread["config"].(map[string]any)
	if p.thread["sandbox"] != "workspace-write" || config["sandbox_workspace_write.network_access"] != false ||
		!slices.Equal(roots(p), []string{"/repo", "/x"}) || p.thread["model"] != "gpt-x" || p.turn["effort"] != "high" ||
		p.thread["cwd"] != "/w" || p.thread["approvalPolicy"] != "never" {
		t.Errorf("Prepare thread %v, turn effort %v; want workspace-write, no network, /repo and /x writable, gpt-x, high, "+
			"in /w, approvals off", p.thread, p.turn["effort"])
	}
	for _, r := range inv.Restrictions {
		if !r.Enforced {
			t.Errorf("%s should be enforced by the sandbox", r.Name)
		}
	}
	if !strings.HasSuffix(input(p), "do it\n") || inv.Stdin != "" {
		t.Errorf("Prepare turn input %q, stdin %q; want the input to end with the prompt and no stdin", input(p),
			inv.Stdin)
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

// Codex takes a custom profile's program lists and reports them as a restriction not enforced, with a key of its own,
// whichever of the two lists is set; nothing of them reaches its arguments. With neither, no restriction names them.
func TestPrepareReportsProgramListsNotEnforced(t *testing.T) {
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/codex", Features: map[string]bool{"--add-dir": true, "--ephemeral": true}}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	perm.Profile = domain.ProfileCustom
	for _, c := range []struct {
		name            string
		allowed, denied []string
	}{
		{"allowed programs", []string{"git"}, nil},
		{"denied programs", nil, []string{"curl"}},
		{"both lists", []string{"git"}, []string{"curl"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			perm := perm
			perm.Custom = &domain.CustomPermissions{AllowedPrograms: c.allowed, DeniedPrograms: c.denied}
			inv, out, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, ExecutionDir: "/x"})
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(inv.Restrictions, func(r ports.Restriction) bool { return r.Name == "program_lists" })
			if i < 0 || inv.Restrictions[i].Enforced || inv.Restrictions[i].Text.Key != "restriction_codex_program_lists" {
				t.Errorf("Prepare(%s) restrictions = %+v, want program_lists not enforced, restriction_codex_program_lists",
					c.name, inv.Restrictions)
			}
			if thread := fmt.Sprint(out.(*parser).thread); strings.Contains(thread, "git") || strings.Contains(thread, "curl") {
				t.Errorf("Prepare(%s) thread = %s, want no program of the lists", c.name, thread)
			}
		})
	}
	perm.Custom = &domain.CustomPermissions{AllowedTools: []string{"Read"}}
	inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, ExecutionDir: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(inv.Restrictions, func(r ports.Restriction) bool { return r.Name == "program_lists" }) {
		t.Errorf("Prepare(no program lists) restrictions = %+v, want none named program_lists", inv.Restrictions)
	}
}

// Codex's sandbox reads anywhere, so a custom profile's read paths are a restriction not enforced, with a key of its
// own. They are never added directories, where the workspace-write sandbox would let the session write; with none, no
// restriction names them.
func TestPrepareReportsReadPathsNotEnforced(t *testing.T) {
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/codex", Features: map[string]bool{"--add-dir": true, "--ephemeral": true}}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	perm.Profile = domain.ProfileCustom
	perm.Custom = &domain.CustomPermissions{WritablePaths: []string{"/out"}, ReadablePaths: []string{"/docs"}}
	inv, out, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, ExecutionDir: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	restriction := func(rs []ports.Restriction, name string) (ports.Restriction, bool) {
		i := slices.IndexFunc(rs, func(r ports.Restriction) bool { return r.Name == name })
		if i < 0 {
			return ports.Restriction{}, false
		}
		return rs[i], true
	}
	r, ok := restriction(inv.Restrictions, "readablePaths")
	if !ok || r.Enforced || r.Text.Key != "restriction_codex_read_paths" {
		t.Errorf("Prepare(read paths) readablePaths = %+v (found %v), want not enforced, restriction_codex_read_paths", r, ok)
	}
	if w := roots(out.(*parser)); slices.Contains(w, "/docs") || !slices.Contains(w, "/out") {
		t.Errorf("Prepare(read paths) writable roots = %q, want /out and no /docs", w)
	}
	perm.Custom = &domain.CustomPermissions{WritablePaths: []string{"/out"}}
	inv, _, _ = a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, ExecutionDir: "/x"})
	if r, ok := restriction(inv.Restrictions, "readablePaths"); ok {
		t.Errorf("Prepare(no read paths) readablePaths = %+v, want none", r)
	}
}

// A read_only task runs in Codex's read-only sandbox, forced with a -c override as approvals off are, with the
// execution's folder added and its network blocked. The profile is enforced when Codex's managed requirements, as
// read, allow the read-only sandbox and approvals off, or manage nothing. When they leave one out, or could not be
// read, it is reported as not enforced, with its key, and the task still runs.
func TestPrepareReadOnly(t *testing.T) {
	for _, c := range []struct {
		name         string
		requirements *ports.ManagedRequirements
		enforced     bool
	}{
		{"requirements unread", nil, false},
		{"nothing managed", &ports.ManagedRequirements{}, true},
		{"both allowed", &ports.ManagedRequirements{Managed: true, ApprovalPolicies: []string{"never", "on-request"},
			SandboxModes: []string{"read-only", "workspace-write"}}, true},
		{"approvals off left out", &ports.ManagedRequirements{Managed: true,
			ApprovalPolicies: []string{"on-request", "untrusted"}}, false},
		{"read-only sandbox left out", &ports.ManagedRequirements{Managed: true,
			SandboxModes: []string{"workspace-write"}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := &Adapter{Home: t.TempDir()}
			info := ports.HarnessInfo{Path: "/bin/codex", Features: map[string]bool{"--add-dir": true},
				ManagedRequirements: c.requirements}
			perm := *domain.TaskDocument{}.Effective().Spec.Permissions
			perm.Profile = domain.ProfileReadOnly
			inv, out, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, ExecutionDir: "/x"})
			if err != nil {
				t.Fatalf("Prepare(read_only) = %v, want no error: the task still runs", err)
			}
			p := out.(*parser)
			if p.thread["sandbox"] != "read-only" || p.thread["approvalPolicy"] != "never" || p.thread["config"] != nil {
				t.Errorf("Prepare(read_only) thread = %v, want the read-only sandbox, approvals off, and no writable root",
					p.thread)
			}
			got := map[string]ports.Restriction{}
			for _, r := range inv.Restrictions {
				got[r.Name] = r
			}
			r, ok := got["read_only"]
			switch {
			case !ok:
				t.Errorf("Prepare(read_only) restrictions = %+v, want one for read_only", inv.Restrictions)
			case c.enforced && (!r.Enforced || r.Text.Message == ""):
				t.Errorf("Prepare(read_only) read_only restriction = %+v, want enforced, with its text", r)
			case !c.enforced && (r.Enforced || r.Text.Key != "restriction_codex_read_only" || r.Text.Message == ""):
				t.Errorf("Prepare(read_only) read_only restriction = %+v, want not enforced, with the key "+
					"restriction_codex_read_only and its text", r)
			}
			if r, ok := got["network"]; !ok || !r.Enforced {
				t.Errorf("Prepare(read_only) network restriction = %+v (found %t), want enforced", r, ok)
			}
		})
	}
}

// harmflow-app#473: a session is ephemeral unless the task keeps it. Kept, the thread starts not ephemeral, and its
// start names the folder where Codex keeps it, under the CODEX_HOME the attempt runs with.
func TestPrepareEphemeralSession(t *testing.T) {
	daemonHome := t.TempDir()
	t.Setenv("CODEX_HOME", daemonHome)
	a := &Adapter{Home: t.TempDir()}
	if !a.KeepsSessions() {
		t.Error("KeepsSessions() = false, want true")
	}
	attemptHome := t.TempDir()
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	for _, c := range []struct {
		name    string
		keep    bool
		env     []string
		wantDir string
	}{
		{name: "on"},
		{name: "off", keep: true, wantDir: filepath.Join(daemonHome, "sessions")},
		{name: "off with the attempt's own CODEX_HOME", keep: true, env: []string{"CODEX_HOME=" + attemptHome},
			wantDir: filepath.Join(attemptHome, "sessions")},
	} {
		t.Run(c.name, func(t *testing.T) {
			info := ports.HarnessInfo{Path: "/bin/codex", Version: "0.163.0", Features: map[string]bool{}}
			inv, out, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, Prompt: "do it",
				KeepSession: c.keep, Env: c.env})
			if err != nil {
				t.Fatal(err)
			}
			p := out.(*parser)
			if p.thread["ephemeral"] != !c.keep || slices.ContainsFunc(inv.Restrictions,
				func(r ports.Restriction) bool { return r.Name == "ephemeral" }) {
				t.Errorf("Prepare thread ephemeral %v, restrictions %+v; want %v, and none about it", p.thread["ephemeral"],
					inv.Restrictions, !c.keep)
			}
			begun(t, p)
			feed(p, `{"id":1,"result":{}}`)
			events := p.Line([]byte(`{"id":2,"result":{"thread":{"id":"thread-1"},"approvalPolicy":"never"}}`))
			if len(events) != 1 || events[0].Kind != ports.HarnessStarted || events[0].SessionID != "thread-1" ||
				events[0].SessionDir != c.wantDir {
				t.Errorf("thread start = %+v, want session thread-1 kept in %q", events, c.wantDir)
			}
		})
	}
}

// A task that sets no model or effort, or sets default, passes Codex no model and no reasoning effort, so those of
// Codex's own configuration apply. TestPrepareSandbox shows a set model and effort reaching it.
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
			_, out, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, Prompt: "do it",
				Model: c.model, Effort: c.effort})
			if err != nil {
				t.Fatal(err)
			}
			p := out.(*parser)
			if _, ok := p.thread["model"]; ok {
				t.Errorf("Prepare(model %q) thread = %v, want no model", c.model, p.thread)
			}
			if _, ok := p.turn["effort"]; ok {
				t.Errorf("Prepare(effort %q) turn = %v, want no effort", c.effort, p.turn)
			}
		})
	}
}

// harmflow-app#849: the session's system context, the user's general instructions before HarmFlow's orchestration
// context, opens Codex's input whole, and the task's prompt follows under its own heading.
func TestPrepareOpensTheInputWithTheSessionContext(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/codex", Features: map[string]bool{"--add-dir": true}}
	context := "## The user's general instructions\n\nAnswer in pt-BR.\n\n" +
		"## HarmFlow's orchestration context\n\nThis session is run by HarmFlow, a local scheduler.\n"
	_, out, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", ExecutionDir: t.TempDir(), Prompt: "Do the work.",
		Context: context, Permissions: *domain.TaskDocument{}.Effective().Spec.Permissions})
	if err != nil {
		t.Fatal(err)
	}
	if want := context + "\n\n## Task\n\nDo the work.\n"; input(out.(*parser)) != want {
		t.Errorf("Prepare instructions =\n%s\nwant\n%s", input(out.(*parser)), want)
	}
}

// Built-ins are read from copies in the execution's directory, never from the app; the user's own items stay put. No
// agent is inlined as the session's role: each one is listed by name, and the task's prompt carries the role.
func TestPrepareDeliversBuiltins(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "skills", "hf-x")
	_ = os.MkdirAll(filepath.Join(skill, "scripts"), 0o700)
	_ = os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: hf-x\ndescription: d\n---\nbody"), 0o600)
	_ = os.WriteFile(filepath.Join(skill, "scripts", "run.sh"), []byte("echo hi"), 0o700)
	first := filepath.Join(root, "agents", "hf-a.md")
	other := filepath.Join(root, "agents", "hf-b.md")
	_ = os.MkdirAll(filepath.Dir(first), 0o700)
	_ = os.WriteFile(first, []byte("---\nname: hf-a\ndescription: an agent\n---\nYou implement."), 0o600)
	_ = os.WriteFile(other, []byte("---\nname: hf-b\ndescription: another\n---\nYou review."), 0o600)
	userSkill := filepath.Join(t.TempDir(), "skills", "mine")
	exec := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/codex", Features: map[string]bool{"--add-dir": true}}
	_, out, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", ExecutionDir: exec, Prompt: "do it",
		Permissions: *domain.TaskDocument{}.Effective().Spec.Permissions,
		Skills: []ports.ResolvedItem{{Name: "hf-x", Kind: "skill", Builtin: true, Path: skill},
			{Name: "mine", Kind: "skill", Path: userSkill}},
		Agents: []ports.ResolvedItem{{Name: "hf-a", Kind: "agent", Builtin: true, Path: first, Description: "an agent"},
			{Name: "hf-b", Kind: "agent", Builtin: true, Path: other, Description: "another"}}})
	if err != nil {
		t.Fatal(err)
	}
	stdin := input(out.(*parser))
	delivered := filepath.Join(exec, "codex-builtins")
	for _, f := range []string{"skills/hf-x/SKILL.md", "skills/hf-x/scripts/run.sh", "agents/hf-a.md", "agents/hf-b.md"} {
		if _, err := os.Stat(filepath.Join(delivered, f)); err != nil {
			t.Errorf("Prepare delivered no %s: os.Stat = %v, want no error", f, err)
		}
	}
	for _, want := range []string{filepath.Join(delivered, "skills", "hf-x", "SKILL.md"),
		"hf-a — an agent — " + filepath.Join(delivered, "agents", "hf-a.md"),
		"hf-b — another — " + filepath.Join(delivered, "agents", "hf-b.md"), filepath.Join(userSkill, "SKILL.md"),
		"## Task\n\ndo it\n"} {
		if !strings.Contains(stdin, want) {
			t.Errorf("Prepare instructions =\n%s\nwant them to contain %q", stdin, want)
		}
	}
	for _, unwanted := range []string{"You implement.", "Your role"} {
		if strings.Contains(stdin, unwanted) {
			t.Errorf("Prepare instructions =\n%s\nwant no inlined agent, and no %q", stdin, unwanted)
		}
	}
	if strings.Contains(stdin, root) {
		t.Errorf("Prepare instructions =\n%s\nwant no built-in named at its source %s", stdin, root)
	}
	if entries, _ := os.ReadDir(a.Home); len(entries) > 0 {
		t.Error("the user's home was touched")
	}
}

// The Costs tab (harmflow-app#39), on a real Codex 0.163.0 run: the thread's token usage carries the attempt's usage,
// its inputTokens include the cached ones, and the cost is estimated from the table when the attempt ends.
func TestUsageFromTurnCompleted(t *testing.T) {
	price := domain.Price{Input: 1.25, CacheRead: 0.125, Output: 10}
	prices := domain.PriceTable{AsOf: "2026-01-02", Prices: map[string]domain.Price{"gpt-test": price}}

	p, events := replay(t, "usage.jsonl")
	p.model, p.modelSource, p.prices = "gpt-test", domain.ModelFromConfig, prices
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
	w.Line([]byte(`{"method":"thread/tokenUsage/updated","params":{"tokenUsage":{"total":{"inputTokens":5000,"cachedInputTokens":3000,"cacheWriteInputTokens":1500,"outputTokens":20}}}}`))
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
	models, efforts, src := a.models(a.codexHome())
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

// With no readable models cache, Codex's capabilities are only declared, so a model or effort other than default
// cannot be checked and says so; with the cache, it is checked as before and nothing warns.
func TestProfileWarningWithoutTheModelsCache(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Detected: true, Version: "0.163.0"}
	info.Models, info.Efforts, info.CapabilitySource = a.models(a.codexHome())
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
	info.Models, info.Efforts, info.CapabilitySource = a.models(a.codexHome())
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

// A harness is a file whose --version names it. Discovery skips one that does not and takes the next location; a path
// set by hand that does not is installed but not ready, saying why; Identify refuses both it and a file that cannot be
// executed.
func TestDetectSkipsAFileThatIsNotCodex(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	impostor := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(impostor, []byte("#!/bin/bash\necho \"GNU bash, version 5.2.0\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Both on PATH, the impostor first, so no installation of this machine's comes before them.
	real := filepath.Join(t.TempDir(), "codex")
	data, err := os.ReadFile(standIn(t, `echo "codex-cli 0.163.0"`, `echo "Logged in using ChatGPT" >&2`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, data, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Home: t.TempDir(), PathEnv: filepath.Dir(impostor) + ":" + filepath.Dir(real)}
	info := a.Detect(context.Background(), "")
	if want, _ := filepath.EvalSymlinks(real); (info.Path != real && info.Path != want) || info.Readiness != "ready" {
		t.Errorf("discovery took %s (%s, %s), want %s", info.Path, info.Readiness, info.Diagnostic, real)
	}
	info = a.Detect(context.Background(), impostor)
	if info.Readiness != "installed" || info.DiagnosticKey != "harness_not_identified" || !info.Detected {
		t.Errorf("a path set by hand that is not Codex: %s, %s (%s)", info.Readiness, info.DiagnosticKey,
			info.Diagnostic)
	}
	plain := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(plain, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, path, key string }{
		{"a system program", "/bin/sh", "harness_not_identified"},
		{"an impostor", impostor, "harness_not_identified"},
		{"a file that is not executable", plain, "harness_path_not_executable"},
		{"Codex", real, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.path == "/bin/sh" && runtime.GOOS != "darwin" {
				t.Skip("macOS's /bin/sh is bash, which answers --version as a program that is not Codex; " +
					"elsewhere it is often dash, which fails --version")
			}
			got := ""
			if err := a.Identify(context.Background(), c.path); err != nil {
				got = domain.TextOf(err).Key
			}
			if got != c.key {
				t.Errorf("Identify(%s) = %q, want %q", c.path, got, c.key)
			}
		})
	}
}

// harmflow-app#475: an attempt on an installation runs with that installation's home as CODEX_HOME, in place of the one
// the daemon's environment carries, and keeps its session and reads its configured model there.
func TestPrepareRunsWithTheInstallationsHome(t *testing.T) {
	daemonHome, home := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", daemonHome)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(`model = "model-of-the-home"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/codex", Home: home, InstallationID: "~/other",
		Features: map[string]bool{"--ephemeral": true}}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	inv, out, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, Prompt: "do it",
		KeepSession: true, Env: []string{"PATH=/bin", "CODEX_HOME=" + daemonHome}})
	if err != nil {
		t.Fatal(err)
	}
	var homes []string
	for _, kv := range inv.Env {
		if v, ok := strings.CutPrefix(kv, "CODEX_HOME="); ok {
			homes = append(homes, v)
		}
	}
	if !slices.Equal(homes, []string{home}) || !slices.Contains(inv.Env, "PATH=/bin") {
		t.Errorf("Prepare env %q, want PATH kept and CODEX_HOME %q alone", inv.Env, home)
	}
	begun(t, out.(*parser))
	out.Line([]byte(`{"id":1,"result":{}}`))
	events := out.Line([]byte(`{"id":2,"result":{"thread":{"id":"thread-1"},"approvalPolicy":"never"}}`))
	if len(events) != 1 || events[0].SessionDir != filepath.Join(home, "sessions") {
		t.Errorf("thread start = %+v, want the session kept under %q", events, home)
	}
	if p, ok := out.(*parser); !ok || p.model != "model-of-the-home" || p.modelSource != domain.ModelFromConfig {
		t.Errorf("Prepare parser = %+v, want the model of the home's configuration", out)
	}
}

// harmflow-app#1028: a session's environment carries no CODEX_HOME, so an attempt on an installation detection did not
// give still runs with the home in use, the one the daemon's CODEX_HOME names, as its only CODEX_HOME.
func TestPrepareWithoutAnInstallationRunsWithTheHomeInUse(t *testing.T) {
	daemonHome := t.TempDir()
	t.Setenv("CODEX_HOME", daemonHome)
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/codex", Features: map[string]bool{"--ephemeral": true}}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, Prompt: "do it",
		Env: ports.SessionEnv([]string{"PATH=/bin", "CODEX_HOME=" + daemonHome})})
	if err != nil {
		t.Fatal(err)
	}
	var homes []string
	for _, kv := range inv.Env {
		if v, ok := strings.CutPrefix(kv, "CODEX_HOME="); ok {
			homes = append(homes, v)
		}
	}
	if !slices.Equal(homes, []string{daemonHome}) || !slices.Contains(inv.Env, "PATH=/bin") {
		t.Errorf("Prepare env %q, want PATH kept and CODEX_HOME %q alone", inv.Env, daemonHome)
	}
}
