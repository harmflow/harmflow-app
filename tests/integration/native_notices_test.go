package integration

import (
	"fmt"
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
// which is what it does while that identity's notices are not allowed. macOS lists every notice it still holds for the
// identity, other processes' and earlier runs' included, so the task's name is this run's own and only the notices
// naming it count.
func TestChosenEventsReachMacOSAsNativeNotices(t *testing.T) {
	bin := realDesktop(t)
	d := startDaemon(t, t.TempDir())
	// The texts the test expects are the interface's in en-US, whatever the system's language.
	d.call("PATCH", "/settings", map[string]any{"language": "en-US"}, 200)
	id := fmt.Sprintf("noticed-%x", time.Now().UnixNano())
	events := "  notifications:\n    native:\n      events: [execution.completed, execution.failed]\n"
	created := d.call("POST", "/tasks", map[string]any{"yaml": task(id, "SCENARIO=ok", events)}, 201)
	serveDashboard(t, d)
	s := startShell(t, bin, d)
	s.waitState(true, true)

	completed := d.call("POST", "/tasks/"+id+"/run", nil, 202)["executionId"].(string)
	if x := d.waitStatus(completed, "completed", "failed", "cancelled", "interrupted"); x["status"] != "completed" {
		t.Fatalf("the first execution ended %v, want completed", x["status"])
	}
	edit := map[string]any{"yaml": task(id, "SCENARIO=error", events), "baseRevision": created["revision"]}
	d.call("PUT", "/tasks/"+id, edit, 200)
	failed := d.call("POST", "/tasks/"+id+"/run", nil, 202)["executionId"].(string)
	if x := d.waitStatus(failed, "completed", "failed", "cancelled", "interrupted"); x["status"] != "failed" {
		t.Fatalf("the second execution ended %v, want failed", x["status"])
	}

	name := "Task " + id
	want := map[string]string{name + " completed": "The execution finished.", name + " failed": "The execution failed:"}
	got := s.waitNotices(name, len(want))
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

// waitNotices waits until macOS holds n notices naming the task, for 30 seconds at most, and returns the notices naming
// it. The dashboard gets the daemon's notice from its event stream, so each one lands a moment after its execution
// ends. It skips the test, naming the identity the shell delivers under, when macOS holds none of them: that is what
// macOS does while that identity's notices are not allowed.
func (s *shell) waitNotices(name string, n int) []notice {
	s.t.Helper()
	var bundle string
	var got []notice
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		var all []notice
		bundle, all = s.notices()
		if got = naming(all, name); len(got) >= n {
			break
		}
	}
	if len(got) == 0 {
		s.t.Skipf("macOS holds none of the shell's notices, delivered under %q: allow that identity's notifications in "+
			"System Settings → Notifications, then run the test again", bundle)
	}
	return got
}

// naming is the notices of delivered whose title or body names the task.
func naming(delivered []notice, name string) []notice {
	var out []notice
	for _, n := range delivered {
		if strings.Contains(n.title, name) || strings.Contains(n.body, name) {
			out = append(out, n)
		}
	}
	return out
}

// failureNotice is a task's choice of the one event whose native notice these tests expect: its execution's failure.
const failureNotice = "  notifications:\n    native:\n      events: [execution.failed]\n"

// hiddenFor is how long the window stays hidden before the failure: not a moment after it hid, so that a page WebKit
// throttles or suspends while its window is hidden has had the time to be.
const hiddenFor = 15 * time.Second

// waitListening waits until the window's page shows its event stream connected, which it learns only once it listens
// to the events the shell forwards: from then on, a notice the daemon creates reaches the page's handler. The page is
// read through the shell's test control channel, whether the window shows or not, and an answer that it cannot be read
// yet, before it has loaded, is read again.
func (s *shell) waitListening() {
	s.t.Helper()
	var answer map[string]any
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		answer = s.ask("read .conn-pill--connected")
		if list, _ := answer["elements"].([]any); len(list) > 0 {
			return
		}
	}
	s.t.Fatalf("read .conn-pill--connected = %v after 60s, want the page's event stream connected", answer)
}

// failOnce runs the task once, on the stand-in's error scenario, and waits until its execution fails.
func (d *daemon) failOnce(id string) string {
	d.t.Helper()
	x := d.call("POST", "/tasks/"+id+"/run", nil, 202)["executionId"].(string)
	if got := d.waitStatus(x, "completed", "failed", "cancelled", "interrupted"); got["status"] != "failed" {
		d.t.Fatalf("the execution %s of %s ended %v, want failed", x, id, got["status"])
	}
	return x
}

// harmflow-app#516: with no dashboard window shown, a task's failure still reaches macOS as a native notice, which the
// running desktop app hands it. The shell starts with the tray on either as the login item starts it, with --hidden,
// or with its window shown, which the test then closes through the debug build's control channel, as the close button
// does. Either way the window stays hidden while a task that notifies on execution.failed fails, and the test reads
// back from macOS's notification center one notice of the failure, with the interface's en-US title and body. The
// interface's page sends the notices, so a hidden page that WebKit throttled or suspended would lose it. The run spends
// no usage: the stand-in fake-claude is the harness, and its error scenario fails the execution. The task's name is
// this run's own, as in TestChosenEventsReachMacOSAsNativeNotices, and the test skips the same way when macOS holds
// none of the shell's notices.
func TestAHiddenWindowStillHandsMacOSAFailuresNotice(t *testing.T) {
	bin := realDesktop(t)
	for _, hidden := range []bool{true, false} {
		t.Run(map[bool]string{true: "started_hidden", false: "closed_window"}[hidden], func(t *testing.T) {
			d := startDaemon(t, t.TempDir()).in(t)
			// The texts the test expects are the interface's in en-US, on the shell's own screens rather than first use's.
			d.call("PATCH", "/settings", map[string]any{"language": "en-US", "onboardingCompleted": true}, 200)
			id := fmt.Sprintf("unseen-%x", time.Now().UnixNano())
			d.call("POST", "/tasks", map[string]any{"yaml": task(id, "SCENARIO=error", failureNotice)}, 201)
			serveDashboard(t, d)

			var s *shell
			if hidden {
				s = startShell(t, bin, d, "--hidden")
				s.waitState(false, true)
				s.waitListening()
			} else {
				s = startShell(t, bin, d)
				s.waitState(true, true)
				s.waitListening()
				s.send("close")
				s.waitState(false, true)
			}
			time.Sleep(hiddenFor)
			failed := d.failOnce(id)

			name := "Task " + id
			got := s.waitNotices(name, 1)
			if len(got) != 1 || got[0].title != name+" failed" || !strings.HasPrefix(got[0].body, "The execution failed:") {
				t.Errorf("macOS holds %+v for the failure of %s, want one notice %q whose body starts %q", got, failed,
					name+" failed", "The execution failed:")
			}
			if state := s.send("state"); state["visible"] != false {
				t.Errorf("the shell's state is %v once the notice is out, want the window hidden throughout", state)
			}
		})
	}
}

// harmflow-app#516: the daemon hands macOS no native notice of its own. With no desktop app running, a task that
// notifies on execution.failed fails: the daemon holds an unread notice of the failure, and macOS holds no native
// notice of it. macOS is read afterwards, through a shell that starts on a second daemon of its own: that daemon knows
// nothing of the first one's events, so the shell cannot send the missed notice itself. A task of the second daemon
// fails first, so that the shell has sent a notice and has the identity to read macOS's notification center under, and
// its notice shows that the read reaches the center a notice under that identity lands in. The daemon's own notice
// would name the task, as its English fallback does, so any notice naming it fails the test. The run spends no usage.
func TestTheDaemonAloneHandsMacOSNoNotice(t *testing.T) {
	bin := realDesktop(t)
	alone := startDaemon(t, t.TempDir())
	id := fmt.Sprintf("unattended-%x", time.Now().UnixNano())
	alone.call("POST", "/tasks", map[string]any{"yaml": task(id, "SCENARIO=error", failureNotice)}, 201)
	failed := alone.failOnce(id)
	held := func() []map[string]any {
		var out []map[string]any
		for _, n := range alone.list("/notifications?unread=true") {
			if n["executionId"] == failed && n["eventType"] == "execution.failed" {
				out = append(out, n)
			}
		}
		return out
	}
	var unread []map[string]any
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && len(unread) == 0; time.Sleep(200 * time.Millisecond) {
		unread = held()
	}
	if len(unread) != 1 {
		t.Fatalf("GET /notifications?unread=true holds %d notices of the failure of %s, want one", len(unread), failed)
	}

	reader := startDaemon(t, t.TempDir())
	reader.call("PATCH", "/settings", map[string]any{"language": "en-US", "onboardingCompleted": true}, 200)
	probe := fmt.Sprintf("reader-%x", time.Now().UnixNano())
	reader.call("POST", "/tasks", map[string]any{"yaml": task(probe, "SCENARIO=error", failureNotice)}, 201)
	serveDashboard(t, reader)
	s := startShell(t, bin, reader)
	s.waitState(true, true)
	s.waitListening()
	reader.failOnce(probe)
	s.waitNotices("Task "+probe, 1)

	if _, all := s.notices(); len(naming(all, "Task "+id)) > 0 {
		t.Errorf("macOS holds %+v, notices of the failure of %s, which no desktop app saw", naming(all, "Task "+id), failed)
	}
	if got := held(); len(got) != 1 {
		t.Errorf("GET /notifications?unread=true holds %d notices of the failure of %s at the end, want it still unread",
			len(got), failed)
	}
}
