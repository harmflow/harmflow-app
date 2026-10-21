package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/contracts"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// run is the state of one execution while it runs. Its definition is the snapshot taken when it started (D-083).
type run struct {
	a        *App
	x        *domain.Execution
	h        *runHandle
	ctx      context.Context
	def      domain.TaskDocument
	eff      domain.TaskDocument
	dir      string
	work     string
	extra    []string
	saveMu   sync.Mutex
	lastSave time.Time

	progressOffset int64
	commandIndex   map[string]int
	lastOperation  string
	attempt        int
	finalText      string // the agent's last message, the summary when the session leaves no report
}

func (a *App) launch(x *domain.Execution, h *runHandle) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.execute(x, h)
	}()
}

// execute carries one execution to its terminal state, then starts the task's waiting firing, if any.
func (a *App) execute(x *domain.Execution, h *runHandle) {
	defer func() {
		a.mu.Lock()
		delete(a.running, x.TaskID)
		e := a.tasks[x.TaskID]
		deletePending := e != nil && e.deletePending
		a.mu.Unlock()
		close(h.done)
		if l, ok := a.d.Events.(interface{ Close(string) }); ok {
			l.Close(x.ID)
		}
		if deletePending {
			_ = a.removeTask(x.TaskID)
		}
		a.startQueued()
	}()

	a.mu.Lock()
	e := a.tasks[x.TaskID]
	if e == nil {
		a.mu.Unlock()
		a.finish(&run{a: a, x: x, h: h}, domain.StatusCancelled, "the task no longer exists")
		return
	}
	def := e.doc
	rev := e.file.Revision
	a.mu.Unlock()

	eff := def.Effective()
	now := a.now()
	timeout := time.Duration(*eff.Spec.Execution.TimeoutMinutes) * time.Minute
	deadline := now.Add(timeout)
	// The timeout runs on real time; the record shows it on the daemon's clock.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	a.mu.Lock()
	h.cancel = cancel
	cancelled := h.cancelRequested
	if a.stopping {
		cancel() // the daemon began stopping before this execution could be cancelled with the others
	}
	a.mu.Unlock()

	r := &run{a: a, x: x, h: h, ctx: ctx, def: def, eff: eff, commandIndex: map[string]int{}}
	a.mu.Lock()
	x.DefinitionRevision = rev
	x.TaskName = def.Metadata.Name
	x.TaskType = def.Spec.Type
	a.mu.Unlock()
	if cancelled {
		r.finish(domain.StatusCancelled, "cancelled before it started")
		return
	}
	// Leave queued before the harness check: the scheduler and cancellation take a queued execution for a waiting firing.
	r.setStatus(domain.StatusPreparing, "Checking the harness")
	status, reason := r.attempts(func() error {
		started := a.now()
		a.mu.Lock()
		x.StartedAt = &started
		x.TimeoutAt = &deadline
		a.mu.Unlock()
		r.setStatus(domain.StatusPreparing, "Preparing the workspace")
		r.event(domain.Event{Type: domain.EventExecutionStarted, Message: def.Metadata.Name,
			Payload: map[string]any{"trigger": string(x.Trigger), "timeoutAt": deadline, "definitionRevision": rev}})
		return r.prepare()
	})
	if status == "" {
		status, reason = r.outcomeFromReport()
	} else if r.daemonStopping() && !r.cancelRequested() {
		// A daemon that stops never continues the execution: it is interrupted (D-089).
		status, reason = domain.StatusInterrupted, "the daemon stopped before the execution finished"
	}
	r.finish(status, reason)
}

func (r *run) daemonStopping() bool {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return r.a.stopping
}

func (r *run) event(e domain.Event) domain.Event {
	e.TaskID = r.x.TaskID
	e.ExecutionID = r.x.ID
	if e.Attempt == 0 {
		e.Attempt = r.attempt
	}
	if e.Harness == "" && r.attempt > 0 {
		r.a.mu.Lock()
		if at := r.x.CurrentAttempt(); at != nil {
			e.Harness = at.Harness
		}
		r.a.mu.Unlock()
	}
	if e.WorkItemID == "" {
		r.a.mu.Lock()
		e.WorkItemID = r.x.WorkItem
		r.a.mu.Unlock()
	}
	return r.a.emit(e)
}

// save persists the record, at most every second unless forced.
func (r *run) save(force bool) {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	if !force && time.Since(r.lastSave) < time.Second {
		return
	}
	r.lastSave = time.Now()
	r.a.mu.Lock()
	data, _ := json.Marshal(r.x)
	r.a.mu.Unlock()
	var snapshot domain.Execution
	_ = json.Unmarshal(data, &snapshot)
	r.a.persistExecution(&snapshot)
}

func (r *run) setStatus(s domain.ExecutionStatus, step string) {
	r.a.mu.Lock()
	from := r.x.Status
	if from == s || from.CanTransition(s) != nil {
		if step != "" {
			r.x.CurrentStep = step
		}
		r.a.mu.Unlock()
		return
	}
	r.x.Status = s
	if step != "" {
		r.x.CurrentStep = step
	}
	r.a.mu.Unlock()
	r.save(true)
	r.event(domain.Event{Type: domain.EventExecutionStatusChanged, Message: step,
		Payload: map[string]any{"from": string(from), "status": string(s)}})
}

// prepare creates the execution directory and the workspace, and writes context.json for the session.
func (r *run) prepare() error {
	a := r.a
	r.dir = filepath.Join(a.d.Paths.Runs, r.x.ID)
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return fmt.Errorf("creating the execution directory: %w", err)
	}
	work := filepath.Join(a.d.Paths.Workspace, r.x.TaskID)
	if p := r.eff.Spec.Workspace.Path; p != "" {
		expanded, err := a.expand(p)
		if err != nil {
			return err
		}
		work = expanded
	}
	if err := os.MkdirAll(work, 0o700); err != nil {
		return fmt.Errorf("creating the workspace %s: %w", work, err)
	}
	r.work = work
	for _, repo := range r.eff.Repositories() {
		p, err := a.expand(repo.LocalPath)
		if err != nil {
			return err
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return fmt.Errorf("the local path %s of %s does not exist", p, repo.Name)
		}
		r.extra = append(r.extra, p)
	}
	a.mu.Lock()
	r.x.WorkspacePath = work
	r.x.ExecutionDir = r.dir
	if repos := r.eff.Repositories(); len(repos) == 1 {
		r.x.Repository = repos[0].Name
	}
	a.mu.Unlock()
	_ = os.WriteFile(filepath.Join(r.dir, "execution-report.schema.json"), contracts.ExecutionReportSchema, 0o600)
	return r.writeContext(nil)
}

type previousAttempt struct {
	Number        int    `json:"number"`
	Harness       string `json:"harness"`
	Model         string `json:"model"`
	Outcome       string `json:"outcome"`
	Error         string `json:"error,omitempty"`
	LastOperation string `json:"lastOperation,omitempty"`
	LastMessage   string `json:"lastMessage,omitempty"`
}

// writeContext gives the session the task's parameters and history: what the skills need to skip processed work,
// resume, and reconcile (D-069, D-089).
func (r *run) writeContext(prev []previousAttempt) error {
	a := r.a
	a.mu.Lock()
	items := []domain.WorkItem{}
	for _, k := range sortedKeys(a.ledger[r.x.TaskID]) {
		items = append(items, *a.ledger[r.x.TaskID][k])
	}
	var interrupted []map[string]any
	var history []map[string]any
	for _, x := range a.executions {
		if x.TaskID != r.x.TaskID || x.ID == r.x.ID || !x.Status.IsTerminal() {
			continue
		}
		h := map[string]any{"executionId": x.ID, "status": x.Status, "createdAt": x.CreatedAt}
		if x.WorkItem != "" {
			h["workItem"] = x.WorkItem
		}
		if x.PullRequestURL != "" {
			h["pullRequestUrl"] = x.PullRequestURL
		}
		history = append(history, h)
		if x.Status == domain.StatusInterrupted {
			interrupted = append(interrupted, map[string]any{"executionId": x.ID, "workItem": x.WorkItem,
				"lastOperation": x.LastOperation, "endedAt": x.EndedAt})
		}
	}
	a.mu.Unlock()
	sort.Slice(history, func(i, j int) bool {
		return history[i]["createdAt"].(time.Time).After(history[j]["createdAt"].(time.Time))
	})
	if len(history) > 50 {
		history = history[:50]
	}
	ctxDoc := map[string]any{
		"schemaVersion": "1",
		"harmflow":      map[string]any{"version": a.d.Version},
		"task": map[string]any{
			"id": r.def.Metadata.ID, "name": r.def.Metadata.Name, "type": r.def.Spec.Type,
			"parameters": r.eff.TypeParameters(),
		},
		"execution": map[string]any{"id": r.x.ID, "trigger": r.x.Trigger, "attempt": r.attempt + 1,
			"timeoutAt": r.x.TimeoutAt},
		"paths": map[string]any{
			"executionDir": r.dir, "workspace": r.work, "report": filepath.Join(r.dir, "report.json"),
			"progress": filepath.Join(r.dir, "progress.jsonl"), "repositories": r.repositoryPaths(),
		},
		"workItems":             items,
		"interruptedExecutions": interrupted,
		"recentExecutions":      history,
		"previousAttempts":      prev,
	}
	data, err := json.MarshalIndent(ctxDoc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.dir, "context.json"), data, 0o600)
}

func (r *run) repositoryPaths() map[string]string {
	out := map[string]string{}
	for i, repo := range r.eff.Repositories() {
		if i < len(r.extra) {
			out[repo.Name] = r.extra[i]
		}
	}
	return out
}

// attempts runs the harness chain: quota exhaustion and an unavailable harness switch to the next profile, a rate
// limit retries on the same one, and everything shares the execution's one timeout (D-067). start runs once, when the
// first harness of the chain is found ready, so an execution with none ready never starts (FS-004 FR-003). It
// returns a terminal status when the attempts themselves decide it, or "" when the report decides.
func (r *run) attempts(start func() error) (domain.ExecutionStatus, string) {
	a := r.a
	chain, invalid := r.chain()
	for _, w := range invalid {
		r.warn(domain.Warning{Code: domain.WarningInvalidFallback, Message: w}, domain.EventHarnessNoAlternative, false)
	}
	fallback := r.eff.Spec.Harness.Fallback
	idx, rateRetries := 0, 0
	reason := "first attempt"
	var prev []previousAttempt
	started := false
	for {
		if r.cancelRequested() {
			return domain.StatusCancelled, "cancelled"
		}
		if r.ctx.Err() != nil {
			return domain.StatusFailed, "the execution timeout elapsed"
		}
		profile := chain[idx]
		ad := a.adapters[profile.Provider]
		info := a.detect(r.ctx, profile.Provider) // revalidate before every attempt (FS-004 FR-003)
		var end domain.AttemptEnd
		if !info.Ready() {
			end = domain.AttemptEnd{Class: domain.ClassHarnessUnavailable, Message: unavailableMessage(ad, info)}
			r.event(domain.Event{Type: domain.EventHarnessUnavailable, Harness: profile.Provider, Severity: domain.SeverityWarning,
				Message: end.Message, Payload: map[string]any{"readiness": info.Readiness}})
		} else {
			if !started {
				if err := start(); err != nil {
					return domain.StatusFailed, err.Error()
				}
				started = true
			}
			if err := r.writeContext(prev); err != nil {
				return domain.StatusFailed, "writing context.json: " + err.Error()
			}
			end = r.runAttempt(ad, info, profile, reason, prev)
			if end.Class == domain.ClassCancelled {
				return domain.StatusCancelled, "cancelled"
			}
			if end.Class == domain.ClassTimeout {
				return domain.StatusFailed, "the execution timeout elapsed"
			}
			a.mu.Lock()
			at := r.x.CurrentAttempt()
			prev = append(prev, previousAttempt{Number: at.Number, Harness: at.Harness, Model: at.Model,
				Outcome: string(at.Outcome), Error: end.Message, LastOperation: r.lastOperation,
				LastMessage: trimTo(end.FinalText, 1500)})
			a.mu.Unlock()
		}
		a.mu.Lock()
		switches := r.x.HarnessSwitches
		a.mu.Unlock()
		action := domain.DecideAfterAttempt(domain.FailoverInput{
			End: end, FallbackOn: fallback.On, HasNextCandidate: idx+1 < len(chain), Switches: switches,
			MaxSwitches: *fallback.MaxSwitches, RateLimitRetries: rateRetries,
			MaxRateRetries: *r.eff.Spec.Execution.RateLimit.MaxRetries,
		})
		switch action {
		case domain.ActionFinish:
			r.finalText = end.FinalText
			return "", ""
		case domain.ActionRetrySame:
			rl := r.eff.Spec.Execution.RateLimit
			wait := domain.RateLimitWait(rateRetries, end.RetryAfter, time.Duration(*rl.InitialWaitSeconds)*time.Second,
				time.Duration(*rl.MaxWaitSeconds)*time.Second)
			rateRetries++
			r.event(domain.Event{Type: domain.EventHarnessRateLimited, Harness: profile.Provider, Severity: domain.SeverityWarning,
				Message: fmt.Sprintf("rate limited; retrying on %s in %s (retry %d of %d)", ad.Name(), wait.Round(time.Second),
					rateRetries, *rl.MaxRetries), Payload: map[string]any{"waitSeconds": int(wait.Seconds()), "retry": rateRetries}})
			r.setStatus(domain.StatusRunning, "Waiting for the rate limit")
			select {
			case <-time.After(wait):
			case <-r.ctx.Done():
			}
			reason = "retry after a rate limit"
		case domain.ActionSwitch:
			next := chain[idx+1]
			if end.Class == domain.ClassQuotaExhausted {
				r.event(domain.Event{Type: domain.EventHarnessQuotaExhausted, Harness: profile.Provider, Severity: domain.SeverityWarning,
					Message: ad.Name() + " exhausted its quota", Payload: map[string]any{"error": end.Message}})
			}
			a.mu.Lock()
			r.x.HarnessSwitches++
			a.mu.Unlock()
			r.setStatus(domain.StatusSwitchingHarness, "Switching to "+a.adapters[next.Provider].Name())
			r.event(domain.Event{Type: domain.EventHarnessSwitched, Severity: domain.SeverityWarning,
				Message: fmt.Sprintf("switching from %s to %s", ad.Name(), a.adapters[next.Provider].Name()),
				Payload: map[string]any{"from": profile.Provider, "to": next.Provider, "reason": string(end.Class),
					"model": next.Model, "effort": next.Effort}})
			idx++
			rateRetries = 0
			reason = "failover after " + strings.ReplaceAll(string(end.Class), "_", " ")
		case domain.ActionNoAlternative:
			if end.Class == domain.ClassQuotaExhausted {
				r.event(domain.Event{Type: domain.EventHarnessQuotaExhausted, Harness: profile.Provider, Severity: domain.SeverityWarning,
					Message: ad.Name() + " exhausted its quota", Payload: map[string]any{"error": end.Message}})
			}
			msg := fmt.Sprintf("%s cannot continue (%s) and no alternative harness is available", ad.Name(),
				strings.ReplaceAll(string(end.Class), "_", " "))
			r.event(domain.Event{Type: domain.EventHarnessNoAlternative, Severity: domain.SeverityError, Message: msg,
				Payload: map[string]any{"harness": profile.Provider, "reason": string(end.Class), "error": end.Message}})
			return domain.StatusFailed, msg + ": " + end.Message
		default:
			msg := end.Message
			if msg == "" {
				msg = "the harness ended with an error"
			}
			return domain.StatusFailed, msg
		}
	}
}

func unavailableMessage(ad ports.HarnessAdapter, info ports.HarnessInfo) string {
	if info.Diagnostic != "" {
		return ad.Name() + " is unavailable: " + info.Diagnostic
	}
	return ad.Name() + " is unavailable (" + info.Readiness + ")"
}

// chain returns the preferred profile and the fallbacks that can load the task's agents and skills (D-092).
func (r *run) chain() ([]domain.HarnessProfile, []string) {
	profiles := r.def.Profiles()
	out := []domain.HarnessProfile{profiles[0]}
	var invalid []string
	for _, p := range profiles[1:] {
		ad, ok := r.a.adapters[p.Provider]
		if !ok {
			continue
		}
		if missing := r.a.missingItems(r.def, p.Provider); len(missing) > 0 {
			invalid = append(invalid, fmt.Sprintf("%s is not a valid fallback: %s %q cannot be found for it",
				ad.Name(), missing[0].kind, missing[0].name))
			continue
		}
		out = append(out, p)
	}
	return out, invalid
}

func (r *run) cancelRequested() bool {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return r.h.cancelRequested
}

// warn records a warning on the execution and emits it once.
func (r *run) warn(w domain.Warning, eventType string, emit bool) {
	r.a.mu.Lock()
	added := r.x.AddWarning(w)
	r.a.mu.Unlock()
	if added && emit {
		r.event(domain.Event{Type: eventType, Severity: domain.SeverityWarning, Message: w.Message,
			Payload: map[string]any{"code": w.Code, "subject": w.Subject}})
	}
}

// runAttempt starts one harness process and follows it to its end.
func (r *run) runAttempt(ad ports.HarnessAdapter, info ports.HarnessInfo, p domain.HarnessProfile, reason string,
	prev []previousAttempt) domain.AttemptEnd {
	a := r.a
	agents, skills, err := r.resolve(p.Provider)
	if err != nil {
		return domain.AttemptEnd{Class: domain.ClassTaskError, Message: err.Error()}
	}
	now := a.now()
	a.mu.Lock()
	n := len(r.x.Attempts) + 1
	r.attempt = n
	r.x.Attempts = append(r.x.Attempts, domain.Attempt{Number: n, Harness: p.Provider, HarnessVersion: info.Version,
		Model: p.Model, Effort: p.Effort, StartedAt: now, Outcome: domain.AttemptRunning, Reason: reason})
	r.x.Harness, r.x.Model = p.Provider, p.Model
	a.mu.Unlock()
	r.setStatus(domain.StatusRunning, "Starting "+ad.Name())
	r.save(true)

	req := ports.RunRequest{
		ExecutionID: r.x.ID, TaskID: r.x.TaskID, Attempt: n, Prompt: r.eff.Spec.Prompt,
		Context: r.harmflowContext(n, prev), Agents: agents, Skills: skills, Model: p.Model, Effort: p.Effort,
		WorkDir: r.work, AdditionalDirs: r.extra, Permissions: *r.eff.Spec.Permissions, ExecutionDir: r.dir,
		Env: r.env(),
	}
	inv, parser, err := ad.Prepare(info, req)
	if err != nil {
		end := domain.AttemptEnd{Class: domain.ClassTaskError, Message: err.Error()}
		r.endAttempt(end, -1)
		return end
	}
	for _, res := range inv.Restrictions {
		if !res.Enforced {
			r.warn(domain.Warning{Code: domain.WarningPolicyUnenforced, Subject: res.Name, Message: res.Message},
				domain.EventPolicyUnenforced, true)
		}
	}
	if info.VersionSupport == "untested_newer" {
		r.warn(domain.Warning{Code: domain.WarningVersionUntested, Subject: p.Provider,
			Message: fmt.Sprintf("%s %s is newer than the versions HarmFlow was tested with", ad.Name(), info.Version)},
			domain.EventHarnessVersionUntested, true)
	}
	raw, err := a.d.Events.RawWriter(r.x.ID, n)
	if err != nil {
		raw = nil
	}
	// The reader calls back in order and finishes before the process counts as done.
	proc, err := procutil.Start(inv, raw, func(line []byte) {
		for _, he := range parser.Line(line) {
			r.onHarnessEvent(he)
		}
	})
	if err != nil {
		if raw != nil {
			raw.Close()
		}
		end := domain.AttemptEnd{Class: domain.ClassHarnessUnavailable, Message: "starting " + ad.Name() + ": " + err.Error()}
		r.endAttempt(end, -1)
		return end
	}
	r.event(domain.Event{Type: domain.EventHarnessStarted, Harness: p.Provider,
		Message: fmt.Sprintf("%s %s started", ad.Name(), info.Version),
		Payload: map[string]any{"model": p.Model, "effort": p.Effort, "version": info.Version, "pid": proc.Pid(),
			"reason": reason, "path": info.Path}})

	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopProgress:
				r.readProgress()
				return
			case <-t.C:
				r.readProgress()
				r.save(false)
			}
		}
	}()
	grace := time.Duration(*r.eff.Spec.Execution.CancelGraceSeconds) * time.Second
	proc.Wait(r.ctx, grace)
	close(stopProgress)
	<-progressDone
	if raw != nil {
		raw.Close()
	}
	timedOut := errors.Is(r.ctx.Err(), context.DeadlineExceeded) && !r.cancelRequested()
	end := parser.Finish(proc.ExitCode(), proc.StderrTail(), timedOut, r.cancelRequested())
	r.endAttempt(end, proc.ExitCode())
	return end
}

func (r *run) endAttempt(end domain.AttemptEnd, exit int) {
	a := r.a
	now := a.now()
	a.mu.Lock()
	at := r.x.CurrentAttempt()
	if at == nil {
		a.mu.Unlock()
		return
	}
	at.EndedAt = &now
	if exit >= 0 {
		code := exit
		at.ExitCode = &code
	}
	at.SessionID = end.SessionID
	at.ErrorMessage = end.Message
	at.Outcome = attemptOutcome(end.Class)
	harness := at.Harness
	outcome := at.Outcome
	// Commands still marked running when the process ended did not complete.
	for i := range r.x.Commands {
		if r.x.Commands[i].Status == "running" && r.x.Commands[i].Attempt == at.Number {
			r.x.Commands[i].Status = "failed"
			r.x.Commands[i].EndedAt = &now
		}
	}
	a.mu.Unlock()
	sev := domain.SeverityInfo
	if end.Class != domain.ClassNone {
		sev = domain.SeverityWarning
	}
	r.event(domain.Event{Type: domain.EventHarnessExited, Harness: harness, Severity: sev,
		Message: fmt.Sprintf("%s ended: %s", harness, strings.ReplaceAll(string(outcome), "_", " ")),
		Payload: map[string]any{"exitCode": exit, "outcome": string(outcome), "error": end.Message}})
	r.save(true)
}

func attemptOutcome(c domain.ErrorClass) domain.AttemptOutcome {
	switch c {
	case domain.ClassNone:
		return domain.AttemptSucceeded
	case domain.ClassQuotaExhausted:
		return domain.AttemptQuotaExhausted
	case domain.ClassRateLimited:
		return domain.AttemptRateLimited
	case domain.ClassHarnessUnavailable:
		return domain.AttemptHarnessUnavailable
	case domain.ClassTimeout:
		return domain.AttemptTimedOut
	case domain.ClassCancelled:
		return domain.AttemptCancelled
	default:
		return domain.AttemptFailed
	}
}

// resolve finds the task's agents and skills for one harness.
func (r *run) resolve(harness string) ([]ports.ResolvedItem, []ports.ResolvedItem, error) {
	var agents, skills []ports.ResolvedItem
	for _, name := range r.eff.Spec.Agents {
		it, where, ok := r.a.resolveItem("agent", name, harness)
		if !ok {
			return nil, nil, fmt.Errorf("agent %q cannot be found in %s", name, where)
		}
		agents = append(agents, it)
	}
	for _, name := range r.eff.Spec.Skills {
		it, where, ok := r.a.resolveItem("skill", name, harness)
		if !ok {
			return nil, nil, fmt.Errorf("skill %q cannot be found in %s", name, where)
		}
		skills = append(skills, it)
	}
	return agents, skills, nil
}

// harmflowContext is HarmFlow's orchestration context for the session: where to find the task's parameters and
// history, and where to leave progress and the report. It never changes what the task does.
func (r *run) harmflowContext(attempt int, prev []previousAttempt) string {
	var b strings.Builder
	fmt.Fprintf(&b, `This session is run by HarmFlow, a local scheduler. HarmFlow only starts and records the session; it does not define the task. The task is the user's message: do what it asks, on its own terms. Never reinterpret it as being about HarmFlow, this note, or the files below — they are only the plumbing for reporting. Nobody reads this session live, so do not ask questions; decide, act, and report.

Plumbing for this run — execution %s, attempt %d, trigger %s, task id %s (type %s):
Workspace: %s
Execution directory: %s
- context.json: the task's parameters and history — its work items with their status, resumes, and failures, interrupted executions with their last operation, and previous attempts of this execution. Read it when the task works on items such as issues.
- progress.jsonl: append one JSON object per line as you reach each step, so the user can follow the execution. Types: step.started {"phase":"preparing|running|testing|publishing","message"}, work_item.selected {"workItem":"owner/repo#123","message":"<title>","repository"}, workspace.changed {"message","branch"}, test.started {"message":"<command>"}, test.completed {"status":"passed|failed|not_run","message"}, pull_request.created and pull_request.updated {"url","number","workItem","branch"}, repository.unprotected {"repository","message"}.
- report.json: write it once, at the end, following execution-report.schema.json in the same directory: {"outcome":"completed|no_work|needs_attention|failed","summary","lastOperation","workItems":[...],"warnings":[...]}. The summary is your answer to the task in a few sentences. Use no_work when nothing was eligible; it is not a failure.
`, r.x.ID, attempt, r.x.Trigger, r.def.Metadata.ID, r.def.Spec.Type, r.work, r.dir)
	if len(r.extra) > 0 {
		b.WriteString("Repositories, by local path:\n")
		for name, path := range r.repositoryPaths() {
			fmt.Fprintf(&b, "- %s: %s\n", name, path)
		}
	}
	if len(prev) > 0 {
		last := prev[len(prev)-1]
		fmt.Fprintf(&b, `
This is a new session continuing the same execution. The previous attempt ran on %s (%s) and ended: %s.`, last.Harness,
			last.Model, strings.ReplaceAll(last.Outcome, "_", " "))
		if last.LastOperation != "" {
			fmt.Fprintf(&b, " Its last recorded operation: %s.", last.LastOperation)
		}
		if last.LastMessage != "" {
			fmt.Fprintf(&b, " Its last message: %s", last.LastMessage)
		}
		b.WriteString(`
Continue from the current state of the workspace. Reconcile before repeating any external operation — a commit, a push, a pull request — so nothing is duplicated.
`)
	}
	return b.String()
}

// env is the environment of the harness process: the daemon's own, with a PATH that finds the user's tools, and
// HarmFlow's identifiers.
func (r *run) env() []string {
	base := r.a.d.HarnessEnv
	if base == nil {
		base = os.Environ()
	}
	out := make([]string, 0, len(base)+6)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "CLAUDECODE", k == "CLAUDE_CODE_ENTRYPOINT", k == "CLAUDE_CODE_SSE_PORT", strings.HasPrefix(k, "HARMFLOW_"):
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		"HARMFLOW_EXECUTION_ID="+r.x.ID,
		"HARMFLOW_TASK_ID="+r.x.TaskID,
		"HARMFLOW_EXECUTION_DIR="+r.dir,
		"HARMFLOW_WORKSPACE="+r.work,
	)
}

// onHarnessEvent turns the adapter's observations into events, commands, and changed files.
func (r *run) onHarnessEvent(he ports.HarnessEvent) {
	a := r.a
	now := a.now()
	switch he.Kind {
	case ports.HarnessStarted:
		a.mu.Lock()
		if at := r.x.CurrentAttempt(); at != nil && he.SessionID != "" {
			at.SessionID = he.SessionID
		}
		a.mu.Unlock()
		if he.Model != "" {
			r.event(domain.Event{Type: domain.EventHarnessOutput, Severity: domain.SeverityDebug,
				Message: "session " + he.SessionID + " on model " + he.Model,
				Payload: map[string]any{"sessionId": he.SessionID, "model": he.Model}})
		}
	case ports.HarnessMessage:
		r.event(domain.Event{Type: domain.EventHarnessMessage, Message: trimTo(he.Message, 8000)})
	case ports.HarnessToolStarted:
		title := he.Tool
		if he.Command != "" {
			title = he.Command
		} else if he.Message != "" {
			title = he.Tool + ": " + he.Message
		}
		a.mu.Lock()
		r.x.CurrentStep = trimTo(firstNonEmpty(he.Message, title), 160)
		if he.Command != "" {
			r.commandIndex[he.ToolID] = len(r.x.Commands)
			r.x.Commands = append(r.x.Commands, domain.CommandRecord{ID: he.ToolID, Command: trimTo(he.Command, 2000),
				Status: "running", StartedAt: now, Attempt: r.attempt})
		}
		a.mu.Unlock()
		r.event(domain.Event{Type: domain.EventToolStarted, Message: trimTo(title, 2000),
			Payload: map[string]any{"toolId": he.ToolID, "tool": he.Tool, "command": he.Command, "path": he.Path,
				"description": he.Message}})
	case ports.HarnessToolDone:
		status := "succeeded"
		if he.IsError {
			status = "failed"
		}
		a.mu.Lock()
		if i, ok := r.commandIndex[he.ToolID]; ok && i < len(r.x.Commands) {
			c := &r.x.Commands[i]
			c.Status = status
			c.EndedAt = &now
			c.ExitCode = he.ExitCode
			if !he.IsError {
				r.lastOperation = trimTo(c.Command, 300)
				r.x.LastOperation = r.lastOperation
			}
		}
		a.mu.Unlock()
		sev := domain.SeverityInfo
		if he.IsError {
			sev = domain.SeverityWarning
		}
		payload := map[string]any{"toolId": he.ToolID, "tool": he.Tool, "status": status, "output": he.Output}
		if he.ExitCode != nil {
			payload["exitCode"] = *he.ExitCode
		}
		r.event(domain.Event{Type: domain.EventToolCompleted, Severity: sev, Message: he.Tool + " " + status, Payload: payload})
	case ports.HarnessFileChanged:
		path := he.Path
		if strings.HasPrefix(filepath.Clean(path), r.dir+string(filepath.Separator)) {
			return // the session's progress and report, not a change to the workspace
		}
		if rel, err := filepath.Rel(r.work, path); err == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
		a.mu.Lock()
		added := r.x.AddChangedFile(path)
		a.mu.Unlock()
		if added {
			r.event(domain.Event{Type: domain.EventWorkspaceChanged, Message: path,
				Payload: map[string]any{"path": path, "change": he.Message}})
		}
	case ports.HarnessWarning:
		r.event(domain.Event{Type: domain.EventHarnessOutput, Severity: domain.SeverityDebug, Message: trimTo(he.Message, 2000)})
	case ports.HarnessRateLimit:
		r.event(domain.Event{Type: domain.EventHarnessOutput, Severity: domain.SeverityWarning, Message: he.Message})
	}
}

// readProgress reads the lines the session appended to progress.jsonl since the last read.
func (r *run) readProgress() {
	f, err := os.Open(filepath.Join(r.dir, "progress.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(r.progressOffset, 0); err != nil {
		return
	}
	rd := bufio.NewReader(f)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return // a partial last line is read on the next pass
		}
		r.progressOffset += int64(len(line))
		var p domain.ProgressLine
		if json.Unmarshal(line, &p) != nil {
			continue
		}
		r.onProgress(p)
	}
}

func (r *run) onProgress(p domain.ProgressLine) {
	a := r.a
	msg := trimTo(p.Message, 1000)
	payload := map[string]any{}
	for k, v := range map[string]string{"phase": p.Phase, "status": p.Status, "url": p.URL, "branch": p.Branch,
		"repository": p.Repository, "workItem": p.WorkItem} {
		if v != "" {
			payload[k] = v
		}
	}
	if p.Number > 0 {
		payload["number"] = p.Number
	}
	switch p.Type {
	case domain.EventStepStarted:
		if s := domain.ExecutionStatus(p.Phase); s == domain.StatusPreparing || s == domain.StatusRunning ||
			s == domain.StatusTesting || s == domain.StatusPublishing {
			r.setStatus(s, msg)
		} else {
			a.mu.Lock()
			r.x.CurrentStep = msg
			a.mu.Unlock()
		}
	case domain.EventWorkItemSelected:
		a.mu.Lock()
		r.x.WorkItem = p.WorkItem
		if p.Repository != "" {
			r.x.Repository = p.Repository
		}
		r.x.CurrentStep = msg
		a.mu.Unlock()
		a.trackWorkItem(r.x.TaskID, p.WorkItem, msg, p.Repository, r.x.ID)
	case domain.EventTestStarted:
		r.setStatus(domain.StatusTesting, msg)
	case domain.EventTestCompleted:
		a.mu.Lock()
		r.x.TestsStatus = p.Status
		a.mu.Unlock()
		r.setStatus(domain.StatusRunning, msg)
	case domain.EventPullRequestCreated, domain.EventPullRequestUpdated:
		a.mu.Lock()
		if p.URL != "" {
			r.x.PullRequestURL = p.URL
		}
		r.lastOperation = p.Type + " " + p.URL
		r.x.LastOperation = r.lastOperation
		a.mu.Unlock()
		r.setStatus(domain.StatusPublishing, msg)
	case domain.EventRepositoryUnprotected:
		r.warn(domain.Warning{Code: domain.WarningRepositoryUnprotected, Subject: p.Repository,
			Message: firstNonEmpty(msg, p.Repository+": the base branch is not protected against merging without review")},
			"", false)
	case domain.EventWorkspaceChanged:
		if p.Branch != "" {
			a.mu.Lock()
			r.lastOperation = msg
			r.x.LastOperation = msg
			a.mu.Unlock()
		}
	default:
		return
	}
	sev := domain.SeverityInfo
	if p.Type == domain.EventRepositoryUnprotected || (p.Type == domain.EventTestCompleted && p.Status == "failed") {
		sev = domain.SeverityWarning
	}
	r.event(domain.Event{Type: p.Type, WorkItemID: p.WorkItem, Severity: sev, Message: msg, Payload: payload})
}

// outcomeFromReport reads report.json: the session's agents and skills decide the outcome, the daemon records it.
func (r *run) outcomeFromReport() (domain.ExecutionStatus, string) {
	data, err := os.ReadFile(filepath.Join(r.dir, "report.json"))
	if err != nil {
		r.a.mu.Lock()
		r.x.Summary = trimTo(r.finalText, 4000)
		r.a.mu.Unlock()
		return domain.StatusCompleted, ""
	}
	value, err := schema.FromJSON(data)
	if err != nil {
		r.warn(domain.Warning{Code: domain.WarningOther, Message: "report.json is not valid JSON; it was ignored"}, "", false)
		return domain.StatusCompleted, ""
	}
	if issues := r.a.d.Validator.ValidateReport(value); len(issues) > 0 {
		r.warn(domain.Warning{Code: domain.WarningOther,
			Message: "report.json does not follow its schema (" + issues[0].Path + ": " + issues[0].Message + "); it was ignored"}, "", false)
		return domain.StatusCompleted, ""
	}
	var rep domain.ExecutionReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return domain.StatusCompleted, ""
	}
	r.applyReport(&rep)
	status := domain.StatusForReport(&rep)
	reason := ""
	if status == domain.StatusFailed || status == domain.StatusNeedsAttention {
		reason = trimTo(rep.Summary, 500)
	}
	return status, reason
}

func (r *run) applyReport(rep *domain.ExecutionReport) {
	a := r.a
	a.mu.Lock()
	r.x.Summary = trimTo(rep.Summary, 4000)
	if rep.LastOperation != "" {
		r.x.LastOperation = rep.LastOperation
	}
	r.x.WorkItems = append([]domain.WorkItemResult{}, rep.WorkItems...)
	for _, w := range rep.WorkItems {
		if r.x.WorkItem == "" {
			r.x.WorkItem = w.Key
		}
		if w.Repository != "" && r.x.Repository == "" {
			r.x.Repository = w.Repository
		}
		if w.PullRequest != nil && w.PullRequest.URL != "" {
			r.x.PullRequestURL = w.PullRequest.URL
		}
		if w.Tests != nil && w.Tests.Status != "" {
			r.x.TestsStatus = w.Tests.Status
		}
		for _, f := range w.ChangedFiles {
			r.x.AddChangedFile(f)
		}
	}
	a.mu.Unlock()
	for _, w := range rep.Warnings {
		code := domain.WarningOther
		eventType := ""
		if w.Code == "repository_unprotected" {
			code, eventType = domain.WarningRepositoryUnprotected, domain.EventRepositoryUnprotected
		}
		r.warn(domain.Warning{Code: code, Subject: w.Repository, Message: w.Message}, eventType, eventType != "")
	}
	limits := r.limits()
	for _, w := range rep.WorkItems {
		a.applyWorkItem(r.x.TaskID, w, r.x.ID, limits)
		if w.PullRequest != nil && w.PullRequest.URL != "" {
			t := domain.EventPullRequestUpdated
			if w.PullRequest.Created != nil && *w.PullRequest.Created {
				t = domain.EventPullRequestCreated
			}
			if !r.sawEvent(t, w.PullRequest.URL) {
				r.event(domain.Event{Type: t, WorkItemID: w.Key, Message: w.PullRequest.URL,
					Payload: map[string]any{"url": w.PullRequest.URL, "number": w.PullRequest.Number, "branch": w.Branch}})
			}
		}
		if w.Tests != nil && w.Tests.Status == "failed" {
			r.event(domain.Event{Type: domain.EventTestFailed, WorkItemID: w.Key, Severity: domain.SeverityWarning,
				Message: firstNonEmpty(w.Tests.Summary, "tests failed; the draft pull request documents them"),
				Payload: map[string]any{"url": prURL(w)}})
		}
	}
}

func prURL(w domain.WorkItemResult) string {
	if w.PullRequest != nil {
		return w.PullRequest.URL
	}
	return ""
}

// sawEvent reports whether the session already announced a pull request through progress.jsonl.
func (r *run) sawEvent(eventType, url string) bool {
	events, _, err := r.a.d.Events.Replay(r.x.ID, 0, 0)
	if err != nil {
		return false
	}
	for _, e := range events {
		if e.Type == eventType && (e.Payload["url"] == url || e.Message == url) {
			return true
		}
	}
	return false
}

func (r *run) limits() domain.WorkItemLimits {
	l := domain.WorkItemLimits{MaxResumeAttempts: domain.DefaultMaxResumeAttempts, MaxFailedRetries: domain.DefaultMaxFailedRetries}
	if g := r.eff.Spec.GitHubIssueImplementation; g != nil && g.Queue != nil {
		l.MaxResumeAttempts, l.MaxFailedRetries = *g.Queue.MaxResumeAttempts, *g.Queue.MaxFailedRetries
	}
	return l
}

func (a *App) finish(r *run, status domain.ExecutionStatus, reason string) {
	r.finish(status, reason)
}

// finish records the terminal state and its event. A failed or interrupted work item counts toward its retry limit.
func (r *run) finish(status domain.ExecutionStatus, reason string) {
	a := r.a
	now := a.now()
	a.mu.Lock()
	if r.x.Status.IsTerminal() {
		a.mu.Unlock()
		return
	}
	r.x.Status = status
	r.x.EndedAt = &now
	if reason != "" && (status == domain.StatusFailed || status == domain.StatusCancelled || status == domain.StatusNeedsAttention) {
		r.x.ErrorMessage = trimTo(reason, 2000)
	}
	if status == domain.StatusCancelled && r.x.CancelRequestedAt == nil && r.h != nil && r.h.cancelRequested {
		r.x.CancelRequestedAt = &now
	}
	r.x.CurrentStep = ""
	workItem := r.x.WorkItem
	hasResult := len(r.x.WorkItems) > 0
	a.mu.Unlock()
	if (status == domain.StatusFailed || status == domain.StatusInterrupted) && workItem != "" && !hasResult {
		a.failWorkItem(r.x.TaskID, workItem, r.x.ID, r.lastOperation, r.limitsOrDefault())
	}
	r.save(true)
	sev := domain.SeverityInfo
	switch status {
	case domain.StatusFailed, domain.StatusInterrupted:
		sev = domain.SeverityError
	case domain.StatusNeedsAttention, domain.StatusCancelled:
		sev = domain.SeverityWarning
	}
	a.mu.Lock()
	payload := map[string]any{"status": string(status), "testsStatus": r.x.TestsStatus, "pullRequestUrl": r.x.PullRequestURL,
		"summary": r.x.Summary, "error": r.x.ErrorMessage, "attempts": len(r.x.Attempts)}
	a.mu.Unlock()
	r.event(domain.Event{Type: domain.TerminalEventFor(status), Severity: sev, Message: firstNonEmpty(reason, string(status)),
		Payload: payload})
}

func (r *run) limitsOrDefault() domain.WorkItemLimits {
	if r.eff.Spec.Type == "" {
		return domain.WorkItemLimits{MaxResumeAttempts: domain.DefaultMaxResumeAttempts, MaxFailedRetries: domain.DefaultMaxFailedRetries}
	}
	return r.limits()
}

// CancelExecution records the intention durably and ends the execution's process tree after the grace period; no
// other execution is affected (FS-003 FR-004, FR-005).
func (a *App) CancelExecution(id string) error {
	a.mu.Lock()
	x, ok := a.executions[id]
	if !ok {
		a.mu.Unlock()
		return errf(CodeNotFound, "no execution %s", id)
	}
	if x.Status.IsTerminal() {
		a.mu.Unlock()
		return errf(CodeConflict, "the execution already ended as %s", x.Status)
	}
	now := a.now()
	if x.Status == domain.StatusQueued {
		x.Status = domain.StatusCancelled
		x.EndedAt = &now
		x.CancelRequestedAt = &now
		a.mu.Unlock()
		a.persistExecution(x)
		a.emit(domain.Event{Type: domain.EventExecutionCancelled, TaskID: x.TaskID, ExecutionID: x.ID,
			Message: "cancelled before it started", Payload: map[string]any{"status": string(domain.StatusCancelled)}})
		return nil
	}
	h := a.running[x.TaskID]
	x.CancelRequestedAt = &now
	a.mu.Unlock()
	a.persistExecution(x)
	a.emit(domain.Event{Type: domain.EventExecutionCancelRequest, TaskID: x.TaskID, ExecutionID: x.ID,
		Message: "cancellation requested"})
	if h != nil {
		a.mu.Lock()
		h.cancelRequested = true
		cancel := h.cancel
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
