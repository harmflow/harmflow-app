package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// A script task runs a program of the user's instead of a harness session: one process per execution, as
// its only attempt, with no failover, and an outcome from the program's exit status.

// currentOS names the running system as a script task declares it.
func currentOS() string {
	switch runtime.GOOS {
	case "darwin":
		return domain.SystemMacOS
	default:
		return runtime.GOOS
	}
}

// systemsText says where a script task runs, for a task that does not declare this system.
func (a *App) systemsText(doc domain.TaskDocument) domain.Text {
	return domain.T("script_system_not_declared", "this task runs only on {{systems}}, not on {{system}}",
		"systems", strings.Join(doc.Spec.Script.Systems, ", "), "system", a.d.OS)
}

// scriptEnv is the environment the program starts with: base, with the task's variables replacing any of the same
// name.
func scriptEnv(base []string, vars map[string]string) []string {
	out := make([]string, 0, len(base)+len(vars))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, replaced := vars[k]; !replaced {
			out = append(out, kv)
		}
	}
	for _, k := range sortedKeys(vars) {
		out = append(out, k+"="+vars[k])
	}
	return out
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// findProgram resolves a program the way its process finds it: a path, after ~ and ${NAME}, relative to the working
// directory; a bare name on the PATH of env.
func (a *App) findProgram(name, workDir string, env []string) (string, error) {
	notFound := domain.TextErr("script_program_not_found", "the program {{program}} was not found on this machine",
		"program", name)
	p, err := a.expand(name)
	if err != nil {
		return "", err
	}
	if strings.Contains(p, "/") {
		if !filepath.IsAbs(p) {
			p = filepath.Join(workDir, p)
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
		return "", notFound
	}
	if hits := procutil.LookPath(p, envValue(env, "PATH"), nil); len(hits) > 0 {
		return hits[0], nil
	}
	return "", notFound
}

// workingDirectory is where the program starts: the task's, expanded, or the user's home directory.
func (a *App) workingDirectory(s *domain.ScriptSpec) (string, error) {
	if s == nil || s.WorkingDirectory == "" {
		return a.d.Paths.UserHome, nil
	}
	return a.expand(s.WorkingDirectory)
}

func (a *App) baseEnv() []string {
	if a.d.HarnessEnv != nil {
		return a.d.HarnessEnv
	}
	return os.Environ()
}

// scriptChecks are a script task's rules the schema cannot express. Each is a warning, never an error: a task written
// for another machine still saves and imports.
func (a *App) scriptChecks(r *ValidationResult, doc domain.TaskDocument) {
	s := doc.Spec.Script
	if s == nil {
		return
	}
	if !doc.RunsOn(a.d.OS) {
		r.addWarning("/spec/script/systems", domain.WarningOther, a.systemsText(doc))
	}
	wd, err := a.workingDirectory(s)
	if err != nil {
		r.addWarning("/spec/script/workingDirectory", domain.WarningOther, domain.TextOf(err))
	} else if st, err := os.Stat(wd); err != nil || !st.IsDir() {
		r.addWarning("/spec/script/workingDirectory", domain.WarningOther, workingDirectoryMissing(wd))
	}
	path := "/spec/script/command/0"
	if len(s.Command) == 0 {
		path = "/spec/script/inline/interpreter/0"
	}
	if program := doc.ProgramName(); program != "" {
		if _, err := a.findProgram(program, wd, scriptEnv(a.baseEnv(), s.Environment)); err != nil {
			r.addWarning(path, domain.WarningOther, domain.TextOf(err))
		}
	}
}

func workingDirectoryMissing(path string) domain.Text {
	return domain.T("script_working_directory_missing", "the working directory {{path}} does not exist", "path", path)
}

// scriptRestrictions names each restriction a script task declares. Nothing confines a program of the user's, so none
// is enforced, and each runs with a warning.
func scriptRestrictions(p domain.Permissions) []domain.Warning {
	var names []string
	switch p.Profile {
	case domain.ProfileReadOnly:
		names = append(names, domain.ProfileReadOnly)
	case domain.ProfileCustom:
		if c := p.Custom; c != nil && (len(c.AllowedTools) > 0 || len(c.DeniedTools) > 0) {
			names = append(names, "tool_lists")
		}
		if c := p.Custom; c != nil && len(c.WritablePaths) > 0 {
			names = append(names, "writablePaths")
		}
		if p.RestrictToWorkspace != nil && *p.RestrictToWorkspace {
			names = append(names, "restrictToWorkspace")
		}
	default: // workspace_write
		if p.RestrictToWorkspace != nil && *p.RestrictToWorkspace {
			names = append(names, "restrictToWorkspace")
		}
	}
	if p.Network == domain.NetworkDeny {
		names = append(names, "network")
	}
	out := make([]domain.Warning, 0, len(names))
	for _, n := range names {
		out = append(out, domain.Warning{Code: domain.WarningPolicyUnenforced, Subject: n, Text: domain.T(
			"restriction_script_unenforced",
			"nothing confines a script's program, which runs with the user's rights: {{restriction}} is not enforced",
			"restriction", n)})
	}
	return out
}

// scriptFileName names the file an inline script is written to, with the extension its interpreter expects: PowerShell
// runs only a .ps1 file, and the others tell the user what the file holds.
func scriptFileName(interpreter string) string {
	base := strings.ToLower(filepath.Base(interpreter))
	switch {
	case base == "pwsh" || base == "powershell" || base == "pwsh.exe" || base == "powershell.exe":
		return "script.ps1"
	case strings.HasPrefix(base, "python"):
		return "script.py"
	case base == "node" || base == "deno" || base == "bun":
		return "script.js"
	case base == "ruby":
		return "script.rb"
	case base == "perl":
		return "script.pl"
	case base == "bash" || base == "sh" || base == "zsh" || base == "dash" || base == "ksh" || base == "fish":
		return "script.sh"
	}
	return "script"
}

// runScript carries a script execution from preparing to its outcome. It returns the terminal status and why.
func (r *run) runScript(ctx context.Context, deadline time.Time) (domain.ExecutionStatus, domain.Text) {
	a := r.a
	spec := r.eff.Spec.Script
	if !r.def.RunsOn(a.d.OS) {
		return domain.StatusFailed, a.systemsText(r.def)
	}
	if issues := a.taskPathIssues(r.def, a.currentPathEnv()); len(issues) > 0 {
		return domain.StatusFailed, refusedPath(issues[0])
	}
	r.dir = filepath.Join(a.d.Paths.Runs, r.x.ID)
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return domain.StatusFailed, domain.T("execution_dir_failed", "creating the execution directory: {{detail}}",
			"detail", err.Error())
	}
	wd, err := a.workingDirectory(spec)
	if err != nil {
		return domain.StatusFailed, domain.TextOf(err)
	}
	if st, err := os.Stat(wd); err != nil || !st.IsDir() {
		return domain.StatusFailed, workingDirectoryMissing(wd)
	}
	r.work = wd
	rec := &domain.ScriptRun{WorkingDirectory: wd, Environment: redactedMap(spec.Environment)}
	argv := append([]string{}, spec.Command...)
	if spec.Inline != nil {
		script := filepath.Join(r.dir, scriptFileName(spec.Inline.Interpreter[0]))
		if err := os.WriteFile(script, []byte(spec.Inline.Source), 0o700); err != nil {
			return domain.StatusFailed, domain.T("script_write_failed", "writing the inline script: {{detail}}",
				"detail", err.Error())
		}
		argv = append(append([]string{}, spec.Inline.Interpreter...), script)
		rec.Interpreter, rec.Source, rec.ScriptPath = redactedSlice(spec.Inline.Interpreter), eventlog.Redact(spec.Inline.Source), script
	} else {
		rec.Command = redactedSlice(spec.Command)
	}
	env := scriptEnv(r.env(), spec.Environment)
	program, err := a.findProgram(argv[0], wd, env)
	if err != nil {
		return domain.StatusFailed, domain.TextOf(err)
	}
	rec.Program = program
	now := a.now()
	a.mu.Lock()
	r.x.StartedAt = &now
	r.x.TimeoutAt = &deadline
	r.x.WorkspacePath, r.x.ExecutionDir = wd, r.dir
	r.x.Script = rec
	r.attempt = 1
	at := domain.Attempt{Number: 1, StartedAt: now, Outcome: domain.AttemptRunning}
	at.SetReason(domain.T("attempt_first", "first attempt"))
	r.x.Attempts = append(r.x.Attempts, at)
	r.x.Changed()
	a.mu.Unlock()
	r.event(domain.Event{Type: domain.EventExecutionStarted, Message: r.def.Metadata.Name,
		Payload: map[string]any{"trigger": string(r.x.Trigger), "timeoutAt": deadline,
			"definitionRevision": r.x.DefinitionRevision}})
	for _, w := range scriptRestrictions(*r.eff.Spec.Permissions) {
		r.warn(w, domain.EventPolicyUnenforced, true)
	}
	name := filepath.Base(program)
	r.setStatus(domain.StatusRunning, domain.T("step_running_program", "Running {{program}}", "program", name))
	r.save(true)

	raw, err := a.d.Events.RawWriter(r.x.ID, 1)
	if err != nil {
		raw = nil
	}
	output := func(stream string) func([]byte) {
		return func(line []byte) {
			r.event(domain.Event{Type: domain.EventScriptOutput, Message: trimTo(string(line), 8000),
				Payload: map[string]any{"stream": stream}})
		}
	}
	// The output is held until script.started is logged, so the log has the program start before anything it wrote,
	// however soon it writes; the start waits for the process identity, which is saved first for crash recovery.
	proc, err := procutil.StartHeld(ports.Invocation{Path: program, Args: argv[1:], Env: env, Dir: wd,
		ExecutionID: r.x.ID}, raw, output("stdout"), output("stderr"))
	if err != nil {
		if raw != nil {
			r.closeRaw(raw)
		}
		reason := domain.T("script_start_failed", "starting {{program}}: {{detail}}", "program", name,
			"detail", err.Error())
		r.endScript(domain.AttemptFailed, reason, -1, "")
		return domain.StatusFailed, reason
	}
	if id, err := procutil.Identify(ctx, proc.Pid()); err == nil {
		a.mu.Lock()
		rec.Process = &domain.ProcessIdentity{Group: id.Group, LeaderStart: id.LeaderStart, Boot: id.Boot}
		r.x.Changed()
		a.mu.Unlock()
	}
	r.save(true)
	r.event(domain.Event{Type: domain.EventScriptStarted,
		Payload: map[string]any{"pid": proc.Pid(), "program": program}}.Says(
		domain.T("script_started", "{{program}} started", "program", name)))
	proc.Release()

	grace := time.Duration(*r.eff.Spec.Execution.CancelGraceSeconds) * time.Second
	proc.Wait(ctx, grace)
	r.logEnded(proc.Ended())
	if raw != nil {
		r.closeRaw(raw)
	}
	exit, signal := proc.ExitCode(), proc.Signal()
	ended := domain.T("script_exit_code", "the program exited with code {{code}}", "code", strconv.Itoa(exit))
	if signal != "" {
		ended = domain.T("script_signaled", "the program was ended by {{signal}}", "signal", signal)
	}
	var status domain.ExecutionStatus
	var reason domain.Text
	outcome := domain.AttemptFailed
	// A program that exited on its own keeps the result its exit status gives, whatever came while what it left running
	// was ended.
	stopped := !proc.ExitedOnItsOwn()
	switch {
	case stopped && r.cancelRequested():
		status, reason, outcome = domain.StatusCancelled, domain.T("execution_cancelled", "cancelled"), domain.AttemptCancelled
	case stopped && r.daemonStopping():
		status, reason, outcome = domain.StatusInterrupted, daemonStopped(), domain.AttemptInterrupted
	case stopped && errors.Is(ctx.Err(), context.DeadlineExceeded):
		status, reason = domain.StatusFailed, domain.T("execution_timed_out", "the execution timeout elapsed")
		outcome = domain.AttemptTimedOut
	case signal == "" && exit == 0:
		status, outcome = domain.StatusCompleted, domain.AttemptSucceeded
	default:
		status, reason = domain.StatusFailed, ended
	}
	r.endScript(outcome, reason, exit, signal)
	sev := domain.SeverityInfo
	if status != domain.StatusCompleted {
		sev = domain.SeverityWarning
	}
	payload := map[string]any{"exitCode": exit, "outcome": string(outcome)}
	if signal != "" {
		payload["signal"] = signal
	}
	r.event(domain.Event{Type: domain.EventScriptExited, Severity: sev, Payload: payload}.Says(ended))
	return status, reason
}

// endScript closes the execution's only attempt and records the exit status; the process is gone, so its identity is
// dropped and a restart ends nothing for it.
func (r *run) endScript(outcome domain.AttemptOutcome, reason domain.Text, exit int, signal string) {
	a := r.a
	now := a.now()
	a.mu.Lock()
	if at := r.x.CurrentAttempt(); at != nil {
		at.EndedAt = &now
		at.Outcome = outcome
		at.SetError(reason)
		if exit >= 0 {
			code := exit
			at.ExitCode = &code
		}
	}
	if s := r.x.Script; s != nil {
		if exit >= 0 {
			code := exit
			s.ExitCode = &code
		}
		s.Signal = signal
		s.Process = nil
	}
	r.x.Changed()
	a.mu.Unlock()
	r.save(true)
}

// endLeftovers ends what an interrupted script execution's program left running, and says so on the execution. It
// runs when the daemon starts, before the record is saved.
func (a *App) endLeftovers(ctx context.Context, x *domain.Execution) {
	if x.Script == nil || x.Script.Process == nil {
		return
	}
	p := x.Script.Process
	n, err := procutil.EndLeftovers(ctx, procutil.Identity{Group: p.Group, LeaderStart: p.LeaderStart, Boot: p.Boot})
	if err != nil {
		a.log.Printf("ending what execution %s left running: %v", x.ID, err)
	}
	a.mu.Lock()
	x.Script.Process = nil
	x.Changed()
	if n > 0 {
		template := "{{count}} processes the interrupted program left running were ended"
		if n == 1 {
			template = "{{count}} process the interrupted program left running was ended"
		}
		x.AddWarning(domain.Warning{Code: domain.WarningOther,
			Text: domain.T("script_leftovers_ended", template, "count", strconv.Itoa(n))})
	}
	a.mu.Unlock()
}

func redactedSlice(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = eventlog.Redact(s)
	}
	return out
}

func redactedMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = eventlog.Redact(v)
	}
	return out
}
