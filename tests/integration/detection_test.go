package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// cli is a harness CLI a test wrote, a stand-in or a wrapper of the real one, and the log where it records how it was
// invoked.
type cli struct {
	path, log string
}

// standIn copies a stand-in from testdata into a temporary directory under the CLI's name, with text replaced in pairs
// (old, new), and makes it record the first two arguments of each invocation, one line each, in its log. The log's
// path is part of the copy, so recording needs no variable, which a run's environment could drop.
func standIn(t *testing.T, fake, name string, replace ...string) cli {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(repoRoot(), "tests", "integration", "testdata", fake))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(replace); i += 2 {
		if !bytes.Contains(src, []byte(replace[i])) {
			t.Fatalf("%s has no %q", fake, replace[i])
		}
		src = bytes.Replace(src, []byte(replace[i]), []byte(replace[i+1]), 1)
	}
	// The path the daemon reports is the real one: on macOS the temporary directories are under a symlink.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := cli{path: filepath.Join(dir, name), log: filepath.Join(dir, "invocations.log")}
	src = bytes.Replace(src, []byte("#!/bin/bash\n"), []byte("#!/bin/bash\necho \"$1 $2\" >> '"+c.log+"'\n"), 1)
	if err := os.WriteFile(c.path, src, 0o755); err != nil {
		t.Fatal(err)
	}
	return c
}

// invocations are the first two arguments of each time the stand-in ran, in order.
func (c cli) invocations(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(c.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		out = append(out, strings.TrimSpace(line))
	}
	return out
}

// only fails the test on any invocation of the stand-in outside allowed, and counts each allowed one.
func (c cli) only(t *testing.T, allowed ...string) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, inv := range c.invocations(t) {
		if !slices.Contains(allowed, inv) {
			t.Errorf("%s was invoked with %q, outside %q", filepath.Base(c.path), inv, allowed)
		}
		counts[inv]++
	}
	return counts
}

// The probes detection runs, and how a run invokes each CLI. Claude Code's probes include the quota read before an
// attempt, `claude -p /usage`, which runs no turn and makes no model call.
var (
	claudeProbes = []string{"--version", "--help", "auth status", claudeQuota}
	claudeQuota  = "-p /usage"
	codexProbes  = []string{"--version", "exec --help", "--help", "login status"}
	claudeRun    = "-p --output-format"
	codexRun     = "exec --json"
)

// setHarness sets a harness's path by hand and returns its entry in GET /harnesses once the entry is that path's. The
// PUT is repeated until then: the daemon's first detection, started before it, can land after it.
func (d *daemon) setHarness(id, path string) map[string]any {
	d.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		d.call("PUT", "/harnesses/"+id, map[string]any{"path": path}, 200)
		h := d.harness(id)
		if h["path"] == path {
			return h
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("GET /harnesses: %s is at %v, want %s", id, h["path"], path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// runOn runs a new task on the harness, with no fallback, and returns its execution once it has ended.
func (d *daemon) runOn(id, harness string) map[string]any {
	d.t.Helper()
	doc := strings.Replace(task(id, "Say hi.", ""), "provider: claude-code", "provider: "+harness, 1)
	d.call("POST", "/tasks", map[string]any{"yaml": doc}, 201)
	run := d.call("POST", "/tasks/"+id+"/run", nil, 202)
	return d.waitStatus(run["executionId"].(string), "completed", "failed")
}

// neverStarted fails the test unless the execution failed before it started a harness, with an error naming it.
func neverStarted(t *testing.T, x map[string]any, name string) {
	t.Helper()
	attempts, _ := x["attempts"].([]any)
	message, _ := x["errorMessage"].(string)
	if x["status"] != "failed" || x["startedAt"] != nil || len(attempts) != 0 || !strings.Contains(message, name) {
		t.Errorf("status %v, startedAt %v, attempts %v, error %q; want it failed, never started, naming %s", x["status"],
			x["startedAt"], attempts, message, name)
	}
}

// harmflow-app#68: the daemon never installs, updates, or signs in a harness. Detecting a signed-out Claude Code and a
// Codex below the minimum, and running a task on each, invokes only the probes; each execution fails without starting,
// and the inventory says why.
func TestDetectionOnlyProbes(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	claude := standIn(t, "fake-claude", "claude", `auth) echo '{"loggedIn": true}'; exit 0 ;;`,
		`auth) echo '{"loggedIn": false, "authMethod": "none"}'; exit 1 ;;`)
	codex := standIn(t, "fake-codex", "codex", "--sandbox <MODE>  ", "")
	d.setHarness("claude-code", claude.path)
	d.setHarness("codex", codex.path)

	neverStarted(t, d.runOn("signedout", "claude-code"), "Claude Code")
	neverStarted(t, d.runOn("belowminimum", "codex"), "Codex")
	for id, key := range map[string]string{"claude-code": "claude_not_signed_in", "codex": "harness_feature_missing"} {
		h := d.harness(id)
		if h["readiness"] != "not_ready" || h["diagnosticKey"] != key || h["diagnostic"] == nil {
			t.Errorf("GET /harnesses: %s is %v, diagnostic %v (%v); want not_ready, %s", id, h["readiness"],
				h["diagnosticKey"], h["diagnostic"], key)
		}
	}
	for _, c := range []struct {
		cli    cli
		probes []string
	}{{claude, claudeProbes}, {codex, codexProbes}} {
		t.Run(filepath.Base(c.cli.path), func(t *testing.T) {

			if counts := c.cli.only(t, c.probes...); len(counts) == 0 {
				t.Errorf("%s.only(%v) = no probe, want it probed", c.cli.path, c.probes)
			}
		})
	}
}

// harmflow-app#68: with ready harnesses, the daemon invokes each one for its probes and its runs, and for nothing else.
func TestReadyHarnessesOnlyProbeAndRun(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	claude, codex := standIn(t, "fake-claude", "claude"), standIn(t, "fake-codex", "codex")
	d.setHarness("claude-code", claude.path)
	d.setHarness("codex", codex.path)
	for _, c := range []struct {
		task, harness string
		cli           cli
		probes        []string
		run           string
	}{
		{"readyclaude", "claude-code", claude, claudeProbes, claudeRun},
		{"readycodex", "codex", codex, codexProbes, codexRun},
	} {
		t.Run(c.task, func(t *testing.T) {
			d := d.in(t)

			if x := d.runOn(c.task, c.harness); x["status"] != "completed" {
				t.Fatalf("runOn(%s, %s) status = %v, error %v, want completed", c.task, c.harness, x["status"], x["errorMessage"])
			}
			if counts := c.cli.only(t, append(c.probes, c.run)...); counts[c.run] != 1 || counts["--version"] == 0 {
				t.Errorf("%s invocations = %v, want the probes and one run", c.harness, counts)
			}
		})
	}
}

// harmflow-app#68: a signed-out harness is detected, at its version, with an invalid sign-in: not ready, and the
// diagnostic says to sign in.
func TestSignedOutHarnessIsNotReady(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	cases := []struct {
		id, version, key string
		cli              cli
	}{
		{"claude-code", "2.1.239", "claude_not_signed_in", standIn(t, "fake-claude", "claude",
			`auth) echo '{"loggedIn": true}'; exit 0 ;;`, `auth) echo '{"loggedIn": false, "authMethod": "none"}'; exit 1 ;;`)},
		{"codex", "0.163.0", "not_signed_in", standIn(t, "fake-codex", "codex",
			`login) echo "Logged in using ChatGPT"; exit 0 ;;`, `login) echo "Not logged in" >&2; exit 1 ;;`)},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			d := d.in(t)

			h := d.setHarness(c.id, c.cli.path)
			if h["detected"] != true || h["version"] != c.version || h["authentication"] != "invalid" ||
				h["readiness"] != "not_ready" || h["diagnosticKey"] != c.key || h["diagnostic"] == nil {
				t.Errorf("PUT /harnesses/%s = detected %v, version %v, authentication %v, readiness %v, diagnostic %v (%v); "+
					"want detected, %s, invalid, not_ready, %s", c.id, h["detected"], h["version"], h["authentication"],
					h["readiness"], h["diagnosticKey"], h["diagnostic"], c.version, c.key)
			}
		})
	}
}

// harmflow-app#327: the real Claude Code, signed out in a configuration directory of the test's own, reads installed at
// the version it prints, with an invalid sign-in, and not ready; a task on it fails without starting, and the daemon
// invoked it for its probes alone. The stand-ins above print what the adapter reads; this test shows the real CLI does.
func TestSignedOutRealClaudeCodeOnlyProbes(t *testing.T) {
	signedOutOnlyProbes(t, realClaude(t), "claude-code", "Claude Code", "claude_not_signed_in", claudeProbes)
}

// harmflow-app#327: the same as TestSignedOutRealClaudeCodeOnlyProbes with the real Codex, signed out in the test
// daemon's own Codex home.
func TestSignedOutRealCodexOnlyProbes(t *testing.T) {
	signedOutOnlyProbes(t, realCodex(t), "codex", "Codex", "not_signed_in", codexProbes)
}

// signedOutOnlyProbes sets the real CLI at installed, through a wrapper that records each invocation, as the harness
// id, with the daemon signed out of it: Claude Code reads a configuration directory of the test's own, Codex the
// daemon's own Codex home, and the daemon's environment carries no credential. The harness must read detected, at the
// version the CLI prints, with an invalid sign-in and the diagnostic key, and not ready; a task on it must fail without
// starting, naming it; and the CLI must have been invoked for probes alone, and be the same file at the same version
// after the test. The test never signs in or out, so the user's own sign-in stays as it is.
func signedOutOnlyProbes(t *testing.T, installed, id, name, key string, probes []string) {
	home := t.TempDir()
	// Codex reads no configuration from a home that does not exist, and so cannot say it is signed out.
	codexHome := filepath.Join(home, "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	env := signedOutEnv(t.TempDir())
	// The test's own probes run with the environment the daemon runs with, so they read the same state.
	probeEnv := append(append(os.Environ(), "CODEX_HOME="+codexHome), env...)
	file, version := resolved(t, installed), versionOf(t, probeEnv, installed)

	d := startDaemon(t, home, env...)
	wrapper := wrap(t, installed)
	h := d.setHarness(id, wrapper.path)
	if h["detected"] != true || h["version"] != version || h["authentication"] != "invalid" ||
		h["readiness"] != "not_ready" || h["diagnosticKey"] != key || h["diagnostic"] == nil {
		t.Errorf("PUT /harnesses/%s = detected %v, version %v, authentication %v, readiness %v, diagnostic %v (%v); "+
			"want detected, %s, invalid, not_ready, %s", id, h["detected"], h["version"], h["authentication"],
			h["readiness"], h["diagnosticKey"], h["diagnostic"], version, key)
	}

	x := d.runOn("signedout", id)
	neverStarted(t, x, name)
	for _, e := range d.call("GET", "/executions/"+x["id"].(string)+"/events", nil, 200)["events"].([]any) {
		if e := e.(map[string]any); e["type"] == "execution.started" {
			t.Errorf("the execution on the signed-out %s logged %v, want it never started", name, e)
		}
	}
	if counts := wrapper.only(t, probes...); len(counts) == 0 {
		t.Errorf("%s.only(%v) = no probe, want it probed", wrapper.path, probes)
	}
	if after := resolved(t, installed); after != file {
		t.Errorf("%s resolves to %s after the test, want %s, as before it", installed, after, file)
	}
	if after := versionOf(t, probeEnv, installed); after != version {
		t.Errorf("%s --version = %s after the test, want %s, as before it", installed, after, version)
	}
}

// signedOutEnv is what the daemon of a signed-out real CLI adds to the test's environment: config as Claude Code's
// configuration directory, and empty each variable through which either harness takes an API key or a token instead
// of its sign-in, with every CLAUDE_CODE_ variable, which a run inside a Claude Code session inherits from it.
func signedOutEnv(config string) []string {
	env := []string{"CLAUDE_CONFIG_DIR=" + config}
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY",
		"CODEX_API_KEY"} {
		env = append(env, name+"=")
	}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "CLAUDE_CODE_") {
			env = append(env, name+"=")
		}
	}
	return env
}

// wrap writes a wrapper of the real CLI at installed, under the CLI's name, which records the first two arguments of
// each invocation in its log, as a stand-in does, then runs the real CLI with all of them. The log's path is part of
// the script, as a stand-in's is.
func wrap(t *testing.T, installed string) cli {
	t.Helper()
	// The path the daemon reports is the real one: on macOS the temporary directories are under a symlink.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := cli{path: filepath.Join(dir, filepath.Base(installed)), log: filepath.Join(dir, "invocations.log")}
	script := "#!/bin/bash\necho \"$1 $2\" >> '" + c.log + "'\nexec '" + installed + "' \"$@\"\n"
	if err := os.WriteFile(c.path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return c
}

// resolved is the file path names, with every link in it resolved: an install links the CLI from its bin folder.
func resolved(t *testing.T, path string) string {
	t.Helper()
	file, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

var versionNumber = regexp.MustCompile(`\d+\.\d+\.\d+`)

// versionOf is the version number the CLI at path prints for --version, run with env.
func versionOf(t *testing.T, env []string, path string) string {
	t.Helper()
	cmd := exec.Command(path, "--version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s --version: %v", path, err)
	}
	v := versionNumber.FindString(string(out))
	if v == "" {
		t.Fatalf("%s --version printed %q, with no version number", path, out)
	}
	return v
}

// harmflow-app#68: the inventory names a path set by hand as such, and a run uses that file.
func TestManualPathIsTheOneRun(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	claude := standIn(t, "fake-claude", "claude")
	h := d.setHarness("claude-code", claude.path)
	if h["pathSource"] != "manual" || h["readiness"] != "ready" {
		t.Errorf("GET /harnesses: path %v (%v), readiness %v; want the path set by hand, ready", h["path"],
			h["pathSource"], h["readiness"])
	}
	if x := d.runOn("manual", "claude-code"); x["status"] != "completed" {
		t.Fatalf("status %v, error %v", x["status"], x["errorMessage"])
	}
	if counts := claude.only(t, append(claudeProbes, claudeRun)...); counts[claudeRun] != 1 || counts[claudeQuota] != 1 {
		t.Errorf("the file set by hand ran %d times and read the quota %d times, want once each: %v", counts[claudeRun],
			counts[claudeQuota], counts)
	}
}

// harmflow-app#68: the inventory never carries a credential, even when a sign-in probe prints one on its output or its
// error stream.
func TestInventoryCarriesNoCredential(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	// Built here, so no token-shaped literal is in the repository.
	secrets := []string{"sk-ant-oat01-" + strings.Repeat("q", 40), "ghp_" + strings.Repeat("x", 36),
		"sk-proj-" + strings.Repeat("z", 40), "ghp_" + strings.Repeat("y", 36)}
	claude := standIn(t, "fake-claude", "claude", `auth) echo '{"loggedIn": true}'; exit 0 ;;`,
		`auth) echo '{"loggedIn": true, "token": "`+secrets[0]+`"}'; echo "refreshed `+secrets[1]+`" >&2; exit 0 ;;`)
	codex := standIn(t, "fake-codex", "codex", `login) echo "Logged in using ChatGPT"; exit 0 ;;`,
		`login) echo "Logged in using an API key - `+secrets[2]+`"; echo "token `+secrets[3]+`" >&2; exit 0 ;;`)
	for id, path := range map[string]string{"claude-code": claude.path, "codex": codex.path} {
		if h := d.setHarness(id, path); h["authentication"] != "valid" || h["readiness"] != "ready" {
			t.Errorf("%s: authentication %v, readiness %v; want the probe read as signed in", id, h["authentication"],
				h["readiness"])
		}
	}
	status, body := d.request("GET", "/harnesses", nil, nil)
	if status != 200 {
		t.Fatalf("GET /harnesses: %d %s", status, body)
	}
	for _, s := range secrets {
		if bytes.Contains(body, []byte(s)) {
			t.Errorf("GET /harnesses carries %s…: %s", s[:8], body)
		}
	}
}

// harmflow-app#68: a version below the minimum is not ready, and a task on it never starts; an untested newer version
// is ready, and each run on it records one warning naming the version.
func TestVersionsOutsideTheTestedRange(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	claude := standIn(t, "fake-claude", "claude", "--permission-mode <mode>  ", "")
	codex := standIn(t, "fake-codex", "codex", `echo "codex-cli 0.163.0"`, `echo "codex-cli 0.999.0"`)

	h := d.setHarness("claude-code", claude.path)
	params, _ := h["diagnosticParams"].(map[string]any)
	if h["versionSupport"] != "below_minimum" || h["readiness"] != "not_ready" ||
		h["diagnosticKey"] != "harness_feature_missing" || params["feature"] != "--permission-mode" {
		t.Errorf("Claude Code: versionSupport %v, readiness %v, diagnostic %v %v; want below_minimum, not_ready, naming "+
			"--permission-mode", h["versionSupport"], h["readiness"], h["diagnosticKey"], params)
	}
	h = d.setHarness("codex", codex.path)
	if h["version"] != "0.999.0" || h["versionSupport"] != "untested_newer" || h["readiness"] != "ready" {
		t.Errorf("Codex: version %v, versionSupport %v, readiness %v; want 0.999.0, untested_newer, ready", h["version"],
			h["versionSupport"], h["readiness"])
	}

	neverStarted(t, d.runOn("tooold", "claude-code"), "Claude Code")
	if counts := claude.only(t, claudeProbes...); len(counts) == 0 {
		t.Error("Claude Code was never probed")
	}

	x := d.runOn("toonew", "codex")
	if x["status"] != "completed" {
		t.Fatalf("Codex: status %v, error %v", x["status"], x["errorMessage"])
	}
	var warnings []map[string]any
	for _, w := range x["warnings"].([]any) {
		if w := w.(map[string]any); w["code"] == "version_untested" {
			warnings = append(warnings, w)
		}
	}
	if len(warnings) != 1 || warnings[0]["subject"] != "codex" || warnings[0]["messageKey"] != "harness_version_untested" ||
		!strings.Contains(jsonString(warnings[0]["message"]), "0.999.0") {
		t.Errorf("the record's untested-version warnings: %v; want one naming 0.999.0", warnings)
	}
	var logged []map[string]any
	for _, e := range d.call("GET", "/executions/"+x["id"].(string)+"/events", nil, 200)["events"].([]any) {
		if e := e.(map[string]any); e["type"] == "harness.version_untested" {
			logged = append(logged, e)
		}
	}
	if len(logged) != 1 || logged[0]["severity"] != "warning" || !strings.Contains(jsonString(logged[0]["message"]), "0.999.0") {
		t.Errorf("the log's harness.version_untested events: %v; want one warning naming 0.999.0", logged)
	}
}

// jsonString is a JSON value as a string, empty when it is none.
func jsonString(v any) string {
	s, _ := v.(string)
	return s
}
