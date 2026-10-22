package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// tempDir is a temporary directory by its real path: on macOS the temporary directories are under /var, a symlink to
// /private/var, and the adapter resolves the path it uses.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// standInAt puts a signed-in stand-in reporting the given version in dir, under the CLI's name.
func standInAt(t *testing.T, dir, version string) string {
	t.Helper()
	data, err := os.ReadFile(standIn(t, `echo "`+version+` (Claude Code)"`, `echo '{"loggedIn": true}'`))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// FS-004 FR-001, AC-001 (harmflow-app#68): detection searches PATH, then the known locations, and keeps every
// installation it finds in that order. The first is the one used, and its source says where it was found.
func TestDetectOnPathAndInKnownLocations(t *testing.T) {
	cases := []struct {
		name              string
		onPath, atKnown   bool
		wantSource        string
		wantVersion       string
		wantInstallations func(onPath, atKnown string) []string
	}{
		{"on PATH", true, false, "path", "2.1.239", func(p, _ string) []string { return []string{p} }},
		{"in a known location", false, true, "known_location", "2.1.200", func(_, k string) []string { return []string{k} }},
		{"in both", true, true, "path", "2.1.239", func(p, k string) []string { return []string{p, k} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			empty, pathDir, knownDir := tempDir(t), tempDir(t), tempDir(t)
			onPath, atKnown := filepath.Join(pathDir, "claude"), filepath.Join(knownDir, "claude")
			if c.onPath {
				standInAt(t, pathDir, "2.1.239")
			}
			if c.atKnown {
				standInAt(t, knownDir, "2.1.200")
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

// FS-004 FR-006, AC-007 (harmflow-app#68): each step of detection has its readiness, and the path detection found is
// absolute.
func TestDetectReadinessAtEachStep(t *testing.T) {
	signedIn, signedOut := `echo '{"loggedIn": true}'`, `echo '{"loggedIn": false, "authMethod": "none"}'; exit 1`
	version := `echo "2.1.239 (Claude Code)"`
	cases := []struct {
		name, version, auth    string
		installed              bool
		wantDetected           bool
		wantReadiness, wantKey string
	}{
		{"not found", "", "", false, false, "not_installed", "claude_not_found"},
		{"--version fails", `echo "error: cannot start" >&2; exit 1`, signedIn, true, true, "installed", "command_failed"},
		{"signed out", version, signedOut, true, true, "not_ready", "claude_not_signed_in"},
		{"signed in", version, signedIn, true, true, "ready", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := tempDir(t)
			if c.installed {
				data, err := os.ReadFile(standIn(t, c.version, c.auth))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "claude"), data, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			a := &Adapter{Home: tempDir(t), PathEnv: dir, KnownLocations: []string{}}
			info := a.Detect(context.Background(), "")
			if info.Detected != c.wantDetected || info.Readiness != c.wantReadiness || info.DiagnosticKey != c.wantKey {
				t.Errorf("detected %v, readiness %s, diagnostic %q (%s); want detected %v, readiness %s, diagnostic %q",
					info.Detected, info.Readiness, info.DiagnosticKey, info.Diagnostic, c.wantDetected, c.wantReadiness,
					c.wantKey)
			}
			if c.installed && (!filepath.IsAbs(info.Path) || info.Path != filepath.Join(dir, "claude")) {
				t.Errorf("path %q, want the absolute %q", info.Path, filepath.Join(dir, "claude"))
			}
			if !c.installed && (info.Path != "" || len(info.Installations) != 0) {
				t.Errorf("path %q, installations %q, for a harness that is not installed", info.Path, info.Installations)
			}
		})
	}
}

// FS-004 FR-006 (harmflow-app#68): a symlinked installation, as a package manager links one, is used by the path it
// resolves to.
func TestDetectResolvesASymlinkedPath(t *testing.T) {
	targetDir, pathDir := tempDir(t), tempDir(t)
	resolved := standInAt(t, targetDir, "2.1.239")
	link := filepath.Join(pathDir, "claude")
	if err := os.Symlink(resolved, link); err != nil {
		t.Fatal(err)
	}
	info := (&Adapter{Home: tempDir(t), PathEnv: pathDir, KnownLocations: []string{}}).Detect(context.Background(), "")
	if info.Path != resolved || info.PathSource != "path" || info.Readiness != "ready" || !slices.Equal(info.Installations, []string{link}) {
		t.Errorf("path %q (%s), readiness %s, installations %q; want %q found on PATH, ready, installations [%q]",
			info.Path, info.PathSource, info.Readiness, info.Installations, resolved, link)
	}
}

// harmflow-app#68: what the shipped daemon searches stays the usual install locations; only the code that builds the
// adapter can replace them.
func TestKnownLocationsDefault(t *testing.T) {
	home := "/Users/someone"
	want := []string{
		"/Users/someone/.claude/local/claude",
		"/Users/someone/.local/bin/claude",
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
		"/Users/someone/.npm-global/bin/claude",
		"/Users/someone/.bun/bin/claude",
	}
	if got := (&Adapter{Home: home}).knownLocations(); !slices.Equal(got, want) {
		t.Errorf("known locations %q, want %q", got, want)
	}
	if got := (&Adapter{Home: home, KnownLocations: []string{}}).knownLocations(); len(got) != 0 {
		t.Errorf("an empty list searches %q", got)
	}
}
