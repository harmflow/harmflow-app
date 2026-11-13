package tools

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// standIn writes into dir a stand-in for the CLI name whose --version and `auth status` run the given shell commands,
// and returns its path.
func standIn(t *testing.T, dir, name, version, auth string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := `#!/bin/bash
case "$1" in
  --version) ` + version + ` ;;
  auth) ` + auth + ` ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// probe runs a Prober whose PATH is dir alone and whose usual locations are an empty folder, so a git or gh installed
// on the machine is never found, and returns what it reports of git and of gh.
func probe(t *testing.T, dir string) (git, gh ports.ToolInfo) {
	t.Helper()
	tools := (&Prober{PathEnv: dir, Dirs: []string{t.TempDir()}}).Probe(context.Background())
	if len(tools) != 2 || tools[0].ID != "git" || tools[1].ID != "gh" {
		t.Fatalf("Probe() = %+v, want git then gh", tools)
	}
	return tools[0], tools[1]
}

const (
	gitVersion = `echo "git version 2.47.1"`
	ghVersion  = `echo "gh version 2.83.0 (2026-10-01)"; echo "https://github.com/cli/cli/releases/tag/v2.83.0"`
	ghSignedIn = `echo "github.com"; echo "  ✓ Logged in to github.com account octocat (keyring)"`
)

func TestProbeFindsGitWithItsVersion(t *testing.T) {
	dir := t.TempDir()
	path := standIn(t, dir, "git", gitVersion, "exit 1")
	git, _ := probe(t, dir)
	if !git.Detected || git.Path != path || git.Version != "2.47.1" || git.DiagnosticKey != "" || git.Diagnostic != "" {
		t.Errorf("git: detected %v, path %q, version %q, diagnostic %q (%s); want detected, %q, 2.47.1, no diagnostic",
			git.Detected, git.Path, git.Version, git.DiagnosticKey, git.Diagnostic, path)
	}
}

// harmflow-app#71's rule, which gh's sign-in follows: gh is signed out only when it says so. Any other failure of
// `gh auth status`, a probe killed at its timeout included, leaves the sign-in unknown, with a diagnostic saying it
// could not be verified.
func TestProbeReadsGhsSignIn(t *testing.T) {
	cases := []struct {
		name, auth string
		wantAuth   string
		wantKey    string
		wantParams map[string]string
	}{
		{"signed in", ghSignedIn, "valid", "", nil},
		// What gh prints when no host is signed in, exiting 1.
		{"signed out", `echo "You are not logged into any GitHub hosts. To log in, run: gh auth login" >&2; exit 1`,
			"invalid", "not_signed_in", map[string]string{"name": "gh", "command": "gh auth login"}},
		{"another failure", `echo "github.com"; echo "  X Failed to log in to github.com: HTTP 401: Bad credentials" >&2; exit 1`,
			"unknown", "authentication_unverified", nil},
		// The shell ends itself as the probe's timeout would end it, with no 15-second wait.
		{"killed at its timeout", `kill -KILL $$`, "unknown", "authentication_unverified", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := standIn(t, dir, "gh", ghVersion, c.auth)
			_, gh := probe(t, dir)
			if !gh.Detected || gh.Path != path || gh.Version != "2.83.0" {
				t.Errorf("gh: detected %v, path %q, version %q; want detected, %q, 2.83.0", gh.Detected, gh.Path,
					gh.Version, path)
			}
			if gh.Authentication != c.wantAuth || gh.DiagnosticKey != c.wantKey || (c.wantKey == "") != (gh.Diagnostic == "") ||
				!maps.Equal(gh.DiagnosticParams, c.wantParams) {
				t.Errorf("gh: authentication %s, diagnostic %q (%s) %v; want %s, %q %v", gh.Authentication,
					gh.DiagnosticKey, gh.Diagnostic, gh.DiagnosticParams, c.wantAuth, c.wantKey, c.wantParams)
			}
		})
	}
}

// A tool found neither on PATH nor in the usual locations reads not detected, with the diagnostic that names it, and
// the other tool is still found.
func TestProbeReportsAMissingTool(t *testing.T) {
	t.Run("git absent", func(t *testing.T) {
		dir := t.TempDir()
		standIn(t, dir, "gh", ghVersion, ghSignedIn)
		git, gh := probe(t, dir)
		if git.Detected || git.Path != "" || git.DiagnosticKey != "git_not_found" || git.Diagnostic == "" {
			t.Errorf("git: detected %v, path %q, diagnostic %q (%s); want not detected, no path, git_not_found",
				git.Detected, git.Path, git.DiagnosticKey, git.Diagnostic)
		}
		if !gh.Detected {
			t.Errorf("gh: not detected (%s), want the stand-in", gh.Diagnostic)
		}
	})
	t.Run("gh absent", func(t *testing.T) {
		dir := t.TempDir()
		standIn(t, dir, "git", gitVersion, "exit 1")
		git, gh := probe(t, dir)
		if gh.Detected || gh.Path != "" || gh.DiagnosticKey != "gh_not_found" || gh.Diagnostic == "" ||
			gh.Authentication != "unknown" {
			t.Errorf("gh: detected %v, path %q, authentication %s, diagnostic %q (%s); want not detected, no path, "+
				"unknown, gh_not_found", gh.Detected, gh.Path, gh.Authentication, gh.DiagnosticKey, gh.Diagnostic)
		}
		if !git.Detected {
			t.Errorf("git: not detected (%s), want the stand-in", git.Diagnostic)
		}
	})
}

// A tool whose --version fails is not detected, and its diagnostic names the command that failed; gh's sign-in is then
// not read.
func TestProbeReportsAVersionThatFails(t *testing.T) {
	dir := t.TempDir()
	gitPath := standIn(t, dir, "git", `echo "git: broken install" >&2; exit 2`, "exit 1")
	ghPath := standIn(t, dir, "gh", `echo "gh: broken install" >&2; exit 2`, ghSignedIn)
	git, gh := probe(t, dir)
	for _, c := range []struct {
		tool          ports.ToolInfo
		path, command string
	}{{git, gitPath, "git --version"}, {gh, ghPath, "gh --version"}} {
		if c.tool.Detected || c.tool.Path != c.path || c.tool.DiagnosticKey != "command_failed" ||
			c.tool.DiagnosticParams["command"] != c.command || c.tool.DiagnosticParams["detail"] == "" ||
			c.tool.Authentication != "unknown" {
			t.Errorf("%s: detected %v, path %q, authentication %s, diagnostic %q %v; want not detected, %q, unknown, "+
				"command_failed naming %q", c.tool.ID, c.tool.Detected, c.tool.Path, c.tool.Authentication,
				c.tool.DiagnosticKey, c.tool.DiagnosticParams, c.path, c.command)
		}
	}
}

// harmflow-app#1028: the daemon's CODEX_HOME names a Codex installation, whose environment reaches no other process, so
// git's and gh's probes start without it, whether it comes in the prober's environment or, with none, in the daemon's
// own.
func TestProbesRunWithoutCodexHome(t *testing.T) {
	const record = `printf '%s|%s\n' "$0 $*" "${CODEX_HOME-unset}" >> "$ENVLOG"; `
	for _, c := range []struct {
		name    string
		fromEnv bool
	}{{"the prober's environment", false}, {"the daemon's own environment", true}} {
		t.Run(c.name, func(t *testing.T) {
			dir, envLog, codexHome := t.TempDir(), filepath.Join(t.TempDir(), "env.log"), t.TempDir()
			standIn(t, dir, "git", record+gitVersion, "exit 1")
			standIn(t, dir, "gh", record+ghVersion, record+ghSignedIn)
			t.Setenv("CODEX_HOME", codexHome)
			p := &Prober{PathEnv: dir, Dirs: []string{t.TempDir()}}
			if c.fromEnv {
				t.Setenv("ENVLOG", envLog)
			} else {
				p.Env = []string{"PATH=/usr/bin:/bin", "ENVLOG=" + envLog, "CODEX_HOME=" + codexHome}
			}

			tools := p.Probe(t.Context())
			if len(tools) != 2 || !tools[0].Detected || !tools[1].Detected || tools[1].Authentication != "valid" {
				t.Fatalf("Probe() = %+v, want git and gh detected, gh signed in", tools)
			}
			data, err := os.ReadFile(envLog)
			if err != nil {
				t.Fatalf("os.ReadFile(%s) = %v, want the probes' environments", envLog, err)
			}
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			for _, l := range lines {
				if probe, home, _ := strings.Cut(l, "|"); home != "unset" {
					t.Errorf("%s ran with CODEX_HOME %q, want none", probe, home)
				}
			}
			if len(lines) != 3 {
				t.Errorf("the probes ran %q, want git --version, gh --version, and gh auth status", lines)
			}
		})
	}
}
