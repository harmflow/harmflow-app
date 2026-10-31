package integration

import (
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// dashboardURL is where the shell's debug build loads the interface from: the dev server's address in tauri.conf.json.
const dashboardURL = "http://localhost:1420"

// serveDashboard starts the interface's dev server for the shell's debug build, which loads the interface from it and
// sends the notices from it, and stops it when the test ends. It skips the test without pnpm on the PATH.
func serveDashboard(t *testing.T, d *daemon) {
	t.Helper()
	pnpm, err := exec.LookPath("pnpm")
	if err != nil {
		t.Skip("needs pnpm on the PATH and the desktop's dependencies (make deps), to serve the interface")
	}
	if resp, err := http.Get(dashboardURL); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("something already answers at %s: stop it, the shell's debug build would load it", dashboardURL)
	}
	cmd := exec.Command(pnpm, "-C", filepath.Join(repoRoot(), "desktop", "ui"), "dev")
	// The dev server's /api reaches the daemon whose home it reads, the test's own.
	cmd.Env = daemonEnv([]string{"HARMFLOW_HOME=" + d.home})
	// pnpm starts vite as its child: the group ends both.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
	})
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if resp, err := http.Get(dashboardURL); err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the interface's dev server does not answer at %s within 60s", dashboardURL)
		}
	}
}

// notice is a notice macOS holds as delivered for the shell.
type notice struct{ title, body string }

// notices reads the notices macOS delivered for the shell, through its test control channel, and the bundle identifier
// they were delivered under: empty while the shell has sent none.
func (s *shell) notices() (bundle string, delivered []notice) {
	s.t.Helper()
	answer := s.send("notices")
	bundle, _ = answer["bundle"].(string)
	list, _ := answer["notices"].([]any)
	for _, item := range list {
		n, _ := item.(map[string]any)
		title, _ := n["title"].(string)
		body, _ := n["body"].(string)
		delivered = append(delivered, notice{title, body})
	}
	return bundle, delivered
}

// harmflow-app#515: a task that notifies on execution.completed and execution.failed has one execution complete and
// another fail, and the running desktop app hands macOS one native notice for each, with the interface's translated
// title and body. The notices are read back from macOS's notification center through the debug build's control channel,
// not from a stand-in for the notification plugin. The run spends no usage: the stand-in fake-claude is the harness,
// and its error scenario fails the second execution. A debug build delivers its notices under the identity the
// notification plugin gives a development build, Terminal's: the test skips, naming it, when macOS holds none of them,
// which is what it does while that identity's notices are not allowed.
func TestChosenEventsReachMacOSAsNativeNotices(t *testing.T) {
	bin := realDesktop(t)
	d := startDaemon(t, t.TempDir())
	// The texts the test expects are the interface's in en-US, whatever the system's language.
	d.call("PATCH", "/settings", map[string]any{"language": "en-US"}, 200)
	events := "  notifications:\n    native:\n      events: [execution.completed, execution.failed]\n"
	created := d.call("POST", "/tasks", map[string]any{"yaml": task("noticed", "SCENARIO=ok", events)}, 201)
	serveDashboard(t, d)
	s := startShell(t, bin, d)
	s.waitState(true, true)
	_, before := s.notices()

	completed := d.call("POST", "/tasks/noticed/run", nil, 202)["executionId"].(string)
	if x := d.waitStatus(completed, "completed", "failed", "cancelled", "interrupted"); x["status"] != "completed" {
		t.Fatalf("the first execution ended %v, want completed", x["status"])
	}
	d.call("PUT", "/tasks/noticed", map[string]any{"yaml": task("noticed", "SCENARIO=error", events), "baseRevision": created["revision"]}, 200)
	failed := d.call("POST", "/tasks/noticed/run", nil, 202)["executionId"].(string)
	if x := d.waitStatus(failed, "completed", "failed", "cancelled", "interrupted"); x["status"] != "failed" {
		t.Fatalf("the second execution ended %v, want failed", x["status"])
	}

	// Each notice the interface sent since the shell started, as macOS holds it. The dashboard gets the daemon's notice
	// from its event stream, so each one lands a moment after its execution ends.
	want := map[string]string{"Task noticed completed": "The execution finished.", "Task noticed failed": "The execution failed:"}
	var bundle string
	var got []notice
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		var all []notice
		bundle, all = s.notices()
		got = newNotices(before, all)
		if len(got) >= len(want) {
			break
		}
	}
	if len(got) == 0 {
		t.Skipf("macOS holds none of the shell's notices, delivered under %q: allow that identity's notifications in "+
			"System Settings → Notifications, then run the test again", bundle)
	}
	count := map[string]int{}
	for _, n := range got {
		prefix, ok := want[n.title]
		if !ok {
			t.Errorf("macOS holds a notice %q (%q), which the task's chosen events do not give", n.title, n.body)
			continue
		}
		count[n.title]++
		if !strings.HasPrefix(n.body, prefix) {
			t.Errorf("the notice %q reads %q, want the interface's translated body, which starts %q", n.title, n.body, prefix)
		}
	}
	for title := range want {
		if count[title] != 1 {
			t.Errorf("macOS holds %d notices %q, want one: delivered %+v", count[title], title, got)
		}
	}
}

// newNotices is what all holds beyond before: macOS lists the notices it still holds for the identity, the ones other
// processes delivered under it included.
func newNotices(before, all []notice) []notice {
	seen := map[notice]int{}
	for _, n := range before {
		seen[n]++
	}
	var added []notice
	for _, n := range all {
		if seen[n] > 0 {
			seen[n]--
			continue
		}
		added = append(added, n)
	}
	return added
}
