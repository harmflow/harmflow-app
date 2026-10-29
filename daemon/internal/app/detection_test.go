package app

import (
	"context"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/claudecode"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/codex"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// A stand-in for the claude CLI: signed in, at a supported version, with every flag the adapter needs.
const claudeStandIn = `#!/bin/bash
case "$1" in
  --version) echo "2.1.239 (Claude Code)" ;;
  --help) echo "-p, --print  --output-format <format>  --permission-mode <mode>  --allowedTools <tools...>  --append-system-prompt <prompt>" ;;
  auth) echo '{"loggedIn": true}' ;;
esac
`

// harmflow-app#68: with Claude Code installed and Codex nowhere, the inventory shows where Claude Code is and at which
// version, and Codex as not installed with the reason. The real adapters search only the test's directories, and
// detection writes nothing under the user's home.
func TestInventoryReportsWhatIsInstalled(t *testing.T) {
	userHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(bin, "claude")
	if err := os.WriteFile(claude, []byte(claudeStandIn), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + userHome}
	inventory := detectAll(t, userHome,
		&claudecode.Adapter{Home: userHome, PathEnv: bin, Env: env, KnownLocations: []string{}},
		&codex.Adapter{Home: userHome, PathEnv: bin, Env: env, KnownLocations: []string{}})

	c := inventory[claudecode.ID]
	if !c.Detected || c.Path != claude || c.PathSource != "path" || c.Version != "2.1.239" || c.Readiness != "ready" ||
		!slices.Equal(c.Installations, []string{claude}) {
		t.Errorf("Claude Code: detected %v, path %q (%s), version %q, readiness %s, installations %q", c.Detected, c.Path,
			c.PathSource, c.Version, c.Readiness, c.Installations)
	}
	wantNotInstalled(t, inventory[codex.ID], "codex_not_found")

	if written := writtenUnder(userHome); len(written) != 0 {
		t.Errorf("detection wrote under the user's home: %q", written)
	}
}

// realClaudeEnv and realCodexEnv opt in to the tests that run detection on the installed Claude Code or Codex instead
// of a stand-in. They have the names and the meaning they have in tests/integration, whose helpers a daemon package
// cannot import. These tests start no session and spend no usage, but they need the real CLI, so they run only on
// request, never in CI.
const (
	realClaudeEnv = "HARMFLOW_TEST_CLAUDE_CODE"
	realCodexEnv  = "HARMFLOW_TEST_CODEX"
)

// realClaude is the installed claude's path; it skips the test unless realClaudeEnv opts in and claude is on the PATH.
func realClaude(t *testing.T) string {
	t.Helper()
	if os.Getenv(realClaudeEnv) != "1" {
		t.Skipf("runs detection on the real Claude Code: set %s=1 on a machine where claude is installed", realClaudeEnv)
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not on PATH")
	}
	return path
}

// realCodex is the installed codex's path; it skips the test unless realCodexEnv opts in and codex is on the PATH.
func realCodex(t *testing.T) string {
	t.Helper()
	if os.Getenv(realCodexEnv) != "1" {
		t.Skipf("runs detection on the real Codex: set %s=1 on a machine where codex is installed", realCodexEnv)
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex is not on PATH")
	}
	return path
}

// With the real Claude Code installed and Codex nowhere, the inventory shows Claude Code where the search found it, at
// the version the binary itself prints, and Codex as not installed. The test only detects: it runs no task and asserts
// no sign-in.
func TestInventoryReportsARealClaudeCodeAndNoCodex(t *testing.T) {
	target := evalSymlinks(t, realClaude(t))
	inventory, link, env := realDetection(t, "claude", target)

	c, want := inventory[claudecode.ID], versionOf(t, env, link)
	if !c.Detected || c.PathSource != "path" || c.Installation != link || !slices.Equal(c.Installations, []string{link}) ||
		c.Path != target || !filepath.IsAbs(c.Path) || c.Version != want || c.VersionSupport == "below_minimum" {
		t.Errorf("Claude Code: detected %v, path source %q, installation %q, installations %q, path %q, version %q (%s), "+
			"diagnostic %q; want detected, path source path, installation and installations %q, path %q, version %q, "+
			"not below the minimum", c.Detected, c.PathSource, c.Installation, c.Installations, c.Path, c.Version,
			c.VersionSupport, c.Diagnostic, link, target, want)
	}
	wantNotInstalled(t, inventory[codex.ID], "codex_not_found")
}

// The same as TestInventoryReportsARealClaudeCodeAndNoCodex the other way round: with the real Codex installed and
// Claude Code nowhere, the inventory shows Codex where the search found it, at the version the CLI itself prints, and
// Claude Code as not installed.
func TestInventoryReportsARealCodexAndNoClaudeCode(t *testing.T) {
	inventory, link, env := realDetection(t, "codex", evalSymlinks(t, realCodex(t)))

	x, want := inventory[codex.ID], versionOf(t, env, link)
	if !x.Detected || x.PathSource != "path" || x.Installation != link || !slices.Equal(x.Installations, []string{link}) ||
		x.Version != want || x.VersionSupport == "below_minimum" {
		t.Errorf("Codex: detected %v, path source %q, installation %q, installations %q, version %q (%s), diagnostic %q; "+
			"want detected, path source path, installation and installations %q, version %q, not below the minimum",
			x.Detected, x.PathSource, x.Installation, x.Installations, x.Version, x.VersionSupport, x.Diagnostic, link, want)
	}
	wantNotInstalled(t, inventory[claudecode.ID], "claude_not_found")
}

// realDetection runs detection through the use cases with the real adapters, each searching only a directory of the
// test's own: the one of the CLI named cli holds a link to target, the installed CLI, and the other's nothing. It
// returns the inventory, the link, and the environment the probes ran with. The adapters' Home and the probes' HOME are
// two temporary directories: a real CLI keeps state of its own under its HOME, Codex on every run and Claude Code once
// its sign-in check finds no configuration there, so only the adapters' Home shows what detection itself writes, and
// it must stay empty. What the CLI wrote under its HOME is logged.
func realDetection(t *testing.T, cli, target string) (map[string]ports.HarnessInfo, string, []string) {
	t.Helper()
	home, probeHome := evalSymlinks(t, t.TempDir()), evalSymlinks(t, t.TempDir())
	search := map[string]string{"claude": evalSymlinks(t, t.TempDir()), "codex": evalSymlinks(t, t.TempDir())}
	link := filepath.Join(search[cli], cli)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	env := probeEnv(probeHome)
	// Codex starts its app server once it reads ready; Close ends it with the test.
	cx := &codex.Adapter{Home: home, PathEnv: search["codex"], Env: env, KnownLocations: []string{}}
	t.Cleanup(cx.Close)
	inventory := detectAll(t, home,
		&claudecode.Adapter{Home: home, PathEnv: search["claude"], Env: env, KnownLocations: []string{}}, cx)
	if written := writtenUnder(home); len(written) != 0 {
		t.Errorf("detection wrote under the adapters' home: %q", written)
	}
	t.Logf("%s wrote under the probes' HOME: %q", cli, writtenUnder(probeHome))
	return inventory, link, env
}

// evalSymlinks is path with every link in it resolved, as the adapters resolve what they find: the installed CLI's own
// file, which an install links from its bin folder, or a temporary directory, which on macOS is under /var, a link to
// /private/var.
func evalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// probeEnv is the environment the real CLIs' probes run with, and nothing else of the test's: home as HOME, so they
// keep the user's own configuration out of reach, and a PATH of the system's tools and of the node that runs an npm
// install of either CLI, found where the test's own PATH finds it.
func probeEnv(home string) []string {
	path := "/usr/bin:/bin"
	if node, err := exec.LookPath("node"); err == nil {
		path = filepath.Dir(node) + ":" + path
	}
	return []string{"PATH=" + path, "HOME=" + home}
}

// detectAll runs detection through the use cases with the given adapters, as the daemon does, and returns the inventory
// by harness.
func detectAll(t *testing.T, home string, harnesses ...ports.HarnessAdapter) map[string]ports.HarnessInfo {
	t.Helper()
	paths := NewPaths(t.TempDir(), home, t.TempDir())
	events, err := eventlog.New(paths.Logs, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Deps{Paths: paths, Events: events, Logger: log.New(io.Discard, "", 0), Harnesses: harnesses})
	if err != nil {
		t.Fatal(err)
	}
	a.RefreshHarnesses(context.Background())
	inventory := map[string]ports.HarnessInfo{}
	for _, h := range a.Harnesses() {
		inventory[h.ID] = h
	}
	return inventory
}

var versionNumber = regexp.MustCompile(`\d+\.\d+\.\d+`)

// versionOf is the version number cli prints for --version, run with env.
func versionOf(t *testing.T, env []string, cli string) string {
	t.Helper()
	cmd := exec.Command(cli, "--version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s --version: %v", cli, err)
	}
	v := versionNumber.FindString(string(out))
	if v == "" {
		t.Fatalf("%s --version printed %q, with no version number", cli, out)
	}
	return v
}

// wantNotInstalled checks that a harness the search cannot find reads not installed, with the diagnostic key and no
// path or installation.
func wantNotInstalled(t *testing.T, h ports.HarnessInfo, diagnostic string) {
	t.Helper()
	if h.Detected || h.Readiness != "not_installed" || h.DiagnosticKey != diagnostic || h.Diagnostic == "" || h.Path != "" ||
		len(h.Installations) != 0 {
		t.Errorf("%s: detected %v, readiness %s, diagnostic %q (%s), path %q, installations %q; want not installed, %s",
			h.Name, h.Detected, h.Readiness, h.DiagnosticKey, h.Diagnostic, h.Path, h.Installations, diagnostic)
	}
}

// writtenUnder lists every file and folder under dir.
func writtenUnder(dir string) []string {
	var written []string
	_ = filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if path != dir {
			written = append(written, path)
		}
		return err
	})
	return written
}
