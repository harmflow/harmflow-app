package procutil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// helperVariable makes the test binary stand in for a program that is not an Apple platform binary, whose environment
// KERN_PROCARGS2 returns: "daemonize" starts a daemon and exits, the first half of a double fork; "daemon" ignores
// SIGTERM and sleeps.
const helperVariable = "PROCUTIL_HELPER"

func TestMain(m *testing.M) {
	switch os.Getenv(helperVariable) {
	case "daemonize":
		// The daemon gets a session of its own and /dev/null for its output; once this process exits, launchd adopts it.
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), helperVariable+"=daemon")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if cmd.Start() != nil || writePid(os.Getenv("PROCUTIL_PIDFILE"), cmd.Process.Pid) != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "daemon":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}
	// The readers of a process's output are goroutines; none may outlive the test that started it.
	goleak.VerifyTestMain(m)
}

// writePid writes the file whole, so a reader never sees part of the pid.
func writePid(path string, pid int) error {
	if err := os.WriteFile(path+".tmp", []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// Cancelling ends the whole process tree after the grace period, even a child that ignores SIGTERM.
func TestStopKillsTheProcessTree(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	script := `trap '' TERM; sh -c 'trap "" TERM; sleep 300' & echo $! > ` + pidFile + `; echo started; wait`
	var lines []string
	p, err := Start(ports.Invocation{Path: "/bin/sh", Args: []string{"-c", script}, Env: os.Environ()}, nil,
		func(b []byte) { lines = append(lines, string(b)) })
	if err != nil {
		t.Fatal(err)
	}
	// A loaded host can take seconds to run a shell: wait for the pid while the shell lives, up to a ceiling.
	var child int
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if data, err := os.ReadFile(pidFile); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			break
		}
		select {
		case <-p.Done():
			t.Fatalf("the shell exited before starting the child, exit %d: %q", p.ExitCode(), p.StderrTail())
		default:
		}
	}
	if child == 0 {
		t.Fatal("the child did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	p.Wait(ctx, 300*time.Millisecond)
	if time.Since(start) > 5*time.Second {
		t.Fatal("stopping took too long")
	}
	// The killed child is gone once it has been reaped, which a loaded host can take a while to do.
	for deadline := time.Now().Add(10 * time.Second); syscall.Kill(child, 0) == nil && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(child, 0); err == nil {
		t.Errorf("child %d survived", child)
	}
	if len(lines) == 0 || lines[0] != "started" {
		t.Errorf("stdout lines: %v", lines)
	}
}

// Both streams reach raw whole, one write at a time, Start's stderr too, though it has no callback.
func TestRawGetsOneWriteAtATime(t *testing.T) {
	script := `i=0; while [ $i -lt 500 ]; do echo out $i; echo err $i >&2; i=$((i+1)); done`
	var raw bytes.Buffer
	p, err := Start(ports.Invocation{Path: "/bin/sh", Args: []string{"-c", script}}, &raw, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	if out, errs := strings.Count(raw.String(), "out "), strings.Count(raw.String(), "err "); out != 500 || errs != 500 {
		t.Errorf("raw has %d stdout and %d stderr lines, want 500 each", out, errs)
	}
}

// Every line of both streams is recorded at one point, so each stream keeps its order and the raw capture holds the
// lines in the order the callbacks got them.
func TestStreamsAreRecordedInOneOrder(t *testing.T) {
	const n = 500
	script := `i=0; while [ $i -lt ` + strconv.Itoa(n) + ` ]; do echo out $i; echo err $i >&2; i=$((i+1)); done`
	var raw bytes.Buffer
	var mu sync.Mutex
	var got []string
	record := func(b []byte) {
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
	}
	p, err := StartLines(ports.Invocation{Path: "/bin/sh", Args: []string{"-c", script}}, &raw, record, record)
	if err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	captured := strings.Split(strings.TrimSuffix(raw.String(), "\n"), "\n")
	if len(got) != 2*n || len(captured) != 2*n {
		t.Fatalf("%d lines to the callbacks and %d in the raw capture, want %d each", len(got), len(captured), 2*n)
	}
	next := map[string]int{}
	for i, line := range got {
		stream, number, _ := strings.Cut(line, " ")
		if number != strconv.Itoa(next[stream]) {
			t.Fatalf("line %d is %q, want %s %d: a stream lost its order", i, line, stream, next[stream])
		}
		next[stream]++
		if captured[i] != line {
			t.Fatalf("line %d is %q for the callbacks and %q in the raw capture", i, line, captured[i])
		}
	}
}

// A probe returns once its timeout ends even when a child of the command keeps its output open, as the native binary
// an npm wrapper starts can; otherwise a harness's check would never end.
func TestOutputReturnsWhenAChildKeepsThePipeOpen(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	script := `sleep 300 & echo $! > ` + pidFile + `; echo started; wait`
	// A loaded host can take seconds to run a shell, so the timeout grows until the child is up when it ends.
	for timeout := 500 * time.Millisecond; ; timeout *= 2 {
		done := make(chan error, 1)
		start := time.Now()
		go func() {
			_, _, err := Output(context.Background(), timeout, nil, "/bin/sh", "-c", script)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("the probe outlived its timeout with no error")
			}
		case <-time.After(timeout + 30*time.Second):
			t.Fatalf("the probe did not return %v after its %v timeout", time.Since(start)-timeout, timeout)
		}
		if _, err := os.Stat(pidFile); err == nil {
			return
		}
		if timeout > time.Minute {
			t.Fatal("the shell never started the child")
		}
	}
}

// A probe that exits by itself succeeds with what it printed, even when a child it left keeps the output open.
func TestOutputOfAProbeThatLeavesAChild(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	done := make(chan struct{})
	var out string
	var err error
	go func() {
		defer close(done)
		out, _, err = Output(context.Background(), 3*time.Minute, nil, "/bin/sh", "-c", `sleep 300 & echo $! > `+pidFile+`; echo 1.2.3`)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("the probe waited for the child it left")
	}
	if err != nil || strings.TrimSpace(out) != "1.2.3" {
		t.Errorf("out %q, err %v", out, err)
	}
}

func TestExitCode(t *testing.T) {
	p, err := Start(ports.Invocation{Path: "/bin/sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"}}, nil, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	if p.ExitCode() != 3 || !strings.Contains(p.StderrTail(), "err") {
		t.Errorf("exit %d stderr %q", p.ExitCode(), p.StderrTail())
	}
}

// testEnv is the test's environment with none of HarmFlow's variables, which it has when it runs inside a HarmFlow
// execution, and with extra added.
func testEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HARMFLOW_") && !strings.HasPrefix(kv, helperVariable+"=") {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

// executionID is an id that no other run of these tests uses, even one running at the same time.
func executionID(t *testing.T) string {
	return fmt.Sprintf("procutil-%s-%d-%d", t.Name(), os.Getpid(), time.Now().UnixNano())
}

func needProcessTable(t *testing.T) {
	t.Helper()
	if _, err := processTable(); err != nil {
		t.Skip(err)
	}
}

// startShell starts script with /bin/sh as the harness; args are $1 and on.
func startShell(t *testing.T, script string, env []string, id string, onLine func([]byte), args ...string) *Process {
	t.Helper()
	if onLine == nil {
		onLine = func([]byte) {}
	}
	p, err := Start(ports.Invocation{Path: "/bin/sh", Args: append([]string{"-c", script, "sh"}, args...), Env: env,
		ExecutionID: id}, nil, onLine)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.Stop(0)
		<-p.Done()
	})
	return p
}

// waitFor polls cond up to a ceiling that a loaded host needs.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitForPid reads the pid the harness's tree wrote to path, and knows the process by it and its start time; with p
// given, the harness must not exit first. The process is killed when the test ends, whatever the test found.
func waitForPid(t *testing.T, p *Process, path string) ident {
	t.Helper()
	var id ident
	waitFor(t, "a pid in "+filepath.Base(path), func() bool {
		if p != nil {
			select {
			case <-p.Done():
				t.Fatalf("the harness exited first, exit %d: %q", p.ExitCode(), p.StderrTail())
			default:
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return false
		}
		info, ok := processInfo(pid)
		id = ident{pid, info.start}
		return ok
	})
	t.Cleanup(func() {
		if running(id) {
			_ = syscall.Kill(id.pid, syscall.SIGKILL)
		}
	})
	return id
}

// cancelAndWait cancels the execution and returns how long Wait took.
func cancelAndWait(p *Process, grace time.Duration) time.Duration {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	p.Wait(ctx, grace)
	return time.Since(start)
}

func endedOne(ended []Ended, pid int) (Ended, bool) {
	for _, e := range ended {
		if e.Pid == pid {
			return e, true
		}
	}
	return Ended{}, false
}

// A descendant that left the group and the session, ignores SIGTERM, and redirects its output is ended, though the
// harness exits on SIGTERM first. The snapshot taken before SIGTERM, while its parent ran, keeps it.
func TestStopEndsADescendantThatLeftTheGroup(t *testing.T) {
	needProcessTable(t)
	pidFile := t.TempDir() + "/escaped.pid"
	// macOS has no setsid command; Perl's POSIX::setsid stands in for it.
	script := `perl -e 'use POSIX; POSIX::setsid() or die; $SIG{TERM} = "IGNORE"; ` +
		`open STDOUT, ">/dev/null"; open STDERR, ">/dev/null"; ` +
		`open F, ">", "$ARGV[0].tmp"; print F $$; close F; rename "$ARGV[0].tmp", $ARGV[0]; ` +
		`exec "sleep", "300"' "$1" & echo started; wait`
	p := startShell(t, script, testEnv(), executionID(t), nil, pidFile)
	escaped := waitForPid(t, p, pidFile)
	waitFor(t, "the escaped process to run sleep in a session of its own", func() bool {
		info, ok := processInfo(escaped.pid)
		return ok && info.name == "sleep" && info.pgid == escaped.pid
	})
	grace := 300 * time.Millisecond
	if took := cancelAndWait(p, grace); took < grace {
		t.Errorf("Wait took %v: the grace period did not apply to the descendant that ignores SIGTERM", took)
	}
	if running(escaped) {
		t.Errorf("the descendant %d that left the group survived", escaped.pid)
	}
	if e, ok := endedOne(p.Ended(), escaped.pid); !ok || e.Executable != "sleep" || !e.OutsideGroup {
		t.Errorf("ended %+v, want pid %d, sleep, outside the group", p.Ended(), escaped.pid)
	}
	if e, ok := endedOne(p.Ended(), p.Pid()); !ok || e.OutsideGroup {
		t.Errorf("ended %+v, want the harness %d, in its group", p.Ended(), p.Pid())
	}
}

// A descendant that daemonized with a double fork, so launchd adopted it before the cancel, is found by the execution's
// id in its environment, and ended. The test binary stands in for it: unlike an Apple platform binary's, its
// environment can be read.
func TestStopEndsADaemonThatCarriesTheExecutionID(t *testing.T) {
	needProcessTable(t)
	id := executionID(t)
	pidFile := t.TempDir() + "/daemon.pid"
	env := testEnv(executionIDVariable+"="+id, helperVariable+"=daemonize", "PROCUTIL_PIDFILE="+pidFile)
	p := startShell(t, `"$1"; echo started; sleep 300`, env, id, nil, os.Args[0])
	daemon := waitForPid(t, p, pidFile)
	waitFor(t, "launchd to adopt the daemon", func() bool {
		info, ok := processInfo(daemon.pid)
		return ok && info.ppid == 1
	})
	cancelAndWait(p, 300*time.Millisecond)
	if running(daemon) {
		t.Errorf("the daemon %d that carries the execution's id survived", daemon.pid)
	}
	name := filepath.Base(os.Args[0])
	if len(name) > 16 {
		name = name[:16]
	}
	if e, ok := endedOne(p.Ended(), daemon.pid); !ok || e.Executable != name || !e.OutsideGroup {
		t.Errorf("ended %+v, want pid %d, %s, outside the group", p.Ended(), daemon.pid, name)
	}
}

// A process outside the tree that carries another execution's id keeps running, even one whose id begins with this
// execution's. This execution's own daemon shows that the scan ran.
func TestStopLeavesAnotherExecutionsProcess(t *testing.T) {
	needProcessTable(t)
	id := executionID(t)
	var others []ident
	for _, other := range []string{id + "0", "other-" + id} {
		cmd := exec.Command(os.Args[0])
		cmd.Env = testEnv(executionIDVariable+"="+other, helperVariable+"=daemon")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		info, _ := processInfo(cmd.Process.Pid)
		others = append(others, ident{cmd.Process.Pid, info.start})
	}
	pidFile := t.TempDir() + "/daemon.pid"
	env := testEnv(executionIDVariable+"="+id, helperVariable+"=daemonize", "PROCUTIL_PIDFILE="+pidFile)
	p := startShell(t, `"$1"; echo started; sleep 300`, env, id, nil, os.Args[0])
	ours := waitForPid(t, p, pidFile)
	waitFor(t, "launchd to adopt the daemon", func() bool {
		info, ok := processInfo(ours.pid)
		return ok && info.ppid == 1
	})
	cancelAndWait(p, 300*time.Millisecond)
	if running(ours) {
		t.Errorf("this execution's daemon %d survived", ours.pid)
	}
	for _, o := range others {
		if !running(o) {
			t.Errorf("process %d, which carries another execution's id, was ended", o.pid)
		}
		if _, ok := endedOne(p.Ended(), o.pid); ok {
			t.Errorf("ended %+v names process %d, which carries another execution's id", p.Ended(), o.pid)
		}
	}
}

// A process that keeps the harness's stdout, and that the search for the execution's processes cannot find, holds up
// Wait only up to outputDelay after a cancel. It daemonizes with a double fork, so launchd adopts it, and clears the
// execution's id before it becomes an Apple platform binary: macOS hides such a binary's environment only while System
// Integrity Protection is on, which a CI host may turn off.
func TestWaitIsBoundedWhileAStrayKeepsTheOutput(t *testing.T) {
	needProcessTable(t)
	pidFile := t.TempDir() + "/stray.pid"
	script := `perl -e 'use POSIX; exit 0 if fork; POSIX::setsid(); exit 0 if fork; ` +
		`open F, ">", "$ARGV[0].tmp"; print F $$; close F; rename "$ARGV[0].tmp", $ARGV[0]; ` +
		`delete $ENV{HARMFLOW_EXECUTION_ID}; exec "sleep", "300"' "$1"; echo started; sleep 300`
	var lines []string
	id := executionID(t)
	p := startShell(t, script, testEnv(executionIDVariable+"="+id), id, func(b []byte) { lines = append(lines, string(b)) },
		pidFile)
	stray := waitForPid(t, p, pidFile)
	waitFor(t, "launchd to adopt the stray, running sleep", func() bool {
		info, ok := processInfo(stray.pid)
		return ok && info.ppid == 1 && info.name == "sleep"
	})
	grace := 300 * time.Millisecond
	took := cancelAndWait(p, grace)
	if !running(stray) {
		t.Fatalf("the stray %d was ended, so the bound went untested", stray.pid)
	}
	if took < outputDelay {
		t.Errorf("Wait took %v, less than the bound: the stray did not keep the output", took)
	}
	// The slack is for a loaded host; without the bound, Wait returns only when the stray exits.
	if limit := grace + outputDelay + 5*time.Second; took > limit {
		t.Errorf("Wait took %v, more than %v", took, limit)
	}
	if len(lines) == 0 || lines[0] != "started" {
		t.Errorf("stdout lines: %v", lines)
	}
}

// A harness that exits on its own leaves no process running once Wait returns, even a background one that redirects
// its output and so holds nothing up. It is named as one the harness left running, in its group; the harness, which
// exited by itself, is not named.
func TestWaitEndsWhatTheHarnessLeftRunning(t *testing.T) {
	needProcessTable(t)
	pidFile := t.TempDir() + "/left.pid"
	p := startShell(t, `sleep 300 >/dev/null 2>&1 & echo $! > "$1"; echo done`, testEnv(), executionID(t), nil, pidFile)
	<-p.Done()
	left := waitForPid(t, nil, pidFile)
	// The shell writes $! once it has forked, before the child runs sleep, and Wait keeps the name it first finds.
	waitFor(t, "the background process to run sleep", func() bool {
		info, ok := processInfo(left.pid)
		return ok && info.start == left.start && info.name == "sleep"
	})
	p.Wait(context.Background(), 300*time.Millisecond)
	if running(left) {
		t.Errorf("the background process %d the harness left survived Wait", left.pid)
	}
	if e, ok := endedOne(p.Ended(), left.pid); !ok || e.Executable != "sleep" || !e.LeftRunning || e.OutsideGroup {
		t.Errorf("ended %+v, want pid %d, sleep, left running, in the group", p.Ended(), left.pid)
	}
	if _, ok := endedOne(p.Ended(), p.Pid()); ok {
		t.Errorf("ended %+v names the harness %d, which exited by itself", p.Ended(), p.Pid())
	}
}

// Once the harness has exited on its own, launchd has adopted what it left in a session of its own, so no snapshot of
// its descendants finds it. One that carries the execution's id is found by the scan and ended, after the grace period
// since it ignores SIGTERM; the test binary stands in for it, as its environment can be read. One that cleared the id
// survives, as EndTree's known limit says.
func TestWaitAfterTheHarnessExitedEndsOnlyWhatCarriesTheID(t *testing.T) {
	needProcessTable(t)
	id := executionID(t)
	dir := t.TempDir()
	daemonFile, strayFile := dir+"/daemon.pid", dir+"/stray.pid"
	env := testEnv(executionIDVariable+"="+id, helperVariable+"=daemonize", "PROCUTIL_PIDFILE="+daemonFile)
	// The stray leaves the group and the session, and clears the execution's id, before it becomes sleep: macOS hides an
	// Apple platform binary's environment only while System Integrity Protection is on, which a CI host may turn off.
	script := `"$1"; perl -e 'use POSIX; POSIX::setsid() or die; open STDOUT, ">/dev/null"; open STDERR, ">/dev/null"; ` +
		`open F, ">", "$ARGV[0].tmp"; print F $$; close F; rename "$ARGV[0].tmp", $ARGV[0]; ` +
		`delete $ENV{HARMFLOW_EXECUTION_ID}; exec "sleep", "300"' "$2" & echo done`
	p := startShell(t, script, env, id, nil, os.Args[0], strayFile)
	<-p.Done()
	daemon := waitForPid(t, nil, daemonFile)
	stray := waitForPid(t, nil, strayFile)
	waitFor(t, "launchd to adopt the daemon and the stray, running sleep in a session of its own", func() bool {
		d, dok := processInfo(daemon.pid)
		s, sok := processInfo(stray.pid)
		return dok && d.ppid == 1 && sok && s.ppid == 1 && s.name == "sleep" && s.pgid == stray.pid
	})
	p.Wait(context.Background(), 300*time.Millisecond)
	if running(daemon) {
		t.Errorf("the daemon %d that carries the execution's id survived Wait", daemon.pid)
	}
	if e, ok := endedOne(p.Ended(), daemon.pid); !ok || !e.LeftRunning || !e.OutsideGroup {
		t.Errorf("ended %+v, want pid %d, left running, outside the group", p.Ended(), daemon.pid)
	}
	if !running(stray) {
		t.Errorf("the stray %d was ended, though nothing can find it: EndTree's known limit no longer says what survives",
			stray.pid)
	}
}
