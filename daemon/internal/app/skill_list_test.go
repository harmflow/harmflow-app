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

// A Codex user's skill is found where Codex finds it: a Codex task that cites it saves, a Claude Code task with Codex as
// its fallback imports with no invalid-fallback warning, and an execution that fails over to Codex resolves it at the
// SKILL.md Codex reports. Each lookup asks for the right workspace, never for none.
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

// pluginClaude is a stand-in Claude Code that is signed in and has the plugin pdf@market enabled, installed at root,
// whose skills are pdf and Forms.
func pluginClaude(root string) string {
	return `#!/bin/bash
case "$1" in
  --version) echo "2.1.309 (Claude Code)" ;;
  --help) echo "-p, --print  --output-format <format>  --permission-mode <mode>  --allowedTools <tools...>  --append-system-prompt <prompt>" ;;
  auth) echo '{"loggedIn": true}' ;;
  plugin)
    case "$2" in
      list) echo '[{"id":"pdf@market","enabled":true,"projectEnabled":false,"installPath":"` + root + `"}]' ;;
      details) printf 'pdf 1.0.0\n\nComponent inventory\n  Skills (2)  pdf, Forms\n' ;;
    esac ;;
esac
`
}

// A Claude Code task cites a plugin's skill by the name Claude Code gives it, `<plugin>:<skill>`. It saves when a
// plugin Claude Code has enabled has that skill, and fails naming the skill and the plugins looked in when none has it;
// the task editor's catalog offers it; an import whose skill no enabled plugin has warns, naming it; and an execution
// resolves it in the plugin's folder.
func TestClaudeCodePluginSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "skills", "pdf"))
	a := newClaudeAppOn(t, pluginClaude(root))
	ctx := t.Context()
	ws := filepath.Join(a.d.Paths.UserHome, "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	placed := func(id, skills string) string {
		return taskYAML(id, "  skills: ["+skills+"]\n  workspace:\n    path: ~/ws\n")
	}

	if _, err := a.CreateTask(ctx, TaskInput{YAML: placed("plugins", "pdf:pdf, pdf:Forms")}); err != nil {
		t.Fatalf("CreateTask(plugins), citing two skills of an enabled plugin = %v, want no error", err)
	}
	_, err := a.CreateTask(ctx, TaskInput{YAML: placed("absent", "pdf:missing")})
	ae, ok := err.(*Error)
	if !ok || ae.Key != "skill_not_found" || ae.Params["name"] != "pdf:missing" || ae.Params["source"] != "plugin_list" ||
		ae.Params["workspace"] != ws || !strings.Contains(ae.Message, "Claude Code's plugins for "+ws) {
		t.Errorf("CreateTask(absent) = %v (%+v), want skill_not_found naming pdf:missing and Claude Code's plugins for %s",
			err, ae, ws)
	}

	if got, want := claudeCodeOffers(ctx, a, "~/ws", "plugins"), []string{"pdf:Forms", "pdf:pdf"}; !slices.Equal(got, want) {
		t.Errorf(`Catalog(ctx, "~/ws", plugins) offers %q on Claude Code, want %q`, got, want)
	}

	imported, err := a.ImportTask(ctx, placed("imported", "pdf:pdf, other:skill"), false)
	if err != nil {
		t.Fatalf("ImportTask(imported) = %v, want no error", err)
	}
	var missing []string
	for _, w := range imported.Warnings {
		if w.Key == "skill_missing" {
			missing = append(missing, w.Params["name"])
		}
	}
	if !slices.Equal(missing, []string{"other:skill"}) {
		t.Errorf("ImportTask(imported) warns skill_missing for %q, want only other:skill", missing)
	}

	res := a.Validate(ctx, TaskInput{YAML: placed("plugins", "pdf:pdf, pdf:Forms")})
	if res.doc == nil {
		t.Fatalf("Validate(plugins) errors = %+v, want none", res.Errors)
	}
	r := &run{a: a, def: *res.doc, eff: res.doc.Effective(), work: ws}
	_, skills, err := r.resolve(ctx, a.adapters[domain.HarnessClaudeCode])
	var paths []string
	for _, s := range skills {
		paths = append(paths, s.Name+" "+s.Path)
	}
	if want := []string{"pdf:pdf " + filepath.Join(root, "skills", "pdf"), "pdf:Forms " + root}; err != nil || !slices.Equal(paths, want) {
		t.Errorf("resolve(claude-code) = %q, %v; want %q", paths, err, want)
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

// A Claude Code user's skill in the workspace's .claude/skills is found for that workspace only. A task placed there
// cites it and saves, the task editor's catalog offers it there, and an execution there, the attempt a failover hands to
// Claude Code included, resolves it in that folder. A task placed elsewhere is refused, naming the configuration and the
// workspace looked in, and a Codex task placed elsewhere is warned that its Claude Code fallback is invalid. A skill of
// Claude Code's configuration directory is found for every workspace, as before.
func TestClaudeCodeSkillsComeFromTheWorkspace(t *testing.T) {
	a, config := newClaudeApp(t)
	ctx := t.Context()
	ws, other := filepath.Join(a.d.Paths.UserHome, "ws"), filepath.Join(a.d.Paths.UserHome, "other")
	writeSkill(t, filepath.Join(ws, ".claude", "skills", "user-skill"))
	writeSkill(t, filepath.Join(config, "skills", "review-notes"))
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	placed := func(id, skills, workspace string) string {
		return taskYAML(id, "  skills: ["+skills+"]\n  workspace:\n    path: "+workspace+"\n")
	}

	if _, err := a.CreateTask(ctx, TaskInput{YAML: placed("here", "user-skill, review-notes", "~/ws")}); err != nil {
		t.Fatalf("CreateTask(here), citing a skill of its workspace and one of the configuration = %v, want no error", err)
	}
	if _, err := a.CreateTask(ctx, TaskInput{YAML: placed("configured", "review-notes", "~/other")}); err != nil {
		t.Fatalf("CreateTask(configured), citing a skill of the configuration = %v, want no error", err)
	}
	_, err := a.CreateTask(ctx, TaskInput{YAML: placed("elsewhere", "user-skill", "~/other")})
	ae, ok := err.(*Error)
	if !ok || ae.Key != "skill_not_found" || ae.Params["source"] != "harness_workspace" || ae.Params["workspace"] != other ||
		!strings.Contains(ae.Message, "the Claude Code configuration and the workspace "+other) {
		t.Errorf("CreateTask(elsewhere) = %v (%+v), want skill_not_found naming the configuration and %s", err, ae, other)
	}

	if got, want := claudeCodeOffers(ctx, a, "~/ws", "here"), []string{"review-notes", "user-skill"}; !slices.Equal(got, want) {
		t.Errorf(`Catalog(ctx, "~/ws", here) offers %q on Claude Code, want %q`, got, want)
	}
	if got, want := claudeCodeOffers(ctx, a, "~/other", "elsewhere"), []string{"review-notes"}; !slices.Equal(got, want) {
		t.Errorf(`Catalog(ctx, "~/other", elsewhere) offers %q on Claude Code, want %q`, got, want)
	}

	// The fake Codex finds user-skill for every workspace, so only Claude Code's lookup tells the two tasks apart.
	fallback := func(id, workspace string) string {
		return codexTaskYAML(id, "    fallback:\n      candidates:\n        - provider: claude-code\n"+
			"  skills: [user-skill]\n  workspace:\n    path: "+workspace+"\n")
	}
	imported, err := a.ImportTask(ctx, fallback("fallback-elsewhere", "~/other"), false)
	if err != nil {
		t.Fatalf("ImportTask(fallback-elsewhere) = %v, want no error", err)
	}
	var invalid []string
	for _, w := range imported.Warnings {
		if w.Code == domain.WarningInvalidFallback {
			invalid = append(invalid, w.Key)
		}
	}
	if !slices.Equal(invalid, []string{"fallback_skill_missing"}) {
		t.Errorf("ImportTask(fallback-elsewhere) invalid-fallback warnings %q, want fallback_skill_missing", invalid)
	}
	res := a.Validate(ctx, TaskInput{YAML: fallback("fallback-here", "~/ws")})
	if res.doc == nil {
		t.Fatalf("Validate(fallback-here) errors = %+v, want none", res.Errors)
	}
	for _, w := range res.Warnings {
		if w.Code == domain.WarningInvalidFallback {
			t.Errorf("Validate(fallback-here) warns %+v, want no invalid-fallback warning for Claude Code", w)
		}
	}
	r := &run{a: a, def: *res.doc, eff: res.doc.Effective(), work: ws}
	_, skills, err := r.resolve(ctx, a.adapters[domain.HarnessClaudeCode])
	if want := filepath.Join(ws, ".claude", "skills", "user-skill"); err != nil || len(skills) != 1 || skills[0].Path != want {
		t.Errorf("resolve(claude-code) in %s = %+v, %v; want user-skill at %s", ws, skills, err, want)
	}
	r.work = other
	if _, _, err := r.resolve(ctx, a.adapters[domain.HarnessClaudeCode]); domain.TextOf(err).Key != "skill_not_found" {
		t.Errorf("resolve(claude-code) in %s error %v, want skill_not_found", other, err)
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

// A task with no workspace path finds the same Claude Code skills in the task editor's catalog as on save: those of the
// folder HarmFlow runs it in. A skill of that folder's .claude/skills is offered for the task and passes validation; a
// skill of HarmFlow's workspace root, or of another task's folder, is neither offered nor accepted.
func TestClaudeCodeSkillsOfATaskWithNoWorkspacePath(t *testing.T) {
	a, _ := newClaudeApp(t)
	writeSkill(t, filepath.Join(a.d.Paths.Workspace, "unplaced", ".claude", "skills", "run-skill"))
	writeSkill(t, filepath.Join(a.d.Paths.Workspace, ".claude", "skills", "root-skill"))

	for _, c := range []struct {
		task string
		want []string
	}{
		{"unplaced", []string{"run-skill"}},
		{"another", nil},
	} {
		t.Run(c.task, func(t *testing.T) {
			if got := claudeCodeOffers(t.Context(), a, "", c.task); !slices.Equal(got, c.want) {
				t.Errorf(`Catalog(ctx, "", %s) offers %q on Claude Code, want %q`, c.task, got, c.want)
			}
			for _, skill := range []string{"run-skill", "root-skill"} {
				res := a.Validate(t.Context(), TaskInput{YAML: taskYAML(c.task, "  skills: ["+skill+"]\n")})
				if got, want := res.Valid, slices.Contains(c.want, skill); got != want {
					t.Errorf("Validate(%s citing %s) valid = %v (errors %+v), want %v, as the catalog offers it", c.task,
						skill, got, res.Errors, want)
				}
			}
		})
	}
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
