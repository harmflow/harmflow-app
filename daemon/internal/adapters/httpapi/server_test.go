package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/app"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
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
		Executions: execs, Events: events, Validator: v, TickInterval: time.Hour, Logger: log.New(io.Discard, "", 0)})
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
