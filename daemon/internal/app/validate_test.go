package app

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/codex"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// standInCodex answers the commands Codex's Detect runs, as a signed-in Codex of the tested version does.
const standInCodex = `#!/bin/sh
case "$1" in
  --version) echo "codex-cli 0.163.0" ;;
  login) echo "Logged in using ChatGPT" ;;
  exec) echo "Usage: codex exec --json --sandbox <MODE> --cd <DIR> --skip-git-repo-check" ;;
esac
`

// newCodexApp is an App whose Codex is the real adapter, run on a stand-in CLI, with a temporary CODEX_HOME, which it
// returns. The App is not started: every detection is one the test asks for, on the stand-in's path, so no Codex
// installed on this machine ever runs.
func newCodexApp(t *testing.T) (*App, string) {
	t.Helper()
	return newCodexAppOn(t, standInCodex)
}

// newCodexAppOn is newCodexApp on the stand-in script given; its Codex adapter, and the app server it may start, end
// with the test.
func newCodexAppOn(t *testing.T, script string) (*App, string) {
	t.Helper()
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	bin := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cx := &codex.Adapter{Home: t.TempDir(), KnownLocations: []string{}}
	t.Cleanup(cx.Close)
	paths := NewPaths(t.TempDir(), t.TempDir(), t.TempDir())
	tasks, _ := fsstore.NewTasks(paths.Tasks)
	state, _ := fsstore.NewState(paths.State)
	execs, _ := fsstore.NewExecutions(paths.Executions)
	events, _ := eventlog.New(paths.Logs, time.Now)
	v, err := schema.New()
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Deps{Paths: paths, Version: "test", Location: time.UTC, Tasks: tasks, State: state,
		Executions: execs, Events: events, Validator: v, TickInterval: time.Hour, Logger: log.New(io.Discard, "", 0),
		Harnesses: []ports.HarnessAdapter{&fakeHarness{id: domain.HarnessClaudeCode}, cx}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := a.SetHarnessPath(context.Background(), domain.HarnessCodex, &bin)
	if err != nil || !info.Ready() || info.CapabilitySource != "declared" {
		t.Fatalf("Codex on the stand-in: %v %+v", err, info)
	}
	return a, codexHome
}

func codexTaskYAML(id, profile string) string {
	return strings.Replace(taskYAML(id, profile), "provider: claude-code", "provider: codex", 1)
}

func uncheckedAt(warnings []domain.Warning, path string) bool {
	for _, w := range warnings {
		if w.Key == "codex_profile_unchecked" && w.Subject == path {
			return true
		}
	}
	return false
}

// With no readable models cache, a Codex model or effort other than default is saved, as the preferred harness and as
// a fallback, with a warning at the profile's path that it was not checked; a default profile saves with no such
// warning. With the cache, an unknown model or effort is refused as before.
func TestUncheckedCodexProfileSavesWithAWarning(t *testing.T) {
	a, codexHome := newCodexApp(t)
	saved, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("pick", "      model: no-such-model\n      effort: ultra\n")})
	if err != nil {
		t.Fatalf("an unchecked model was refused: %v", err)
	}
	if !uncheckedAt(saved.Warnings, "/spec/harness/preferred") {
		t.Errorf("the preferred profile saved with no warning: %+v", saved.Warnings)
	}
	fallback, err := a.CreateTask(t.Context(), TaskInput{YAML: taskYAML("fallback",
		"    fallback:\n      candidates:\n        - provider: codex\n          effort: ultra\n")})
	if err != nil {
		t.Fatalf("an unchecked fallback was refused: %v", err)
	}
	if !uncheckedAt(fallback.Warnings, "/spec/harness/fallback/candidates/0") {
		t.Errorf("the fallback saved with no warning: %+v", fallback.Warnings)
	}
	plain, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("plain", "      model: default\n")})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range plain.Warnings {
		if w.Key == "codex_profile_unchecked" {
			t.Errorf("a default profile warns: %+v", w)
		}
	}

	if err := os.WriteFile(filepath.Join(codexHome, "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-test","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if info := a.detect(context.Background(), domain.HarnessCodex); info.CapabilitySource != "reported" {
		t.Fatalf("the cache was not read: %+v", info)
	}
	for _, c := range []struct{ profile, key string }{
		{"      model: no-such-model\n", "codex_model_unknown"},
		{"      model: gpt-test\n      effort: ultra\n", "codex_effort_invalid"},
	} {
		t.Run(c.key, func(t *testing.T) {

			_, err := a.UpdateTask(t.Context(), "pick", TaskInput{YAML: codexTaskYAML("pick", c.profile), BaseRevision: saved.Revision})
			var refused *Error
			if !errors.As(err, &refused) || refused.Key != c.key {
				t.Errorf("UpdateTask(profile %q) error = %v, want %s", c.profile, err, c.key)
				return
			}
			if r := refused.Validation; len(r.Errors) == 0 || r.Errors[0].Path != "/spec/harness/preferred" {
				t.Errorf("UpdateTask(profile %q) errors = %+v, want the first at /spec/harness/preferred", c.profile, r.Errors)
			}
		})
	}
	checked, err := a.UpdateTask(t.Context(), "pick", TaskInput{YAML: codexTaskYAML("pick", "      model: gpt-test\n      effort: high\n"),
		BaseRevision: saved.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if uncheckedAt(checked.Warnings, "/spec/harness/preferred") {
		t.Errorf("a checked profile warns: %+v", checked.Warnings)
	}
}
