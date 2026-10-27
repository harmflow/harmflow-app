package domain

import "time"

// QuotaWindow is one limit of a harness's quota, named as the harness names it: how much of it is used, and when it
// resets, as a time when the harness gives one, or in the harness's own words when it gives only those.
type QuotaWindow struct {
	Name        string
	UsedPercent float64
	ResetsAt    time.Time
	Resets      string
}

// Quota is one reading of a harness's quota: its plan windows, the rate limit it reports as reached, whether the
// account's spend control is reached, and whether it is spending extra usage now.
type Quota struct {
	Windows []QuotaWindow
	// Limit is the rate limit the harness reports as reached; nil when it reports none.
	Limit               *QuotaWindow
	SpendControlReached bool
	UsingExtraUsage     bool
}

// SpendControl names the account's spend control when it is what exhausts a quota.
const SpendControl = "spendControlReached"

// Exhausted reports the first limit that leaves the quota exhausted: a plan window at 100%, a rate limit reported as
// reached, or the account's spend control reached. An attempt that does not allow extra usage does not start on it.
func (q Quota) Exhausted() (QuotaWindow, bool) {
	for _, w := range q.Windows {
		if w.UsedPercent >= 100 {
			return w, true
		}
	}
	if q.Limit != nil {
		return *q.Limit, true
	}
	if q.SpendControlReached {
		return QuotaWindow{Name: SpendControl}, true
	}
	return QuotaWindow{}, false
}
