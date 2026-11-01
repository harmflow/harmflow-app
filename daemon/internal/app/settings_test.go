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
