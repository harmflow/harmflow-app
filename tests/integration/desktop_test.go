package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	cmd := exec.Command(bin, args...)
	cmd.Env = daemonEnv([]string{"HARMFLOW_HOME=" + d.home, "HARMFLOW_TEST_SHELL_CONTROL=1", "HARMFLOW_DAEMON_BIN=" + binary})
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
