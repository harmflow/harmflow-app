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

// A share of the quota counts a plan window as exhausted once it is used at or above the share: a reading below it
// starts, one at it does not. A share outside (0, 100] counts as the whole window, which keeps the check at 100%.
func TestQuotaExhaustedAtAShare(t *testing.T) {
	cases := []struct {
		name  string
		used  float64
		share float64
		want  bool
	}{
		{"below the share", 94, 95, false},
		{"at the share", 95, 95, true},
		{"above the share", 96.5, 95, true},
		{"the default share below 100%", 99.9, FullShare, false},
		{"the default share at 100%", 100, FullShare, true},
		{"a share of 0 counts as the whole window", 50, 0, false},
		{"a share above 100 counts as the whole window", 100, 150, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := Quota{Windows: []QuotaWindow{{Name: "session", UsedPercent: c.used}}}
			if _, got := q.ExhaustedAt(c.share); got != c.want {
				t.Errorf("ExhaustedAt(%v) with %v%% used = %v, want %v", c.share, c.used, got, c.want)
			}
		})
	}
}

// A share is valid above 0 and at most 100.
func TestValidShare(t *testing.T) {
	for share, want := range map[float64]bool{-1: false, 0: false, 0.5: true, 95: true, 100: true, 100.1: false} {
		if got := ValidShare(share); got != want {
			t.Errorf("ValidShare(%v) = %v, want %v", share, got, want)
		}
	}
}
