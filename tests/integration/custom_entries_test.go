package integration

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// harmflow-app#826: across the real daemon, two Codex homes the owner adds as custom entries from a command line are
// installations of their own. The stand-in Codex, set by hand, finds the daemon user's .codex home; the first entry's
// CODEX_HOME names a home signed in with ChatGPT, the second's one signed in with an API key, with other models. POST
// /harness-entries lists each under Codex, beside the detected home, with its own sign-in and models and a default name
// that holds no home. A task naming each entry runs at the same time as the other; each attempt's Codex process starts
// with its own entry's home as CODEX_HOME, and records the entry's identifier and name, never its home.
func TestEachCustomEntryRunsWithItsOwnHome(t *testing.T) {
	t.Parallel()
	codex := standIn(t, "fake-codex", "codex")
	user := resolvedTempDir(t)
	detected := codexHomeAt(t, filepath.Join(user, ".codex"), "model-a", `{"type":"chatgpt","planType":"plus"}`)
	personal := codexHomeAt(t, filepath.Join(resolvedTempDir(t), "personal"), "model-p",
		`{"type":"chatgpt","planType":"pro"}`)
	work := codexHomeAt(t, filepath.Join(resolvedTempDir(t), "work"), "model-w", `{"type":"apiKey"}`)
	// The daemon's own CODEX_HOME names the detected home, so no home of the machine running the test is found.
	d := startDaemon(t, t.TempDir(), "HOME="+user, "CODEX_HOME="+detected)
	d.setHarness("codex", codex.path)

	type entry struct{ id, name, signIn, models, readiness string }
	read := func(h map[string]any) []entry {
		var out []entry
		list, _ := h["customEntries"].([]any)
		for _, e := range list {
			m, _ := e.(map[string]any)
			account, _ := m["account"].(map[string]any)
			var models []string
			ms, _ := m["models"].([]any)
			for _, model := range ms {
				model, _ := model.(map[string]any)
				models = append(models, jsonString(model["id"]))
			}
			out = append(out, entry{jsonString(m["installationId"]), jsonString(m["entryName"]),
				jsonString(account["signInMethod"]), strings.Join(models, " "), jsonString(m["readiness"])})
		}
		return out
	}
	var h map[string]any
	for _, home := range []string{personal, work} {
		h = d.call("POST", "/harness-entries", map[string]any{"line": "CODEX_HOME=" + home + " " + codex.path}, 201)
	}
	want := []entry{{"custom-1", "Codex 2", "chatgpt", "default model-p", "ready"},
		{"custom-2", "Codex 3", "api_key", "default model-w", "ready"}}
	if got := read(h); !slices.Equal(got, want) {
		t.Fatalf("POST /harness-entries twice lists the custom entries %+v, want %+v", got, want)
	}
	if homes, _ := h["homes"].([]any); len(homes) != 1 {
		t.Errorf("POST /harness-entries lists %d detected homes, want the daemon user's .codex alone", len(homes))
	}

	// Both tasks run before either is awaited, since each Codex waits until the other execution has started too. That
	// wait writes nothing to the execution's log, and under a full run's load it outlasts the silence waitStatus allows,
	// so each execution is awaited until it ends, for longer than the stand-in waits.
	homeOf := map[string]string{"custom-1": personal, "custom-2": work}
	ran := map[string]string{}
	for _, e := range want {
		name := "on" + strings.TrimPrefix(e.id, "custom-")
		doc := strings.Replace(task(name, "CODEX=home CODEX=together", "      installation: \""+e.id+"\"\n"),
			"provider: claude-code", "provider: codex", 1)
		d.call("POST", "/tasks", map[string]any{"yaml": doc}, 201)
		ran[e.id] = d.call("POST", "/tasks/"+name+"/run", nil, 202)["executionId"].(string)
	}
	for _, e := range want {
		t.Run(e.id, func(t *testing.T) {
			d, id := d.in(t), ran[e.id]
			x := d.waitEnd(id, 2*time.Minute)
			attempts, _ := x["attempts"].([]any)
			if x["status"] != "completed" || len(attempts) != 1 {
				t.Fatalf("execution %s ended %v with the attempts %v, error %v; want completed in one attempt", id,
					x["status"], attempts, x["errorMessage"])
			}
			a := attempts[0].(map[string]any)
			if a["installation"] != e.id || a["installationName"] != e.name || a["installationHome"] != nil {
				t.Errorf("execution %s's attempt records the installation %v named %v at %v, want %s named %s with no home",
					id, a["installation"], a["installationName"], a["installationHome"], e.id, e.name)
			}
			started, err := os.ReadFile(filepath.Join(d.home, "runs", id, "codex-home"))
			if err != nil {
				t.Fatalf("os.ReadFile(execution %s's codex-home) error = %v, want the CODEX_HOME its Codex started with", id,
					err)
			}
			if string(started) != homeOf[e.id] {
				t.Errorf("os.ReadFile(execution %s's codex-home) = %s, want %s alone", id, started, homeOf[e.id])
			}
		})
	}
}
