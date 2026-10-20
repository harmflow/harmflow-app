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
	"sync"
	"syscall"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

type Process struct {
	cmd      *exec.Cmd
	done     chan struct{}
	exitCode int
	stderr   *tail
	mu       sync.Mutex
	stopping bool
}

// Start launches the invocation; onLine receives each stdout line, raw receives stdout and stderr verbatim.
func Start(inv ports.Invocation, raw io.Writer, onLine func([]byte)) (*Process, error) {
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
	var errw io.Writer = p.stderr
	if raw != nil {
		errw = io.MultiWriter(p.stderr, raw)
	}
	cmd.Stderr = errw
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		r := bufio.NewReaderSize(stdout, 256*1024)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				if raw != nil {
					_, _ = raw.Write(line)
				}
				onLine(bytes.TrimRight(line, "\r\n"))
			}
			if err != nil {
				break
			}
		}
		werr := cmd.Wait()
		p.exitCode = exitCode(werr)
		close(p.done)
	}()
	return p, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return -1
}

// Done is closed when the process has exited and its output is drained.
func (p *Process) Done() <-chan struct{} { return p.done }

func (p *Process) ExitCode() int { return p.exitCode }

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

// Output runs a short probe command with a timeout.
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
	err := cmd.Run()
	return out.String(), errb.String(), err
}
