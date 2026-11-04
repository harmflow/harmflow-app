package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/app"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// busyTask is a script task whose program runs until the execution is stopped; it needs no harness.
const busyTask = `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: busy
  name: Busy
spec:
  type: script
  script:
    command: [/bin/sleep, "600"]
  execution:
    cancelGraceSeconds: 1
`

// startedApp starts the daemon's use cases over a new installation, with no harness, and stops them when the test ends.
func startedApp(t *testing.T) *app.App {
	t.Helper()
	return startedAppWith(t, nil)
}

// startedAppWith is startedApp with system as the reader of the machine's gauges; nil reads none.
func startedAppWith(t *testing.T, system ports.SystemInfo) *app.App {
	t.Helper()
	paths := app.NewPaths(t.TempDir(), t.TempDir(), t.TempDir())
	tasks, err := fsstore.NewTasks(paths.Tasks)
	if err != nil {
		t.Fatal(err)
	}
	state, err := fsstore.NewState(paths.State)
	if err != nil {
		t.Fatal(err)
	}
	execs, err := fsstore.NewExecutions(paths.Executions)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventlog.New(paths.Logs, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := schema.New()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Deps{Paths: paths, Version: "test", Location: time.UTC, Tasks: tasks, State: state,
		Executions: execs, Events: events, Validator: v, TickInterval: time.Hour, Logger: log.New(io.Discard, "", 0),
		System: system})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Stop(domain.Text{Message: "test done"}) })
	return a
}

// harmflow-app#628: with an execution active, POST /api/v1/daemon/stop without force answers 409 with the
// executions_running error and its count, through the handler the daemon serves, and the daemon goes on.
func TestUnforcedStopRequestIsRefusedWhileAnExecutionIsActive(t *testing.T) {
	a := startedApp(t)
	if _, err := a.CreateTask(t.Context(), app.TaskInput{YAML: busyTask}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	res, err := a.RunTask("busy")
	if err != nil {
		t.Fatalf("RunTask(%q): %v", "busy", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		x, err := a.GetExecution(res.ExecutionID)
		if err == nil && x.Status == domain.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s is %s after 30 s, want %s", res.ExecutionID, x.Status, domain.StatusRunning)
		}
		time.Sleep(20 * time.Millisecond)
	}

	s := &Server{App: a}
	s.SetToken(validToken)
	r := httptest.NewRequest("POST", "/api/v1/daemon/stop", strings.NewReader(`{"force":false}`))
	r.Host = "127.0.0.1:7000"
	r.Header.Set("Authorization", "Bearer "+validToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != 409 {
		t.Errorf("POST /api/v1/daemon/stop: status %d, want 409", w.Code)
	}
	var body struct {
		Error struct {
			Code   string            `json:"code"`
			Key    string            `json:"messageKey"`
			Params map[string]string `json:"params"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("POST /api/v1/daemon/stop: body %q: %v", w.Body.String(), err)
	}
	if body.Error.Code != app.CodeExecutionsRunning || body.Error.Key != "executions_running" ||
		body.Error.Params["count"] != "1" {
		t.Errorf("POST /api/v1/daemon/stop: error %+v, want code %q, key %q, count 1", body.Error,
			app.CodeExecutionsRunning, "executions_running")
	}
	select {
	case <-a.ShutdownRequested:
		t.Error("ShutdownRequested is closed after a refused stop, want it open")
	default:
	}
	if !a.AcceptingFirings() {
		t.Error("AcceptingFirings() = false after a refused stop, want true")
	}
	if x, _ := a.GetExecution(res.ExecutionID); x.Status != domain.StatusRunning {
		t.Errorf("execution %s is %s after a refused stop, want %s", res.ExecutionID, x.Status, domain.StatusRunning)
	}
}

// GET /api/v1/executions reads the period from createdFrom, inclusive, and createdUntil, exclusive, RFC 3339 instants
// apart from the before cursor; a malformed one bounds nothing.
func TestListExecutionsReadsThePeriod(t *testing.T) {
	a := startedApp(t)
	if _, err := a.CreateTask(t.Context(), app.TaskInput{YAML: busyTask}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	res, err := a.RunTask("busy")
	if err != nil {
		t.Fatalf("RunTask(%q): %v", "busy", err)
	}
	x, err := a.GetExecution(res.ExecutionID)
	if err != nil {
		t.Fatalf("GetExecution(%q): %v", res.ExecutionID, err)
	}
	// at is the instant d after the execution's creation, as the query writes it.
	at := func(d time.Duration) string { return x.CreatedAt.Add(d).UTC().Format(time.RFC3339Nano) }
	s := &Server{App: a}
	s.SetToken(validToken)
	for _, c := range []struct {
		name  string
		query url.Values
		want  []string
	}{
		{"a period from its creation", url.Values{"createdFrom": {at(0)}}, []string{x.ID}},
		{"a period from after it", url.Values{"createdFrom": {at(time.Nanosecond)}}, nil},
		{"a period until after it", url.Values{"createdUntil": {at(time.Hour)}}, []string{x.ID}},
		{"a period until its creation", url.Values{"createdUntil": {at(0)}}, nil},
		{"a period around it", url.Values{"createdFrom": {at(-time.Hour)}, "createdUntil": {at(time.Hour)}},
			[]string{x.ID}},
		{"a period around it, the cursor at its creation", url.Values{"createdFrom": {at(-time.Hour)},
			"createdUntil": {at(time.Hour)}, "before": {at(0)}}, nil},
		{"a malformed bound", url.Values{"createdFrom": {"tomorrow"}}, []string{x.ID}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := "/api/v1/executions?" + c.query.Encode()
			r := httptest.NewRequest("GET", path, nil)
			r.Host = "127.0.0.1:7000"
			r.Header.Set("Authorization", "Bearer "+validToken)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("GET %s: status %d, want 200", path, w.Code)
			}
			var rows []app.ExecutionSummary
			if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
				t.Fatalf("GET %s: body %q: %v", path, w.Body.String(), err)
			}
			var got []string
			for _, row := range rows {
				got = append(got, row.ID)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("GET %s = %v, want %v", path, got, c.want)
			}
		})
	}
}

// GET /api/v1/executions filters by the trigger and the search text of its query, and reports in X-Total-Count how
// many executions its filters match, past the page, while the body stays the page's bare array.
func TestListExecutionsReportsTheFilteredTotal(t *testing.T) {
	a := startedApp(t)
	if _, err := a.CreateTask(t.Context(), app.TaskInput{YAML: busyTask}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	res, err := a.RunTask("busy")
	if err != nil {
		t.Fatalf("RunTask(%q): %v", "busy", err)
	}
	s := &Server{App: a}
	s.SetToken(validToken)
	for _, c := range []struct {
		name  string
		query url.Values
		want  []string
		total string
	}{
		{"no filter", url.Values{}, []string{res.ExecutionID}, "1"},
		{"the trigger that fired it", url.Values{"trigger": {"manual"}}, []string{res.ExecutionID}, "1"},
		{"another trigger", url.Values{"trigger": {"schedule"}}, nil, "0"},
		{"its task's name, case aside", url.Values{"search": {"BUSY"}}, []string{res.ExecutionID}, "1"},
		{"a text it does not hold", url.Values{"search": {"standup"}}, nil, "0"},
		{"both, matching", url.Values{"trigger": {"manual"}, "search": {"busy"}}, []string{res.ExecutionID}, "1"},
		{"a page past it", url.Values{"limit": {"1"}, "before": {"2000-01-01T00:00:00Z"}}, nil, "1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := "/api/v1/executions?" + c.query.Encode()
			r := httptest.NewRequest("GET", path, nil)
			r.Host = "127.0.0.1:7000"
			r.Header.Set("Authorization", "Bearer "+validToken)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("GET %s: status %d, want 200", path, w.Code)
			}
			if got := w.Header().Get("X-Total-Count"); got != c.total {
				t.Errorf("GET %s: X-Total-Count %q, want %q", path, got, c.total)
			}
			var rows []app.ExecutionSummary
			if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
				t.Fatalf("GET %s: body %q is not an array: %v", path, w.Body.String(), err)
			}
			var got []string
			for _, row := range rows {
				got = append(got, row.ID)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("GET %s = %v, want %v", path, got, c.want)
			}
		})
	}
}

// fakeSystem is a SystemInfo that answers gauges, and sends each directory it is asked about to dirs while it has room.
type fakeSystem struct {
	gauges ports.SystemGauges
	dirs   chan string
}

func (f *fakeSystem) Snapshot(dir string) ports.SystemGauges {
	select {
	case f.dirs <- dir:
	default:
	}
	return f.gauges
}

// get serves GET path through the handler the daemon serves and decodes its 200 answer into keys.
func get(t *testing.T, s *Server, path string) map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Host = "127.0.0.1:7000"
	r.Header.Set("Authorization", "Bearer "+validToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GET %s: status %d, want 200", path, w.Code)
	}
	var keys map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &keys); err != nil {
		t.Fatalf("GET %s: body %q: %v", path, w.Body.String(), err)
	}
	return keys
}

// GET /api/v1/system answers the machine's gauges, each under its key, a gauge not read yet left out and one read as
// 0 kept; GET /api/v1/stats answers the execution counters alone.
func TestSystemGaugesHaveTheirOwnRoute(t *testing.T) {
	system := &fakeSystem{
		gauges: ports.SystemGauges{CPUPercent: new(12.5), MemoryUsedBytes: new(int64(0)), MemoryTotalBytes: new(int64(16e9)),
			DiskUsedBytes: new(int64(48e9)), DiskTotalBytes: new(int64(500e9))},
		dirs: make(chan string, 1),
	}
	a := startedAppWith(t, system)
	s := &Server{App: a}
	s.SetToken(validToken)

	got := get(t, s, "/api/v1/system")
	want := map[string]any{"cpuPercent": 12.5, "memoryUsedBytes": 0.0, "memoryTotalBytes": 16e9, "diskUsedBytes": 48e9,
		"diskTotalBytes": 500e9}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GET /api/v1/system = %v, want %v", got, want)
	}
	select {
	case dir := <-system.dirs:
		if home := a.Paths().Home; dir != home {
			t.Errorf("GET /api/v1/system read the disk of %q, want HarmFlow's directory %q", dir, home)
		}
	default:
		t.Error("GET /api/v1/system read no gauges, want the SystemInfo's snapshot")
	}

	stats := get(t, s, "/api/v1/stats")
	if _, ok := stats["system"]; ok {
		t.Errorf("GET /api/v1/stats = %v, want no system field", stats)
	}
	for _, k := range []string{"running", "queued", "completedToday", "failedToday", "activity"} {
		if _, ok := stats[k]; !ok {
			t.Errorf("GET /api/v1/stats = %v, want %s", stats, k)
		}
	}
	select {
	case <-system.dirs:
		t.Error("GET /api/v1/stats read the gauges, want only the execution counters")
	default:
	}
}

// GET /api/v1/spend answers the month's spend per day, from the first of the month to today, with its total in both
// units.
func TestSpendRouteAnswersTheMonthPerDay(t *testing.T) {
	s := &Server{App: startedApp(t)}
	s.SetToken(validToken)
	got := get(t, s, "/api/v1/spend")
	for _, k := range []string{"month", "daysInMonth", "days", "total"} {
		if _, ok := got[k]; !ok {
			t.Errorf("GET /api/v1/spend = %v, want %s", got, k)
		}
	}
	days, _ := got["days"].([]any)
	if len(days) == 0 {
		t.Fatalf("GET /api/v1/spend days = %v, want today at least", got["days"])
	}
	day, _ := days[0].(map[string]any)
	for _, k := range []string{"date", "costUsd", "tokens", "attempts", "undercount"} {
		if _, ok := day[k]; !ok {
			t.Errorf("GET /api/v1/spend days[0] = %v, want %s", day, k)
		}
	}
	if total, _ := got["total"].(map[string]any); total["costUsd"] != 0.0 || total["tokens"] != 0.0 {
		t.Errorf("GET /api/v1/spend total = %v, want zero in both units with no execution", got["total"])
	}
}

// With no SystemInfo, GET /api/v1/system answers an empty object: every gauge left out.
func TestSystemRouteWithoutAReaderLeavesOutEveryGauge(t *testing.T) {
	s := &Server{App: startedApp(t)}
	s.SetToken(validToken)
	if got := get(t, s, "/api/v1/system"); len(got) != 0 {
		t.Errorf("GET /api/v1/system with no SystemInfo = %v, want {}", got)
	}
}

// A route added through Routes is served by the handler Prepare builds, behind the same checks as the API's own: a
// request without the token is refused, and one with it reaches the route. The API's own routes still answer beside it.
func TestAddedRouteIsServedBehindTheToken(t *testing.T) {
	added := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"added": true}) })
	s := &Server{App: startedApp(t), Routes: []Route{{Pattern: "GET /api/v1/added", Handler: added}}}
	s.SetToken(validToken)
	if err := s.Prepare(); err != nil {
		t.Fatalf("Prepare() = %v, want nil", err)
	}
	serve := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "127.0.0.1:7000"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		return w
	}
	if w := serve("/api/v1/added", ""); w.Code != 401 {
		t.Errorf("GET /api/v1/added without a token: status %d, want 401", w.Code)
	}
	if w := serve("/api/v1/added", "not-the-token"); w.Code != 401 {
		t.Errorf("GET /api/v1/added with a wrong token: status %d, want 401", w.Code)
	}
	if w := serve("/api/v1/added", validToken); w.Code != 200 || !strings.Contains(w.Body.String(), `"added":true`) {
		t.Errorf("GET /api/v1/added with the token: status %d, body %q, want 200 from the added route", w.Code, w.Body.String())
	}
	if w := serve("/api/v1/health", validToken); w.Code != 200 {
		t.Errorf("GET /api/v1/health beside an added route: status %d, want 200", w.Code)
	}
}

// Prepare refuses a route it cannot add, rather than serve without it or panic, and builds no handler: one with no
// handler, a path outside /api/v1/, a host, a pattern http.ServeMux refuses, or one that conflicts with the API's own
// routes or with another added route.
func TestPrepareRefusesARouteItCannotAdd(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	cases := []struct {
		name   string
		routes []Route
	}{
		{"no handler", []Route{{Pattern: "GET /api/v1/added"}}},
		{"outside the API", []Route{{Pattern: "GET /added", Handler: ok}}},
		{"another version", []Route{{Pattern: "GET /api/v2/added", Handler: ok}}},
		{"with a host", []Route{{Pattern: "example.com/api/v1/added", Handler: ok}}},
		{"not a pattern", []Route{{Pattern: "GET /api/v1/{added", Handler: ok}}},
		{"the API's own route", []Route{{Pattern: "GET /api/v1/health", Handler: ok}}},
		{"twice", []Route{{Pattern: "GET /api/v1/added", Handler: ok}, {Pattern: "GET /api/v1/added", Handler: ok}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{App: startedApp(t), Routes: c.routes}
			err := s.Prepare()
			if last := c.routes[len(c.routes)-1].Pattern; err == nil || !strings.Contains(err.Error(), strconv.Quote(last)) {
				t.Errorf("Prepare() with %v = %v, want an error naming %q", c.routes, err, last)
			}
			if s.handler != nil {
				t.Errorf("Prepare() with %v built a handler, want none", c.routes)
			}
		})
	}
}

// With no route added, Prepare builds the API's own routes alone, as Handler does.
func TestPrepareWithNoAddedRoute(t *testing.T) {
	s := &Server{App: startedApp(t)}
	s.SetToken(validToken)
	if err := s.Prepare(); err != nil {
		t.Fatalf("Prepare() = %v, want nil", err)
	}
	for _, c := range []struct {
		path string
		want int
	}{{"/api/v1/health", 200}, {"/api/v1/added", 404}} {
		r := httptest.NewRequest("GET", c.path, nil)
		r.Host = "127.0.0.1:7000"
		r.Header.Set("Authorization", "Bearer "+validToken)
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("GET %s: status %d, want %d", c.path, w.Code, c.want)
		}
	}
}
