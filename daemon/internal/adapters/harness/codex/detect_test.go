package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// tempDir is a temporary directory by its real path: on macOS the temporary directories are under /var, a symlink to
// /private/var.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// standInAt puts a stand-in whose --version and `login status` run the given shell commands in dir, under the CLI's
// name.
func standInAt(t *testing.T, dir, version, login string) string {
	t.Helper()
	data, err := os.ReadFile(standIn(t, version, login))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const signedIn = `echo "Logged in using ChatGPT" >&2`

// harmflow-app#68: detection searches PATH, then the known locations, and keeps every installation it finds in that
// order. The first is the one used, and its source says where it was found.
func TestDetectOnPathAndInKnownLocations(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	cases := []struct {
		name              string
		onPath, atKnown   bool
		wantSource        string
		wantVersion       string
		wantInstallations func(onPath, atKnown string) []string
	}{
		{"on PATH", true, false, "path", "0.163.0", func(p, _ string) []string { return []string{p} }},
		{"in a known location", false, true, "known_location", "0.150.0", func(_, k string) []string { return []string{k} }},
		{"in both", true, true, "path", "0.163.0", func(p, k string) []string { return []string{p, k} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			empty, pathDir, knownDir := tempDir(t), tempDir(t), tempDir(t)
			onPath, atKnown := filepath.Join(pathDir, "codex"), filepath.Join(knownDir, "codex")
			if c.onPath {
				standInAt(t, pathDir, `echo "codex-cli 0.163.0"`, signedIn)
			}
			if c.atKnown {
				standInAt(t, knownDir, `echo "codex-cli 0.150.0"`, signedIn)
			}
			a := &Adapter{Home: tempDir(t), PathEnv: empty + ":" + pathDir, KnownLocations: []string{atKnown}}
			info := a.Detect(context.Background(), "")
			want := c.wantInstallations(onPath, atKnown)
			used := want[0]
			if !info.Detected || info.Path != used || info.PathSource != c.wantSource || info.Version != c.wantVersion ||
				info.Readiness != "ready" || !slices.Equal(info.Installations, want) {
				t.Errorf("detected %v, path %q (%s), version %q, readiness %s, installations %q; want path %q (%s), "+
					"version %q, ready, installations %q", info.Detected, info.Path, info.PathSource, info.Version,
					info.Readiness, info.Installations, used, c.wantSource, c.wantVersion, want)
			}
		})
	}
}

// harmflow-app#68: each step of detection has its readiness, and the path detection found is absolute.
func TestDetectReadinessAtEachStep(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	version, signedOut := `echo "codex-cli 0.163.0"`, `echo "Not logged in" >&2; exit 1`
	cases := []struct {
		name, version, login   string
		installed              bool
		wantDetected           bool
		wantReadiness, wantKey string
	}{
		{"not found", "", "", false, false, "not_installed", "codex_not_found"},
		{"--version fails", `echo "error: cannot start" >&2; exit 1`, signedIn, true, true, "installed", "command_failed"},
		{"signed out", version, signedOut, true, true, "not_ready", "not_signed_in"},
		{"signed in", version, signedIn, true, true, "ready", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := tempDir(t)
			if c.installed {
				standInAt(t, dir, c.version, c.login)
			}
			a := &Adapter{Home: tempDir(t), PathEnv: dir, KnownLocations: []string{}}
			info := a.Detect(context.Background(), "")
			if info.Detected != c.wantDetected || info.Readiness != c.wantReadiness || info.DiagnosticKey != c.wantKey {
				t.Errorf("detected %v, readiness %s, diagnostic %q (%s); want detected %v, readiness %s, diagnostic %q",
					info.Detected, info.Readiness, info.DiagnosticKey, info.Diagnostic, c.wantDetected, c.wantReadiness,
					c.wantKey)
			}
			if c.installed && (!filepath.IsAbs(info.Path) || info.Path != filepath.Join(dir, "codex")) {
				t.Errorf("path %q, want the absolute %q", info.Path, filepath.Join(dir, "codex"))
			}
			if !c.installed && (info.Path != "" || len(info.Installations) != 0) {
				t.Errorf("path %q, installations %q, for a harness that is not installed", info.Path, info.Installations)
			}
		})
	}
}

// harmflow-app#68: what the shipped daemon searches stays the usual install locations; only the code that builds the
// adapter can replace them.
func TestKnownLocationsDefault(t *testing.T) {
	home := "/Users/someone"
	want := []string{
		"/Applications/Codex.app/Contents/Resources/codex-cli/bin/codex",
		"/Users/someone/Applications/Codex.app/Contents/Resources/codex-cli/bin/codex",
		"/opt/homebrew/bin/codex",
		"/usr/local/bin/codex",
		"/Users/someone/.local/bin/codex",
		"/Users/someone/.npm-global/bin/codex",
		"/Users/someone/.bun/bin/codex",
	}
	if got := (&Adapter{Home: home}).knownLocations(); !slices.Equal(got, want) {
		t.Errorf("known locations %q, want %q", got, want)
	}
	if got := (&Adapter{Home: home, KnownLocations: []string{}}).knownLocations(); len(got) != 0 {
		t.Errorf("an empty list searches %q", got)
	}
}

// harmflow-app#70: a path set by hand is offered with the installations detection found, once, and it is the one in
// use.
func TestDetectOffersThePathSetByHand(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	pathDir, knownDir := tempDir(t), tempDir(t)
	onPath := standInAt(t, pathDir, `echo "codex-cli 0.163.0"`, signedIn)
	atKnown := standInAt(t, knownDir, `echo "codex-cli 0.150.0"`, signedIn)
	byHand := standInAt(t, tempDir(t), `echo "codex-cli 0.140.0"`, signedIn)
	gone := filepath.Join(tempDir(t), "codex")
	cases := []struct {
		name, manual                 string
		wantInstallation, wantSource string
		wantVersion, wantReadiness   string
		want                         []string
	}{
		{"none set", "", onPath, "path", "0.163.0", "ready", []string{onPath, atKnown}},
		{"one detection did not find", byHand, byHand, "manual", "0.140.0", "ready", []string{onPath, atKnown, byHand}},
		{"one detection found", atKnown, atKnown, "manual", "0.150.0", "ready", []string{onPath, atKnown}},
		{"a file that is gone", gone, gone, "manual", "", "not_installed", []string{onPath, atKnown, gone}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Adapter{Home: tempDir(t), PathEnv: pathDir, KnownLocations: []string{atKnown}}
			info := a.Detect(context.Background(), c.manual)
			if !slices.Equal(info.Installations, c.want) || info.Installation != c.wantInstallation ||
				info.Path != c.wantInstallation || info.PathSource != c.wantSource || info.Version != c.wantVersion ||
				info.Readiness != c.wantReadiness {
				t.Errorf("installations %q, installation %q, path %q (%s), version %q, readiness %s; want %q, %q (%s), %q, %s",
					info.Installations, info.Installation, info.Path, info.PathSource, info.Version, info.Readiness, c.want,
					c.wantInstallation, c.wantSource, c.wantVersion, c.wantReadiness)
			}
		})
	}
}
