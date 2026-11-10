package app

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// quotaTTL is how long a harness's quota reading stands before an attempt reads it again.
const quotaTTL = 5 * time.Minute

// quotaBackoffMax is the longest a harness's failed quota reads keep the next attempt from reading again.
const quotaBackoffMax = time.Hour

// quotaReading is a harness's last quota reading and when it was taken.
type quotaReading struct {
	quota domain.Quota
	at    time.Time
}

// quotaFailure is the quota reads of a harness's installation that failed in a row since its last good one: how many,
// the error of the last, and when it failed.
type quotaFailure struct {
	count int
	err   error
	at    time.Time
}

// backoff is how long the failed reads keep the next attempt from reading again: quotaTTL after the first, as long as
// a good reading stands, doubled with each failure after it, up to quotaBackoffMax. Claude Code's usage endpoint
// throttles with a Retry-After of 30 to 50 minutes, which the CLI does not pass on. The wait reaches 40 minutes at the
// fourth failure in a row and an hour at the fifth, so a throttled endpoint gets a few reads, not one per attempt, and
// once it answers again the hour bounds how long the check stays off.
func (f quotaFailure) backoff() time.Duration {
	d := quotaTTL
	for i := 1; i < f.count && d < quotaBackoffMax; i++ {
		d *= 2
	}
	return min(d, quotaBackoffMax)
}

// quota is the quota of the installation of a harness that info describes, as an attempt checks it before it starts:
// the reading of the last five minutes, or a new one, read with no model call. Attempts that start together share one
// read. read is false when the adapter reads no quota. Each installation runs under its own account, so each keeps a
// reading, and a backoff, of its own.
//
// A read that fails is kept for its backoff: the attempts in it get its error without reading again, and the first
// after it reads again. Only a good read ends the backoff. A running attempt's report is a reading, which stands for
// five minutes, but it is not a read of the harness's usage and leaves the backoff running. A read cut short by the
// attempt's own context, its execution's timeout or cancel, says nothing of the harness and is not kept.
func (a *App) quota(ctx context.Context, ad ports.HarnessAdapter, info ports.HarnessInfo) (q domain.Quota, read bool, err error) {
	reader, ok := ad.(ports.QuotaReader)
	if !ok {
		return domain.Quota{}, false, nil
	}
	key := quotaKey(ad.ID(), info.InstallationID)
	a.mu.Lock()
	lock := a.quotaReads[key]
	if lock == nil {
		lock = &sync.Mutex{}
		a.quotaReads[key] = lock
	}
	a.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	a.mu.Lock()
	last, kept := a.quotas[key]
	failed, failing := a.quotaFailures[key]
	a.mu.Unlock()
	now := a.now()
	if kept && now.Sub(last.at) < quotaTTL {
		return last.quota, true, nil
	}
	if failing && now.Sub(failed.at) < failed.backoff() {
		return domain.Quota{}, true, failed.err
	}
	q, err = reader.ReadQuota(ctx, info)
	if err != nil {
		if ctx.Err() == nil {
			a.mu.Lock()
			a.quotaFailures[key] = quotaFailure{count: failed.count + 1, err: err, at: a.now()}
			a.mu.Unlock()
		}
		return domain.Quota{}, true, err
	}
	a.mu.Lock()
	delete(a.quotaFailures, key)
	a.mu.Unlock()
	a.storeQuota(key, q)
	return q, true, nil
}

// quotaKey is what an installation's quota reading is kept under: the harness, and the installation when it has an
// identifier.
func quotaKey(harness, installation string) string {
	if installation == "" {
		return harness
	}
	return harness + " " + installation
}

// installationQuota is the reading as Settings shows it.
func (r quotaReading) installationQuota() *ports.InstallationQuota {
	out := &ports.InstallationQuota{Windows: []ports.QuotaWindowInfo{}, ReadAt: r.at}
	for _, w := range r.quota.Windows {
		win := ports.QuotaWindowInfo{Name: w.Name, UsedPercent: w.UsedPercent}
		if !w.ResetsAt.IsZero() {
			resets := w.ResetsAt
			win.ResetsAt = &resets
		}
		out.Windows = append(out.Windows, win)
	}
	return out
}

// storeQuota keeps an installation's quota reading, under its quotaKey, read before an attempt or reported by a running
// one.
func (a *App) storeQuota(key string, q domain.Quota) {
	a.mu.Lock()
	a.quotas[key] = quotaReading{quota: q, at: a.now()}
	a.mu.Unlock()
}

// quotaHeld checks the harness's quota before each attempt. For a task that allows no extra usage, an exhausted quota
// holds the attempt back: the end it returns is that quota, found before the session opens. A plan window counts as
// exhausted once it reaches the share of it the settings let HarmFlow spend, the whole window by default. A quota that
// cannot be read lets the attempt start, with a warning on the execution. A task that allows extra usage is checked
// too, and nothing holds it back. The share is checked here only: it never ends a running attempt.
func (r *run) quotaHeld(ctx context.Context, ad ports.HarnessAdapter, info ports.HarnessInfo) (domain.AttemptEnd, bool) {
	q, read, err := r.a.quota(ctx, ad, info)
	if !read {
		return domain.AttemptEnd{}, false
	}
	if err != nil {
		r.warn(domain.Warning{Code: domain.WarningOther, Subject: ad.ID(), Text: domain.T("quota_unread",
			"{{harness}}'s quota could not be read, so the attempt started without that check: {{detail}}",
			"harness", ad.Name()).Nest("detail", domain.TextOf(err))}, "", false)
		return domain.AttemptEnd{}, false
	}
	if r.def.AllowsExtraUsage() {
		return domain.AttemptEnd{}, false
	}
	share := r.a.quotaShare(ad.ID())
	w, exhausted := q.ExhaustedAt(share)
	if !exhausted {
		return domain.AttemptEnd{}, false
	}
	text := r.a.quotaExhaustedText(ad.Name(), w)
	// A plan window below the whole of it is held back by the share alone, and the end names the share.
	if w.UsedPercent < domain.FullShare && slices.Contains(q.Windows, w) {
		text = r.a.quotaShareText(ad.Name(), share, w)
	}
	return domain.AttemptEnd{Class: domain.ClassQuotaExhausted, Error: text, ResetsAt: w.ResetsAt, ExitCode: -1}, true
}

// quotaShareText names the plan window that reached the share of the harness's quota HarmFlow may spend, the share,
// and when the window resets, as the harness reported it.
func (a *App) quotaShareText(harness string, share float64, w domain.QuotaWindow) domain.Text {
	resets := w.Resets
	if !w.ResetsAt.IsZero() {
		resets = w.ResetsAt.In(a.d.Location).Format("2006-01-02 15:04 MST")
	}
	pct := strconv.FormatFloat(share, 'f', -1, 64)
	if resets == "" {
		return domain.T("quota_share_reached", "{{harness}} reached the {{share}}% of its quota HarmFlow may spend ({{limit}})",
			"harness", harness, "share", pct, "limit", w.Name)
	}
	return domain.T("quota_share_reached_until",
		"{{harness}} reached the {{share}}% of its quota HarmFlow may spend ({{limit}}) until {{resets}}",
		"harness", harness, "share", pct, "limit", w.Name, "resets", resets)
}

// quotaExhaustedText names the limit that exhausts a harness's quota and when it resets, as the harness reported it.
func (a *App) quotaExhaustedText(harness string, w domain.QuotaWindow) domain.Text {
	resets := w.Resets
	if !w.ResetsAt.IsZero() {
		resets = w.ResetsAt.In(a.d.Location).Format("2006-01-02 15:04 MST")
	}
	if resets == "" {
		return domain.T("quota_exhausted", "{{harness}} has no quota left ({{limit}})", "harness", harness,
			"limit", w.Name)
	}
	return domain.T("quota_exhausted_until", "{{harness}} has no quota left ({{limit}}) until {{resets}}",
		"harness", harness, "limit", w.Name, "resets", resets)
}

// onQuota keeps what a running attempt reported of its harness's quota, and ends an attempt that turned to extra usage
// when its task does not allow it: the response that turned to it is the only one that ran on it.
func (r *run) onQuota(q domain.Quota) {
	r.a.mu.Lock()
	key := ""
	if at := r.x.CurrentAttempt(); at != nil && at.Harness != "" {
		key = quotaKey(at.Harness, at.Installation)
	}
	r.a.mu.Unlock()
	if key != "" {
		r.a.storeQuota(key, q)
	}
	if q.UsingExtraUsage && !r.def.AllowsExtraUsage() && r.extraUsage.CompareAndSwap(false, true) {
		r.stopAttempt()
	}
}
