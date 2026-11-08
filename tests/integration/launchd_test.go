package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// launchdEnv opts in to the test that has the user's own launchd start a daemon in the signed-in user's GUI domain.
const launchdEnv = "HARMFLOW_TEST_LAUNCHD"

// userAgentLabel is the label of the user agent HarmFlow writes: the user's own job, which no test loads, replaces,
// stops, or unloads.
const userAgentLabel = "com.harmflow.daemon"

// loginItemLabel is the label of the login item's user agent, which the shell's autostart plugin names after the app's
// product name: the user's own job too, which no test loads, replaces, stops, or unloads.
const loginItemLabel = "HarmFlow"

// realLaunchd skips the test unless it runs on macOS with HARMFLOW_TEST_LAUNCHD exactly 1, in a signed-in user session
// whose GUI domain launchd serves, and otherwise returns that domain, gui/<uid>.
func realLaunchd(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" || os.Getenv(launchdEnv) != "1" {
		t.Skipf("needs macOS, a signed-in user session, and %s=1: launchd starts a daemon in the user's session",
			launchdEnv)
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if err := exec.Command("/bin/launchctl", "print", domain).Run(); err != nil {
		t.Skipf("needs a signed-in user session: launchctl print %s: %v", domain, err)
	}
	return domain
}

// launchdPIDLine is the line of launchctl print that names the pid of a job's running process.
var launchdPIDLine = regexp.MustCompile(`(?m)^\s*pid = (\d+)$`)

// launchdJob reads launchd's own record of the job with the label in the domain: whether it is loaded, and the pid of
// its running process, 0 when none runs.
func launchdJob(domain, label string) (loaded bool, pid int) {
	out, err := exec.Command("/bin/launchctl", "print", domain+"/"+label).Output()
	if err != nil {
		return false, 0
	}
	if m := launchdPIDLine.FindSubmatch(out); m != nil {
		pid, _ = strconv.Atoi(string(m[1]))
	}
	return true, pid
}

// plistJSON reads a property list as JSON, through plutil.
func plistJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	out, err := exec.Command("/usr/bin/plutil", "-convert", "json", "-o", "-", path).Output()
	if err != nil {
		t.Fatalf("plutil -convert json %s: %v", path, err)
	}
	var p map[string]any
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("plutil -convert json %s printed no object: %v", path, err)
	}
	return p
}

// launchAgents lists the files of a LaunchAgents folder with their size and modification time; an absent folder lists
// none.
func launchAgents(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = fmt.Sprintf("%d bytes, modified %s", info.Size(), info.ModTime().Format(time.RFC3339Nano))
	}
	return files
}

// keepOwnSetup fails the test, once it ends, unless the user's own ~/Library/LaunchAgents and the user's own jobs of
// the labels read as they did when it was called: loaded or not, with the same pid. Called before the test loads any
// job, its check runs last, once the test's own jobs are booted out.
func keepOwnSetup(t *testing.T, domain string, labels ...string) {
	t.Helper()
	ownHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	ownAgentsDir := filepath.Join(ownHome, "Library", "LaunchAgents")
	ownAgents := launchAgents(t, ownAgentsDir)
	type job struct {
		loaded bool
		pid    int
	}
	ownJobs := map[string]job{}
	for _, label := range labels {
		loaded, pid := launchdJob(domain, label)
		ownJobs[label] = job{loaded, pid}
	}
	t.Cleanup(func() {
		if got := launchAgents(t, ownAgentsDir); !maps.Equal(got, ownAgents) {
			t.Errorf("%s changed during the test: %v, was %v", ownAgentsDir, got, ownAgents)
		}
		for label, was := range ownJobs {
			if loaded, pid := launchdJob(domain, label); loaded != was.loaded || pid != was.pid {
				t.Errorf("the user's own job %s reads loaded %v, pid %d, was loaded %v, pid %d", label, loaded, pid,
					was.loaded, was.pid)
			}
		}
	})
}

// testLabel is a launchd label unique to the run, for a job of the kind the test loads.
func testLabel(kind string) string {
	return fmt.Sprintf("com.harmflow.test.%s-%d-%d", kind, os.Getpid(), time.Now().UnixNano())
}

// copyAgent copies the user agent at src beside it, under the label and with only its Label changed to it, then
// applies each edit to the copy, the arguments of one plutil call such as {"-insert", "Key", "-string", "value"}, and
// returns the copy's path.
func copyAgent(t *testing.T, src, label string, edits ...[]string) string {
	t.Helper()
	copied := filepath.Join(filepath.Dir(src), label+".plist")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copied, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, edit := range append([][]string{{"-replace", "Label", "-string", label}}, edits...) {
		if out, err := exec.Command("/usr/bin/plutil", append(edit, copied)...).CombinedOutput(); err != nil {
			t.Fatalf("plutil %s %s: %v\n%s", strings.Join(edit, " "), copied, err, out)
		}
	}
	return copied
}

// loadJob has launchd load the agent at plist, whose label is the label, into the domain, as a login loads a user
// agent, and returns the pid of the process launchd starts for it, 0 when it starts none within the duration. The job
// is booted out when the test ends, failed or not, and its process, once booted out, must exit within ten seconds or
// is killed.
func loadJob(t *testing.T, domain, plist, label string, within time.Duration) int {
	t.Helper()
	var pid int
	t.Cleanup(func() {
		if out, err := exec.Command("/bin/launchctl", "bootout", domain+"/"+label).CombinedOutput(); err != nil {
			if loaded, _ := launchdJob(domain, label); loaded {
				t.Errorf("launchctl bootout %s/%s: %v\n%s", domain, label, err, out)
			}
		}
		for deadline := time.Now().Add(10 * time.Second); pid > 0 && syscall.Kill(pid, 0) == nil; time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Errorf("the process launchd started for %s, pid %d, still runs after its job was booted out", label, pid)
				_ = syscall.Kill(pid, syscall.SIGKILL)
				return
			}
		}
	})
	if out, err := exec.Command("/bin/launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
		t.Fatalf("launchctl bootstrap %s %s: %v\n%s", domain, plist, err, out)
	}
	for deadline := time.Now().Add(within); pid == 0 && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		_, pid = launchdJob(domain, label)
	}
	return pid
}

// wantDaemonAgent fails the test unless the user agent the daemon on the home wrote keeps the daemon launchd starts
// from it on that home. launchd gives the job the user's own HOME, not the throwaway one: the agent's --home and its
// file token store keep the daemon away from the user's own HarmFlow home and login keychain.
func wantDaemonAgent(t *testing.T, written map[string]any, home string) {
	t.Helper()
	args, _ := written["ProgramArguments"].([]any)
	if i := slices.Index(args, any("--home")); i < 1 || i+1 >= len(args) || args[i+1] != home {
		t.Fatalf("the agent's ProgramArguments are %v, want --home %s", args, home)
	}
	if env, _ := written["EnvironmentVariables"].(map[string]any); env["HARMFLOW_TOKEN_STORE"] != "file" {
		t.Fatalf("the agent's EnvironmentVariables are %v, want HARMFLOW_TOKEN_STORE=file", env)
	}
}

// launchdDaemon is the daemon launchd started on the home as its job's process, pid, once it has written its address
// and token. It fails the test unless that daemon's GET /health names the pid, launchd's own.
func launchdDaemon(t *testing.T, home string, pid int) *daemon {
	t.Helper()
	base, token := awaitRuntime(home, runtimeWait)
	if base == "" {
		t.Fatalf("the daemon launchd started, pid %d, wrote no runtime file within %v\nits log ends:\n%s", pid,
			runtimeWait, daemonLogTail(home))
	}
	started := &daemon{t: t, home: home, base: base, token: token}
	if got, _ := started.call("GET", "/health", nil, 200)["pid"].(float64); int(got) != pid {
		t.Fatalf("GET /health of the daemon launchd started names the pid %v, want %d, launchd's own", got, pid)
	}
	return started
}

// The daemon launchd starts from the user agent HarmFlow writes runs in the user's context, without elevated
// privileges, as one started by hand does (TestDaemonRunsAsTheUser). A daemon of the test's own, on a home and a HOME
// of its own, writes its agent when autostart is turned on through the local API. The test stops it, copies the agent
// with only its label changed to one of the run's own, and has launchd load the copy into the user's GUI domain, as a
// login loads the agent, so launchd starts harmflowd with the arguments HarmFlow wrote. The user's own agent, its job,
// and ~/Library/LaunchAgents stay as they are, and the job the test loaded is booted out whether it passes or fails.
func TestLaunchdStartedDaemonRunsAsTheUser(t *testing.T) {
	domain := realLaunchd(t)
	if os.Getuid() == 0 {
		t.Fatal("the test process runs as root, so a daemon that keeps its user still runs with elevated privileges: " +
			"run the tests as a user that is not root")
	}
	keepOwnSetup(t, domain, userAgentLabel)

	home, userHome := t.TempDir(), t.TempDir()
	// The daemon's HOME alone: its user agent lies under the throwaway HOME, never in the user's own LaunchAgents.
	d := startDaemon(t, home, "HOME="+userHome)
	agent := filepath.Join(userHome, "Library", "LaunchAgents", userAgentLabel+".plist")
	a, _ := d.call("PATCH", "/settings", map[string]any{"autostart": true}, 200)["autostart"].(map[string]any)
	if a["enabled"] != true || a["mechanism"] != "launchd" || a["plistPath"] != agent {
		t.Fatalf("PATCH /settings {autostart: true} reads autostart %v, want on, through launchd, at %s", a, agent)
	}
	written := plistJSON(t, agent)
	wantDaemonAgent(t, written, home)
	d.stop()
	if err := os.Remove(filepath.Join(home, "run", "daemon.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	label := testLabel("launchd")
	copied := copyAgent(t, agent, label)
	want := maps.Clone(written)
	want["Label"] = label
	if got := plistJSON(t, copied); !reflect.DeepEqual(got, want) {
		t.Fatalf("the copied agent reads %v, want the agent HarmFlow wrote with only its Label changed: %v", got, want)
	}

	pid := loadJob(t, domain, copied, label, runtimeWait)
	if pid == 0 {
		t.Fatalf("launchd started no process for %s/%s within %v\nthe daemon's log ends:\n%s", domain, label,
			runtimeWait, daemonLogTail(home))
	}
	launchdDaemon(t, home, pid)
	wantUserIDs(t, pid)
}

// waitAgents waits until each of the files, user agents being written, reads as a whole property list, and fails the
// test, listing the folder of the first, when one does not within ten seconds.
func waitAgents(t *testing.T, files ...string) {
	t.Helper()
	for _, file := range files {
		for deadline := time.Now().Add(10 * time.Second); exec.Command("/usr/bin/plutil", "-lint", file).Run() != nil; time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("no whole property list at %s within 10s; the folder holds %v", file,
					launchAgents(t, filepath.Dir(files[0])))
			}
		}
	}
}

// end stops a shell the test started, as startShell's cleanup does, before the test goes on.
func (s *shell) end() {
	s.t.Helper()
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.exited:
	case <-time.After(10 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.exited
	}
}

// launchdShell is the shell launchd started as its job's process, pid, driven through its test control channel: the
// commands go to in, the named pipe the job's StandardInPath names, and the answers are read from out, the file its
// StandardOutPath names. The shell counts as exited once its process is gone. Both of its goroutines stop when the
// test ends.
func launchdShell(t *testing.T, in *os.File, out string, pid int) *shell {
	t.Helper()
	s := &shell{t: t, in: in, answers: make(chan map[string]any, 1), exited: make(chan struct{})}
	done := make(chan struct{})
	tailed, watched := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		close(done)
		<-tailed
		<-watched
	})
	// pause waits a moment, and says false once the test has ended.
	pause := func() bool {
		select {
		case <-done:
			return false
		case <-time.After(50 * time.Millisecond):
			return true
		}
	}
	go func() {
		defer close(tailed)
		f, err := os.Open(out)
		for ; err != nil; f, err = os.Open(out) {
			if !pause() {
				return
			}
		}
		defer f.Close()
		// The shell may print lines of its own: only a JSON object is an answer. A line still being written is kept
		// until its end arrives.
		r := bufio.NewReader(f)
		var line []byte
		for {
			chunk, err := r.ReadBytes('\n')
			line = append(line, chunk...)
			if err != nil {
				if !pause() {
					return
				}
				continue
			}
			var answer map[string]any
			if json.Unmarshal(line, &answer) == nil {
				select {
				case s.answers <- answer:
				case <-done:
					return
				}
			}
			line = nil
		}
	}()
	go func() {
		defer close(watched)
		for syscall.Kill(pid, 0) == nil {
			if !pause() {
				return
			}
		}
		close(s.exited)
	}()
	return s
}

// harmflow-app#953: with autostart and the tray on, the two user agents a sign-in loads — the daemon's and the login
// item's — leave both the daemon and the tray process running, with no window open. A daemon of the test's own runs on
// a home and a throwaway HOME of its own, and the shell's debug build, started on the same HOME, turns autostart on as
// a click on its Settings toggle does: the daemon writes its agent, and the shell's autostart plugin writes the login
// item's, both under that HOME. The test stops both, copies each agent beside it under a label unique to the run, and
// has launchd load the copies into the user's GUI domain, as a sign-in loads them. The daemon's copy differs from its
// agent only in its Label. The login item's keeps its program and arguments, --hidden included, and adds launchd's own
// keys: a HOME and a HarmFlow home of the test's own, so the shell reaches the test's daemon and never the user's, its
// test control channel, and a named pipe and a file that carry the channel's commands and answers. The daemon launchd
// starts must answer GET /health with launchd's pid; the shell launchd starts must run the login item's program and
// arguments, keep its window hidden with the tray shown, and show a task only the test's daemon holds. The user's own
// agents, their jobs, and ~/Library/LaunchAgents stay as they are, and the jobs the test loaded are booted out, their
// processes ended, whether it passes or fails. The run spends no usage and starts no harness.
func TestLoginAgentsStartTheDaemonAndAHiddenTray(t *testing.T) {
	domain := realLaunchd(t)
	bin := realDesktop(t)
	if os.Getuid() == 0 {
		t.Fatal("the test process runs as root, so the jobs it loads would not be the user's: run the tests as a user " +
			"that is not root")
	}
	keepOwnSetup(t, domain, userAgentLabel, loginItemLabel)

	home, userHome := t.TempDir(), t.TempDir()
	d := startDaemon(t, home, "HOME="+userHome)
	// The texts the test reads are the interface's in en-US, whatever the system's language, on the shell's own screens
	// rather than first use's.
	d.call("PATCH", "/settings", map[string]any{"language": "en-US", "onboardingCompleted": true, "trayEnabled": true}, 200)
	serveDashboard(t, d)
	writer := startShellWith(t, bin, d, []string{"HOME=" + userHome})
	writer.waitState(true, true)
	writer.click(navSettings)
	writer.click("#settings-group-computer + .settings-nav__item")
	writer.waitShown(".app__content", "Start the daemon automatically with the system")
	writer.click("#settings-autostart")

	agentsDir := filepath.Join(userHome, "Library", "LaunchAgents")
	daemonAgent := filepath.Join(agentsDir, userAgentLabel+".plist")
	loginItem := filepath.Join(agentsDir, loginItemLabel+".plist")
	waitAgents(t, daemonAgent, loginItem)
	if got := slices.Sorted(maps.Keys(launchAgents(t, agentsDir))); !slices.Equal(got, []string{loginItemLabel + ".plist", userAgentLabel + ".plist"}) {
		t.Fatalf("the throwaway LaunchAgents hold %v, want the daemon's agent and the login item's alone", got)
	}
	writtenDaemon := plistJSON(t, daemonAgent)
	wantDaemonAgent(t, writtenDaemon, home)
	writtenItem := plistJSON(t, loginItem)
	program, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	if args, _ := writtenItem["ProgramArguments"].([]any); writtenItem["Label"] != loginItemLabel ||
		!slices.Equal(args, []any{program, "--hidden"}) {
		t.Fatalf("the login item's agent reads %v, want the label %s and the arguments [%s --hidden]", writtenItem,
			loginItemLabel, program)
	}
	writer.end()
	d.stop()
	if err := os.Remove(filepath.Join(home, "run", "daemon.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	daemonLabel := testLabel("launchd-daemon")
	daemonCopy := copyAgent(t, daemonAgent, daemonLabel)
	want := maps.Clone(writtenDaemon)
	want["Label"] = daemonLabel
	if got := plistJSON(t, daemonCopy); !reflect.DeepEqual(got, want) {
		t.Fatalf("the daemon agent's copy reads %v, want the agent the daemon wrote with only its Label changed: %v", got,
			want)
	}
	daemonPID := loadJob(t, domain, daemonCopy, daemonLabel, runtimeWait)
	if daemonPID == 0 {
		t.Fatalf("launchd started no process for %s/%s within %v\nthe daemon's log ends:\n%s", domain, daemonLabel,
			runtimeWait, daemonLogTail(home))
	}
	started := launchdDaemon(t, home, daemonPID)

	channel := t.TempDir()
	commands, answers := filepath.Join(channel, "commands"), filepath.Join(channel, "answers")
	if err := syscall.Mkfifo(commands, 0o600); err != nil {
		t.Fatal(err)
	}
	// Open for reading and writing, so that launchd's open of the pipe for the shell's standard input finds a writer
	// and does not wait for one. Closed only once the shell's job is booted out: its end of file would end the channel.
	in, err := os.OpenFile(commands, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close() })
	env := map[string]any{"HOME": userHome, "HARMFLOW_HOME": home, "HARMFLOW_TEST_SHELL_CONTROL": "1"}
	itemLabel := testLabel("launchd-shell")
	itemCopy := copyAgent(t, loginItem, itemLabel,
		[]string{"-insert", "EnvironmentVariables", "-json", mustJSON(env)},
		[]string{"-insert", "StandardInPath", "-string", commands},
		[]string{"-insert", "StandardOutPath", "-string", answers})
	want = maps.Clone(writtenItem)
	want["Label"] = itemLabel
	want["EnvironmentVariables"] = env
	want["StandardInPath"] = commands
	want["StandardOutPath"] = answers
	if got := plistJSON(t, itemCopy); !reflect.DeepEqual(got, want) {
		t.Fatalf("the login item's copy reads %v, want the agent the plugin wrote with only its Label and launchd's "+
			"environment and standard input and output changed: %v", got, want)
	}
	shellPID := loadJob(t, domain, itemCopy, itemLabel, runtimeWait)
	if shellPID == 0 {
		t.Fatalf("launchd started no process for %s/%s within %v", domain, itemLabel, runtimeWait)
	}
	// -ww: the arguments whole, however wide, as no terminal is there to cut them to.
	ran, err := exec.Command("/bin/ps", "-ww", "-p", strconv.Itoa(shellPID), "-o", "args=").Output()
	if err != nil {
		t.Fatalf("ps -p %d: %v", shellPID, err)
	}
	if got := strings.TrimSpace(string(ran)); got != program+" --hidden" {
		t.Fatalf("the process launchd started for %s runs %q, want the login item's %q", itemLabel, got,
			program+" --hidden")
	}

	s := launchdShell(t, in, answers, shellPID)
	s.waitState(false, true)
	s.keepsHidden(2 * time.Second)
	// A task only the test's daemon holds: a shell connected to any other daemon never shows it.
	id := fmt.Sprintf("login-%x", time.Now().UnixNano())
	started.call("POST", "/tasks", map[string]any{"yaml": task(id, "SCENARIO=ok", "")}, 201)
	s.waitListening()
	s.click(navTasks)
	s.waitShown(".task-row", "Task "+id)
	s.keepsHidden(2 * time.Second)

	if _, pid := launchdJob(domain, daemonLabel); pid != daemonPID {
		t.Errorf("launchd's job %s reads pid %d once the shell runs, want the daemon's %d still running", daemonLabel,
			pid, daemonPID)
	}
	if _, pid := launchdJob(domain, itemLabel); pid != shellPID {
		t.Errorf("launchd's job %s reads pid %d at the end, want the shell's %d still running", itemLabel, pid, shellPID)
	}
}
