package domain

import (
	"math"
	"time"
)

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

// FullShare is the share of a harness's quota HarmFlow may spend when the user sets none: the whole of each window.
const FullShare = 100.0

// ValidShare reports whether maxPercent is a share of a quota window HarmFlow may spend: above 0 and at most 100.
func ValidShare(maxPercent float64) bool {
	return maxPercent > 0 && maxPercent <= FullShare
}

// DefaultQuotaCheckMinutes is how long, in minutes, a harness's quota reading stands when the user sets no interval.
const DefaultQuotaCheckMinutes = 5

// MaxQuotaCheckMinutes is the longest quota check interval the user may set, and the longest a failed read is kept
// when the interval is shorter.
const MaxQuotaCheckMinutes = 60

// ValidQuotaCheckMinutes reports whether minutes is a quota check interval the user may set: a whole number from 1 to
// 60.
func ValidQuotaCheckMinutes(minutes float64) bool {
	return minutes >= 1 && minutes <= MaxQuotaCheckMinutes && minutes == math.Trunc(minutes)
}

// Exhausted reports the first limit that leaves the quota exhausted: a plan window at the whole of it, a rate limit
// reported as reached, or the account's spend control reached. An attempt that does not allow extra usage does not
// start on it.
func (q Quota) Exhausted() (QuotaWindow, bool) {
	return q.ExhaustedAt(FullShare)
}

// ExhaustedAt is Exhausted with a plan window counted as exhausted once it is used at or above maxPercent, the share of
// it HarmFlow may spend. A share outside ValidShare counts as the whole window.
func (q Quota) ExhaustedAt(maxPercent float64) (QuotaWindow, bool) {
	if !ValidShare(maxPercent) {
		maxPercent = FullShare
	}
	for _, w := range q.Windows {
		if w.UsedPercent >= maxPercent {
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
