package domain

import "testing"

// harmflow-app#206: execution.maxAttemptCostUsd takes US dollars from 0.01 to 1,000 with at most two decimals.
func TestValidAttemptCost(t *testing.T) {
	for v, want := range map[float64]bool{
		0.01: true, 0.07: true, 1: true, 1.5: true, 19.99: true, 333.33: true, 1000: true,
		0: false, 0.001: false, 0.009: false, 1.005: false, 2.125: false, 1000.01: false, -1: false,
	} {
		if got := ValidAttemptCost(v); got != want {
			t.Errorf("ValidAttemptCost(%v) = %v, want %v", v, got, want)
		}
	}
}

// A task with no cap has none, and the cap survives the defaults and a clone.
func TestAttemptCostCap(t *testing.T) {
	var d TaskDocument
	if _, ok := d.AttemptCostCap(); ok {
		t.Error("a task with no execution block has a cap, want none")
	}
	v := 2.5
	d.Spec.Execution = &ExecutionLimits{MaxAttemptCostUSD: &v}
	if got, ok := d.Effective().AttemptCostCap(); !ok || got != 2.5 {
		t.Errorf("the effective task's cap is %v (%v), want 2.5", got, ok)
	}
}
