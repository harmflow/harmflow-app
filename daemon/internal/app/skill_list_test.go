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
)

// skillListCodex is a stand-in Codex whose app server answers skills/list with the shell commands onList, which read
// the request's id in $id, and appends every line it receives to received.
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

// The catalog lists a skill of Codex's list once, with Codex once among its harnesses, for the workspace asked, or for
// the workspace root; what Codex could not load is in its problems and in a missing skill's message. A skill a task
// cannot cite, a plugin's `<plugin>:<skill>` or a name with the hf- prefix, is not offered, though Codex lists it.
func TestCatalogListsCodexSkillsForTheWorkspace(t *testing.T) {
	received := filepath.Join(t.TempDir(), "received.jsonl")
	broken := `{"path":"/x/broken/SKILL.md","message":"invalid front matter"}`
	a, _ := newCodexAppOn(t, skillListCodex(received,
		listAnswer([]string{"my-skill", "/u/my-skill/SKILL.md", "my-skill", "/r/my-skill/SKILL.md",
			"pdf:pdf", "/p/pdf/SKILL.md", "hf-users-own", "/u/hf-users-own/SKILL.md"}, broken)))
	ctx := t.Context()
	ws := t.TempDir()

	c := a.Catalog(ctx, ws)
	var entries []CatalogEntry
	for _, e := range c.Skills {
		switch e.Name {
		case "my-skill":
			entries = append(entries, e)
		case "pdf:pdf", "hf-users-own":
			t.Errorf("Catalog(ctx, %s) offers %+v, want no skill a task cannot cite", ws, e)
		}
	}
	if len(entries) != 1 || !slices.Equal(entries[0].Harnesses, []string{domain.HarnessCodex}) {
		t.Errorf("Catalog(ctx, %s) my-skill = %+v, want one entry, on codex once", ws, entries)
	}
	if got := lastCwds(t, received); !slices.Equal(got, []string{ws}) {
		t.Errorf("Catalog(ctx, %s) listed skills for %q, want [%s]", ws, got, ws)
	}
	if len(c.Problems) != 1 || c.Problems[0].Path != "/x/broken/SKILL.md" || c.Problems[0].Message != "invalid front matter" ||
		c.Problems[0].Harness != domain.HarnessCodex {
		t.Errorf("Catalog(ctx, %s) problems = %+v, want the SKILL.md Codex could not load", ws, c.Problems)
	}
	a.Catalog(ctx, "")
	if got := lastCwds(t, received); !slices.Equal(got, []string{a.d.Paths.Workspace}) {
		t.Errorf(`Catalog(ctx, "") listed skills for %q, want the workspace root %q`, got, a.d.Paths.Workspace)
	}

	_, err := a.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("gone", "  skills: [no-such-skill]\n")})
	ae, ok := err.(*Error)
	if !ok || ae.Key != "skill_not_found" || ae.Params["source"] != "skill_list_errors" ||
		!strings.Contains(ae.Message, "invalid front matter") || !strings.Contains(ae.Message, "skill list for") {
		t.Errorf("CreateTask(gone) = %v (%+v), want skill_not_found naming Codex's skill list and its errors", err, ae)
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
	if c := a.Catalog(ctx, ""); len(c.Problems) != 1 || c.Problems[0].Key != "skill_list_unreadable" {
		t.Errorf(`Catalog(ctx, "") problems = %+v, want one, skill_list_unreadable`, c.Problems)
	}
}

// After an explicit refresh, the next list asks Codex to read its skills again, and the one after it does not; both run
// on the one app server.
func TestRefreshReloadsCodexSkills(t *testing.T) {
	received := filepath.Join(t.TempDir(), "received.jsonl")
	a, _ := newCodexAppOn(t, skillListCodex(received, listAnswer(nil, "")))
	ctx := context.Background()
	a.Catalog(ctx, "")
	a.RefreshHarnesses(ctx)
	a.Catalog(ctx, "")
	a.Catalog(ctx, "")
	var reloads []bool
	for _, p := range listParams(t, received) {
		reloads = append(reloads, p["forceReload"] == true)
	}
	if want := []bool{false, true, false}; !slices.Equal(reloads, want) {
		t.Errorf(`Catalog(ctx, ""), before and after RefreshHarnesses, sent forceReload %v, want %v`, reloads, want)
	}
	data, _ := os.ReadFile(received)
	if n := strings.Count(string(data), `"method":"initialize"`); n != 1 {
		t.Errorf(`Catalog(ctx, ""), three calls, sent initialize %d times, want once`, n)
	}
}
