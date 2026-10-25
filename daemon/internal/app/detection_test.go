package app

import (
	"context"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
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
	paths := NewPaths(t.TempDir(), userHome, t.TempDir())
	events, err := eventlog.New(paths.Logs, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Deps{Paths: paths, Events: events, Logger: log.New(io.Discard, "", 0),
		Harnesses: []ports.HarnessAdapter{
			&claudecode.Adapter{Home: userHome, PathEnv: bin, Env: env, KnownLocations: []string{}},
			&codex.Adapter{Home: userHome, PathEnv: bin, Env: env, KnownLocations: []string{}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	a.RefreshHarnesses(context.Background())
	inventory := map[string]ports.HarnessInfo{}
	for _, h := range a.Harnesses() {
		inventory[h.ID] = h
	}

	c := inventory[claudecode.ID]
	if !c.Detected || c.Path != claude || c.PathSource != "path" || c.Version != "2.1.239" || c.Readiness != "ready" ||
		!slices.Equal(c.Installations, []string{claude}) {
		t.Errorf("Claude Code: detected %v, path %q (%s), version %q, readiness %s, installations %q", c.Detected, c.Path,
			c.PathSource, c.Version, c.Readiness, c.Installations)
	}
	x := inventory[codex.ID]
	if x.Detected || x.Readiness != "not_installed" || x.DiagnosticKey != "codex_not_found" || x.Diagnostic == "" ||
		x.Path != "" || len(x.Installations) != 0 {
		t.Errorf("Codex: detected %v, readiness %s, diagnostic %q (%s), path %q, installations %q", x.Detected,
			x.Readiness, x.DiagnosticKey, x.Diagnostic, x.Path, x.Installations)
	}

	var written []string
	_ = filepath.WalkDir(userHome, func(path string, _ fs.DirEntry, err error) error {
		if path != userHome {
			written = append(written, path)
		}
		return err
	})
	if len(written) != 0 {
		t.Errorf("detection wrote under the user's home: %q", written)
	}
}
