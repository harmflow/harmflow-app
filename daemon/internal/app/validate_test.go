package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
// with the test. Each probe of the stand-in has a minute, where the daemon gives Codex 15 or 20 seconds: under the load
// of a full test run, starting the newly written script alone can take longer than that.
func newCodexAppOn(t *testing.T, script string) (*App, string) {
	t.Helper()
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	bin := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cx := &codex.Adapter{Home: t.TempDir(), KnownLocations: []string{}, ProbeTimeout: time.Minute}
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

// harmflow-app#427: saving a workspace_write Claude Code task reads the user's editing rules against the task's own
// folders, resolved as an execution resolves them: its workspace, or HarmFlow's folder for the task when it sets none,
// and its repositories. A rule within them warns of nothing, and the save creates neither folder. A rule outside them
// warns, and so does one within HarmFlow's folder for a task whose workspace path does not expand, where no execution
// would run.
func TestSavingReadsEditRulesAgainstTheTasksFolders(t *testing.T) {
	a, config := newClaudeApp(t)
	home := a.Paths().UserHome
	repo := mkdir(t, filepath.Join(home, "repo"))
	ws, _ := domain.ResolvePath(filepath.Join(home, "ws"))
	resolvedRepo, _ := domain.ResolvePath(repo)
	const profile = "  permissions:\n    profile: workspace_write\n"
	const placed = "  workspace:\n    path: ~/ws\n" + profile
	for i, c := range []struct {
		name, rule, extra string
		github, warns     bool
	}{
		{name: "a rule within the workspace", rule: "Write(/" + ws + "/src/**)", extra: placed},
		{name: "a rule within HarmFlow's folder for the task", rule: "Write(/<own>/**)", extra: profile},
		{name: "a rule within a repository", rule: "Edit(/" + resolvedRepo + "/**)", github: true,
			extra: profile + fmt.Sprintf("  github:\n    repositories:\n      - name: o/r\n        localPath: %q\n", repo)},
		{name: "a rule outside the workspace", rule: "Write(//etc/**)", extra: placed, warns: true},
		{name: "a rule beside the workspace", rule: "Write(/" + ws + "-other/**)", extra: placed, warns: true},
		{name: "a workspace path that does not expand", rule: "Write(/<own>/**)", warns: true,
			extra: "  workspace:\n    path: ${HARMFLOW_UNDEFINED_FOLDER}/ws\n" + profile},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := fmt.Sprintf("edits-%d", i)
			own := filepath.Join(a.Paths().Workspace, id)
			rule := strings.ReplaceAll(c.rule, "<own>", own)
			settings := fmt.Sprintf(`{"permissions":{"allow":[%q]}}`, rule)
			if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(settings), 0o600); err != nil {
				t.Fatal(err)
			}
			doc := taskYAML(id, c.extra)
			if c.github {
				doc = strings.Replace(doc, "type: general", "type: github", 1)
			}
			saved, err := a.CreateTask(t.Context(), TaskInput{YAML: doc})
			if err != nil {
				t.Fatalf("CreateTask(%s): %v", id, err)
			}
			var edits []domain.Warning
			for _, w := range saved.Warnings {
				if w.Key == "restriction_claude_edit_rules" {
					edits = append(edits, w)
				}
			}
			switch {
			case !c.warns && len(edits) > 0:
				t.Errorf("CreateTask(%s) with the user rule %s warns %+v, want no warning about file edits", id, rule, edits)
			case c.warns && (len(edits) != 1 || edits[0].Params["sources"] != "user"):
				t.Errorf("CreateTask(%s) with the user rule %s warns %+v, want one about file edits naming the user settings",
					id, rule, edits)
			}
			for _, p := range []string{own, filepath.Join(home, "ws")} {
				if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("CreateTask(%s) left %s with Lstat error %v, want it not created", id, p, err)
				}
			}
		})
	}
}
