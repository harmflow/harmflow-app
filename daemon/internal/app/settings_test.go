package app

import "testing"

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
	e.app.d.Service = &fakeAutostart{}
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
