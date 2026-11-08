package app

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
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

// homesCodex answers the commands Codex's Detect runs for the home its CODEX_HOME names: signed in while the home holds
// a file signed-in.
const homesCodex = `#!/bin/sh
case "$1" in
  --version) echo "codex-cli 0.163.0" ;;
  login) if [ -f "$CODEX_HOME/signed-in" ]; then echo "Logged in using ChatGPT"; else echo "Not logged in"; exit 1; fi ;;
  exec) echo "Usage: codex exec --json --sandbox <MODE> --cd <DIR> --skip-git-repo-check" ;;
esac
`

// newCodexHomesApp is an App whose Codex is the real adapter, run on a stand-in CLI, with two Codex homes: ~/.codex,
// whose models cache lists model-a, and the one the daemon's CODEX_HOME names, in use, whose cache lists model-b. Both
// are signed in. It returns the App and the two homes. The App is not started, as newCodexApp's.
func newCodexHomesApp(t *testing.T) (a *App, first, second string) {
	t.Helper()
	user := t.TempDir()
	first, second = filepath.Join(user, ".codex"), filepath.Join(user, ".codex-b")
	for home, model := range map[string]string{first: "model-a", second: "model-b"} {
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		cache := `{"models":[{"slug":"` + model + `","supported_reasoning_levels":[{"effort":"low"}]}]}`
		for name, content := range map[string]string{"models_cache.json": cache, "signed-in": ""} {
			if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("CODEX_HOME", second)
	bin := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(bin, []byte(homesCodex), 0o700); err != nil {
		t.Fatal(err)
	}
	cx := &codex.Adapter{Home: user, KnownLocations: []string{}, ProbeTimeout: time.Minute}
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
	a, err = New(Deps{Paths: paths, Version: "test", Location: time.UTC, Tasks: tasks, State: state,
		Executions: execs, Events: events, Validator: v, TickInterval: time.Hour, Logger: log.New(io.Discard, "", 0),
		Harnesses: []ports.HarnessAdapter{&fakeHarness{id: domain.HarnessClaudeCode}, cx}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := a.SetHarnessPath(context.Background(), domain.HarnessCodex, &bin)
	if err != nil || !info.Ready() || len(info.Homes) != 2 || info.InstallationID != "~/.codex-b" {
		t.Fatalf("SetHarnessPath(codex, %q) = ready %t, %d homes, in use %q, error %v; want ready, 2 homes, in use "+
			"~/.codex-b, no error", bin, info.Ready(), len(info.Homes), info.InstallationID, err)
	}
	return a, first, second
}

// refusedAt reports whether err is a validation error with key at path.
func refusedAt(err error, path, key string) bool {
	var refused *Error
	if !errors.As(err, &refused) || refused.Validation == nil {
		return false
	}
	for _, is := range refused.Validation.Errors {
		if is.Path == path && is.Key == key {
			return true
		}
	}
	return false
}

// harmflow-app#475: a task's model and effort are checked against the installation its profile names, or the one in
// use when it names none. The installation in use offers model-b only, ~/.codex model-a only.
func TestModelIsCheckedAgainstTheNamedInstallation(t *testing.T) {
	a, _, _ := newCodexHomesApp(t)
	if _, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("named",
		"      installation: \"~/.codex\"\n      model: model-a\n")}); err != nil {
		t.Errorf("CreateTask(model-a on ~/.codex) error = %v, want none", err)
	}
	_, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("unnamed", "      model: model-a\n")})
	if !refusedAt(err, "/spec/harness/preferred", "codex_model_unknown") {
		t.Errorf("CreateTask(model-a with no installation) error = %v, want codex_model_unknown at the profile", err)
	}
	if !strings.Contains(err.Error(), "model-a") {
		t.Errorf("CreateTask(model-a with no installation) error = %v, want it to name the model", err)
	}
	if _, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("in-use", "      model: model-b\n")}); err != nil {
		t.Errorf("CreateTask(model-b with no installation) error = %v, want none", err)
	}
}

// harmflow-app#475: saving a task whose installation key names one the harness does not list, or one that says it is
// signed out, fails at the key; importing it keeps it, with a warning there, as a missing skill is.
func TestInstallationKeyIsValidated(t *testing.T) {
	a, first, _ := newCodexHomesApp(t)
	const path = "/spec/harness/preferred/installation"
	_, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("gone", "      installation: \"~/.nowhere\"\n")})
	if !refusedAt(err, path, "harness_installation_missing") {
		t.Errorf("CreateTask(an installation not listed) error = %v, want harness_installation_missing at %s", err, path)
	}
	imported, err := a.ImportTask(t.Context(), codexTaskYAML("gone", "      installation: \"~/.nowhere\"\n"), false)
	if err != nil || !slices.ContainsFunc(imported.Warnings, func(w domain.Warning) bool {
		return w.Key == "harness_installation_missing" && w.Subject == path
	}) {
		t.Errorf("ImportTask(an installation not listed) = %+v, %v; want it kept with a warning at %s", imported.Warnings,
			err, path)
	}

	if err := os.Remove(filepath.Join(first, "signed-in")); err != nil {
		t.Fatal(err)
	}
	if info := a.detect(t.Context(), domain.HarnessCodex); info.Homes[0].Authentication != "invalid" ||
		info.Authentication != "valid" {
		t.Fatalf("detect() after ~/.codex signed out = %+v, want it alone signed out", info.Homes)
	}
	_, err = a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("signed-out", "      installation: \"~/.codex\"\n")})
	if !refusedAt(err, path, "harness_installation_signed_out") {
		t.Errorf("CreateTask(a signed-out installation) error = %v, want harness_installation_signed_out at %s", err, path)
	}
}

// harmflow-app#475: a fallback on another installation of the preferred harness, with the same model, runs under
// another account, so it is no repeat of the preferred profile.
func TestFallbackOnAnotherInstallationIsNoRepeat(t *testing.T) {
	a, _, _ := newCodexHomesApp(t)
	// warnings gives the keys of the warnings CreateTask gives a task whose fallback is extra.
	warnings := func(id, extra string) []string {
		t.Helper()
		saved, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML(id, extra)})
		if err != nil {
			t.Fatalf("CreateTask(%q) error = %v, want none", extra, err)
		}
		keys := make([]string, 0, len(saved.Warnings))
		for _, w := range saved.Warnings {
			keys = append(keys, w.Key)
		}
		return keys
	}
	got := warnings("same", "    fallback:\n      candidates:\n        - provider: codex\n")
	if !slices.Contains(got, "fallback_repeats_preferred") {
		t.Errorf("CreateTask(fallback on the installation in use) warnings = %q, want fallback_repeats_preferred", got)
	}
	got = warnings("other", "    fallback:\n      candidates:\n        - provider: codex\n          installation: \"~/.codex\"\n")
	if slices.Contains(got, "fallback_repeats_preferred") {
		t.Errorf("CreateTask(fallback on ~/.codex) warnings = %q, want no fallback_repeats_preferred", got)
	}
}

// harmflow-app#475: a task whose profile names an installation runs on it, its home given to the harness and recorded
// on the attempt; a task whose profile names none runs on the installation in use, as before.
func TestTaskRunsOnTheInstallationItNames(t *testing.T) {
	e := newEnv(t, func(e *env) { e.codex.homes = []string{"~/.codex", "~/.codex-b"} })
	e.app.RefreshHarnesses(t.Context())
	for _, c := range []struct{ task, extra, want string }{
		{"named", "      installation: \"~/.codex-b\"\n", "~/.codex-b"},
		{"unnamed", "", "~/.codex"},
	} {
		t.Run(c.task, func(t *testing.T) {
			if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML(c.task, c.extra)}); err != nil {
				t.Fatalf("CreateTask(%s) error = %v, want none", c.task, err)
			}
			r, err := e.app.RunTask(c.task)
			if err != nil {
				t.Fatal(err)
			}
			x := e.wait(r.ExecutionID, domain.StatusCompleted)
			home := filepath.Join("/homes", c.want)
			if len(x.Attempts) != 1 || x.Attempts[0].Installation != c.want || x.Attempts[0].InstallationHome != home {
				t.Errorf("attempts %+v, want one on %s at %s", x.Attempts, c.want, home)
			}
			e.codex.mu.Lock()
			last := e.codex.preparedOn[len(e.codex.preparedOn)-1]
			e.codex.mu.Unlock()
			if last != home {
				t.Errorf("the attempt was prepared with the home %q, want %q", last, home)
			}
		})
	}
}

// harmflow-app#475: a task whose installation was removed after it was saved does not start there: its attempt reads
// the harness unavailable, naming the installation, and the execution fails as with no harness to fail over to.
func TestTaskOnARemovedInstallationDoesNotStart(t *testing.T) {
	e := newEnv(t, func(e *env) { e.codex.homes = []string{"~/.codex", "~/.codex-b"} })
	e.app.RefreshHarnesses(t.Context())
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("removed",
		"      installation: \"~/.codex-b\"\n")}); err != nil {
		t.Fatal(err)
	}
	e.codex.mu.Lock()
	e.codex.homes = []string{"~/.codex"}
	e.codex.mu.Unlock()
	r, err := e.app.RunTask("removed")
	if err != nil {
		t.Fatal(err)
	}
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	if !strings.Contains(x.ErrorMessage, `no installation "~/.codex-b"`) {
		t.Errorf("execution error %q, want it to name the installation that is gone", x.ErrorMessage)
	}
	e.codex.mu.Lock()
	defer e.codex.mu.Unlock()
	if len(e.codex.preparedOn) != 0 {
		t.Errorf("attempts prepared on %q, want none", e.codex.preparedOn)
	}
}
