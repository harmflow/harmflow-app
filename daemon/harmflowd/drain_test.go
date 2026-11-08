package harmflowd

import (
	"errors"
	"io"
	"log"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// fakeStopper records what awaitStop asks of the use cases.
type fakeStopper struct {
	mu      sync.Mutex
	drains  []time.Duration
	stopped []string // the key of each Stop's reason
	reason  domain.Text
}

var _ stopper = (*fakeStopper)(nil)

func (f *fakeStopper) RequestDrain(deadline time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drains = append(f.drains, deadline)
	return nil
}

func (f *fakeStopper) ShutdownReason() domain.Text { return f.reason }

func (f *fakeStopper) Stop(reason domain.Text) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, reason.Key)
}

func (f *fakeStopper) drained() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.drains)
}

// awaiting runs awaitStop on a goroutine and returns the channel its result comes on.
func awaiting(f *fakeStopper, requested <-chan struct{}, sig <-chan os.Signal, serveErr <-chan error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- awaitStop(f, requested, sig, serveErr, log.New(io.Discard, "", 0)) }()
	return done
}

// result waits for awaitStop to return; it fails the test when it does not within 5 seconds.
func result(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("awaitStop did not return within 5 seconds")
		return nil
	}
}

// SIGQUIT asks for a draining stop with the default deadline and does not stop the daemon by itself; the drain's end,
// which closes requested, stops it for the reason the use cases give.
func TestSIGQUITDrains(t *testing.T) {
	f := &fakeStopper{reason: domain.T("daemon_drained", "drained")}
	requested := make(chan struct{})
	sig := make(chan os.Signal, 1)
	done := awaiting(f, requested, sig, nil)

	sig <- syscall.SIGQUIT
	deadline := time.Now().Add(5 * time.Second)
	for f.drained() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("awaitStop() = %v after SIGQUIT, want it to go on waiting", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(requested)
	if err := result(t, done); err != nil {
		t.Errorf("awaitStop() = %v, want nil", err)
	}
	if len(f.drains) != 1 || f.drains[0] != 0 {
		t.Errorf("RequestDrain calls %v, want one with the default deadline (0)", f.drains)
	}
	if len(f.stopped) != 1 || f.stopped[0] != "daemon_drained" {
		t.Errorf("Stop calls %v, want one for daemon_drained", f.stopped)
	}
}

// SIGINT and SIGTERM stop the daemon at once, a drain under way or not, and never ask for a drain.
func TestSIGINTAndSIGTERMStopAtOnce(t *testing.T) {
	for _, s := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(s.String(), func(t *testing.T) {
			for _, draining := range []bool{false, true} {
				f := &fakeStopper{}
				sig := make(chan os.Signal, 2)
				if draining {
					sig <- syscall.SIGQUIT
				}
				sig <- s
				if err := result(t, awaiting(f, make(chan struct{}), sig, nil)); err != nil {
					t.Errorf("awaitStop() = %v, want nil", err)
				}
				if len(f.stopped) != 1 || f.stopped[0] != "daemon_signal" {
					t.Errorf("Stop calls %v with a drain under way %v, want one for daemon_signal", f.stopped, draining)
				}
				if want := map[bool]int{false: 0, true: 1}[draining]; len(f.drains) != want {
					t.Errorf("RequestDrain calls %v with a drain under way %v, want %d", f.drains, draining, want)
				}
			}
		})
	}
}

// A local API that stops serving stops the daemon, and its error is returned.
func TestLocalAPIFailureStops(t *testing.T) {
	f := &fakeStopper{}
	serveErr := make(chan error, 1)
	failure := errors.New("the listener closed")
	serveErr <- failure
	if err := result(t, awaiting(f, make(chan struct{}), make(chan os.Signal), serveErr)); !errors.Is(err, failure) {
		t.Errorf("awaitStop() = %v, want %v", err, failure)
	}
	if len(f.stopped) != 1 || f.stopped[0] != "local_api_stopped" {
		t.Errorf("Stop calls %v, want one for local_api_stopped", f.stopped)
	}
}
