package integration

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// signedIn is a stand-in CLI signed in on its own: it reads its credential from a file in a configuration folder of
// its own, which HarmFlow never writes, and records the arguments and the environment of each invocation.
type signedIn struct {
	path       string // the CLI
	credential string // the credential's file
	record     string // the arguments and the environment of each invocation, each after a line @@
	secret     string // the credential, built when the test runs
}

// The stand-ins' scripts. Each records its invocation before it reads its credential, and never exports it, so the
// record holds only what it was started with. Without its credential's file each one reads signed out, as the real CLI
// does; with it, each prints the credential on its error stream and in its output, where a run keeps what it printed.
// The paths are part of the copy, so they need no variable, which a run's environment could drop.
const (
	// signedInClaude wraps fake-claude. A session whose prompt holds RUN_GH=1 runs gh from its PATH, as a GitHub task's
	// session does, and reports what gh printed as the output of a Bash command.
	signedInClaude = `#!/bin/bash
{ echo "@@"; printf '%s\n' "$@"; env; } >> '@RECORD@'
case "$1" in
  --version|--help|plugin) exec '@FAKE@' "$@" ;;
esac
if [ ! -f '@CREDENTIAL@' ]; then
  [ "$1" = auth ] && echo '{"loggedIn": false, "authMethod": "none"}'
  echo "Not logged in · Please run /login" >&2
  exit 1
fi
CREDENTIAL=$(< '@CREDENTIAL@')
echo "claude-code signed in with $CREDENTIAL" >&2
if [ "$1" = auth ]; then
  echo '{"loggedIn": true, "token": "'"$CREDENTIAL"'"}'
  exit 0
fi
[ "$1 $2" = "-p /usage" ] && exec '@FAKE@' "$@"
IFS= read -r -d '' PROMPT
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"claude-code signed in with '"$CREDENTIAL"'"}]}}'
if [[ "$PROMPT" == *"RUN_GH=1"* ]]; then
  OUT=$(gh auth status 2>&1) || { echo "gh auth status failed: $OUT" >&2; exit 1; }
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"g1","name":"Bash","input":{"command":"gh auth status","description":"Check the GitHub CLI"}}]}}'
  echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"g1","content":"'"${OUT//$'\n'/ }"'","is_error":false}]}}'
fi
printf '%s' "$PROMPT" | '@FAKE@' "$@"
`
	// signedInCodex wraps fake-codex.
	signedInCodex = `#!/bin/bash
{ echo "@@"; printf '%s\n' "$@"; env; } >> '@RECORD@'
SESSION=
[ "$1" = exec ] && [ "$2" != --help ] && SESSION=1
if [ ! -f '@CREDENTIAL@' ]; then
  if [ "$1" = login ] || [ -n "$SESSION" ]; then
    echo "Not logged in" >&2
    exit 1
  fi
  exec '@FAKE@' "$@"
fi
CREDENTIAL=$(< '@CREDENTIAL@')
if [ "$1" = login ]; then
  echo "Logged in using an API key - $CREDENTIAL"
  echo "codex signed in with $CREDENTIAL" >&2
  exit 0
fi
if [ -n "$SESSION" ]; then
  echo "codex signed in with $CREDENTIAL" >&2
  echo '{"type":"item.completed","item":{"id":"c0","type":"agent_message","text":"codex signed in with '"$CREDENTIAL"'"}}'
fi
exec '@FAKE@' "$@"
`
	// signedInGh stands in for the GitHub CLI.
	signedInGh = `#!/bin/bash
{ echo "@@"; printf '%s\n' "$@"; env; } >> '@RECORD@'
if [ "$1" = --version ]; then
  echo "gh version 2.83.0 (2026-10-01)"
  exit 0
fi
if [ ! -f '@CREDENTIAL@' ]; then
  echo "You are not logged into any GitHub hosts. To log in, run: gh auth login" >&2
  exit 1
fi
CREDENTIAL=$(< '@CREDENTIAL@')
echo "github.com: logged in with $CREDENTIAL"
echo "gh signed in with $CREDENTIAL" >&2
`
)

// newSignedIn writes a stand-in CLI named name from script, in a temporary folder of its own, with fake as the
// stand-in it wraps, and a credential of prefix followed by random characters, which it signs in with once signIn
// writes its file. The credential is built here, so no token-shaped literal is in the repository.
func newSignedIn(t *testing.T, name, script, fake, prefix string) signedIn {
	t.Helper()
	// The path the daemon reports is the real one: on macOS the temporary directories are under a symlink.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	s := signedIn{path: filepath.Join(dir, "bin", name), credential: filepath.Join(dir, "config", "credential"),
		record: filepath.Join(dir, "invocations.log"), secret: prefix + hex.EncodeToString(random)}
	src := strings.NewReplacer("@RECORD@", s.record, "@CREDENTIAL@", s.credential,
		"@FAKE@", filepath.Join(repoRoot(), "tests", "integration", "testdata", fake)).Replace(script)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

// signIn writes the stand-in's credential into its own configuration folder.
func (s signedIn) signIn(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.credential), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.credential, []byte(s.secret), 0o600); err != nil {
		t.Fatal(err)
	}
}

// leakedPieces names where data holds the secret or a piece of it of eight characters or more, by the piece's offset
// in the secret, never by the piece itself: every longer piece holds one of eight.
func leakedPieces(data []byte, secret string) []int {
	var at []int
	for i := 0; i+8 <= len(secret); i++ {
		if bytes.Contains(data, []byte(secret[i:i+8])) {
			at = append(at, i)
		}
	}
	return at
}

// homeFiles reads every regular file under the daemon's home, by its path relative to the home.
func homeFiles(t *testing.T, home string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(home, func(path string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, path)
		files[rel] = data
		return nil
	})
	if err != nil {
		t.Fatalf("reading the daemon's home: %v", err)
	}
	return files
}

// noLeakInHome fails the test for each file under the daemon's home that holds a secret or a piece of one, naming the
// secret by what, never by its value.
func noLeakInHome(t *testing.T, files map[string][]byte, secrets map[string]string) {
	t.Helper()
	for rel, data := range files {
		for what, secret := range secrets {
			if at := leakedPieces(data, secret); len(at) != 0 {
				t.Errorf("%s holds pieces of %s, at offsets %v of it", rel, what, at)
			}
		}
	}
}

// harmflow-app#447: tasks run on the tools' own sign-ins, and HarmFlow's files keep no credential. The stand-in Claude
// Code, Codex, and GitHub CLI read their credentials from files of their own that HarmFlow never writes, and read
// signed out without them. A general task on each harness, and a GitHub task whose session runs the GitHub CLI first on
// its PATH, each complete through the real daemon. No request the test sends carries a credential, no stand-in was started
// with one in its arguments or its environment, and no file under the daemon's home holds one, nor a piece of one of
// eight characters or more: the files that kept what the tools printed hold [REDACTED] instead.
func TestTasksRunOnTheToolsOwnSignIns(t *testing.T) {
	t.Parallel()
	claude := newSignedIn(t, "claude", signedInClaude, "fake-claude", "sk-ant-oat01-")
	codex := newSignedIn(t, "codex", signedInCodex, "fake-codex", "sk-proj-")
	gh := newSignedIn(t, "gh", signedInGh, "", "gho_")
	stands := map[string]signedIn{"Claude Code's credential": claude, "Codex's credential": codex,
		"gh's credential": gh}
	secrets := map[string]string{}
	for what, s := range stands {
		secrets[what] = s.secret
	}
	// send sends a request whose body must carry no credential.
	send := func(d *daemon, method, path string, body any, want int) map[string]any {
		t.Helper()
		data, _ := json.Marshal(body)
		for what, secret := range secrets {
			if at := leakedPieces(data, secret); len(at) != 0 {
				t.Fatalf("the test's own %s %s carries pieces of %s", method, path, what)
			}
		}
		return d.call(method, path, body, want)
	}

	// The stand-in gh comes first on the PATH the daemon hands its harnesses. The daemon puts the login shell's PATH
	// before its own, and a login shell can put a gh installed on the machine first, so SHELL is a program that fails:
	// the daemon then reads no login shell, and its own PATH comes first.
	home := t.TempDir()
	d := startDaemon(t, home, "PATH="+filepath.Dir(gh.path)+":"+os.Getenv("PATH"), "SHELL=/usr/bin/false")
	ghTool := func() map[string]any {
		for _, tool := range d.list("/tools") {
			if tool["id"] == "gh" {
				return tool
			}
		}
		t.Fatal("GET /tools lists no gh")
		return nil
	}
	for _, id := range []string{"claude-code", "codex"} {
		path := claude.path
		if id == "codex" {
			path = codex.path
		}
		if h := d.setHarness(id, path); h["authentication"] != "invalid" || h["readiness"] != "not_ready" {
			t.Errorf("%s without its credential: authentication %v, readiness %v; want it signed out", id,
				h["authentication"], h["readiness"])
		}
	}
	if tool := ghTool(); tool["path"] != gh.path || tool["authentication"] != "invalid" {
		t.Errorf("gh without its credential: path %v, authentication %v; want %s, signed out", tool["path"],
			tool["authentication"], gh.path)
	}

	for _, s := range stands {
		s.signIn(t)
	}
	for id, path := range map[string]string{"claude-code": claude.path, "codex": codex.path} {
		if h := d.setHarness(id, path); h["authentication"] != "valid" || h["readiness"] != "ready" {
			t.Errorf("%s with its own credential: authentication %v, readiness %v; want it signed in, ready", id,
				h["authentication"], h["readiness"])
		}
	}
	if tool := ghTool(); tool["path"] != gh.path || tool["authentication"] != "valid" {
		t.Errorf("gh with its own credential: path %v, authentication %v; want %s, signed in", tool["path"],
			tool["authentication"], gh.path)
	}

	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	send(d, "PATCH", "/settings", map[string]any{"allowedFolders": []any{repo}}, 200)
	onCodex := strings.Replace(task("own-codex", "Say hi.", ""), "provider: claude-code", "provider: codex", 1)
	gitHub := `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: own-github
  name: Task own-github
spec:
  type: github
  prompt: RUN_GH=1 Check that the GitHub CLI is signed in.
  harness:
    preferred:
      provider: claude-code
  github:
    repositories:
      - name: octo/repo
        localPath: ` + strconv.Quote(repo) + `
`
	ids := map[string]string{}
	for id, doc := range map[string]string{"own-claude": task("own-claude", "Say hi.", ""), "own-codex": onCodex,
		"own-github": gitHub} {
		send(d, "POST", "/tasks", map[string]any{"yaml": doc}, 201)
		run := send(d, "POST", "/tasks/"+id+"/run", nil, 202)
		x := d.waitStatus(run["executionId"].(string), "completed", "failed", "needs_attention")
		if x["status"] != "completed" {
			t.Errorf("task %s: execution %v (%v), want completed", id, x["status"], x["errorMessage"])
		}
		ids[id] = x["id"].(string)
	}
	// The GitHub task's session ran the stand-in gh, which its environment names by the execution's id.
	ranGh := false
	for _, inv := range strings.Split(string(readOrEmpty(t, gh.record)), "@@\n") {
		ranGh = ranGh || strings.HasPrefix(inv, "auth\nstatus\n") &&
			strings.Contains(inv, "\nHARMFLOW_EXECUTION_ID="+ids["own-github"]+"\n")
	}
	if !ranGh {
		t.Errorf("the GitHub task's session never ran the stand-in gh at %s", gh.path)
	}

	for what, s := range stands {
		if at := leakedPieces(readOrEmpty(t, s.record), s.secret); len(at) != 0 {
			t.Errorf("%s was started with pieces of %s in its arguments or its environment", filepath.Base(s.path), what)
		}
	}

	// The daemon writes a partial line of a log only when it closes it.
	d.stop()
	files := homeFiles(t, home)
	for _, id := range ids {
		var kept bool
		for rel := range files {
			kept = kept || strings.HasPrefix(rel, filepath.Join("logs", id))
		}
		if !kept {
			t.Errorf("the daemon's home holds no log of execution %s, so the search proves nothing of it: %v", id,
				keys(files))
		}
	}
	noLeakInHome(t, files, secrets)
	for _, said := range []string{"claude-code signed in with [REDACTED]", "codex signed in with [REDACTED]",
		"logged in with [REDACTED]"} {
		var found bool
		for _, data := range files {
			found = found || bytes.Contains(data, []byte(said))
		}
		if !found {
			t.Errorf("no file under the daemon's home holds %q, the tools' output with its credential redacted: %v", said,
				keys(files))
		}
	}
}

// readOrEmpty is the file at path, empty when it was never written.
func readOrEmpty(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return data
}

// keys are a map's keys, for a failure message.
func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// harmflow-app#447, with the real tools: a task runs to completed in a fresh daemon home on the real, signed-in Claude
// Code, whose session runs one read-only gh command, and afterwards no file under that home holds the token gh is
// signed in with, nor a piece of it of eight characters or more. The token is read here and never printed.
func TestRealClaudeCodeAndGhLeaveNoCredentialInTheHome(t *testing.T) {
	claude := realClaude(t)
	out, err := exec.Command("gh", "auth", "token").Output()
	token := strings.TrimSpace(string(out))
	if err != nil || len(token) < 8 {
		t.Fatalf("gh auth token failed (%v): the test needs a gh signed in on this machine", err)
	}
	home := t.TempDir()
	d := startDaemon(t, home)
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": claude}, 200)
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{work}}, 200)
	const command = "gh api user --jq .login"
	prompt := "Run this command with the Bash tool, exactly as written, once, and nothing else: `" + command + "`. " +
		"Then say what it printed."
	extra := `      model: sonnet
      effort: low
  workspace:
    path: ` + strconv.Quote(work) + `
  permissions:
    profile: custom
    custom:
      allowedTools: ["Bash(gh api user:*)"]
  execution:
    timeoutMinutes: 5
`
	d.call("POST", "/tasks", map[string]any{"yaml": task("real-sign-in", prompt, extra)}, 201)
	id := d.call("POST", "/tasks/real-sign-in/run", nil, 202)["executionId"].(string)
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(id, 6*time.Minute)
	if x["status"] != "completed" {
		t.Errorf("execution %s = %v (%v), want completed", id, x["status"], x["errorMessage"])
	}
	var tool any
	ran := false
	for _, e := range d.events(id) {
		p, _ := e["payload"].(map[string]any)
		switch {
		case e["type"] == "tool.started" && strings.Contains(jsonString(p["command"]), "gh api user"):
			tool = p["toolId"]
		case e["type"] == "tool.completed" && tool != nil && p["toolId"] == tool && p["status"] == "succeeded":
			ran = true
		}
	}
	if !ran {
		t.Errorf("the session never ran %s to success, so it says nothing about gh's sign-in", command)
	}

	d.stop()
	noLeakInHome(t, homeFiles(t, home), map[string]string{"the token gh auth token prints": token})
}
