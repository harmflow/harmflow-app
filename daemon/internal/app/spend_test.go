package app

import (
	"bytes"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
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

// spendAfterRestart keeps ended executions of this month and of the last one through the real file store, in a time
// zone whose midnight is not UTC's, and restarts the daemon on the same home. It returns the restarted env, the IDs of
// the executions kept, and the spend their records state. Every cost is a binary fraction, so each sum is exact, and
// each day's sums and the month's totals are values no single attempt carries.
func spendAfterRestart(t *testing.T) (*env, []string, Spend) {
	t.Helper()
	loc := time.FixedZone("UTC-3", -3*3600)
	at := func(month time.Month, day, hour, minute int) *time.Time {
		v := time.Date(2026, month, day, hour, minute, 0, 0, loc)
		return &v
	}
	attempt := func(n int, end *time.Time, u *domain.Usage) domain.Attempt {
		return domain.Attempt{Number: n, Harness: "claude-code", StartedAt: end.Add(-time.Minute), EndedAt: end, Usage: u}
	}
	ended := func(id string, status domain.ExecutionStatus, attempts ...domain.Attempt) *domain.Execution {
		return &domain.Execution{SchemaVersion: domain.ExecutionSchemaVersion, ID: id, TaskID: "t", Status: status,
			CreatedAt: attempts[0].StartedAt, StartedAt: &attempts[0].StartedAt,
			EndedAt: attempts[len(attempts)-1].EndedAt, Attempts: attempts}
	}
	reported := func(tokens int64, cost float64) *domain.Usage {
		return spendUsage(domain.UsageReported, tokens, usd(cost))
	}
	xs := []*domain.Execution{
		ended("october", domain.StatusCompleted, attempt(1, at(10, 20, 12, 0), reported(9_000_001, 5.5))),
		// 23:30 of the 31st here is 02:30 of the first in UTC: that attempt is last month's.
		ended("first-day", domain.StatusCompleted,
			attempt(1, at(10, 31, 23, 30), reported(800_009, 0.71875)),
			attempt(2, at(11, 1, 9, 0), reported(1_234_567, 0.4140625)),
			attempt(3, at(11, 1, 9, 30), reported(2_345_678, 1.15625))),
		// Its attempts end on both sides of the local midnight, both on the third in UTC.
		ended("midnight", domain.StatusCompleted,
			attempt(1, at(11, 2, 23, 40), reported(4_102_301, 3.0390625)),
			attempt(2, at(11, 3, 0, 20), reported(2_918_406, 2.1171875))),
		ended("unpriced", domain.StatusCompleted,
			attempt(1, at(11, 2, 10, 0), reported(711_019, 0.28125)),
			attempt(2, at(11, 2, 11, 0), spendUsage(domain.UsageReported, 1_650_433, nil))),
		ended("partial", domain.StatusFailed,
			attempt(1, at(11, 3, 8, 0), spendUsage(domain.UsagePartial, 533_377, nil)),
			attempt(2, at(11, 3, 8, 30), reported(1_027_604, 0.6484375))),
		ended("not-reported", domain.StatusFailed, attempt(1, at(11, 3, 14, 0), nil)),
	}
	want := Spend{Month: "2026-11", DaysInMonth: 30, Days: []SpendDay{
		{Date: "2026-11-01", CostUSD: 1.5703125, Tokens: 3_580_245, Attempts: 2},
		{Date: "2026-11-02", CostUSD: 3.3203125, Tokens: 6_463_753, Attempts: 3, Undercount: SpendUndercount{Unpriced: 1}},
		{Date: "2026-11-03", CostUSD: 2.765625, Tokens: 4_479_387, Attempts: 4,
			Undercount: SpendUndercount{NotReported: 1, Partial: 1}},
		{Date: "2026-11-04"},
	}, Total: SpendTotal{CostUSD: 7.65625, Tokens: 14_523_385}}
	ids := make([]string, len(xs))
	for i, x := range xs {
		ids[i] = x.ID
	}
	// Every execution ended within the retention period, so no pass of it saves a record again.
	e := newEnv(t, func(e *env) {
		e.location = loc
		e.clock.now = *at(11, 4, 15, 0)
		for _, x := range xs {
			if err := e.records.Save(x); err != nil {
				t.Fatalf("Save(%s) = %v", x.ID, err)
			}
		}
	})
	if _, err := os.Stat(filepath.Join(e.paths.Executions, fsstore.IndexFile)); err != nil {
		t.Fatalf("the first start wrote no index: %v", err)
	}
	// Once the daemon is down, each record is overwritten with blanks of its size and given back its modification
	// time: the index still matches it, and a start that read the record instead would find no execution in it.
	n := e.restart(t, func(n *env) {
		for _, id := range ids {
			path := filepath.Join(n.paths.Executions, id+".json")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, bytes.Repeat([]byte(" "), int(info.Size())), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
		}
	})
	return n, ids, want
}

func TestSpendAfterARestartSumsTheIndexSummaries(t *testing.T) {
	e, ids, want := spendAfterRestart(t)
	e.app.mu.Lock()
	for _, id := range ids {
		if e.app.ended[id] == nil || e.app.executions[id] != nil {
			t.Errorf("after the restart, execution %s is held whole or not at all, want held as its summary", id)
		}
	}
	e.app.mu.Unlock()
	if got := e.app.Spend(); !reflect.DeepEqual(got, want) {
		t.Errorf("Spend() = %+v, want %+v", got, want)
	}
}

func TestSpendKeepsNoFileOfTotals(t *testing.T) {
	e, _, want := spendAfterRestart(t)
	files := func() map[string]string {
		t.Helper()
		m := map[string]string{}
		err := filepath.WalkDir(e.home, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			m[path] = string(data)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	before := files()
	for range 2 {
		if got := e.app.Spend(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Spend() = %+v, want %+v", got, want)
		}
	}
	after := files()
	if !maps.Equal(after, before) {
		t.Errorf("the calls to Spend changed the files under the home: %d files before, %d after", len(before), len(after))
	}
	var sums []string
	for _, d := range want.Days {
		if d.Attempts > 0 {
			sums = append(sums, strconv.FormatFloat(d.CostUSD, 'f', -1, 64), strconv.FormatInt(d.Tokens, 10))
		}
	}
	sums = append(sums, strconv.FormatFloat(want.Total.CostUSD, 'f', -1, 64), strconv.FormatInt(want.Total.Tokens, 10))
	for _, sum := range sums {
		// The sum as a number of its own, not as the digits of a longer one.
		re := regexp.MustCompile(`(^|[^0-9.])` + regexp.QuoteMeta(sum) + `($|[^0-9])`)
		for path, data := range after {
			if re.MatchString(data) {
				t.Errorf("%s holds the sum %s", path, sum)
			}
		}
	}
}
