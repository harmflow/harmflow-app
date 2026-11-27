package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// envStandIn is a `codex login` that writes the environment it was started with into the file ENV_OUT names, one
// assignment a line, and ends before the browser's step.
const envStandIn = `#!/bin/bash
[ "$*" = login ] || exit 2
env > "$ENV_OUT"
exit 1
`

// startedEnv runs the sign-in of info on the stand-in that records its environment, and returns the values that
// environment gives each name, in order, so a name given twice shows twice.
func startedEnv(t *testing.T, a *Adapter, info ports.HarnessInfo, out string) map[string][]string {
	t.Helper()
	if res := a.SignIn(context.Background(), info, filepath.Join(tempDir(t), "sign-in")); res.Outcome != ports.SignInEnded {
		t.Fatalf("SignIn(%s, assignments %q) = %+v, want %s once the stand-in exits", info.Path, info.EntryEnv, res,
			ports.SignInEnded)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want the environment the stand-in was started with", out, err)
	}
	env := map[string][]string{}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			env[name] = append(env[name], value)
		}
	}
	return env
}

// harmflow-app#1136: the sign-in of a custom entry starts `codex login` with the entry's assignments added to the
// daemon's environment, each winning over a variable of the same name, and the CODEX_HOME they set winning over the
// daemon's own, whether detection gave the entry that home or not, so the sign-in lands in the entry's home alone.
func TestSignInOfACustomEntryRunsWithItsAssignments(t *testing.T) {
	path := filepath.Join(tempDir(t), "codex")
	if err := os.WriteFile(path, []byte(envStandIn), 0o755); err != nil {
		t.Fatal(err)
	}
	daemonHome, entryHome := tempDir(t), tempDir(t)
	assignments := []string{"CODEX_HOME=" + entryHome, "ENTRY_MARK=on"}
	for _, c := range []struct {
		name string
		home string
	}{
		{"its home given by detection", entryHome},
		{"its assignments alone", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := filepath.Join(tempDir(t), "env")
			a := &Adapter{Home: tempDir(t), Env: []string{"PATH=/usr/bin:/bin", "ENV_OUT=" + out,
				"CODEX_HOME=" + daemonHome, "ENTRY_MARK=off"}}
			info := ports.HarnessInfo{ID: ID, Path: path, Detected: true, Home: c.home,
				EntryEnv: slices.Clone(assignments)}
			env := startedEnv(t, a, info, out)
			for name, want := range map[string][]string{"CODEX_HOME": {entryHome}, "ENTRY_MARK": {"on"}} {
				if !slices.Equal(env[name], want) {
					t.Errorf("SignIn(assignments %q, home %q) started codex with %s = %q, want %q", assignments, c.home,
						name, env[name], want)
				}
			}
		})
	}
}
