package integration

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tokenClaude is a stand-in Claude Code with no sign-in of its own: its probe reads signed in, and its sessions run as
// fake-claude's, only with the token @ACCEPTED@ holds in CLAUDE_CODE_OAUTH_TOKEN. With another token, a session fails
// as the real CLI does with a token it rejects. Started with no arguments, as the daemon's sign-in starts it, it waits.
// The paths are part of the copy, so they need no variable, which the daemon's environment could drop.
const tokenClaude = `#!/bin/bash
ACCEPTED=$(< '@ACCEPTED@')
case "$1" in
  --version|--help|plugin) exec '@FAKE@' "$@" ;;
  auth)
    if [ -n "$CLAUDE_CODE_OAUTH_TOKEN" ]; then echo '{"loggedIn": true, "authMethod": "oauth_token"}'; exit 0; fi
    echo '{"loggedIn": false, "authMethod": "none"}'
    exit 1 ;;
esac
if [ $# -eq 0 ]; then
  sleep 120
  exit 0
fi
[ "$1 $2" = "-p /usage" ] && exec '@FAKE@' "$@"
if [ -z "$CLAUDE_CODE_OAUTH_TOKEN" ] || [ "$CLAUDE_CODE_OAUTH_TOKEN" != "$ACCEPTED" ]; then
  echo "Failed to authenticate. API Error: 401 OAuth access token is invalid." >&2
  exit 1
fi
exec '@FAKE@' "$@"
`

// tokenStandIn is a tokenClaude written for one test: its path, and the file that holds the token it accepts.
type tokenStandIn struct {
	path, accepted string
}

func newTokenStandIn(t *testing.T) tokenStandIn {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := tokenStandIn{path: filepath.Join(dir, "bin", "claude"), accepted: filepath.Join(dir, "accepted")}
	src := strings.NewReplacer("@FAKE@", filepath.Join(repoRoot(), "tests", "integration", "testdata", "fake-claude"),
		"@ACCEPTED@", s.accepted).Replace(tokenClaude)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	s.accept(t, "")
	return s
}

// accept makes the stand-in accept token, and reject every other.
func (s tokenStandIn) accept(t *testing.T, token string) {
	t.Helper()
	if err := os.WriteFile(s.accepted, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newTestToken is a token in the form `claude setup-token` prints, built here, so no token-shaped literal is in the
// repository.
func newTestToken(t *testing.T) string {
	t.Helper()
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	return "sk-ant-oat01-" + hex.EncodeToString(random)
}

// harnessToken is a harness's token as the inventory shows it, nil for none.
func harnessToken(h map[string]any) map[string]any {
	token, _ := h["token"].(map[string]any)
	return token
}

// keychainItem is the harness's token item in keychain, and whether there is one.
func keychainItem(t *testing.T, keychain, harness string) (string, bool) {
	t.Helper()
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", "com.harmflow.harness-token", "-a",
		harness, "-w", keychain).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// harmflow-app#733: a long-lived token goes to the Keychain alone. Claude Code's checks and sessions run with it, after
// a restart too, and a task completes on it; once removed, Claude Code is back on its own sign-in. A token Claude Code
// rejects is no longer used and a notice says so. No file under the daemon's home holds a piece of the token, nor does
// an answer of the API.
func TestHarnessTokenThroughTheKeychain(t *testing.T) {
	keychain := throwawayKeychain(t)
	home := t.TempDir()
	claude := newTokenStandIn(t)
	token := newTestToken(t)
	claude.accept(t, token)
	d := startDaemonOn(t, home, keychain, "HARMFLOW_SIGN_IN_TIMEOUT=2s")
	if h := d.setHarness("claude-code", claude.path); h["authentication"] != "invalid" || h["takesToken"] != true {
		t.Fatalf("PUT /harnesses/claude-code = authentication %v, takesToken %v; want invalid, true", h["authentication"],
			h["takesToken"])
	}

	status, body := d.request("PUT", "/harnesses/claude-code/token", map[string]any{"token": token}, nil)
	var h map[string]any
	if status != 200 || json.Unmarshal(body, &h) != nil {
		t.Fatalf("PUT /harnesses/claude-code/token = %d", status)
	}
	if strings.Contains(string(body), token) {
		t.Error("PUT /harnesses/claude-code/token answered with the token")
	}
	if got := harnessToken(h); h["readiness"] != "ready" || got == nil || got["rejected"] != false || got["savedAt"] == nil {
		t.Errorf("after the token, readiness %v and token %v; want ready, saved, not rejected", h["readiness"], got)
	}
	if got, ok := keychainItem(t, keychain, "claude-code"); !ok || got != token {
		t.Error("the Keychain holds no item with the token")
	}
	if x := d.runOn("ontoken", "claude-code"); x["status"] != "completed" {
		t.Errorf("a task on the token: %v (%v), want completed", x["status"], x["errorMessage"])
	}

	d.stop()
	d = startDaemonOn(t, home, keychain, "HARMFLOW_SIGN_IN_TIMEOUT=2s")
	if h := d.setHarness("claude-code", claude.path); h["readiness"] != "ready" || harnessToken(h) == nil {
		t.Errorf("after a restart, readiness %v and token %v; want ready on the saved token", h["readiness"], harnessToken(h))
	}

	h = d.call("DELETE", "/harnesses/claude-code/token", nil, 200)
	if harnessToken(h) != nil || h["authentication"] != "invalid" {
		t.Errorf("after DELETE, token %v and authentication %v; want none, invalid", harnessToken(h), h["authentication"])
	}
	if _, ok := keychainItem(t, keychain, "claude-code"); ok {
		t.Error("the Keychain still holds the token's item after DELETE")
	}

	d.call("PUT", "/harnesses/claude-code/token", map[string]any{"token": token}, 200)
	claude.accept(t, newTestToken(t))
	if x := d.runOn("rejected", "claude-code"); x["status"] != "failed" {
		t.Errorf("a task on a rejected token: %v, want failed", x["status"])
	}
	h = d.waitHarness("claude-code", runtimeWait, func(h map[string]any) bool {
		got := harnessToken(h)
		return got != nil && got["rejected"] == true
	})
	if h["authentication"] != "invalid" {
		t.Errorf("after the rejection, authentication %v, want invalid on Claude Code's own sign-in", h["authentication"])
	}
	if got := d.noticeTypes(); !containsAll(got, "harness.token_rejected") {
		t.Errorf("notices %v, want the token's rejection", got)
	}
	d.stop()
	noLeakInHome(t, homeFiles(t, home), map[string]string{"the token": token})
}

// harmflow-app#733: a daemon that keeps its secrets in a file keeps no token, and says so.
func TestHarnessTokenNeedsACredentialStore(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	if h := d.harness("claude-code"); h["takesToken"] != nil {
		t.Errorf("GET /harnesses: takesToken %v with the file store, want none", h["takesToken"])
	}
	status, body := d.request("PUT", "/harnesses/claude-code/token", map[string]any{"token": newTestToken(t)}, nil)
	if status != 409 || !strings.Contains(string(body), "harness_token_store_unavailable") {
		t.Errorf("PUT /harnesses/claude-code/token with the file store = %d %s, want 409 harness_token_store_unavailable",
			status, body)
	}
}

// harmflow-app#733, with the real Claude Code: signed out in a configuration directory of the test's own, it runs a
// task on the long-lived token HARMFLOW_TEST_CLAUDE_TOKEN holds, which the daemon keeps in a throwaway keychain. The
// token comes from the person running the test, never from the repository, and no file under the daemon's home holds a
// piece of it afterwards.
func TestHarnessTokenRealClaudeCode(t *testing.T) {
	installed := realClaude(t)
	token := realClaudeToken(t)
	keychain := throwawayKeychain(t)
	config := t.TempDir()
	if err := os.WriteFile(filepath.Join(config, ".claude.json"),
		[]byte(`{"hasCompletedOnboarding": true, "theme": "dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	// The sign-in the signed-out Claude Code starts ends before its first key, so no browser opens.
	d := startDaemonOn(t, home, keychain, append(signedOutEnv(config), "HARMFLOW_SIGN_IN_TIMEOUT=1s")...)
	d.setHarness("claude-code", wrap(t, installed).path)
	h := d.call("PUT", "/harnesses/claude-code/token", map[string]any{"token": token}, 200)
	if h["readiness"] != "ready" || h["authentication"] != "valid" {
		t.Errorf("after the token, readiness %v and authentication %v; want ready, valid", h["readiness"], h["authentication"])
	}
	if x := d.runOn("realtoken", "claude-code"); x["status"] != "completed" {
		t.Errorf("a task on the token: %v (%v), want completed", x["status"], x["errorMessage"])
	}
	d.stop()
	noLeakInHome(t, homeFiles(t, home), map[string]string{"the token": token})
}

// realClaudeTokenEnv holds the long-lived token TestHarnessTokenRealClaudeCode runs on, which `claude setup-token`
// prints; the person running the test sets it, and nothing in the repository holds one.
const realClaudeTokenEnv = "HARMFLOW_TEST_CLAUDE_TOKEN"

// realClaudeToken is the token realClaudeTokenEnv holds; it skips the test when the variable is unset, as an opt-in.
func realClaudeToken(t *testing.T) string {
	t.Helper()
	token := os.Getenv(realClaudeTokenEnv)
	if token == "" {
		t.Skipf("runs on a long-lived Claude Code token: set %s to the one `claude setup-token` printed", realClaudeTokenEnv)
	}
	return token
}
