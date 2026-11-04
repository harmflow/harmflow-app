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

// quotaReading is a harness's last quota reading and when it was taken.
type quotaReading struct {
	quota domain.Quota
	at    time.Time
}

// quota is the harness's quota as an attempt checks it before it starts: the reading of the last five minutes, or a new
// one, read with no model call. Attempts that start together share one read. read is false when the adapter reads no
// quota; a read that fails is not kept, so the next attempt reads again.
func (a *App) quota(ctx context.Context, ad ports.HarnessAdapter, info ports.HarnessInfo) (q domain.Quota, read bool, err error) {
	reader, ok := ad.(ports.QuotaReader)
	if !ok {
		return domain.Quota{}, false, nil
	}
	a.mu.Lock()
	lock := a.quotaReads[ad.ID()]
	if lock == nil {
		lock = &sync.Mutex{}
		a.quotaReads[ad.ID()] = lock
	}
	a.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	a.mu.Lock()
	last, kept := a.quotas[ad.ID()]
	a.mu.Unlock()
	if kept && a.now().Sub(last.at) < quotaTTL {
		return last.quota, true, nil
	}
	q, err = reader.ReadQuota(ctx, info)
	if err != nil {
		return domain.Quota{}, true, err
	}
	a.storeQuota(ad.ID(), q)
	return q, true, nil
}

// storeQuota keeps a harness's quota reading, read before an attempt or reported by a running one.
func (a *App) storeQuota(harness string, q domain.Quota) {
	a.mu.Lock()
	a.quotas[harness] = quotaReading{quota: q, at: a.now()}
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
	harness := ""
	if at := r.x.CurrentAttempt(); at != nil {
		harness = at.Harness
	}
	r.a.mu.Unlock()
	if harness != "" {
		r.a.storeQuota(harness, q)
	}
	if q.UsingExtraUsage && !r.def.AllowsExtraUsage() && r.extraUsage.CompareAndSwap(false, true) {
		r.stopAttempt()
	}
}
