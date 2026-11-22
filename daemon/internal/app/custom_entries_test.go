package app

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// entryHarness is a fake Codex that takes custom entries: its Identify names every executable called codex, and an
// entry it probes is the installation of the CODEX_HOME its assignments set, signed out when the fake lists that home
// in signedOut, with one model named after the home.
type entryHarness struct{ *fakeHarness }

var (
	_ ports.EntryProber       = entryHarness{}
	_ ports.HarnessIdentifier = entryHarness{}
)

func (h entryHarness) Identify(_ context.Context, path string) error {
	if filepath.Base(path) != "codex" {
		return domain.TextErr("harness_not_identified", "{{path}} is not {{harness}}", "path", path, "harness", h.id)
	}
	return nil
}

func (h entryHarness) EntryExecutable(name string) string { return procutil.FindExecutable(name, "") }

func (h entryHarness) ProbeEntry(ctx context.Context, path string, assignments []string) ports.HarnessInfo {
	info := h.Detect(ctx, path)
	home, _ := ports.AssignmentValue(assignments, "CODEX_HOME")
	info.Homes, info.Path, info.Home, info.EntryEnv = nil, path, home, slices.Clone(assignments)
	info.Models = []ports.ModelInfo{{ID: "model-" + filepath.Base(home)}}
	h.mu.Lock()
	if h.signedOut[home] {
		info.Readiness, info.Authentication = "not_ready", "invalid"
	}
	h.mu.Unlock()
	return info
}

// newEntriesEnv is an env whose Codex takes custom entries, with ~/.codex as its one detected home, and the path of an
// executable called codex beside one called other.
func newEntriesEnv(t *testing.T, configure ...func(*env)) (e *env, codexBin, otherBin string) {
	t.Helper()
	bins := t.TempDir()
	codexBin, otherBin = filepath.Join(bins, "codex"), filepath.Join(bins, "other")
	for _, p := range []string{codexBin, otherBin} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatalf("WriteFile(%s) error = %v, want none", p, err)
		}
	}
	e = newEnv(t, append([]func(*env){func(e *env) {
		e.codex.homes = []string{"~/.codex"}
		e.signer = entryHarness{e.codex}
	}}, configure...)...)
	e.app.RefreshHarnesses(t.Context())
	return e, codexBin, otherBin
}

func ptr(s string) *string { return &s }

// customEntryCount is how many custom entries the app keeps.
func customEntryCount(a *App) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.customEntries)
}

// harmflow-app#826: the command line is read with no shell: assignments, then an executable, then arguments, with
// $HOME and ~ expanded and nothing else.
func TestEntryLineIsReadWithNoShell(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		for _, c := range []struct {
			line, executable string
			assignments      []string
			args             []string
		}{
			{`CODEX_HOME="$HOME/.codex-personal" codex`, "codex", []string{"CODEX_HOME=/u/.codex-personal"}, nil},
			{`CODEX_HOME=~/.codex-b MARK='a b' /opt/codex --profile x`, "/opt/codex",
				[]string{"CODEX_HOME=/u/.codex-b", "MARK=a b"}, []string{"--profile", "x"}},
			{`CLAUDE_CONFIG_DIR=${HOME}/c claude`, "claude", []string{"CLAUDE_CONFIG_DIR=/u/c"}, nil},
			{`~/bin/codex 'a$HOME' a\ b`, "/u/bin/codex", nil, []string{"a$HOME", "a b"}},
			{"codex ~\t--profile x", "codex", nil, []string{"/u", "--profile", "x"}},
			{`X=a~b codex`, "codex", []string{"X=a~b"}, nil},
		} {
			t.Run(c.line, func(t *testing.T) {
				got, err := parseEntryLine(c.line, "/u")
				if err != nil || got.executable != c.executable || !slices.Equal(got.assignments, c.assignments) ||
					!slices.Equal(got.args, c.args) {
					t.Errorf("parseEntryLine(%q) = %+v, %v; want executable %q, assignments %q, args %q", c.line, got, err,
						c.executable, c.assignments, c.args)
				}
			})
		}
	})
	t.Run("shell syntax", func(t *testing.T) {
		for _, c := range []struct{ line, syntax string }{
			{"codex | tee log", syntaxPipe},
			{"codex; rm -rf x", syntaxSeparator},
			{"true && codex", syntaxAnd},
			{"codex || true", syntaxOr},
			{"X=$(whoami) codex", syntaxSubstitution},
			{"X=`whoami` codex", syntaxSubstitution},
			{`X="$(whoami)" codex`, syntaxSubstitution},
			{"codex > log", syntaxRedirection},
			{"codex < in", syntaxRedirection},
			{"codex &", syntaxBackground},
			{"codex\nrm x", syntaxNewLine},
			{"X=$USER codex", syntaxVariable},
		} {
			t.Run(c.line, func(t *testing.T) {
				_, err := parseEntryLine(c.line, "/u")
				got := domain.TextOf(err)
				if err == nil || got.Key != "custom_entry_shell_syntax" || got.Params["syntax"] != c.syntax ||
					got.Params["syntaxName"] != syntaxNames[c.syntax] {
					t.Errorf("parseEntryLine(%q) error = %v (params %v), want custom_entry_shell_syntax naming %q, %q", c.line,
						err, got.Params, c.syntax, syntaxNames[c.syntax])
				}
			})
		}
	})
	t.Run("refused", func(t *testing.T) {
		for _, c := range []struct{ line, key string }{
			{"CODEX_HOME=/x", "custom_entry_no_executable"},
			{"", "custom_entry_no_executable"},
			{`codex "open`, "custom_entry_unclosed_quote"},
		} {
			t.Run(c.line, func(t *testing.T) {
				if _, err := parseEntryLine(c.line, "/u"); domain.TextOf(err).Key != c.key {
					t.Errorf("parseEntryLine(%q) error = %v, want %s", c.line, err, c.key)
				}
			})
		}
	})
}

// harmflow-app#826: a line with shell syntax, one whose executable names no harness, and one with no executable file
// are refused at the line, and none is saved.
func TestCustomEntryRefusalsSaveNothing(t *testing.T) {
	e, codexBin, otherBin := newEntriesEnv(t)
	for _, c := range []struct{ name, line, key string }{
		{"shell syntax", "CODEX_HOME=/x " + codexBin + " | tee log", "custom_entry_shell_syntax"},
		{"no harness", "CODEX_HOME=/x " + otherBin, "custom_entry_no_harness"},
		{"no executable", "CODEX_HOME=/x /no/such/codex", "custom_entry_executable_missing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := e.app.AddCustomEntry(t.Context(), CustomEntryInput{Line: &c.line}); !refusedAt(err, "/line", c.key) {
				t.Errorf("AddCustomEntry(%q) error = %v, want %s at /line", c.line, err, c.key)
			}
		})
	}
	if n := customEntryCount(e.app); n != 0 {
		t.Errorf("AddCustomEntry after three refusals kept %d custom entries, want none", n)
	}
}

// harmflow-app#826: two custom entries of Codex, each with its own home, are installations a task names by their
// identifier; each task runs with its own home and assignments alone, and each attempt records the entry's identifier
// and name, never its home.
func TestCustomEntriesRunWithTheirOwnHome(t *testing.T) {
	e, codexBin, _ := newEntriesEnv(t)
	homes := []string{"/accounts/personal", "/accounts/work"}
	var ids []string
	for i, home := range homes {
		info, err := e.app.AddCustomEntry(t.Context(), CustomEntryInput{
			Line: ptr("CODEX_HOME=" + home + " MARK=" + filepath.Base(home) + " " + codexBin)})
		if err != nil || len(info.CustomEntries) != i+1 {
			t.Fatalf("AddCustomEntry(%s) = %d entries, error %v; want %d entries", home, len(info.CustomEntries), err, i+1)
		}
		entry := info.CustomEntries[i]
		if want := []string{"codex 2", "codex 3"}[i]; entry.EntryName != want || !entry.Ready() ||
			strings.Contains(entry.EntryName, home) {
			t.Errorf("AddCustomEntry(%s) gives the entry name %q, readiness %s; want %q, ready, with no home in its name",
				home, entry.EntryName, entry.Readiness, want)
		}
		if len(info.Homes) != 1 || info.Homes[0].InstallationID != "~/.codex" {
			t.Errorf("AddCustomEntry(%s) gives the detected homes %+v, want ~/.codex alone", home, info.Homes)
		}
		ids = append(ids, entry.InstallationID)
	}
	for i, id := range ids {
		t.Run(id, func(t *testing.T) {
			task := "on-" + filepath.Base(homes[i])
			model := "model-" + filepath.Base(homes[i])
			if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML(task,
				"      installation: \""+id+"\"\n      model: "+model+"\n")}); err != nil {
				t.Fatalf("CreateTask(%s on %s) error = %v, want none", task, id, err)
			}
			r, err := e.app.RunTask(task)
			if err != nil {
				t.Fatalf("RunTask(%s) error = %v, want none", task, err)
			}
			x := e.wait(r.ExecutionID, domain.StatusCompleted)
			want := []string{"codex 2", "codex 3"}[i]
			if len(x.Attempts) != 1 || x.Attempts[0].Installation != id || x.Attempts[0].InstallationName != want ||
				x.Attempts[0].InstallationHome != "" {
				t.Errorf("RunTask(%s) gives the attempts %+v, want one on %s named %q with no home", task, x.Attempts, id, want)
			}
			record, err := json.Marshal(x)
			if err != nil {
				t.Fatalf("json.Marshal(execution %s) error = %v, want none", x.ID, err)
			}
			if strings.Contains(string(record), homes[i]) {
				t.Errorf("json.Marshal(execution %s) holds the entry's home %s, want none of it: %s", x.ID, homes[i], record)
			}
			e.codex.mu.Lock()
			run := e.codex.runs[len(e.codex.runs)-1]
			e.codex.mu.Unlock()
			wantEnv := []string{"CODEX_HOME=" + homes[i], "MARK=" + filepath.Base(homes[i])}
			if !slices.Equal(run.info.EntryEnv, wantEnv) {
				t.Errorf("RunTask(%s) prepared the attempt with the assignments %q, want %q alone", task, run.info.EntryEnv,
					wantEnv)
			}
		})
	}
}

// harmflow-app#826: an identifier is never given twice: a new entry after a removal, and after a restart, takes the
// next number, so a task that named a removed entry keeps failing validation instead of running on the new one.
func TestCustomEntryIdentifierIsNeverGivenAgain(t *testing.T) {
	e, codexBin, _ := newEntriesEnv(t)
	add := func(home string) string {
		t.Helper()
		info, err := e.app.AddCustomEntry(t.Context(), CustomEntryInput{Line: ptr("CODEX_HOME=" + home + " " + codexBin)})
		if err != nil || len(info.CustomEntries) == 0 {
			t.Fatalf("AddCustomEntry(%s) = %+v, %v; want the entry added", home, info.CustomEntries, err)
		}
		return info.CustomEntries[len(info.CustomEntries)-1].InstallationID
	}
	remove := func(id string) {
		t.Helper()
		if _, err := e.app.RemoveCustomEntry(t.Context(), id); err != nil {
			t.Fatalf("RemoveCustomEntry(%s) error = %v, want none", id, err)
		}
	}
	first, second := add("/a"), add("/b")
	if first != "custom-1" || second != "custom-2" {
		t.Fatalf("AddCustomEntry twice gives %s and %s, want custom-1 and custom-2", first, second)
	}
	remove(second)
	if got := add("/c"); got != "custom-3" {
		t.Errorf("AddCustomEntry after removing %s gives %s, want custom-3", second, got)
	}
	_, err := e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("gone", "      installation: \""+second+"\"\n")})
	if !refusedAt(err, "/spec/harness/preferred/installation", "harness_installation_missing") {
		t.Errorf("CreateTask(on the removed %s) error = %v, want harness_installation_missing at the key", second, err)
	}

	e = e.restart(t, func(e *env) { e.signer = entryHarness{e.codex} })
	remove(first)
	remove("custom-3")
	if got := add("/d"); got != "custom-4" {
		t.Errorf("AddCustomEntry after a restart and removing every entry gives %s, want custom-4", got)
	}
}

// harmflow-app#826: a custom entry shows the skills its own list holds and the quota the check before an attempt on it
// read, each read as the entry's installation, with its home.
func TestCustomEntryShowsItsSkillsAndQuota(t *testing.T) {
	resets := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	e, codexBin, _ := newEntriesEnv(t, func(e *env) {
		e.codex.homeSkills = map[string][]string{"/accounts/personal": {"release-notes", "pdf"}}
		e.codex.quota = func() (domain.Quota, error) {
			return domain.Quota{Windows: []domain.QuotaWindow{{Name: "primary", UsedPercent: 40, ResetsAt: resets}}}, nil
		}
	})
	info, err := e.app.AddCustomEntry(t.Context(), CustomEntryInput{Line: ptr("CODEX_HOME=/accounts/personal " + codexBin)})
	if err != nil || len(info.CustomEntries) != 1 {
		t.Fatalf("AddCustomEntry(/accounts/personal) = %+v, %v; want one entry", info.CustomEntries, err)
	}
	if got := info.CustomEntries[0].Skills; !slices.Equal(got, []string{"pdf", "release-notes"}) {
		t.Errorf("AddCustomEntry(/accounts/personal) gives the entry the skills %q, want pdf and release-notes", got)
	}
	id := info.CustomEntries[0].InstallationID
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("on-entry",
		"      installation: \""+id+"\"\n")}); err != nil {
		t.Fatalf("CreateTask(on %s) error = %v, want none", id, err)
	}
	r, err := e.app.RunTask("on-entry")
	if err != nil {
		t.Fatalf("RunTask(on-entry) error = %v, want none", err)
	}
	e.wait(r.ExecutionID, domain.StatusCompleted)
	e.codex.mu.Lock()
	read := slices.Clone(e.codex.quotaHomes)
	e.codex.mu.Unlock()
	if !slices.Contains(read, "/accounts/personal") {
		t.Errorf("RunTask(on-entry) read the quota with the homes %q, want the entry's, /accounts/personal", read)
	}
	var codexInfo ports.HarnessInfo
	for _, h := range e.app.Harnesses() {
		if h.ID == domain.HarnessCodex {
			codexInfo = h
		}
	}
	entry, found := codexInfo.InstallationNamed(id)
	if q := entry.Quota; !found || q == nil || len(q.Windows) != 1 || q.Windows[0].UsedPercent != 40 {
		t.Errorf("Harnesses() gives %s the quota %+v (found %t), want the primary window at 40%%", id, q, found)
	}
}

// harmflow-app#826: an entry is kept across restarts, renamed and edited, and removed; removing it leaves the detected
// installations as they are, and a task that still names it fails validation at its profile's installation key.
func TestCustomEntryIsKeptEditedAndRemoved(t *testing.T) {
	e, codexBin, _ := newEntriesEnv(t)
	info, err := e.app.AddCustomEntry(t.Context(), CustomEntryInput{Line: ptr("CODEX_HOME=/a " + codexBin),
		Name: ptr("Personal")})
	if err != nil || len(info.CustomEntries) != 1 || info.CustomEntries[0].EntryName != "Personal" {
		t.Fatalf("AddCustomEntry(named Personal) = %+v, %v; want one entry named Personal", info.CustomEntries, err)
	}
	id := info.CustomEntries[0].InstallationID
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("kept",
		"      installation: \""+id+"\"\n")}); err != nil {
		t.Fatalf("CreateTask(on %s) error = %v, want none", id, err)
	}

	e = e.restart(t, func(e *env) { e.signer = entryHarness{e.codex} })
	codex := e.app.harnessInfo(t.Context(), domain.HarnessCodex)
	if len(codex.CustomEntries) != 1 || codex.CustomEntries[0].InstallationID != id ||
		codex.CustomEntries[0].EntryName != "Personal" {
		t.Fatalf("harnessInfo(codex) after a restart gives the custom entries %+v, want %s named Personal",
			codex.CustomEntries, id)
	}

	info, err = e.app.UpdateCustomEntry(t.Context(), id, CustomEntryInput{Name: ptr("Work"),
		Line: ptr("CODEX_HOME=/b " + codexBin)})
	if err != nil || len(info.CustomEntries) != 1 || info.CustomEntries[0].EntryName != "Work" ||
		!slices.Equal(info.CustomEntries[0].EntryEnv, []string{"CODEX_HOME=/b"}) {
		t.Errorf("UpdateCustomEntry(%s) = %+v, %v; want it named Work, with CODEX_HOME=/b", id, info.CustomEntries, err)
	}
	if _, err := e.app.UpdateCustomEntry(t.Context(), id, CustomEntryInput{Name: ptr(" ")}); !refusedAt(err, "/name",
		"custom_entry_name_empty") {
		t.Errorf("UpdateCustomEntry(an empty name) error = %v, want custom_entry_name_empty at /name", err)
	}

	info, err = e.app.RemoveCustomEntry(t.Context(), id)
	if err != nil || len(info.CustomEntries) != 0 || len(info.Homes) != 1 || info.Homes[0].InstallationID != "~/.codex" {
		t.Fatalf("RemoveCustomEntry(%s) = entries %+v, homes %+v, %v; want no entry and ~/.codex as before", id,
			info.CustomEntries, info.Homes, err)
	}
	_, err = e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("gone", "      installation: \""+id+"\"\n")})
	if !refusedAt(err, "/spec/harness/preferred/installation", "harness_installation_missing") {
		t.Errorf("CreateTask(on the removed %s) error = %v, want harness_installation_missing at the key", id, err)
	}
	if _, err := e.app.RemoveCustomEntry(t.Context(), id); err == nil {
		t.Errorf("RemoveCustomEntry(%s) again error = nil, want not found", id)
	}
}

// harmflow-app#826: a signed-out custom entry is listed, not hidden, and is no validation matter: a task naming it
// saves, and its execution reads the entry not ready before the attempt, and fails over to the next candidate.
func TestSignedOutCustomEntryIsAReadinessMatter(t *testing.T) {
	e, codexBin, _ := newEntriesEnv(t)
	e.codex.mu.Lock()
	e.codex.signedOut = map[string]bool{"/signed-out": true}
	e.codex.mu.Unlock()
	info, err := e.app.AddCustomEntry(t.Context(), CustomEntryInput{Line: ptr("CODEX_HOME=/signed-out " + codexBin)})
	if err != nil || len(info.CustomEntries) != 1 || info.CustomEntries[0].Authentication != "invalid" {
		t.Fatalf("AddCustomEntry(signed out) = %+v, %v; want the entry listed, signed out", info.CustomEntries, err)
	}
	id := info.CustomEntries[0].InstallationID
	yaml := codexTaskYAML("out", "      installation: \""+id+"\"\n"+
		"    fallback:\n      candidates:\n        - provider: claude-code\n")
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: yaml}); err != nil {
		t.Fatalf("CreateTask(on a signed-out entry) error = %v, want none", err)
	}
	r, err := e.app.RunTask("out")
	if err != nil {
		t.Fatalf("RunTask(out) error = %v, want none", err)
	}
	x := e.wait(r.ExecutionID, domain.StatusCompleted, domain.StatusFailed)
	if len(x.Attempts) == 0 || x.Attempts[len(x.Attempts)-1].Harness != domain.HarnessClaudeCode {
		t.Errorf("RunTask(out) gives the attempts %+v, want the execution to fail over to claude-code", x.Attempts)
	}
	e.codex.mu.Lock()
	defer e.codex.mu.Unlock()
	if len(e.codex.runs) != 0 {
		t.Errorf("RunTask(out) prepared %d attempts on the signed-out entry, want none", len(e.codex.runs))
	}
}

// harmflow-app#826: on the real Codex adapter, run on the stand-in CLI, a custom entry's probes run with its own
// CODEX_HOME — its sign-in and models are its home's, a signed-out one listed as such — and an attempt on it is
// prepared with its assignments and its home, beside the homes detection finds. An executable whose --version names no
// harness is refused, by that reading.
func TestCodexCustomEntryIsProbedWithItsOwnHome(t *testing.T) {
	a, _, _ := newCodexHomesApp(t)
	bin := a.harnessInfo(t.Context(), domain.HarnessCodex).Path
	user := a.d.Paths.UserHome
	third, fourth := filepath.Join(t.TempDir(), "personal"), filepath.Join(t.TempDir(), "signed-out")
	for home, files := range map[string]map[string]string{
		third:  {"models_cache.json": modelsCache("model-c", "low"), "signed-in": ""},
		fourth: {"models_cache.json": modelsCache("model-d", "low")},
	} {
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v, want none", home, err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600); err != nil {
				t.Fatalf("WriteFile(%s) error = %v, want none", filepath.Join(home, name), err)
			}
		}
	}
	info, err := a.AddCustomEntry(t.Context(), CustomEntryInput{Line: ptr("CODEX_HOME=" + third + " MARK=1 " + bin)})
	if err != nil || len(info.CustomEntries) != 1 {
		t.Fatalf("AddCustomEntry(%s) = %+v, %v; want one entry", third, info.CustomEntries, err)
	}
	entry := info.CustomEntries[0]
	models := func(info ports.HarnessInfo) (ids []string) {
		for _, m := range info.Models {
			ids = append(ids, m.ID)
		}
		return ids
	}
	if !entry.Ready() || entry.Authentication != "valid" || !slices.Contains(models(entry), "model-c") ||
		slices.Contains(models(entry), "model-b") || entry.EntryName != "Codex 2" {
		t.Errorf("AddCustomEntry(%s) gives the entry readiness %s, authentication %s, models %+v, name %q; want ready, "+
			"signed in, model-c, Codex 2", third, entry.Readiness, entry.Authentication, entry.Models, entry.EntryName)
	}
	if len(info.Homes) != 2 {
		t.Errorf("AddCustomEntry(%s) gives %d detected homes, want the 2 found before", third, len(info.Homes))
	}
	if _, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("on-entry",
		"      installation: \""+entry.InstallationID+"\"\n      model: model-c\n")}); err != nil {
		t.Errorf("CreateTask(model-c on the entry) error = %v, want none", err)
	}
	attempt := entry
	attempt.Features = maps.Clone(entry.Features)
	if attempt.Features == nil {
		attempt.Features = map[string]bool{}
	}
	attempt.Features["app-server"] = true // every attempt runs on Codex's app server, which this fake's help omits
	inv, _, err := a.adapters[domain.HarnessCodex].Prepare(attempt, ports.RunRequest{WorkDir: t.TempDir(), Prompt: "p",
		Env: []string{"PATH=/usr/bin:/bin", "CODEX_HOME=" + filepath.Join(user, ".codex")}})
	if err != nil {
		t.Fatalf("codex Prepare(the entry's attempt) error = %v, want none", err)
	}
	if home, _ := ports.AssignmentValue(inv.Env, "CODEX_HOME"); home != third || !slices.Contains(inv.Env, "MARK=1") {
		t.Errorf("codex Prepare(the entry's attempt) gives the environment %q, want CODEX_HOME=%s and MARK=1", inv.Env, third)
	}

	info, err = a.AddCustomEntry(t.Context(), CustomEntryInput{Line: ptr("CODEX_HOME=" + fourth + " " + bin)})
	if err != nil || len(info.CustomEntries) != 2 || info.CustomEntries[1].Authentication != "invalid" ||
		info.CustomEntries[1].Ready() {
		t.Fatalf("AddCustomEntry(%s, signed out) = %+v, %v; want it listed, signed out and not ready", fourth,
			info.CustomEntries, err)
	}
	if _, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("on-signed-out",
		"      installation: \""+info.CustomEntries[1].InstallationID+"\"\n")}); err != nil {
		t.Errorf("CreateTask(on the signed-out entry) error = %v, want none: signing out is no validation matter", err)
	}

	impostor := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(impostor, []byte("#!/bin/sh\necho 'some-other-tool 1.0'\n"), 0o700); err != nil {
		t.Fatalf("WriteFile(%s) error = %v, want none", impostor, err)
	}
	if _, err := a.AddCustomEntry(t.Context(), CustomEntryInput{Line: ptr("CODEX_HOME=" + third + " " + impostor)}); !refusedAt(
		err, "/line", "custom_entry_no_harness") {
		t.Errorf("AddCustomEntry(%s, whose --version names no harness) error = %v, want custom_entry_no_harness at /line",
			impostor, err)
	}
	if n := customEntryCount(a); n != 2 {
		t.Errorf("AddCustomEntry after the refusal keeps %d custom entries, want the 2 added before", n)
	}
}
