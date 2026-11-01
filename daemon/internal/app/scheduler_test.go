package app

import (
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// heldSchedule is a StateStore whose first Save, once it has begun, waits for release before it writes; it records
// what each Save is given, in the order the saves write, and closes second once the second one has written.
type heldSchedule struct {
	entered chan struct{} // closed when the first Save begins
	second  chan struct{}
	release chan struct{}

	mu     sync.Mutex
	calls  int
	writes []scheduleFile
}

func (s *heldSchedule) Load(string, any) (bool, error) { return false, nil }

func (s *heldSchedule) Save(_ string, v any) error {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if n == 1 {
		close(s.entered)
		<-s.release
	}
	s.mu.Lock()
	s.writes = append(s.writes, v.(scheduleFile))
	s.mu.Unlock()
	if n == 2 {
		close(s.second)
	}
	return nil
}

// harmflow-app#699: two saves of the scheduler state at once write it in the order their snapshots were taken, so the
// last one written holds the firing the second save recorded, and a stop after it loses none. The first save is held
// in its write until the second one has written, or for a second while the second waits for it.
func TestSchedulerSavesKeepTheirOrder(t *testing.T) {
	state := &heldSchedule{entered: make(chan struct{}), second: make(chan struct{}), release: make(chan struct{})}
	a, err := New(Deps{Paths: NewPaths(t.TempDir(), t.TempDir(), t.TempDir()), State: state,
		Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	a.mu.Lock()
	a.sched["t"] = &scheduleState{Rule: "every 1h", NextFireAt: &at}
	a.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.saveSchedule()
	}()
	<-state.entered
	go func() {
		defer wg.Done()
		a.mu.Lock()
		a.recordFiringLocked("t", firingRecord{ScheduledFor: at, ProcessedAt: at, Trigger: domain.TriggerManual,
			Outcome: "started", ExecutionID: "x2"})
		a.mu.Unlock()
		a.saveSchedule()
	}()
	select {
	case <-state.second:
	case <-time.After(time.Second):
	}
	close(state.release)
	wg.Wait()

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.writes) != 2 {
		t.Fatalf("Save called %d times; want 2", len(state.writes))
	}
	last := state.writes[1].Firings["t"]
	if len(last) != 1 || last[0].ExecutionID != "x2" {
		t.Errorf("last scheduler state written holds firings %+v; want the one of x2, the newest snapshot", last)
	}
}
