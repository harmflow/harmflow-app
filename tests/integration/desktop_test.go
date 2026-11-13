package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"
)

// desktopEnv opts in to the tests that start the real desktop shell, which opens a window on the screen.
const desktopEnv = "HARMFLOW_TEST_DESKTOP"

// realDesktop skips the test unless it runs on macOS with HARMFLOW_TEST_DESKTOP exactly 1 and cargo on the PATH, and
// otherwise builds the shell in its debug build, the only one with the test's control channel, and returns its path.
// The window needs a display: a session with none opens no window, and the test fails waiting for it.
func realDesktop(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" || os.Getenv(desktopEnv) != "1" {
		t.Skipf("needs macOS, a display, and %s=1: it opens the desktop's real window", desktopEnv)
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("needs cargo on the PATH, to build the desktop shell")
	}
	root := repoRoot()
	sidecars, _ := filepath.Glob(filepath.Join(root, "desktop", "src-tauri", "binaries", "harmflowd-*"))
	if len(sidecars) == 0 {
		t.Fatal("the desktop shell's build needs its sidecar: run make sidecar first")
	}
	// From the repository's root, so that .cargo/config.toml puts the build in target.noindex.
	build := exec.Command(cargo, "build", "--manifest-path", filepath.Join("desktop", "src-tauri", "Cargo.toml"))
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the desktop shell: %v\n%s", err, out)
	}
	return filepath.Join(root, "desktop", "src-tauri", "target.noindex", "debug", "harmflow-desktop")
}

// shell is a running desktop shell, driven through its test control channel: one command per line on its standard
// input, one JSON answer per line on its standard output.
type shell struct {
	t       *testing.T
	cmd     *exec.Cmd
	in      io.Writer
	answers chan map[string]any
	exited  chan struct{}
}

// startShell starts the desktop shell on the daemon's home, with its test control channel on and the arguments args,
// such as the --hidden the login item starts it with.
func startShell(t *testing.T, bin string, d *daemon, args ...string) *shell {
	t.Helper()
	return startShellWith(t, bin, d, nil, args...)
}

// startShellWith is startShell with env applied after the shell's own variables, as daemonEnv applies it, such as a
// throwaway HOME for the login item the shell writes there.
func startShellWith(t *testing.T, bin string, d *daemon, env []string, args ...string) *shell {
	t.Helper()
	cmd := exec.Command(bin, args...)
	own := []string{"HARMFLOW_HOME=" + d.home, "HARMFLOW_TEST_SHELL_CONTROL=1", "HARMFLOW_DAEMON_BIN=" + binary}
	cmd.Env = daemonEnv(append(own, env...))
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// One answer at most is left unread: that of a command that ended the shell, when the shell exited first.
	s := &shell{t: t, cmd: cmd, in: in, answers: make(chan map[string]any, 1), exited: make(chan struct{})}
	// Both goroutines stop once the shell exits, which the cleanup below makes sure of.
	go func() {
		// The shell may print lines of its own: only a JSON object is an answer.
		lines := bufio.NewScanner(out)
		for lines.Scan() {
			var answer map[string]any
			if json.Unmarshal(lines.Bytes(), &answer) == nil {
				s.answers <- answer
			}
		}
	}()
	go func() { _ = cmd.Wait(); close(s.exited) }()
	t.Cleanup(func() {
		select {
		case <-s.exited:
		default:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-s.exited:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-s.exited
			}
		}
	})
	return s
}

// send writes a command and returns its answer, failing the test on an error or no answer within ten seconds.
func (s *shell) send(command string) map[string]any {
	s.t.Helper()
	answer := s.ask(command)
	if answer["error"] != nil {
		s.t.Fatalf("%s: %v", command, answer["error"])
	}
	return answer
}

// ask writes a command and returns its answer, an error the shell answers with included, failing the test when the
// shell exits or gives no answer within ten seconds.
func (s *shell) ask(command string) map[string]any {
	s.t.Helper()
	if _, err := fmt.Fprintln(s.in, command); err != nil {
		s.t.Fatalf("%s: %v", command, err)
	}
	select {
	case answer := <-s.answers:
		return answer
	case <-s.exited:
		s.t.Fatalf("%s: the shell exited", command)
	case <-time.After(10 * time.Second):
		s.t.Fatalf("%s: no answer within 10s", command)
	}
	return nil
}

// act writes a command that may end the shell, which may then exit before it answers: no failure, unlike send.
func (s *shell) act(command string) {
	s.t.Helper()
	if _, err := fmt.Fprintln(s.in, command); err != nil {
		s.t.Fatalf("%s: %v", command, err)
	}
	select {
	case answer := <-s.answers:
		if answer["error"] != nil {
			s.t.Fatalf("%s: %v", command, answer["error"])
		}
	case <-s.exited:
	case <-time.After(10 * time.Second):
		s.t.Fatalf("%s: no answer within 10s", command)
	}
}

// running says whether the shell's process is still running.
func (s *shell) running() bool {
	select {
	case <-s.exited:
		return false
	default:
		return true
	}
}

// waitState waits until the shell's window is visible or not, as visible says, with the tray enabled as the stored
// setting says. A start shows the window once the interface is ready or after its ten-second fallback, and reads the
// tray's setting from the daemon, so both take a moment.
func (s *shell) waitState(visible, tray bool) {
	s.t.Helper()
	var state map[string]any
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if state = s.send("state"); state["visible"] == visible && state["trayEnabled"] == tray {
			return
		}
	}
	s.t.Fatalf("the shell's state is %v, want visible %v and trayEnabled %v", state, visible, tray)
}

// tray reads the tray as macOS shows it, through the shell's test control channel: the title of its status item, nil
// when it shows none, and which of the tray's two images it shows, idle or executing, or unknown when neither.
func (s *shell) tray() (title, image any) {
	s.t.Helper()
	answer := s.send("tray")
	return answer["title"], answer["image"]
}

// waitTray waits until the tray shows the title, nil for none, and the image. The shell refreshes the tray a moment
// after an execution's event reaches it, so a change takes a moment.
func (s *shell) waitTray(title any, image string) {
	s.t.Helper()
	var gotTitle, gotImage any
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if gotTitle, gotImage = s.tray(); gotTitle == title && gotImage == image {
			return
		}
	}
	s.t.Fatalf("tray = title %v, image %v after 30s, want title %v, image %s", gotTitle, gotImage, title, image)
}

// keepsTray fails the test unless the tray shows the title and the image at every read for the duration d.
func (s *shell) keepsTray(title any, image string, d time.Duration) {
	s.t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if gotTitle, gotImage := s.tray(); gotTitle != title || gotImage != image {
			s.t.Fatalf("tray = title %v, image %v, want title %v, image %s throughout %v", gotTitle, gotImage, title,
				image, d)
		}
	}
}

// harmflow-app#880: the menu bar's HarmFlow item, as macOS shows it, follows only the executions that run. Before any
// execution it shows the idle image and no count. While an execution runs, it shows the executing image and the count
// 1, and a second firing of the same task, which waits queued, adds nothing to it. Once both executions have ended, it
// shows the idle image and no count again. The test reads the title and the image of the tray's status item through
// the debug build's control channel, which tells the image by its pixels. The run spends no usage: the stand-in
// fake-claude is the harness, and it holds every session at a gate until the test opens it.
func TestTheTrayShowsOnlyTheRunningExecutions(t *testing.T) {
	bin := realDesktop(t)
	d := startDaemon(t, t.TempDir())
	gate := filepath.Join(t.TempDir(), "gate")
	stand := standIn(t, "fake-claude", "claude",
		"case \"$SCENARIO\" in\n", fmt.Sprintf("until [ -e '%s' ]; do sleep 0.05; done\ncase \"$SCENARIO\" in\n", gate))
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": stand.path}, 200)
	d.call("POST", "/tasks", map[string]any{"yaml": task("gated", "SCENARIO=ok", "")}, 201)

	s := startShell(t, bin, d)
	s.waitState(true, true)
	s.waitTray(nil, "idle")

	first := d.call("POST", "/tasks/gated/run", nil, 202)["executionId"].(string)
	d.waitStatus(first, "running")
	s.waitTray("1", "executing")

	second := d.call("POST", "/tasks/gated/run", nil, 202)
	if second["outcome"] != "queued" {
		t.Fatalf("POST /tasks/gated/run while %s runs = %v, want outcome queued", first, second)
	}
	queued := second["executionId"].(string)
	if x := d.call("GET", "/executions/"+queued, nil, 200); x["status"] != "queued" {
		t.Fatalf("the second execution %s is %v, want queued while %s runs", queued, x["status"], first)
	}
	// The queued firing's event reaches the shell, which refreshes the tray from it, well within the two seconds.
	s.keepsTray("1", "executing", 2*time.Second)

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first, queued} {
		if x := d.waitStatus(id, "completed", "failed", "cancelled", "interrupted"); x["status"] != "completed" {
			t.Fatalf("the execution %s ended %v (%v), want completed", id, x["status"], x["errorMessage"])
		}
	}
	s.waitTray(nil, "idle")
}

// systemAlerts reads, through the shell's test control channel, how many windows macOS shows on screen for the
// system's alert process, which draws an alert that has no parent window, from the window list macOS gives any
// process.
func (s *shell) systemAlerts() int {
	s.t.Helper()
	alerts, ok := s.send("system-alerts")["alerts"].([]any)
	if !ok {
		s.t.Fatal("system-alerts answered no list of alerts")
	}
	return len(alerts)
}

// harmflow-app#903: stopping the daemon from the tray's menu asks first. With one execution running, the tray's menu,
// opened as a click opens it and read from the status item's NSMenu macOS shows, holds in order a disabled header
// counting that execution, its item, Open dashboard, Stop daemon…, and Quit interface, Stop daemon… and Quit interface
// being two items. Choosing Stop daemon… puts the system's alert on screen, seen in the window list as a window of the
// system's alert process, and while it is there the daemon still answers and the execution still runs: no stop
// reaches the daemon before the alert is answered. The test answers no alert, which only Accessibility could, so the
// alert's text, Cancel, and confirming stay with the unit tests of tray.rs. The run spends no usage: the stand-in
// fake-claude is the harness, and it holds the session at a gate until the test opens it.
func TestStoppingTheDaemonFromTheTrayAsksFirst(t *testing.T) {
	bin := realDesktop(t)
	d := startDaemon(t, t.TempDir())
	gate := filepath.Join(t.TempDir(), "gate")
	stand := standIn(t, "fake-claude", "claude",
		"case \"$SCENARIO\" in\n", fmt.Sprintf("until [ -e '%s' ]; do sleep 0.05; done\ncase \"$SCENARIO\" in\n", gate))
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": stand.path}, 200)
	// The texts the test expects are the shell's in en-US, whatever the system's language.
	d.call("PATCH", "/settings", map[string]any{"language": "en-US"}, 200)
	d.call("POST", "/tasks", map[string]any{"yaml": task("gated", "SCENARIO=ok", "")}, 201)

	s := startShell(t, bin, d)
	s.waitState(true, true)
	run := d.call("POST", "/tasks/gated/run", nil, 202)["executionId"].(string)
	d.waitStatus(run, "running")
	// The tray rebuilds its menu with its icon and count, once the execution's event reaches the shell.
	s.waitTray("1", "executing")

	want := []any{
		map[string]any{"id": "header", "text": "1 running", "enabled": false},
		map[string]any{"id": "exec:" + run, "text": "  Task gated", "enabled": true},
		map[string]any{"separator": true},
		map[string]any{"id": "open", "text": "Open dashboard", "enabled": true},
		map[string]any{"separator": true},
		map[string]any{"id": "stop", "text": "Stop daemon…", "enabled": true},
		map[string]any{"id": "quit", "text": "Quit interface", "enabled": true},
	}
	if got := s.send("tray-menu")["items"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("the tray's menu macOS shows = %v, want %v", got, want)
	}

	health := d.call("GET", "/health", nil, 200)
	before := s.systemAlerts()
	s.send("choose-tray stop")
	for deadline := time.Now().Add(10 * time.Second); s.systemAlerts() <= before; time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no window of the system's alert process came on screen within 10s of Stop daemon…; %d before it",
				before)
		}
	}
	// Nothing answers the alert, so for as long as it is on screen no stop may reach the daemon: the same daemon
	// answers, draining nothing.
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		now := d.call("GET", "/health", nil, 200)
		if now["pid"] != health["pid"] || now["startedAt"] != health["startedAt"] || now["draining"] != nil {
			t.Fatalf("GET /health while the stop's alert is on screen = %v, want the daemon of %v, draining nothing", now,
				health)
		}
		if x := d.call("GET", "/executions/"+run, nil, 200); x["status"] != "running" {
			t.Fatalf("the execution %s is %v while the stop's alert is on screen, want running", run, x["status"])
		}
		if n := s.systemAlerts(); n <= before {
			t.Fatalf("the system's alert left the screen with no answer: %d of its windows, %d before Stop daemon…", n,
				before)
		}
	}

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if x := d.waitStatus(run, "completed", "failed", "cancelled", "interrupted"); x["status"] != "completed" {
		t.Fatalf("the execution %s ended %v (%v), want completed", run, x["status"], x["errorMessage"])
	}
}

// waitTrayMenu waits until the tray's menu macOS shows, read as tray-menu reads it, holds exactly the items want, for
// at most the duration d.
func (s *shell) waitTrayMenu(want []any, d time.Duration) {
	s.t.Helper()
	var got any
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if got = s.send("tray-menu")["items"]; reflect.DeepEqual(got, want) {
			return
		}
	}
	s.t.Fatalf("the tray's menu macOS shows = %v after %v, want %v", got, d, want)
}

// withDaemonItem is the menu items with the daemon item at index i replaced by item.
func withDaemonItem(items []any, i int, item map[string]any) []any {
	out := slices.Clone(items)
	out[i] = item
	return out
}

// harmflow-app#1024: the tray's daemon item follows the daemon's state, in the menu macOS shows, with the same shell
// process throughout. While the daemon runs, the menu holds one daemon item, Stop daemon…, enabled. Once the daemon
// stops, through its local API with nothing running, the item reads Start daemon, enabled, in the same place, the rest
// of the menu as it was. Choosing Start daemon starts a daemon on the test's home with no alert on screen, and once it
// answers GET /health the item reads Stop daemon… again. While that daemon is alive but stopped by SIGSTOP, so that the
// shell's reads of its status time out with its pid alive, the item is disabled and reads Not answering, with neither
// Stop daemon… nor Start daemon; once it is resumed by SIGCONT, Stop daemon… comes back. The test asks for no
// Accessibility permission, and spends no usage: it runs no task.
func TestTheTrayDaemonItemFollowsTheDaemonsState(t *testing.T) {
	bin := realDesktop(t)
	home := t.TempDir()
	d := startDaemon(t, home)
	// The texts the test expects are the shell's in en-US, whatever the system's language.
	d.call("PATCH", "/settings", map[string]any{"language": "en-US"}, 200)

	// The daemon the tray starts is no child of the test: the shell starts it in a process group of its own. This
	// cleanup, registered before the shell's, runs once the shell has ended, and ends any daemon left on the home,
	// resumed first, since a process stopped by SIGSTOP takes no SIGTERM.
	t.Cleanup(func() {
		left, _ := daemonsOn(home)
		for _, pid := range left {
			if pid == d.cmd.Process.Pid {
				continue
			}
			_ = syscall.Kill(pid, syscall.SIGCONT)
			stopDetachedDaemon(pid)
		}
	})
	// The shell passes its environment to the daemon it starts, so it gets what startDaemon gives its own: the file
	// token store and a Codex home of the test's, never the user's keychain or ~/.codex. The start runs the test's
	// harmflowd on the test's home, which the user's login agent does not serve, so it never kickstarts that agent.
	s := startShellWith(t, bin, d, []string{"HARMFLOW_TOKEN_STORE=file", "CODEX_HOME=" + filepath.Join(home, "codex")})
	s.waitState(true, true)

	stop := map[string]any{"id": "stop", "text": "Stop daemon…", "enabled": true}
	start := map[string]any{"id": "start", "text": "Start daemon", "enabled": true}
	notAnswering := map[string]any{"id": "not-answering", "text": "Not answering", "enabled": false}
	// The shell reads the daemon's status as it starts, so the menu takes a moment to show the daemon running.
	var running []any
	at := -1
	for deadline := time.Now().Add(30 * time.Second); at < 0 && time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		running, _ = s.send("tray-menu")["items"].([]any)
		at = slices.IndexFunc(running, func(item any) bool { return reflect.DeepEqual(item, stop) })
	}
	if at < 0 {
		t.Fatalf("the tray's menu macOS shows = %v, want one item %v", running, stop)
	}
	for i, item := range running {
		entry, _ := item.(map[string]any)
		if id, _ := entry["id"].(string); i != at && (id == "stop" || id == "start" || id == "not-answering") {
			t.Fatalf("the tray's menu macOS shows = %v, want one daemon item, %v at %d", running, stop, at)
		}
	}

	// A stop with nothing running; the daemon removes its runtime file as it exits.
	d.call("POST", "/daemon/stop", map[string]any{}, 202)
	d.waitExit()
	// The event stream's loss refreshes the tray at once; failing that, the status read every 10 seconds does.
	s.waitTrayMenu(withDaemonItem(running, at, start), 30*time.Second)

	before := s.systemAlerts()
	s.send("choose-tray start")
	started := &daemon{t: t, home: home}
	if started.base, started.token = awaitRuntime(home, runtimeWait); started.base == "" {
		t.Fatalf("no daemon wrote its runtime file on the test's home within %v of Start daemon; its log ends:\n%s",
			runtimeWait, daemonLogTail(home))
	}
	health := started.call("GET", "/health", nil, 200)
	pid := int(health["pid"].(float64))
	watchDaemon(t, pid, true)
	if pid == d.cmd.Process.Pid {
		t.Fatalf("GET /health after Start daemon names pid %d, the daemon the test stopped", pid)
	}
	// The start waits for the daemon to answer before it rebuilds the menu, well within the 30 seconds.
	s.waitTrayMenu(running, 30*time.Second)
	if n := s.systemAlerts(); n > before {
		t.Fatalf("%d windows of the system's alert process on screen after Start daemon, %d before it: the start asked", n,
			before)
	}
	if !s.running() {
		t.Fatal("the shell exited after Start daemon, want the process the test started")
	}

	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP to the daemon %d: %v", pid, err)
	}
	// The status read every 10 seconds waits for its answer for the request's 30-second timeout, and the refresh it
	// starts reads the status once more, as long: the item moves within about 70 seconds, and the margin covers a read
	// already under way.
	s.waitTrayMenu(withDaemonItem(running, at, notAnswering), 120*time.Second)
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		t.Fatalf("SIGCONT to the daemon %d: %v", pid, err)
	}
	// The next status read answers, within 10 seconds and the one under way.
	s.waitTrayMenu(running, 60*time.Second)
	if !s.running() {
		t.Fatal("the shell exited while the daemon did not answer, want the process the test started")
	}
}
