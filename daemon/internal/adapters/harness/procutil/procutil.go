// Package procutil runs one harness process in its own process group, streams its stdout line by line, keeps the
// tail of its stderr, and ends the whole group on cancellation: a graceful SIGTERM, then SIGKILL after the grace
// period, so no orphan survives (FS-003 FR-005).
package procutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

type Process struct {
	cmd      *exec.Cmd
	done     chan struct{}
	exitCode int
	signal   string
	stderr   *tail
	mu       sync.Mutex
	stopping bool
}

// Start launches the invocation; onLine receives each stdout line, raw receives stdout and stderr verbatim.
func Start(inv ports.Invocation, raw io.Writer, onLine func([]byte)) (*Process, error) {
	return StartLines(inv, raw, onLine, nil)
}

// StartLines is Start with stderr read line by line too: onStderr, when given, receives each stderr line, and the
// tail is still kept. One goroutine records every line, so raw and the callbacks hold the lines in one order: each
// stream's own, and across the two the order they arrived (FS-013 FR-008, D-112). The process counts as done once
// both streams are drained and recorded.
func StartLines(inv ports.Invocation, raw io.Writer, onStdout, onStderr func([]byte)) (*Process, error) {
	cmd := exec.Command(inv.Path, inv.Args...)
	cmd.Dir = inv.Dir
	cmd.Env = inv.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if inv.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(inv.Stdin)
	} else {
		devnull, err := os.Open(os.DevNull)
		if err != nil {
			return nil, err
		}
		cmd.Stdin = devnull
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &Process{cmd: cmd, done: make(chan struct{}), stderr: &tail{max: 64 * 1024}}
	if raw != nil {
		// The recorder below and os/exec's stderr copy write from their own goroutines.
		raw = &lockedWriter{w: raw}
	}
	var stderr io.ReadCloser
	if onStderr != nil {
		if stderr, err = cmd.StderrPipe(); err != nil {
			return nil, err
		}
	} else if raw != nil {
		cmd.Stderr = io.MultiWriter(p.stderr, raw)
	} else {
		cmd.Stderr = p.stderr
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	lines := make(chan streamLine, 64)
	var readers sync.WaitGroup
	read := func(r io.Reader, stderr bool) {
		readers.Add(1)
		go func() {
			defer readers.Done()
			readLines(r, func(line []byte) { lines <- streamLine{text: line, stderr: stderr} })
		}()
	}
	read(stdout, false)
	if stderr != nil {
		read(stderr, true)
	}
	recorded := make(chan struct{})
	go func() {
		defer close(recorded)
		for l := range lines {
			if raw != nil {
				_, _ = raw.Write(l.text)
			}
			on := onStdout
			if l.stderr {
				_, _ = p.stderr.Write(l.text)
				on = onStderr
			}
			on(bytes.TrimRight(l.text, "\r\n"))
		}
	}()
	go func() {
		// os/exec closes the pipes in Wait, so it is called only once every read is done.
		readers.Wait()
		close(lines)
		<-recorded
		werr := cmd.Wait()
		p.exitCode, p.signal = exitStatus(werr)
		close(p.done)
	}()
	return p, nil
}

// streamLine is one line a reader took from the process, its line ending included, and the stream it came from.
type streamLine struct {
	text   []byte
	stderr bool
}

// readLines hands each line of r to emit, its line ending included. Each line is a slice of its own.
func readLines(r io.Reader, emit func([]byte)) {
	br := bufio.NewReaderSize(r, 256*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			emit(line)
		}
		if err != nil {
			return
		}
	}
}

// exitStatus is the exit code — 128 plus the signal's number for a process a signal ended, as a shell reports it —
// and the signal's name, empty when the process exited by itself.
func exitStatus(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), signalName(ws.Signal())
		}
		return ee.ExitCode(), ""
	}
	return -1, ""
}

var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT", syscall.SIGILL: "SIGILL",
	syscall.SIGTRAP: "SIGTRAP", syscall.SIGABRT: "SIGABRT", syscall.SIGBUS: "SIGBUS", syscall.SIGFPE: "SIGFPE",
	syscall.SIGKILL: "SIGKILL", syscall.SIGUSR1: "SIGUSR1", syscall.SIGSEGV: "SIGSEGV", syscall.SIGUSR2: "SIGUSR2",
	syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM", syscall.SIGXCPU: "SIGXCPU",
}

func signalName(s syscall.Signal) string {
	if name, ok := signalNames[s]; ok {
		return name
	}
	return "signal " + strconv.Itoa(int(s))
}

// Done is closed when the process has exited and its output is drained.
func (p *Process) Done() <-chan struct{} { return p.done }

func (p *Process) ExitCode() int { return p.exitCode }

// Signal names the signal that ended the process, empty when it exited by itself; ExitCode is then 128 plus its number.
func (p *Process) Signal() string { return p.signal }

func (p *Process) StderrTail() string { return p.stderr.String() }

func (p *Process) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Stop asks the process group to end, and kills it after the grace period.
func (p *Process) Stop(grace time.Duration) {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return
	}
	p.stopping = true
	p.mu.Unlock()
	pgid := p.Pid()
	if pgid == 0 {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(grace):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	// Children that ignored the group signal or left the group are killed too.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// Wait waits for the end or for the context; on the context's end the group is stopped with the grace period.
func (p *Process) Wait(ctx context.Context, grace time.Duration) {
	select {
	case <-p.done:
	case <-ctx.Done():
		p.Stop(grace)
		<-p.done
	}
}

type tail struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// lockedWriter hands w one write at a time.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// LookPath finds a binary on PATH, then in the given known locations, returning every hit in order.
func LookPath(name string, pathEnv string, known []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, dir := range filepathSplitList(pathEnv) {
		add(dir + "/" + name)
	}
	for _, k := range known {
		add(k)
	}
	return out
}

func filepathSplitList(p string) []string {
	if p == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == ':' {
			if i > start {
				out = append(out, p[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// probeWaitDelay bounds how long a probe's output is still read once the probe has exited or its timeout has ended: a
// child it started, such as the native binary an npm wrapper runs, can keep the output open long after.
const probeWaitDelay = time.Second

// Output runs a short probe command with a timeout. The probe's own exit status decides: a child that keeps its output
// open is cut off after probeWaitDelay, and what was read by then is returned.
func Output(ctx context.Context, timeout time.Duration, env []string, name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	cmd.Stdin = nil
	cmd.WaitDelay = probeWaitDelay
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil // the probe succeeded; only a child it left behind held the output
	}
	return out.String(), errb.String(), err
}
