package procutil

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// maxPrinted bounds what a Terminal holds of its output between two calls of Take: past it, the oldest bytes go.
const maxPrinted = 1 << 20

// Terminal is an invocation started on a pseudo-terminal of its own, as an interactive session runs: the process reads
// its keys from the terminal and draws its screens on it. The process leads a session of its own, whose controlling
// terminal is that one, so it is also its own process group.
type Terminal struct {
	cmd  *exec.Cmd
	pty  *os.File
	tree Tree
	// exited is closed once the process has exited and been reaped; read is closed once the terminal reads its end,
	// which comes when no process holds it any longer.
	exited chan struct{}
	read   chan struct{}
	// changed has a buffer of one: a send that finds it full is dropped, since the reader already has something to Take.
	changed chan struct{}

	mu      sync.Mutex
	printed []byte
}

// StartTerminal starts inv on a pseudo-terminal of rows by cols. The terminal reads what the process prints from the
// start, so nothing is lost before the first Take.
func StartTerminal(inv ports.Invocation, rows, cols uint16) (*Terminal, error) {
	cmd := exec.Command(inv.Path, inv.Args...)
	cmd.Dir = inv.Dir
	cmd.Env = inv.Env
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	t := &Terminal{cmd: cmd, pty: f, tree: Tree{Pid: pid, Group: pid, ExecutionID: inv.ExecutionID},
		exited: make(chan struct{}), read: make(chan struct{}), changed: make(chan struct{}, 1)}
	if start, err := startTime(pid); err == nil {
		t.tree.Start = start
	}
	go func() {
		defer close(t.read)
		b := make([]byte, 32*1024)
		for {
			n, err := f.Read(b)
			if n > 0 {
				t.keep(b[:n])
			}
			if err != nil {
				t.signal()
				return
			}
		}
	}()
	go func() {
		_ = cmd.Wait() // how the process ended is not needed: a session's screens say what it did
		close(t.exited)
		t.signal()
	}()
	return t, nil
}

func (t *Terminal) keep(b []byte) {
	t.mu.Lock()
	t.printed = append(t.printed, b...)
	if over := len(t.printed) - maxPrinted; over > 0 {
		t.printed = append([]byte(nil), t.printed[over:]...)
	}
	t.mu.Unlock()
	t.signal()
}

func (t *Terminal) signal() {
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

// Changed receives when the process printed something since the last Take, or exited.
func (t *Terminal) Changed() <-chan struct{} { return t.changed }

// Take returns what the process printed since the last Take, its control sequences included.
func (t *Terminal) Take() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.printed
	t.printed = nil
	return b
}

// Exited is closed once the process has exited.
func (t *Terminal) Exited() <-chan struct{} { return t.exited }

// Type writes keys to the terminal, as a person typing them would.
func (t *Terminal) Type(keys string) error {
	select {
	case <-t.exited:
		return errors.New("the session has exited")
	default:
	}
	_, err := t.pty.WriteString(keys)
	return err
}

// Close ends the process and every process it started that still runs, with EndTree: SIGTERM, then SIGKILL after the
// grace period. It then closes the terminal, once its reader has seen the end or outputDelay has passed, and waits up
// to outputDelay more for the reader, which a process that escaped the tree could still hold.
func (t *Terminal) Close(grace time.Duration) {
	EndTree(t.tree, grace)
	<-t.exited
	select {
	case <-t.read:
	case <-time.After(outputDelay):
	}
	_ = t.pty.Close() // nothing is written to it any longer
	select {
	case <-t.read:
	case <-time.After(outputDelay):
	}
}
