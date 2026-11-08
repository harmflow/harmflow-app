package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/app"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// postStop serves POST /api/v1/daemon/stop with body through the handler the daemon serves, and returns its status
// and the messageKey of its error, if any.
func postStop(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/daemon/stop", strings.NewReader(body))
	r.Host = "127.0.0.1:7000"
	r.Header.Set("Authorization", "Bearer "+validToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var answer struct {
		Error struct {
			Key string `json:"messageKey"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &answer)
	return w.Code, answer.Error.Key
}

// runningBusy starts busyTask and waits until its execution runs.
func runningBusy(t *testing.T, a *app.App) string {
	t.Helper()
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
			return res.ExecutionID
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s is %s after 30 s, want %s", res.ExecutionID, x.Status, domain.StatusRunning)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// closedWithin reports whether ch is closed within d.
func closedWithin(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// With an execution running, a draining stop is accepted at once: health shows the drain, with the execution it waits
// for and its deadline, the daemon goes on until the execution ends, and a forced stop during the drain stops it.
func TestDrainingStopRequest(t *testing.T) {
	a := startedApp(t)
	runningBusy(t, a)
	s := &Server{App: a}
	s.SetToken(validToken)

	asked := time.Now()
	if status, key := postStop(t, s, `{"drain":true,"deadline":600}`); status != 202 {
		t.Fatalf("POST /api/v1/daemon/stop with drain: status %d (%s), want 202", status, key)
	}
	health := get(t, s, "/api/v1/health")
	drain, ok := health["draining"].(map[string]any)
	if !ok {
		t.Fatalf("GET /api/v1/health during a drain = %v, want draining", health)
	}
	if drain["executions"] != float64(1) {
		t.Errorf("GET /api/v1/health: draining.executions = %v, want 1", drain["executions"])
	}
	at, err := time.Parse(time.RFC3339Nano, drain["deadlineAt"].(string))
	if err != nil || at.Before(asked.Add(599*time.Second)) || at.After(time.Now().Add(601*time.Second)) {
		t.Errorf("GET /api/v1/health: draining.deadlineAt = %v (%v), want 600 s after the request", drain["deadlineAt"], err)
	}
	if health["acceptingFirings"] != false {
		t.Errorf("GET /api/v1/health: acceptingFirings = %v during a drain, want false", health["acceptingFirings"])
	}
	if closedWithin(a.ShutdownRequested, 100*time.Millisecond) {
		t.Fatal("ShutdownRequested is closed while the drain waits for the execution, want it open")
	}

	if status, key := postStop(t, s, `{"force":true}`); status != 202 {
		t.Fatalf("POST /api/v1/daemon/stop with force during a drain: status %d (%s), want 202", status, key)
	}
	if !closedWithin(a.ShutdownRequested, time.Second) {
		t.Error("ShutdownRequested is open after a forced stop during a drain, want it closed")
	}
}

// With nothing running, a draining stop with no deadline asks the daemon to stop at once.
func TestDrainingStopRequestWithNothingRunning(t *testing.T) {
	a := startedApp(t)
	s := &Server{App: a}
	s.SetToken(validToken)
	if status, key := postStop(t, s, `{"drain":true}`); status != 202 {
		t.Fatalf("POST /api/v1/daemon/stop with drain: status %d (%s), want 202", status, key)
	}
	if !closedWithin(a.ShutdownRequested, 10*time.Second) {
		t.Error("ShutdownRequested is open after a drain with nothing running, want it closed")
	}
}

// A deadline below one second is refused with 400, and changes nothing.
func TestDrainingStopRequestRefusesABadDeadline(t *testing.T) {
	a := startedApp(t)
	s := &Server{App: a}
	s.SetToken(validToken)
	for _, body := range []string{`{"drain":true,"deadline":0}`, `{"drain":true,"deadline":-5}`} {
		if status, key := postStop(t, s, body); status != 400 || key != "drain_deadline_invalid" {
			t.Errorf("POST /api/v1/daemon/stop %s: status %d, key %q; want 400, drain_deadline_invalid", body, status, key)
		}
	}
	if _, ok := get(t, s, "/api/v1/health")["draining"]; ok {
		t.Error("GET /api/v1/health after a refused drain shows draining, want none")
	}
	if !a.AcceptingFirings() {
		t.Error("AcceptingFirings() = false after a refused drain, want true")
	}
}
