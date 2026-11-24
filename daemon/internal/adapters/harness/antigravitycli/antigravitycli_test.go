package antigravitycli

import (
	"context"
	"encoding/json"
	"errors"
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

const helpText = "Usage of agy:\n  --add-dir\n  --effort\n  --input-format\n  --mode\n  --model\n  --output-format\n  --settings"

// standIn writes an executable named name in dir that prints version for --version and help to stderr for --help, as
// the CLI does, and records in dir/env what a credential variable and the self-update variable are set to.
func standIn(t *testing.T, dir, name, version, help string) string {
	t.Helper()
	script := `#!/bin/sh
echo "GEMINI_API_KEY=${GEMINI_API_KEY:-} AGY_CLI_DISABLE_AUTO_UPDATE=${AGY_CLI_DISABLE_AUTO_UPDATE:-}" > "` + dir + `/env"
case "$1" in
  --version) echo "` + version + `" ;;
  --help) printf '%s\n' "` + help + `" >&2 ;;
esac
`
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// signedIn is a SignedIn that answers ok, or err.
func signedIn(ok bool, err error) func() (bool, error) {
	return func() (bool, error) { return ok, err }
}

// Absent; signed out, a key in the environment notwithstanding; ready by the CLI's own sign-in; and ready with its
// sign-in unknown when it cannot be checked.
func TestDetectReadyByItsOwnSignInAlone(t *testing.T) {
	ctx := context.Background()
	empty := tempDir(t)

	absent := (&Adapter{Home: tempDir(t), PathEnv: empty, KnownLocations: []string{}}).Detect(ctx, "")
	if absent.Detected || absent.Readiness != "not_installed" || absent.DiagnosticKey != "antigravity_not_found" {
		t.Fatalf("Detect() with no agy = %+v, want not_installed, antigravity_not_found", absent)
	}

	dir := tempDir(t)
	standIn(t, dir, "agy", "1.5.3", helpText)
	a := &Adapter{Home: tempDir(t), PathEnv: dir, KnownLocations: []string{}, SignedIn: signedIn(false, nil),
		Env: []string{"PATH=/usr/bin:/bin", "GEMINI_API_KEY=secret-key", "AGY_CLI_DISABLE_AUTO_UPDATE=false"}}
	out := a.Detect(ctx, "")
	if !out.Detected || out.Version != "1.5.3" || out.Path != filepath.Join(dir, "agy") {
		t.Fatalf("Detect() signed out = %+v, want agy 1.5.3 detected", out)
	}
	if out.Authentication != "invalid" || out.Readiness != "not_ready" || out.DiagnosticKey != "antigravity_not_signed_in" {
		t.Fatalf("Detect() signed out with a key = %+v, want invalid, not_ready, antigravity_not_signed_in", out)
	}
	if env, _ := os.ReadFile(filepath.Join(dir, "env")); strings.TrimSpace(string(env)) != "GEMINI_API_KEY= AGY_CLI_DISABLE_AUTO_UPDATE=true" {
		t.Fatalf("the probe's environment = %q, want no key and the self-update off", env)
	}

	a.SignedIn = signedIn(true, nil)
	if out := a.Detect(ctx, ""); out.Authentication != "valid" || !out.Ready() {
		t.Fatalf("Detect() signed in = %+v, want valid and ready", out)
	}
	if out := a.Detect(ctx, ""); !slices.Equal(out.Efforts, efforts) {
		t.Errorf("Detect().Efforts = %v, want %v", out.Efforts, efforts)
	}

	unverified := []struct {
		name  string
		check func() (bool, error)
	}{
		{"no check", nil},
		{"a check that fails", signedIn(false, errors.New("locked"))},
	}
	for _, c := range unverified {
		t.Run(c.name, func(t *testing.T) {
			a.SignedIn = c.check
			if out := a.Detect(ctx, ""); out.Authentication != "unknown" || !out.Ready() || out.DiagnosticKey != "authentication_unverified" {
				t.Errorf("Detect() with %s = %+v, want unknown, ready, authentication_unverified", c.name, out)
			}
		})
	}
}

// A version whose help lacks a flag HarmFlow needs is not ready.
func TestDetectNeedsItsFlags(t *testing.T) {
	dir := tempDir(t)
	standIn(t, dir, "agy", "1.4.0", "Usage of agy:\n  --input-format\n  --mode\n  --model\n  --output-format")
	out := (&Adapter{Home: tempDir(t), PathEnv: dir, KnownLocations: []string{}, SignedIn: signedIn(true, nil)}).Detect(context.Background(), "")
	if out.Readiness != "not_ready" || out.VersionSupport != "below_minimum" || out.DiagnosticKey != "harness_feature_missing" {
		t.Fatalf("Detect() without --settings = %+v, want not_ready, below_minimum, harness_feature_missing", out)
	}
}

// A file is the Antigravity CLI when it is named agy and its --version output is a version number.
func TestIdentifyByNameAndVersionShape(t *testing.T) {
	ctx := context.Background()
	a := &Adapter{Home: tempDir(t)}
	cases := []struct {
		name, file, version string
		ok                  bool
	}{
		{"named agy with a version", "agy", "1.5.3", true},
		{"a pre-release", "agy", "1.6.0-preview.1", true},
		{"another name", "gemini", "1.5.3", false},
		{"a version that names another tool", "agy", "2.1.0 (Claude Code)", false},
		{"no version", "agy", "hello", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := standIn(t, tempDir(t), c.file, c.version, helpText)
			if err := a.Identify(ctx, path); (err == nil) != c.ok {
				t.Fatalf("Identify(%s, %q) = %v, want ok %v", c.file, c.version, err, c.ok)
			}
		})
	}
}

// refusal is the key of the refusal err carries, or "nil" when there is none.
func refusal(err error) string {
	if err == nil {
		return "nil"
	}
	return domain.TextOf(err).Key
}

// profileCase is a model and an effort, and what ValidateProfile answers them: nil, or its refusal's key.
type profileCase struct {
	name, model, effort, want string
}

// Every model the CLI may name and every effort it takes are accepted; anything else is refused with its own key.
func TestValidateProfile(t *testing.T) {
	a := &Adapter{}
	cases := []profileCase{
		{"no model", "", "", "nil"},
		{"the default model", domain.DefaultModel, "", "nil"},
		{"a Gemini model", "gemini-3.8-flash-medium", "", "nil"},
		{"a Claude model", "claude-sonnet-4-6", "", "nil"},
		{"a display name", "Gemini Pro", "", "antigravity_model_unknown"},
		{"an effort it lacks", "", "extreme", "antigravity_effort_invalid"},
	}
	for _, e := range efforts {
		cases = append(cases, profileCase{"effort " + e, "", e, "nil"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := a.ValidateProfile(ports.HarnessInfo{}, c.model, c.effort); refusal(err) != c.want {
				t.Errorf("ValidateProfile(%q, %q) = %v, want %s", c.model, c.effort, err, c.want)
			}
		})
	}
}

func boolp(b bool) *bool { return &b }

// settingsOf is the --settings a run was prepared with.
func settingsOf(t *testing.T, args []string) sessionSettings {
	t.Helper()
	i := slices.Index(args, "--settings")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("args %q have no --settings", args)
	}
	var s sessionSettings
	if err := json.Unmarshal([]byte(args[i+1]), &s); err != nil {
		t.Fatalf("--settings %q: %v", args[i+1], err)
	}
	return s
}

// restrictionKeys is the key of each restriction's warning, by the restriction's name.
var restrictionKeys = map[string]string{
	"tool_allowlist":      "restriction_antigravity_tool_lists",
	"readablePaths":       "restriction_antigravity_read_paths",
	"restrictToWorkspace": "restriction_antigravity_workspace",
	"network":             "restriction_antigravity_network",
}

// unenforcedNames is the names of the restrictions warned as not enforced, each with the key of its own text and no
// param.
func unenforcedNames(rs []ports.Restriction) map[string]bool {
	names := map[string]bool{}
	for _, r := range rs {
		if !r.Enforced && r.Text.Key == restrictionKeys[r.Name] && len(r.Text.Params) == 0 {
			names[r.Name] = true
		}
	}
	return names
}

// The run reads one stream-json user message with HarmFlow's context in front of the prompt, writes stream-json, pins
// its mode, adds the extra folders to the workspace, runs with no credential variable and no self-update, and turns the
// profile into rules for that session; what a command may reach is reported as not enforced.
func TestPrepare(t *testing.T) {
	a := &Adapter{}
	info := ports.HarnessInfo{Path: "/bin/agy", Features: map[string]bool{"--add-dir": true, "--effort": true}}
	req := ports.RunRequest{Prompt: "do it", Context: "ctx", Model: "gemini-3.8-flash-high", Effort: "high", WorkDir: "/w",
		AdditionalDirs: []string{"/extra"}, ExecutionDir: "/exec",
		Env: []string{"A=1", "GEMINI_API_KEY=k", "AGY_ADC_AUTH=true", "AGY_CLI_DISABLE_AUTO_UPDATE=false"},
		Permissions: domain.Permissions{Profile: domain.ProfileWorkspaceWrite, RestrictToWorkspace: boolp(true),
			Network: domain.NetworkDeny}}
	inv, parser, err := a.Prepare(info, req)
	if err != nil || parser == nil {
		t.Fatalf("Prepare() = %v", err)
	}
	args := strings.Join(inv.Args, " ")
	for _, want := range []string{"--input-format stream-json", "--output-format stream-json", "--mode accept-edits",
		"--model gemini-3.8-flash-high", "--effort high", "--add-dir /extra", "--add-dir /exec"} {
		if !strings.Contains(args, want) {
			t.Errorf("Prepare() args %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "dangerously") || strings.Contains(args, "-p") {
		t.Errorf("Prepare() args %q approve every tool or put the prompt in the arguments", args)
	}
	var msg userMessage
	if err := json.Unmarshal([]byte(inv.Stdin), &msg); err != nil || msg.Event != "user" || msg.Message.Content != "ctx\n\ndo it" ||
		!strings.HasSuffix(inv.Stdin, "\n") {
		t.Errorf("Prepare() stdin = %q (%v), want one user line holding ctx and the prompt", inv.Stdin, err)
	}
	if inv.Dir != "/w" {
		t.Errorf("Prepare() dir = %q, want /w", inv.Dir)
	}
	for _, kv := range []string{"GEMINI_API_KEY=k", "AGY_ADC_AUTH=true", "AGY_CLI_DISABLE_AUTO_UPDATE=false"} {
		if slices.Contains(inv.Env, kv) {
			t.Errorf("Prepare() env = %q, carries %s", inv.Env, kv)
		}
	}
	if !slices.Contains(inv.Env, "AGY_CLI_DISABLE_AUTO_UPDATE=true") || !slices.Contains(inv.Env, "A=1") {
		t.Errorf("Prepare() env = %q, want A=1 and the self-update off", inv.Env)
	}
	s := settingsOf(t, inv.Args)
	if s.AllowNonWorkspaceAccess || !slices.Equal(s.Permissions.Allow, []string{"command(*)"}) ||
		!slices.Equal(s.Permissions.Deny, []string{"read_url(*)", "execute_url(*)"}) {
		t.Errorf("workspace_write settings = %+v, want commands allowed, URLs denied, files kept to the workspace", s)
	}
	if names := unenforcedNames(inv.Restrictions); !names["restrictToWorkspace"] || !names["network"] || len(names) != 2 {
		t.Errorf("workspace_write restrictions = %v, want restrictToWorkspace and network", names)
	}
}

// read_only denies every file write and every command, so no allow rule of the user's own lifts either: nothing is
// left to warn about.
func TestPrepareReadOnly(t *testing.T) {
	inv, _, _ := (&Adapter{}).Prepare(ports.HarnessInfo{}, ports.RunRequest{WorkDir: "/w",
		Permissions: domain.Permissions{Profile: domain.ProfileReadOnly, Network: domain.NetworkDeny}})
	s := settingsOf(t, inv.Args)
	if len(s.Permissions.Allow) != 0 || !slices.Equal(s.Permissions.Deny, []string{"write_file(*)", "command(*)", "read_url(*)", "execute_url(*)"}) {
		t.Errorf("read_only settings = %+v, want writes, commands, and URLs denied", s)
	}
	if len(inv.Restrictions) != 0 {
		t.Errorf("read_only restrictions = %+v, want none", inv.Restrictions)
	}
}

// A custom profile's program lists become command rules, its writable paths are added to the workspace, and its tool
// lists and read paths are reported as not enforced.
func TestPrepareCustom(t *testing.T) {
	info := ports.HarnessInfo{Features: map[string]bool{"--add-dir": true}}
	inv, _, _ := (&Adapter{}).Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: domain.Permissions{
		Profile: domain.ProfileCustom, Custom: &domain.CustomPermissions{AllowedTools: []string{"Read"},
			AllowedPrograms: []string{"git", "go"}, DeniedPrograms: []string{"rm"}, WritablePaths: []string{"/out"},
			ReadablePaths: []string{"/r"}}}})
	s := settingsOf(t, inv.Args)
	if !slices.Equal(s.Permissions.Allow, []string{"command(git)", "command(go)"}) || !slices.Equal(s.Permissions.Deny, []string{"command(rm)"}) {
		t.Errorf("custom settings = %+v, want git and go allowed, rm denied", s)
	}
	if !strings.Contains(strings.Join(inv.Args, " "), "--add-dir /out") {
		t.Errorf("custom args %q do not add the writable path", inv.Args)
	}
	if names := unenforcedNames(inv.Restrictions); !names["tool_allowlist"] || !names["readablePaths"] || names["program_lists"] {
		t.Errorf("custom restrictions = %v, want tool_allowlist and readablePaths alone", names)
	}

	inv, _, _ = (&Adapter{}).Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: domain.Permissions{
		Profile: domain.ProfileCustom, Custom: &domain.CustomPermissions{DeniedPrograms: []string{"rm"}}}})
	if s := settingsOf(t, inv.Args); !slices.Equal(s.Permissions.Allow, []string{"command(*)"}) {
		t.Errorf("custom with a denied program alone: settings %+v, want every other command allowed", s)
	}
}

// stream is a real run's output from the Antigravity CLI 1.5.3, its paths and identifiers replaced: an answer, a file
// written, a write outside the workspace refused, a command run, and the result.
const stream = `{"event":"init","conversation_id":"c-1","init":{"cwd":"/w","tools":["run_command","write_file"],"permission_mode":"request-review"}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":0,"state":"DONE","step_type":"user_input"}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"Looking"}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":1,"state":"DONE","step_type":"agent_response","text_delta":" now.","duration_seconds":2.3}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"write_file","tool_info":{"name":"write_file","parameters":{"Path":"/w/probe.txt"}}}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":2,"state":"DONE","step_type":"tool","tool_name":"write_file","duration_seconds":0.97,"tool_info":{"name":"write_file","parameters":{"Path":"/w/probe.txt"}}}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":3,"state":"ACTIVE","step_type":"tool","tool_name":"write_file","tool_info":{"name":"write_file","parameters":{"Path":"/outside/x.txt"}}}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":3,"state":"ERROR","step_type":"tool","tool_name":"write_file","duration_seconds":0.43,"tool_info":{"name":"write_file","parameters":{"Path":"/outside/x.txt"},"error":{"type":"TOOL_ERROR","message":"permission check failed for write_file \"/outside/x.txt\": user denied permission for write_file(/outside/x.txt)"}}}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":4,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"ls -1"}}}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":4,"state":"DONE","step_type":"tool","tool_name":"run_command","duration_seconds":0.29,"tool_info":{"name":"run_command","parameters":{"CommandLine":"ls -1"},"output":"Command exited with code 0.\n\nprobe.txt"}}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":5,"state":"DONE","step_type":"agent_response","text_delta":"Done.","usage":{"input_tokens":17184,"output_tokens":26,"thinking_tokens":25,"cache_read_tokens":0,"total_tokens":17210}}}
{"event":"result","result":{"conversation_id":"c-1","status":"SUCCESS","response":"Looking now.\n\nDone.\n","duration_seconds":9.1,"num_turns":1,"usage":{"input_tokens":17184,"output_tokens":26,"thinking_tokens":25,"cache_read_tokens":0,"total_tokens":17210}}}`

// The stream's lines become the event kinds the other harnesses produce.
func TestParserNormalizesTheStream(t *testing.T) {
	_, p, _ := (&Adapter{}).Prepare(ports.HarnessInfo{}, ports.RunRequest{})
	var events []ports.HarnessEvent
	for _, l := range strings.Split(stream, "\n") {
		events = append(events, p.Line([]byte(l))...)
	}
	var kinds []ports.HarnessEventKind
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	want := []ports.HarnessEventKind{ports.HarnessStarted, ports.HarnessMessage, ports.HarnessToolStarted,
		ports.HarnessToolDone, ports.HarnessToolStarted, ports.HarnessToolDone, ports.HarnessToolStarted,
		ports.HarnessToolDone, ports.HarnessMessage}
	if !slices.Equal(kinds, want) {
		t.Fatalf("Line(stream) kinds = %v, want %v", kinds, want)
	}
	if got := events[0].SessionID; got != "c-1" {
		t.Errorf("Line(stream)[0].SessionID = %q, want c-1", got)
	}
	if got := events[1].Message; got != "Looking now." {
		t.Errorf("Line(stream)[1].Message = %q, want %q", got, "Looking now.")
	}
	if got := events[2].Path; got != "/w/probe.txt" {
		t.Errorf("Line(stream)[2].Path = %q, want /w/probe.txt", got)
	}
	if events[3].IsError || !events[5].IsError {
		t.Errorf("Line(stream) IsError of the file written and of the write refused = %v, %v; want false, true",
			events[3].IsError, events[5].IsError)
	}
	if got := events[5].Output; !strings.Contains(got, "user denied permission") {
		t.Errorf("Line(stream)[5].Output = %q, want the refusal's user denied permission", got)
	}
	if got := events[6].Command; got != "ls -1" {
		t.Errorf("Line(stream)[6].Command = %q, want ls -1", got)
	}
	if got := events[7]; got.Tool != "run_command" || !strings.Contains(got.Output, "probe.txt") {
		t.Errorf("Line(stream)[7] = tool %q, output %q; want run_command, its output listing probe.txt", got.Tool, got.Output)
	}
	if got := events[8].Message; got != "Done." {
		t.Errorf("Line(stream)[8].Message = %q, want Done.", got)
	}
	end := p.Finish(0, "", false, false)
	if end.Class != "" || end.FinalText != "Done." || end.SessionID != "c-1" {
		t.Fatalf("Finish(0) = %+v, want completed with the last answer", end)
	}
}

// The Antigravity CLI 1.5.3's own result and stderr when its signed-in account is not eligible for it.
const (
	ineligibleResult = `{"event":"result","result":{"conversation_id":"","status":"ERROR","response":"","error":"Eligibility check failed: Your current account is not eligible for Antigravity. Try signing in with another personal Google account. If you believe this is an error, please contact your administrator.","duration_seconds":0,"num_turns":0,"finished_reason":"error"}}`
	ineligibleStderr = "error: Eligibility check failed: Your current account is not eligible for Antigravity. Try signing in with another personal Google account. If you believe this is an error, please contact your administrator."
	// deniedStderr is its notice when headless mode refused an action no rule allowed; the run then exits 0 with no
	// answer.
	deniedStderr = `jetski: no output produced — a tool required the "command" permission that headless mode cannot prompt for, so it was auto-denied. Add an allow-rule under permissions.allow in settings.json (e.g. command(<target>)). Alternatively, re-run with --dangerously-skip-permissions to auto-approve all tools.`
	emptySuccess = `{"event":"result","result":{"conversation_id":"c-1","status":"SUCCESS","response":"","duration_seconds":4,"num_turns":1}}`
)

// Each failure class, from the CLI's result and its stderr.
func TestFinishClassifiesFailures(t *testing.T) {
	cases := []struct {
		name      string
		lines     string
		exit      int
		stderr    string
		class     domain.ErrorClass
		signedOut bool
	}{
		{"ineligible account", ineligibleResult, 1, ineligibleStderr, domain.ClassHarnessUnavailable, false},
		{"signed out", `{"event":"result","result":{"status":"ERROR","error":"authentication required"}}`, 1,
			"error getting token source: You are not logged into Antigravity.", domain.ClassHarnessUnavailable, true},
		{"keyring locked", "", 1, "Error: failed to retrieve token: secret keyring is locked", domain.ClassHarnessUnavailable, true},
		{"quota", `{"event":"result","result":{"status":"ERROR","error":"You have exhausted your weekly quota for Gemini Flash"}}`, 1, "",
			domain.ClassQuotaExhausted, false},
		{"rate limit", "", 1, "429 Too Many Requests", domain.ClassRateLimited, false},
		{"network", "", 1, "dial tcp: lookup antigravity.google: no such host", domain.ClassNetwork, false},
		{"an action headless mode refused", emptySuccess, 0, deniedStderr, domain.ClassTaskError, false},
		{"an error result with no text", `{"event":"result","result":{"status":"ERROR"}}`, 1, "", domain.ClassTaskError, false},
		{"exit 0 with no result", "", 0, "", domain.ClassTaskError, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, p, _ := (&Adapter{}).Prepare(ports.HarnessInfo{}, ports.RunRequest{})
			if c.lines != "" {
				p.Line([]byte(c.lines))
			}
			end := p.Finish(c.exit, c.stderr, false, false)
			if end.Class != c.class || end.SignedOut != c.signedOut {
				t.Fatalf("Finish(%d, %q) = %s signedOut=%v (%s), want %s %v", c.exit, c.stderr, end.Class, end.SignedOut,
					end.Error.Message, c.class, c.signedOut)
			}
		})
	}
	_, p, _ := (&Adapter{}).Prepare(ports.HarnessInfo{}, ports.RunRequest{})
	if end := p.Finish(-1, "", true, false); end.Class != domain.ClassTimeout {
		t.Errorf("Finish timed out = %+v, want timeout", end)
	}
	if end := p.Finish(-1, "", false, true); end.Class != domain.ClassCancelled {
		t.Errorf("Finish cancelled = %+v, want cancelled", end)
	}
}

// A refused action that still let the CLI answer completes; the refusal shows in its tool's failed event.
func TestFinishKeepsAnAnswerDespiteARefusal(t *testing.T) {
	_, p, _ := (&Adapter{}).Prepare(ports.HarnessInfo{}, ports.RunRequest{})
	p.Line([]byte(`{"event":"result","result":{"conversation_id":"c-1","status":"SUCCESS","response":"Step 3 failed: permission denied."}}`))
	if end := p.Finish(0, deniedStderr, false, false); end.Class != "" || end.FinalText != "Step 3 failed: permission denied." {
		t.Errorf("Finish(0) with an answer = %+v, want completed", end)
	}
}

// The ineligible account's error is the CLI's sentence once, not repeated from its result and its stderr.
func TestFinishSaysTheErrorOnce(t *testing.T) {
	_, p, _ := (&Adapter{}).Prepare(ports.HarnessInfo{}, ports.RunRequest{})
	p.Line([]byte(ineligibleResult))
	msg := p.Finish(1, ineligibleStderr, false, false).Error.Message
	if strings.Count(msg, "Eligibility check failed") != 1 {
		t.Errorf("Finish error = %q, want the CLI's sentence once", msg)
	}
}

// An added folder given through a symbolic link is passed by its real path, as the CLI refuses every write into a
// folder it was given through a link; the workspace itself is not added again under either path.
func TestPrepareAddsFoldersByTheirRealPaths(t *testing.T) {
	real, work := tempDir(t), tempDir(t)
	link := filepath.Join(tempDir(t), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	workLink := filepath.Join(tempDir(t), "work-link")
	if err := os.Symlink(work, workLink); err != nil {
		t.Fatal(err)
	}
	info := ports.HarnessInfo{Features: map[string]bool{"--add-dir": true}}
	inv, _, _ := (&Adapter{}).Prepare(info, ports.RunRequest{WorkDir: work, AdditionalDirs: []string{link, workLink, "/not/there"}})
	var added []string
	for i, a := range inv.Args {
		if a == "--add-dir" {
			added = append(added, inv.Args[i+1])
		}
	}
	if want := []string{real, "/not/there"}; !slices.Equal(added, want) {
		t.Errorf("Prepare() --add-dir = %q, want %q", added, want)
	}
}
