package app

import (
	"strconv"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// scheduleState is what survives a restart for each task: the rule it was computed from, the next firing, and the
// last processed one (FS-002 FR-006).
type scheduleState struct {
	Rule        string     `json:"rule"`
	Timezone    string     `json:"timezone,omitempty"`
	NextFireAt  *time.Time `json:"nextFireAt,omitempty"`
	LastFiredAt *time.Time `json:"lastFiredAt,omitempty"`
}

type scheduleFile struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Tasks         map[string]*scheduleState `json:"tasks"`
}

func (a *App) loadSchedule() {
	var f scheduleFile
	if ok, err := a.d.State.Load("scheduler", &f); err != nil {
		a.log.Printf("scheduler state: %v", err)
	} else if ok && f.Tasks != nil {
		a.mu.Lock()
		for id, s := range f.Tasks {
			if _, exists := a.tasks[id]; exists {
				a.sched[id] = s
			}
		}
		a.mu.Unlock()
	}
	a.mu.Lock()
	ids := sortedKeys(a.tasks)
	a.mu.Unlock()
	for _, id := range ids {
		a.rescheduleTask(id)
	}
}

func (a *App) saveSchedule() {
	a.mu.Lock()
	f := scheduleFile{SchemaVersion: "1", Tasks: map[string]*scheduleState{}}
	for id, s := range a.sched {
		c := *s
		f.Tasks[id] = &c
	}
	a.mu.Unlock()
	if err := a.d.State.Save("scheduler", f); err != nil {
		a.log.Printf("saving scheduler state: %v", err)
	}
}

// rescheduleTask recomputes the next firing when the schedule changed, at once (D-083, FS-002 FR-010). An unchanged
// schedule keeps its pending firing, so a restart still sees the misfire.
func (a *App) rescheduleTask(id string) {
	a.mu.Lock()
	e := a.tasks[id]
	if e == nil {
		a.mu.Unlock()
		return
	}
	doc := e.doc
	s := a.sched[id]
	now := a.now()
	if doc.Spec.Schedule == nil || e.invalid != nil {
		delete(a.sched, id)
		a.mu.Unlock()
		a.saveSchedule()
		return
	}
	rule, err := domain.ParseSchedule(*doc.Spec.Schedule, a.d.Location)
	if err != nil {
		delete(a.sched, id)
		a.mu.Unlock()
		a.saveSchedule()
		return
	}
	tz := doc.Spec.Schedule.Timezone
	if s == nil || s.Rule != rule.String() || s.Timezone != tz || s.NextFireAt == nil {
		if s == nil {
			s = &scheduleState{}
			a.sched[id] = s
		}
		from := now
		if _, interval := rule.(*domain.IntervalRule); interval && s.LastFiredAt != nil && s.Rule == rule.String() {
			from = *s.LastFiredAt
		}
		next := rule.Next(from)
		s.Rule, s.Timezone, s.NextFireAt = rule.String(), tz, &next
	}
	a.mu.Unlock()
	a.saveSchedule()
}

func (a *App) schedulerLoop() {
	defer a.wg.Done()
	t := time.NewTicker(a.d.TickInterval)
	defer t.Stop()
	a.Tick()
	for {
		select {
		case <-a.stopCh:
			return
		case <-t.C:
			a.Tick()
		}
	}
}

type dueFiring struct {
	taskID       string
	trigger      domain.Trigger
	scheduledFor time.Time
	missed       int
	skip         bool
}

// Tick fires every due task. A firing scheduled while the daemon was unavailable is a misfire, however late it is;
// so is one later than MisfireAfter. All misfires coalesce into one catch-up or are skipped, per the task's policy
// (D-071).
func (a *App) Tick() {
	a.tickMu.Lock()
	defer a.tickMu.Unlock()
	now := a.now()
	var due []dueFiring
	a.mu.Lock()
	if !a.accepting {
		a.mu.Unlock()
		return
	}
	for _, id := range sortedKeys(a.sched) {
		s := a.sched[id]
		e := a.tasks[id]
		if e == nil || s.NextFireAt == nil || now.Before(*s.NextFireAt) {
			continue
		}
		rule, err := domain.ParseSchedule(*e.doc.Spec.Schedule, a.d.Location)
		if err != nil {
			continue
		}
		scheduled := *s.NextFireAt
		next := rule.Next(now)
		s.NextFireAt = &next
		if !e.doc.IsEnabled() || e.invalid != nil || e.deletePending {
			continue
		}
		fired := now
		s.LastFiredAt = &fired
		f := dueFiring{taskID: id, trigger: domain.TriggerSchedule, scheduledFor: scheduled}
		if scheduled.Before(a.unavailableUntil) || now.Sub(scheduled) > a.d.MisfireAfter {
			f.missed = countMissed(rule, scheduled, now)
			f.trigger = domain.TriggerCatchUp
			f.skip = e.doc.Effective().Spec.Schedule.MissedRun.Policy == domain.MissedRunSkip
		}
		due = append(due, f)
	}
	a.mu.Unlock()
	if len(due) == 0 {
		return
	}
	a.saveSchedule()
	for _, f := range due {
		if f.trigger == domain.TriggerCatchUp {
			policy := domain.MissedRunOnResume
			if f.skip {
				policy = domain.MissedRunSkip
			}
			a.emit(domain.Event{Type: domain.EventFiringMissed, TaskID: f.taskID, Severity: domain.SeverityWarning,
				Payload: map[string]any{"missed": f.missed, "policy": policy, "firstMissedAt": f.scheduledFor}}.Says(
				missedText(f.missed, f.skip)))
			if f.skip {
				continue
			}
		}
		sf := f.scheduledFor
		_, _ = a.fire(f.taskID, f.trigger, &sf)
	}
}

// missedText counts the missed firings; the desktop picks the plural form from the count, and so does the en-US text.
func missedText(n int, skip bool) domain.Text {
	count := strconv.Itoa(n)
	if skip {
		if n == 1 {
			return domain.T("firings_missed_skipped", "{{count}} firing was missed and skipped, per the task's missed-run policy",
				"count", count)
		}
		return domain.T("firings_missed_skipped", "{{count}} firings were missed and skipped, per the task's missed-run policy",
			"count", count)
	}
	if n == 1 {
		return domain.T("firings_missed_catch_up", "{{count}} firing was missed; a catch-up execution runs for it",
			"count", count)
	}
	return domain.T("firings_missed_catch_up", "{{count}} firings were missed; one catch-up execution runs for all of them",
		"count", count)
}

// countMissed counts the occurrences between the first missed one and now, capped so a long absence stays cheap.
func countMissed(rule domain.Rule, first, now time.Time) int {
	n := 1
	t := first
	for n < 100000 {
		t = rule.Next(t)
		if t.IsZero() || t.After(now) {
			break
		}
		n++
	}
	return n
}

// FiringResult says what became of a firing.
type FiringResult struct {
	Outcome     string `json:"outcome"`
	ExecutionID string `json:"executionId,omitempty"`
}

// RunTask is the manual trigger (FS-002 FR-001).
func (a *App) RunTask(id string) (FiringResult, error) {
	e, err := a.entry(id)
	if err != nil {
		return FiringResult{}, err
	}
	a.mu.Lock()
	enabled := e.doc.IsEnabled() && e.invalid == nil
	pending := e.deletePending
	accepting := a.accepting
	a.mu.Unlock()
	if !accepting {
		return FiringResult{}, newError(CodeConflict,
			domain.T("daemon_stopping", "the daemon is stopping and accepts no new firing"))
	}
	if !enabled {
		return FiringResult{}, newError(CodeTaskDisabled, domain.T("task_disabled", "the task is disabled; enable it to run it"))
	}
	if pending {
		return FiringResult{}, newError(CodeConflict, domain.T("task_being_deleted", "the task is being deleted"))
	}
	return a.fire(id, domain.TriggerManual, nil)
}

// fire starts an execution, or applies the overlap policy when the task is running: one firing waits and later
// ones coalesce into it, or the firing is skipped and logged (D-072).
func (a *App) fire(taskID string, trigger domain.Trigger, scheduledFor *time.Time) (FiringResult, error) {
	a.mu.Lock()
	e := a.tasks[taskID]
	if e == nil {
		a.mu.Unlock()
		return FiringResult{}, newError(CodeNotFound, domain.T("task_not_found", "no task {{task}}", "task", taskID))
	}
	doc := e.doc
	if !doc.RunsOn(a.d.OS) {
		// Every firing — scheduled, catch-up, or by hand — passes here, so a script task never runs on a system it does
		// not declare (FS-013 FR-014).
		a.mu.Unlock()
		text := a.systemsText(doc)
		if trigger == domain.TriggerManual {
			return FiringResult{}, newError(CodeConflict, text)
		}
		a.emit(domain.Event{Type: domain.EventFiringSkipped, TaskID: taskID, Severity: domain.SeverityWarning,
			Payload: map[string]any{"trigger": string(trigger)}}.Says(text))
		return FiringResult{Outcome: "skipped"}, nil
	}
	_, running := a.running[taskID]
	var waiting, active *domain.Execution
	for _, x := range a.executions {
		if x.TaskID != taskID {
			continue
		}
		if x.Status == domain.StatusQueued {
			waiting = x
		} else if x.Status.IsActive() {
			active = x
		}
	}
	now := a.now()
	if running || waiting != nil {
		if doc.Effective().Spec.Concurrency.OverlappingRuns == domain.OverlapSkip {
			// The skip is logged with the execution that caused it, as a coalesced firing is with the one it joined; an
			// execution just claimed stays queued until it starts.
			cause := active
			if cause == nil {
				cause = waiting
			}
			var causeID string
			if cause != nil {
				causeID = cause.ID
			}
			a.mu.Unlock()
			a.emit(domain.Event{Type: domain.EventFiringSkipped, TaskID: taskID, ExecutionID: causeID,
				Severity: domain.SeverityWarning, Payload: map[string]any{"trigger": string(trigger)}}.Says(
				domain.T("firing_skipped", "the task was running; this firing was skipped, per its overlap policy")))
			return FiringResult{Outcome: "skipped"}, nil
		}
		if waiting != nil {
			a.mu.Unlock()
			a.emit(domain.Event{Type: domain.EventFiringCoalesced, TaskID: taskID, ExecutionID: waiting.ID,
				Payload: map[string]any{"trigger": string(trigger)}}.Says(
				domain.T("firing_coalesced", "a firing already waits; this one coalesced into it")))
			return FiringResult{Outcome: "coalesced", ExecutionID: waiting.ID}, nil
		}
		x := domain.NewExecution(doc, trigger, scheduledFor, now)
		a.executions[x.ID] = x
		status := x.Status
		a.mu.Unlock()
		a.persistExecution(x)
		a.emit(domain.Event{Type: domain.EventExecutionCreated, TaskID: taskID, ExecutionID: x.ID,
			Payload: map[string]any{"trigger": string(trigger), "status": string(status)}})
		a.emit(domain.Event{Type: domain.EventFiringQueued, TaskID: taskID, ExecutionID: x.ID}.Says(
			domain.T("firing_queued", "the task is running; this firing waits for it")))
		return FiringResult{Outcome: "queued", ExecutionID: x.ID}, nil
	}
	x := domain.NewExecution(doc, trigger, scheduledFor, now)
	a.executions[x.ID] = x
	h := a.claimLocked(taskID)
	status := x.Status
	a.mu.Unlock()
	a.persistExecution(x)
	a.emit(domain.Event{Type: domain.EventExecutionCreated, TaskID: taskID, ExecutionID: x.ID,
		Payload: map[string]any{"trigger": string(trigger), "status": string(status)}})
	a.launch(x, h)
	return FiringResult{Outcome: "started", ExecutionID: x.ID}, nil
}

// claimLocked marks a task as running; the per-task exclusion of D-023.
func (a *App) claimLocked(taskID string) *runHandle {
	h := &runHandle{done: make(chan struct{})}
	a.running[taskID] = h
	return h
}

// startQueued starts the waiting firing of each task that is free, with the definition current now (D-083). A firing
// whose task is disabled or gone never starts: it is dropped, however the change arrived, even while the daemon was
// down (FS-001 FR-015, FS-003 FR-007).
func (a *App) startQueued() {
	a.mu.Lock()
	var start []*domain.Execution
	var handles []*runHandle
	var drops []droppedFiring
	if a.accepting {
		var queued []*domain.Execution
		for _, x := range a.executions {
			if x.Status == domain.StatusQueued {
				queued = append(queued, x)
			}
		}
		sortByCreated(queued)
		for _, x := range queued {
			if _, busy := a.running[x.TaskID]; busy {
				continue
			}
			e := a.tasks[x.TaskID]
			switch {
			case e == nil:
				drops = append(drops, a.dropQueuedLocked(x.TaskID, domain.T("task_file_removed", "the task file was removed"))...)
			case e.deletePending: // the task goes as its execution ends; its deletion already dropped the firing
			case !e.doc.IsEnabled():
				drops = append(drops, a.dropQueuedLocked(x.TaskID, domain.T("task_was_disabled", "the task was disabled"))...)
			default:
				start = append(start, x)
				handles = append(handles, a.claimLocked(x.TaskID))
			}
		}
	}
	a.mu.Unlock()
	a.announceDropped(drops)
	for i, x := range start {
		a.launch(x, handles[i])
	}
}

// droppedFiring is a waiting firing cancelled under a.mu, to save and announce once a.mu is released.
type droppedFiring struct {
	x      *domain.Execution
	reason domain.Text
}

// dropQueued cancels the waiting firing of a task that was disabled or deleted (D-083).
func (a *App) dropQueued(taskID string, reason domain.Text) {
	a.mu.Lock()
	drops := a.dropQueuedLocked(taskID, reason)
	a.mu.Unlock()
	a.announceDropped(drops)
}

func (a *App) dropQueuedLocked(taskID string, reason domain.Text) []droppedFiring {
	var drops []droppedFiring
	now := a.now()
	for _, x := range a.executions {
		if x.TaskID == taskID && x.Status == domain.StatusQueued {
			x.Status = domain.StatusCancelled
			x.EndedAt = &now
			x.SetError(reason)
			drops = append(drops, droppedFiring{x: x, reason: reason})
		}
	}
	return drops
}

func (a *App) announceDropped(drops []droppedFiring) {
	for _, d := range drops {
		a.persistExecution(d.x)
		a.emit(domain.Event{Type: domain.EventExecutionCancelled, TaskID: d.x.TaskID, ExecutionID: d.x.ID,
			Payload: map[string]any{"status": string(domain.StatusCancelled)}}.Says(d.reason))
	}
}

// SchedulePreview lists the next occurrences in the schedule's time zone (FS-002 FR-002).
// SchedulePreview carries an invalid schedule's error as a text the desktop translates: Error is the en-US one.
type SchedulePreview struct {
	Occurrences []time.Time       `json:"occurrences"`
	Timezone    string            `json:"timezone,omitempty"`
	Error       string            `json:"error,omitempty"`
	ErrorKey    string            `json:"errorKey,omitempty"`
	ErrorParams map[string]string `json:"errorParams,omitempty"`
}

func (a *App) PreviewSchedule(s domain.Schedule, count int) SchedulePreview {
	if count <= 0 || count > 20 {
		count = 5
	}
	rule, err := domain.ParseSchedule(s, a.d.Location)
	if err != nil {
		text := domain.TextOf(err)
		return SchedulePreview{Occurrences: []time.Time{}, Error: text.Message, ErrorKey: text.Key, ErrorParams: text.Params}
	}
	loc := a.d.Location
	if s.Timezone != "" {
		if l, err := time.LoadLocation(s.Timezone); err == nil {
			loc = l
		}
	}
	occ := domain.Occurrences(rule, a.now(), count)
	for i := range occ {
		occ[i] = occ[i].In(loc)
	}
	return SchedulePreview{Occurrences: occ, Timezone: loc.String()}
}
