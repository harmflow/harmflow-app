package harmflowd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// post sends body as JSON to path on the daemon, with its token, and returns the status and the body.
func (r *running) post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", "http://"+r.address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("POST %s: reading the body: %v", path, err)
	}
	return res.StatusCode, string(data)
}

// sameJSON reports whether v, marshalled, decodes to the same value as body, a JSON text the local API answered.
func sameJSON(t *testing.T, v any, body string) bool {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body), &want); err != nil {
		t.Fatalf("the local API answered %q, which is no JSON: %v", body, err)
	}
	return reflect.DeepEqual(got, want)
}

// keep is a fake module's registration that keeps the daemon it is given.
func keep(given **Daemon) Registration {
	return func(d *Daemon) error {
		*given = d
		return nil
	}
}

// A module checks a task document with the validation the local API's task validation applies, and gets each error.
func TestARegistrationValidatesATaskAsTheLocalAPIDoes(t *testing.T) {
	var d *Daemon
	r := start(t, keep(&d))
	cases := []struct {
		name string
		yaml string
	}{
		{"YAML that does not parse", "name: [\n"},
		{"a document missing its fields", "name: only a name\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := d.ValidateTask(context.Background(), TaskInput{YAML: c.yaml})
			if got.Valid || len(got.Errors) == 0 {
				t.Errorf("ValidateTask(%q) = valid %v with %d errors, want invalid with errors", c.yaml, got.Valid, len(got.Errors))
			}
			body, _ := json.Marshal(map[string]string{"yaml": c.yaml})
			status, want := r.post(t, "/api/v1/tasks/validate", string(body))
			if status != 200 {
				t.Fatalf("POST /api/v1/tasks/validate: status %d, body %q, want 200", status, want)
			}
			if !sameJSON(t, got, want) {
				t.Errorf("ValidateTask(%q) = %+v, want what POST /api/v1/tasks/validate answers, %s", c.yaml, got, want)
			}
		})
	}
}

// A module reads the execution records, their events, and their output, as the local API's executions routes give them.
func TestARegistrationReadsTheExecutionRecords(t *testing.T) {
	home := isolate(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ended := created.Add(time.Minute)
	record := domain.Execution{SchemaVersion: "1", ID: "exec-20260102-030405-abcdef", TaskID: "a-task", TaskName: "A task",
		TaskType: "prompt", Status: domain.ExecutionStatus("completed"), Trigger: domain.Trigger("manual"),
		CreatedAt: created, StartedAt: &created, EndedAt: &ended, Harness: "claude-code"}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "state", "executions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, record.ID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var d *Daemon
	r := startAt(t, home, keep(&d))

	page := d.Executions(ExecutionFilter{})
	if page.Total != 1 || len(page.Rows) != 1 || page.Rows[0].ID != record.ID {
		t.Fatalf("Executions() = %+v, want the one record %s", page, record.ID)
	}
	if status, want := r.request(t, "GET", "/api/v1/executions", true); status != 200 || !sameJSON(t, page.Rows, want) {
		t.Errorf("Executions() = %+v, want what GET /api/v1/executions answers, %d %s", page.Rows, status, want)
	}

	view, err := d.Execution(record.ID)
	if err != nil {
		t.Fatalf("Execution(%q) = %v", record.ID, err)
	}
	if status, want := r.request(t, "GET", "/api/v1/executions/"+record.ID, true); status != 200 || !sameJSON(t, view, want) {
		t.Errorf("Execution(%q) = %+v, want what the local API answers, %d %s", record.ID, view, status, want)
	}

	events, expired, err := d.ExecutionEvents(record.ID, 0, 0)
	if err != nil {
		t.Fatalf("ExecutionEvents(%q) = %v", record.ID, err)
	}
	got := map[string]any{"events": events, "logExpired": expired}
	if status, want := r.request(t, "GET", "/api/v1/executions/"+record.ID+"/events", true); status != 200 || !sameJSON(t, got, want) {
		t.Errorf("ExecutionEvents(%q) = %v, want what the local API answers, %d %s", record.ID, got, status, want)
	}

	output, err := d.ExecutionOutput(record.ID, 0)
	if err != nil {
		t.Fatalf("ExecutionOutput(%q) = %v", record.ID, err)
	}
	if status, want := r.request(t, "GET", "/api/v1/executions/"+record.ID+"/output", true); status != 200 || !sameJSON(t, output, want) {
		t.Errorf("ExecutionOutput(%q) = %+v, want what the local API answers, %d %s", record.ID, output, status, want)
	}

	const unknown = "exec-20260102-030405-000000"
	if _, err := d.Execution(unknown); err == nil {
		t.Errorf("Execution(%q) = nil error, want the execution unknown", unknown)
	}
	if _, _, err := d.ExecutionEvents(unknown, 0, 0); err == nil {
		t.Errorf("ExecutionEvents(%q) = nil error, want the execution unknown", unknown)
	}
	if _, err := d.ExecutionOutput(unknown, 0); err == nil {
		t.Errorf("ExecutionOutput(%q) = nil error, want the execution unknown", unknown)
	}
}
