package app

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// tokenLifetime is how long a harness's long-lived token lasts, counted from the day the user saved it: the token does
// not say when it expires.
const tokenLifetime = 365 * 24 * time.Hour

// tokenNotices are the days before a token's expiry on which the daemon tells the user, each once.
var tokenNotices = []int{30, 7}

// tokenText is what a token may hold: the characters of a token, no space, quote, or line break, which the credential
// store's command would read as its own.
var tokenText = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)

// tokenRecord is what the daemon keeps of a harness's long-lived token besides the token, which only the credential
// store holds: when the user saved it, whether the harness rejected it, and the days of the notices of its expiry
// already sent.
type tokenRecord struct {
	SavedAt  time.Time `json:"savedAt"`
	Rejected bool      `json:"rejected"`
	Noticed  []int     `json:"noticed"`
}

// tokenTaker is the harness's adapter when it takes a long-lived token and the daemon has a credential store to keep
// one in, and that store.
func (a *App) tokenTaker(id string) (ports.TokenSignIn, ports.SecretStore, bool) {
	t, ok := a.adapters[id].(ports.TokenSignIn)
	if !ok || a.d.HarnessTokens == nil {
		return nil, nil, false
	}
	return t, a.d.HarnessTokens(id), true
}

// loadHarnessTokens reads what the daemon keeps of the harnesses' tokens, and has each adapter use its saved token
// unless the harness rejected it. A token the credential store cannot give keeps its record, and goes unused until the
// next start: a locked keychain is not a token removed.
func (a *App) loadHarnessTokens() {
	records := map[string]*tokenRecord{}
	if _, err := a.d.State.Load("harness-tokens", &records); err != nil {
		a.log.Printf("loading the harnesses' tokens; none is used until the daemon starts again: %v", err)
		return
	}
	for id, r := range records {
		t, store, ok := a.tokenTaker(id)
		if !ok {
			continue
		}
		a.mu.Lock()
		a.tokens[id] = r
		a.mu.Unlock()
		if r.Rejected {
			continue
		}
		token, err := store.Get()
		if err != nil || token == "" {
			a.log.Printf("reading the %s token from the credential store; it is not used until the daemon starts again: %v",
				a.adapters[id].Name(), err)
			continue
		}
		t.UseToken(token)
	}
}

// tokenSnapshotLocked is a copy of the tokens' records, to save once a.mu is released.
func (a *App) tokenSnapshotLocked() map[string]*tokenRecord {
	out := map[string]*tokenRecord{}
	for id, r := range a.tokens {
		c := *r
		c.Noticed = slices.Clone(r.Noticed)
		out[id] = &c
	}
	return out
}

func (a *App) saveTokens() {
	a.mu.Lock()
	records := a.tokenSnapshotLocked()
	a.mu.Unlock()
	if err := a.d.State.Save("harness-tokens", records); err != nil {
		a.log.Printf("saving the harnesses' tokens' records: %v", err)
	}
}

// withTokenLocked is info with what the inventory shows of the harness's token.
func (a *App) withTokenLocked(id string, info ports.HarnessInfo) ports.HarnessInfo {
	if _, _, ok := a.tokenTaker(id); !ok {
		return info
	}
	info.TakesToken = true
	if r := a.tokens[id]; r != nil {
		info.Token = &ports.TokenInfo{SavedAt: r.SavedAt, ExpiresAt: r.SavedAt.Add(tokenLifetime), Rejected: r.Rejected}
	}
	return info
}

// SetHarnessToken saves a long-lived token for a harness in the credential store, and has the harness's checks and
// sessions run with it from then on; it replaces a token saved before, a rejected one included. The token is never
// written anywhere else, and no answer carries it back.
func (a *App) SetHarnessToken(ctx context.Context, id, token string) (ports.HarnessInfo, error) {
	t, store, err := a.harnessTokenStore(id)
	if err != nil {
		return ports.HarnessInfo{}, err
	}
	token = strings.TrimSpace(token)
	switch {
	case token == "":
		return ports.HarnessInfo{}, &Error{Code: CodeValidationFailed, Text: domain.T("harness_token_empty", "the token is empty"),
			Validation: fieldIssue("/token", domain.T("harness_token_empty", "the token is empty"))}
	case !tokenText.MatchString(token):
		text := domain.T("harness_token_invalid", "the token holds a character no token has, such as a space or a quote")
		return ports.HarnessInfo{}, &Error{Code: CodeValidationFailed, Text: text, Validation: fieldIssue("/token", text)}
	}
	if err := store.Set(token); err != nil {
		a.log.Printf("saving the %s token in the credential store %s: %v", a.adapters[id].Name(), store.Kind(), err)
		return ports.HarnessInfo{}, newError(CodeConflict, domain.T("harness_token_store_failed",
			"the credential store could not keep the token; see the daemon's log"))
	}
	a.mu.Lock()
	a.tokens[id] = &tokenRecord{SavedAt: a.now(), Noticed: []int{}}
	a.mu.Unlock()
	a.saveTokens()
	t.UseToken(token)
	return a.detect(ctx, id), nil
}

// DeleteHarnessToken removes a harness's long-lived token from the credential store; its checks and sessions go back
// to the harness's own sign-in.
func (a *App) DeleteHarnessToken(ctx context.Context, id string) (ports.HarnessInfo, error) {
	t, store, err := a.harnessTokenStore(id)
	if err != nil {
		return ports.HarnessInfo{}, err
	}
	if err := store.Delete(); err != nil {
		a.log.Printf("removing the %s token from the credential store %s: %v", a.adapters[id].Name(), store.Kind(), err)
		return ports.HarnessInfo{}, newError(CodeConflict, domain.T("harness_token_remove_failed",
			"the credential store could not remove the token; see the daemon's log"))
	}
	t.UseToken("")
	a.mu.Lock()
	delete(a.tokens, id)
	a.mu.Unlock()
	a.saveTokens()
	return a.detect(ctx, id), nil
}

// harnessTokenStore is the harness's adapter and the credential store of its token, or why a token cannot be kept
// for it.
func (a *App) harnessTokenStore(id string) (ports.TokenSignIn, ports.SecretStore, error) {
	ad, known := a.adapters[id]
	if !known {
		return nil, nil, newError(CodeNotFound, domain.T("harness_not_found", "no harness {{harness}}", "harness", id))
	}
	if _, ok := ad.(ports.TokenSignIn); !ok {
		return nil, nil, newError(CodeConflict, domain.T("harness_token_unsupported",
			"{{harness}} takes no long-lived token", "harness", ad.Name()))
	}
	t, store, ok := a.tokenTaker(id)
	if !ok {
		return nil, nil, newError(CodeConflict, domain.T("harness_token_store_unavailable",
			"this daemon keeps its secrets in a file, not in the operating system's credential store, so it keeps no token"))
	}
	return t, store, nil
}

// tokenInUse reports whether the harness's checks and sessions run with a saved token.
func (a *App) tokenInUse(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := a.tokens[id]
	return r != nil && !r.Rejected
}

// rejectToken stops using the harness's saved token, which the harness rejected, and says so; it reports whether a
// token was in use. The token stays in the credential store, marked rejected, until the user saves another or removes
// it.
func (a *App) rejectToken(id string) bool {
	t, _, ok := a.tokenTaker(id)
	if !ok {
		return false
	}
	a.mu.Lock()
	r := a.tokens[id]
	if r == nil || r.Rejected {
		a.mu.Unlock()
		return false
	}
	r.Rejected = true
	a.mu.Unlock()
	t.UseToken("")
	a.saveTokens()
	name := a.adapters[id].Name()
	a.emit(domain.Event{Type: domain.EventHarnessTokenRejected, Harness: id, Severity: domain.SeverityError}.Says(
		domain.T("harness_token_rejected",
			"{{harness}} rejected the saved token, which may have expired or been revoked; HarmFlow no longer uses it, and {{harness}} goes back to its own sign-in",
			"harness", name)))
	return true
}

// noticeTokenExpiry tells the user, once for each of tokenNotices, that a harness's token expires soon, by the count of
// its year. When more than one notice is due at once, as after the daemon was down, only the nearest is sent.
func (a *App) noticeTokenExpiry() {
	now := a.now()
	type due struct {
		id   string
		days int
		at   time.Time
	}
	var notices []due
	a.mu.Lock()
	for _, id := range sortedKeys(a.tokens) {
		r := a.tokens[id]
		if r.Rejected {
			continue
		}
		expires := r.SavedAt.Add(tokenLifetime)
		nearest := 0
		for _, days := range tokenNotices {
			if !now.Before(expires.Add(-time.Duration(days)*24*time.Hour)) && !slices.Contains(r.Noticed, days) {
				r.Noticed = append(r.Noticed, days)
				if nearest == 0 || days < nearest {
					nearest = days
				}
			}
		}
		if nearest != 0 {
			notices = append(notices, due{id: id, days: nearest, at: expires})
		}
	}
	a.mu.Unlock()
	if len(notices) == 0 {
		return
	}
	a.saveTokens()
	for _, n := range notices {
		left := int(n.at.Sub(now).Hours() / 24)
		if left < 0 {
			left = 0
		}
		a.emit(domain.Event{Type: domain.EventHarnessTokenExpiring, Harness: n.id, Severity: domain.SeverityWarning,
			Payload: map[string]any{"expiresAt": n.at}}.Says(domain.T("harness_token_expiring",
			"the saved {{harness}} token expires around {{date}}, in {{days}} days; make a new one and save it in Settings",
			"harness", a.adapters[n.id].Name(), "date", n.at.Format("2006-01-02"), "days", strconv.Itoa(left))))
	}
}
