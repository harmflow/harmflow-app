package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A GitHub task's queue, edited as the dashboard's editor saves it, reaches the next execution: the context.json the
// daemon gives each session holds the task's queue under task.parameters, the defaults before the edit and the edited
// values after it. An edit the task schema refuses changes neither the task file nor what the next execution gets. The
// daemon enforces none of these settings; the session reads them there. The stand-in Claude Code runs each execution.
func TestEditedQueueReachesTheNextExecution(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	d := startDaemon(t, home)
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The repository is outside the home folder, so an allowed folder.
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{repo}}, 200)
	const id = "queue-edit"
	d.call("POST", "/tasks", map[string]any{"yaml": issueTask(id, repo)}, 201)
	defaults := map[string]any{"ordering": "oldest_first", "maxDiscoveredPerCycle": 10.0, "maxItemsPerCycle": 1.0,
		"resumeStrategy": "resume_first", "maxResumeAttempts": 3.0, "maxFailedRetries": 3.0}
	if got := nextQueue(t, d, id); !reflect.DeepEqual(got, defaults) {
		t.Errorf("the first execution's context.json gives task.parameters.queue = %v, want the defaults %v", got, defaults)
	}

	edited := issueTask(id, repo) + "    queue:\n      ordering: newest_first\n      maxDiscoveredPerCycle: 4\n" +
		"      maxItemsPerCycle: 2\n      resumeStrategy: new_first\n"
	want := map[string]any{"ordering": "newest_first", "maxDiscoveredPerCycle": 4.0, "maxItemsPerCycle": 2.0,
		"resumeStrategy": "new_first", "maxResumeAttempts": 3.0, "maxFailedRetries": 3.0}
	rev := d.call("GET", "/tasks/"+id, nil, 200)["revision"]
	d.call("PUT", "/tasks/"+id, map[string]any{"yaml": edited, "baseRevision": rev}, 200)
	if got := nextQueue(t, d, id); !reflect.DeepEqual(got, want) {
		t.Errorf("after the edit, the next execution's context.json gives task.parameters.queue = %v, want %v", got, want)
	}

	files := taskFiles(t, home)
	rev = d.call("GET", "/tasks/"+id, nil, 200)["revision"]
	for _, c := range []struct {
		name, from, to string
		key, path      string
	}{
		{"no item a cycle", "maxItemsPerCycle: 2", "maxItemsPerCycle: 0", "schema_minimum",
			"/spec/github/queue/maxItemsPerCycle"},
		{"an order the schema does not list", "ordering: newest_first", "ordering: random", "schema_enum",
			"/spec/github/queue/ordering"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := d.in(t)
			invalid := strings.Replace(edited, c.from, c.to, 1)
			if key, path := d.refusal("PUT", "/tasks/"+id, map[string]any{"yaml": invalid, "baseRevision": rev}); key != c.key ||
				path != c.path {
				t.Errorf("PUT /tasks/%s with %q refused with %s at %s, want %s at %s", id, c.to, key, path, c.key, c.path)
			}
		})
	}
	if after := taskFiles(t, home); !reflect.DeepEqual(after, files) {
		t.Errorf("the refused edits changed the task files: %v, was %v", after, files)
	}
	if got := nextQueue(t, d, id); !reflect.DeepEqual(got, want) {
		t.Errorf("after the refused edits, the next execution's context.json gives task.parameters.queue = %v, want the "+
			"saved edit's %v", got, want)
	}
}

// nextQueue runs the task once and returns the queue its execution's context.json gives the session, once the
// execution has completed: a task runs one execution at a time.
func nextQueue(t *testing.T, d *daemon, taskID string) map[string]any {
	t.Helper()
	id := d.call("POST", "/tasks/"+taskID+"/run", nil, 202)["executionId"].(string)
	x := d.waitStatus(id, "completed")
	return contextQueue(t, x["executionDir"].(string))
}

// contextQueue is the queue the execution's context.json gives its session, task.parameters.queue.
func contextQueue(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ctx struct {
		Task struct {
			Parameters struct {
				Queue map[string]any `json:"queue"`
			} `json:"parameters"`
		} `json:"task"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		t.Fatalf("context.json: %v", err)
	}
	return ctx.Task.Parameters.Queue
}
