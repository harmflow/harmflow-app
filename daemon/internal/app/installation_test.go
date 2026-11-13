package app

import (
	"context"
	"errors"
	"io"
	"log"
	"maps"
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
	return newCodexHomesAppWith(t, modelsCache("model-a", "low"), modelsCache("model-b", "low"))
}

// modelsCache is a Codex models cache that lists one model, which accepts the efforts.
func modelsCache(model string, efforts ...string) string {
	levels := make([]string, len(efforts))
	for i, effort := range efforts {
		levels[i] = `{"effort":"` + effort + `"}`
	}
	return `{"models":[{"slug":"` + model + `","supported_reasoning_levels":[` + strings.Join(levels, ",") + `]}]}`
}

// newCodexHomesAppWith is newCodexHomesApp with the models cache of ~/.codex and of the home in use given.
func newCodexHomesAppWith(t *testing.T, firstCache, secondCache string) (a *App, first, second string) {
	t.Helper()
	user := t.TempDir()
	first, second = filepath.Join(user, ".codex"), filepath.Join(user, ".codex-b")
	for home, cache := range map[string]string{first: firstCache, second: secondCache} {
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
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

// harmflow-app#475: validation fails a task whose installation key names one the harness does not list, or one that
// says it is signed out, at the key: on save, on import, and when the daemon checks the task files it loaded, so a task
// saved on an installation that has signed out since loads as invalid.
func TestInstallationKeyIsValidated(t *testing.T) {
	a, first, _ := newCodexHomesApp(t)
	const path = "/spec/harness/preferred/installation"
	_, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("gone", "      installation: \"~/.nowhere\"\n")})
	if !refusedAt(err, path, "harness_installation_missing") {
		t.Errorf("CreateTask(an installation not listed) error = %v, want harness_installation_missing at %s", err, path)
	}
	_, err = a.ImportTask(t.Context(), codexTaskYAML("gone", "      installation: \"~/.nowhere\"\n"), false)
	if !refusedAt(err, path, "harness_installation_missing") {
		t.Errorf("ImportTask(an installation not listed) error = %v, want harness_installation_missing at %s", err, path)
	}
	if _, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("saved", "      installation: \"~/.codex\"\n")}); err != nil {
		t.Fatalf("CreateTask(~/.codex, signed in) error = %v, want none", err)
	}
	// The check once the harnesses are detected validates again only the files loaded at start (#950), so the task
	// saved on ~/.codex is loaded as the daemon loads it at start.
	if err := a.loadTasks(t.Context()); err != nil {
		t.Fatalf("loadTasks() error = %v, want none", err)
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
	_, err = a.ImportTask(t.Context(), codexTaskYAML("signed-out", "      installation: \"~/.codex\"\n"), false)
	if !refusedAt(err, path, "harness_installation_signed_out") {
		t.Errorf("ImportTask(a signed-out installation) error = %v, want harness_installation_signed_out at %s", err, path)
	}
	a.checkTasks(t.Context())
	a.mu.Lock()
	invalid := a.tasks["saved"].invalid
	a.mu.Unlock()
	if invalid == nil || !slices.ContainsFunc(invalid.Errors, func(is schema.Issue) bool {
		return is.Path == path && is.Params["detailKey"] == "harness_installation_signed_out"
	}) {
		t.Errorf("checkTasks() after ~/.codex signed out left the task saved on it with errors %+v, want "+
			"harness_installation_signed_out at %s", invalid, path)
	}
}

// harmflow-app#475: each installation shows the skills its own list holds, read with its home at detection, and the
// quota the check before an attempt on it read with its home, and none of another's.
func TestEachInstallationShowsItsSkillsAndQuota(t *testing.T) {
	resets := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	e := newEnv(t, func(e *env) {
		e.codex.homes = []string{"~/.codex", "~/.codex-b"}
		e.codex.homeSkills = map[string][]string{"/homes/~/.codex": {"release-notes", "pdf"}, "/homes/~/.codex-b": {"triage"}}
		e.codex.quota = func() (domain.Quota, error) {
			return domain.Quota{Windows: []domain.QuotaWindow{{Name: "primary", UsedPercent: 40, ResetsAt: resets}}}, nil
		}
	})
	e.app.RefreshHarnesses(t.Context())
	for _, c := range []struct{ task, extra string }{{"named", "      installation: \"~/.codex-b\"\n"}, {"unnamed", ""}} {
		if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML(c.task, c.extra)}); err != nil {
			t.Fatalf("CreateTask(%s) error = %v, want none", c.task, err)
		}
		r, err := e.app.RunTask(c.task)
		if err != nil {
			t.Fatal(err)
		}
		e.wait(r.ExecutionID, domain.StatusCompleted)
	}
	var codexInfo ports.HarnessInfo
	for _, h := range e.app.Harnesses() {
		if h.ID == domain.HarnessCodex {
			codexInfo = h
		}
	}
	want := map[string][]string{"~/.codex": {"pdf", "release-notes"}, "~/.codex-b": {"triage"}}
	if len(codexInfo.Homes) != 2 {
		t.Fatalf("Harnesses() lists Codex homes %+v, want two", codexInfo.Homes)
	}
	for _, home := range codexInfo.Homes {
		if !slices.Equal(home.Skills, want[home.InstallationID]) {
			t.Errorf("Harnesses() gives %s the skills %q, want %q", home.InstallationID, home.Skills, want[home.InstallationID])
		}
		if q := home.Quota; q == nil || len(q.Windows) != 1 || q.Windows[0].Name != "primary" ||
			q.Windows[0].UsedPercent != 40 || q.Windows[0].ResetsAt == nil || !q.Windows[0].ResetsAt.Equal(resets) ||
			q.ReadAt.IsZero() {
			t.Errorf("Harnesses() gives %s the quota %+v, want the primary window at 40%% resetting at %v", home.InstallationID,
				q, resets)
		}
	}
	e.codex.mu.Lock()
	read := slices.Sorted(slices.Values(e.codex.quotaHomes))
	e.codex.mu.Unlock()
	if !slices.Equal(read, []string{"/homes/~/.codex", "/homes/~/.codex-b"}) {
		t.Errorf("the quota was read with the homes %q, want each home once, by the check before its attempt", read)
	}
	if codexInfo.Skills != nil || codexInfo.Quota != nil {
		t.Errorf("Harnesses() gives the harness itself skills %q and quota %+v, want them on its homes alone",
			codexInfo.Skills, codexInfo.Quota)
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

// harmflow-app#475: the sign-in the daemon starts is the one of the installation in use, so an attempt that ends
// signed out on another installation starts none, which would sign the wrong home in; one on the installation in use
// still starts it.
func TestSignedOutAttemptOnAnotherInstallationStartsNoSignIn(t *testing.T) {
	signer := newCodexSigningHarness(true)
	signer.homes = []string{"~/.codex", "~/.codex-b"}
	e := signInEnv(t, signer, 0)
	signer.setScript(func(ports.RunRequest) string { return "echo 'END unavailable'" })
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("other", "      installation: \"~/.codex-b\"\n")}); err != nil {
		t.Fatal(err)
	}
	r, err := e.app.RunTask("other")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(r.ExecutionID, domain.StatusFailed)
	time.Sleep(200 * time.Millisecond) // a sign-in starts on a goroutine of its own
	if n := signer.started(); n != 0 {
		t.Errorf("%d sign-ins started after a signed-out attempt on ~/.codex-b, want none", n)
	}

	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("in-use", "")}); err != nil {
		t.Fatal(err)
	}
	if r, err = e.app.RunTask("in-use"); err != nil {
		t.Fatal(err)
	}
	e.wait(r.ExecutionID, domain.StatusFailed)
	e.waitSignIns(signer, 1)
	signer.end <- ports.SignInResult{Outcome: ports.SignInSucceeded}
	e.waitPublished(domain.EventHarnessSignInCompleted)
}

// newHomesEnv starts an App on fake harnesses whose Codex lists two installations, ~/.codex, in use, and ~/.codex-b,
// with Claude Code exhausting its quota on every attempt, so a task whose preferred harness is Claude Code fails over.
func newHomesEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, func(e *env) { e.codex.homes = []string{"~/.codex", "~/.codex-b"} })
	e.app.RefreshHarnesses(t.Context())
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END quota'" })
	return e
}

// codexHomes are the homes the fake Codex prepared its attempts with, in order.
func (e *env) codexHomes() []string {
	e.codex.mu.Lock()
	defer e.codex.mu.Unlock()
	return slices.Clone(e.codex.preparedOn)
}

// harmflow-app#1029: a fallback candidate runs on the installation it names. Claude Code exhausts its quota, and the
// second attempt, on the Codex candidate, is prepared with the home of the installation the candidate names, which is
// not the one in use, and records that installation and its home; a candidate that names none runs on the installation
// in use.
func TestFallbackRunsOnTheInstallationItNames(t *testing.T) {
	for _, c := range []struct{ name, extra, want string }{
		{"named", "          installation: \"~/.codex-b\"\n", "~/.codex-b"},
		{"unnamed", "", "~/.codex"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// An App of its own: the quota one case exhausts does not hold the other's first attempt.
			e := newHomesEnv(t)
			if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: taskYAML("fallback", codexFallback+c.extra)}); err != nil {
				t.Fatalf("CreateTask(fallback) error = %v, want none", err)
			}
			r, err := e.app.RunTask("fallback")
			if err != nil {
				t.Fatal(err)
			}
			x := e.wait(r.ExecutionID, domain.StatusCompleted)
			home := filepath.Join("/homes", c.want)
			if len(x.Attempts) != 2 || x.Attempts[1].Harness != domain.HarnessCodex || x.Attempts[1].Installation != c.want ||
				x.Attempts[1].InstallationHome != home {
				t.Errorf("attempts %+v, want two, the second on Codex's %s at %s", x.Attempts, c.want, home)
			}
			if got := e.codexHomes(); !slices.Equal(got, []string{home}) {
				t.Errorf("Codex prepared attempts with the homes %q, want only %q", got, home)
			}
		})
	}
}

// goneCandidate is a fallback candidate on Codex's ~/.codex-b, the installation runRemoved removes once the task is
// saved, and missingInstallation is what the execution says of it.
const (
	goneCandidate       = "        - provider: codex\n          installation: \"~/.codex-b\"\n"
	missingInstallation = `no installation "~/.codex-b"`
)

// runRemoved saves a task whose preferred Claude Code exhausts its quota and whose fallback candidates are candidates,
// removes the installation ~/.codex-b, runs the task, and returns its execution once it has ended with want. It fails
// the test unless a harness.unavailable event of the execution, from Codex, names the missing installation.
func runRemoved(t *testing.T, candidates string, want domain.ExecutionStatus) (*env, ExecutionView) {
	t.Helper()
	e := newHomesEnv(t)
	// Two switches: to the candidate whose installation is gone, and on to the next.
	fallback := "    fallback:\n      maxSwitches: 2\n      candidates:\n" + candidates
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: taskYAML("removed", fallback)}); err != nil {
		t.Fatalf("CreateTask(removed) error = %v, want none", err)
	}
	e.codex.mu.Lock()
	e.codex.homes = []string{"~/.codex"}
	e.codex.mu.Unlock()
	r, err := e.app.RunTask("removed")
	if err != nil {
		t.Fatal(err)
	}
	x := e.wait(r.ExecutionID, want)
	var got []string // each harness.unavailable event of the execution, as its harness and its message
	named := false
	for _, ev := range e.published(domain.EventHarnessUnavailable) {
		if ev.ExecutionID != r.ExecutionID {
			continue
		}
		got = append(got, ev.Harness+": "+ev.Message)
		named = named || ev.Harness == domain.HarnessCodex && strings.Contains(ev.Message, missingInstallation)
	}
	if !named {
		t.Errorf("the execution's %s events = %q, want one from %s naming %s", domain.EventHarnessUnavailable, got,
			domain.HarnessCodex, missingInstallation)
	}
	return e, x
}

// harmflow-app#1029: a fallback candidate whose installation was removed after the task was saved is skipped: no
// attempt is prepared on it, the execution names the missing installation, and the chain goes on to the next candidate.
func TestFallbackOnARemovedInstallationIsSkipped(t *testing.T) {
	e, x := runRemoved(t, goneCandidate+"        - provider: codex\n", domain.StatusCompleted)
	if len(x.Attempts) != 2 || x.Attempts[1].Installation != "~/.codex" {
		t.Errorf("attempts %+v, want two, the second on Codex's ~/.codex", x.Attempts)
	}
	if got := e.codexHomes(); !slices.Equal(got, []string{"/homes/~/.codex"}) {
		t.Errorf("Codex prepared attempts with the homes %q, want only /homes/~/.codex", got)
	}
}

// harmflow-app#1029: when the candidate whose installation was removed is the last one, the execution fails with an
// error that names the missing installation, and no attempt is prepared on Codex.
func TestFallbackOnARemovedInstallationWithNoneLeftFails(t *testing.T) {
	e, x := runRemoved(t, goneCandidate, domain.StatusFailed)
	if len(x.Attempts) != 1 || !strings.Contains(x.ErrorMessage, missingInstallation) {
		t.Errorf("attempts %+v, error %q; want Claude Code's alone, and the error naming %s", x.Attempts, x.ErrorMessage,
			missingInstallation)
	}
	if got := e.codexHomes(); len(got) != 0 {
		t.Errorf("Codex prepared attempts with the homes %q, want none", got)
	}
}

// harmflow-app#1029: an effort is checked against the installation the profile names, as a model is. Both homes list
// model-c; ~/.codex accepts the effort xhigh for it, and the home in use does not. A profile that names ~/.codex saves
// with xhigh; one that names no installation is refused at its path, naming the effort, and nothing is saved in its
// place; the same holds for a fallback candidate.
func TestEffortIsCheckedAgainstTheNamedInstallation(t *testing.T) {
	a, _, _ := newCodexHomesAppWith(t, modelsCache("model-c", "low", "xhigh"), modelsCache("model-c", "low"))
	// profile is a profile's keys at indent: model-c at xhigh, on ~/.codex when named is set.
	profile := func(indent string, named bool) string {
		keys := indent + "model: model-c\n" + indent + "effort: xhigh\n"
		if named {
			keys = indent + "installation: \"~/.codex\"\n" + keys
		}
		return keys
	}
	const preferred, candidate = "      ", "          "
	for _, c := range []struct {
		name, yaml, path string
	}{
		{"preferred", codexTaskYAML("preferred-unnamed", profile(preferred, false)), "/spec/harness/preferred"},
		{"fallback", taskYAML("fallback-unnamed", codexFallback+profile(candidate, false)),
			"/spec/harness/fallback/candidates/0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := a.CreateTask(t.Context(), TaskInput{YAML: c.yaml})
			if !refusedAt(err, c.path, "codex_effort_invalid") {
				t.Errorf("CreateTask(xhigh with no installation) error = %v, want codex_effort_invalid at %s", err, c.path)
			}
			if err == nil || !strings.Contains(err.Error(), `"xhigh"`) {
				t.Errorf("CreateTask(xhigh with no installation) error = %v, want it to name the effort", err)
			}
			for _, task := range a.ListTasks() {
				if strings.HasSuffix(task.ID, "-unnamed") {
					t.Errorf("ListTasks() holds %s, want no task saved in place of the refused one", task.ID)
				}
			}
		})
	}
	for _, c := range []struct{ name, yaml string }{
		{"preferred-named", codexTaskYAML("preferred-named", profile(preferred, true))},
		{"fallback-named", taskYAML("fallback-named", codexFallback+profile(candidate, true))},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := a.CreateTask(t.Context(), TaskInput{YAML: c.yaml}); err != nil {
				t.Errorf("CreateTask(%s, xhigh on ~/.codex) error = %v, want none", c.name, err)
			}
		})
	}
}

// harmflow-app#1029: two tasks, one naming each installation, run at the same time. While both attempts run, each was
// prepared with its own installation's home and records its own installation, and the environment Codex's adapter
// gives its process sets CODEX_HOME to that home alone, though the daemon's own CODEX_HOME names ~/.codex's.
func TestTwoInstallationsRunAtOnce(t *testing.T) {
	// An attempt on ~/.codex-b whose environment kept the daemon's own CODEX_HOME would carry ~/.codex's home.
	t.Setenv("CODEX_HOME", "/homes/~/.codex")
	release := filepath.Join(t.TempDir(), "release")
	e := newEnv(t, func(e *env) { e.codex.homes = []string{"~/.codex", "~/.codex-b"} })
	e.app.RefreshHarnesses(t.Context())
	e.codex.setScript(func(ports.RunRequest) string {
		return "until [ -f '" + release + "' ]; do sleep 0.05; done; echo 'END ok'"
	})
	// Release the held scripts even when the test fails before it releases them, so none outlives it.
	t.Cleanup(func() {
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Error(err)
		}
	})
	installations := map[string]string{} // by execution
	for task, installation := range map[string]string{"on-default": "~/.codex", "on-other": "~/.codex-b"} {
		if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML(task,
			"      installation: \""+installation+"\"\n")}); err != nil {
			t.Fatalf("CreateTask(%s) error = %v, want none", task, err)
		}
		r, err := e.app.RunTask(task)
		if err != nil {
			t.Fatal(err)
		}
		installations[r.ExecutionID] = installation
	}
	live := 0 // the last count the poll read
	if !e.poll(10*time.Second, func() bool {
		e.codex.mu.Lock()
		defer e.codex.mu.Unlock()
		live = e.codex.live
		return live == 2
	}) {
		t.Fatalf("%d attempts ran at once, want %d", live, 2)
	}
	e.codex.mu.Lock()
	runs := slices.Clone(e.codex.runs)
	e.codex.mu.Unlock()
	if len(runs) != 2 {
		t.Fatalf("Codex prepared %d attempts, want 2", len(runs))
	}
	homes := map[string]string{"~/.codex": "/homes/~/.codex", "~/.codex-b": "/homes/~/.codex-b"}
	cx := &codex.Adapter{Home: t.TempDir()}
	for _, run := range runs {
		want := installations[run.req.ExecutionID]
		home := homes[want]
		if run.info.InstallationID != want || run.info.Home != home {
			t.Errorf("execution %s's attempt was prepared on %q at %q, want %s at %s", run.req.ExecutionID,
				run.info.InstallationID, run.info.Home, want, home)
		}
		x, err := e.app.GetExecution(run.req.ExecutionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.Attempts) != 1 || x.Attempts[0].Outcome != domain.AttemptRunning || x.Attempts[0].Installation != want ||
			x.Attempts[0].InstallationHome != home {
			t.Errorf("execution %s's attempts %+v, want one running on %s at %s", run.req.ExecutionID, x.Attempts, want, home)
		}
		inv, _, err := cx.Prepare(run.info, run.req)
		if err != nil {
			t.Fatalf("codex Prepare(execution %s's attempt) error = %v, want none", run.req.ExecutionID, err)
		}
		var set []string
		for _, kv := range inv.Env {
			name, value, _ := strings.Cut(kv, "=")
			if name == "CODEX_HOME" {
				set = append(set, value)
			} else if slices.Contains(slices.Collect(maps.Values(homes)), value) {
				t.Errorf("execution %s's environment sets %s to an installation's home, want only CODEX_HOME to name one",
					run.req.ExecutionID, kv)
			}
		}
		if !slices.Equal(set, []string{home}) {
			t.Errorf("execution %s's environment sets CODEX_HOME to %q, want %s alone", run.req.ExecutionID, set, home)
		}
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for id := range installations {
		e.wait(id, domain.StatusCompleted)
	}
}
