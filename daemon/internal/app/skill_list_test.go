package app

import (
	"context"
	"encoding/json"
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
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/claudecode"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// skillListCodex is a stand-in Codex whose app server answers skills/list with the shell commands onList, which read
// the request's id in $id, reads no managed requirements, and appends every line it receives to received.
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

// A Codex user's skill is found where Codex finds it: a Codex task that cites it saves, a Claude Code task with Codex
// as its fallback imports with no invalid-fallback warning, and an execution that fails over to Codex resolves it at
// the SKILL.md Codex reports. Each lookup asks for the right workspace, never for none.
func TestCodexSkillsComeFromItsSkillList(t *testing.T) {
	received := filepath.Join(t.TempDir(), "received.jsonl")
	skill := filepath.Join(t.TempDir(), ".agents", "skills", "review-notes", "SKILL.md")
	a, _ := newCodexAppOn(t, skillListCodex(received, listAnswer([]string{"review-notes", skill}, "")))
	ctx := t.Context()

	if _, err := a.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("plain", codexSkills)}); err != nil {
		t.Fatalf("CreateTask(plain), citing a skill of Codex's list = %v, want no error", err)
	}
	if got, want := lastCwds(t, received), []string{filepath.Join(a.d.Paths.Workspace, "plain")}; !slices.Equal(got, want) {
		t.Errorf("CreateTask(plain), with no workspace path, listed skills for %q, want its run folder %q", got, want)
	}
	if _, err := a.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("placed", codexSkills+"  workspace:\n    path: ~/ws\n")}); err != nil {
		t.Fatalf("CreateTask(placed), with a workspace path = %v, want no error", err)
	}
	if got, want := lastCwds(t, received), []string{filepath.Join(a.d.Paths.UserHome, "ws")}; !slices.Equal(got, want) {
		t.Errorf("CreateTask(placed) listed skills for %q, want its expanded workspace path %q", got, want)
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
	_, skills, err := r.resolve(ctx, a.adapters[domain.HarnessCodex])
	if err != nil || len(skills) != 1 || skills[0].Path != filepath.Dir(skill) {
		t.Errorf("resolve(codex) = %+v, %v; want review-notes at %s", skills, err, filepath.Dir(skill))
	}
	if got := lastCwds(t, received); !slices.Equal(got, []string{work}) {
		t.Errorf("resolve(codex) listed skills for %q, want the execution's working directory %q", got, work)
	}
}

// The catalog lists a skill of Codex's list once, with Codex once among its harnesses, for the workspace path asked,
// for the run folder of a task asked with none, as validation does, or for the workspace root with neither; what Codex
// could not load is in its problems and in a missing skill's message. A plugin's skill is offered by the name Codex
// reports, `<plugin>:<skill>`, spaces included; a skill a task cannot cite, a name with the hf- prefix or one that is
// neither an identifier nor a plugin's skill, is not offered, though Codex lists it.
func TestCatalogListsCodexSkillsForTheWorkspace(t *testing.T) {
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
	if got := lastCwds(t, received); !slices.Equal(got, []string{ws}) {
		t.Errorf("Catalog(ctx, %s, plain) listed skills for %q, want the workspace path [%s]", ws, got, ws)
	}
	if len(c.Problems) != 1 || c.Problems[0].Path != "/x/broken/SKILL.md" || c.Problems[0].Message != "invalid front matter" ||
		c.Problems[0].Harness != domain.HarnessCodex {
		t.Errorf("Catalog(ctx, %s, plain) problems = %+v, want the SKILL.md Codex could not load", ws, c.Problems)
	}
	a.Catalog(ctx, "", "plain")
	if got, want := lastCwds(t, received), []string{filepath.Join(a.d.Paths.Workspace, "plain")}; !slices.Equal(got, want) {
		t.Errorf(`Catalog(ctx, "", plain) listed skills for %q, want the task's run folder %q, as validation reads`, got, want)
	}
	a.Catalog(ctx, "", "")
	if got := lastCwds(t, received); !slices.Equal(got, []string{a.d.Paths.Workspace}) {
		t.Errorf(`Catalog(ctx, "", "") listed skills for %q, want the workspace root %q`, got, a.d.Paths.Workspace)
	}

	_, err := a.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("gone", "  skills: [no-such-skill]\n")})
	ae, ok := err.(*Error)
	if !ok || ae.Key != "skill_not_found" || ae.Params["source"] != "skill_list_errors" ||
		!strings.Contains(ae.Message, "invalid front matter") || !strings.Contains(ae.Message, "skill list for") {
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
	_, skills, err := r.resolve(ctx, a.adapters[domain.HarnessCodex])
	if err != nil || len(skills) != 1 || skills[0].Name != "presentations:Presentations" || skills[0].Path != "/p/presentations" {
		t.Errorf("resolve(codex) = %+v, %v; want presentations:Presentations at /p/presentations", skills, err)
	}
}

// A skill list that cannot be read is said so, never a missing skill: on save, on import, and for a fallback.
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

// A Claude Code task's skills are the ones Claude Code lists for its workspace and added folders, and only those. A
// task whose workspace is a folder of a repository cites the repository's skill, a bundled one, and a plugin's, and
// saves; one placed elsewhere is refused, naming Claude Code's skill list for its workspace; a skill on disk that Claude
// Code does not list is not found. The folder a task adds with a writable path counts, in the task editor's catalog,
// on save, and in an execution. A failover to Claude Code reads the same list. Each list is asked for once per
// workspace and added folders, and again after a refresh.
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

	if _, err := a.CreateTask(ctx, TaskInput{YAML: placed("sub", "repo-skill, simplify, pdf:pdf", "~/repo/sub")}); err != nil {
		t.Fatalf("CreateTask(sub), citing the repository's skill, a bundled one, and a plugin's = %v, want no error", err)
	}
	for _, c := range []struct{ id, skill, workspace string }{
		{"elsewhere", "repo-skill", "~/other"},
		{"on-disk", "on-disk", "~/repo/sub"},
	} {
		_, err := a.CreateTask(ctx, TaskInput{YAML: placed(c.id, c.skill, c.workspace)})
		ae, ok := err.(*Error)
		if !ok || ae.Key != "skill_not_found" || ae.Params["name"] != c.skill || ae.Params["source"] != "skill_list" ||
			!strings.Contains(ae.Message, "Claude Code's skill list for ") {
			t.Errorf("CreateTask(%s) = %v (%+v), want skill_not_found naming %s and Claude Code's skill list", c.id, err, ae,
				c.skill)
		}
	}
	if _, err := a.CreateTask(ctx, TaskInput{YAML: adding("adding", "extra-skill")}); err != nil {
		t.Fatalf("CreateTask(adding), citing the skill of a folder it adds = %v, want no error", err)
	}

	if got, want := claudeCodeOffers(ctx, a, "~/repo/sub", "sub"), []string{"pdf:pdf", "repo-skill", "simplify"}; !slices.Equal(got, want) {
		t.Errorf(`Catalog(ctx, "~/repo/sub", sub) offers %q on Claude Code, want %q`, got, want)
	}
	var offered []string
	for _, e := range a.Catalog(ctx, "~/other", "adding", "~/extra").Skills {
		if e.Source == "user" && slices.Contains(e.Harnesses, domain.HarnessClaudeCode) {
			offered = append(offered, e.Name)
		}
	}
	if want := []string{"extra-skill", "pdf:pdf", "simplify"}; !slices.Equal(offered, want) {
		t.Errorf(`Catalog(ctx, "~/other", adding, "~/extra") offers %q on Claude Code, want %q`, offered, want)
	}

	res := a.Validate(ctx, TaskInput{YAML: adding("adding", "extra-skill")})
	if res.doc == nil {
		t.Fatalf("Validate(adding) errors = %+v, want none", res.Errors)
	}
	resolvedExtra, _ := domain.ResolvePath(extra)
	r := &run{a: a, def: *res.doc, eff: res.doc.Effective(), work: other, writable: []string{resolvedExtra}}
	if _, skills, err := r.resolve(ctx, a.adapters[domain.HarnessClaudeCode]); err != nil || len(skills) != 1 ||
		skills[0].Name != "extra-skill" {
		t.Errorf("resolve(claude-code) adding %s = %+v, %v; want extra-skill", extra, skills, err)
	}

	// The fake Codex finds every skill named user-skill, so a Codex task cites it with repo-skill.
	fallback := func(id, workspace string) string {
		return codexTaskYAML(id, "    fallback:\n      candidates:\n        - provider: claude-code\n"+
			"  skills: [repo-skill]\n  workspace:\n    path: "+workspace+"\n")
	}
	for _, c := range []struct {
		workspace string
		invalid   bool
	}{{"~/repo/sub", false}, {"~/other", true}} {
		res := a.Validate(ctx, TaskInput{YAML: fallback("fb", c.workspace)})
		invalid := slices.ContainsFunc(res.Warnings, func(w schemaIssue) bool { return w.Code == domain.WarningInvalidFallback })
		if invalid != c.invalid {
			t.Errorf("Validate(fallback in %s) invalid-fallback warning = %v (%+v), want %v", c.workspace, invalid,
				res.Warnings, c.invalid)
		}
	}

	before := len(listings(t, log))
	claudeCodeOffers(ctx, a, "~/repo/sub", "sub")
	if got := len(listings(t, log)); got != before {
		t.Errorf("a second catalog for ~/repo/sub ran %d more listings, want none: the list is kept", got-before)
	}
	a.RefreshHarnesses(ctx)
	claudeCodeOffers(ctx, a, "~/repo/sub", "sub")
	if got := len(listings(t, log)); got != before+1 {
		t.Errorf("the catalog after a refresh ran %d more listings, want 1", got-before)
	}
	for _, l := range listings(t, log) {
		if !strings.Contains(l, "|-p --output-format stream-json --verbose --no-session-persistence") {
			t.Errorf("a listing ran as %q, want claude -p with stream-json output and no session kept", l)
		}
	}
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

// When Claude Code cannot say which skills it loads — absent, or failing — a Claude Code task that cites a user's
// skill is not refused: it saves, and validation warns that it could not verify the task's skills, naming why. The
// catalog says the list could not be read.
func TestClaudeCodeSkillListUnavailableWarns(t *testing.T) {
	failing := "#!/bin/bash\ncase \"$1\" in\n  --version) echo \"2.1.309 (Claude Code)\" ;;\n" +
		"  --help) echo \"-p, --print  --output-format <format>  --permission-mode <mode>  --allowedTools <tools...>  --append-system-prompt <prompt>\" ;;\n" +
		"  auth) echo '{\"loggedIn\": true}' ;;\n  -p) echo 'Not logged in' >&2; exit 1 ;;\nesac\n"
	for _, c := range []struct {
		name, script, wantDetail string
	}{
		{"absent", "", "harness_not_detected"},
		{"failing", failing, "command_failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, _ := newClaudeApp(t)
			if c.script != "" {
				a = newClaudeAppOn(t, c.script)
			}
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
			problems := a.Catalog(ctx, "", "unverified").Problems
			if !slices.ContainsFunc(problems, func(p CatalogProblem) bool { return p.Key == "skill_list_unreadable" }) {
				t.Errorf(`Catalog(ctx, "", unverified) problems = %+v, want skill_list_unreadable`, problems)
			}
		})
	}
}

// claudeCodeOffers is the names of the user's skills the catalog offers on Claude Code for a task with this workspace
// path and ID.
func claudeCodeOffers(ctx context.Context, a *App, workspace, task string) []string {
	var names []string
	for _, e := range a.Catalog(ctx, workspace, task).Skills {
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
