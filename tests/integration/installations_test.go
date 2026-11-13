package integration

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// installed counts how often each path is among a harness's installations in GET /harnesses.
func installed(h map[string]any) map[string]int {
	counts := map[string]int{}
	list, _ := h["installations"].([]any)
	for _, p := range list {
		counts[jsonString(p)]++
	}
	return counts
}

// harmflow-app#70: with one Codex on the daemon's PATH and a second outside it, setting the second by hand offers both,
// and a run uses the second. Choosing the first then makes the next run use the first. Only the stand-ins are checked:
// a real Codex on the login shell's PATH can be offered too.
func TestChooseAmongInstallations(t *testing.T) {
	t.Parallel()
	first, second := standIn(t, "fake-codex", "codex"), standIn(t, "fake-codex", "codex")
	d := startDaemon(t, t.TempDir(), "PATH="+filepath.Dir(first.path)+":"+os.Getenv("PATH"))

	h := d.setHarness("codex", second.path)
	if n := installed(h); n[first.path] != 1 || n[second.path] != 1 || h["installation"] != second.path ||
		h["pathSource"] != "manual" {
		t.Fatalf("with the second set by hand: installations %v, installation %v (%v); want both once, the second in use",
			h["installations"], h["installation"], h["pathSource"])
	}
	if x := d.runOn("onsecond", "codex"); x["status"] != "completed" {
		t.Fatalf("on the second: status %v, error %v", x["status"], x["errorMessage"])
	}
	if runs := second.only(t, append(codexProbes, codexRun)...)[codexRun]; runs != 1 {
		t.Errorf("the second ran %d times, want once", runs)
	}
	if runs := first.only(t, append(codexProbes, codexRun)...)[codexRun]; runs != 0 {
		t.Errorf("the first ran %d times before it was chosen", runs)
	}

	h = d.setHarness("codex", first.path)
	if n := installed(h); n[first.path] != 1 || h["installation"] != first.path || h["pathSource"] != "manual" {
		t.Fatalf("with the first chosen: installations %v, installation %v (%v); want the first once, in use",
			h["installations"], h["installation"], h["pathSource"])
	}
	if x := d.runOn("onfirst", "codex"); x["status"] != "completed" {
		t.Fatalf("on the first: status %v, error %v", x["status"], x["errorMessage"])
	}
	if runs := first.only(t, append(codexProbes, codexRun)...)[codexRun]; runs != 1 {
		t.Errorf("the first ran %d times, want once", runs)
	}
	if runs := second.only(t, append(codexProbes, codexRun)...)[codexRun]; runs != 1 {
		t.Errorf("the second ran %d times in all, want only the first run", runs)
	}
}

// codexHomesAppServer is a branch of fake-codex's case that answers as Codex's app server from the home its CODEX_HOME
// names: account/read gives the account of the home's account.json, the managed requirements are none, the quota has
// room left, and the skill list is empty; any other request is an error.
const codexHomesAppServer = `  --help) echo "Commands: exec app-server login"; exit 0 ;;
  app-server)
    while IFS= read -r line; do
      [[ $line =~ \"id\":([0-9]+) ]] || continue
      id=${BASH_REMATCH[1]}
      method=
      [[ $line =~ \"method\":\"([^\"]+)\" ]] && method=${BASH_REMATCH[1]}
      case "$method" in
        initialize) printf '{"id":%s,"result":{"userAgent":"stand-in"}}\n' "$id" ;;
        account/read) printf '{"id":%s,"result":{"account":%s}}\n' "$id" "$(< "$CODEX_HOME/account.json")" ;;
        configRequirements/read) printf '{"id":%s,"result":{"requirements":null}}\n' "$id" ;;
        account/rateLimits/read) printf '{"id":%s,"result":{"rateLimits":{"primary":{"usedPercent":12,"resetsAt":null},"secondary":null,"rateLimitReachedType":null,"spendControlReached":false}}}\n' "$id" ;;
        skills/list) printf '{"id":%s,"result":{"data":[]}}\n' "$id" ;;
        *) printf '{"id":%s,"error":{"code":-32601,"message":"the stand-in does not answer %s"}}\n' "$id" "$method" ;;
      esac
    done
    exit 0 ;;
`

// codexHomeAt makes a signed-in Codex home at dir, whose models cache lists model and whose account is account, the
// JSON account/read answers.
func codexHomeAt(t *testing.T, dir, model, account string) string {
	t.Helper()
	files := map[string]string{
		"models_cache.json": `{"models":[{"slug":"` + model + `","supported_reasoning_levels":[{"effort":"low"}]}]}`,
		"account.json":      account,
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// startedWith reads the log where a copy of fake-codex recorded each of its processes, and gives the CODEX_HOME each
// started with, in order, by the process's first two arguments.
func startedWith(t *testing.T, log string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("fake-codex recorded no process: %v", err)
	}
	homes := map[string][]string{}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		home, args, _ := strings.Cut(line, "\t")
		args = strings.TrimSpace(args)
		homes[args] = append(homes[args], home)
	}
	return homes
}

// harmflow-app#1029: across the real daemon, each Codex home is an installation of its own. The daemon's user folder
// holds a .codex home signed in with ChatGPT, and its CODEX_HOME names a second home signed in with an API key, whose
// models differ. GET /harnesses lists both, each with its own sign-in and models. A task naming ~/.codex and a task
// naming no installation run at the same time, and each attempt's Codex process starts with its own installation's home
// as CODEX_HOME, the other's nowhere, as the executions' attempts record. The stand-in records the CODEX_HOME each of
// its processes started with, and each probe of a home ran with that home, never with the other's.
func TestEachCodexInstallationRunsWithItsOwnHome(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "codex-homes")
	codex := standIn(t, "fake-codex", "codex", "HOMES_LOG=\n", "HOMES_LOG='"+log+"'\n",
		"case \"$1\" in\n", "case \"$1\" in\n"+codexHomesAppServer)
	user := resolvedTempDir(t)
	first := codexHomeAt(t, filepath.Join(user, ".codex"), "model-a", `{"type":"chatgpt","planType":"plus"}`)
	second := codexHomeAt(t, filepath.Join(resolvedTempDir(t), "codex-b"), "model-b", `{"type":"apiKey"}`)
	d := startDaemon(t, t.TempDir(), "HOME="+user, "CODEX_HOME="+second)

	h := d.setHarness("codex", codex.path)
	type home struct{ id, home, signIn, models string }
	var got []home
	homes, _ := h["homes"].([]any)
	for _, entry := range homes {
		m, _ := entry.(map[string]any)
		account, _ := m["account"].(map[string]any)
		var models []string
		list, _ := m["models"].([]any)
		for _, model := range list {
			model, _ := model.(map[string]any)
			models = append(models, jsonString(model["id"]))
		}
		got = append(got, home{jsonString(m["installationId"]), jsonString(m["home"]), jsonString(account["signInMethod"]),
			strings.Join(models, " ")})
	}
	want := []home{{"~/.codex", first, "chatgpt", "default model-a"}, {second, second, "api_key", "default model-b"}}
	if !slices.Equal(got, want) || h["installationId"] != second {
		t.Fatalf("GET /harnesses: Codex homes %+v, in use %v; want %+v, the second in use", got, h["installationId"], want)
	}

	type installation struct{ id, home string }
	ran := map[string]installation{} // by execution
	for _, c := range []struct {
		task, key string
		want      installation
	}{
		{"onfirst", "      installation: \"~/.codex\"\n", installation{"~/.codex", first}},
		{"inuse", "", installation{second, second}},
	} {
		doc := strings.Replace(task(c.task, "CODEX=home CODEX=together", c.key), "provider: claude-code",
			"provider: codex", 1)
		d.call("POST", "/tasks", map[string]any{"yaml": doc}, 201)
		ran[d.call("POST", "/tasks/"+c.task+"/run", nil, 202)["executionId"].(string)] = c.want
	}
	for id, want := range ran {
		x := d.waitStatus(id, "completed", "failed")
		attempts, _ := x["attempts"].([]any)
		if x["status"] != "completed" || len(attempts) != 1 {
			t.Errorf("execution %s: status %v, attempts %v, error %v; want completed in one attempt", id, x["status"],
				attempts, x["errorMessage"])
			continue
		}
		if a := attempts[0].(map[string]any); a["installation"] != want.id || a["installationHome"] != want.home {
			t.Errorf("execution %s's attempt records the installation %v at %v, want %s at %s", id, a["installation"],
				a["installationHome"], want.id, want.home)
		}
		started, err := os.ReadFile(filepath.Join(d.home, "runs", id, "codex-home"))
		if err != nil {
			t.Fatalf("execution %s's Codex recorded no CODEX_HOME: %v", id, err)
		}
		if string(started) != want.home {
			t.Errorf("execution %s's Codex started with CODEX_HOME=%s, want %s alone", id, started, want.home)
		}
	}

	// Every process of the stand-in, probe or attempt, ran with one of the two homes. Each probe of a home, its sign-in
	// and its app server, ran with each home, as each attempt did with its own installation's. A probe of the binary,
	// its version or its help, reads no home: detection runs it once for the binary, not for each installation, with the
	// daemon's own environment, so it carries the CODEX_HOME of the installation in use, never ~/.codex's.
	both := []string{first, second}
	slices.Sort(both)
	wantHomes := map[string][]string{"--version": {second}, "exec --help": {second}, "--help": {second},
		"login status": both, "app-server": both, codexRun: both}
	started := startedWith(t, log)
	for args, homes := range started {
		if got := slices.Compact(slices.Sorted(slices.Values(homes))); !slices.Equal(got, wantHomes[args]) {
			t.Errorf("fake-codex %s started with CODEX_HOME %q, want %q", args, homes, wantHomes[args])
		}
	}
	for args, homes := range wantHomes {
		if len(started[args]) == 0 {
			t.Errorf("fake-codex %s never started, want it with CODEX_HOME %q", args, homes)
		}
	}
	if n := len(started[codexRun]); n != 2 {
		t.Errorf("fake-codex %s started %d times, want twice, once for each task", codexRun, n)
	}
}
