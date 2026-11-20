package geminicli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// tempDir is a temporary directory by its real path, as the adapter resolves none.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

const helpText = "--output-format --approval-mode --model --include-directories"

// standIn writes an executable named name in dir that prints version for --version and the help for --help, and
// records in dir/env what the API-key variables are set to, so a test can tell whether they reached it.
func standIn(t *testing.T, dir, name, version string) string {
	t.Helper()
	script := `#!/bin/sh
echo "GEMINI_API_KEY=${GEMINI_API_KEY:-}" > "` + dir + `/env"
case "$1" in
  --version) echo "` + version + `" ;;
  --help) echo "` + helpText + `" ;;
esac
`
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func signIn(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", credentialsFile), []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Absent, signed out despite an API key in the environment, and ready by the CLI's own sign-in.
func TestDetectReadyByItsOwnSignInAlone(t *testing.T) {
	ctx := context.Background()
	empty := tempDir(t)

	absent := (&Adapter{Home: tempDir(t), PathEnv: empty, KnownLocations: []string{}}).Detect(ctx, "")
	if absent.Detected || absent.Readiness != "not_installed" || absent.DiagnosticKey != "gemini_not_found" {
		t.Fatalf("absent: %+v", absent)
	}

	dir, home := tempDir(t), tempDir(t)
	standIn(t, dir, "gemini", "0.18.0")
	a := &Adapter{Home: home, PathEnv: dir, KnownLocations: []string{},
		Env: []string{"PATH=/usr/bin:/bin", "GEMINI_API_KEY=secret-key"}}
	out := a.Detect(ctx, "")
	if !out.Detected || out.Version != "0.18.0" || out.Path != filepath.Join(dir, "gemini") {
		t.Fatalf("signed out: detection %+v", out)
	}
	if out.Authentication != "invalid" || out.Readiness != "not_ready" || out.DiagnosticKey != "gemini_not_signed_in" {
		t.Fatalf("signed out with an API key: %+v", out)
	}
	if env, _ := os.ReadFile(filepath.Join(dir, "env")); strings.TrimSpace(string(env)) != "GEMINI_API_KEY=" {
		t.Fatalf("the API key reached the CLI: %q", env)
	}

	signIn(t, home)
	if out := a.Detect(ctx, ""); out.Authentication != "valid" || !out.Ready() {
		t.Fatalf("signed in: %+v", out)
	}
}

// GEMINI_CLI_HOME moves the folder the sign-in is read from.
func TestDetectReadsTheSignInUnderGeminiCLIHome(t *testing.T) {
	dir, home, moved := tempDir(t), tempDir(t), tempDir(t)
	standIn(t, dir, "gemini", "0.18.0")
	signIn(t, moved)
	a := &Adapter{Home: home, PathEnv: dir, KnownLocations: []string{},
		Env: []string{"PATH=/usr/bin:/bin", homeVariable + "=" + moved}}
	if out := a.Detect(context.Background(), ""); !out.Ready() {
		t.Fatalf("got %+v", out)
	}
}

// A file is the Gemini CLI when it is named gemini and its --version output is a version number.
func TestIdentifyByNameAndVersionShape(t *testing.T) {
	ctx := context.Background()
	a := &Adapter{Home: tempDir(t)}
	cases := []struct {
		name, file, version string
		ok                  bool
	}{
		{"named gemini with a version", "gemini", "0.18.0", true},
		{"a pre-release", "gemini", "0.19.0-preview.1", true},
		{"another name", "claude", "0.18.0", false},
		{"a version that names another tool", "gemini", "2.1.0 (Claude Code)", false},
		{"no version", "gemini", "hello", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := standIn(t, tempDir(t), c.file, c.version)
			if err := a.Identify(ctx, path); (err == nil) != c.ok {
				t.Fatalf("Identify(%s, %q) = %v, want ok %v", c.file, c.version, err, c.ok)
			}
		})
	}
}

func TestValidateProfile(t *testing.T) {
	a := &Adapter{}
	for _, m := range []string{"", domain.DefaultModel, "gemini-2.5-pro", "gemini-3-flash"} {
		if err := a.ValidateProfile(ports.HarnessInfo{}, m, ""); err != nil {
			t.Errorf("model %q: %v", m, err)
		}
	}
	if err := a.ValidateProfile(ports.HarnessInfo{}, "gpt-5", ""); err == nil {
		t.Error("an unknown model passed")
	}
	if err := a.ValidateProfile(ports.HarnessInfo{}, "", "high"); err == nil {
		t.Error("an effort passed")
	}
}

func boolp(b bool) *bool { return &b }

// The run is non-interactive, with stream-json, the model, the prompt on stdin, the
// extra folders included, no API key, and each setting the CLI cannot enforce reported as not enforced.
func TestPrepare(t *testing.T) {
	a := &Adapter{}
	info := ports.HarnessInfo{Path: "/bin/gemini", Features: map[string]bool{"--include-directories": true}}
	req := ports.RunRequest{Prompt: "do it", Context: "ctx", Model: "gemini-2.5-pro", WorkDir: "/w",
		AdditionalDirs: []string{"/extra"}, ExecutionDir: "/exec", Env: []string{"A=1", "GEMINI_API_KEY=k"},
		Permissions: domain.Permissions{Profile: domain.ProfileWorkspaceWrite, RestrictToWorkspace: boolp(true),
			Network: domain.NetworkDeny}}
	inv, parser, err := a.Prepare(info, req)
	if err != nil || parser == nil {
		t.Fatal(err)
	}
	args := strings.Join(inv.Args, " ")
	for _, want := range []string{"--output-format stream-json", "--model gemini-2.5-pro", "--approval-mode yolo",
		"--include-directories /extra,/exec"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if inv.Dir != "/w" || inv.Stdin != "ctx\n\ndo it" || slices.Contains(inv.Env, "GEMINI_API_KEY=k") {
		t.Errorf("invocation %+v", inv)
	}
	names := map[string]bool{}
	for _, r := range inv.Restrictions {
		if r.Enforced || r.Text.Key != "restriction_gemini_unenforced" {
			t.Errorf("restriction %+v", r)
		}
		names[r.Name] = true
	}
	if !names["restrictToWorkspace"] || !names["network"] {
		t.Errorf("restrictions %v", names)
	}

	req.Permissions = domain.Permissions{Profile: domain.ProfileCustom, Custom: &domain.CustomPermissions{
		AllowedTools: []string{"Read"}, AllowedPrograms: []string{"git"}, ReadablePaths: []string{"/r"}}}
	inv, _, _ = a.Prepare(info, req)
	names = map[string]bool{}
	for _, r := range inv.Restrictions {
		names[r.Name] = !r.Enforced
	}
	for _, n := range []string{"tool_allowlist", "program_lists", "readablePaths"} {
		if !names[n] {
			t.Errorf("custom: %s not reported as not enforced: %v", n, names)
		}
	}

	req.Permissions = domain.Permissions{Profile: domain.ProfileReadOnly}
	inv, _, _ = a.Prepare(info, req)
	if !strings.Contains(strings.Join(inv.Args, " "), "--approval-mode default") || len(inv.Restrictions) != 1 ||
		inv.Restrictions[0].Name != "read_only" || inv.Restrictions[0].Enforced {
		t.Errorf("read only: %v %+v", inv.Args, inv.Restrictions)
	}
}

const stream = `{"type":"init","session_id":"s-1","model":"gemini-2.5-pro"}
{"type":"message","role":"user","content":"do it"}
{"type":"message","role":"assistant","content":"Looking","delta":true}
{"type":"message","role":"assistant","content":" now.","delta":true}
{"type":"tool_use","tool_name":"run_shell_command","tool_id":"t-1","parameters":{"command":"ls"}}
{"type":"tool_result","tool_id":"t-1","status":"success","output":"a b"}
{"type":"message","role":"assistant","content":"Done.","delta":true}
{"type":"result","status":"success","stats":{"total_tokens":10}}`

// The stream's lines become the event kinds the other harnesses produce.
func TestParserNormalizesTheStream(t *testing.T) {
	a := &Adapter{}
	_, p, _ := a.Prepare(ports.HarnessInfo{}, ports.RunRequest{})
	var kinds []ports.HarnessEventKind
	var events []ports.HarnessEvent
	for _, l := range strings.Split(stream, "\n") {
		for _, ev := range p.Line([]byte(l)) {
			kinds = append(kinds, ev.Kind)
			events = append(events, ev)
		}
	}
	want := []ports.HarnessEventKind{ports.HarnessStarted, ports.HarnessMessage, ports.HarnessToolStarted,
		ports.HarnessToolDone, ports.HarnessMessage}
	if !slices.Equal(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	if events[0].SessionID != "s-1" || events[0].Model != "gemini-2.5-pro" || events[1].Message != "Looking now." ||
		events[2].Command != "ls" || events[3].Tool != "run_shell_command" || events[3].Output != "a b" {
		t.Fatalf("events %+v", events)
	}
	end := p.Finish(0, "", false, false)
	if end.Class != "" || end.FinalText != "Done." || end.SessionID != "s-1" {
		t.Fatalf("end %+v", end)
	}
}

// Each failure class, from the CLI's own texts and its exit code.
func TestFinishClassifiesFailures(t *testing.T) {
	cases := []struct {
		name      string
		lines     string
		exit      int
		stderr    string
		class     domain.ErrorClass
		signedOut bool
	}{
		{"quota", `{"type":"result","status":"error","error":{"type":"api","message":"Quota exceeded for quota metric 'Gemini requests per day'"}}`, 1, "", domain.ClassQuotaExhausted, false},
		{"rate limit", `{"type":"error","severity":"error","message":"429 Too Many Requests"}`, 1, "", domain.ClassRateLimited, false},
		{"signed out by exit code", "", 41, "Error authenticating", domain.ClassHarnessUnavailable, true},
		{"signed out by text", "", 1, "Login required. Please sign in.", domain.ClassHarnessUnavailable, true},
		{"network", "", 1, "fetch failed: getaddrinfo ENOTFOUND generativelanguage.googleapis.com", domain.ClassNetwork, false},
		{"task error", "", 1, "the tests failed", domain.ClassTaskError, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, p, _ := (&Adapter{}).Prepare(ports.HarnessInfo{}, ports.RunRequest{})
			if c.lines != "" {
				p.Line([]byte(c.lines))
			}
			end := p.Finish(c.exit, c.stderr, false, false)
			if end.Class != c.class || end.SignedOut != c.signedOut {
				t.Fatalf("got %s signedOut=%v, want %s %v (%s)", end.Class, end.SignedOut, c.class, c.signedOut, end.Error.Message)
			}
		})
	}
	_, p, _ := (&Adapter{}).Prepare(ports.HarnessInfo{}, ports.RunRequest{})
	if end := p.Finish(-1, "", true, false); end.Class != domain.ClassTimeout {
		t.Fatalf("timeout: %+v", end)
	}
}
