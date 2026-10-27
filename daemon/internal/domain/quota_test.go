package domain

import (
	"testing"
	"time"
)

// A task allows no extra usage unless it says so; a script task, which runs no harness, is given no such setting.
func TestExtraUsageDefault(t *testing.T) {
	task := minimalTask()
	if e := task.Effective(); e.Spec.Execution.AllowExtraUsage == nil || *e.Spec.Execution.AllowExtraUsage ||
		task.AllowsExtraUsage() {
		t.Errorf("Effective().Spec.Execution.AllowExtraUsage = %v, AllowsExtraUsage() = %v, want false and false",
			e.Spec.Execution.AllowExtraUsage, task.AllowsExtraUsage())
	}
	task.Spec.Execution = &ExecutionLimits{AllowExtraUsage: ptr(true)}
	if e := task.Effective(); !*e.Spec.Execution.AllowExtraUsage || !task.AllowsExtraUsage() {
		t.Errorf("a task that allows extra usage lost it: %v", e.Spec.Execution.AllowExtraUsage)
	}
	script := TaskDocument{Spec: TaskSpec{Type: TypeScriptTask, Script: &ScriptSpec{Command: []string{"/bin/true"}}}}
	if e := script.Effective(); e.Spec.Execution.AllowExtraUsage != nil {
		t.Errorf("a script task's Effective().Spec.Execution.AllowExtraUsage = %v, want nil", *e.Spec.Execution.AllowExtraUsage)
	}
}

// A quota is exhausted by a plan window at 100%, by a rate limit reported as reached, or by the spend control, the
// first of them in that order; extra usage alone, or windows below 100%, leave it usable.
func TestQuotaExhausted(t *testing.T) {
	resets := time.Date(2026, 10, 27, 18, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		quota Quota
		want  string
	}{
		{"nothing used", Quota{}, ""},
		{"windows below 100%", Quota{Windows: []QuotaWindow{{Name: "session", UsedPercent: 99.5}}}, ""},
		{"extra usage alone", Quota{UsingExtraUsage: true}, ""},
		{"a window at 100%", Quota{Windows: []QuotaWindow{{Name: "session", UsedPercent: 20},
			{Name: "week", UsedPercent: 100, ResetsAt: resets}}, Limit: &QuotaWindow{Name: "five_hour"}}, "week"},
		{"a rate limit reached", Quota{Limit: &QuotaWindow{Name: "five_hour"}, SpendControlReached: true}, "five_hour"},
		{"the spend control reached", Quota{SpendControlReached: true}, SpendControl},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, exhausted := c.quota.Exhausted()
			if exhausted != (c.want != "") || w.Name != c.want {
				t.Errorf("Exhausted() = %+v, %v, want %q", w, exhausted, c.want)
			}
		})
	}
}
