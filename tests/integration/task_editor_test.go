package integration

import (
	"testing"
	"time"
)

// The parts of the task editor the test reads, in the screen that holds it.
const (
	editorScreen = ".screen:has(> .editor)"
	editorBody   = ".editor__body"
	editorTabRow = ".editor .tabs"
	editorTab    = ".editor .tabs__tab"
	editorYAML   = `.editor .tabs [role="tab"]:nth-child(2)`
)

// height is the rendered height of the first element of the window's page that the CSS selector matches, read through
// the shell's test control channel, and whether one matched.
func (s *shell) height(selector string) (float64, bool) {
	s.t.Helper()
	elements := s.read(selector)
	if len(elements) == 0 {
		return 0, false
	}
	return elements[0].height, true
}

// waitTallForm waits until the editor's form, laid out in full, is taller than the screen that holds it: the form the
// tab row has to share its screen with.
func (s *shell) waitTallForm() {
	s.t.Helper()
	var body, screen float64
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		body, _ = s.height(editorBody)
		screen, _ = s.height(editorScreen)
		if screen > 0 && body > screen {
			return
		}
	}
	s.t.Fatalf("height of %s = %v, of %s = %v, want the editor's body taller than its screen", editorBody, body,
		editorScreen, screen)
}

// checkTabRow fails the test unless the editor's tab row shows Form and YAML and is at least as tall as its tabs: a row
// shrunk to its border shows its tabs clipped to nothing.
func (s *shell) checkTabRow(view string) {
	s.t.Helper()
	s.waitShown(editorTabRow, "Form", "YAML")
	row, _ := s.height(editorTabRow)
	tab, ok := s.height(editorTab)
	if !ok {
		s.t.Fatalf("read %s found no tab with the %s tab open", editorTab, view)
	}
	if row < tab {
		s.t.Errorf("height of %s = %v with the %s tab open, want at least its tab's %v: the row shrank to its border",
			editorTabRow, row, view, tab)
	}
}

// harmflow-app#912: the task editor shows its row of Form and YAML tabs whichever of them is open, with a form taller
// than the window. The row scrolls sideways, which makes it a scroll container whose automatic minimum height is 0, so
// in the editor's flex column of fixed height it alone absorbed the overflow of a tall form and shrank to its border.
// The test opens a task's editor in the running desktop app against a real harmflowd, waits for its form to lay out
// taller than the screen that holds it, and reads the tab row's height against its tabs' on the Form tab, the
// editor's default. A click on YAML then opens the YAML editor, under a row that keeps its height. The window is read
// and clicked through the debug build's test control channel, so the test needs no Accessibility or Automation
// permission.
func TestTheTaskEditorShowsItsTabsOverATallForm(t *testing.T) {
	bin := realDesktop(t)
	const id = "tall-form"
	d := startDaemon(t, t.TempDir())
	// The texts the test reads are the interface's in en-US, whatever the system's language, on the shell's own screens
	// rather than first use's.
	d.call("PATCH", "/settings", map[string]any{"language": "en-US", "onboardingCompleted": true}, 200)
	d.call("POST", "/tasks", map[string]any{"yaml": task(id, "Say hi.", "")}, 201)

	serveDashboard(t, d)
	s := startShell(t, bin, d)
	s.waitState(true, true)
	s.click(navTasks)
	s.waitShown(".task-row", "Task "+id)
	s.click(".task-row__main")
	s.waitShown(".editor__title", "Task "+id)

	s.waitTallForm()
	s.checkTabRow("Form")

	s.click(editorYAML)
	s.waitShown(".editor .yaml-editor", "id: "+id)
	s.checkTabRow("YAML")
}
