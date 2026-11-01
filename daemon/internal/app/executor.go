package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/contracts"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// run is the state of one execution while it runs. Its definition is the snapshot taken when it started.
type run struct {
	a        *App
	x        *domain.Execution
	h        *runHandle
	def      domain.TaskDocument
	eff      domain.TaskDocument
	dir      string
	work     string
	extra    []string
	writable []string // a custom profile's writable paths, resolved
	saveMu   sync.Mutex
	lastSave time.Time
	// saved is the revision of the record the last save wrote, once written is set; both are guarded by saveMu.
	saved   uint64
	written bool

	progressOffset int64
	commandIndex   map[string]int
	lastOperation  string
	attempt        int
	items          []attemptWorkItem // the work items the current attempt works, guarded by a.mu
	finalText      string            // the agent's last message, the summary when the session leaves no report
	hasContext     bool              // context.json holds parameters or history the session can use, not only the task's identity
	// lineOperation is set while the last operation is a pull request a progress line recorded, and lineCommands is how
	// many commands had started when that line was read; both are guarded by the App's mutex.
	lineOperation bool
	lineCommands  int
	// ownEnd is set while the outcome is the end of an attempt whose harness exited on its own: a daemon that began
	// stopping while what it left running was ended does not make that outcome interrupted.
	ownEnd bool
	// stopAttempt ends the running attempt alone, and extraUsage is set once it was ended for turning to extra usage
	// that its task does not allow. Both are set before the attempt's harness starts.
	stopAttempt context.CancelFunc
	extraUsage  atomic.Bool
	// selected holds every work item the session selected, in order, each with what the ledger held for it before;
	// guarded by the App's mutex.
	selected []selection
	// announced holds the pull requests the execution's events announced, in every attempt, by event type and by their
	// URL or message, so the report announces none again; guarded by the App's mutex.
	announced map[announcement]bool
}

// announcement is a pull request event of an execution: its type, and a URL or message it carries.
type announcement struct{ eventType, text string }

// selectionOf is what the ledger held for key when the session selected it, nil when the session did not select it.
func (r *run) selectionOf(key string) *selection {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	for i := range r.selected {
		if r.selected[i].key == key {
			s := r.selected[i]
			return &s
		}
	}
	return nil
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
			if err := a.removeTask(x.TaskID); err != nil {
				a.log.Printf("deleting task %s once its execution ended: %v", x.TaskID, err)
			}
		}
		a.startQueued()
	}()

	a.mu.Lock()
	e := a.tasks[x.TaskID]
	if e == nil {
		a.mu.Unlock()
		a.finish(&run{a: a, x: x, h: h}, domain.StatusCancelled, domain.T("task_gone", "the task no longer exists"))
		return
	}
	def := e.doc
	rev := e.file.Revision
	a.mu.Unlock()

	eff := def.Effective()
	now := a.now()
	timeout := time.Duration(*eff.Spec.Execution.TimeoutMinutes) * a.d.TimeoutUnit
	deadline := now.Add(timeout)
	// The timeout runs on real time; the record shows it on the daemon's clock. An execution outlives whatever fired it
	// (an API request, a tick of the scheduler), so its context starts from none of theirs: it ends by its timeout, or by
	// its handle's cancel, which cancellation and Stop call.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	a.mu.Lock()
	h.cancel = cancel
	cancelled := h.cancelRequested
	if a.stopping {
		cancel() // the daemon began stopping before this execution could be cancelled with the others
	}
	a.mu.Unlock()

	r := &run{a: a, x: x, h: h, def: def, eff: eff, commandIndex: map[string]int{}}
	a.mu.Lock()
	x.DefinitionRevision = rev
	x.TaskName = def.Metadata.Name
	x.TaskType = def.Spec.Type
	x.Changed()
	a.mu.Unlock()
	if cancelled {
		r.finish(domain.StatusCancelled, domain.T("cancelled_before_start", "cancelled before it started"))
		return
	}
	if def.IsScript() {
		// A program of the user's, with no harness to check or fail over to.
		if !r.setStatus(domain.StatusPreparing, domain.T("step_preparing_program", "Preparing the program")) {
			return
		}
		r.finish(r.runScript(ctx, deadline))
		return
	}
	// Leave queued before the harness check: the scheduler and cancellation take a queued execution for a waiting firing.
	// One cancelled or dropped while still queued, after its task was claimed for it, has already ended: it never starts.
	if !r.setStatus(domain.StatusPreparing, domain.T("step_checking_harness", "Checking the harness")) {
		return
	}
	status, reason := r.attempts(ctx, func(harness string) error {
		started := a.now()
		a.mu.Lock()
		x.StartedAt = &started
		x.TimeoutAt = &deadline
		x.Changed()
		a.mu.Unlock()
		r.setStatus(domain.StatusPreparing, domain.T("step_preparing_workspace", "Preparing the workspace"))
		// No attempt exists yet to name the harness, so the event names it itself.
		r.event(domain.Event{Type: domain.EventExecutionStarted, Harness: harness, Message: def.Metadata.Name,
			Payload: map[string]any{"trigger": string(x.Trigger), "timeoutAt": deadline, "definitionRevision": rev}})
		return r.prepare()
	})
	switch {
	case r.daemonStopping() && !r.cancelRequested() && !r.ownEnd:
		// A daemon that stops never continues the execution: it is interrupted, even when the harness it ended exited as
		// one that finished, with code 0 and a final message.
		status, reason = domain.StatusInterrupted, daemonStopped()
	case status == "":
		status, reason = r.outcomeFromReport()
	}
	r.finish(status, reason)
}

func (r *run) daemonStopping() bool {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return r.a.stopping
}

// event emits an event of the execution; one that names no work item takes the execution's current one.
func (r *run) event(e domain.Event) domain.Event {
	if e.WorkItemID == "" {
		r.a.mu.Lock()
		e.WorkItemID = r.x.WorkItem
		r.a.mu.Unlock()
	}
	return r.emit(e)
}

// emit emits an event of the execution under the work item it names, and under none when it names none.
func (r *run) emit(e domain.Event) domain.Event {
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
	stored := r.a.emitFor(e, r.started())
	if stored.Type == domain.EventPullRequestCreated || stored.Type == domain.EventPullRequestUpdated {
		url, _ := stored.Payload["url"].(string)
		r.a.mu.Lock()
		if r.announced == nil {
			r.announced = map[announcement]bool{}
		}
		for _, text := range []string{url, stored.Message} {
			if text != "" {
				r.announced[announcement{stored.Type, text}] = true
			}
		}
		r.a.mu.Unlock()
	}
	return stored
}

// started is the definition the execution started with, or nil when it ended before it took one: its task was gone.
func (r *run) started() *domain.TaskDocument {
	if r.def.Metadata.ID == "" {
		return nil
	}
	return &r.def
}

// save persists the record. Unforced, it saves at most every second, and only a record that changed since the last save
// wrote it. What recovery reads from the record, the work item and the last operation, is saved at once, before the
// event that logs it, so a crash never leaves the record behind the log.
func (r *run) save(force bool) {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	if !force && (time.Since(r.lastSave) < time.Second || r.unchanged()) {
		return
	}
	r.lastSave = time.Now()
	if revision, ok := r.a.persistExecution(r.x); ok {
		r.saved, r.written = revision, true
	}
}

// unchanged reports whether the last save wrote the record as it is now; the caller holds saveMu.
func (r *run) unchanged() bool {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return r.written && r.x.Revision() == r.saved
}

// setStatus moves the execution to s and records the step, unless it is empty. It changes nothing and returns false
// once the execution has ended.
func (r *run) setStatus(s domain.ExecutionStatus, step domain.Text) bool {
	r.a.mu.Lock()
	from := r.x.Status
	if from.IsTerminal() {
		r.a.mu.Unlock()
		return false
	}
	if from == s || from.CanTransition(s) != nil {
		if step.Message != "" {
			r.x.SetStep(step)
		}
		r.a.mu.Unlock()
		return true
	}
	r.x.Status = s
	r.x.Changed()
	if step.Message != "" {
		r.x.SetStep(step)
	}
	r.a.mu.Unlock()
	r.save(true)
	r.event(domain.Event{Type: domain.EventExecutionStatusChanged,
		Payload: map[string]any{"from": string(from), "status": string(s)}}.Says(step))
	return true
}

// permissions are the task's, with a custom profile's writable paths as prepare resolved them.
func (r *run) permissions() domain.Permissions {
	p := *r.eff.Spec.Permissions
	if p.Custom != nil && len(p.Custom.WritablePaths) > 0 {
		c := *p.Custom
		c.WritablePaths = append([]string{}, r.writable...)
		p.Custom = &c
	}
	return p
}

// prepare creates the execution directory and the workspace, and writes context.json for the session.
func (r *run) prepare() error {
	a := r.a
	r.dir = filepath.Join(a.d.Paths.Runs, r.x.ID)
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return domain.TextErr("execution_dir_failed", "creating the execution directory: {{detail}}", "detail", err.Error())
	}
	// The paths are checked again as the execution starts, and the harness gets them resolved, so a symlink retargeted
	// after saving neither leads out nor differs from what was checked.
	pe := a.currentPathEnv()
	if issues := a.taskPathIssues(r.def, pe); len(issues) > 0 {
		return &domain.TextError{Text: refusedPath(issues[0])}
	}
	ws, dirs := a.taskFolders(r.eff, r.x.TaskID, pe)
	if ws.err != nil {
		return ws.err
	}
	work := ws.path
	if err := os.MkdirAll(work, 0o700); err != nil {
		return domain.TextErr("workspace_create_failed", "creating the workspace {{path}}: {{detail}}", "path", work,
			"detail", err.Error())
	}
	r.work = work
	if c := r.eff.Spec.Permissions.Custom; c != nil {
		for _, w := range c.WritablePaths {
			resolved, err := pe.writablePath(a, w)
			if err != nil {
				return err
			}
			r.writable = append(r.writable, resolved)
		}
	}
	for i, repo := range r.eff.Repositories() {
		if dirs[i].err != nil {
			return dirs[i].err
		}
		p := dirs[i].path
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return domain.TextErr("repository_path_missing", "the local path {{path}} of {{repository}} does not exist",
				"path", p, "repository", repo.Name)
		}
		r.extra = append(r.extra, p)
	}
	a.mu.Lock()
	r.x.WorkspacePath = work
	r.x.ExecutionDir = r.dir
	if repos := r.eff.Repositories(); len(repos) == 1 {
		r.x.Repository = repos[0].Name
	}
	r.x.Changed()
	a.mu.Unlock()
	// The schema only lets the session check its report before it writes it; the daemon validates the report itself.
	_ = os.WriteFile(filepath.Join(r.dir, "execution-report.schema.json"), contracts.ExecutionReportSchema, 0o600)
	// What each field of context.json holds, for the session to read beside it.
	_ = os.WriteFile(filepath.Join(r.dir, "context.schema.json"), contracts.ContextSchema, 0o600)
	return r.writeContext(nil)
}

type previousAttempt struct {
	Number        int    `json:"number"`
	Harness       string `json:"harness"`
	Model         string `json:"model,omitempty"`
	Outcome       string `json:"outcome"`
	Error         string `json:"error,omitempty"`
	LastOperation string `json:"lastOperation,omitempty"`
	LastMessage   string `json:"lastMessage,omitempty"`
	// WorkItems are the work items the attempt worked, with what it reported of each and what its checkpoint found.
	WorkItems []attemptWorkItem `json:"workItems,omitempty"`
	// checkpoint is the files the attempt left changed, which the next attempt's session context names; context.json
	// carries them only by work item.
	checkpoint *domain.Checkpoint
}

// writeContext gives the session the task's parameters and history: what the skills need to skip processed work,
// resume, and reconcile.
func (r *run) writeContext(prev []previousAttempt) error {
	a := r.a
	a.mu.Lock()
	items := []domain.WorkItem{}
	for _, k := range sortedKeys(a.ledger[r.x.TaskID]) {
		items = append(items, *a.ledger[r.x.TaskID][k])
	}
	var interrupted []map[string]any
	var history []map[string]any
	a.eachSummaryLocked(func(x *domain.ExecutionSummary) {
		if x.TaskID != r.x.TaskID || x.ID == r.x.ID || !x.Status.IsTerminal() {
			return
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
	})
	a.mu.Unlock()
	sort.Slice(history, func(i, j int) bool {
		return history[i]["createdAt"].(time.Time).After(history[j]["createdAt"].(time.Time))
	})
	if len(history) > 50 {
		history = history[:50]
	}
	params := r.eff.TypeParameters()
	// Without any of these the file holds only the task's own name and past runs, which a session can mistake for
	// the subject of the prompt.
	r.hasContext = params != nil || len(items) > 0 || len(interrupted) > 0 || len(prev) > 0
	ctxDoc := map[string]any{
		"schemaVersion": "1",
		"harmflow":      map[string]any{"version": a.d.Version},
		"task": map[string]any{
			"id": r.def.Metadata.ID, "name": r.def.Metadata.Name, "type": r.def.Spec.Type,
			"parameters": params,
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
// limit retries on the same one, and everything shares the execution's one timeout. start runs once, when the first
// harness of the chain is found ready, and is given that harness, so an execution with none ready never starts. It
// returns a terminal status when the attempts themselves decide it, or "" when the report decides.
func (r *run) attempts(ctx context.Context, start func(harness string) error) (domain.ExecutionStatus, domain.Text) {
	a := r.a
	chain, invalid := r.chain(ctx)
	for _, w := range invalid {
		r.warn(domain.Warning{Code: domain.WarningInvalidFallback, Text: w}, domain.EventHarnessNoAlternative, false)
	}
	fallback := r.eff.Spec.Harness.Fallback
	idx, rateRetries := 0, 0
	reason := domain.T("attempt_first", "first attempt")
	var prev []previousAttempt
	started := false
	for {
		r.ownEnd = false
		if r.cancelRequested() {
			return domain.StatusCancelled, domain.T("execution_cancelled", "cancelled")
		}
		if ctx.Err() != nil {
			return domain.StatusFailed, domain.T("execution_timed_out", "the execution timeout elapsed")
		}
		profile := chain[idx]
		ad := a.adapters[profile.Provider]
		info := a.detect(ctx, profile.Provider) // revalidate before every attempt
		var end domain.AttemptEnd
		var mark *ports.WorkspaceMark // set when an attempt ran in this pass and a switch can follow it
		if !info.Ready() {
			end = domain.AttemptEnd{Class: domain.ClassHarnessUnavailable, Error: unavailableText(ad, info)}
			r.event(domain.Event{Type: domain.EventHarnessUnavailable, Harness: profile.Provider, Severity: domain.SeverityWarning,
				Payload: map[string]any{"readiness": info.Readiness}}.Says(end.Error))
		} else if held, ok := r.quotaHeld(ctx, ad, info); ok {
			// No session opens on an exhausted quota: it fails over as an unavailable harness does.
			end = held
		} else {
			if !started {
				if err := start(profile.Provider); err != nil {
					return domain.StatusFailed, domain.TextOf(err)
				}
				started = true
			}
			if err := r.writeContext(prev); err != nil {
				return domain.StatusFailed, domain.T("context_write_failed", "writing context.json: {{detail}}", "detail", err.Error())
			}
			mark = r.markWorkspace(ctx, idx+1 < len(chain))
			var timedOut bool
			end, timedOut = r.runAttempt(ctx, ad, info, profile, reason, prev)
			if end.Class == domain.ClassCancelled {
				return domain.StatusCancelled, domain.T("execution_cancelled", "cancelled")
			}
			// The attempt's outcome may name another cause, and the execution still fails for its timeout.
			if end.Class == domain.ClassTimeout || timedOut {
				return domain.StatusFailed, domain.T("execution_timed_out", "the execution timeout elapsed")
			}
			a.mu.Lock()
			at := r.x.CurrentAttempt()
			prev = append(prev, previousAttempt{Number: at.Number, Harness: at.Harness, Model: at.Model,
				Outcome: string(at.Outcome), Error: end.Error.Message, LastOperation: r.lastOperation,
				LastMessage: trimTo(end.FinalText, 1500), WorkItems: slices.Clone(r.items)})
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
			return "", domain.Text{}
		case domain.ActionRetrySame:
			rl := r.eff.Spec.Execution.RateLimit
			wait := domain.RateLimitWait(rateRetries, end.RetryAfter, time.Duration(*rl.InitialWaitSeconds)*time.Second,
				time.Duration(*rl.MaxWaitSeconds)*time.Second)
			rateRetries++
			r.event(domain.Event{Type: domain.EventHarnessRateLimited, Harness: profile.Provider, Severity: domain.SeverityWarning,
				Payload: map[string]any{"waitSeconds": int(wait.Seconds()), "retry": rateRetries}}.Says(domain.T("harness_rate_limited",
				"rate limited; retrying on {{harness}} in {{wait}} (retry {{retry}} of {{max}})", "harness", ad.Name(),
				"wait", wait.Round(time.Second).String(), "retry", strconv.Itoa(rateRetries), "max", strconv.Itoa(*rl.MaxRetries))))
			r.setStatus(domain.StatusRunning, domain.T("step_waiting_rate_limit", "Waiting for the rate limit"))
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
			reason = domain.T("attempt_rate_limit_retry", "retry after a rate limit")
		case domain.ActionSwitch:
			next := chain[idx+1]
			nextName := a.adapters[next.Provider].Name()
			if end.Class == domain.ClassQuotaExhausted {
				r.quotaExhausted(ad, profile.Provider, end)
			}
			if mark != nil {
				if c := r.readCheckpoint(ctx, *mark); c != nil {
					eventlog.RedactFields(c) // its paths, kept and sent on like the harness's own text
					r.keepCheckpoint(c, &prev[len(prev)-1])
				}
				r.placeWorkItems(ctx, &prev[len(prev)-1])
			}
			a.mu.Lock()
			r.x.HarnessSwitches++
			r.x.Changed()
			a.mu.Unlock()
			r.setStatus(domain.StatusSwitchingHarness, domain.T("step_switching_harness", "Switching to {{harness}}",
				"harness", nextName))
			r.event(domain.Event{Type: domain.EventHarnessSwitched, Severity: domain.SeverityWarning,
				Payload: map[string]any{"from": profile.Provider, "to": next.Provider, "reason": string(end.Class),
					"configuredModel": next.Model, "configuredEffort": next.Effort}}.Says(domain.T("harness_switched",
				"switching from {{from}} to {{to}}", "from", ad.Name(), "to", nextName)))
			idx++
			rateRetries = 0
			reason = domain.T("attempt_failover", "failover after {{reason}}", classParams(end.Class)...)
		case domain.ActionNoAlternative:
			if end.Class == domain.ClassQuotaExhausted {
				r.quotaExhausted(ad, profile.Provider, end)
			}
			r.event(domain.Event{Type: domain.EventHarnessNoAlternative, Severity: domain.SeverityError,
				Payload: domain.ErrorPayload(map[string]any{"harness": profile.Provider, "reason": string(end.Class)},
					end.Error)}.Says(domain.T("no_alternative_harness",
				"{{harness}} cannot continue ({{reason}}) and no alternative harness is available",
				append([]string{"harness", ad.Name()}, classParams(end.Class)...)...)))
			return domain.StatusFailed, domain.T("no_alternative_harness_detail",
				"{{harness}} cannot continue ({{reason}}) and no alternative harness is available: {{detail}}",
				append([]string{"harness", ad.Name()}, classParams(end.Class)...)...).Nest("detail", end.Error)
		default:
			if end.Error.Message == "" {
				return domain.StatusFailed, domain.T("harness_ended_with_error", "the harness ended with an error")
			}
			return domain.StatusFailed, end.Error
		}
	}
}

// classParams name an error class: in words for the en-US text, and as the value the desktop translates.
func classParams(c domain.ErrorClass) []string {
	return []string{"reason", strings.ReplaceAll(string(c), "_", " "), "errorClass", string(c)}
}

// unavailableText says why a harness cannot run, quoting its diagnostic.
func unavailableText(ad ports.HarnessAdapter, info ports.HarnessInfo) domain.Text {
	if info.Diagnostic != "" {
		return domain.T("harness_unavailable", "{{harness}} is unavailable: {{detail}}", "harness", ad.Name()).
			Nest("detail", info.DiagnosticText())
	}
	return domain.T("harness_unavailable_readiness", "{{harness}} is unavailable ({{readiness}})", "harness", ad.Name(),
		"readiness", info.Readiness)
}

func (r *run) quotaExhausted(ad ports.HarnessAdapter, harness string, end domain.AttemptEnd) {
	payload := map[string]any{}
	if !end.ResetsAt.IsZero() {
		payload["resetsAt"] = end.ResetsAt
	}
	r.event(domain.Event{Type: domain.EventHarnessQuotaExhausted, Harness: harness, Severity: domain.SeverityWarning,
		Payload: domain.ErrorPayload(payload, end.Error)}.Says(domain.T("harness_quota_exhausted",
		"{{harness}} exhausted its quota", "harness", ad.Name())))
}

// chain returns the preferred profile and the fallbacks that can load the task's agents and skills.
func (r *run) chain(ctx context.Context) ([]domain.HarnessProfile, []domain.Text) {
	profiles := r.def.Profiles()
	out := []domain.HarnessProfile{profiles[0]}
	var invalid []domain.Text
	for _, p := range profiles[1:] {
		ad, ok := r.a.adapters[p.Provider]
		if !ok {
			continue
		}
		if missing := r.a.missingItems(ctx, r.def, p.Provider, r.itemWorkspace(), r.addedFolders()); len(missing) > 0 {
			invalid = append(invalid, missing[0].invalidFallback(ad.Name()))
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

// warn records a warning on the execution and emits it once, under the work item it names or, when it names none, the
// execution's current one.
func (r *run) warn(w domain.Warning, eventType string, emit bool) {
	if r.addWarning(w) && emit {
		r.event(warningEvent(w, eventType))
	}
}

// addWarning records a warning on the execution, and reports whether it was not recorded yet.
func (r *run) addWarning(w domain.Warning) bool {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return r.x.AddWarning(w)
}

// warningEvent is the event of a warning, under the work item the warning names.
func warningEvent(w domain.Warning, eventType string) domain.Event {
	return domain.Event{Type: eventType, WorkItemID: w.WorkItem, Severity: domain.SeverityWarning,
		Payload: map[string]any{"code": w.Code, "subject": w.Subject}}.Says(w.Text)
}

// runAttempt starts one harness process and follows it to its end. It also reports whether the execution's timeout
// ended the process, which the attempt's class does not always say: an attempt the timeout cut short while its harness
// waited for the network is a network failure.
func (r *run) runAttempt(ctx context.Context, ad ports.HarnessAdapter, info ports.HarnessInfo, p domain.HarnessProfile, reason domain.Text,
	prev []previousAttempt) (domain.AttemptEnd, bool) {
	a := r.a
	agents, skills, err := r.resolve(ctx, ad)
	if err != nil {
		return domain.AttemptEnd{Class: domain.ClassTaskError, Error: domain.TextOf(err)}, false
	}
	now := a.now()
	a.mu.Lock()
	n := len(r.x.Attempts) + 1
	r.attempt = n
	r.items = r.continuedItems(prev)
	// The model and effort used stay unreported until the harness reports them.
	at := domain.Attempt{Number: n, Harness: p.Provider, HarnessVersion: info.Version, ConfiguredModel: p.Model,
		ConfiguredEffort: p.Effort, StartedAt: now, Outcome: domain.AttemptRunning}
	at.SetReason(reason)
	r.x.Attempts = append(r.x.Attempts, at)
	r.x.Harness, r.x.Model = p.Provider, ""
	r.x.Changed()
	a.mu.Unlock()
	r.setStatus(domain.StatusRunning, domain.T("step_starting_harness", "Starting {{harness}}", "harness", ad.Name()))
	r.save(true)

	req := ports.RunRequest{
		ExecutionID: r.x.ID, TaskID: r.x.TaskID, Attempt: n, Prompt: r.eff.Spec.Prompt,
		Context: r.harmflowContext(n, prev), Agents: agents, Skills: skills, Model: p.Model, Effort: p.Effort,
		KeepSession: p.KeepsSession(), WorkDir: r.work, AdditionalDirs: r.extra, Permissions: r.permissions(), ExecutionDir: r.dir,
		Env: r.env(),
	}
	// Recorded before Prepare, so a running attempt, and one whose harness never started, shows its input.
	a.mu.Lock()
	if at := r.x.CurrentAttempt(); at != nil {
		at.Prompt, at.SessionContext = eventlog.Redact(req.Prompt), eventlog.Redact(req.Context)
		r.x.Changed()
	}
	a.mu.Unlock()
	inv, parser, err := ad.Prepare(info, req)
	if err != nil {
		end := domain.AttemptEnd{Class: domain.ClassTaskError, Error: domain.TextOf(err)}
		r.endAttempt(end, -1)
		return end, false
	}
	inv.ExecutionID = r.x.ID
	for _, res := range inv.Restrictions {
		if !res.Enforced {
			r.warn(domain.Warning{Code: domain.WarningPolicyUnenforced, Subject: res.Name, Text: res.Text},
				domain.EventPolicyUnenforced, true)
		}
	}
	if !r.def.AllowsExtraUsage() && !reportsExtraUsage(ad) {
		r.warn(domain.Warning{Code: domain.WarningPolicyUnenforced, Subject: "allowExtraUsage",
			Text: extraUsageUnenforced(ad.Name())}, domain.EventPolicyUnenforced, true)
	}
	switch info.VersionSupport {
	case "untested_newer":
		r.warn(domain.Warning{Code: domain.WarningVersionUntested, Subject: p.Provider,
			Text: domain.T("harness_version_untested",
				"{{harness}} {{version}} is newer than the versions HarmFlow was tested with",
				"harness", ad.Name(), "version", info.Version)},
			domain.EventHarnessVersionUntested, true)
	case "unknown":
		r.warn(domain.Warning{Code: domain.WarningVersionUntested, Subject: p.Provider,
			Text: domain.T("harness_version_unknown",
				"{{harness}} reported no version number, so HarmFlow cannot tell whether it was tested with it",
				"harness", ad.Name())},
			domain.EventHarnessVersionUntested, true)
	}
	raw, err := a.d.Events.RawWriter(r.x.ID, n)
	if err != nil {
		raw = nil
	}
	// The attempt can be ended alone, without the execution's cancel, so a stop for extra usage leaves the execution
	// neither cancelled nor timed out.
	attemptCtx, stopAttempt := context.WithCancel(ctx)
	defer stopAttempt()
	r.stopAttempt = stopAttempt
	r.extraUsage.Store(false)
	// The reader calls back in order and finishes before the process counts as done.
	proc, err := procutil.Start(inv, raw, func(line []byte) {
		for _, he := range parser.Line(line) {
			r.onHarnessEvent(he)
		}
	})
	if err != nil {
		if raw != nil {
			r.closeRaw(raw)
		}
		end := domain.AttemptEnd{Class: domain.ClassHarnessUnavailable, Error: domain.T("harness_start_failed",
			"starting {{harness}}: {{detail}}", "harness", ad.Name(), "detail", err.Error())}
		r.endAttempt(end, -1)
		return end, false
	}
	// Saved before anything else, so from here on a crash leaves a record that names the harness for the next daemon to
	// end.
	if tree := proc.Tree(); tree.Pid > 1 && !tree.Start.IsZero() {
		a.mu.Lock()
		if at := r.x.CurrentAttempt(); at != nil {
			at.Process = &domain.HarnessProcess{Pid: tree.Pid, Group: tree.Group, StartedAt: tree.Start}
			r.x.Changed()
		}
		a.mu.Unlock()
		r.save(true)
	}
	r.event(domain.Event{Type: domain.EventHarnessStarted, Harness: p.Provider,
		Payload: map[string]any{"configuredModel": p.Model, "configuredEffort": p.Effort, "version": info.Version,
			"pid": proc.Pid(), "reason": reason.Message, "path": info.Path}}.Says(domain.T("harness_started",
		"{{harness}} {{version}} started", "harness", ad.Name(), "version", info.Version)))

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
	proc.Wait(attemptCtx, grace)
	r.logEnded(proc.Ended())
	close(stopProgress)
	<-progressDone
	if raw != nil {
		r.closeRaw(raw)
	}
	// A harness that exited on its own keeps the outcome its exit gives, whatever came while what it left ran was ended.
	r.ownEnd = proc.ExitedOnItsOwn()
	stopped := !r.ownEnd
	timedOut := stopped && errors.Is(ctx.Err(), context.DeadlineExceeded) && !r.cancelRequested()
	end := parser.Finish(proc.ExitCode(), proc.StderrTail(), timedOut, stopped && r.cancelRequested())
	if r.extraUsage.Load() && stopped && !timedOut && !r.cancelRequested() && ctx.Err() == nil {
		// Ended for its extra usage, whatever the harness made of the signal: the quota it ran out of is exhausted.
		end.Class = domain.ClassQuotaExhausted
		end.Error = domain.T("extra_usage_stopped",
			"{{harness}} turned to extra usage, which the task does not allow, so its attempt was ended", "harness", ad.Name())
	}
	// Its final text and error quote the harness, and the next attempt's context and the summary quote them in turn.
	eventlog.RedactFields(&end)
	// As for a program, the user's cancellation comes first, then the daemon's stop, then the timeout. A harness the
	// stop ended is interrupted, whatever its exit: code 0 with its success line, another code, or a signal.
	if stopped && !r.cancelRequested() && r.daemonStopping() {
		r.endAttemptAs(end, proc.ExitCode(), domain.AttemptInterrupted, daemonStopped())
	} else {
		r.endAttempt(end, proc.ExitCode())
	}
	return end, timedOut
}

// closeRaw closes an attempt's raw output. A close that fails loses only the end of that copy, whose lines the events
// already carry, so it is logged.
func (r *run) closeRaw(raw io.Closer) {
	if err := raw.Close(); err != nil {
		r.a.log.Printf("closing the raw output of execution %s: %v", r.x.ID, err)
	}
}

// endAttempt closes the current attempt with the outcome and error its end gives.
func (r *run) endAttempt(end domain.AttemptEnd, exit int) {
	r.endAttemptAs(end, exit, attemptOutcome(end.Class), end.Error)
}

// endAttemptAs closes the current attempt with an outcome and an error of the daemon's, which stand in for its end's,
// and records the rest of its end: the exit code, the session, and the usage.
func (r *run) endAttemptAs(end domain.AttemptEnd, exit int, outcome domain.AttemptOutcome, why domain.Text) {
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
	if end.Usage != nil {
		at.Usage = end.Usage
	}
	at.SetError(why)
	at.Outcome = outcome
	r.x.Changed()
	harness := at.Harness
	// Commands still marked running when the process ended did not complete.
	for i := range r.x.Commands {
		if r.x.Commands[i].Status == "running" && r.x.Commands[i].Attempt == at.Number {
			r.x.Commands[i].Status = "failed"
			r.x.Commands[i].EndedAt = &now
		}
	}
	a.mu.Unlock()
	sev := domain.SeverityInfo
	if outcome != domain.AttemptSucceeded {
		sev = domain.SeverityWarning
	}
	name := harness
	if ad, ok := a.adapters[harness]; ok {
		name = ad.Name()
	}
	r.event(domain.Event{Type: domain.EventHarnessExited, Harness: harness, Severity: sev,
		Payload: domain.ErrorPayload(map[string]any{"exitCode": exit, "outcome": string(outcome)}, why)}.Says(
		domain.T("harness_exited", "{{harness}} ended: {{outcome}}", "harness", name,
			"outcome", strings.ReplaceAll(string(outcome), "_", " "), "attemptOutcome", string(outcome))))
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
	case domain.ClassNetwork:
		return domain.AttemptNetworkFailed
	case domain.ClassTimeout:
		return domain.AttemptTimedOut
	case domain.ClassCancelled:
		return domain.AttemptCancelled
	default:
		return domain.AttemptFailed
	}
}

// resolve finds the task's agents and skills for one harness, as it finds them for the execution's working directory.
func (r *run) resolve(ctx context.Context, ad ports.HarnessAdapter) ([]ports.ResolvedItem, []ports.ResolvedItem, error) {
	var agents, skills []ports.ResolvedItem
	for _, kind := range []string{"agent", "skill"} {
		names := r.eff.Spec.Agents
		if kind == "skill" {
			names = r.eff.Spec.Skills
		}
		for _, name := range names {
			l := r.a.resolveItem(ctx, kind, name, ad.ID(), r.itemWorkspace(), r.addedFolders())
			if l.unreadable != nil {
				l.missing.unreadable = l.unreadable
			}
			if l.unreadable != nil || !l.found {
				return nil, nil, &domain.TextError{Text: l.missing.notFound(ad.Name())}
			}
			if kind == "agent" {
				agents = append(agents, l.item)
			} else {
				skills = append(skills, l.item)
			}
		}
	}
	return agents, skills, nil
}

// itemWorkspace is the folder the execution's agents and skills are looked up for: its working directory, or, before
// the workspace is prepared, the one it will be.
func (r *run) itemWorkspace() string {
	if r.work != "" {
		return r.work
	}
	return r.a.itemWorkspace(r.def)
}

// addedFolders are the folders the execution's session adds beside its workspace, as validation reads them: its
// repositories and its writable paths once the workspace is prepared, or, before, the ones they will be.
func (r *run) addedFolders() []string {
	if r.work != "" {
		out := append([]string{}, r.extra...)
		if customWritablePaths(r.eff) != nil {
			out = append(out, r.writable...)
		}
		return out
	}
	return r.a.addedFolders(r.def)
}

// harmflowContext is HarmFlow's orchestration context for the session: where to find the task's parameters and
// history, and where to leave progress and the report. It never changes what the task does, so it names neither the
// task's id nor its name, which a prompt may share with its subject, and it points at context.json only when the
// file holds more than those.
func (r *run) harmflowContext(attempt int, prev []previousAttempt) string {
	var b strings.Builder
	fmt.Fprintf(&b, `This session is run by HarmFlow, a local scheduler. HarmFlow only starts and records the session; it does not define the task. The task is the user's message: do what it asks, on its own terms. Never reinterpret it as being about HarmFlow, this note, or the files below — they are only the plumbing for reporting. A name in the task means what it names in the world, even when it matches a word in this note, a path, or a file. Nobody reads this session live, so do not ask questions; decide, act, and report.

Plumbing for this run — execution %s, attempt %d, trigger %s:
Workspace: %s
Execution directory: %s
`, r.x.ID, attempt, r.x.Trigger, r.work, r.dir)
	if r.hasContext {
		b.WriteString("- context.json: the task's parameters and history — its work items with their status, resumes, and failures, interrupted executions with their last operation, and previous attempts of this execution, each with its work items' branch and head commit, last test result, and changed files. Read it when the task works on items such as issues; context.schema.json in the same directory describes each field.\n")
	}
	b.WriteString(`- progress.jsonl: append one JSON object per line as you reach each step, so the user can follow the execution. Types: step.started {"phase":"preparing|running|testing|publishing","message"}, work_item.selected {"workItem":"owner/repo#123","message":"<title>","repository"}, workspace.changed {"message","branch"}, test.started {"message":"<command>"}, test.completed {"status":"passed|failed|not_run","message"}, pull_request.created and pull_request.updated {"url","number","workItem","branch"}, repository.unprotected {"repository","message"}.
- report.json: write it once, at the end, following execution-report.schema.json in the same directory: {"outcome":"completed|no_work|needs_attention|failed","summary","lastOperation","workItems":[...],"warnings":[...]}. The summary is your answer to the task in a few sentences. Use no_work when nothing was eligible; it is not a failure.
`)
	if len(r.extra) > 0 {
		b.WriteString("Repositories, by local path:\n")
		for name, path := range r.repositoryPaths() {
			fmt.Fprintf(&b, "- %s: %s\n", name, path)
		}
	}
	if len(prev) > 0 {
		last := prev[len(prev)-1]
		ranOn := last.Harness
		if last.Model != "" {
			ranOn += " (" + last.Model + ")"
		}
		fmt.Fprintf(&b, `
This is a new session continuing the same execution. The previous attempt ran on %s and ended: %s.`, ranOn,
			strings.ReplaceAll(last.Outcome, "_", " "))
		if last.LastOperation != "" {
			fmt.Fprintf(&b, " Its last recorded operation: %s.", last.LastOperation)
		}
		if last.LastMessage != "" {
			fmt.Fprintf(&b, " Its last message: %s", last.LastMessage)
		}
		if last.checkpoint != nil {
			b.WriteString("\n" + checkpointText(last.checkpoint))
		}
		if len(last.WorkItems) > 0 {
			b.WriteString("\nFor each work item it worked, previousAttempts in context.json gives what it reported and left: the branch with its head commit, the last test result, and the files changed in the branch's worktree.")
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

// onHarnessEvent turns the adapter's observations into events, commands, and changed files. They are the harness's
// text, redacted before any of it is kept or trimmed.
func (r *run) onHarnessEvent(he ports.HarnessEvent) {
	eventlog.RedactFields(&he)
	a := r.a
	now := a.now()
	switch he.Kind {
	case ports.HarnessStarted:
		a.mu.Lock()
		if at := r.x.CurrentAttempt(); at != nil {
			if he.SessionID != "" {
				at.SessionID = he.SessionID
			}
			// Only the harness's own report says what was used; the configured values stay apart.
			if he.Model != "" {
				at.Model, r.x.Model = he.Model, he.Model
			}
			if he.Effort != "" {
				at.Effort = he.Effort
			}
			r.x.Changed()
		}
		a.mu.Unlock()
		if he.Model != "" || he.Effort != "" {
			r.save(true)
		}
		if he.Model != "" {
			r.event(domain.Event{Type: domain.EventHarnessOutput, Severity: domain.SeverityDebug,
				Payload: map[string]any{"sessionId": he.SessionID, "model": he.Model}}.Says(domain.T("harness_session",
				"session {{session}} on model {{model}}", "session", he.SessionID, "model", he.Model)))
		}
		// A session the harness keeps is the owner's to find: its id and folder are an event of their own.
		if he.SessionDir != "" {
			r.event(domain.Event{Type: domain.EventHarnessOutput, Severity: domain.SeverityInfo,
				Payload: map[string]any{"sessionId": he.SessionID, "sessionDir": he.SessionDir}}.Says(domain.T(
				"harness_session_kept", "the harness keeps session {{session}} in {{folder}}",
				"session", he.SessionID, "folder", he.SessionDir)))
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
		r.x.SetStep(domain.Text{Message: trimTo(firstNonEmpty(he.Message, title), 160)})
		if he.Command != "" {
			r.commandIndex[he.ToolID] = len(r.x.Commands)
			r.x.Commands = append(r.x.Commands, domain.CommandRecord{ID: he.ToolID, Command: trimTo(he.Command, 2000),
				Status: "running", StartedAt: now, Attempt: r.attempt})
			r.x.Changed()
		}
		a.mu.Unlock()
		r.event(domain.Event{Type: domain.EventToolStarted, Message: trimTo(title, 2000),
			Payload: map[string]any{"toolId": he.ToolID, "tool": he.Tool, "command": he.Command, "path": he.Path,
				"description": he.Message}})
	case ports.HarnessToolDone:
		status, sev := "succeeded", domain.SeverityInfo
		text := domain.T("tool_succeeded", "{{tool}} succeeded", "tool", he.Tool)
		switch {
		case he.Declined:
			// The harness refused the call, so it never ran: neither a failure nor an operation done.
			status, sev = "declined", domain.SeverityWarning
			text = domain.T("tool_declined", "{{tool}} was declined", "tool", he.Tool)
		case he.IsError:
			status, sev = "failed", domain.SeverityWarning
			text = domain.T("tool_failed", "{{tool}} failed", "tool", he.Tool)
		}
		a.mu.Lock()
		recorded := false
		if i, ok := r.commandIndex[he.ToolID]; ok && i < len(r.x.Commands) {
			c := &r.x.Commands[i]
			c.Status = status
			// A harness that corrects how a command ended, as Claude Code does for a refusal at the session's end, ends it
			// again: the command keeps the time it ended.
			if c.EndedAt == nil {
				c.EndedAt = &now
			}
			c.ExitCode = he.ExitCode
			r.x.Changed()
			// A command that was running when a progress line recorded a pull request, and that names the progress file,
			// is the one that wrote the line: it ending later leaves the pull request as the last operation.
			wroteLine := r.lineOperation && i < r.lineCommands && strings.Contains(c.Command, "progress.jsonl")
			if status == "succeeded" && !wroteLine {
				r.lastOperation = trimTo(c.Command, 300)
				r.x.LastOperation = r.lastOperation
				r.lineOperation = false
				recorded = true
			}
		}
		a.mu.Unlock()
		if recorded {
			r.save(true)
		}
		payload := map[string]any{"toolId": he.ToolID, "tool": he.Tool, "status": status, "output": he.Output}
		if he.ExitCode != nil {
			payload["exitCode"] = *he.ExitCode
		}
		r.event(domain.Event{Type: domain.EventToolCompleted, Severity: sev, Payload: payload}.Says(text))
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
	case ports.HarnessSettingOverridden:
		// The attempt goes on: only a command that needs what the setting allowed is affected, and the warning tells the
		// user where to look. The text is the harness's own, so it carries no key.
		r.warn(domain.Warning{Code: domain.WarningOther, Text: domain.Text{Message: trimTo(he.Message, 2000)}},
			domain.EventHarnessOutput, true)
	case ports.HarnessRateLimit:
		r.event(domain.Event{Type: domain.EventHarnessOutput, Severity: domain.SeverityWarning, Message: he.Message})
	case ports.HarnessNetworkWait:
		r.event(domain.Event{Type: domain.EventHarnessNetworkWaiting, Severity: domain.SeverityWarning,
			Message: trimTo(he.Message, 2000)})
	case ports.HarnessQuota:
		if he.Quota != nil {
			r.onQuota(*he.Quota)
		}
	case ports.HarnessUsage:
		// No event of its own: the record is saved every second, and an open detail reloads on the attempt's next event.
		a.mu.Lock()
		if at := r.x.CurrentAttempt(); at != nil && he.Usage != nil {
			at.Usage = he.Usage
			r.x.Changed()
		}
		a.mu.Unlock()
	}
}

// readProgress reads the lines the session appended to progress.jsonl since the last read.
func (r *run) readProgress() {
	f, err := os.Open(filepath.Join(r.dir, "progress.jsonl"))
	if err != nil {
		return
	}
	defer f.Close() // read only: closing cannot lose data
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

// onProgress applies a line the agent wrote, redacted before any of it is kept or trimmed.
func (r *run) onProgress(p domain.ProgressLine) {
	eventlog.RedactFields(&p)
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
			r.setStatus(s, domain.Text{Message: msg})
		} else {
			a.mu.Lock()
			r.x.SetStep(domain.Text{Message: msg})
			a.mu.Unlock()
		}
	case domain.EventWorkItemSelected:
		if !githubItemKey.MatchString(p.WorkItem) {
			// A key that names no issue would stay in the record and the ledger as an item no report closes. The line
			// changes neither, and is logged under no work item, so that recovery counts nothing against its key.
			r.emit(domain.Event{Type: p.Type, Severity: domain.SeverityWarning}.Says(domain.T("work_item_key_invalid",
				`work item "{{workItem}}" is not a key of the form owner/repo#<number>; its selection was ignored`,
				"workItem", trimTo(p.WorkItem, 200))))
			return
		}
		a.mu.Lock()
		r.x.WorkItem = p.WorkItem
		if p.Repository != "" {
			r.x.Repository = p.Repository
		}
		r.x.Changed()
		r.x.SetStep(domain.Text{Message: msg})
		r.attemptItem(p.WorkItem, p.Repository)
		a.mu.Unlock()
		// The record names the item before the ledger and the log do: recovery counts the interruption against the item
		// the record names, and against every other one the log's events of this type name.
		r.save(true)
		before := a.trackWorkItem(r.x.TaskID, p.WorkItem, msg, p.Repository, r.x.ID)
		if p.WorkItem != "" && r.selectionOf(p.WorkItem) == nil {
			// A second selection of the item finds it in progress: what it held before is the first one's.
			a.mu.Lock()
			r.selected = append(r.selected, before)
			a.mu.Unlock()
		}
	case domain.EventTestStarted:
		r.setStatus(domain.StatusTesting, domain.Text{Message: msg})
	case domain.EventTestCompleted:
		a.mu.Lock()
		r.x.TestsStatus = p.Status
		r.x.Changed()
		if it := r.attemptItem(firstNonEmpty(p.WorkItem, r.x.WorkItem), p.Repository); it != nil && testStatus(p.Status) {
			it.Tests = &attemptTests{Status: p.Status, Summary: msg}
		}
		a.mu.Unlock()
		r.setStatus(domain.StatusRunning, domain.Text{Message: msg})
	case domain.EventPullRequestCreated, domain.EventPullRequestUpdated:
		a.mu.Lock()
		if p.URL != "" {
			r.x.PullRequestURL = p.URL
		}
		r.lastOperation = p.Type + " " + p.URL
		r.x.LastOperation = r.lastOperation
		r.lineOperation, r.lineCommands = true, len(r.x.Commands)
		r.x.Changed()
		a.mu.Unlock()
		r.save(true) // setStatus saves only the move to publishing, not a later pull request
		r.setStatus(domain.StatusPublishing, domain.Text{Message: msg})
	case domain.EventRepositoryUnprotected:
		// The agent's own words stay as they are; the daemon's text stands in when it wrote none.
		text := domain.Text{Message: msg}
		if msg == "" {
			text = domain.T("repository_unprotected",
				"{{repository}}: the base branch is not protected against merging without review", "repository", p.Repository)
		}
		r.warn(domain.Warning{Code: domain.WarningRepositoryUnprotected, Subject: p.Repository, Text: text}, "", false)
	case domain.EventWorkspaceChanged:
		if p.Branch != "" {
			a.mu.Lock()
			r.lastOperation = msg
			r.x.LastOperation = msg
			r.lineOperation = false
			r.x.Changed()
			if it := r.attemptItem(firstNonEmpty(p.WorkItem, r.x.WorkItem), p.Repository); it != nil {
				it.Branch = p.Branch
			}
			a.mu.Unlock()
			r.save(true)
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
func (r *run) outcomeFromReport() (domain.ExecutionStatus, domain.Text) {
	data, err := os.ReadFile(filepath.Join(r.dir, "report.json"))
	if err != nil {
		r.a.mu.Lock()
		r.x.Summary = trimTo(r.finalText, 4000)
		r.x.Changed()
		r.a.mu.Unlock()
		return domain.StatusCompleted, domain.Text{}
	}
	// An ignored report loses its work items' results, so the execution needs attention, with the reason it was ignored.
	value, err := schema.FromJSON(data)
	if err != nil {
		text := domain.T("report_not_json", "report.json is not valid JSON; it was ignored")
		r.warn(domain.Warning{Code: domain.WarningOther, Text: text}, "", false)
		return domain.StatusNeedsAttention, text
	}
	if issues := r.a.d.Validator.ValidateReport(value); len(issues) > 0 {
		text := domain.T("report_schema_invalid", "report.json does not follow its schema ({{path}}: {{detail}}); it was ignored",
			"path", issues[0].Path, "detail", issues[0].Message)
		r.warn(domain.Warning{Code: domain.WarningOther, Text: text}, "", false)
		return domain.StatusNeedsAttention, text
	}
	var rep domain.ExecutionReport
	if err := json.Unmarshal(data, &rep); err != nil {
		text := domain.T("report_not_decodable", "report.json cannot be decoded ({{detail}}); it was ignored",
			"detail", err.Error())
		r.warn(domain.Warning{Code: domain.WarningOther, Text: text}, "", false)
		return domain.StatusNeedsAttention, text
	}
	eventlog.RedactFields(&rep) // the agent's words, before the record, the ledger, or the reason take any of them
	r.applyReport(&rep)
	status := domain.StatusForReport(&rep)
	var reason domain.Text // the agent's own summary, as it wrote it
	if status == domain.StatusFailed || status == domain.StatusNeedsAttention {
		reason.Message = trimTo(rep.Summary, 500)
	}
	return status, reason
}

func (r *run) applyReport(rep *domain.ExecutionReport) {
	a := r.a
	a.mu.Lock()
	r.x.Summary = trimTo(rep.Summary, 4000)
	r.x.Changed()
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
		warning := domain.Warning{Code: domain.WarningOther, Subject: w.Repository, WorkItem: w.WorkItem,
			Text: domain.Text{Message: w.Message}}
		switch w.Code {
		case domain.WarningRepositoryUnprotected:
			warning.Code = domain.WarningRepositoryUnprotected
			r.warn(warning, domain.EventRepositoryUnprotected, true)
		case domain.WarningConflictingActivity:
			// The conflict is only logged; the work goes on. It belongs to the work item the warning names, or to
			// the report's only one, and to none otherwise: the execution's current item may not be the one that
			// met it.
			warning.Code = domain.WarningConflictingActivity
			if warning.WorkItem == "" && len(rep.WorkItems) == 1 {
				warning.WorkItem = rep.WorkItems[0].Key
			}
			if r.addWarning(warning) {
				r.emit(warningEvent(warning, domain.EventWorkItemConflicting))
			}
		default:
			r.warn(warning, "", false)
		}
	}
	limits := r.limits()
	for _, w := range rep.WorkItems {
		a.applyWorkItem(r.x.TaskID, w, r.x.ID, limits, r.selectionOf(w.Key))
		if w.PullRequest != nil && w.PullRequest.URL != "" {
			t := domain.EventPullRequestUpdated
			if w.PullRequest.Created != nil && *w.PullRequest.Created {
				t = domain.EventPullRequestCreated
			}
			// A report that omits created says nothing of how the pull request came to be: one this execution
			// announced as opened stays opened, not updated as well.
			opened := w.PullRequest.Created == nil && r.sawEvent(domain.EventPullRequestCreated, w.PullRequest.URL)
			if !opened && !r.sawEvent(t, w.PullRequest.URL) {
				r.event(domain.Event{Type: t, WorkItemID: w.Key, Message: w.PullRequest.URL,
					Payload: map[string]any{"url": w.PullRequest.URL, "number": w.PullRequest.Number, "branch": w.Branch}})
			}
		}
		if w.Tests != nil && w.Tests.Status == "failed" {
			// The agent's own summary stays as it is; the daemon's text stands in when it wrote none.
			text := domain.Text{Message: w.Tests.Summary}
			if strings.TrimSpace(w.Tests.Summary) == "" {
				text = domain.T("tests_failed", "tests failed; the draft pull request documents them")
			}
			r.event(domain.Event{Type: domain.EventTestFailed, WorkItemID: w.Key, Severity: domain.SeverityWarning,
				Payload: map[string]any{"url": prURL(w)}}.Says(text))
		}
	}
}

func prURL(w domain.WorkItemResult) string {
	if w.PullRequest != nil {
		return w.PullRequest.URL
	}
	return ""
}

// sawEvent reports whether the execution already announced a pull request, through progress.jsonl or an earlier work
// item of its report. It asks what the run kept of its events, not the log, which it would read whole.
func (r *run) sawEvent(eventType, url string) bool {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return r.announced[announcement{eventType, url}]
}

func (r *run) limits() domain.WorkItemLimits {
	l := domain.WorkItemLimits{MaxResumeAttempts: domain.DefaultMaxResumeAttempts, MaxFailedRetries: domain.DefaultMaxFailedRetries}
	if g := r.eff.Spec.GitHub; g != nil && g.Queue != nil {
		l.MaxResumeAttempts, l.MaxFailedRetries = *g.Queue.MaxResumeAttempts, *g.Queue.MaxFailedRetries
	}
	return l
}

func (a *App) finish(r *run, status domain.ExecutionStatus, reason domain.Text) {
	r.finish(status, reason)
}

// finish records the terminal state and its event. A work item the session selected and left without a result counts
// toward its retry limit, unless the execution was cancelled.
func (r *run) finish(status domain.ExecutionStatus, reason domain.Text) {
	a := r.a
	now := a.now()
	a.mu.Lock()
	if r.x.Status.IsTerminal() {
		a.mu.Unlock()
		return
	}
	r.x.Status = status
	r.x.EndedAt = &now
	r.x.Changed()
	if reason.Message != "" && (status == domain.StatusFailed || status == domain.StatusCancelled || status == domain.StatusNeedsAttention) {
		reason.Message = trimTo(reason.Message, 2000)
		r.x.SetError(reason)
	}
	if status == domain.StatusCancelled && r.x.CancelRequestedAt == nil && r.h != nil && r.h.cancelRequested {
		r.x.CancelRequestedAt = &now
	}
	r.x.SetStep(domain.Text{})
	// Every item the session selected and left without a result counts as an operational failure, as an interruption
	// does, whatever ended the execution, except the user's cancellation, which counts nothing against its items.
	var unreported []string
	if status != domain.StatusCancelled {
		for _, s := range r.selected {
			if !slices.ContainsFunc(r.x.WorkItems, func(w domain.WorkItemResult) bool { return w.Key == s.key }) {
				unreported = append(unreported, s.key)
			}
		}
	}
	a.mu.Unlock()
	for _, key := range unreported {
		a.failWorkItem(r.x.TaskID, key, r.x.ID, r.lastOperation, r.limitsOrDefault())
	}
	r.save(true)
	// Only now: the ledger must keep the marks of its results until the record says the execution ended.
	a.forgetApplied(r.x.ID)
	sev := domain.SeverityInfo
	switch status {
	case domain.StatusFailed, domain.StatusInterrupted:
		sev = domain.SeverityError
	case domain.StatusNeedsAttention, domain.StatusCancelled:
		sev = domain.SeverityWarning
	}
	a.mu.Lock()
	payload := domain.ErrorPayload(map[string]any{"status": string(status), "testsStatus": r.x.TestsStatus,
		"pullRequestUrl": r.x.PullRequestURL, "summary": r.x.Summary, "attempts": len(r.x.Attempts)}, r.x.ErrorText())
	a.mu.Unlock()
	if strings.TrimSpace(reason.Message) == "" {
		reason = domain.T("execution_status", "{{status}}", "status", string(status))
	}
	r.event(domain.Event{Type: domain.TerminalEventFor(status), Severity: sev, Payload: payload}.Says(reason))
}

func (r *run) limitsOrDefault() domain.WorkItemLimits {
	if r.eff.Spec.Type == "" {
		return domain.WorkItemLimits{MaxResumeAttempts: domain.DefaultMaxResumeAttempts, MaxFailedRetries: domain.DefaultMaxFailedRetries}
	}
	return r.limits()
}

// CancelExecution records the intention durably and ends the execution's process tree after the grace period; no
// other execution is affected.
func (a *App) CancelExecution(id string) error {
	a.mu.Lock()
	x, ok := a.executions[id]
	if !ok {
		s, ended := a.ended[id]
		a.mu.Unlock()
		if !ended {
			return executionNotFound(id)
		}
		return newError(CodeConflict,
			domain.T("execution_ended", "the execution already ended as {{status}}", "status", string(s.Status)))
	}
	if x.Status.IsTerminal() {
		a.mu.Unlock()
		return newError(CodeConflict,
			domain.T("execution_ended", "the execution already ended as {{status}}", "status", string(x.Status)))
	}
	now := a.now()
	if x.Status == domain.StatusQueued {
		x.Status = domain.StatusCancelled
		x.EndedAt = &now
		x.CancelRequestedAt = &now
		x.Changed()
		a.mu.Unlock()
		a.persistExecution(x)
		a.emit(domain.Event{Type: domain.EventExecutionCancelled, TaskID: x.TaskID, ExecutionID: x.ID,
			Payload: map[string]any{"status": string(domain.StatusCancelled)}}.Says(
			domain.T("cancelled_before_start", "cancelled before it started")))
		return nil
	}
	h := a.running[x.TaskID]
	x.CancelRequestedAt = &now
	x.Changed()
	a.mu.Unlock()
	a.persistExecution(x)
	a.emit(domain.Event{Type: domain.EventExecutionCancelRequest, TaskID: x.TaskID, ExecutionID: x.ID}.Says(
		domain.T("cancel_requested", "cancellation requested")))
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

// daemonStopped is why an execution the daemon did not finish was interrupted.
func daemonStopped() domain.Text {
	return domain.T("daemon_stopped_execution", "the daemon stopped before the execution finished")
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
