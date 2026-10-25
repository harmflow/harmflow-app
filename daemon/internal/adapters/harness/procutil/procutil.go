// Package procutil runs one harness process in its own process group, streams its stdout line by line, keeps the
// tail of its stderr, and on cancellation ends every process the harness started, inside its group or not: a graceful
// SIGTERM, then SIGKILL after the grace period, so no orphan survives.
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// outputDelay bounds the wait for the end of the process's output once the process has exited. A process it left
// running outside its group can keep the pipes open; past the bound, the output is taken as ended, so the execution
// ends, frees its task, and lets the daemon stop.
const outputDelay = 2 * time.Second

// Process is a started invocation, in a process group of its own, with its exit status and the tail of its stderr.
type Process struct {
	cmd      *exec.Cmd
	tree     Tree
	done     chan struct{}
	exitCode int
	signal   string
	stderr   *tail
	mu       sync.Mutex
	stopping bool
	ended    []Ended
}

// Start launches the invocation; onLine receives each stdout line, raw receives stdout and stderr verbatim.
func Start(inv ports.Invocation, raw io.Writer, onLine func([]byte)) (*Process, error) {
	return StartLines(inv, raw, onLine, nil)
}

// StartLines is Start with stderr read line by line too: onStderr, when given, receives each stderr line, and the
// tail is still kept. One goroutine records every line, so raw and the callbacks hold the lines in one order: each
// stream's own, and across the two the order they arrived. The process counts as done once both streams are drained
// and recorded, or once outputDelay has passed since it exited.
func StartLines(inv ports.Invocation, raw io.Writer, onStdout, onStderr func([]byte)) (*Process, error) {
	cmd := exec.Command(inv.Path, inv.Args...)
	cmd.Dir = inv.Dir
	cmd.Env = inv.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// os/exec writes stdin from its own goroutine, which a process left holding the pipe unread would block.
	cmd.WaitDelay = outputDelay
	var files []*os.File // the child's ends, closed here once it has them
	if inv.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(inv.Stdin)
	} else {
		devnull, err := os.Open(os.DevNull)
		if err != nil {
			return nil, err
		}
		cmd.Stdin = devnull
		files = append(files, devnull)
	}
	// The daemon's own pipes rather than os/exec's: os/exec closes its pipes in Wait, which could drop output not yet
	// read, and it waits for their end with no limit.
	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		closeAll(files)
		return nil, err
	}
	stderr, stderrW, err := os.Pipe()
	if err != nil {
		closeAll(append(files, stdout, stdoutW))
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = stdoutW, stderrW
	files = append(files, stdoutW, stderrW)
	err = cmd.Start()
	closeAll(files)
	if err != nil {
		closeAll([]*os.File{stdout, stderr})
		return nil, err
	}
	pid := cmd.Process.Pid
	p := &Process{cmd: cmd, done: make(chan struct{}), stderr: &tail{max: 64 * 1024},
		tree: Tree{Pid: pid, Group: pid, ExecutionID: inv.ExecutionID}}
	// Read before Wait can reap the process, while its pid is surely its own.
	if start, err := startTime(pid); err == nil {
		p.tree.Start = start
	}
	if onStderr == nil {
		onStderr = func([]byte) {}
	}
	out, errs := &pipeReader{f: stdout}, &pipeReader{f: stderr}
	// Up to 64 lines wait between the two readers and the recorder, so a burst of output does not stall a pipe while the
	// recorder runs its callbacks. When the buffer fills, the readers wait, and once the pipe's own buffer fills, so does
	// the process's write: no line is dropped.
	lines := make(chan streamLine, 64)
	var readers sync.WaitGroup
	read := func(r io.Reader, stderr bool) {
		readers.Add(1)
		go func() {
			defer readers.Done()
			readLines(r, func(line []byte) { lines <- streamLine{text: line, stderr: stderr} })
		}()
	}
	read(out, false)
	read(errs, true)
	drained := make(chan struct{})
	go func() {
		readers.Wait()
		close(drained)
	}()
	recorded := make(chan struct{})
	go func() {
		defer close(recorded)
		for l := range lines {
			if raw != nil {
				_, _ = raw.Write(l.text) // the raw capture is a copy; the line still reaches on below
			}
			on := onStdout
			if l.stderr {
				_, _ = p.stderr.Write(l.text) // a tail in memory never fails a write
				on = onStderr
			}
			on(bytes.TrimRight(l.text, "\r\n"))
		}
	}()
	go func() {
		_ = cmd.Wait() // how the process ended is read from cmd.ProcessState below
		select {
		case <-drained:
		case <-time.After(outputDelay):
			out.stop()
			errs.stop()
			<-drained
		}
		close(lines)
		<-recorded
		closeAll([]*os.File{stdout, stderr})
		p.exitCode, p.signal = exitStatus(cmd.ProcessState)
		close(p.done)
	}()
	return p, nil
}

// streamLine is one line a reader took from the process, its line ending included, and the stream it came from.
type streamLine struct {
	text   []byte
	stderr bool
}

// closeAll closes the daemon's ends of the pipes once their output is read; nothing is written to them, so a failed
// close loses nothing.
func closeAll(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// pipeReader reads the daemon's end of a pipe the process writes to. Until stop, a read waits for data, and the
// stream ends once every process holding the other end has closed it. After stop, a read takes only what the pipe
// already holds, then the stream ends: what the harness wrote before it exited is read, and a process left holding
// the other end no longer holds up the end of the output.
type pipeReader struct {
	f       *os.File
	stopped atomic.Bool
	drained int
}

// maxDrain bounds what is read after stop, in case the process left holding the pipe keeps writing to it; a pipe
// holds at most 64 KiB.
const maxDrain = 1 << 20

func (r *pipeReader) Read(b []byte) (int, error) {
	if !r.stopped.Load() {
		n, err := r.f.Read(b)
		// Only stop sets a deadline.
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			return n, err
		}
		if n > 0 {
			return n, nil
		}
	}
	if r.drained >= maxDrain || len(b) == 0 {
		return 0, io.EOF
	}
	if len(b) > maxDrain-r.drained {
		b = b[:maxDrain-r.drained]
	}
	rc, err := r.f.SyscallConn()
	if err != nil {
		return 0, io.EOF
	}
	n, rerr := 0, error(nil)
	// Control ignores the deadline, and the pipe does not block, so a read of an empty pipe returns EAGAIN at once.
	if err := rc.Control(func(fd uintptr) {
		if rerr = syscall.SetNonblock(int(fd), true); rerr != nil {
			return // a read that could block would hold up the end of the output
		}
		for {
			if n, rerr = syscall.Read(int(fd), b); rerr != syscall.EINTR {
				return
			}
		}
	}); err != nil || rerr != nil || n <= 0 {
		return 0, io.EOF
	}
	r.drained += n
	return n, nil
}

// stop ends the wait for more data, waking a read that waits.
func (r *pipeReader) stop() {
	r.stopped.Store(true)
	if err := r.f.SetReadDeadline(time.Now()); err != nil {
		_ = r.f.Close() // closing wakes the read as the deadline would; the stream ends either way
	}
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
func exitStatus(ps *os.ProcessState) (int, string) {
	if ps == nil {
		return -1, ""
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), signalName(ws.Signal())
	}
	return ps.ExitCode(), ""
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

// Done is closed when the process has exited and its output is drained, or has stayed open outputDelay past its exit.
func (p *Process) Done() <-chan struct{} { return p.done }

// ExitCode is the process's exit code, read once Done is closed.
func (p *Process) ExitCode() int { return p.exitCode }

// Signal names the signal that ended the process, empty when it exited by itself; ExitCode is then 128 plus its number.
func (p *Process) Signal() string { return p.signal }

// StderrTail returns the last 64 KiB of the process's stderr.
func (p *Process) StderrTail() string { return p.stderr.String() }

// Pid is the process's id, the id of its group too; 0 when it never started.
func (p *Process) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Stop ends the process and every process it started, with EndTree: SIGTERM, then SIGKILL after the grace period.
func (p *Process) Stop(grace time.Duration) {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return
	}
	p.stopping = true
	p.mu.Unlock()
	if p.tree.Pid == 0 {
		return
	}
	ended := EndTree(p.tree, grace)
	p.mu.Lock()
	p.ended = ended
	p.mu.Unlock()
}

// Ended lists the processes Stop ended, the harness among them, for the execution's log.
func (p *Process) Ended() []Ended {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ended
}

// Wait waits for the end or for the context; on the context's end the process is stopped with the grace period.
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

// LookPath finds a binary on PATH, then in the given known locations, returning every hit in order.
func LookPath(name string, pathEnv string, known []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if Executable(p) {
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

// Executable reports whether path names a regular file with an execute bit, a symlink to one included.
func Executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// IdentifyHarness checks that path is the harness named harness: an executable file whose --version output, which it
// returns, names it as names says, where cli is the command's own name. A file that prints a harness's name passes:
// this catches a wrong file, not an impostor.
func IdentifyHarness(ctx context.Context, env []string, path, harness, cli string, names func(string) bool) (string, error) {
	if !Executable(path) {
		return "", domain.TextErr("harness_path_not_executable", "no executable file at {{path}}", "path", path)
	}
	out, _, err := Output(ctx, 15*time.Second, env, path, "--version")
	if err != nil {
		return "", domain.TextErr("command_failed", "{{command}} failed: {{detail}}", "command", cli+" --version",
			"detail", err.Error())
	}
	if !names(out) {
		return out, domain.TextErr("harness_not_identified",
			"{{path}} is not {{harness}}: its --version output does not name it", "path", path, "harness", harness)
	}
	return out, nil
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
