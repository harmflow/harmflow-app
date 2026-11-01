package integration

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// signInClaude is a stand-in Claude Code that signs in the way the real one does from its interactive session. Its
// probes read signed in once its credential's file exists; its sessions run as fake-claude's. Started with no
// arguments, it records the start in @SESSIONS@ and draws the screens Claude Code 2.1.309 drew: the question whether to
// trust its folder, asked once per folder, the session's header, /login's menu, and the browser's step, whose URL holds
// @STATE@. It then waits for the test to approve the sign-in by writing @APPROVED@, writes its credential, and says the
// sign-in succeeded. With @UNKNOWN@ present, it draws a screen the adapter does not know instead, holding @STATE@ too.
// The paths are part of the copy, so they need no variable, which the daemon's environment could drop.
const signInClaude = `#!/bin/bash
case "$1" in
  --version|--help|plugin) exec '@FAKE@' "$@" ;;
  auth)
    if [ -f '@CREDENTIAL@' ]; then echo '{"loggedIn": true}'; exit 0; fi
    echo '{"loggedIn": false, "authMethod": "none"}'
    exit 1 ;;
esac
if [ $# -gt 0 ]; then
  exec '@FAKE@' "$@"
fi
echo started >> '@SESSIONS@'
if [ -f '@UNKNOWN@' ]; then
  printf 'A screen of a newer CLI\r\nhttps://claude.com/cai/oauth/authorize?code=true&state=%s\r\n' '@STATE@'
  sleep 120
  exit 0
fi
if [ ! -f .trusted ]; then
  printf 'Quick\x1b[8Gsafety check: Is this a project you created or one you trust?\r\n\r\n'
  printf ' \xe2\x9d\xaf No, exit\r\n   Yes,\x1b[9GI\x1b[11Gtrust\x1b[17Gthis\x1b[22Gfolder\r\n'
  IFS= read -r answer
  [ "$answer" = $'\x1b[B' ] || exit 3
  : > .trusted
fi
printf ' \x1b[1mClaude\x1b[19GCode\x1b[24G\x1b[22mv2.1.309\r\n Not logged in \xc2\xb7 Run /login\r\n'
IFS= read -r command
[ "$command" = /login ] || exit 4
printf 'Select login method:\r\n\r\n \xe2\x9d\xaf 1. Claude account with subscription \xc2\xb7 Pro, Max, Team, or Enterprise\r\n'
IFS= read -r choice
[ -z "$choice" ] || exit 5
printf 'Opening browser to sign in\xe2\x80\xa6\r\nBrowser didn'"'"'t open? Use the url below to sign in\r\n'
printf 'https://claude.com/cai/oauth/authorize?code=true&state=%s\r\n\r\nPaste code here if prompted >\r\n' '@STATE@'
until [ -f '@APPROVED@' ]; do sleep 0.1; done
mkdir -p "$(dirname '@CREDENTIAL@')"
printf 'signed-in' > '@CREDENTIAL@'
printf ' Login successful. Press Enter to continue\xe2\x80\xa6\r\n'
sleep 120
`

// signInStandIn is a signInClaude written for one test: its path, the files through which the test approves the
// sign-in or makes the session show an unknown screen, the log of its sessions, and the state its URL holds.
type signInStandIn struct {
	path, approved, unknown, sessions, credential, state string
}

func newSignInStandIn(t *testing.T) signInStandIn {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	s := signInStandIn{path: filepath.Join(dir, "bin", "claude"), approved: filepath.Join(dir, "approved"),
		unknown: filepath.Join(dir, "unknown"), sessions: filepath.Join(dir, "sessions.log"),
		credential: filepath.Join(dir, "config", "credential"), state: "state" + hex.EncodeToString(random)}
	src := strings.NewReplacer("@FAKE@", filepath.Join(repoRoot(), "tests", "integration", "testdata", "fake-claude"),
		"@SESSIONS@", s.sessions, "@UNKNOWN@", s.unknown, "@APPROVED@", s.approved, "@CREDENTIAL@", s.credential,
		"@STATE@", s.state).Replace(signInClaude)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

// started counts the sessions the stand-in started.
func (s signInStandIn) started(t *testing.T) int {
	t.Helper()
	return strings.Count(string(readOrEmpty(t, s.sessions)), "started\n")
}

func (s signInStandIn) write(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitHarness polls GET /harnesses until the harness satisfies ok, and returns it.
func (d *daemon) waitHarness(id string, timeout time.Duration, ok func(map[string]any) bool) map[string]any {
	d.t.Helper()
	var h map[string]any
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if h = d.harness(id); ok(h) {
			return h
		}
	}
	d.t.Fatalf("GET /harnesses: %s is %v", id, h)
	return nil
}

// noticeTypes are the event types of the daemon's notices.
func (d *daemon) noticeTypes() []string {
	d.t.Helper()
	var out []string
	for _, n := range d.list("/notifications") {
		out = append(out, n["eventType"].(string))
	}
	return out
}

func containsAll(list []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, l := range list {
			found = found || l == w
		}
		if !found {
			return false
		}
	}
	return true
}

// harmflow-app#543: a Claude Code that says it is signed out signs in again through its own /login. The daemon starts
// the session by itself, answers the trust question and /login's menu, and waits at the browser's step, with a notice
// saying so; a check and the button meanwhile start no second session. Once the user approves, the CLI says it is
// signed in, its check agrees, and the harness is ready. No file under the daemon's home holds the URL's state.
func TestSignInThroughTheBrowser(t *testing.T) {
	t.Parallel()
	claude := newSignInStandIn(t)
	home := t.TempDir()
	d := startDaemon(t, home)
	if h := d.setHarness("claude-code", claude.path); h["authentication"] != "invalid" {
		t.Fatalf("PUT /harnesses/claude-code = authentication %v, want invalid", h["authentication"])
	}
	d.waitHarness("claude-code", 30*time.Second, func(h map[string]any) bool { return h["signIn"] == "running" })
	for deadline := time.Now().Add(30 * time.Second); claude.started(t) == 0 && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
	}

	d.call("POST", "/harnesses/refresh", nil, 200)
	if h := d.call("POST", "/harnesses/claude-code/sign-in", nil, 202); h["signIn"] != "running" {
		t.Errorf("POST /harnesses/claude-code/sign-in during a sign-in = %v, want the one running", h["signIn"])
	}
	time.Sleep(time.Second)
	if n := claude.started(t); n != 1 {
		t.Errorf("%d sessions after a check and the button during the sign-in, want one", n)
	}

	claude.write(t, claude.approved)
	h := d.waitHarness("claude-code", 60*time.Second, func(h map[string]any) bool {
		return h["readiness"] == "ready" && h["signIn"] == "available"
	})
	if h["authentication"] != "valid" {
		t.Errorf("after the sign-in, authentication %v, want valid", h["authentication"])
	}
	if got := d.noticeTypes(); !containsAll(got, "harness.sign_in_started", "harness.sign_in_completed") {
		t.Errorf("notices %v, want the sign-in's start and its end", got)
	}
	d.stop()
	noLeakInHome(t, homeFiles(t, home), map[string]string{"the sign-in URL's state": claude.state})
}

// harmflow-app#543: a sign-in nobody approves ends at its timeout. The harness stays not ready, saying the browser
// sign-in was not completed, and no session starts again by itself, however often the harness is checked; the button
// starts one.
func TestSignInTimesOutAndStartsAgainByHand(t *testing.T) {
	t.Parallel()
	claude := newSignInStandIn(t)
	d := startDaemon(t, t.TempDir(), "HARMFLOW_SIGN_IN_TIMEOUT=8s")
	d.setHarness("claude-code", claude.path)
	h := d.waitHarness("claude-code", 60*time.Second, func(h map[string]any) bool {
		return h["diagnosticKey"] == "harness_sign_in_not_completed"
	})
	if h["readiness"] != "not_ready" || h["signIn"] != "available" {
		t.Errorf("after the timeout, readiness %v and sign-in %v; want not_ready, available", h["readiness"], h["signIn"])
	}
	if got := d.noticeTypes(); !containsAll(got, "harness.sign_in_started", "harness.sign_in_timed_out") {
		t.Errorf("notices %v, want the sign-in's start and its timeout", got)
	}

	d.call("POST", "/harnesses/refresh", nil, 200)
	d.call("POST", "/harnesses/refresh", nil, 200)
	time.Sleep(time.Second)
	if n := claude.started(t); n != 1 {
		t.Fatalf("%d sessions after the timeout and two checks, want the first alone", n)
	}
	if h := d.call("POST", "/harnesses/claude-code/sign-in", nil, 202); h["signIn"] != "running" {
		t.Errorf("POST /harnesses/claude-code/sign-in = %v, want its sign-in running", h["signIn"])
	}
	for deadline := time.Now().Add(30 * time.Second); claude.started(t) < 2 && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
	}
	if n := claude.started(t); n != 2 {
		t.Errorf("%d sessions after the button, want a second", n)
	}
}

// harmflow-app#543: a screen the adapter does not recognize stops the sign-in. The harness keeps the diagnostic its
// check gives, a notice says the sign-in stopped, and no file under the daemon's home holds what the screen showed.
func TestSignInStopsAtAnUnknownScreen(t *testing.T) {
	t.Parallel()
	claude := newSignInStandIn(t)
	claude.write(t, claude.unknown)
	home := t.TempDir()
	d := startDaemon(t, home)
	d.setHarness("claude-code", claude.path)
	// The adapter waits 30 seconds for a screen it knows.
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if containsAll(d.noticeTypes(), "harness.sign_in_failed") {
			break
		}
	}
	if got := d.noticeTypes(); !containsAll(got, "harness.sign_in_failed") {
		t.Fatalf("notices %v, want the sign-in's stop", got)
	}
	if h := d.harness("claude-code"); h["diagnosticKey"] != "claude_not_signed_in" || h["signIn"] != "available" {
		t.Errorf("after an unknown screen, diagnostic %v and sign-in %v; want claude_not_signed_in, available",
			h["diagnosticKey"], h["signIn"])
	}
	d.stop()
	noLeakInHome(t, homeFiles(t, home), map[string]string{"the unknown screen's state": claude.state})
}

// harmflow-app#543, with the real Claude Code: signed out in a configuration directory of the test's own, it signs in
// again through its own /login, which a person approves in the browser it opens, within the sign-in's ten minutes.
// The test then signs that directory out, so the user's own sign-in stays as it is.
func TestSignInRealClaudeCodeThroughTheBrowser(t *testing.T) {
	installed := realClaude(t)
	config := t.TempDir()
	// A directory that has been through Claude Code's first run, which the sign-in does not drive.
	if err := os.WriteFile(filepath.Join(config, ".claude.json"),
		[]byte(`{"hasCompletedOnboarding": true, "theme": "dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := signedOutEnv(config)
	t.Cleanup(func() {
		logout := exec.Command(installed, "auth", "logout")
		logout.Env = daemonEnv(env)
		if out, err := logout.CombinedOutput(); err != nil {
			t.Errorf("signing the test's configuration directory out: %v\n%s", err, out)
		}
	})
	d := startDaemon(t, t.TempDir(), env...)
	// The daemon reports the path a link resolves to, and the installed claude is one.
	d.setHarness("claude-code", wrap(t, installed).path)
	t.Log("approve the Claude Code sign-in in the browser it opens")
	h := d.waitHarness("claude-code", 11*time.Minute, func(h map[string]any) bool {
		return h["readiness"] == "ready" || h["diagnosticKey"] == "harness_sign_in_not_completed"
	})
	if h["readiness"] != "ready" || h["authentication"] != "valid" {
		t.Errorf("after the sign-in, readiness %v, authentication %v, diagnostic %v; want ready, valid", h["readiness"],
			h["authentication"], h["diagnosticKey"])
	}
}
