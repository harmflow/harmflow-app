package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// fakeAutostart stands in for the launchd agent: it keeps whether autostart is on, and never reaches launchctl or the
// user's LaunchAgents folder. It records each Enable, with what it was asked to start, and counts each Disable.
type fakeAutostart struct {
	enabled  bool
	enables  []autostartCall
	disables int
}

var _ ports.HostServiceManager = (*fakeAutostart)(nil)

type autostartCall struct {
	executable string
	args       []string
	env        map[string]string
}

func (f *fakeAutostart) Enabled() bool { return f.enabled }
func (f *fakeAutostart) Enable(executable string, args []string, env map[string]string) error {
	f.enabled = true
	f.enables = append(f.enables, autostartCall{executable, args, env})
	return nil
}
func (f *fakeAutostart) Disable() error {
	f.enabled = false
	f.disables++
	return nil
}
func (f *fakeAutostart) Mechanism() string { return "launchd" }
func (f *fakeAutostart) Location() string  { return "/LaunchAgents/com.harmflow.daemon.plist" }

// harmflow-app#81: a settings patch that turns autostart on or off answers with the new state, which GET /settings
// then reads too.
func TestAutostartPatchAnswersTheNewState(t *testing.T) {
	e := newEnv(t)
	e.app.d.Service, e.app.d.DaemonExecutable = &fakeAutostart{}, "/Applications/HarmFlow.app/Contents/MacOS/harmflowd"
	for _, on := range []bool{true, false} {
		v, err := e.app.UpdateSettings(SettingsPatch{Autostart: &on})
		if err != nil {
			t.Fatalf("autostart %v: %v", on, err)
		}
		if v.Autostart.Enabled != on || v.Autostart.Mechanism != "launchd" {
			t.Errorf("autostart %v: answered %+v", on, v.Autostart)
		}
		if got := e.app.GetSettings().Autostart.Enabled; got != on {
			t.Errorf("autostart %v: GET /settings reads %v", on, got)
		}
	}
}

// A daemon that could not find its own path refuses to enable autostart, rather than write an agent with no program.
func TestAutostartNeedsTheDaemonsExecutable(t *testing.T) {
	e := newEnv(t)
	service := &fakeAutostart{}
	e.app.d.Service, e.app.d.DaemonExecutable = service, ""
	on := true
	if _, err := e.app.UpdateSettings(SettingsPatch{Autostart: &on}); err == nil {
		t.Error("UpdateSettings(autostart: true) = nil error with no executable, want an error")
	}
	if len(service.enables) != 0 {
		t.Errorf("Enable calls = %+v, want none", service.enables)
	}
}

// harmflow-app#234: keeping HarmFlow in the menu bar on ⌘Q is off on a fresh data folder, a patch turns it on and off,
// a patch of another setting leaves it as it is, and it outlives a restart of the daemon.
func TestKeepTrayOnQuitIsOffByDefaultAndKeepsAPatch(t *testing.T) {
	e := newEnv(t)
	if got := e.app.GetSettings().KeepTrayOnQuit; got {
		t.Fatalf("GetSettings().KeepTrayOnQuit = %v on a fresh data folder, want false", got)
	}
	on := true
	v, err := e.app.UpdateSettings(SettingsPatch{KeepTrayOnQuit: &on})
	if err != nil {
		t.Fatalf("UpdateSettings(keepTrayOnQuit: true): %v", err)
	}
	if !v.KeepTrayOnQuit || !v.TrayEnabled {
		t.Errorf("UpdateSettings(keepTrayOnQuit: true) = keepTrayOnQuit %v, trayEnabled %v, want true, true", v.KeepTrayOnQuit, v.TrayEnabled)
	}
	lang := "pt-BR"
	if v, err := e.app.UpdateSettings(SettingsPatch{Language: &lang}); err != nil || !v.KeepTrayOnQuit {
		t.Errorf("UpdateSettings(language) = keepTrayOnQuit %v, %v, want true, nil", v.KeepTrayOnQuit, err)
	}

	e = e.restart(t)
	if got := e.app.GetSettings().KeepTrayOnQuit; !got {
		t.Errorf("GetSettings().KeepTrayOnQuit = %v after a restart, want true", got)
	}
	off := false
	if v, err := e.app.UpdateSettings(SettingsPatch{KeepTrayOnQuit: &off}); err != nil || v.KeepTrayOnQuit {
		t.Errorf("UpdateSettings(keepTrayOnQuit: false) = keepTrayOnQuit %v, %v, want false, nil", v.KeepTrayOnQuit, err)
	}
}

// harmflow-app#525: the language setting takes the three official languages and the choice to follow the system's,
// and refuses any other language with settings_language_invalid, leaving the saved language as it was.
func TestLanguageSettingTakesOnlyTheOfficialLanguages(t *testing.T) {
	for _, tc := range []struct {
		language string
		accepted bool
	}{
		{"pt-BR", true},
		{"en-US", true},
		{"es-ES", true},
		{"system", true},
		{"fr-FR", false},
		{"pt-PT", false},
		{"pt-br", false},
		{"en", false},
	} {
		t.Run(tc.language, func(t *testing.T) {
			e := newEnv(t)
			// The language saved before the patch differs from the one the patch asks for, so a patch that applied, or
			// one that changed nothing, shows.
			before := "es-ES"
			if tc.language == before {
				before = "pt-BR"
			}
			if _, err := e.app.UpdateSettings(SettingsPatch{Language: &before}); err != nil {
				t.Fatalf("UpdateSettings(language %q): %v", before, err)
			}
			want := before
			v, err := e.app.UpdateSettings(SettingsPatch{Language: &tc.language})
			if tc.accepted {
				want = tc.language
				if err != nil || v.Language != want {
					t.Errorf("UpdateSettings(language %q) = %q, %v, want %q, nil", tc.language, v.Language, err, want)
				}
			} else if code, key := appErrorKey(err); code != CodeValidationFailed || key != "settings_language_invalid" {
				t.Errorf("UpdateSettings(language %q) = %s %s %v, want %s settings_language_invalid", tc.language, code,
					key, err, CodeValidationFailed)
			}
			if got := e.app.GetSettings().Language; got != want {
				t.Errorf("GetSettings().Language = %q after language %q, want %q", got, tc.language, want)
			}
			e = e.restart(t)
			if got := e.app.GetSettings().Language; got != want {
				t.Errorf("GetSettings().Language = %q after language %q and a restart, want %q", got, tc.language, want)
			}
		})
	}
}

// harmflow-app#464: the tray is on in the settings of a fresh data folder, and a patch turns it off and back on,
// each of which a restart of the daemon keeps: the shell reads the stored choice at start to decide what closing the
// window does.
func TestTrayIsOnByDefaultAndARestartKeepsAPatch(t *testing.T) {
	e := newEnv(t)
	if got := e.app.GetSettings().TrayEnabled; !got {
		t.Fatalf("GetSettings().TrayEnabled = %v on a fresh data folder, want true", got)
	}
	for _, on := range []bool{false, true} {
		v, err := e.app.UpdateSettings(SettingsPatch{TrayEnabled: &on})
		if err != nil {
			t.Fatalf("UpdateSettings(trayEnabled: %v): %v", on, err)
		}
		if v.TrayEnabled != on {
			t.Errorf("UpdateSettings(trayEnabled: %v) = trayEnabled %v", on, v.TrayEnabled)
		}
		e = e.restart(t)
		if got := e.app.GetSettings().TrayEnabled; got != on {
			t.Errorf("GetSettings().TrayEnabled = %v after a restart, want %v", got, on)
		}
	}
}

// A use-case error keeps its text through any wrapping, as the firing history records it.
func TestErrorTextFindsAWrappedError(t *testing.T) {
	want := domain.T("task_not_found", "no task {{task}}", "task", "nightly")
	err := fmt.Errorf("firing: %w", newError(CodeNotFound, want))
	if got := errorText(err); got.Key != want.Key || got.Message != want.Message {
		t.Errorf("errorText(%v) = %+v, want %+v", err, got, want)
	}
}

// A share of a harness's quota is kept and returned with the settings; a share that is not above 0 and at most 100 is
// refused, naming it by its harness, and so is a share of a harness HarmFlow does not run. A refused change keeps the
// shares as they were.
func TestQuotaShareSettings(t *testing.T) {
	e := newEnv(t)
	v, err := e.app.UpdateSettings(SettingsPatch{Quota: map[string]QuotaShare{domain.HarnessClaudeCode: {MaxPercent: 95}}})
	if err != nil {
		t.Fatalf("UpdateSettings(claude-code at 95) = %v, want no error", err)
	}
	if got := v.Quota[domain.HarnessClaudeCode].MaxPercent; got != 95 || len(v.Quota) != 1 {
		t.Errorf("UpdateSettings(claude-code at 95).Quota = %v, want claude-code at 95 alone", v.Quota)
	}
	if got := e.app.quotaShare(domain.HarnessCodex); got != domain.FullShare {
		t.Errorf("quotaShare(codex) = %v, want %v for a harness left out", got, domain.FullShare)
	}
	cases := []struct {
		name    string
		harness string
		share   float64
		key     string
		path    string
	}{
		{"zero", domain.HarnessClaudeCode, 0, "settings_quota_share_invalid", "/quota/claude-code/maxPercent"},
		{"negative", domain.HarnessCodex, -5, "settings_quota_share_invalid", "/quota/codex/maxPercent"},
		{"above 100", domain.HarnessClaudeCode, 101, "settings_quota_share_invalid", "/quota/claude-code/maxPercent"},
		{"an unknown harness", "ollama", 50, "settings_quota_harness_unknown", "/quota/ollama"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.app.UpdateSettings(SettingsPatch{Quota: map[string]QuotaShare{c.harness: {MaxPercent: c.share}}})
			var ae *Error
			if !errors.As(err, &ae) || ae.Code != CodeValidationFailed || ae.Key != c.key || ae.Validation == nil ||
				len(ae.Validation.Errors) != 1 || ae.Validation.Errors[0].Path != c.path {
				t.Fatalf("UpdateSettings(%s at %v) = %v, want %s at %s", c.harness, c.share, err, c.key, c.path)
			}
			if !strings.Contains(ae.Message, "quota."+c.harness) {
				t.Errorf("UpdateSettings(%s at %v) says %q, want it to name quota.%s", c.harness, c.share, ae.Message,
					c.harness)
			}
			if got := e.app.quotaShare(domain.HarnessClaudeCode); got != 95 {
				t.Errorf("after a refused change, quotaShare(claude-code) = %v, want 95", got)
			}
		})
	}
}

// A share outside the bounds in the settings file, edited by hand, is refused when the settings load: its harness may
// spend the whole of each window, and the other shares load.
func TestQuotaShareRefusedOnLoad(t *testing.T) {
	e := newEnv(t)
	s := defaultSettings()
	s.Quota = map[string]QuotaShare{domain.HarnessClaudeCode: {MaxPercent: 150}, domain.HarnessCodex: {MaxPercent: 80}}
	if err := e.app.d.State.Save("settings", s); err != nil {
		t.Fatalf("Save(settings) = %v", err)
	}
	if err := e.app.loadSettings(); err != nil {
		t.Fatalf("loadSettings() = %v, want no error", err)
	}
	if got := e.app.quotaShare(domain.HarnessClaudeCode); got != domain.FullShare {
		t.Errorf("quotaShare(claude-code) = %v, want %v for a share refused on load", got, domain.FullShare)
	}
	if got := e.app.quotaShare(domain.HarnessCodex); got != 80 {
		t.Errorf("quotaShare(codex) = %v, want 80", got)
	}
}

// A harness's quota check interval is kept with its share and returned with the settings, and a harness that sets none
// reads its quota every five minutes. An interval that is not a whole number from 1 to 60 minutes, 0 included, is
// refused with its own message, naming it by its harness, and a refused change keeps the intervals as they were. The
// patch is decoded as the local API decodes it, so a fraction is refused as an interval, not as a body that is no JSON.
func TestQuotaIntervalSettings(t *testing.T) {
	e := newEnv(t)
	v, err := e.app.UpdateSettings(SettingsPatch{Quota: map[string]QuotaShare{
		domain.HarnessClaudeCode: {MaxPercent: 100, CheckIntervalMinutes: new(20.0)}}})
	if err != nil {
		t.Fatalf("UpdateSettings(claude-code every 20 minutes) = %v, want no error", err)
	}
	if got := v.Quota[domain.HarnessClaudeCode]; got.CheckIntervalMinutes == nil || *got.CheckIntervalMinutes != 20 ||
		got.MaxPercent != 100 {
		t.Errorf("UpdateSettings(claude-code every 20 minutes).Quota = %v, want 20 minutes at 100%%", v.Quota)
	}
	if got := e.app.quotaInterval(domain.HarnessClaudeCode); got != 20*time.Minute {
		t.Errorf("quotaInterval(claude-code) = %v, want 20m", got)
	}
	if got := e.app.quotaInterval(domain.HarnessCodex); got != 5*time.Minute {
		t.Errorf("quotaInterval(codex) = %v, want 5m for a harness left out", got)
	}
	for _, minutes := range []string{"-1", "0", "2.5", "61"} {
		t.Run(minutes, func(t *testing.T) {
			var p SettingsPatch
			body := `{"quota":{"codex":{"maxPercent":100,"checkIntervalMinutes":` + minutes + `}}}`
			if err := json.Unmarshal([]byte(body), &p); err != nil {
				t.Fatalf("json.Unmarshal(%s) = %v, want no error", body, err)
			}
			_, err := e.app.UpdateSettings(p)
			var ae *Error
			if !errors.As(err, &ae) || ae.Code != CodeValidationFailed || ae.Key != "settings_quota_interval_invalid" ||
				ae.Validation == nil || len(ae.Validation.Errors) != 1 ||
				ae.Validation.Errors[0].Path != "/quota/codex/checkIntervalMinutes" {
				t.Fatalf("UpdateSettings(codex every %s minutes) = %v, want settings_quota_interval_invalid at "+
					"/quota/codex/checkIntervalMinutes", minutes, err)
			}
			if got := e.app.quotaInterval(domain.HarnessClaudeCode); got != 20*time.Minute {
				t.Errorf("after a refused change, quotaInterval(claude-code) = %v, want 20m", got)
			}
		})
	}
}

// An interval outside the bounds in the settings file, edited by hand, a fraction included, is refused alone when the
// settings load: its harness keeps its share and reads its quota every five minutes.
func TestQuotaIntervalRefusedOnLoad(t *testing.T) {
	for _, minutes := range []float64{600, 2.5} {
		t.Run(fmt.Sprint(minutes), func(t *testing.T) {
			e := newEnv(t)
			s := defaultSettings()
			s.Quota = map[string]QuotaShare{domain.HarnessClaudeCode: {MaxPercent: 90, CheckIntervalMinutes: &minutes}}
			if err := e.app.d.State.Save("settings", s); err != nil {
				t.Fatalf("Save(settings) = %v", err)
			}
			if err := e.app.loadSettings(); err != nil {
				t.Fatalf("loadSettings() = %v, want no error", err)
			}
			if got := e.app.quotaInterval(domain.HarnessClaudeCode); got != 5*time.Minute {
				t.Errorf("quotaInterval(claude-code) = %v, want 5m for an interval refused on load", got)
			}
			if got := e.app.quotaShare(domain.HarnessClaudeCode); got != 90 {
				t.Errorf("quotaShare(claude-code) = %v, want 90 kept beside a refused interval", got)
			}
		})
	}
}

// logBuffer keeps what an App logs, for a test to read while the App's goroutines may still write to it.
type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// lines are the lines logged so far that hold s.
func (b *logBuffer) lines(s string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var got []string
	for _, l := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(l, s) {
			got = append(got, l)
		}
	}
	return got
}

// The share refused when the settings load is written to the daemon's log, named by its harness, and the other
// harness's share, which loads, is not: a message that names no harness, or always the same one, fails a case.
func TestQuotaShareRefusedOnLoadIsLogged(t *testing.T) {
	cases := []struct {
		name    string
		refused string
		share   float64
		kept    string
		keep    float64
	}{
		{"claude-code at 0", domain.HarnessClaudeCode, 0, domain.HarnessCodex, 80},
		{"claude-code at 101", domain.HarnessClaudeCode, 101, domain.HarnessCodex, 80},
		{"codex at 101", domain.HarnessCodex, 101, domain.HarnessClaudeCode, 95},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logged := &logBuffer{}
			e := newEnv(t, func(e *env) { e.logger = log.New(logged, "", 0) })
			s := defaultSettings()
			s.Quota = map[string]QuotaShare{c.refused: {MaxPercent: c.share}, c.kept: {MaxPercent: c.keep}}
			if err := e.app.d.State.Save("settings", s); err != nil {
				t.Fatalf("Save(settings) = %v", err)
			}
			if err := e.app.loadSettings(); err != nil {
				t.Fatalf("loadSettings() = %v, want no error", err)
			}
			refusals := logged.lines("the share is refused")
			if len(refusals) != 1 || !strings.Contains(refusals[0], "quota."+c.refused+".maxPercent") {
				t.Errorf("loadSettings() logged the refusals %q, want one naming quota.%s.maxPercent", refusals, c.refused)
			}
			if got := logged.lines("quota." + c.kept); len(got) != 0 {
				t.Errorf("loadSettings() logged %q, want no refusal of quota.%s", got, c.kept)
			}
			if got := e.app.quotaShare(c.refused); got != domain.FullShare {
				t.Errorf("quotaShare(%s) = %v, want %v for a share refused on load", c.refused, got, domain.FullShare)
			}
			if got := e.app.quotaShare(c.kept); got != c.keep {
				t.Errorf("quotaShare(%s) = %v, want %v", c.kept, got, c.keep)
			}
		})
	}
}

// harmflow-app#849: the general system prompt is empty on a fresh data folder and on settings saved before it existed;
// a patch sets it, a restart keeps it, a patch of another setting leaves it as it is, and an empty text clears it.
func TestSystemPromptIsEmptyByDefaultAndKeepsAPatch(t *testing.T) {
	e := newEnv(t)
	if got := e.app.GetSettings().SystemPrompt; got != "" {
		t.Fatalf("GetSettings().SystemPrompt = %q on a fresh data folder, want empty", got)
	}

	// Settings saved by a version without the setting.
	if err := e.app.d.State.Save("settings", map[string]any{"schemaVersion": "1", "language": "en-US", "trayEnabled": true}); err != nil {
		t.Fatalf("Save(settings): %v", err)
	}
	e = e.restart(t)
	if v := e.app.GetSettings(); v.SystemPrompt != "" || v.Language != "en-US" {
		t.Fatalf("GetSettings() = systemPrompt %q, language %q on settings saved without it, want empty, en-US",
			v.SystemPrompt, v.Language)
	}

	text := "Answer in pt-BR.\nNever touch the CI credentials."
	v, err := e.app.UpdateSettings(SettingsPatch{SystemPrompt: &text})
	if err != nil || v.SystemPrompt != text {
		t.Fatalf("UpdateSettings(systemPrompt) = %q, %v, want %q, nil", v.SystemPrompt, err, text)
	}
	lang := "pt-BR"
	if v, err := e.app.UpdateSettings(SettingsPatch{Language: &lang}); err != nil || v.SystemPrompt != text {
		t.Errorf("UpdateSettings(language) = systemPrompt %q, %v, want %q, nil", v.SystemPrompt, err, text)
	}
	e = e.restart(t)
	if got := e.app.GetSettings().SystemPrompt; got != text {
		t.Errorf("GetSettings().SystemPrompt = %q after a restart, want %q", got, text)
	}

	empty := ""
	if v, err := e.app.UpdateSettings(SettingsPatch{SystemPrompt: &empty}); err != nil || v.SystemPrompt != "" {
		t.Errorf("UpdateSettings(systemPrompt: \"\") = %q, %v, want empty, nil", v.SystemPrompt, err)
	}
	e = e.restart(t)
	if got := e.app.GetSettings().SystemPrompt; got != "" {
		t.Errorf("GetSettings().SystemPrompt = %q after clearing it and a restart, want empty", got)
	}
}

// harmflow-app#849: the general system prompt holds at most 20,000 characters, counted as characters, not bytes; a
// longer text is refused with settings_system_prompt_too_long on the field, and the saved text stays.
func TestSystemPromptLengthLimit(t *testing.T) {
	e := newEnv(t)
	// Each "é" is two bytes, so a limit counted in bytes would refuse this text.
	most := strings.Repeat("é", 20000)
	if v, err := e.app.UpdateSettings(SettingsPatch{SystemPrompt: &most}); err != nil || v.SystemPrompt != most {
		t.Fatalf("UpdateSettings(systemPrompt of 20000 characters) = %d characters, %v, want 20000, nil",
			len([]rune(v.SystemPrompt)), err)
	}
	for _, tc := range []struct {
		name string
		text string
	}{
		{"one character more", strings.Repeat("é", 20001)},
		{"one ASCII character more", strings.Repeat("a", 20001)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.app.UpdateSettings(SettingsPatch{SystemPrompt: &tc.text})
			var ae *Error
			if !errors.As(err, &ae) || ae.Code != CodeValidationFailed || ae.Key != "settings_system_prompt_too_long" ||
				ae.Validation == nil || len(ae.Validation.Errors) != 1 || ae.Validation.Errors[0].Path != "/systemPrompt" {
				t.Fatalf("UpdateSettings(systemPrompt of %d characters) = %v, want settings_system_prompt_too_long at /systemPrompt",
					len([]rune(tc.text)), err)
			}
			if got := e.app.GetSettings().SystemPrompt; got != most {
				t.Errorf("after a refused change, GetSettings().SystemPrompt holds %d characters, want the 20000 saved",
					len([]rune(got)))
			}
		})
	}
}

// The restriction warnings of an execution's detail show on a fresh data folder and on one saved before the setting
// existed; a patch turns them off, and that outlives a restart of the daemon.
func TestShowRestrictionWarningsIsOnByDefaultAndKeepsAPatch(t *testing.T) {
	e := newEnv(t)
	if got := e.app.GetSettings().ShowRestrictionWarnings; !got {
		t.Fatalf("GetSettings().ShowRestrictionWarnings = %v on a fresh data folder, want true", got)
	}

	// Settings saved by a version without the option.
	if err := e.app.d.State.Save("settings", map[string]any{"schemaVersion": "1", "language": "en-US", "trayEnabled": true}); err != nil {
		t.Fatalf("Save(settings): %v", err)
	}
	e = e.restart(t)
	if got := e.app.GetSettings().ShowRestrictionWarnings; !got {
		t.Fatalf("GetSettings().ShowRestrictionWarnings = %v on settings saved without it, want true", got)
	}

	off := false
	if v, err := e.app.UpdateSettings(SettingsPatch{ShowRestrictionWarnings: &off}); err != nil || v.ShowRestrictionWarnings {
		t.Fatalf("UpdateSettings(showRestrictionWarnings: false) = %v, %v, want false, nil", v.ShowRestrictionWarnings, err)
	}
	e = e.restart(t)
	if got := e.app.GetSettings().ShowRestrictionWarnings; got {
		t.Errorf("GetSettings().ShowRestrictionWarnings = %v after a restart, want false", got)
	}
}
