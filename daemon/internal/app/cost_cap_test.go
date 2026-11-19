package app

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// harmflow-app#206: an attempt the cost cap stops ends budget_exhausted, and the execution fails with the cap as its
// reason, with no failover to the Codex fallback. The attempt received the whole cap and records it beside its cost,
// and the failure raises the failure event the operational-failure notice follows.
func TestCostCapEndsTheExecutionWithNoFailover(t *testing.T) {
	e := newEnv(t)
	e.create("capped", codexFallback+"  execution:\n    maxAttemptCostUsd: 1.5\n")
	var mu sync.Mutex
	var got []float64
	e.claude.setScript(func(req ports.RunRequest) string {
		mu.Lock()
		got = append(got, req.MaxCostUSD)
		mu.Unlock()
		return "echo 'END budget'"
	})
	e.codex.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	r, _ := e.app.RunTask("capped")
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	if len(x.Attempts) != 1 || x.Attempts[0].Outcome != domain.AttemptBudgetExhausted || x.HarnessSwitches != 0 {
		t.Fatalf("attempts %+v, %d switches; want one attempt budget_exhausted and no switch", x.Attempts, x.HarnessSwitches)
	}
	if c := x.Attempts[0].CostCapUSD; c == nil || *c != 1.5 {
		t.Errorf("the attempt's costCapUsd is %v, want 1.5", c)
	}
	mu.Lock()
	if len(got) != 1 || got[0] != 1.5 {
		t.Errorf("Claude Code was asked for caps %v, want [1.5]", got)
	}
	mu.Unlock()
	if x.ErrorKey != "execution_budget_exhausted" || !strings.Contains(x.ErrorMessage, "USD 1.50") {
		t.Errorf("the execution's error is %q (%q), want the cap of USD 1.50", x.ErrorMessage, x.ErrorKey)
	}
	if n := e.codex.promptCount(); n != 0 {
		t.Errorf("Codex got %d prompts, want 0", n)
	}
	// The event follows the record's terminal status.
	if !e.poll(5*time.Second, func() bool {
		counts, _ := e.eventCounts(r.ExecutionID)
		return counts[domain.EventExecutionFailed] == 1
	}) {
		t.Errorf("no %s event, want one", domain.EventExecutionFailed)
	}
}

// Every attempt gets the whole cap: a retry after a rate limit runs with it again. A Codex attempt, after a switch,
// runs with none, records none, and warns that the cap is not enforced.
func TestCostCapForEveryAttempt(t *testing.T) {
	e := newEnv(t)
	e.create("capped", codexFallback+"  execution:\n    maxAttemptCostUsd: 2\n")
	var mu sync.Mutex
	var claude, codex []float64
	e.claude.setScript(func(req ports.RunRequest) string {
		mu.Lock()
		defer mu.Unlock()
		claude = append(claude, req.MaxCostUSD)
		if len(claude) == 1 {
			return "echo 'END rate'"
		}
		return "echo 'END quota'"
	})
	e.codex.setScript(func(req ports.RunRequest) string {
		mu.Lock()
		codex = append(codex, req.MaxCostUSD)
		mu.Unlock()
		return "echo 'END ok'"
	})
	r, _ := e.app.RunTask("capped")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	mu.Lock()
	if len(claude) != 2 || claude[0] != 2 || claude[1] != 2 || len(codex) != 1 || codex[0] != 0 {
		t.Errorf("caps asked for: Claude Code %v, Codex %v; want [2 2] and [0]", claude, codex)
	}
	mu.Unlock()
	if len(x.Attempts) != 3 {
		t.Fatalf("%d attempts, want 3", len(x.Attempts))
	}
	for i, at := range x.Attempts {
		want := at.Harness == domain.HarnessClaudeCode
		if (at.CostCapUSD != nil) != want {
			t.Errorf("attempt %d on %s has costCapUsd %v, want one: %t", i+1, at.Harness, at.CostCapUSD, want)
		}
	}
	unenforced := false
	for _, w := range x.Warnings {
		if w.Code == domain.WarningPolicyUnenforced && w.Subject == "maxAttemptCostUsd" {
			unenforced = true
		}
	}
	if !unenforced {
		t.Errorf("warnings %+v; want maxAttemptCostUsd not enforced on Codex", x.Warnings)
	}
}

// Validation warns on a Codex profile of a capped task, rejects a cap out of range or with more than two decimals, and
// rejects a cap on a script task.
func TestCostCapValidation(t *testing.T) {
	e := newEnv(t)
	capped := func(v string) string {
		return taskYAML("check", codexFallback+"  execution:\n    maxAttemptCostUsd: "+v+"\n")
	}
	res := e.app.Validate(t.Context(), TaskInput{YAML: capped("1.25")})
	var unenforced []string
	for _, w := range res.Warnings {
		if w.Code == domain.WarningPolicyUnenforced && w.Key == "cost_cap_unenforced" {
			unenforced = append(unenforced, w.Path)
		}
	}
	if !res.Valid || len(unenforced) != 1 || unenforced[0] != "/spec/harness/fallback/candidates/0/provider" {
		t.Errorf("valid %v, cost-cap warnings at %v; want one, on the Codex fallback", res.Valid, unenforced)
	}
	res = e.app.Validate(t.Context(), TaskInput{YAML: taskYAML("check", codexFallback)})
	for _, w := range res.Warnings {
		if w.Key == "cost_cap_unenforced" {
			t.Errorf("warning %+v on a task with no cap", w)
		}
	}
	for _, v := range []string{"0", "0.001", "1.005", "1000.01", "-1"} {
		if res := e.app.Validate(t.Context(), TaskInput{YAML: capped(v)}); res.Valid {
			t.Errorf("maxAttemptCostUsd %s: valid, want rejected", v)
		}
	}
	script := `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: program
  name: Program
spec:
  type: script
  script:
    command: [/bin/echo, hi]
  execution:
    maxAttemptCostUsd: 1
`
	if res := e.app.Validate(t.Context(), TaskInput{YAML: script}); res.Valid || len(res.Errors) == 0 ||
		!strings.Contains(res.Errors[0].Path, "/spec/execution") {
		t.Errorf("a script task that sets maxAttemptCostUsd: valid %v, errors %+v; want it rejected", res.Valid, res.Errors)
	}
}
