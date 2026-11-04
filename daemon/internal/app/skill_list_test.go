package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/claudecode"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/codex"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// skillListCodex is a stand-in Codex whose app server answers skills/list with the shell commands onList, which read
// the request's id in $id, reads no managed requirements and a ChatGPT account, and appends every line it receives to
// received.
func skillListCodex(received, onList string) string {
	return `#!/bin/bash
case "$1" in
  --version) echo "codex-cli 0.163.0" ;;
  --help) echo "Commands: exec app-server" ;;
  login) echo "Logged in using ChatGPT" ;;
  exec) echo "Usage: codex exec --json --sandbox <MODE> --cd <DIR> --skip-git-repo-check" ;;
  app-server)
    while IFS= read -r line; do
      printf '%s\n' "$line" >> '` + received + `'
      id=$(printf '%s' "$line" | sed -n 's/^{"id":\([0-9]*\),.*/\1/p')
      case "$line" in
        *'"method":"initialize"'*) printf '{"id":%s,"result":{}}\n' "$id" ;;
        *'"method":"configRequirements/read"'*) printf '{"id":%s,"result":{"requirements":null}}\n' "$id" ;;
        *'"method":"account/read"'*) printf '{"id":%s,"result":{"account":{"type":"chatgpt","planType":"plus"}}}\n' "$id" ;;
        *'"method":"skills/list"'*) ` + onList + ` ;;
      esac
    done ;;
esac
`
}

// listAnswer answers skills/list with these skills, given as name and SKILL.md pairs, and these discovery errors.
func listAnswer(skills []string, errors string) string {
	var entries []string
	for i := 0; i+1 < len(skills); i += 2 {
		entries = append(entries, `{"name":"`+skills[i]+`","description":"A skill.","enabled":true,"path":"`+skills[i+1]+
			`","scope":"user"}`)
	}
	result := `{"data":[{"cwd":"/w","skills":[` + strings.Join(entries, ",") + `],"errors":[` + errors + `]}]}`
	return `printf '{"id":%s,"result":%s}\n' "$id" '` + result + `'`
}

// listParams are the params of every skills/list the stand-in received.
func listParams(t *testing.T, received string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(received)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want what the stand-in received", received, err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal([]byte(l), &m) == nil && m.Method == "skills/list" {
			out = append(out, m.Params)
		}
	}
	return out
}

func cwds(p map[string]any) []string {
	var out []string
	list, _ := p["cwds"].([]any)
	for _, c := range list {
		s, _ := c.(string)
		out = append(out, s)
	}
	return out
}

func lastCwds(t *testing.T, received string) []string {
	t.Helper()
	all := listParams(t, received)
	if len(all) == 0 {
		t.Fatal("listParams(received) = no skills/list, want at least one")
	}
	return cwds(all[len(all)-1])
}

const codexSkills = "  skills: [review-notes]\n"

// codexListingFolder is the folder the Codex adapter of a asks its skill list for: the user's home folder it was given.
func codexListingFolder(a *App) []string {
	return []string{a.adapters[domain.HarnessCodex].(*codex.Adapter).Home}
}

// A Codex user's skill is found where Codex finds it: a Codex task that cites it saves, a Claude Code task with Codex
// as its fallback imports with no invalid-fallback warning, and an execution that fails over to Codex resolves it at
// the SKILL.md Codex reports. Every lookup asks for the installation's list, in the user's home folder, whatever the
// task's workspace.
func TestCodexSkillsComeFromItsSkillList(t *testing.T) {
	received := filepath.Join(t.TempDir(), "received.jsonl")
	skill := filepath.Join(t.TempDir(), ".agents", "skills", "review-notes", "SKILL.md")
	a, _ := newCodexAppOn(t, skillListCodex(received, listAnswer([]string{"review-notes", skill}, "")))
	ctx := t.Context()
	home := codexListingFolder(a)

	if _, err := a.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("plain", codexSkills)}); err != nil {
		t.Fatalf("CreateTask(plain), citing a skill of Codex's list = %v, want no error", err)
	}
	if got := lastCwds(t, received); !slices.Equal(got, home) {
		t.Errorf("CreateTask(plain), with no workspace path, listed skills for %q, want the home folder %q", got, home)
	}
	if _, err := a.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("placed", codexSkills+"  workspace:\n    path: ~/ws\n")}); err != nil {
		t.Fatalf("CreateTask(placed), with a workspace path = %v, want no error", err)
	}
	if got := lastCwds(t, received); !slices.Equal(got, home) {
		t.Errorf("CreateTask(placed) listed skills for %q, want the home folder %q, not its workspace", got, home)
	}

	fallback := taskYAML("failover", "    fallback:\n      candidates:\n        - provider: codex\n"+codexSkills)
	imported, err := a.ImportTask(ctx, fallback, false)
	if err != nil {
		t.Fatalf("ImportTask(failover) = %v, want no error", err)
	}
	for _, w := range imported.Warnings {
		if w.Code == domain.WarningInvalidFallback {
			t.Errorf("ImportTask(failover) warns %+v, want no invalid-fallback warning for Codex", w)
		}
	}

	res := a.Validate(ctx, TaskInput{YAML: fallback})
	if res.doc == nil {
		t.Fatalf("Validate(failover) errors = %+v, want none", res.Errors)
	}
	work := t.TempDir()
	r := &run{a: a, def: *res.doc, eff: res.doc.Effective(), work: work}
	chain, invalid := r.chain(ctx)
	if len(chain) != 2 || len(invalid) != 0 {
		t.Errorf("chain() = %v, invalid %v; want Claude Code then Codex", chain, invalid)
	}
	_, skills, _, err := r.resolve(ctx, a.adapters[domain.HarnessCodex])
	if err != nil || len(skills) != 1 || skills[0].Path != filepath.Dir(skill) {
		t.Errorf("resolve(codex) = %+v, %v; want review-notes at %s", skills, err, filepath.Dir(skill))
	}
	if got := lastCwds(t, received); !slices.Equal(got, home) {
		t.Errorf("resolve(codex) listed skills for %q, want the home folder %q, not the working directory %s", got, home,
			work)
	}
}

// The catalog lists a skill of Codex's list once, with Codex once among its harnesses, from the installation's list,
// whatever workspace path or task it is asked for; what Codex could not load is in its problems and in a missing
// skill's message. A plugin's skill is offered by the name Codex
// reports, `<plugin>:<skill>`, spaces included; a skill a task cannot cite, a name with the hf- prefix or one that is
// neither an identifier nor a plugin's skill, is not offered, though Codex lists it.
func TestCatalogListsCodexSkillsOfTheInstallation(t *testing.T) {
	received := filepath.Join(t.TempDir(), "received.jsonl")
	broken := `{"path":"/x/broken/SKILL.md","message":"invalid front matter"}`
	a, _ := newCodexAppOn(t, skillListCodex(received,
		listAnswer([]string{"my-skill", "/u/my-skill/SKILL.md", "my-skill", "/r/my-skill/SKILL.md",
			"pdf:pdf", "/p/pdf/SKILL.md", "pdf:Fill Forms", "/p/fill-forms/SKILL.md",
			"hf-users-own", "/u/hf-users-own/SKILL.md", "My Skill", "/u/spaced/SKILL.md"},
			broken)))
	ctx := t.Context()
	ws := t.TempDir()

	c := a.Catalog(ctx, ws, "plain")
	entries := map[string][]CatalogEntry{}
	for _, e := range c.Skills {
		switch e.Name {
		case "my-skill", "pdf:pdf", "pdf:Fill Forms":
			entries[e.Name] = append(entries[e.Name], e)
		case "hf-users-own", "My Skill":
			t.Errorf("Catalog(ctx, %s, plain) offers %+v, want no skill a task cannot cite", ws, e)
		}
	}
	for _, name := range []string{"my-skill", "pdf:pdf", "pdf:Fill Forms"} {
		if got := entries[name]; len(got) != 1 || !slices.Equal(got[0].Harnesses, []string{domain.HarnessCodex}) {
			t.Errorf("Catalog(ctx, %s, plain) %s = %+v, want one entry, on codex once", ws, name, got)
		}
	}
	home := codexListingFolder(a)
	if got := lastCwds(t, received); !slices.Equal(got, home) {
		t.Errorf("Catalog(ctx, %s, plain) listed skills for %q, want the home folder %q", ws, got, home)
	}
	if len(c.Problems) != 1 || c.Problems[0].Path != "/x/broken/SKILL.md" || c.Problems[0].Message != "invalid front matter" ||
		c.Problems[0].Harness != domain.HarnessCodex {
		t.Errorf("Catalog(ctx, %s, plain) problems = %+v, want the SKILL.md Codex could not load", ws, c.Problems)
	}
	for _, task := range []string{"plain", ""} {
		a.Catalog(ctx, "", task)
		if got := lastCwds(t, received); !slices.Equal(got, home) {
			t.Errorf(`Catalog(ctx, "", %q) listed skills for %q, want the home folder %q`, task, got, home)
		}
	}

	_, err := a.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("gone", "  skills: [no-such-skill]\n")})
	ae, ok := err.(*Error)
	if !ok || ae.Key != "skill_not_found" || ae.Params["source"] != "skill_list_errors" ||
		!strings.Contains(ae.Message, "invalid front matter") || !strings.Contains(ae.Message, "Codex's skill list") {
		t.Errorf("CreateTask(gone) = %v (%+v), want skill_not_found naming Codex's skill list and its errors", err, ae)
	}
}

// A Codex task cites a plugin's skill by the name Codex's skill list reports, uppercase letters included. It saves when
// the list returns the skill for the task's workspace, and fails naming it when the list does not; it exports and
// imports with its names unchanged, and an import whose skill this machine's Codex lacks warns, naming it. An execution
// resolves the skill at the SKILL.md Codex reports.
func TestCodexPluginSkills(t *testing.T) {
	received := filepath.Join(t.TempDir(), "received.jsonl")
	a, _ := newCodexAppOn(t, skillListCodex(received, listAnswer([]string{"pdf:pdf", "/p/pdf/SKILL.md",
		"presentations:Presentations", "/p/presentations/SKILL.md"}, "")))
	ctx := t.Context()

	created, err := a.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("plugins", "  skills: [pdf:pdf, presentations:Presentations]\n")})
	if err != nil {
		t.Fatalf("CreateTask(plugins), citing two plugin skills of Codex's list = %v, want no error", err)
	}
	if !strings.Contains(created.YAML, "skills: [pdf:pdf, presentations:Presentations]") {
		t.Errorf("CreateTask(plugins) wrote\n%s\nwant the skills as Codex names them", created.YAML)
	}
	_, err = a.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("absent", "  skills: [spreadsheets:excel-live-control]\n")})
	ae, ok := err.(*Error)
	if !ok || ae.Key != "skill_not_found" || ae.Params["name"] != "spreadsheets:excel-live-control" ||
		!strings.Contains(ae.Message, `"spreadsheets:excel-live-control"`) {
		t.Errorf("CreateTask(absent) = %v (%+v), want skill_not_found naming spreadsheets:excel-live-control", err, ae)
	}

	exported, err := a.ExportTask("plugins")
	if err != nil {
		t.Fatalf("ExportTask(plugins) = %v, want no error", err)
	}
	if !strings.Contains(string(exported), "pdf:pdf") || !strings.Contains(string(exported), "presentations:Presentations") {
		t.Errorf("ExportTask(plugins) =\n%s\nwant both plugin skills by their names", exported)
	}
	elsewhere := strings.Replace(string(exported), "pdf:pdf", "spreadsheets:excel-live-control", 1)
	imported, err := a.ImportTask(ctx, elsewhere, true)
	if err != nil {
		t.Fatalf("ImportTask(export of plugins, citing a skill Codex lacks) = %v, want no error", err)
	}
	if !strings.Contains(imported.YAML, "skills: [spreadsheets:excel-live-control, presentations:Presentations]") {
		t.Errorf("ImportTask(export of plugins) wrote\n%s\nwant the skills' names unchanged", imported.YAML)
	}
	var missing []string
	for _, w := range imported.Warnings {
		if w.Key == "skill_missing" {
			missing = append(missing, w.Params["name"])
		}
	}
	if !slices.Equal(missing, []string{"spreadsheets:excel-live-control"}) {
		t.Errorf("ImportTask(export of plugins) warns skill_missing for %q, want only spreadsheets:excel-live-control",
			missing)
	}

	res := a.Validate(ctx, TaskInput{YAML: codexTaskYAML("plugins", "  skills: [presentations:Presentations]\n")})
	if res.doc == nil {
		t.Fatalf("Validate(plugins) errors = %+v, want none", res.Errors)
	}
	r := &run{a: a, def: *res.doc, eff: res.doc.Effective(), work: t.TempDir()}
	_, skills, _, err := r.resolve(ctx, a.adapters[domain.HarnessCodex])
	if err != nil || len(skills) != 1 || skills[0].Name != "presentations:Presentations" || skills[0].Path != "/p/presentations" {
		t.Errorf("resolve(codex) = %+v, %v; want presentations:Presentations at /p/presentations", skills, err)
	}
}

// A skill list that cannot be read is said so, never a missing skill: on save, on import, for a fallback, and as a
// Codex attempt starts, which it fails.
func TestUnreadableSkillListIsNotAMissingSkill(t *testing.T) {
	received := filepath.Join(t.TempDir(), "received.jsonl")
	a, _ := newCodexAppOn(t, skillListCodex(received,
		`printf '{"id":%s,"error":{"code":-32603,"message":"skills unavailable"}}\n' "$id"`))
	ctx := t.Context()

	_, err := a.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("unread", codexSkills)})
	ae, ok := err.(*Error)
	if !ok || ae.Key != "skill_list_unreadable" || ae.Params["detailKey"] != "codex_app_server_error" ||
		!strings.Contains(ae.Message, "skills unavailable") {
		t.Errorf("CreateTask(unread) = %v (%+v), want skill_list_unreadable with the server's error", err, ae)
	}
	imported, err := a.ImportTask(ctx, taskYAML("fb", "    fallback:\n      candidates:\n        - provider: codex\n"+codexSkills), false)
	if err != nil {
		t.Fatalf("ImportTask(fb) = %v, want no error", err)
	}
	var keys []string
	for _, w := range imported.Warnings {
		keys = append(keys, w.Key)
	}
	if !slices.Contains(keys, "fallback_skill_list_unreadable") || slices.Contains(keys, "fallback_skill_missing") {
		t.Errorf("ImportTask(fb) warns %q, want fallback_skill_list_unreadable and no fallback_skill_missing", keys)
	}
	if c := a.Catalog(ctx, "", ""); len(c.Problems) != 1 || c.Problems[0].Key != "skill_list_unreadable" {
		t.Errorf(`Catalog(ctx, "", "") problems = %+v, want one, skill_list_unreadable`, c.Problems)
	}
	res := a.Validate(ctx, TaskInput{YAML: codexTaskYAML("unread", codexSkills)})
	if res.doc == nil {
		t.Fatalf("Validate(unread) errors = %+v, want none", res.Errors)
	}
	r := &run{a: a, def: *res.doc, eff: res.doc.Effective(), work: t.TempDir()}
	_, _, _, err = r.resolve(ctx, a.adapters[domain.HarnessCodex])
	var te *domain.TextError
	if !errors.As(err, &te) || te.Key != "skill_list_unreadable" {
		t.Errorf("resolve(codex) = %v, want skill_list_unreadable", err)
	}
}

// newClaudeApp is an App whose Claude Code is the real adapter, with a temporary configuration directory, which it
// returns, and whose Codex is a fake. The adapter finds no CLI, so no Claude Code installed on this machine ever runs.
func newClaudeApp(t *testing.T) (*App, string) {
	t.Helper()
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	paths := NewPaths(t.TempDir(), t.TempDir(), t.TempDir())
	tasks, _ := fsstore.NewTasks(paths.Tasks)
	state, _ := fsstore.NewState(paths.State)
	execs, _ := fsstore.NewExecutions(paths.Executions)
	events, _ := eventlog.New(paths.Logs, time.Now)
	v, err := schema.New()
	if err != nil {
		t.Fatal(err)
	}
	cc := &claudecode.Adapter{Home: t.TempDir(), PathEnv: t.TempDir(), KnownLocations: []string{}}
	a, err := New(Deps{Paths: paths, Version: "test", Location: time.UTC, Tasks: tasks, State: state,
		Executions: execs, Events: events, Validator: v, TickInterval: time.Hour, Logger: log.New(io.Discard, "", 0),
		Harnesses: []ports.HarnessAdapter{cc, &fakeHarness{id: domain.HarnessCodex}}})
	if err != nil {
		t.Fatal(err)
	}
	return a, config
}

// newClaudeAppOn is newClaudeApp with a stand-in Claude Code CLI, whose script answers its probes and its plugin
// commands, so the adapter detects it.
func newClaudeAppOn(t *testing.T, script string) *App {
	t.Helper()
	a, _ := newClaudeApp(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cc := a.adapters[domain.HarnessClaudeCode].(*claudecode.Adapter)
	cc.PathEnv = bin
	return a
}

// newClaudeEnvOn is an env whose Claude Code is the real adapter on a stand-in CLI with this script, whose listing has
// listTimeout (zero is the adapter's default), with a temporary configuration directory, and whose Codex is the env's
// fake. As for newClaudeApp, no Claude Code installed on this machine ever runs.
func newClaudeEnvOn(t *testing.T, script string, listTimeout time.Duration) *env {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cc := &claudecode.Adapter{Home: t.TempDir(), PathEnv: bin, KnownLocations: []string{}, SkillListTimeout: listTimeout}
	return newEnv(t, func(e *env) { e.signer = cc })
}

// initClaude is a stand-in Claude Code that is signed in and, started with -p, appends its folder and arguments to log
// and prints an init line, as Claude Code 2.1.309 does, then exits. Its list holds a bundled skill, simplify, and a
// plugin's, pdf:pdf, in any folder; repo-skill in a folder named sub, as the skill of the repository above it; and
// <name>-skill for each folder <name> it adds with --add-dir. No SKILL.md is read: the list is the stand-in's answer.
func initClaude(log string) string {
	return `#!/bin/bash
case "$1" in
  --version) echo "2.1.309 (Claude Code)" ;;
  --help) echo "-p, --print  --output-format <format>  --permission-mode <mode>  --allowedTools <tools...>  --append-system-prompt <prompt>  --add-dir <dirs...>  --no-session-persistence" ;;
  auth) echo '{"loggedIn": true}' ;;
  -p)
    printf '%s|%s\n' "$(pwd -P)" "$*" >> "` + log + `"
    skills='"simplify","pdf:pdf"'
    [ "$(basename "$PWD")" = sub ] && skills="$skills"',"repo-skill"'
    while [ $# -gt 0 ]; do
      [ "$1" = --add-dir ] && skills="$skills"',"'"$(basename "$2")"'-skill"'
      shift
    done
    echo '{"type":"system","subtype":"init","skills":['"$skills"']}' ;;
esac
`
}

// listings are the -p sessions the stand-in of initClaude recorded, each its folder and arguments.
func listings(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// A Claude Code task's skills are the ones the installed Claude Code lists, and only those, whatever the task's
// workspace and the folders it adds. A task citing a bundled skill and a plugin's saves wherever it is placed; one
// citing a skill only its repository's folder, a folder it adds, or a SKILL.md on disk would bring is refused, naming
// Claude Code's skill list, as HarmFlow neither reads a task's project nor copies Claude Code's rules for finding
// skills. The task editor's catalog, an execution, and a failover to Claude Code read the same list. The list is asked
// for once, in the home folder with no folder added, and again after a refresh.
func TestClaudeCodeSkillsComeFromItsSkillList(t *testing.T) {
	log := filepath.Join(t.TempDir(), "listings.log")
	a := newClaudeAppOn(t, initClaude(log))
	ctx := t.Context()
	home := a.d.Paths.UserHome
	sub, other, extra := filepath.Join(home, "repo", "sub"), filepath.Join(home, "other"), filepath.Join(home, "extra")
	for _, d := range []string{sub, other, extra} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(t, filepath.Join(home, "repo", ".claude", "skills", "on-disk"))
	placed := func(id, skills, workspace string) string {
		return taskYAML(id, "  skills: ["+skills+"]\n  workspace:\n    path: "+workspace+"\n")
	}
	adding := func(id, skills string) string {
		return taskYAML(id, "  skills: ["+skills+"]\n  workspace:\n    path: ~/other\n"+
			"  permissions:\n    profile: custom\n    custom:\n      writablePaths: [~/extra]\n")
	}

	for _, c := range []struct{ id, yaml string }{
		{"sub", placed("sub", "simplify, pdf:pdf", "~/repo/sub")},
		{"elsewhere", placed("elsewhere", "simplify, pdf:pdf", "~/other")},
		{"adding", adding("adding", "simplify")},
	} {
		if _, err := a.CreateTask(ctx, TaskInput{YAML: c.yaml}); err != nil {
			t.Errorf("CreateTask(%s), citing a bundled skill and a plugin's = %v, want no error", c.id, err)
		}
	}
	for _, c := range []struct{ id, skill, yaml string }{
		{"repo-skill", "repo-skill", placed("repo-skill", "repo-skill", "~/repo/sub")},
		{"on-disk", "on-disk", placed("on-disk", "on-disk", "~/repo/sub")},
		{"extra-skill", "extra-skill", adding("extra-skill", "extra-skill")},
	} {
		_, err := a.CreateTask(ctx, TaskInput{YAML: c.yaml})
		ae, ok := err.(*Error)
		if !ok || ae.Key != "skill_not_found" || ae.Params["name"] != c.skill || ae.Params["source"] != "skill_list" ||
			!strings.Contains(ae.Message, "Claude Code's skill list)") {
			t.Errorf("CreateTask(%s) = %v (%+v), want skill_not_found naming %s and Claude Code's skill list", c.id, err, ae,
				c.skill)
		}
	}

	want := []string{"pdf:pdf", "simplify"}
	if got := claudeCodeOffers(ctx, a, "~/repo/sub", "sub"); !slices.Equal(got, want) {
		t.Errorf(`Catalog(ctx, "~/repo/sub", sub) offers %q on Claude Code, want %q`, got, want)
	}
	if got := claudeCodeOffers(ctx, a, "~/other", "adding", "~/extra"); !slices.Equal(got, want) {
		t.Errorf(`Catalog(ctx, "~/other", adding, "~/extra") offers %q on Claude Code, want %q`, got, want)
	}

	res := a.Validate(ctx, TaskInput{YAML: adding("adding", "simplify")})
	if res.doc == nil {
		t.Fatalf("Validate(adding) errors = %+v, want none", res.Errors)
	}
	resolvedExtra, _ := domain.ResolvePath(extra)
	r := &run{a: a, def: *res.doc, eff: res.doc.Effective(), work: other, writable: []string{resolvedExtra}}
	if _, skills, _, err := r.resolve(ctx, a.adapters[domain.HarnessClaudeCode]); err != nil || len(skills) != 1 ||
		skills[0].Name != "simplify" {
		t.Errorf("resolve(claude-code) in %s = %+v, %v; want simplify", other, skills, err)
	}

	// The fake Codex finds every skill named user-skill, so a Codex task cites it with a skill Claude Code may list.
	fallback := func(skill string) string {
		return codexTaskYAML("fb", "    fallback:\n      candidates:\n        - provider: claude-code\n"+
			"  skills: ["+skill+"]\n  workspace:\n    path: ~/repo/sub\n")
	}
	for _, c := range []struct {
		skill   string
		invalid bool
	}{{"simplify", false}, {"repo-skill", true}} {
		res := a.Validate(ctx, TaskInput{YAML: fallback(c.skill)})
		invalid := slices.ContainsFunc(res.Warnings, func(w schemaIssue) bool { return w.Code == domain.WarningInvalidFallback })
		if invalid != c.invalid {
			t.Errorf("Validate(fallback citing %s) invalid-fallback warning = %v (%+v), want %v", c.skill, invalid,
				res.Warnings, c.invalid)
		}
	}

	resolvedHome, _ := domain.ResolvePath(a.adapters[domain.HarnessClaudeCode].(*claudecode.Adapter).Home)
	only := []string{resolvedHome + "|-p --output-format stream-json --verbose --no-session-persistence"}
	if got := listings(t, log); !slices.Equal(got, only) {
		t.Errorf("tasks, catalogs, and an execution in three folders ran listings %q, want the one %q", got, only)
	}
	a.RefreshHarnesses(ctx)
	claudeCodeOffers(ctx, a, "~/repo/sub", "sub")
	if got := len(listings(t, log)); got != 2 {
		t.Errorf("the catalog after a refresh made %d listings in all, want 2", got)
	}
}

// An execution's session runs without the variables of a Claude Code session that started the daemon and without the
// daemon's own HARMFLOW_* variables, as Claude Code's skill listing does, and with the execution's own identifiers.
func TestExecutionRunsWithTheSessionEnvironment(t *testing.T) {
	for _, kv := range [][2]string{{"CLAUDECODE", "1"}, {"CLAUDE_CODE_ENTRYPOINT", "cli"},
		{"CLAUDE_CODE_SSE_PORT", "50123"}, {"HARMFLOW_EXECUTION_ID", "exec-of-the-daemon"}} {
		t.Setenv(kv[0], kv[1])
	}
	envLog := filepath.Join(t.TempDir(), "env.log")
	e := newEnv(t)
	e.create("nested", "")
	x := runTask(e, "nested", domain.StatusCompleted, `env > '`+envLog+`'; echo 'END ok'`)

	data, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want the session's environment", envLog, err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for _, k := range []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SSE_PORT"} {
		if slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, k+"=") }) {
			t.Errorf("the session ran with %s, want it left out", k)
		}
	}
	if want := "HARMFLOW_EXECUTION_ID=" + x.ID; !slices.Contains(got, want) {
		t.Errorf("the session ran without %s, want the execution's own ID in place of the daemon's", want)
	}
	for _, k := range []string{"HARMFLOW_TASK_ID=nested", "HARMFLOW_EXECUTION_DIR=", "HARMFLOW_WORKSPACE="} {
		if !slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, k) }) {
			t.Errorf("the session ran without %s…, want the execution's identifiers", k)
		}
	}
}

// A repository's local path is a folder a Claude Code task's session adds, but the skill list is the installation's: a
// skill Claude Code lists only with that folder added is not on it. The task is refused citing it, the task editor's
// catalog does not offer it even when the path is sent as addDir, and no listing adds the folder.
func TestClaudeCodeSkillOfARepositoryFolderIsNotListed(t *testing.T) {
	log := filepath.Join(t.TempDir(), "listings.log")
	a := newClaudeAppOn(t, initClaude(log))
	ctx := t.Context()
	home := a.d.Paths.UserHome
	checkout, other := filepath.Join(home, "checkout"), filepath.Join(home, "other")
	for _, d := range []string{checkout, other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	doc := strings.Replace(taskYAML("repo", "  skills: [checkout-skill]\n  workspace:\n    path: ~/other\n"+
		fmt.Sprintf("  github:\n    repositories:\n      - name: o/r\n        localPath: %q\n", checkout)),
		"type: general", "type: github", 1)

	_, err := a.CreateTask(ctx, TaskInput{YAML: doc})
	if ae, ok := err.(*Error); !ok || ae.Key != "skill_not_found" || ae.Params["name"] != "checkout-skill" {
		t.Errorf("CreateTask(repo), citing the skill of its repository's folder = %v, want skill_not_found", err)
	}
	if got, want := claudeCodeOffers(ctx, a, "~/other", "repo", checkout), []string{"pdf:pdf", "simplify"}; !slices.Equal(got, want) {
		t.Errorf(`Catalog(ctx, "~/other", repo, %s) offers %q on Claude Code, want %q`, checkout, got, want)
	}
	if slices.ContainsFunc(listings(t, log), func(l string) bool { return strings.Contains(l, "--add-dir") }) {
		t.Errorf("listings ran as %q, want none adding a folder", listings(t, log))
	}
}

// A Claude Code task cites a plugin's skill by the name Claude Code's skill list reports, and keeps it through export
// and import: the export holds it unchanged, an import of the export writes it unchanged with no warning for it, and an
// import that also cites a plugin skill the list lacks keeps both names and warns about that one alone.
func TestClaudeCodePluginSkillsSurviveExportAndImport(t *testing.T) {
	a := newClaudeAppOn(t, initClaude(filepath.Join(t.TempDir(), "listings.log")))
	ctx := t.Context()
	missing := func(warnings []domain.Warning) []string {
		var names []string
		for _, w := range warnings {
			if w.Key == "skill_missing" {
				names = append(names, w.Params["name"])
			}
		}
		return names
	}

	var exported string
	if !t.Run("saves and exports", func(t *testing.T) {
		created, err := a.CreateTask(ctx, TaskInput{YAML: taskYAML("plugin", "  skills: [pdf:pdf]\n")})
		if err != nil {
			t.Fatalf("CreateTask(plugin), citing a plugin skill of Claude Code's list = %v, want no error", err)
		}
		if !strings.Contains(created.YAML, "skills: [pdf:pdf]") {
			t.Errorf("CreateTask(plugin) wrote\n%s\nwant the skill as Claude Code names it", created.YAML)
		}
		data, err := a.ExportTask("plugin")
		if err != nil {
			t.Fatalf("ExportTask(plugin) = %v, want no error", err)
		}
		if !strings.Contains(string(data), "skills: [pdf:pdf]") {
			t.Errorf("ExportTask(plugin) =\n%s\nwant the plugin skill by its name", data)
		}
		exported = string(data)
	}) {
		return
	}

	t.Run("imports as a new task", func(t *testing.T) {
		imported, err := a.ImportTask(ctx, strings.Replace(exported, "id: plugin", "id: imported", 1), false)
		if err != nil {
			t.Fatalf("ImportTask(export of plugin, as imported) = %v, want no error", err)
		}
		if !strings.Contains(imported.YAML, "id: imported") || !strings.Contains(imported.YAML, "skills: [pdf:pdf]") {
			t.Errorf("ImportTask(export of plugin, as imported) wrote\n%s\nwant the new task with the skill's name unchanged",
				imported.YAML)
		}
		if got := missing(imported.Warnings); len(got) != 0 {
			t.Errorf("ImportTask(export of plugin, as imported) warns skill_missing for %q, want none", got)
		}
	})

	t.Run("warns only about a plugin skill the list lacks", func(t *testing.T) {
		doc := strings.Replace(exported, "id: plugin", "id: imported-other", 1)
		doc = strings.Replace(doc, "skills: [pdf:pdf]", "skills: [pdf:pdf, other:skill]", 1)
		imported, err := a.ImportTask(ctx, doc, false)
		if err != nil {
			t.Fatalf("ImportTask(export of plugin, citing other:skill) = %v, want no error", err)
		}
		if !strings.Contains(imported.YAML, "skills: [pdf:pdf, other:skill]") {
			t.Errorf("ImportTask(export of plugin, citing other:skill) wrote\n%s\nwant both skills' names unchanged",
				imported.YAML)
		}
		if got := missing(imported.Warnings); !slices.Equal(got, []string{"other:skill"}) {
			t.Errorf("ImportTask(export of plugin, citing other:skill) warns skill_missing for %q, want only other:skill", got)
		}
	})
}

// writeSkill writes a SKILL.md into dir, named for the folder.
func writeSkill(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + filepath.Base(dir) + "\ndescription: A skill.\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
}

// claudeListing is a stand-in Claude Code that is signed in and, started with -p, runs the shell commands onList.
func claudeListing(onList string) string {
	return "#!/bin/bash\ncase \"$1\" in\n  --version) echo \"2.1.309 (Claude Code)\" ;;\n" +
		"  --help) echo \"-p, --print  --output-format <format>  --permission-mode <mode>  --allowedTools <tools...>  --append-system-prompt <prompt>\" ;;\n" +
		"  auth) echo '{\"loggedIn\": true}' ;;\n  -p) " + onList + " ;;\nesac\n"
}

// attemptClaude is claudeListing with an attempt's other calls of -p told apart from the listing: its usage report
// names one plan window, and its session, the one call given a permission mode, appends a line to sessions and ends
// with a successful result. The listing appends a line to listings and runs onList.
func attemptClaude(listings, onList, sessions string) string {
	return claudeListing(`case "$*" in
    "-p /usage") echo 'Current session: 4% used' ;;
    *--permission-mode*) echo started >> '` + sessions + `'
      echo '{"type":"result","subtype":"success","is_error":false,"result":"done"}' ;;
    *) echo listed >> '` + listings + `'; ` + onList + ` ;;
  esac`)
}

// When Claude Code cannot say which skills it loads — absent, failing, or printing no init line within the listing's
// timeout — a Claude Code task that cites a user's skill is not refused: it saves, and imports as valid, and validation
// warns that it could not verify the task's skills, naming why. The catalog says the list could not be read.
func TestClaudeCodeSkillListUnavailableWarns(t *testing.T) {
	for _, c := range []struct {
		name, script, wantDetail string
		timeout                  time.Duration // the listing's; zero is the adapter's default
	}{
		{"absent", "", "harness_not_detected", 0},
		{"failing", claudeListing("echo 'Not logged in' >&2; exit 1"), "command_failed", 0},
		{"timed out", claudeListing("sleep 30"), "claude_skill_list_timed_out", time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, _ := newClaudeApp(t)
			if c.script != "" {
				a = newClaudeAppOn(t, c.script)
			}
			a.adapters[domain.HarnessClaudeCode].(*claudecode.Adapter).SkillListTimeout = c.timeout
			ctx := t.Context()
			doc := taskYAML("unverified", "  skills: [review-notes]\n")
			res := a.Validate(ctx, TaskInput{YAML: doc})
			unverified := slices.ContainsFunc(res.Warnings, func(w schemaIssue) bool {
				return w.Key == "skill_list_unverified" && w.Code == domain.WarningMissingSkill &&
					w.Params["detailKey"] == c.wantDetail
			})
			if !res.Valid || !unverified {
				t.Errorf("Validate(unverified) valid = %v, errors %+v, warnings %+v; want valid with skill_list_unverified "+
					"naming %s", res.Valid, res.Errors, res.Warnings, c.wantDetail)
			}
			if _, err := a.CreateTask(ctx, TaskInput{YAML: doc}); err != nil {
				t.Errorf("CreateTask(unverified) = %v, want no error", err)
			}
			imported, err := a.ImportTask(ctx, taskYAML("imported", "  skills: [review-notes]\n"), false)
			if err != nil {
				t.Fatalf("ImportTask(imported) = %v, want no error", err)
			}
			unverified = slices.ContainsFunc(imported.Warnings, func(w domain.Warning) bool {
				return w.Key == "skill_list_unverified" && w.Code == domain.WarningMissingSkill &&
					w.Params["detailKey"] == c.wantDetail
			})
			if imported.Invalid || imported.Validation == nil || !imported.Validation.Valid || !unverified {
				t.Errorf("ImportTask(imported) invalid = %v, warnings %+v; want valid with skill_list_unverified naming %s",
					imported.Invalid, imported.Warnings, c.wantDetail)
			}
			problems := a.Catalog(ctx, "", "unverified").Problems
			if !slices.ContainsFunc(problems, func(p CatalogProblem) bool { return p.Key == "skill_list_unreadable" }) {
				t.Errorf(`Catalog(ctx, "", unverified) problems = %+v, want skill_list_unreadable`, problems)
			}
		})
	}
}

// When Claude Code cannot say which skills it loads as an attempt starts — its listing exiting, printing no list, or
// printing no init line within the listing's timeout — the attempt starts with the task's skills: a user's skill is
// passed through by name, unverified, a built-in one as always, and the execution gets the warning validation gives,
// naming why. The list is asked for once, not again for each skill. A task's execution runs that attempt's session to
// its end and holds the warning once. A failover to Claude Code does not change: a Claude Code fallback whose list
// cannot be read is still no valid fallback.
func TestClaudeCodeAttemptStartsWithUnverifiedSkills(t *testing.T) {
	for _, c := range []struct {
		name, onList, wantDetail string
		timeout                  time.Duration // the listing's; zero is the adapter's default
	}{
		{"exits", "echo 'Not logged in' >&2; exit 1", "command_failed", 0},
		{"prints no list", `echo '{"type":"system","subtype":"init"}'`, "claude_skill_list_unrecognized", 0},
		{"outruns the timeout", "sleep 30", "claude_skill_list_timed_out", time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			log, sessions := filepath.Join(t.TempDir(), "listings.log"), filepath.Join(t.TempDir(), "sessions.log")
			e := newClaudeEnvOn(t, attemptClaude(log, c.onList, sessions), c.timeout)
			a := e.app
			cc := a.adapters[domain.HarnessClaudeCode]
			builtin := filepath.Join(a.d.Paths.Builtins, "skills", "hf-notes")
			writeSkill(t, builtin)
			ctx := t.Context()
			cited := "  skills: [review-notes, hf-notes, pdf:pdf]\n"

			res := a.Validate(ctx, TaskInput{YAML: taskYAML("unverified", cited)})
			if res.doc == nil {
				t.Fatalf("Validate(unverified) errors = %+v, want none", res.Errors)
			}
			r := &run{a: a, def: *res.doc, eff: res.doc.Effective(), work: t.TempDir()}
			before := len(listings(t, log))
			_, skills, warnings, err := r.resolve(ctx, cc)
			want := []ports.ResolvedItem{{Name: "review-notes", Kind: "skill"},
				{Name: "hf-notes", Kind: "skill", Builtin: true, Path: builtin, Description: "A skill."},
				{Name: "pdf:pdf", Kind: "skill"}}
			if err != nil || !reflect.DeepEqual(skills, want) {
				t.Errorf("resolve(claude-code) = %+v, %v; want %+v", skills, err, want)
			}
			if len(warnings) != 1 || warnings[0].Key != "skill_list_unverified" || warnings[0].Code != domain.WarningMissingSkill ||
				warnings[0].Subject != "/spec/skills" || warnings[0].Params["detailKey"] != c.wantDetail {
				t.Errorf("resolve(claude-code) warns %+v, want one skill_list_unverified on /spec/skills naming %s", warnings,
					c.wantDetail)
			}
			if n := len(listings(t, log)) - before; n != 1 {
				t.Errorf("resolve(claude-code), citing two of the user's skills, ran %d listings, want 1", n)
			}

			e.create("unverified", cited)
			started, err := a.RunTask("unverified")
			if err != nil {
				t.Fatalf("RunTask(unverified) = %+v, %v; want started", started, err)
			}
			x := e.wait(started.ExecutionID, domain.StatusCompleted)
			if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessClaudeCode ||
				x.Attempts[0].Outcome != domain.AttemptSucceeded {
				t.Errorf("RunTask(unverified) attempts %+v, want one on Claude Code that succeeded", x.Attempts)
			}
			if got := listings(t, sessions); len(got) != 1 {
				t.Errorf("RunTask(unverified) started %d sessions of the stand-in, want 1", len(got))
			}
			var unverified []domain.Warning
			for _, w := range x.Warnings {
				if w.Key == "skill_list_unverified" {
					unverified = append(unverified, w)
				}
			}
			if len(unverified) != 1 || unverified[0].Code != domain.WarningMissingSkill ||
				unverified[0].Subject != "/spec/skills" || unverified[0].Params["detailKey"] != c.wantDetail {
				t.Errorf("RunTask(unverified) execution warnings %+v, want one skill_list_unverified on /spec/skills naming %s",
					x.Warnings, c.wantDetail)
			}

			res = a.Validate(ctx, TaskInput{YAML: codexTaskYAML("fb", "    fallback:\n      candidates:\n"+
				"        - provider: claude-code\n  skills: [user-skill]\n")})
			if res.doc == nil {
				t.Fatalf("Validate(fb) errors = %+v, want none", res.Errors)
			}
			r = &run{a: a, def: *res.doc, eff: res.doc.Effective(), work: t.TempDir()}
			chain, invalid := r.chain(ctx)
			if len(chain) != 1 || chain[0].Provider != domain.HarnessCodex || len(invalid) != 1 ||
				invalid[0].Key != "fallback_skill_list_unreadable" {
				t.Errorf("chain() = %+v, invalid %+v; want Codex alone, Claude Code invalid as its skill list could not be read",
					chain, invalid)
			}
		})
	}
}

// claudeCodeOffers is the names of the user's skills the catalog offers on Claude Code for a task with this workspace
// path and ID, and these added folders.
func claudeCodeOffers(ctx context.Context, a *App, workspace, task string, added ...string) []string {
	var names []string
	for _, e := range a.Catalog(ctx, workspace, task, added...).Skills {
		if e.Source == "user" && slices.Contains(e.Harnesses, domain.HarnessClaudeCode) {
			names = append(names, e.Name)
		}
	}
	return names
}

// After an explicit refresh, the next list asks Codex to read its skills again, and the one after it does not; both run
// on the one app server.
func TestRefreshReloadsCodexSkills(t *testing.T) {
	received := filepath.Join(t.TempDir(), "received.jsonl")
	a, _ := newCodexAppOn(t, skillListCodex(received, listAnswer(nil, "")))
	ctx := context.Background()
	a.Catalog(ctx, "", "")
	a.RefreshHarnesses(ctx)
	a.Catalog(ctx, "", "")
	a.Catalog(ctx, "", "")
	var reloads []bool
	for _, p := range listParams(t, received) {
		reloads = append(reloads, p["forceReload"] == true)
	}
	if want := []bool{false, true, false}; !slices.Equal(reloads, want) {
		t.Errorf(`Catalog(ctx, "", ""), before and after RefreshHarnesses, sent forceReload %v, want %v`, reloads, want)
	}
	data, _ := os.ReadFile(received)
	if n := strings.Count(string(data), `"method":"initialize"`); n != 1 {
		t.Errorf(`Catalog(ctx, "", ""), three calls, sent initialize %d times, want once`, n)
	}
}

// heldListingClaude is initClaude whose -p session, once it is logged, waits until the file released exists before it
// prints its init line.
func heldListingClaude(log, released string) string {
	return strings.Replace(initClaude(log), "    skills='", "    until [ -e '"+released+"' ]; do sleep 0.05; done\n    skills='", 1)
}

// The daemon starts without waiting for a harness: Start returns while Claude Code's skill list is held, with the task
// file loaded, valid, and without the warning only the list can give. Once the list comes, the check Start left running
// gives the task its missing-skill warning and announces it as updated, with task_checked.
func TestStartDoesNotWaitForTheSkillList(t *testing.T) {
	released := filepath.Join(t.TempDir(), "released")
	a := newClaudeAppOn(t, heldListingClaude(filepath.Join(t.TempDir(), "listings.log"), released))
	t.Cleanup(func() { os.WriteFile(released, nil, 0o600) })
	doc := taskYAML("cites", "  skills: [simplify, no-such-skill]\n")
	if err := os.WriteFile(filepath.Join(a.d.Paths.Tasks, "cites.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	events, _, unsubscribe := a.Subscribe(0)
	defer unsubscribe()

	started := make(chan error, 1)
	go func() { started <- a.Start(t.Context()) }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start = %v, want no error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return within 10s while the skill list was held, want it not to wait for it")
	}
	t.Cleanup(func() { a.Stop(domain.Text{Message: "test done"}) })
	missingSkill := func(v TaskView) bool {
		return slices.ContainsFunc(v.Warnings, func(w domain.Warning) bool { return w.Code == domain.WarningMissingSkill })
	}
	if v, err := a.GetTask("cites"); err != nil || v.Invalid || missingSkill(v) {
		t.Errorf("GetTask(cites) after Start = %+v, %v; want valid, without the missing-skill warning yet", v, err)
	}

	if err := os.WriteFile(released, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(30 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type != domain.EventTaskUpdated || e.TaskID != "cites" || e.MessageKey != "task_checked" {
				continue
			}
			if v, err := a.GetTask("cites"); err != nil || v.Invalid || !missingSkill(v) {
				t.Errorf("GetTask(cites) after task_checked = %+v, %v; want valid, warning no-such-skill is missing", v, err)
			}
			return
		case <-deadline:
			t.Fatal("no task.updated with task_checked for cites within 30s of the skill list, want the check to announce it")
		}
	}
}

// Stop ends the check Start left running, and its listing, though Claude Code never gives its skill list.
func TestStopEndsTheCheckOfTheTasks(t *testing.T) {
	released := filepath.Join(t.TempDir(), "released")
	log := filepath.Join(t.TempDir(), "listings.log")
	a := newClaudeAppOn(t, heldListingClaude(log, released))
	t.Cleanup(func() { os.WriteFile(released, nil, 0o600) })
	if err := os.WriteFile(filepath.Join(a.d.Paths.Tasks, "cites.yaml"), []byte(taskYAML("cites", "  skills: [simplify]\n")),
		0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("Start = %v, want no error", err)
	}
	for deadline := time.Now().Add(30 * time.Second); len(listings(t, log)) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the check started no listing within 30s, want one for the task's skill")
		}
	}
	stopped := make(chan struct{})
	go func() {
		a.Stop(domain.Text{Message: "test done"})
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return within 10s while the check waited for the skill list, want it to end the check")
	}
}
