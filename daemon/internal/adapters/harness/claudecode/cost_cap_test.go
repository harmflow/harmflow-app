package claudecode

import (
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// harmflow-app#206: the attempt's cost cap reaches the CLI as --max-budget-usd, and no cap passes no flag.
func TestPreparePassesTheCostCap(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/claude"}
	perm := domain.TaskDocument{}.Effective().Spec.Permissions
	for _, c := range []struct {
		cap  float64
		want string
	}{{1.5, "1.50"}, {0.01, "0.01"}, {1000, "1000.00"}, {0, ""}} {
		inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: *perm, ExecutionDir: t.TempDir(),
			Prompt: "hi", MaxCostUSD: c.cap})
		if err != nil {
			t.Fatal(err)
		}
		got, ok := flagValue(inv.Args, "--max-budget-usd")
		if c.want == "" && ok {
			t.Errorf("no cap: --max-budget-usd %q, want no flag", got)
		}
		if c.want != "" && got != c.want {
			t.Errorf("cap %v: --max-budget-usd %q, want %q", c.cap, got, c.want)
		}
	}
	if !a.CapsCost() {
		t.Error("CapsCost() = false, want true")
	}
}

// A session --max-budget-usd stopped ends budget_exhausted, whatever its exit code; the result line is the one
// Claude Code 2.1.309 printed when it stopped at a cap of USD 0.001.
func TestFinishReadsTheCostCap(t *testing.T) {
	for _, exit := range []int{0, 1} {
		p := &parser{tools: map[string]toolUse{}}
		p.Line([]byte(`{"type":"result","subtype":"error_max_budget_usd","is_error":true,"num_turns":1,` +
			`"session_id":"s","total_cost_usd":0.0026}`))
		end := p.Finish(exit, "", false, false)
		if end.Class != domain.ClassBudgetExhausted || end.Error.Key != "attempt_budget_exhausted" {
			t.Errorf("Finish(%d).Class = %s (%q, %q), want %s", exit, end.Class, end.Error.Message, end.Error.Key,
				domain.ClassBudgetExhausted)
		}
	}
}
