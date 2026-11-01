package claudecode

import (
	"context"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

var _ ports.SignInDriver = (*Adapter)(nil)

// The size of the sign-in session's terminal: wide enough that the CLI's menus keep each option on one line.
const (
	signInRows = 50
	signInCols = 200
)

// defaultSignInStep bounds the wait for each screen of the sign-in but the browser's: a screen the session does not
// show by then is one the adapter does not recognize.
const defaultSignInStep = 30 * time.Second

// signInGrace is what the session has to end on SIGTERM before SIGKILL.
const signInGrace = 2 * time.Second

// The screens of the sign-in, as Claude Code 2.1.309 draws them, matched on their text without its spaces, since the
// CLI moves the cursor between words as often as it prints a space:
//   - the question whether to trust the session's folder, asked once per folder, whose options are "No, exit",
//     selected, then "Yes, I trust this folder";
//   - the interactive session, whose header names "Claude Code v<version>", which its first-run welcome reads as
//     "Welcome to Claude Code v<version>";
//   - /login's "Select login method:" menu, its first option "1. Claude account with subscription" selected;
//   - "Opening browser to sign in…", with "Browser didn't open? Use the url below to sign in" and the URL;
//   - "Login successful. Press Enter to continue…" once the user approved the sign-in in the browser.
var (
	trustScreen   = "Yes,Itrustthisfolder"
	sessionHeader = regexp.MustCompile(`ClaudeCodev\d`)
	welcomeWords  = "Welcometo"
	loginMenu     = "Selectloginmethod:"
	firstMethod   = "❯1.Claudeaccountwithsubscription"
	browserScreen = []string{"Openingbrowsertosignin", "Browserdidn'topen"}
	signedInText  = "Loginsuccessful"
)

// The keys the sign-in types.
const (
	keyDown  = "\x1b[B"
	keyEnter = "\r"
)

// SignIn implements ports.SignInDriver: it starts the detected claude on a terminal of its own in dir, with the
// user's environment, trusts dir when the CLI asks, runs /login, takes its first method, the Claude account, and waits
// for the user to approve the sign-in in the browser the CLI opens. Once the CLI says the sign-in succeeded, it ends
// the session without typing anything more: a key typed then could answer a question the CLI asks next, such as
// whether to trust the settings an organization manages, which is the user's to answer. It types nothing on a screen
// it does not recognize either, and returns it, its secrets masked.
func (a *Adapter) SignIn(ctx context.Context, info ports.HarnessInfo, dir string) ports.SignInResult {
	if info.Path == "" {
		return ports.SignInResult{Outcome: ports.SignInEnded}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ports.SignInResult{Outcome: ports.SignInEnded, Err: err}
	}
	env := append(slices.Clone(a.Env), "TERM=xterm-256color") // the last of a duplicate key wins
	term, err := procutil.StartTerminal(ports.Invocation{Path: info.Path, Dir: dir, Env: env}, signInRows, signInCols)
	if err != nil {
		return ports.SignInResult{Outcome: ports.SignInEnded, Err: err}
	}
	defer term.Close(signInGrace)
	step := a.signInStep
	if step == 0 {
		step = defaultSignInStep
	}
	s := &signInSession{term: term}

	text, res, ok := s.await(ctx, step, func(t string) bool { return strings.Contains(t, trustScreen) || interactive(t) })
	if !ok {
		return res
	}
	if !interactive(text) {
		if res, ok := s.typeKeys(ctx, keyDown, keyEnter); !ok {
			return res
		}
		if _, res, ok := s.await(ctx, step, interactive); !ok {
			return res
		}
	}
	if res, ok := s.typeKeys(ctx, "/login", keyEnter); !ok {
		return res
	}
	if _, res, ok := s.await(ctx, step, func(t string) bool {
		return strings.Contains(t, loginMenu) && strings.Contains(t, firstMethod)
	}); !ok {
		return res
	}
	if res, ok := s.typeKeys(ctx, keyEnter); !ok {
		return res
	}
	if _, res, ok := s.await(ctx, step, func(t string) bool {
		return strings.Contains(t, browserScreen[0]) || strings.Contains(t, browserScreen[1])
	}); !ok {
		return res
	}
	// The user approves in the browser: the wait has no bound of its own, only ctx's.
	if _, res, ok := s.await(ctx, 0, func(t string) bool { return strings.Contains(t, signedInText) }); !ok {
		return res
	}
	return ports.SignInResult{Outcome: ports.SignInSucceeded}
}

// interactive reports whether the text shows the interactive session's header, not its first-run welcome.
func interactive(t string) bool {
	for _, m := range sessionHeader.FindAllStringIndex(t, -1) {
		if !strings.HasSuffix(t[:m[0]], welcomeWords) {
			return true
		}
	}
	return false
}

// signInSession reads the screens of one sign-in session. screen is what the session printed since the current step
// began.
type signInSession struct {
	term   *procutil.Terminal
	screen []byte
}

// maxScreen bounds what a step keeps of the session's output; the CLI's spinner keeps printing while the browser waits.
const maxScreen = 256 * 1024

// await reads the session until done reports the screen it waits for, from what the session printed since the last
// step, and returns that screen's text without its spaces. A bound of zero waits as long as ctx. It reports false, with
// the result to return, when the bound passes first, which is a screen the adapter does not recognize, or when the
// session ends, or ctx does.
func (s *signInSession) await(ctx context.Context, bound time.Duration, done func(string) bool) (string, ports.SignInResult, bool) {
	s.screen = s.screen[:0]
	var limit <-chan time.Time
	if bound > 0 {
		timer := time.NewTimer(bound)
		defer timer.Stop()
		limit = timer.C
	}
	for {
		s.screen = append(s.screen, s.term.Take()...)
		if over := len(s.screen) - maxScreen; over > 0 {
			s.screen = append(s.screen[:0], s.screen[over:]...)
		}
		text := squeeze(screenText(s.screen))
		if done(text) {
			return text, ports.SignInResult{}, true
		}
		select {
		case <-ctx.Done():
			return "", ports.SignInResult{Outcome: ports.SignInStopped}, false
		case <-limit:
			return "", ports.SignInResult{Outcome: ports.SignInUnknownScreen, Screen: maskScreen(screenText(s.screen))}, false
		case <-s.term.Exited():
			if more := s.term.Take(); len(more) > 0 {
				s.screen = append(s.screen, more...)
				continue
			}
			return "", ports.SignInResult{Outcome: ports.SignInEnded}, false
		case <-s.term.Changed():
		}
	}
}

// settleFor is how long the session must print nothing before a key is typed, so its input takes keys again, and
// settleMax bounds that wait for a session that keeps drawing.
const (
	settleFor = 700 * time.Millisecond
	settleMax = 5 * time.Second
)

// typeKeys types each key once the session has stopped drawing, as a person waits for a screen before typing. What the
// session prints meanwhile stays for the next step to read.
func (s *signInSession) typeKeys(ctx context.Context, keys ...string) (ports.SignInResult, bool) {
	for _, k := range keys {
		if res, ok := s.settle(ctx); !ok {
			return res, false
		}
		if err := s.term.Type(k); err != nil {
			return ports.SignInResult{Outcome: ports.SignInEnded, Err: err}, false
		}
	}
	return ports.SignInResult{}, true
}

func (s *signInSession) settle(ctx context.Context) (ports.SignInResult, bool) {
	most := time.NewTimer(settleMax)
	defer most.Stop()
	quiet := time.NewTimer(settleFor)
	defer quiet.Stop()
	for {
		select {
		case <-ctx.Done():
			return ports.SignInResult{Outcome: ports.SignInStopped}, false
		case <-s.term.Exited():
			return ports.SignInResult{Outcome: ports.SignInEnded}, false
		case <-most.C:
			return ports.SignInResult{}, true
		case <-quiet.C:
			return ports.SignInResult{}, true
		case <-s.term.Changed():
			// The output stays in the terminal for the next await; only the quiet period starts again.
			quiet.Reset(settleFor)
		}
	}
}

// Control sequences of a terminal's output: an operating system command, a control sequence, or another escape. A
// cursor move to a column (G) or forward (C) stands where the CLI leaves a space.
var (
	oscSequence   = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)?`)
	csiSequence   = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*([@-~])`)
	otherSequence = regexp.MustCompile(`\x1b[()][0-9A-Za-z]|\x1b[ -/]*[0-~]`)
)

// screenText is the text a terminal's output shows, its control sequences taken out, a cursor move across a line as a
// space, and a carriage return as a line break.
func screenText(raw []byte) string {
	s := oscSequence.ReplaceAllString(string(raw), "")
	s = csiSequence.ReplaceAllStringFunc(s, func(m string) string {
		if f := m[len(m)-1]; f == 'G' || f == 'C' {
			return " "
		}
		return ""
	})
	s = otherSequence.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// squeeze takes every space out of a screen's text, a no-break space included.
func squeeze(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// What a screen masks before it is logged: a URL and the lines the terminal wrapped it onto, which hold no space; an
// e-mail address; and any long run of a token's characters, wherever the terminal cut it.
var (
	wrappedURL = regexp.MustCompile(`https?://\S*(\n\S+)*`)
	emailText  = regexp.MustCompile(`[\w.+-]+@[\w-]+(\.[\w-]+)+`)
	tokenRun   = regexp.MustCompile(`[A-Za-z0-9%+=&_~./-]{24,}`)
	blankLines = regexp.MustCompile(`\n\s*\n+`)
)

// maxLoggedScreen is the end of a screen that is kept for the log.
const maxLoggedScreen = 2000

// maskScreen is a screen's text as the log keeps it: its end, without blank lines, its URLs, e-mail addresses, and
// token-like runs masked, and then what Redact masks.
func maskScreen(s string) string {
	s = wrappedURL.ReplaceAllString(s, "[URL]")
	s = emailText.ReplaceAllString(s, "[EMAIL]")
	s = tokenRun.ReplaceAllString(s, "[REDACTED]")
	s = strings.TrimSpace(blankLines.ReplaceAllString(s, "\n"))
	if len(s) > maxLoggedScreen {
		s = s[len(s)-maxLoggedScreen:]
		for len(s) > 0 && !utf8Start(s[0]) {
			s = s[1:]
		}
	}
	return eventlog.Redact(s)
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
