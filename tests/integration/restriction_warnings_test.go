package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The interface's en-US texts of the warning: the label of a restriction not enforced, as an execution's banner, its
// Events timeline, and a task's warning show it, and the words that name the tool allowlist the user's settings lift.
const (
	unenforcedLabel  = "Restriction not enforced"
	allowlistLifted  = "to the task's tool list"
	restrictedTaskID = "custom-read"
)

// The sidebar's items, by their place in it, and the second tab of a Claude Code execution's detail, its Events.
const (
	navExecutions = ".nav .nav__item:nth-child(2)"
	navTasks      = ".nav .nav__item:nth-child(3)"
	navSettings   = ".nav .nav__item:nth-child(5)"
	eventsTab     = `.detail .tabs [role="tab"]:nth-child(2)`
)

// element is an element of the window's page, as the shell's test control channel reads it: its text, its tooltip, and
// the height it renders at, in CSS pixels.
type element struct {
	text, title string
	height      float64
}

// read is the elements of the window's page that the CSS selector matches, in document order, read through the shell's
// test control channel.
func (s *shell) read(selector string) []element {
	s.t.Helper()
	list, _ := s.send("read " + selector)["elements"].([]any)
	var out []element
	for _, item := range list {
		e, _ := item.(map[string]any)
		text, _ := e["text"].(string)
		title, _ := e["title"].(string)
		height, _ := e["height"].(float64)
		out = append(out, element{text, title, height})
	}
	return out
}

// click clicks the first element of the window's page that the CSS selector matches, through the shell's test control
// channel, as a user's click does; send fails the test when nothing matches.
func (s *shell) click(selector string) {
	s.t.Helper()
	s.send("click " + selector)
}

// waitShown waits until an element the CSS selector matches shows every one of texts, in its text or its tooltip: the
// interface renders what the daemon serves, and a screen it opens, a moment later.
func (s *shell) waitShown(selector string, texts ...string) {
	s.t.Helper()
	var got []element
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if got = s.read(selector); showing(got, texts...) {
			return
		}
	}
	s.t.Fatalf("read %s = %+v, want an element showing %q", selector, got, texts)
}

// showing says whether one of the elements shows every one of texts, in its text or its tooltip.
func showing(elements []element, texts ...string) bool {
	for _, e := range elements {
		all := true
		for _, text := range texts {
			all = all && (strings.Contains(e.text, text) || strings.Contains(e.title, text))
		}
		if all {
			return true
		}
	}
	return false
}

// harmflow-app#873: the setting that hides the restriction warnings hides only an execution's banner, in the running
// desktop app against a real harmflowd. A custom task that allows only Read runs to completed on the stand-in Claude
// Code, whose user settings, in a configuration folder of the test's own, allow Bash(*) and so lift the task's tool
// allowlist. With the fresh settings, the execution's detail in the app's window shows the banner naming that
// restriction. The setting is then switched off through the app's Settings, as a click on its toggle does, with no
// restart of the app: the same execution's detail shows no restriction banner, its Events timeline still lists the
// restriction, the task's row on the Tasks screen still shows its warning, and the daemon's log of the execution still
// holds the policy.unenforced event. The window is read and clicked through the debug build's test control channel, so
// the test needs no Accessibility or Automation permission.
func TestTheRestrictionWarningsSettingHidesOnlyAnExecutionsBanner(t *testing.T) {
	bin := realDesktop(t)
	home := t.TempDir()
	config := filepath.Join(home, "claude")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := mustJSON(map[string]any{"permissions": map[string]any{"allow": []string{"Bash(*)"}}})
	if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	d := startDaemon(t, home, "CLAUDE_CONFIG_DIR="+config)
	// The texts the test reads are the interface's in en-US, whatever the system's language, on the shell's own screens
	// rather than first use's.
	d.call("PATCH", "/settings", map[string]any{"language": "en-US", "onboardingCompleted": true}, 200)
	if got := d.call("GET", "/settings", nil, 200)["showRestrictionWarnings"]; got != true {
		t.Fatalf("GET /settings showRestrictionWarnings = %v, want true: the fresh settings show the warnings", got)
	}
	allowlist := "  permissions:\n    profile: custom\n    custom:\n      allowedTools: [Read]\n"
	saved := d.call("POST", "/tasks", map[string]any{"yaml": task(restrictedTaskID, "Say hi.", allowlist)}, 201)
	if policyWarning(saved["warnings"], "messageKey", "restriction_claude_allow_rules") == nil {
		t.Fatalf("POST /tasks %s warnings = %v, want the warning that the user's settings lift its tool allowlist", restrictedTaskID, saved["warnings"])
	}
	x := d.completedRun(restrictedTaskID)
	id := x["id"].(string)
	if policyWarning(x["warnings"], "subject", "tool_allowlist") == nil {
		t.Fatalf("completedRun(%s) warnings = %v, want the tool allowlist's warning", restrictedTaskID, x["warnings"])
	}
	health := d.call("GET", "/health", nil, 200)

	serveDashboard(t, d)
	s := startShell(t, bin, d)
	s.waitState(true, true)
	// The Executions screen opens on its latest execution, the only one here.
	s.waitShown(".detail__title", "Task "+restrictedTaskID)
	s.waitShown(".detail__alerts .banner", unenforcedLabel, allowlistLifted)

	s.click(navSettings)
	s.waitShown(".app__content", "Show restriction warnings")
	s.click("#settings-restriction-warnings")
	var stored any
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if stored = d.call("GET", "/settings", nil, 200)["showRestrictionWarnings"]; stored == false {
			break
		}
	}
	if stored != false {
		t.Fatalf("GET /settings showRestrictionWarnings = %v after a click on the Settings toggle, want false", stored)
	}

	s.click(navExecutions)
	s.waitShown(".detail__title", "Task "+restrictedTaskID)
	// The detail loads its events before the banner could show: the Events tab's count waits for them.
	s.click(eventsTab)
	s.waitShown(".detail .timeline", unenforcedLabel)
	if banners := s.read(".detail__alerts .banner"); showing(banners, unenforcedLabel) {
		t.Errorf("read .detail__alerts .banner = %+v with the setting off, want no restriction banner", banners)
	}

	s.click(navTasks)
	s.waitShown(".task-row", "Task "+restrictedTaskID)
	if rows := s.read(".task-row .chip"); !showing(rows, unenforcedLabel, allowlistLifted) {
		t.Errorf("read .task-row .chip = %+v with the setting off, want the task's own warning naming its tool allowlist", rows)
	}

	if !s.running() {
		t.Error("the shell ended while the setting was switched off, want it running throughout")
	}
	now := d.call("GET", "/health", nil, 200)
	if now["pid"] != health["pid"] || now["startedAt"] != health["startedAt"] {
		t.Errorf("GET /health = pid %v, startedAt %v, want the same daemon as before the shell started: pid %v, startedAt %v",
			now["pid"], now["startedAt"], health["pid"], health["startedAt"])
	}
	var types []string
	for _, line := range readLog(t, filepath.Join(d.home, "logs", id+".jsonl")) {
		types = append(types, line.Type)
	}
	if !strings.Contains(","+strings.Join(types, ",")+",", ",policy.unenforced,") {
		t.Errorf("the execution's log holds %s, want a policy.unenforced event with the setting off", strings.Join(types, ", "))
	}
}
