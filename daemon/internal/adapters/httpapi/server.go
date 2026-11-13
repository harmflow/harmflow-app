// Package httpapi is the local API (contracts/api/openapi.yaml): HTTP on 127.0.0.1 with an SSE stream. Every
// request carries the per-installation token; a Host that is not loopback (DNS rebinding) or an Origin that is a web
// page (CSRF) is rejected.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/app"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// Server serves the local API over the use cases of App. The token it checks is held in memory and set with SetToken;
// a rotation stores a new one, made by NewToken, in Secrets.
type Server struct {
	App      *app.App
	Secrets  ports.SecretStore
	NewToken func() (string, error)
	// Logger is daemon.log, where rejected requests are recorded; nil records none.
	Logger *log.Logger
	// Clock defaults to the system clock.
	Clock ports.Clock
	// RejectionWindow is the least time between two log entries for the same reason of rejection, a minute when zero.
	RejectionWindow time.Duration
	// Routes are added to the API's own routes, behind the same checks; Prepare refuses one that cannot be added.
	Routes []Route

	mu    sync.RWMutex
	token string
	// stopping, guarded by mu and made on first use, is closed when Shutdown starts: it ends the open event streams,
	// which http.Server.Shutdown would otherwise wait for until its deadline.
	stopping chan struct{}
	listener net.Listener
	port     int
	srv      *http.Server
	handler  http.Handler
	rejected rejections
}

// Route is a route added to the local API beside its own: Handler serves the requests that Pattern, an http.ServeMux
// pattern under /api/v1/ such as "GET /api/v1/example/{id}", matches.
type Route struct {
	Pattern string
	Handler http.Handler
}

// allowedOrigins are the desktop's own webview origins; any other Origin is a web page.
var allowedOrigins = map[string]bool{"tauri://localhost": true, "http://tauri.localhost": true, "https://tauri.localhost": true}

// Listen binds a free port on the loopback interface.
func (s *Server) Listen(port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	s.listener = ln
	s.port = ln.Addr().(*net.TCPAddr).Port
	return nil
}

// Port is the port Listen bound.
func (s *Server) Port() int { return s.port }

// Address is the loopback address and port Listen bound, as host:port.
func (s *Server) Address() string { return fmt.Sprintf("127.0.0.1:%d", s.port) }

// SetToken sets the token every request must carry; with none set, every request is rejected.
func (s *Server) SetToken(t string) {
	s.mu.Lock()
	s.token = t
	s.mu.Unlock()
}

// Prepare builds the handler Serve serves, and returns an error instead when a route of Routes has no handler, a
// pattern outside /api/v1/, a pattern http.ServeMux refuses, or one that conflicts with another route. Serve builds the
// handler itself when Prepare was not called.
func (s *Server) Prepare() (err error) {
	for _, rt := range s.Routes {
		if rt.Handler == nil {
			return fmt.Errorf("adding the route %q to the local API: it has no handler", rt.Pattern)
		}
		if !underAPI(rt.Pattern) {
			return fmt.Errorf("adding the route %q to the local API: its path is not under /api/v1/", rt.Pattern)
		}
	}
	// http.ServeMux panics on a pattern it refuses or one that conflicts with a route it holds; that is a failure to
	// start, returned as one.
	defer func() {
		switch r := recover().(type) {
		case nil:
		case error:
			err = fmt.Errorf("adding a route to the local API: %w", r)
		default:
			err = fmt.Errorf("adding a route to the local API: %v", r)
		}
	}()
	s.handler = s.Handler()
	return nil
}

// underAPI reports whether the path of an http.ServeMux pattern, which may follow a method and a space, is under
// /api/v1/. A pattern with a host is not: the API answers only on the loopback, whatever the host.
func underAPI(pattern string) bool {
	path := pattern
	if i := strings.IndexAny(pattern, " \t"); i >= 0 {
		path = strings.TrimLeft(pattern[i:], " \t")
	}
	return strings.HasPrefix(path, "/api/v1/")
}

// Serve serves the API on the listener of Listen until Shutdown, which returns nil.
func (s *Server) Serve() error {
	handler := s.handler
	if handler == nil {
		handler = s.Handler()
	}
	s.srv = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	err := s.srv.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown ends the open event streams and stops serving, then records the rejected requests no entry has counted yet.
// Any other request still running holds it until ctx ends, and it then returns ctx's error.
func (s *Server) Shutdown(ctx context.Context) error {
	stopping := s.stoppingCh()
	s.mu.Lock()
	select {
	case <-stopping:
	default:
		close(stopping)
	}
	s.mu.Unlock()
	var err error
	if s.srv != nil {
		err = s.srv.Shutdown(ctx)
	}
	s.flushRejections()
	return err
}

// stoppingCh is the channel Shutdown closes when it starts.
func (s *Server) stoppingCh() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping == nil {
		s.stopping = make(chan struct{})
	}
	return s.stopping
}

// Handler routes the API's requests, Routes' included, every one through the Host, Origin, and token checks. Like
// http.ServeMux, it panics on a route of Routes it cannot add; Prepare returns that as an error instead.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, fn func(w http.ResponseWriter, r *http.Request)) { mux.HandleFunc(pattern, fn) }

	h("GET /api/v1/health", s.health)
	h("POST /api/v1/daemon/stop", s.stop)
	h("GET /api/v1/diagnostics", s.diagnostics)
	h("GET /api/v1/stats", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.Stats()) })
	h("GET /api/v1/spend", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.Spend()) })
	h("GET /api/v1/system", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.System()) })
	h("GET /api/v1/harnesses", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.Harnesses()) })
	h("POST /api/v1/harnesses/refresh", func(w http.ResponseWriter, r *http.Request) {
		s.App.RedetectHarnesses()
		writeJSON(w, 200, s.App.Harnesses())
	})
	h("PUT /api/v1/harnesses/{harnessId}", s.setHarnessPath)
	h("POST /api/v1/harnesses/{harnessId}/sign-in", s.signInHarness)
	h("PUT /api/v1/harnesses/{harnessId}/token", s.setHarnessToken)
	h("DELETE /api/v1/harnesses/{harnessId}/token", s.deleteHarnessToken)
	h("GET /api/v1/tools", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.Tools(r.Context())) })
	h("GET /api/v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		writeJSON(w, 200, s.App.Catalog(r.Context(), q.Get("workspace"), q.Get("task"), q["addDir"]...))
	})

	h("GET /api/v1/templates", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.Templates()) })
	h("PUT /api/v1/templates/{templateId}", s.updateTemplate)
	h("DELETE /api/v1/templates/{templateId}", s.deleteTemplate)
	h("POST /api/v1/templates/{templateId}/draft", s.draftTask)

	h("GET /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.ListTasks()) })
	h("POST /api/v1/tasks", s.createTask)
	h("POST /api/v1/tasks/validate", s.validateTask)
	h("POST /api/v1/tasks/import", s.importTask)
	h("GET /api/v1/tasks/{taskId}", s.getTask)
	h("PUT /api/v1/tasks/{taskId}", s.updateTask)
	h("DELETE /api/v1/tasks/{taskId}", s.deleteTask)
	h("POST /api/v1/tasks/{taskId}/run", s.runTask)
	h("POST /api/v1/tasks/{taskId}/duplicate", s.duplicateTask)
	h("POST /api/v1/tasks/{taskId}/enable", func(w http.ResponseWriter, r *http.Request) { s.setEnabled(w, r, true) })
	h("POST /api/v1/tasks/{taskId}/disable", func(w http.ResponseWriter, r *http.Request) { s.setEnabled(w, r, false) })
	h("GET /api/v1/tasks/{taskId}/export", s.exportTask)
	h("POST /api/v1/schedules/preview", s.previewSchedule)

	h("GET /api/v1/executions", s.listExecutions)
	h("GET /api/v1/executions/{executionId}", s.getExecution)
	h("POST /api/v1/executions/{executionId}/cancel", s.cancelExecution)
	h("GET /api/v1/executions/{executionId}/events", s.executionEvents)
	h("GET /api/v1/executions/{executionId}/output", s.executionOutput)
	h("GET /api/v1/events/stream", s.stream)
	h("GET /api/v1/queue", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.Queue()) })

	h("GET /api/v1/settings", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.GetSettings()) })
	h("PATCH /api/v1/settings", s.updateSettings)
	h("POST /api/v1/settings/token/rotate", s.rotateToken)
	h("POST /api/v1/workspaces/clean", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.App.CleanWorkspaces())
	})
	h("GET /api/v1/notifications", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.App.Notifications(r.URL.Query().Get("unread") == "true"))
	})
	h("POST /api/v1/notifications/read", s.markRead)
	for _, rt := range s.Routes {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	return s.guard(mux)
}

// guard enforces the Host and Origin checks and the bearer token on every route, the SSE stream included, and records
// each request it rejects.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "[::1]" {
			s.reject(w, r, reasonForbiddenHost, 403, "forbidden",
				domain.T("host_forbidden", "the Host header must be the loopback address"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !allowedOrigins[origin] {
			s.reject(w, r, reasonForbiddenOrigin, 403, "forbidden",
				domain.T("origin_forbidden", "requests from web pages are not accepted"))
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			writeError(w, 404, "not_found", domain.T("route_not_found", "no such route"))
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.RLock()
		want := s.token
		s.mu.RUnlock()
		if want == "" || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			reason := reasonInvalidToken
			if got == "" {
				reason = reasonMissingToken
			}
			s.reject(w, r, reason, 401, "unauthorized", domain.T("token_required", "a valid token is required"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	// The status is already sent, so a body that fails to encode or write has no one left to be told.
	_ = enc.Encode(v)
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code string `json:"code"`
	domain.Text
	Validation      *app.ValidationResult `json:"validation,omitempty"`
	CurrentRevision string                `json:"currentRevision,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code string, text domain.Text) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Text: text}})
}

// fail maps a use-case error to its response.
func fail(w http.ResponseWriter, err error) {
	var ae *app.Error
	if errors.As(err, &ae) {
		status := map[string]int{
			app.CodeBadRequest: 400, app.CodeNotFound: 404, app.CodeConflict: 409, app.CodeValidationFailed: 422,
			app.CodeTaskDisabled: 409, app.CodeExecutionsRunning: 409,
		}[ae.Code]
		if status == 0 {
			status = 400
		}
		writeJSON(w, status, errorBody{Error: errorDetail{Code: ae.Code, Text: ae.Text, Validation: ae.Validation,
			CurrentRevision: ae.CurrentRevision}})
		return
	}
	writeError(w, 500, "internal", domain.Text{Message: err.Error()})
}

const maxBody = 4 << 20

func decode(r *http.Request, v any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return &app.Error{Code: app.CodeBadRequest,
			Text: domain.T("body_not_json", "the body is not valid JSON: {{detail}}", "detail", err.Error())}
	}
	return nil
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	// One read gives both fields, so that no response pairs a drain with firings still accepted.
	state := s.App.FiringState()
	body := map[string]any{"status": "ok", "version": s.App.Version(), "apiVersion": "v1",
		"pid": pid(), "startedAt": s.App.StartedAt(), "acceptingFirings": state.Accepting}
	if commit := s.App.Commit(); commit != "" {
		body["commit"] = commit
	}
	if builtAt := s.App.BuiltAt(); !builtAt.IsZero() {
		body["builtAt"] = builtAt
	}
	if state.Draining {
		body["draining"] = state.Drain
	}
	writeJSON(w, 200, body)
}

// stop asks the daemon to stop: at once, or, with drain, once its running executions end or the deadline, in seconds,
// passes. A forced stop wins over a drain, in the same request or during one.
func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Force    bool `json:"force"`
		Drain    bool `json:"drain"`
		Deadline *int `json:"deadline"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.Drain && !body.Force {
		var deadline time.Duration
		if body.Deadline != nil {
			if *body.Deadline < 1 {
				fail(w, &app.Error{Code: app.CodeBadRequest, Text: domain.T("drain_deadline_invalid",
					"the drain's deadline must be a whole number of seconds above zero")})
				return
			}
			deadline = time.Duration(*body.Deadline) * time.Second
		}
		if err := s.App.RequestDrain(deadline); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 202, map[string]any{"ok": true, "message": "the daemon stops once its running executions end"})
		return
	}
	if err := s.App.RequestShutdown(body.Force); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "message": "the daemon is stopping"})
}

func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.App.Diagnostics(s.Address(), s.Secrets.Kind()))
}

func (s *Server) setHarnessPath(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path *string `json:"path"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	info, err := s.App.SetHarnessPath(r.Context(), r.PathValue("harnessId"), body.Path)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, info)
}

// signInHarness starts a harness's sign-in, or joins the one that runs; the harness comes back with its sign-in
// running.
func (s *Server) signInHarness(w http.ResponseWriter, r *http.Request) {
	info, err := s.App.SignIn(r.PathValue("harnessId"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, info)
}

// setHarnessToken saves a harness's long-lived token; the harness comes back checked again, without the token's value.
func (s *Server) setHarnessToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	info, err := s.App.SetHarnessToken(r.Context(), r.PathValue("harnessId"), body.Token)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, info)
}

// deleteHarnessToken removes a harness's long-lived token; the harness comes back checked on its own sign-in.
func (s *Server) deleteHarnessToken(w http.ResponseWriter, r *http.Request) {
	info, err := s.App.DeleteHarnessToken(r.Context(), r.PathValue("harnessId"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var in app.TaskInput
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	v, err := s.App.CreateTask(r.Context(), in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, v)
}

// updateTemplate reads no body: no template can be changed yet, as UpdateTemplate says.
func (s *Server) updateTemplate(w http.ResponseWriter, r *http.Request) {
	fail(w, s.App.UpdateTemplate(r.PathValue("templateId")))
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	fail(w, s.App.DeleteTemplate(r.PathValue("templateId")))
}

func (s *Server) draftTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Language string `json:"language"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	v, err := s.App.DraftTask(r.Context(), r.PathValue("templateId"), body.Language)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) validateTask(w http.ResponseWriter, r *http.Request) {
	var in app.TaskInput
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, s.App.Validate(r.Context(), in))
}

func (s *Server) importTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML      string `json:"yaml"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	v, err := s.App.ImportTask(r.Context(), body.YAML, body.Overwrite)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	v, err := s.App.GetTask(r.PathValue("taskId"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	var in app.TaskInput
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	v, err := s.App.UpdateTask(r.Context(), r.PathValue("taskId"), in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	pending, err := s.App.DeleteTask(r.PathValue("taskId"))
	if err != nil {
		fail(w, err)
		return
	}
	if pending {
		writeJSON(w, 202, map[string]any{"ok": true, "message": "the task is deleted once its running execution finishes"})
		return
	}
	w.WriteHeader(204)
}

func (s *Server) runTask(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.RunTask(r.PathValue("taskId"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, res)
}

func (s *Server) duplicateTask(w http.ResponseWriter, r *http.Request) {
	v, err := s.App.DuplicateTask(r.Context(), r.PathValue("taskId"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	v, err := s.App.SetEnabled(r.Context(), r.PathValue("taskId"), enabled)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) exportTask(w http.ResponseWriter, r *http.Request) {
	data, err := s.App.ExportTask(r.PathValue("taskId"))
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.yaml"`, r.PathValue("taskId")))
	_, _ = w.Write(data) // the status is sent; a client gone mid-body has no one left to be told
}

func (s *Server) previewSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Schedule domain.Schedule `json:"schedule"`
		Count    int             `json:"count"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, s.App.PreviewSchedule(body.Schedule, body.Count))
}

func (s *Server) listExecutions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := app.ExecutionFilter{TaskID: q.Get("taskId"), Statuses: app.ParseStatuses(q.Get("status")),
		Trigger: q.Get("trigger"), Search: q.Get("search")}
	if v := q.Get("active"); v != "" {
		b := v == "true"
		f.Active = &b
	}
	if v := q.Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v) // a limit that is not a number is 0, the default
	}
	f.Before, f.CreatedFrom, f.CreatedUntil = instant(q.Get("before")), instant(q.Get("createdFrom")), instant(q.Get("createdUntil"))
	page := s.App.ListExecutions(f)
	// The total goes in a header, so that the body stays the page's bare array for every caller.
	w.Header().Set("X-Total-Count", strconv.Itoa(page.Total))
	writeJSON(w, 200, page.Rows)
}

// instant reads an RFC 3339 instant of a query; an empty or malformed one is none, which bounds nothing.
func instant(v string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, v)
	if v == "" || err != nil {
		return nil
	}
	return &t
}

func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	v, err := s.App.GetExecution(r.PathValue("executionId"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) cancelExecution(w http.ResponseWriter, r *http.Request) {
	if err := s.App.CancelExecution(r.PathValue("executionId")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "message": "cancellation requested"})
}

func (s *Server) executionEvents(w http.ResponseWriter, r *http.Request) {
	// A missing or malformed number reads as 0: from the start, and the default limit.
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	events, expired, err := s.App.ExecutionEvents(r.PathValue("executionId"), after, limit)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"events": events, "logExpired": expired})
}

func (s *Server) executionOutput(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64) // a malformed limit is 0, the default
	if limit <= 0 || limit > 8<<20 {
		limit = 1 << 20
	}
	v, err := s.App.ExecutionOutput(r.PathValue("executionId"), limit)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// stream serves the SSE stream: buffered events after Last-Event-ID first, then live ones, with a heartbeat. It ends
// when Shutdown starts, and the client reconnects with Last-Event-ID, as it does when the daemon's process exits.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	stopping := s.stoppingCh()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "internal", domain.Text{Message: "streaming is not supported"})
		return
	}
	// A missing or malformed id reads as 0, every buffered event.
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if v := r.URL.Query().Get("after"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	}
	events, gap, cancel := s.App.Subscribe(after)
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	// A write to a client that left fails silently; the request's context then ends the stream.
	fmt.Fprintf(w, "retry: 2000\n\n")
	if gap {
		fmt.Fprintf(w, "event: replay_gap\ndata: {\"after\":%d}\n\n", after)
	}
	flusher.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-stopping:
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case e, ok := <-events:
			if !ok {
				return // dropped for falling behind; the client reconnects with Last-Event-ID
			}
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, data)
			flusher.Flush()
		}
	}
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var p app.SettingsPatch
	if err := decode(r, &p); err != nil {
		fail(w, err)
		return
	}
	v, err := s.App.UpdateSettings(p)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// rotateToken replaces the token in the credential store; this response is the last the old token authorizes.
func (s *Server) rotateToken(w http.ResponseWriter, r *http.Request) {
	t, err := s.NewToken()
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.Secrets.Set(t); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": "the token was replaced; read it again from the credential store"})
	s.SetToken(t)
}

func (s *Server) markRead(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	s.App.MarkNotificationsRead(body.IDs)
	writeJSON(w, 200, map[string]any{"ok": true})
}
