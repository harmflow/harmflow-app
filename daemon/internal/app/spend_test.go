package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// spendApp holds the executions given, the ended ones as their summaries, at the instant now of loc.
func spendApp(now time.Time, loc *time.Location, xs ...*domain.Execution) *App {
	a := &App{d: Deps{Clock: &fakeClock{now: now}, Location: loc}, executions: map[string]*domain.Execution{},
		ended: map[string]*domain.ExecutionSummary{}, latest: map[string]placed{}}
	for _, x := range xs {
		if x.Status.IsTerminal() {
			a.keepEndedLocked(x, x.Summarize())
		} else {
			a.addExecutionLocked(x)
		}
	}
	return a
}

func spendUsage(status domain.UsageStatus, tokens int64, cost *float64) *domain.Usage {
	return &domain.Usage{Status: status, CostUSD: cost,
		Models: []domain.ModelUsage{{Model: "m", Tokens: domain.Tokens{Input: tokens / 2, Output: tokens - tokens/2}}}}
}

func usd(v float64) *float64 { return &v }

func TestSpendSumsEachDayFromTheAttemptsThatEndedOnIt(t *testing.T) {
	loc := time.FixedZone("UTC-3", -3*3600)
	at := func(month time.Month, day, hour, minute int) *time.Time {
		v := time.Date(2026, month, day, hour, minute, 0, 0, loc)
		return &v
	}
	ended := func(id string, status domain.ExecutionStatus, end *time.Time, attempts ...domain.Attempt) *domain.Execution {
		return &domain.Execution{ID: id, TaskID: "t", Status: status, CreatedAt: attempts[0].StartedAt,
			StartedAt: &attempts[0].StartedAt, EndedAt: end, Attempts: attempts}
	}
	attempt := func(n int, end *time.Time, u *domain.Usage) domain.Attempt {
		return domain.Attempt{Number: n, Harness: "claude-code", StartedAt: end.Add(-time.Minute), EndedAt: end, Usage: u}
	}
	script := ended("script", domain.StatusCompleted, at(11, 3, 9, 0), attempt(1, at(11, 3, 9, 0), nil))
	script.Script = &domain.ScriptRun{Program: "/bin/echo"}
	running := domain.Attempt{Number: 2, Harness: "codex", StartedAt: *at(11, 3, 12, 1)}
	a := spendApp(*at(11, 3, 15, 40), loc,
		ended("october", domain.StatusCompleted, at(10, 15, 9, 0),
			attempt(1, at(10, 15, 9, 0), spendUsage(domain.UsageReported, 900, usd(9)))),
		// Its attempts end on both sides of midnight: the first in October, the second on the first.
		ended("midnight", domain.StatusCompleted, at(11, 1, 0, 10),
			attempt(1, at(10, 31, 23, 30), spendUsage(domain.UsageReported, 400, usd(4))),
			attempt(2, at(11, 1, 0, 10), spendUsage(domain.UsageReported, 1000, usd(1.5)))),
		// 22:00 here is 01:00 of the third in UTC: the machine's time zone places it on the second.
		ended("unpriced", domain.StatusFailed, at(11, 2, 22, 0),
			attempt(1, at(11, 2, 22, 0), spendUsage(domain.UsageReported, 500, nil))),
		ended("short", domain.StatusFailed, at(11, 3, 10, 5),
			attempt(1, at(11, 3, 10, 0), spendUsage(domain.UsagePartial, 200, nil)),
			attempt(2, at(11, 3, 10, 5), nil)),
		script,
		&domain.Execution{ID: "active", TaskID: "t", Status: domain.StatusRunning, CreatedAt: *at(11, 3, 11, 0),
			Attempts: []domain.Attempt{attempt(1, at(11, 3, 12, 0), spendUsage(domain.UsageReported, 300, usd(2))), running}},
	)
	want := Spend{Month: "2026-11", DaysInMonth: 30, Days: []SpendDay{
		{Date: "2026-11-01", CostUSD: 1.5, Tokens: 1000, Attempts: 1},
		{Date: "2026-11-02", Tokens: 500, Attempts: 1, Undercount: SpendUndercount{Unpriced: 1}},
		{Date: "2026-11-03", CostUSD: 2, Tokens: 500, Attempts: 3,
			Undercount: SpendUndercount{NotReported: 1, Partial: 1, Running: 1}},
	}, Total: SpendTotal{CostUSD: 3.5, Tokens: 2000}}
	if got := a.Spend(); !reflect.DeepEqual(got, want) {
		t.Errorf("Spend() = %+v, want %+v", got, want)
	}
}

func TestSpendOnTheFirstDayOfAMonthHasOneDay(t *testing.T) {
	loc := time.FixedZone("UTC+9", 9*3600)
	a := spendApp(time.Date(2027, time.February, 1, 0, 5, 0, 0, loc), loc)
	want := Spend{Month: "2027-02", DaysInMonth: 28, Days: []SpendDay{{Date: "2027-02-01"}}}
	if got := a.Spend(); !reflect.DeepEqual(got, want) {
		t.Errorf("Spend() = %+v, want %+v", got, want)
	}
}
