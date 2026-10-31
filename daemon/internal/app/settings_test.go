package app

import (
	"fmt"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// fakeAutostart stands in for the launchd agent: it keeps whether autostart is on, and never reaches launchctl or the
// user's LaunchAgents folder. It records each Enable, with what it was asked to start, and counts each Disable.
type fakeAutostart struct {
	enabled  bool
	enables  []autostartCall
	disables int
}

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

// A use-case error keeps its text through any wrapping, as the firing history records it.
func TestErrorTextFindsAWrappedError(t *testing.T) {
	want := domain.T("task_not_found", "no task {{task}}", "task", "nightly")
	err := fmt.Errorf("firing: %w", newError(CodeNotFound, want))
	if got := errorText(err); got.Key != want.Key || got.Message != want.Message {
		t.Errorf("errorText(%v) = %+v, want %+v", err, got, want)
	}
}
