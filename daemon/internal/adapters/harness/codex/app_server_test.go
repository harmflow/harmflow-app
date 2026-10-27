package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// appServerStandIn writes a stand-in for the codex CLI whose `app-server` answers initialize, and answers skills/list
// with the shell commands onList, which read the request's id in $id. Every line the server receives is appended to
// the file it returns second.
func appServerStandIn(t *testing.T, onList string) (string, string) {
	t.Helper()
	dir := tempDir(t)
	received := filepath.Join(dir, "received.jsonl")
	script := `#!/bin/bash
case "$1" in
  --version) echo "codex-cli 0.163.0" ;;
  --help) echo "Commands: exec app-server" ;;
  login) echo "Logged in using ChatGPT" >&2 ;;
  exec) echo "Usage: codex exec  --json  --sandbox <MODE>  --cd <DIR>  --skip-git-repo-check" ;;
  app-server)
    while IFS= read -r line; do
      printf '%s\n' "$line" >> '` + received + `'
      id=$(printf '%s' "$line" | sed -n 's/^{"id":\([0-9]*\),.*/\1/p')
      case "$line" in
        *'"method":"initialize"'*) printf '{"id":%s,"result":{"userAgent":"stand-in"}}\n' "$id" ;;
        *'"method":"skills/list"'*) ` + onList + ` ;;
      esac
    done ;;
esac
`
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("os.WriteFile(%s) = %v, want no error", path, err)
	}
	return path, received
}

// answer is the shell commands that answer skills/list with result, after a notification, as Codex can send one
// between a request and its answer.
func answer(result string) string {
	return `printf '{"method":"skills/changed","params":{}}\n'; printf '{"id":%s,"result":%s}\n' "$id" '` + result + `'`
}

// requests reads what the stand-in received, by method.
func requests(t *testing.T, received string) map[string][]map[string]any {
	t.Helper()
	data, err := os.ReadFile(received)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want what the stand-in received", received, err)
	}
	out := map[string][]map[string]any{}
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("json.Unmarshal(%q) of a line the stand-in received = %v, want no error", l, err)
		}
		method, _ := m["method"].(string)
		params, _ := m["params"].(map[string]any)
		out[method] = append(out[method], params)
	}
	return out
}

func appServerAdapter(t *testing.T) *Adapter {
	t.Helper()
	a := &Adapter{Home: tempDir(t), KnownLocations: []string{}}
	t.Cleanup(a.Close)
	return a
}

func appServerInfo(path string) ports.HarnessInfo {
	return ports.HarnessInfo{ID: ID, Detected: true, Path: path, Version: "0.163.0", Features: map[string]bool{"app-server": true}}
}

// A Codex user's skills are what Codex's skill list returns for the workspace: the enabled ones, each name once and the
// first of it in the list's order, at the folder of the SKILL.md Codex reports, with what Codex could not load. Names a
// task cannot cite are kept: leaving them out is the application's decision.
func TestSkillsFromTheAppServer(t *testing.T) {
	home := tempDir(t)
	mine := filepath.Join(home, ".agents", "skills", "my-skill", "SKILL.md")
	result := `{"data":[{"cwd":"/w","skills":[` +
		`{"name":"my-skill","description":"Mine.\nMore.","enabled":true,"path":"` + mine + `","scope":"user"},` +
		`{"name":"my-skill","description":"Again","enabled":true,"path":"/elsewhere/my-skill/SKILL.md","scope":"repo"},` +
		`{"name":"off","description":"d","enabled":false,"path":"/x/off/SKILL.md","scope":"user"},` +
		`{"name":"pdf:pdf","description":"d","enabled":true,"path":"/x/pdf/SKILL.md","scope":"user"},` +
		`{"name":"hf-github-worktree","description":"d","enabled":true,"path":"/x/hf/SKILL.md","scope":"user"},` +
		`{"name":"imagegen","description":"System.","enabled":true,"path":"/sys/imagegen/SKILL.md","scope":"system"}],` +
		`"errors":[{"path":"/x/broken/SKILL.md","message":"invalid front matter"}]}]}`
	path, received := appServerStandIn(t, answer(result))
	a := appServerAdapter(t)
	info := appServerInfo(path)
	workspace := tempDir(t)

	items, problems, err := a.UserItems(context.Background(), info, "skill", workspace)
	if err != nil {
		t.Fatalf("UserItems(skill) = %v, want no error", err)
	}
	var names []string
	for _, it := range items {
		names = append(names, it.Name)
	}
	if want := []string{"my-skill", "pdf:pdf", "hf-github-worktree", "imagegen"}; !slices.Equal(names, want) {
		t.Errorf("UserItems(skill) = %q, want %q", names, want)
	}
	wantProblems := []ports.DiscoveryError{{Path: "/x/broken/SKILL.md", Message: "invalid front matter"}}
	if !slices.Equal(problems, wantProblems) {
		t.Errorf("UserItems(skill) discovery errors %v, want %v", problems, wantProblems)
	}

	it, found, problems, err := a.UserItem(context.Background(), info, "skill", "my-skill", workspace)
	if err != nil || !found || it.Path != filepath.Dir(mine) || it.Description != "Mine." || len(problems) != 1 {
		t.Errorf("UserItem(my-skill) = %+v, %v, %v, %v; want %s, described Mine., one discovery error", it, found,
			problems, err, filepath.Dir(mine))
	}
	for _, name := range []string{"pdf:pdf", "hf-github-worktree"} {
		if _, found, _, err := a.UserItem(context.Background(), info, "skill", name, workspace); !found || err != nil {
			t.Errorf("UserItem(%s) found %v, err %v; want found", name, found, err)
		}
	}
	for _, name := range []string{"off", "missing"} {
		if _, found, _, err := a.UserItem(context.Background(), info, "skill", name, workspace); found || err != nil {
			t.Errorf("UserItem(%s) found %v, err %v; want not found", name, found, err)
		}
	}

	// One UserItems and five UserItem calls share one server.
	got := requests(t, received)
	if n := len(got["initialize"]); n != 1 {
		t.Errorf("UserItems and UserItem, six calls, sent initialize %d times, want once", n)
	}
	if n := len(got["initialized"]); n != 1 {
		t.Errorf("UserItems and UserItem, six calls, sent initialized %d times, want once", n)
	}
	for _, p := range got["skills/list"] {
		cwds, _ := p["cwds"].([]any)
		if len(cwds) != 1 || cwds[0] != workspace || p["forceReload"] != false {
			t.Errorf("UserItem(skill, %s) sent skills/list %v, want cwds [%s] and forceReload false", workspace, p,
				workspace)
		}
	}

	// The session is pointed at the SKILL.md Codex reported.
	inv, _, err := a.Prepare(ports.HarnessInfo{Path: path, Features: map[string]bool{}},
		ports.RunRequest{WorkDir: workspace, ExecutionDir: tempDir(t), Prompt: "do it",
			Permissions: *domain.TaskDocument{}.Effective().Spec.Permissions, Skills: []ports.ResolvedItem{it}})
	if err != nil {
		t.Fatalf("Prepare(my-skill) = %v, want no error", err)
	}
	if !strings.Contains(inv.Stdin, mine) {
		t.Errorf("Prepare(my-skill).Stdin = %q, want it to name %s", inv.Stdin, mine)
	}
}

// forceReload goes with the first list after an explicit refresh only.
func TestReloadSendsForceReloadOnce(t *testing.T) {
	path, received := appServerStandIn(t, answer(`{"data":[]}`))
	a := appServerAdapter(t)
	info := appServerInfo(path)
	for _, reload := range []bool{false, true, false} {
		if reload {
			a.ReloadUserItems()
		}
		if _, _, err := a.UserItems(context.Background(), info, "skill", "/w"); err != nil {
			t.Fatalf("UserItems(skill) = %v, want no error", err)
		}
	}
	var sent []bool
	for _, p := range requests(t, received)["skills/list"] {
		sent = append(sent, p["forceReload"] == true)
	}
	if want := []bool{false, true, false}; !slices.Equal(sent, want) {
		t.Errorf("UserItems(skill), before and after ReloadUserItems, sent forceReload %v, want %v", sent, want)
	}
	if n := len(requests(t, received)["initialize"]); n != 1 {
		t.Errorf("UserItems(skill), three calls, sent initialize %d times, want once", n)
	}
}

// A list that cannot be read is an error of its own, never a skill not found, and says why.
func TestUnreadableSkillList(t *testing.T) {
	cases := []struct {
		name, onList string
		info         func(path string) ports.HarnessInfo
		wantKey      string
	}{
		{"the server answers with an error",
			`printf '{"id":%s,"error":{"code":-32603,"message":"skills unavailable"}}\n' "$id"`, appServerInfo,
			"codex_app_server_error"},
		{"the server exits", `exit 3`, appServerInfo, "codex_app_server_exited"},
		{"the server does not answer in time", `sleep 10`, appServerInfo, "codex_app_server_timed_out"},
		{"this Codex has no app server", answer(`{"data":[]}`), func(path string) ports.HarnessInfo {
			info := appServerInfo(path)
			info.Features = map[string]bool{}
			return info
		}, "codex_app_server_missing"},
		{"Codex is not detected", answer(`{"data":[]}`), func(string) ports.HarnessInfo { return ports.HarnessInfo{} },
			"harness_not_detected"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, _ := appServerStandIn(t, c.onList)
			a := appServerAdapter(t)
			if c.wantKey == "codex_app_server_timed_out" {
				// Long enough for initialize, short of the stand-in's sleep.
				a.listTimeout = 2 * time.Second
			}
			_, found, _, err := a.UserItem(context.Background(), c.info(path), "skill", "my-skill", "/w")
			if err == nil || found || domain.TextOf(err).Key != c.wantKey {
				t.Errorf("UserItem(my-skill) found %v, error %v (%s); want the error %s", found, err, domain.TextOf(err).Key,
					c.wantKey)
			}
			if _, _, err := a.UserItems(context.Background(), c.info(path), "skill", "/w"); domain.TextOf(err).Key != c.wantKey {
				t.Errorf("UserItems(skill) error %v (%s), want %s", err, domain.TextOf(err).Key, c.wantKey)
			}
		})
	}
}

// A server that cannot start, that fails its hello, or whose answer is not a skill list, cannot be read either.
func TestAppServerThatCannotBeUsed(t *testing.T) {
	notExecutable := filepath.Join(tempDir(t), "codex")
	if err := os.WriteFile(notExecutable, []byte("not a program"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%s) = %v, want no error", notExecutable, err)
	}
	failsHello := filepath.Join(tempDir(t), "codex")
	if err := os.WriteFile(failsHello, []byte("#!/bin/bash\n[ \"$1\" = app-server ] && read -r line && exit 1\n"), 0o755); err != nil {
		t.Fatalf("os.WriteFile(%s) = %v, want no error", failsHello, err)
	}
	badAnswer, _ := appServerStandIn(t, `printf '{"id":%s,"result":{"data":"none"}}\n' "$id"`)
	cases := []struct {
		name, path, wantKey string
	}{
		{"it cannot start", notExecutable, "codex_app_server_start_failed"},
		{"it fails initialize", failsHello, "codex_app_server_exited"},
		{"its answer is not a skill list", badAnswer, "codex_app_server_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := appServerAdapter(t)
			_, _, err := a.UserItems(context.Background(), appServerInfo(c.path), "skill", "/w")
			if domain.TextOf(err).Key != c.wantKey {
				t.Errorf("UserItems(skill) error %v (%s), want %s", err, domain.TextOf(err).Key, c.wantKey)
			}
		})
	}
}

// The server names HarmFlow's version in its hello, and a Codex found at another path gets a server of its own.
func TestAppServerFollowsTheDetectedCodex(t *testing.T) {
	first, firstReceived := appServerStandIn(t, answer(`{"data":[]}`))
	second, secondReceived := appServerStandIn(t, answer(`{"data":[]}`))
	a := appServerAdapter(t)
	a.ClientVersion = "1.2.3"
	for _, path := range []string{first, second} {
		if _, _, err := a.UserItems(context.Background(), appServerInfo(path), "skill", "/w"); err != nil {
			t.Fatalf("UserItems(skill) with Codex at %s = %v, want no error", path, err)
		}
	}
	for _, received := range []string{firstReceived, secondReceived} {
		hello := requests(t, received)["initialize"]
		info, _ := hello[0]["clientInfo"].(map[string]any)
		if len(hello) != 1 || info["name"] != "harmflow" || info["version"] != "1.2.3" {
			t.Errorf("UserItems(skill) sent initialize %v to this Codex, want once, from harmflow 1.2.3", hello)
		}
	}
	if skill, agent := a.ListsPerWorkspace("skill"), a.ListsPerWorkspace("agent"); !skill || agent {
		t.Errorf("ListsPerWorkspace(skill), ListsPerWorkspace(agent) = %v, %v; want true, false: skills from the "+
			"workspace's list, agents from the Codex home", skill, agent)
	}
}

// A server that broke is started again for the next list.
func TestAppServerRestartsAfterItExits(t *testing.T) {
	dir := tempDir(t)
	flag := filepath.Join(dir, "exited-once")
	path, received := appServerStandIn(t, `if [ ! -e '`+flag+`' ]; then touch '`+flag+`'; exit 3; fi; `+answer(`{"data":[]}`))
	a := appServerAdapter(t)
	info := appServerInfo(path)
	if _, _, err := a.UserItems(context.Background(), info, "skill", "/w"); domain.TextOf(err).Key != "codex_app_server_exited" {
		t.Fatalf("first UserItems(skill) error %v (%s), want codex_app_server_exited", err, domain.TextOf(err).Key)
	}
	if _, _, err := a.UserItems(context.Background(), info, "skill", "/w"); err != nil {
		t.Fatalf("second UserItems(skill) = %v, want no error", err)
	}
	if n := len(requests(t, received)["initialize"]); n != 2 {
		t.Errorf("UserItems(skill), twice, sent initialize %d times, want twice: once per start", n)
	}
}

// A Codex user's agents are still read from the Codex home, with no app server.
func TestAgentsStayInTheCodexHome(t *testing.T) {
	codexHome := tempDir(t)
	t.Setenv("CODEX_HOME", codexHome)
	agent := filepath.Join(codexHome, "agents", "reviewer.md")
	_ = os.MkdirAll(filepath.Dir(agent), 0o700)
	_ = os.WriteFile(agent, []byte("---\nname: reviewer\ndescription: Reviews.\n---\nYou review."), 0o600)
	a := appServerAdapter(t)
	it, found, _, err := a.UserItem(context.Background(), ports.HarnessInfo{}, "agent", "reviewer", "/w")
	if err != nil || !found || it.Path != agent {
		t.Errorf("UserItem(agent reviewer) = %+v, %v, %v; want %s", it, found, err, agent)
	}
	items, _, err := a.UserItems(context.Background(), ports.HarnessInfo{}, "agent", "/w")
	if err != nil || len(items) != 1 || items[0].Name != "reviewer" {
		t.Errorf("UserItems(agent) = %+v, %v; want reviewer", items, err)
	}
}

// Detection records whether this Codex has an app server, from its help.
func TestDetectRecordsTheAppServer(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	withServer, _ := appServerStandIn(t, answer(`{"data":[]}`))
	without := standIn(t, `echo "codex-cli 0.163.0"`, signedIn)
	for path, want := range map[string]bool{withServer: true, without: false} {
		info := (&Adapter{Home: tempDir(t), KnownLocations: []string{}}).Detect(context.Background(), path)
		if info.Features["app-server"] != want {
			t.Errorf("Detect(%s) app-server %v, want %v", path, info.Features["app-server"], want)
		}
	}
}
