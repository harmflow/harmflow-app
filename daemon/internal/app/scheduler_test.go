package app

import (
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
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

var _ ports.StateStore = (*heldSchedule)(nil)

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

// countedState is a StateStore that counts the saves of each document before it writes them through.
type countedState struct {
	ports.StateStore

	mu    sync.Mutex
	saves map[string]int
}

var _ ports.StateStore = (*countedState)(nil)

func (s *countedState) Save(name string, v any) error {
	s.mu.Lock()
	s.saves[name]++
	s.mu.Unlock()
	return s.StateStore.Save(name, v)
}

// count is how many saves of the document name were made since the last reset, which it makes when reset is true.
func (s *countedState) count(name string, reset bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.saves[name]
	if reset {
		s.saves = map[string]int{}
	}
	return n
}

// One start writes the scheduler state once, however many tasks it schedules: each task's next firing is computed
// first, then the whole state saved, with what one save per task wrote.
func TestLoadScheduleWritesTheStateOnce(t *testing.T) {
	e := newEnv(t)
	e.create("daily", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n")
	e.create("hourly", "  schedule:\n    interval: 1h\n")
	e.create("nightly", "  schedule:\n    cron: \"0 22 * * *\"\n    timezone: UTC\n")
	e.create("manual", "")
	var state *countedState
	e = e.restart(t, func(n *env) {
		state = &countedState{StateStore: n.state, saves: map[string]int{}}
		n.state = state
	})
	before := map[string]time.Time{}
	e.app.mu.Lock()
	for id, s := range e.app.sched {
		before[id] = *s.NextFireAt
	}
	e.app.mu.Unlock()
	state.count("scheduler", true)
	e.app.loadSchedule()
	if got := state.count("scheduler", false); got != 1 {
		t.Errorf("loadSchedule() of four tasks saved the scheduler state %d times, want 1", got)
	}
	var saved scheduleFile
	if ok, err := state.Load("scheduler", &saved); !ok || err != nil {
		t.Fatalf("Load(scheduler) = %v, %v; want the saved state", ok, err)
	}
	for _, id := range []string{"daily", "hourly", "nightly"} {
		s := saved.Tasks[id]
		if s == nil || s.NextFireAt == nil || !s.NextFireAt.Equal(before[id]) {
			t.Errorf("the saved state of %s is %+v, want its next firing at %v", id, s, before[id])
		}
	}
	if s, ok := saved.Tasks["manual"]; ok {
		t.Errorf("the saved state holds the unscheduled task manual: %+v", s)
	}
}
