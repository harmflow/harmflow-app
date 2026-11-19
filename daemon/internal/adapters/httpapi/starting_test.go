package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/app"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// heldRecovery is the execution records, whose Summaries, which the start's recovery reads first, waits for release
// once it has closed entered: a recovery held open, as a large history on a slow disk holds it.
type heldRecovery struct {
	*fsstore.Executions
	entered chan struct{}
	release chan struct{}
}

var _ ports.ExecutionRepository = (*heldRecovery)(nil)

func (h *heldRecovery) Summaries() ([]*domain.ExecutionSummary, error) {
	close(h.entered)
	<-h.release
	return h.Executions.Summaries()
}

// answer sends one request through the handler the daemon serves, with the token when withToken is true, and returns
// its status and its error's code and message key.
func answer(t *testing.T, s *Server, method, path string, withToken bool) (status int, code, key string) {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Host = "127.0.0.1:7000"
	if withToken {
		r.Header.Set("Authorization", "Bearer "+validToken)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var body struct {
		Error struct {
			Code string `json:"code"`
			Key  string `json:"messageKey"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body) // a body that is no error leaves both empty
	return w.Code, body.Error.Code, body.Error.Key
}

// harmflow-app#1091: until the start has recovered the interrupted executions, the local API answers its health at
// once as starting, with no firing accepted, and every other route with 503 and the key daemon_starting; a request
// without the token is still refused first. Once the recovery ends, health reads ok and the routes answer.
func TestTheAPIAnswersThatTheDaemonIsStillStarting(t *testing.T) {
	paths := app.NewPaths(t.TempDir(), t.TempDir(), t.TempDir())
	tasks, err := fsstore.NewTasks(paths.Tasks)
	if err != nil {
		t.Fatal(err)
	}
	templates, err := fsstore.NewTasks(paths.Templates)
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
	held := &heldRecovery{Executions: execs, entered: make(chan struct{}), release: make(chan struct{})}
	a, err := app.New(app.Deps{Paths: paths, Version: "test", Location: time.UTC, Tasks: tasks, Templates: templates,
		State: state, Executions: held, Events: events, Validator: v, TickInterval: time.Hour,
		Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{App: a}
	s.SetToken(validToken)
	if !a.Starting() {
		t.Fatal("Starting() = false before Start, want true")
	}

	started := make(chan error, 1)
	go func() { started <- a.Start(context.Background()) }()
	t.Cleanup(func() { a.Stop(domain.Text{Message: "test done"}) })
	released := false
	defer func() {
		if !released {
			close(held.release) // a test that fails while the recovery is held lets the start end
			<-started
		}
	}()
	select {
	case <-held.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the start did not reach the recovery within 10 s")
	}

	health := get(t, s, "/api/v1/health")
	if health["status"] != "starting" || health["acceptingFirings"] != false {
		t.Errorf("GET /api/v1/health while the recovery is held: status %v, acceptingFirings %v; want starting, false",
			health["status"], health["acceptingFirings"])
	}
	for _, route := range [][2]string{{"GET", "/api/v1/tasks"}, {"GET", "/api/v1/events/stream"},
		{"POST", "/api/v1/daemon/stop"}} {
		if status, code, key := answer(t, s, route[0], route[1], true); status != 503 || code != "daemon_starting" ||
			key != "daemon_starting" {
			t.Errorf("%s %s while the recovery is held: status %d, code %q, key %q; want 503, daemon_starting",
				route[0], route[1], status, code, key)
		}
	}
	if status, _, _ := answer(t, s, "GET", "/api/v1/tasks", false); status != 401 {
		t.Errorf("GET /api/v1/tasks without the token while the recovery is held: status %d, want 401", status)
	}

	close(held.release)
	released = true
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start() = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the start did not end within 10 s of the recovery's release")
	}
	health = get(t, s, "/api/v1/health")
	if health["status"] != "ok" || health["acceptingFirings"] != true {
		t.Errorf("GET /api/v1/health once started: status %v, acceptingFirings %v; want ok, true", health["status"],
			health["acceptingFirings"])
	}
	if status, code, _ := answer(t, s, "GET", "/api/v1/tasks", true); status != 200 {
		t.Errorf("GET /api/v1/tasks once started: status %d (%s), want 200", status, code)
	}
}
