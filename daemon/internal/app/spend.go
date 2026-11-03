package app

import (
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// Spend is the current month's spend per day (harmflow-app#583), summed when asked for from each attempt's figures in
// the executions' summaries: no file of totals is kept. Days run from the first of the month to today, in the machine's
// time zone, the one the counters' midnight follows; the month's days to come are counted by DaysInMonth alone.
type Spend struct {
	Month       string     `json:"month"`
	DaysInMonth int        `json:"daysInMonth"`
	Days        []SpendDay `json:"days"`
	Total       SpendTotal `json:"total"`
}

// SpendDay is one day's own totals, never a running sum: the cost in USD and the tokens, every class summed, of the
// attempts that ended that day, and what makes those figures undercount.
type SpendDay struct {
	Date       string          `json:"date"`
	CostUSD    float64         `json:"costUsd"`
	Tokens     int64           `json:"tokens"`
	Attempts   int             `json:"attempts"`
	Undercount SpendUndercount `json:"undercount"`
}

// SpendUndercount counts the attempts behind each reason a day's figures undercount: an attempt whose model has no
// price, so it has tokens and no cost; one whose harness reported no usage; one that ended with only a partial count;
// and, today, one still running, which counts on no day until it ends.
type SpendUndercount struct {
	Unpriced    int `json:"unpriced"`
	NotReported int `json:"notReported"`
	Partial     int `json:"partial"`
	Running     int `json:"running"`
}

// SpendTotal is the month's total in both units.
type SpendTotal struct {
	CostUSD float64 `json:"costUsd"`
	Tokens  int64   `json:"tokens"`
}

// Spend sums the current month's spend per day. An attempt counts on the day it ended, so an execution whose attempts
// end on both sides of midnight falls in both days, and an attempt of an active execution that has not ended counts as
// running today.
func (a *App) Spend() Spend {
	loc := a.d.Location
	now := a.now().In(loc)
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	next := first.AddDate(0, 1, 0)
	s := Spend{Month: first.Format("2006-01"), DaysInMonth: next.AddDate(0, 0, -1).Day(),
		Days: make([]SpendDay, now.Day())}
	for i := range s.Days {
		s.Days[i].Date = first.AddDate(0, 0, i).Format("2006-01-02")
	}
	today := &s.Days[len(s.Days)-1]
	add := func(spend []domain.AttemptSpend, active bool) {
		for _, at := range spend {
			if at.EndedAt == nil {
				if active {
					today.Undercount.Running++
				}
				continue
			}
			t := at.EndedAt.In(loc)
			if t.Before(first) || !t.Before(next) || t.Day() > len(s.Days) {
				continue
			}
			d := &s.Days[t.Day()-1]
			d.Attempts++
			d.Tokens += at.Tokens
			if at.CostUSD != nil {
				d.CostUSD += *at.CostUSD
			}
			switch {
			case at.Usage == domain.UsagePartial:
				d.Undercount.Partial++
			case at.Usage == domain.UsageNotReported:
				d.Undercount.NotReported++
			case at.CostUSD == nil:
				d.Undercount.Unpriced++
			}
		}
	}
	a.mu.Lock()
	for _, x := range a.executions {
		add(x.Summarize().Spend, x.Status.IsActive())
	}
	// An ended execution's attempts all ended by its own end, so one that ended before the month has none in it.
	for i := len(a.byEnd) - 1; i >= 0 && !a.byEnd[i].at.Before(first); i-- {
		if x := a.ended[a.byEnd[i].id]; x != nil {
			add(x.Spend, false)
		}
	}
	a.mu.Unlock()
	for _, d := range s.Days {
		s.Total.CostUSD += d.CostUSD
		s.Total.Tokens += d.Tokens
	}
	return s
}
