package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// recordingStandIn writes a wrapper, named for the CLI it stands for, around one of the stand-ins in testdata. Before
// the stand-in runs, each execution's arguments are recorded NUL-separated in $HARMFLOW_EXECUTION_DIR/args, the
// CLAUDE_CONFIG_DIR it runs with in claude-config-dir, and its stdin, read to its end, in stdin, which the stand-in
// then reads in turn. The read builtin stands in for cat, as in the stand-ins. The probes run with no execution
// directory and record nothing.
func recordingStandIn(t *testing.T, fake, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	stand := `"` + filepath.Join(repoRoot(), "tests", "integration", "testdata", fake) + `"`
	script := "#!/bin/bash\n" +
		`[ -z "$HARMFLOW_EXECUTION_DIR" ] && exec ` + stand + ` "$@"` + "\n" +
		`printf '%s\0' "$@" > "$HARMFLOW_EXECUTION_DIR/args"` + "\n" +
		`printf '%s' "$CLAUDE_CONFIG_DIR" > "$HARMFLOW_EXECUTION_DIR/claude-config-dir"` + "\n" +
		`IFS= read -r -d '' input` + "\n" +
		`printf '%s' "$input" > "$HARMFLOW_EXECUTION_DIR/stdin"` + "\n" +
		`exec ` + stand + ` "$@" < "$HARMFLOW_EXECUTION_DIR/stdin"` + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// recordedArgs are the arguments the recording stand-in received in an execution.
func recordedArgs(t *testing.T, x map[string]any) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(recorded(t, x, "args"), "\x00"), "\x00")
}

// recorded is what the recording stand-in recorded in an execution under the name: args, claude-config-dir, or stdin.
func recorded(t *testing.T, x map[string]any, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(x["executionDir"].(string), name))
	if err != nil {
		t.Fatalf("the stand-in recorded no %s: %v", name, err)
	}
	return string(data)
}

// passedPair reports whether args hold flag immediately followed by value.
func passedPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// completedRun runs a task and returns its execution, which must complete.
func (d *daemon) completedRun(id string) map[string]any {
	d.t.Helper()
	run := d.call("POST", "/tasks/"+id+"/run", nil, 202)
	x := d.waitStatus(run["executionId"].(string), "completed", "failed")
	if x["status"] != "completed" {
		d.t.Fatalf("task %s: status %v, error %v", id, x["status"], x["errorMessage"])
	}
	return x
}

// firstAttempt is an execution's first attempt.
func firstAttempt(t *testing.T, x map[string]any) map[string]any {
	t.Helper()
	attempts, _ := x["attempts"].([]any)
	if len(attempts) == 0 {
		t.Fatalf("execution %v has no attempt", x["id"])
	}
	return attempts[0].(map[string]any)
}

// profileTask is a task on harness whose preferred profile adds the given lines after its provider.
func profileTask(id, harness, profile string) string {
	return strings.Replace(task(id, "Do it.", profile), "provider: claude-code", "provider: "+harness, 1)
}

// checkClaudeFlags fails the test unless args pass --model and --effort with the given values, or, for an empty one,
// do not pass the flag at all.
func checkClaudeFlags(t *testing.T, what string, args []string, model, effort string) {
	t.Helper()
	for flag, want := range map[string]string{"--model": model, "--effort": effort} {
		switch {
		case want == "" && slices.Contains(args, flag):
			t.Errorf("%s: %s was passed: %q", what, flag, args)
		case want != "" && !passedPair(args, flag, want):
			t.Errorf("%s: no %s %s in %q", what, flag, want, args)
		}
	}
}

// checkCodexFlags fails the test unless args pass --model and model_reasoning_effort with the given values, or, for
// an empty one, pass neither the flag nor the setting.
func checkCodexFlags(t *testing.T, what string, args []string, model, effort string) {
	t.Helper()
	switch {
	case model == "" && slices.Contains(args, "--model"):
		t.Errorf("%s: --model was passed: %q", what, args)
	case model != "" && !passedPair(args, "--model", model):
		t.Errorf("%s: no --model %s in %q", what, model, args)
	}
	setting := `model_reasoning_effort="` + effort + `"`
	anyEffort := func(a string) bool { return strings.HasPrefix(a, "model_reasoning_effort=") }
	switch {
	case effort == "" && slices.ContainsFunc(args, anyEffort):
		t.Errorf("%s: a reasoning effort was passed: %q", what, args)
	case effort != "" && !passedPair(args, "-c", setting):
		t.Errorf("%s: no -c %s in %q", what, setting, args)
	}
}

// checkConfigured fails the test unless the attempt keeps the configured model and effort.
func checkConfigured(t *testing.T, what string, at map[string]any, model, effort string) {
	t.Helper()
	if at["configuredModel"] != model || at["configuredEffort"] != effort {
		t.Errorf("%s: configured model %v and effort %v, want %s and %s", what, at["configuredModel"],
			at["configuredEffort"], model, effort)
	}
}

// A task that sets no model or effort runs Claude Code with neither --model nor --effort, so the CLI's own defaults
// apply, and its attempt keeps default for both. One that sets model sonnet and effort high runs it with --model sonnet
// and --effort high, and its attempt keeps them. The attempt's model is the one the harness reports, whichever was
// configured.
func TestModelAndEffortReachTheHarness(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": recordingStandIn(t, "fake-claude", "claude")}, 200)
	d.call("POST", "/tasks", map[string]any{"yaml": task("defaults", "Say hi.", "")}, 201)
	d.call("POST", "/tasks", map[string]any{"yaml": task("chosen", "Say hi.",
		"      model: sonnet\n      effort: high\n")}, 201)
	for _, c := range []struct{ task, model, effort, configuredModel, configuredEffort string }{
		{"defaults", "", "", "default", "default"},
		{"chosen", "sonnet", "high", "sonnet", "high"},
	} {
		t.Run(c.task, func(t *testing.T) {
			d := d.in(t)

			x := d.completedRun(c.task)
			checkClaudeFlags(t, c.task, recordedArgs(t, x), c.model, c.effort)
			at := firstAttempt(t, x)
			checkConfigured(t, c.task, at, c.configuredModel, c.configuredEffort)
			if at["model"] != "claude-haiku-4-5-20251001" {
				t.Errorf("completedRun(%s) attempt model = %v, want claude-haiku-4-5-20251001, the one the stand-in reported", c.task, at["model"])
			}
		})
	}
}

// Saving a task checks its model and effort with the real adapters, against Codex's models cache and Claude Code's
// --help. An unsupported value is refused with 422 at the profile's path, and the task file keeps its bytes: nothing is
// saved in its place. A supported change is saved, and the next execution runs with it.
func TestChangedProfileIsValidatedAndAppliedNextRun(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// The daemon's CODEX_HOME is <home>/codex. Its cache offers one model, which accepts the efforts low and high.
	cache := filepath.Join(home, "codex", "models_cache.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"models":[
		{"slug":"gpt-test","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := startDaemon(t, home)
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": recordingStandIn(t, "fake-claude", "claude")}, 200)
	d.call("PUT", "/harnesses/codex", map[string]any{"path": recordingStandIn(t, "fake-codex", "codex")}, 200)
	if status, body := d.request("POST", "/harnesses/refresh", nil, nil); status != 200 {
		t.Fatalf("refresh: %d %s", status, body)
	}
	// Without these capabilities, validation would be skipped and every save below would pass.
	if h := d.harness("codex"); h["detected"] != true || h["capabilitySource"] != "reported" {
		t.Fatalf("Codex: detected %v, capabilities %v, diagnostic %v", h["detected"], h["capabilitySource"], h["diagnostic"])
	}
	if h := d.harness("claude-code"); h["detected"] != true || !slices.Contains(h["efforts"].([]any), any("high")) {
		t.Fatalf("Claude Code: detected %v, efforts %v, diagnostic %v", h["detected"], h["efforts"], h["diagnostic"])
	}

	type refusal struct{ profile, key string }
	cases := []struct {
		harness, id string
		refused     []refusal
		// The supported change, and what the next execution passes for it.
		profile, model, effort string
		check                  func(*testing.T, string, []string, string, string)
	}{
		{harness: "codex", id: "codex-pick", refused: []refusal{
			{"      model: no-such-model\n", "codex_model_unknown"},
			{"      model: gpt-test\n      effort: medium\n", "codex_effort_invalid"},
		}, profile: "      model: gpt-test\n      effort: high\n", model: "gpt-test", effort: "high",
			check: checkCodexFlags},
		{harness: "claude-code", id: "claude-pick", refused: []refusal{
			{"      effort: ultra\n", "claude_effort_invalid"},
			{"      model: gpt-5\n", "claude_model_unknown"},
		}, profile: "      model: opus\n      effort: low\n", model: "opus", effort: "low",
			check: checkClaudeFlags},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			d := d.in(t)

			created := d.call("POST", "/tasks", map[string]any{"yaml": profileTask(c.id, c.harness, "")}, 201)
			x := d.completedRun(c.id)
			c.check(t, c.id+" before the change", recordedArgs(t, x), "", "")
			checkConfigured(t, c.id+" before the change", firstAttempt(t, x), "default", "default")

			file := filepath.Join(home, "tasks", c.id+".yaml")
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range c.refused {
				status, body := d.request("PUT", "/tasks/"+c.id,
					map[string]any{"yaml": profileTask(c.id, c.harness, r.profile), "baseRevision": created["revision"]}, nil)
				if status != 422 || !refusedAt(t, body, "/spec/harness/preferred", r.key) {
					t.Errorf("PUT /tasks/%s with %q = %d %s, want 422 with %s at /spec/harness/preferred", c.id,
						strings.TrimSpace(r.profile), status, body, r.key)
				}
				if after, _ := os.ReadFile(file); !bytes.Equal(after, before) {
					t.Errorf("task file after a refused PUT with %q =\n%s\nwant it unchanged", strings.TrimSpace(r.profile), after)
				}
			}

			d.call("PUT", "/tasks/"+c.id,
				map[string]any{"yaml": profileTask(c.id, c.harness, c.profile), "baseRevision": created["revision"]}, 200)
			x = d.completedRun(c.id)
			c.check(t, c.id+" after the change", recordedArgs(t, x), c.model, c.effort)
			checkConfigured(t, c.id+" after the change", firstAttempt(t, x), c.model, c.effort)
		})
	}
}

// refusedAt reports whether a 422 body's validation lists an error with the message key at the path.
func refusedAt(t *testing.T, body []byte, path, key string) bool {
	t.Helper()
	var out struct {
		Error struct {
			Code       string `json:"code"`
			Validation struct {
				Errors []struct {
					Path       string `json:"path"`
					MessageKey string `json:"messageKey"`
				} `json:"errors"`
			} `json:"validation"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("the refusal is not JSON: %v: %s", err, body)
	}
	if out.Error.Code != "validation_failed" {
		return false
	}
	for _, e := range out.Error.Validation.Errors {
		if e.Path == path && e.MessageKey == key {
			return true
		}
	}
	return false
}
