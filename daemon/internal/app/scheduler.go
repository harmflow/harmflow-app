package app

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// scheduleState is what survives a restart for each task: the rule it was computed from, the next firing, and the
// last processed one.
type scheduleState struct {
	Rule        string     `json:"rule"`
	Timezone    string     `json:"timezone,omitempty"`
	NextFireAt  *time.Time `json:"nextFireAt,omitempty"`
	LastFiredAt *time.Time `json:"lastFiredAt,omitempty"`
}

// firingRecord is what became of one firing the daemon processed, scheduled or by hand, kept so no firing is lost
// silently. For a catch-up, ScheduledFor is the first missed time and Missed how many misfires it covers; Policy is the
// missed-run policy a misfire followed. For a manual firing, ScheduledFor is when the run was asked for, as
// ProcessedAt is, and Policy is the overlap policy it followed when the task was running. A skipped firing says why,
// as a text the desktop translates.
type firingRecord struct {
	ScheduledFor time.Time         `json:"scheduledFor"`
	ProcessedAt  time.Time         `json:"processedAt"`
	Trigger      domain.Trigger    `json:"trigger"`
	Missed       int               `json:"missed,omitempty"`
	Policy       string            `json:"policy,omitempty"`
	Outcome      string            `json:"outcome"` // started, queued, coalesced, or skipped
	ExecutionID  string            `json:"executionId,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	ReasonKey    string            `json:"reasonKey,omitempty"`
	ReasonParams map[string]string `json:"reasonParams,omitempty"`
}

func (r *firingRecord) because(t domain.Text) {
	r.Reason, r.ReasonKey, r.ReasonParams = t.Message, t.Key, t.Params
}

// firingHistoryLimit is how many firings each task's history keeps, the latest ones.
const firingHistoryLimit = 20

type scheduleFile struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Tasks         map[string]*scheduleState `json:"tasks"`
	// Firings is each task's history, oldest first. It stays when the task goes, as its executions do.
	Firings map[string][]firingRecord `json:"firings,omitempty"`
}

func (a *App) loadSchedule() {
	var f scheduleFile
	if ok, err := a.d.State.Load("scheduler", &f); err != nil {
		a.log.Printf("scheduler state: %v", err)
	} else if ok {
		a.mu.Lock()
		for id, s := range f.Tasks {
			if _, exists := a.tasks[id]; exists {
				a.sched[id] = s
			}
		}
		for id, h := range f.Firings {
			a.firings[id] = h
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

// saveSchedule writes the schedules and the firing records. Saves run one at a time, each from its snapshot to the end
// of its write (schedMu), so a save that took an older snapshot never writes after one that took a newer.
func (a *App) saveSchedule() {
	a.schedMu.Lock()
	defer a.schedMu.Unlock()
	a.mu.Lock()
	f := scheduleFile{SchemaVersion: "1", Tasks: map[string]*scheduleState{}, Firings: map[string][]firingRecord{}}
	for id, s := range a.sched {
		c := *s
		f.Tasks[id] = &c
	}
	for id, h := range a.firings {
		f.Firings[id] = append([]firingRecord(nil), h...)
	}
	a.mu.Unlock()
	if err := a.d.State.Save("scheduler", f); err != nil {
		a.log.Printf("saving scheduler state: %v", err)
	}
}

// rescheduleTask recomputes the next firing when the schedule changed, at once. An unchanged
// schedule keeps its pending firing, so a restart still sees the misfire. A file that fails validation keeps the
// schedule of its last valid definition, so its firings come due and are recorded as skipped.
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
	if doc.Spec.Schedule == nil {
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

func (a *App) schedulerLoop(ctx context.Context) {
	defer a.wg.Done()
	t := time.NewTicker(a.d.TickInterval)
	defer t.Stop()
	a.Tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Tick()
		}
	}
}

// dueFiring is a firing Tick decided, with its record so far.
type dueFiring struct {
	taskID string
	firingRecord
}

// Tick fires every due task. A firing scheduled while the daemon or the machine was unavailable is a misfire, however
// late it is; so is one later than MisfireAfter. All misfires coalesce into one catch-up or are skipped, per the
// task's policy. Every firing it processes is recorded with what became of it.
func (a *App) Tick() {
	a.tickMu.Lock()
	defer a.tickMu.Unlock()
	// Asked at every check, so the monitor compares its clocks over one interval and sees a wake when it happens.
	wake := a.d.Wake.LastWake()
	now := a.now()
	var due []dueFiring
	a.mu.Lock()
	woke := wake.After(a.unavailableUntil)
	if woke {
		a.unavailableUntil = wake // the machine slept until then
	}
	if !a.accepting {
		a.mu.Unlock()
		return
	}
	processed := false
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
		processed = true
		r := firingRecord{ScheduledFor: scheduled, ProcessedAt: now, Trigger: domain.TriggerSchedule}
		if scheduled.Before(a.unavailableUntil) || now.Sub(scheduled) > a.d.MisfireAfter {
			r.Trigger, r.Missed = domain.TriggerCatchUp, countMissed(rule, scheduled, now)
		}
		if reason, held := heldBack(e); held {
			r.Outcome = "skipped"
			r.because(reason)
			a.recordFiringLocked(id, r)
			continue
		}
		fired := now
		s.LastFiredAt = &fired
		if r.Trigger == domain.TriggerCatchUp {
			r.Policy = domain.MissedRunOnResume
			if e.doc.Effective().Spec.Schedule.MissedRun.Policy == domain.MissedRunSkip {
				r.Policy, r.Outcome = domain.MissedRunSkip, "skipped"
				r.because(missedText(r.Missed, true))
				a.recordFiringLocked(id, r)
			}
		}
		due = append(due, dueFiring{taskID: id, firingRecord: r})
	}
	a.mu.Unlock()
	if woke {
		a.log.Printf("the machine woke from sleep; firings scheduled before %s are misfires", wake.Format(time.RFC3339))
	}
	if !processed {
		return
	}
	// The next firings and the skips are saved before anything runs, so a crash never fires them twice.
	a.saveSchedule()
	ran := false
	for _, f := range due {
		if f.Trigger == domain.TriggerCatchUp {
			skip := f.Policy == domain.MissedRunSkip
			a.emit(domain.Event{Type: domain.EventFiringMissed, TaskID: f.taskID, Severity: domain.SeverityWarning,
				Payload: map[string]any{"missed": f.Missed, "policy": f.Policy, "firstMissedAt": f.ScheduledFor}}.Says(
				missedText(f.Missed, skip)))
			if skip {
				continue
			}
		}
		sf := f.ScheduledFor
		res, err := a.fire(f.taskID, f.Trigger, &sf)
		r := f.firingRecord
		r.Outcome, r.ExecutionID = res.Outcome, res.ExecutionID
		switch {
		case err != nil:
			r.Outcome = "skipped"
			r.because(errorText(err))
		case res.Outcome == "skipped":
			r.ExecutionID = res.cause
			r.because(res.reason)
		}
		a.mu.Lock()
		a.recordFiringLocked(f.taskID, r)
		a.mu.Unlock()
		ran = true
	}
	if ran {
		a.saveSchedule() // what became of the firings that ran
	}
}

// heldBack says why a due firing of a task that cannot fire now is skipped: it is being deleted, its file fails
// validation, or it is disabled.
func heldBack(e *taskEntry) (domain.Text, bool) {
	switch {
	case e.deletePending:
		return domain.T("task_being_deleted", "the task is being deleted"), true
	case e.invalid != nil:
		return domain.T("task_file_invalid", "the task file {{file}} fails validation", "file", taskFileName(e.file)), true
	case !e.doc.IsEnabled():
		return domain.T("task_was_disabled", "the task was disabled"), true
	}
	return domain.Text{}, false
}

// recordFiringLocked adds a firing to its task's history, which keeps the latest firingHistoryLimit.
func (a *App) recordFiringLocked(taskID string, r firingRecord) {
	h := append(a.firings[taskID], r)
	if len(h) > firingHistoryLimit {
		h = h[len(h)-firingHistoryLimit:]
	}
	a.firings[taskID] = h
}

func errorText(err error) domain.Text {
	var e *Error
	if errors.As(err, &e) {
		return e.Text
	}
	return domain.TextOf(err)
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
	// reason says why a firing was skipped, and cause is the execution it was skipped for, as the firing record keeps.
	reason domain.Text
	cause  string
	// policy is the overlap policy a firing of a running task followed; it is empty when the task was idle.
	policy string
}

// RunTask is the manual trigger. A run it starts, queues, coalesces, or skips is recorded in the task's firing
// history; a run it refuses is not.
func (a *App) RunTask(id string) (FiringResult, error) {
	asked := a.now()
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
	res, err := a.fire(id, domain.TriggerManual, nil)
	if err != nil {
		return res, err
	}
	r := firingRecord{ScheduledFor: asked, ProcessedAt: asked, Trigger: domain.TriggerManual, Policy: res.policy,
		Outcome: res.Outcome, ExecutionID: res.ExecutionID}
	if res.Outcome == "skipped" {
		r.ExecutionID = res.cause
		r.because(res.reason)
	}
	a.mu.Lock()
	a.recordFiringLocked(id, r)
	a.mu.Unlock()
	a.saveSchedule()
	return res, nil
}

// fire starts an execution, or applies the overlap policy when the task is running: one firing waits and later
// ones coalesce into it, or the firing is skipped and logged.
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
		// not declare.
		a.mu.Unlock()
		text := a.systemsText(doc)
		if trigger == domain.TriggerManual {
			return FiringResult{}, newError(CodeConflict, text)
		}
		a.emit(domain.Event{Type: domain.EventFiringSkipped, TaskID: taskID, Severity: domain.SeverityWarning,
			Payload: map[string]any{"trigger": string(trigger)}}.Says(text))
		return FiringResult{Outcome: "skipped", reason: text}, nil
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
		policy := doc.Effective().Spec.Concurrency.OverlappingRuns
		if policy == domain.OverlapSkip {
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
			reason := domain.T("firing_skipped", "the task was running; this firing was skipped, per its overlap policy")
			a.emit(domain.Event{Type: domain.EventFiringSkipped, TaskID: taskID, ExecutionID: causeID,
				Severity: domain.SeverityWarning, Payload: map[string]any{"trigger": string(trigger)}}.Says(reason))
			return FiringResult{Outcome: "skipped", reason: reason, cause: causeID, policy: policy}, nil
		}
		if waiting != nil {
			a.mu.Unlock()
			a.emit(domain.Event{Type: domain.EventFiringCoalesced, TaskID: taskID, ExecutionID: waiting.ID,
				Payload: map[string]any{"trigger": string(trigger)}}.Says(
				domain.T("firing_coalesced", "a firing already waits; this one coalesced into it")))
			return FiringResult{Outcome: "coalesced", ExecutionID: waiting.ID, policy: policy}, nil
		}
		x := domain.NewExecution(doc, trigger, scheduledFor, now)
		a.addExecutionLocked(x)
		status := x.Status
		a.mu.Unlock()
		a.persistExecution(x)
		a.emit(domain.Event{Type: domain.EventExecutionCreated, TaskID: taskID, ExecutionID: x.ID,
			Payload: map[string]any{"trigger": string(trigger), "status": string(status)}})
		a.emit(domain.Event{Type: domain.EventFiringQueued, TaskID: taskID, ExecutionID: x.ID}.Says(
			domain.T("firing_queued", "the task is running; this firing waits for it")))
		return FiringResult{Outcome: "queued", ExecutionID: x.ID, policy: policy}, nil
	}
	x := domain.NewExecution(doc, trigger, scheduledFor, now)
	a.addExecutionLocked(x)
	h := a.claimLocked(taskID)
	status := x.Status
	a.mu.Unlock()
	a.persistExecution(x)
	a.emit(domain.Event{Type: domain.EventExecutionCreated, TaskID: taskID, ExecutionID: x.ID,
		Payload: map[string]any{"trigger": string(trigger), "status": string(status)}})
	a.launch(x, h)
	return FiringResult{Outcome: "started", ExecutionID: x.ID}, nil
}

// claimLocked marks a task as running: the per-task exclusion.
func (a *App) claimLocked(taskID string) *runHandle {
	h := &runHandle{done: make(chan struct{})}
	a.running[taskID] = h
	return h
}

// startQueued starts the waiting firing of each task that is free, with the definition current now. A firing
// whose task is disabled or gone never starts: it is dropped, however the change arrived, even while the daemon was
// down.
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

// dropQueued cancels the waiting firing of a task that was disabled or deleted.
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

// SchedulePreview lists the next occurrences in the schedule's time zone.
// SchedulePreview carries an invalid schedule's error as a text the desktop translates: Error is the en-US one.
type SchedulePreview struct {
	Occurrences []time.Time       `json:"occurrences"`
	Timezone    string            `json:"timezone,omitempty"`
	Error       string            `json:"error,omitempty"`
	ErrorKey    string            `json:"errorKey,omitempty"`
	ErrorParams map[string]string `json:"errorParams,omitempty"`
}

// PreviewSchedule lists the next count firings of a schedule, five when count is not between 1 and 20, in the
// schedule's time zone or the daemon's; an invalid schedule has none, and its error.
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
