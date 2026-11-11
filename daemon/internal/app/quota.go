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

// quotaReading is a harness's last quota reading and when it was taken.
type quotaReading struct {
	quota domain.Quota
	at    time.Time
}

// quotaFailure is a run of failed reads of an installation's quota: the last one's error, how many failed in a row, and
// when the last one was taken. It is kept apart from the reading, so a running attempt's report, which comes from the
// API stream and not from the quota endpoint, neither ends it nor resets its count; only a good read does.
type quotaFailure struct {
	err      error
	failures int
	at       time.Time
}

// stands is how long the last failed read stands from when it was taken: the harness's check interval, doubled on each
// failed read in a row after the first, up to an hour, or the interval when it is longer. A harness whose quota
// endpoint is throttled is read a few times an hour.
func (f quotaFailure) stands(interval time.Duration) time.Duration {
	limit := max(interval, domain.MaxQuotaCheckMinutes*time.Minute)
	d := interval
	for i := 1; i < f.failures && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// quota is the quota of the installation of a harness that info describes, as an attempt checks it before it starts:
// the reading that still stands, or a new one, read with no model call. Attempts that start together share one read.
// read is false when the adapter reads no quota. A read that fails is kept too: while it stands, every attempt starts
// with its error and reads nothing, even beside a reading a running attempt reported since. Each installation runs
// under its own account, so each keeps a reading of its own.
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
	interval := a.quotaInterval(ad.ID())
	switch {
	case failing && a.now().Sub(failed.at) < failed.stands(interval):
		return domain.Quota{}, true, failed.err
	case !failing && kept && a.now().Sub(last.at) < interval:
		return last.quota, true, nil
	}
	q, err = reader.ReadQuota(ctx, info)
	if err != nil {
		f := quotaFailure{err: err, failures: 1, at: a.now()}
		if failing {
			f.failures = failed.failures + 1
		}
		a.mu.Lock()
		a.quotaFailures[key] = f
		a.mu.Unlock()
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
