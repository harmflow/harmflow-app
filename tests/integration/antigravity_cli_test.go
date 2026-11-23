package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// antigravityTask is a task whose preferred harness is the Antigravity CLI.
func antigravityTask(id, prompt, extra string) string {
	return strings.Replace(task(id, prompt, extra), "provider: claude-code", "provider: antigravity-cli", 1)
}

// antigravityHarness is the Antigravity CLI in the daemon's harness list.
func antigravityHarness(t *testing.T, d *daemon) map[string]any {
	t.Helper()
	for _, h := range d.list("/harnesses") {
		if h["id"] == "antigravity-cli" {
			return h
		}
	}
	t.Fatal("GET /harnesses has no antigravity-cli")
	return nil
}

// claudeCodeFallback is the task's fallback to Claude Code.
const claudeCodeFallback = "    fallback:\n      candidates:\n        - provider: claude-code\n"

// endedRun saves the task yaml as id, runs it, and returns its execution once it ends, with its attempts.
func (d *daemon) endedRun(id, yaml string) (map[string]any, []any) {
	d.t.Helper()
	d.call("POST", "/tasks", map[string]any{"yaml": yaml}, 201)
	run := d.call("POST", "/tasks/"+id+"/run", nil, 202)
	x := d.waitStatus(run["executionId"].(string), "completed", "failed")
	attempts, _ := x["attempts"].([]any)
	return x, attempts
}

// A task on antigravity-cli runs to completion on the stand-in, unattended in its workspace, with its prompt on stdin
// and its permission rules for that session alone, and its log holds the stream's events as the event types the other
// harnesses produce. The daemon keeps its token in a file here, so it stays off the Keychain and the CLI's sign-in
// reads unknown, which lets it run.
func TestAntigravityCLITaskRunsToCompletion(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir(), "GEMINI_API_KEY=stand-in-key", "AGY_ADC_AUTH=true")
	if h := antigravityHarness(t, d); h["readiness"] != "ready" || h["authentication"] != "unknown" {
		t.Fatalf("antigravity-cli reads %v, authentication %v (%v); want ready, unknown", h["readiness"], h["authentication"],
			h["diagnostic"])
	}
	x, attempts := d.endedRun("agy", antigravityTask("agy", "AGY=ok", ""))
	id := x["id"].(string)
	if x["status"] != "completed" || len(attempts) != 1 {
		t.Fatalf("endedRun(agy) = status %v, %d attempts (error %v); want completed, 1", x["status"], len(attempts),
			x["errorMessage"])
	}
	if a := attempts[0].(map[string]any); a["harness"] != "antigravity-cli" || a["model"] != "gemini-3.8-flash-medium" {
		t.Errorf("attempt %v, want antigravity-cli on the model the stand-in reported", a)
	}
	args, err := os.ReadFile(filepath.Join(d.home, "workspace", "agy", "agy-args"))
	if err != nil {
		t.Fatalf("the stand-in was not started in the workspace: %v", err)
	}
	for _, want := range []string{"--input-format\nstream-json", "--output-format\nstream-json", "--mode\naccept-edits", "--settings"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("the stand-in's arguments %q lack %q", args, want)
		}
	}
	if strings.Contains(string(args), "dangerously") {
		t.Errorf("the daemon ran agy with %q, want no --dangerously-skip-permissions", args)
	}
	events, _ := d.storedLog(t, id)
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Type] = true
	}
	for _, want := range []string{"harness.started", "harness.message", "tool.started", "tool.completed"} {
		if !seen[want] {
			t.Errorf("the log lacks %s: %v", want, seen)
		}
	}
}

// On macOS, the Antigravity CLI reads signed in by its Keychain item alone: with none, it reads not ready, a key in the
// environment notwithstanding; with the item, ready. The daemon only looks the item up, in a throwaway keychain.
func TestAntigravityCLIReadyByItsKeychainItem(t *testing.T) {
	keychain := throwawayKeychain(t)
	d := startDaemonOn(t, t.TempDir(), keychain, "GEMINI_API_KEY=stand-in-key")
	h := d.setHarness("antigravity-cli", fakeHarnesses()["antigravity-cli"])
	if h["readiness"] != "not_ready" || h["authentication"] != "invalid" || h["diagnosticKey"] != "antigravity_not_signed_in" {
		t.Fatalf("with no Keychain item: readiness %v, authentication %v, diagnostic %v; want not_ready, invalid, antigravity_not_signed_in",
			h["readiness"], h["authentication"], h["diagnosticKey"])
	}
	runSecurity(t, "add-generic-password", "-s", "gemini", "-a", "antigravity", "-w", "stand-in-sign-in", keychain)
	if h := d.setHarness("antigravity-cli", fakeHarnesses()["antigravity-cli"]); h["readiness"] != "ready" || h["authentication"] != "valid" {
		t.Fatalf("with the Keychain item: readiness %v, authentication %v; want ready, valid", h["readiness"], h["authentication"])
	}
}

// The Antigravity CLI's failures are classified as the others' are: quota, a sign-in, and an account the CLI will not
// serve fail over to Claude Code, whose attempt succeeds.
func TestAntigravityCLIFailover(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	cases := []struct{ scenario, outcome string }{
		{"quota", "quota_exhausted"},
		{"signedout", "harness_unavailable"},
		{"ineligible", "harness_unavailable"},
	}
	for _, c := range cases {
		t.Run(c.scenario, func(t *testing.T) {
			d := d.in(t)
			id := "agy-" + c.scenario
			x, attempts := d.endedRun(id, antigravityTask(id, "AGY="+c.scenario, claudeCodeFallback))
			if x["status"] != "completed" || len(attempts) != 2 {
				t.Fatalf("endedRun(%s) = status %v, %d attempts (error %v); want completed, 2", id, x["status"],
					len(attempts), x["errorMessage"])
			}
			if a := attempts[0].(map[string]any); a["harness"] != "antigravity-cli" || a["outcome"] != c.outcome {
				t.Errorf("endedRun(%s) first attempt = %v, %v; want antigravity-cli, %s", id, a["harness"], a["outcome"],
					c.outcome)
			}
			if a := attempts[1].(map[string]any); a["harness"] != "claude-code" || a["outcome"] != "succeeded" {
				t.Errorf("endedRun(%s) fallback attempt = %v, %v; want claude-code, succeeded", id, a["harness"],
					a["outcome"])
			}
		})
	}
}

// A network failure and an action headless mode refused fail the task on the Antigravity CLI, its fallback to Claude
// Code notwithstanding.
func TestAntigravityCLINoFailover(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	cases := []struct{ scenario, outcome string }{
		{"network", "network_failed"},
		{"denied", "failed"},
	}
	for _, c := range cases {
		t.Run(c.scenario, func(t *testing.T) {
			d := d.in(t)
			id := "agy-" + c.scenario
			x, attempts := d.endedRun(id, antigravityTask(id, "AGY="+c.scenario, claudeCodeFallback))
			if x["status"] != "failed" || len(attempts) != 1 {
				t.Fatalf("endedRun(%s) = status %v, %d attempts; want failed, 1", id, x["status"], len(attempts))
			}
			if a := attempts[0].(map[string]any); a["harness"] != "antigravity-cli" || a["outcome"] != c.outcome {
				t.Errorf("endedRun(%s) attempt = %v, %v; want antigravity-cli, %s", id, a["harness"], a["outcome"],
					c.outcome)
			}
		})
	}
}

// A task whose Claude Code runs out of quota fails over to the Antigravity CLI, whose attempt succeeds.
func TestAntigravityCLIFailoverFromClaudeCode(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	x, attempts := d.endedRun("to-agy", task("to-agy", "SCENARIO=quota",
		"    fallback:\n      candidates:\n        - provider: antigravity-cli\n"))
	if x["status"] != "completed" || len(attempts) != 2 {
		t.Fatalf("endedRun(to-agy) = status %v, %d attempts (error %v); want completed, 2", x["status"], len(attempts),
			x["errorMessage"])
	}
	if a := attempts[1].(map[string]any); a["harness"] != "antigravity-cli" || a["outcome"] != "succeeded" {
		t.Errorf("endedRun(to-agy) fallback attempt = %v, %v; want antigravity-cli, succeeded", a["harness"], a["outcome"])
	}
}

// A task on the Antigravity CLI whose profile denies the network is validated and run with a warning that the CLI does
// not keep the commands it runs off the network, on the task and on its execution, with a policy.unenforced event.
func TestAntigravityCLIUnenforcedSettingIsWarned(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	extra := "  permissions:\n    profile: workspace_write\n    network: deny\n"
	saved := d.call("POST", "/tasks", map[string]any{"yaml": antigravityTask("agy-offline", "AGY=ok", extra)}, 201)
	if w := policyWarning(saved["warnings"], "messageKey", "restriction_antigravity_unenforced"); w == nil {
		t.Errorf("POST /tasks warnings = %v, want a restriction_antigravity_unenforced warning", saved["warnings"])
	}
	run := d.call("POST", "/tasks/agy-offline/run", nil, 202)
	x := d.waitStatus(run["executionId"].(string), "completed", "failed")
	if x["status"] != "completed" {
		t.Fatalf("status %v error %v, want completed", x["status"], x["errorMessage"])
	}
	if w := policyWarning(x["warnings"], "subject", "network"); w == nil || w["messageKey"] != "restriction_antigravity_unenforced" {
		t.Errorf("execution warnings = %v, want the network setting warned", x["warnings"])
	}
	found := false
	for _, e := range d.events(x["id"].(string)) {
		if p, _ := e["payload"].(map[string]any); e["type"] == "policy.unenforced" && p["subject"] == "network" {
			found = true
		}
	}
	if !found {
		t.Error("the execution's events hold no policy.unenforced event on network")
	}
}
