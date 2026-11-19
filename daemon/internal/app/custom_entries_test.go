package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
func newEntriesEnv(t *testing.T) (e *env, codexBin, otherBin string) {
	t.Helper()
	bins := t.TempDir()
	codexBin, otherBin = filepath.Join(bins, "codex"), filepath.Join(bins, "other")
	for _, p := range []string{codexBin, otherBin} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e = newEnv(t, func(e *env) {
		e.codex.homes = []string{"~/.codex"}
		e.signer = entryHarness{e.codex}
	})
	e.app.RefreshHarnesses(t.Context())
	return e, codexBin, otherBin
}

func ptr(s string) *string { return &s }

// harmflow-app#826: the command line is read with no shell: assignments, then an executable, then arguments, with
// $HOME and ~ expanded and nothing else.
func TestEntryLineIsReadWithNoShell(t *testing.T) {
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
		{`X=a~b codex`, "codex", []string{"X=a~b"}, nil},
	} {
		got, err := parseEntryLine(c.line, "/u")
		if err != nil || got.executable != c.executable || !slices.Equal(got.assignments, c.assignments) ||
			!slices.Equal(got.args, c.args) {
			t.Errorf("parseEntryLine(%q) = %+v, %v; want executable %q, assignments %q, args %q", c.line, got, err,
				c.executable, c.assignments, c.args)
		}
	}
	for _, c := range []struct{ line, syntax string }{
		{"codex | tee log", "a pipe, |"},
		{"codex; rm -rf x", "a command separator, ;"},
		{"true && codex", "&&"},
		{"codex || true", "||"},
		{"X=$(whoami) codex", "a command substitution, $(…)"},
		{"X=`whoami` codex", "a command substitution, `…`"},
		{`X="$(whoami)" codex`, "a command substitution, $(…)"},
		{"codex > log", "a redirection, >"},
		{"codex < in", "a redirection, <"},
		{"codex &", "a background &"},
		{"X=$USER codex", "a variable other than $HOME"},
	} {
		_, err := parseEntryLine(c.line, "/u")
		if t2 := domain.TextOf(err); err == nil || t2.Key != "custom_entry_shell_syntax" || t2.Params["syntax"] != c.syntax {
			t.Errorf("parseEntryLine(%q) error = %v, want custom_entry_shell_syntax naming %q", c.line, err, c.syntax)
		}
	}
	for _, line := range []string{"CODEX_HOME=/x", "", `codex "open`} {
		if _, err := parseEntryLine(line, "/u"); err == nil {
			t.Errorf("parseEntryLine(%q) error = nil, want a refusal", line)
		}
	}
}

// harmflow-app#826: a line with shell syntax, one whose executable names no harness, and one with no executable file
// are refused at the line, and none is saved.
func TestCustomEntryRefusalsSaveNothing(t *testing.T) {
	e, codexBin, otherBin := newEntriesEnv(t)
	for _, c := range []struct{ line, key string }{
		{"CODEX_HOME=/x " + codexBin + " | tee log", "custom_entry_shell_syntax"},
		{"CODEX_HOME=/x " + otherBin, "custom_entry_no_harness"},
		{"CODEX_HOME=/x /no/such/codex", "custom_entry_executable_missing"},
	} {
		if _, err := e.app.AddCustomEntry(t.Context(), CustomEntryInput{Line: &c.line}); !refusedAt(err, "/line", c.key) {
			t.Errorf("AddCustomEntry(%q) error = %v, want %s at /line", c.line, err, c.key)
		}
	}
	e.app.mu.Lock()
	n := len(e.app.customEntries)
	e.app.mu.Unlock()
	if n != 0 {
		t.Errorf("%d custom entries saved after refusals, want none", n)
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
			t.Errorf("entry %d = name %q, readiness %s; want %q, ready, with no home in its name", i, entry.EntryName,
				entry.Readiness, want)
		}
		if len(info.Homes) != 1 || info.Homes[0].InstallationID != "~/.codex" {
			t.Errorf("the detected homes are %+v, want ~/.codex alone", info.Homes)
		}
		ids = append(ids, entry.InstallationID)
	}
	for i, id := range ids {
		task := "on-" + filepath.Base(homes[i])
		model := "model-" + filepath.Base(homes[i])
		if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML(task,
			"      installation: \""+id+"\"\n      model: "+model+"\n")}); err != nil {
			t.Fatalf("CreateTask(%s on %s) error = %v, want none", task, id, err)
		}
		r, err := e.app.RunTask(task)
		if err != nil {
			t.Fatal(err)
		}
		x := e.wait(r.ExecutionID, domain.StatusCompleted)
		want := []string{"codex 2", "codex 3"}[i]
		if len(x.Attempts) != 1 || x.Attempts[0].Installation != id || x.Attempts[0].InstallationName != want ||
			x.Attempts[0].InstallationHome != "" {
			t.Errorf("attempts %+v, want one on %s named %q with no home", x.Attempts, id, want)
		}
		record, _ := json.Marshal(x)
		if strings.Contains(string(record), homes[i]) {
			t.Errorf("the execution's record holds the entry's home %s: %s", homes[i], record)
		}
		e.codex.mu.Lock()
		run := e.codex.runs[len(e.codex.runs)-1]
		e.codex.mu.Unlock()
		wantEnv := []string{"CODEX_HOME=" + homes[i], "MARK=" + filepath.Base(homes[i])}
		if !slices.Equal(run.info.EntryEnv, wantEnv) {
			t.Errorf("the attempt was prepared with the assignments %q, want %q alone", run.info.EntryEnv, wantEnv)
		}
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
		t.Fatalf("after a restart the custom entries are %+v, want %s named Personal", codex.CustomEntries, id)
	}

	info, err = e.app.UpdateCustomEntry(t.Context(), id, CustomEntryInput{Name: ptr("Work"),
		Line: ptr("CODEX_HOME=/b " + codexBin)})
	if err != nil || len(info.CustomEntries) != 1 || info.CustomEntries[0].EntryName != "Work" ||
		!slices.Equal(info.CustomEntries[0].EntryEnv, []string{"CODEX_HOME=/b"}) {
		t.Fatalf("UpdateCustomEntry(%s) = %+v, %v; want it named Work, with CODEX_HOME=/b", id, info.CustomEntries, err)
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
		t.Fatal(err)
	}
	x := e.wait(r.ExecutionID, domain.StatusCompleted, domain.StatusFailed)
	if len(x.Attempts) == 0 || x.Attempts[len(x.Attempts)-1].Harness != domain.HarnessClaudeCode {
		t.Errorf("attempts %+v, want the execution to fail over to claude-code", x.Attempts)
	}
	e.codex.mu.Lock()
	defer e.codex.mu.Unlock()
	if len(e.codex.runs) != 0 {
		t.Errorf("%d attempts were prepared on the signed-out entry, want none", len(e.codex.runs))
	}
}

// harmflow-app#826: on the real Codex adapter, a custom entry's probes run with its own CODEX_HOME — its sign-in and
// models are its home's, a signed-out one listed as such — and an attempt on it is prepared with its assignments and
// its home, beside the homes detection finds.
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
			t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
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
		t.Errorf("the entry on %s = readiness %s, authentication %s, models %+v, name %q; want ready, signed in, model-c, "+
			"Codex 2", third, entry.Readiness, entry.Authentication, entry.Models, entry.EntryName)
	}
	if len(info.Homes) != 2 {
		t.Errorf("the detected homes are %d, want the 2 found before", len(info.Homes))
	}
	if _, err := a.CreateTask(t.Context(), TaskInput{YAML: codexTaskYAML("on-entry",
		"      installation: \""+entry.InstallationID+"\"\n      model: model-c\n")}); err != nil {
		t.Errorf("CreateTask(model-c on the entry) error = %v, want none", err)
	}
	inv, _, err := a.adapters[domain.HarnessCodex].Prepare(entry, ports.RunRequest{WorkDir: t.TempDir(), Prompt: "p",
		Env: []string{"PATH=/usr/bin:/bin", "CODEX_HOME=" + filepath.Join(user, ".codex")}})
	if err != nil {
		t.Fatal(err)
	}
	if home, _ := ports.AssignmentValue(inv.Env, "CODEX_HOME"); home != third || !slices.Contains(inv.Env, "MARK=1") {
		t.Errorf("the attempt's environment %q, want CODEX_HOME=%s and MARK=1", inv.Env, third)
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
}
