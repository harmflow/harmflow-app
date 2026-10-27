package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// defaultListTimeout bounds one call to the app server; a cold skills/list took about 1.4 s on a Mac.
const defaultListTimeout = 20 * time.Second

// appServer is one `codex app-server`, spoken to in JSON lines over its stdin and stdout, one request at a time. Its
// pipes carry deadlines, so a call that waits too long ends without a goroutine of its own.
type appServer struct {
	path   string
	cmd    *exec.Cmd
	in     *os.File // the server's stdin
	out    *os.File // the server's stdout
	reader *bufio.Reader
	nextID int64
}

type rpcRequest struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type rpcNotification struct {
	Method string `json:"method"`
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int64  `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// skillsList is the answer to skills/list, as Codex 0.163.0 writes it.
type skillsList struct {
	Data []struct {
		Cwd    string `json:"cwd"`
		Skills []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Enabled     bool   `json:"enabled"`
			Path        string `json:"path"`
			Scope       string `json:"scope"`
		} `json:"skills"`
		Errors []ports.DiscoveryError `json:"errors"`
	} `json:"data"`
}

// startAppServer starts `<path> app-server` in a process group of its own, so ending it also ends the native binary a
// wrapper such as npm's starts, and says hello as its protocol asks: initialize, then initialized.
func startAppServer(ctx context.Context, path string, env []string, version string, timeout time.Duration) (*appServer, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, startFailed(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, startFailed(err)
	}
	cmd := exec.Command(path, "app-server")
	cmd.Env = env
	cmd.Stdin, cmd.Stdout = inR, outW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	// The child holds its own ends now.
	_ = inR.Close()
	_ = outW.Close()
	if err != nil {
		_ = inW.Close()
		_ = outR.Close()
		return nil, startFailed(err)
	}
	s := &appServer{path: path, cmd: cmd, in: inW, out: outR, reader: bufio.NewReader(outR)}
	if version == "" {
		version = "0.0.0"
	}
	hello := map[string]any{"clientInfo": map[string]string{"name": "harmflow", "title": "HarmFlow", "version": version}}
	if err := s.call(ctx, timeout, "initialize", hello, nil); err != nil {
		s.close()
		return nil, err
	}
	if err := s.write(rpcNotification{Method: "initialized"}); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *appServer) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := s.in.Write(append(data, '\n')); err != nil {
		return exitedErr()
	}
	return nil
}

// call sends one request and reads until the answer whose id matches: notifications, and requests of the server's own,
// can arrive in between. result, when not nil, receives the answer's result.
func (s *appServer) call(ctx context.Context, timeout time.Duration, method string, params, result any) error {
	s.nextID++
	id := s.nextID
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = s.in.SetWriteDeadline(deadline)
	_ = s.out.SetReadDeadline(deadline)
	if err := s.write(rpcRequest{ID: id, Method: method, Params: params}); err != nil {
		return err
	}
	want := strconv.FormatInt(id, 10)
	for {
		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return &serverError{domain.TextErr("codex_app_server_timed_out",
					"Codex's app server did not answer {{method}} within {{seconds}} seconds", "method", method,
					"seconds", strconv.FormatFloat(timeout.Seconds(), 'g', -1, 64)), true}
			}
			return exitedErr()
		}
		var msg rpcMessage
		if json.Unmarshal(line, &msg) != nil || msg.Method != "" || string(msg.ID) != want {
			continue
		}
		if msg.Error != nil {
			return &serverError{domain.TextErr("codex_app_server_error",
				"Codex's app server answered {{method}} with an error: {{detail}}", "method", method,
				"detail", msg.Error.Message), false}
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(msg.Result, result); err != nil {
			return &serverError{domain.TextErr("codex_app_server_error",
				"Codex's app server answered {{method}} with an error: {{detail}}", "method", method,
				"detail", err.Error()), false}
		}
		return nil
	}
}

// close ends the server's process group and waits for it. Closing its stdin first lets a server that reads it end on
// its own; the group is killed in any case, since nothing it holds needs saving.
func (s *appServer) close() {
	_ = s.in.Close()
	if s.cmd.Process != nil {
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	}
	_ = s.cmd.Wait() // its exit after the kill says nothing
	_ = s.out.Close()
}

// serverError is a call that failed. broken says the server can no longer be trusted to answer, so the next call starts
// another.
type serverError struct {
	err    error
	broken bool
}

func (e *serverError) Error() string { return e.err.Error() }

func (e *serverError) Unwrap() error { return e.err }

func exitedErr() error {
	return &serverError{domain.TextErr("codex_app_server_exited", "Codex's app server exited"), true}
}

func startFailed(err error) error {
	return &serverError{domain.TextErr("codex_app_server_start_failed", "Codex's app server could not start: {{detail}}",
		"detail", err.Error()), true}
}

// listSkills asks the app server for the skills Codex has in workspace. The first list after ReloadUserItems carries
// forceReload.
func (a *Adapter) listSkills(ctx context.Context, info ports.HarnessInfo, workspace string) (skillsList, error) {
	var list skillsList
	a.mu.Lock()
	defer a.mu.Unlock()
	params := map[string]any{"cwds": []string{workspace}, "forceReload": a.reload}
	if err := a.callLocked(ctx, info, "skills/list", params, &list); err != nil {
		return skillsList{}, err
	}
	a.reload = false
	return list, nil
}

// callLocked sends one request to the app server, starting it on first use and again once it broke; a.mu is held.
func (a *Adapter) callLocked(ctx context.Context, info ports.HarnessInfo, method string, params, result any) error {
	if !info.Detected || info.Path == "" {
		return domain.TextErr("harness_not_detected", "{{harness}} was not detected on this machine", "harness", Name)
	}
	if !info.Features["app-server"] {
		return domain.TextErr("codex_app_server_missing", "Codex {{version}} has no app server", "version", info.Version)
	}
	timeout := a.listTimeout
	if timeout == 0 {
		timeout = defaultListTimeout
	}
	if a.server != nil && a.server.path != info.Path {
		a.server.close()
		a.server = nil
	}
	if a.server == nil {
		s, err := startAppServer(ctx, info.Path, a.Env, a.ClientVersion, timeout)
		if err != nil {
			return err
		}
		a.server = s
	}
	err := a.server.call(ctx, timeout, method, params, result)
	var se *serverError
	if errors.As(err, &se) && se.broken {
		a.server.close()
		a.server = nil
	}
	return err
}

// rateLimitsRead is the answer to account/rateLimits/read, as Codex 0.163.0 writes it: its plan windows, which some
// plans leave null, the limit it reports reached, and the account's spend control.
type rateLimitsRead struct {
	RateLimits *struct {
		Primary              *rateLimitWindow `json:"primary"`
		Secondary            *rateLimitWindow `json:"secondary"`
		RateLimitReachedType *string          `json:"rateLimitReachedType"`
		SpendControlReached  *bool            `json:"spendControlReached"`
	} `json:"rateLimits"`
}

// rateLimitWindow is one plan window of Codex: the share of it used, in percent, and when it resets.
type rateLimitWindow struct {
	UsedPercent float64 `json:"usedPercent"`
	ResetsAt    *int64  `json:"resetsAt"`
}

// ReadQuota implements ports.QuotaReader with the app server's account/rateLimits/read, which makes no model call.
func (a *Adapter) ReadQuota(ctx context.Context, info ports.HarnessInfo) (domain.Quota, error) {
	var read rateLimitsRead
	a.mu.Lock()
	err := a.callLocked(ctx, info, "account/rateLimits/read", nil, &read)
	a.mu.Unlock()
	if err != nil {
		return domain.Quota{}, err
	}
	return read.quota()
}

// quota is the reading the answer gives; an answer without rateLimits is one the adapter does not understand.
func (r rateLimitsRead) quota() (domain.Quota, error) {
	l := r.RateLimits
	if l == nil {
		return domain.Quota{}, domain.TextErr("quota_report_unrecognized",
			"{{harness}}'s usage report names no plan window HarmFlow can read", "harness", Name)
	}
	var q domain.Quota
	for _, w := range []struct {
		name   string
		window *rateLimitWindow
	}{{"primary", l.Primary}, {"secondary", l.Secondary}} {
		if w.window == nil {
			continue
		}
		var resets time.Time
		if w.window.ResetsAt != nil && *w.window.ResetsAt > 0 {
			resets = time.Unix(*w.window.ResetsAt, 0)
		}
		q.Windows = append(q.Windows, domain.QuotaWindow{Name: w.name, UsedPercent: w.window.UsedPercent, ResetsAt: resets})
	}
	if l.RateLimitReachedType != nil && *l.RateLimitReachedType != "" {
		q.Limit = &domain.QuotaWindow{Name: *l.RateLimitReachedType}
	}
	q.SpendControlReached = l.SpendControlReached != nil && *l.SpendControlReached
	return q, nil
}

// skills are the skills Codex lists for workspace and has enabled, each name once: the first in the list's own order,
// never sorted again. A skill's folder is the one of the SKILL.md Codex reports. Which names a task can cite is the
// application's to decide, so every name Codex lists is kept.
func (a *Adapter) skills(ctx context.Context, info ports.HarnessInfo, workspace string) ([]ports.ResolvedItem, []ports.DiscoveryError, error) {
	list, err := a.listSkills(ctx, info, workspace)
	if err != nil {
		return nil, nil, err
	}
	var items []ports.ResolvedItem
	var problems []ports.DiscoveryError
	seen := map[string]bool{}
	for _, entry := range list.Data {
		problems = append(problems, entry.Errors...)
		for _, s := range entry.Skills {
			if !s.Enabled || seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			description, _, _ := strings.Cut(strings.TrimSpace(s.Description), "\n")
			items = append(items, ports.ResolvedItem{Name: s.Name, Kind: "skill", Path: filepath.Dir(s.Path),
				Description: strings.TrimSpace(description)})
		}
	}
	return items, problems, nil
}

// ListsPerWorkspace implements ports.WorkspaceLister: skills come from Codex's skill list, agents from the Codex home.
func (a *Adapter) ListsPerWorkspace(kind string) bool { return kind == "skill" }

// ReloadUserItems implements ports.ItemReloader: the next skills/list asks Codex to read its skills from disk again.
func (a *Adapter) ReloadUserItems() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload = true
}

// Close implements ports.HarnessCloser: it ends the app server, if one runs.
func (a *Adapter) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.server != nil {
		a.server.close()
		a.server = nil
	}
}
