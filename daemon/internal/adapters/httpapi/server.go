// Package httpapi is the local API (contracts/api/openapi.yaml): HTTP on 127.0.0.1 with an SSE stream. Every
// request carries the per-installation token; a Host that is not loopback (DNS rebinding) or an Origin that is a web
// page (CSRF) is rejected (D-090, FS-009 FR-005).
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

type Server struct {
	App      *app.App
	Secrets  ports.SecretStore
	NewToken func() (string, error)
	// Logger is daemon.log, where rejected requests are recorded; nil records none.
	Logger *log.Logger
	// Clock defaults to the system clock.
	Clock ports.Clock

	mu       sync.RWMutex
	token    string
	listener net.Listener
	port     int
	srv      *http.Server
	rejected rejections
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

func (s *Server) Port() int       { return s.port }
func (s *Server) Address() string { return fmt.Sprintf("127.0.0.1:%d", s.port) }

func (s *Server) SetToken(t string) {
	s.mu.Lock()
	s.token = t
	s.mu.Unlock()
}

func (s *Server) Serve() error {
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	err := s.srv.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops serving, then records the rejected requests no entry has counted yet.
func (s *Server) Shutdown(ctx context.Context) {
	if s.srv != nil {
		_ = s.srv.Shutdown(ctx)
	}
	s.flushRejections()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, fn func(w http.ResponseWriter, r *http.Request)) { mux.HandleFunc(pattern, fn) }

	h("GET /api/v1/health", s.health)
	h("POST /api/v1/daemon/stop", s.stop)
	h("GET /api/v1/diagnostics", s.diagnostics)
	h("GET /api/v1/stats", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.Stats()) })
	h("GET /api/v1/harnesses", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.Harnesses()) })
	h("POST /api/v1/harnesses/refresh", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.App.RefreshHarnesses(r.Context()))
	})
	h("PUT /api/v1/harnesses/{harnessId}", s.setHarnessPath)
	h("GET /api/v1/tools", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.Tools(r.Context())) })
	h("GET /api/v1/catalog", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.App.Catalog()) })

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
	return s.guard(mux)
}

// guard enforces the Host and Origin checks and the bearer token on every route, the SSE stream included, and records
// each request it rejects (FS-009 FR-003).
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
	body := map[string]any{"status": "ok", "version": s.App.Version(), "apiVersion": "v1",
		"pid": pid(), "startedAt": s.App.StartedAt(), "acceptingFirings": s.App.AcceptingFirings()}
	if commit := s.App.Commit(); commit != "" {
		body["commit"] = commit
	}
	writeJSON(w, 200, body)
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Force bool `json:"force"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
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

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var in app.TaskInput
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	v, err := s.App.CreateTask(in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) validateTask(w http.ResponseWriter, r *http.Request) {
	var in app.TaskInput
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, s.App.Validate(in))
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
	v, err := s.App.ImportTask(body.YAML, body.Overwrite)
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
	v, err := s.App.UpdateTask(r.PathValue("taskId"), in)
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
	v, err := s.App.DuplicateTask(r.PathValue("taskId"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	v, err := s.App.SetEnabled(r.PathValue("taskId"), enabled)
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
	_, _ = w.Write(data)
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
	f := app.ExecutionFilter{TaskID: q.Get("taskId"), Statuses: app.ParseStatuses(q.Get("status"))}
	if v := q.Get("active"); v != "" {
		b := v == "true"
		f.Active = &b
	}
	if v := q.Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v)
	}
	if v := q.Get("before"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			f.Before = &t
		}
	}
	writeJSON(w, 200, s.App.ListExecutions(f))
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
	limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64)
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

// stream serves the SSE stream: buffered events after Last-Event-ID first, then live ones, with a heartbeat.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "internal", domain.Text{Message: "streaming is not supported"})
		return
	}
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
