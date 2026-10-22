// Package integration runs the real harmflowd binary and talks to it over its local API, with stand-in harness CLIs
// that speak the real output formats. These are the E3 tests across the daemon's process boundary.
package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

var binary string

// builtAt is the build time the binary is stamped with, as make stamps it.
const builtAt = "2026-10-22T20:30:05Z"

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "harmflowd-bin")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "harmflowd")
	args := []string{"build", "-ldflags", "-X main.BuiltAt=" + builtAt, "-o", binary}
	// make coverage-daemon sets GOCOVERDIR: each daemon then writes its counters there when it exits, to be merged
	// with the unit tests' own, in the same counter mode (ADR-015).
	if os.Getenv("GOCOVERDIR") != "" {
		args = append(args, "-cover", "-covermode=atomic")
	}
	build := exec.Command("go", append(args, "./cmd/harmflowd")...)
	build.Dir = filepath.Join(repoRoot(), "daemon")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic("building harmflowd: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

type daemon struct {
	t     *testing.T
	home  string
	cmd   *exec.Cmd
	base  string
	token string
}

func startDaemon(t *testing.T, home string) *daemon {
	t.Helper()
	_ = os.Remove(filepath.Join(home, "run", "daemon.json"))
	cmd := exec.Command(binary, "--home", home, "--builtins", repoRoot())
	// Codex's configuration is the test's own, never the user's.
	cmd.Env = append(os.Environ(), "HARMFLOW_TOKEN_STORE=file", "CODEX_HOME="+filepath.Join(home, "codex"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d := &daemon{t: t, home: home, cmd: cmd}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(home, "run", "daemon.json"))
		if err == nil {
			var rt struct {
				Address string `json:"address"`
			}
			if json.Unmarshal(data, &rt) == nil && rt.Address != "" {
				tok, _ := os.ReadFile(filepath.Join(home, "run", "token"))
				d.base = "http://" + rt.Address + "/api/v1"
				d.token = strings.TrimSpace(string(tok))
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if d.base == "" {
		_ = cmd.Process.Kill()
		t.Fatalf("the daemon did not start:\n%s", out.String())
	}
	t.Cleanup(func() { d.stop() })
	fakes := filepath.Join(repoRoot(), "tests", "integration", "testdata")
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": filepath.Join(fakes, "fake-claude")}, 200)
	d.call("PUT", "/harnesses/codex", map[string]any{"path": filepath.Join(fakes, "fake-codex")}, 200)
	return d
}

func (d *daemon) stop() {
	if d.cmd.ProcessState != nil {
		return
	}
	_ = d.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = d.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = d.cmd.Process.Kill()
		<-done
	}
}

func (d *daemon) kill() {
	_ = d.cmd.Process.Kill()
	_ = d.cmd.Wait()
}

func (d *daemon) request(method, path string, body any, headers map[string]string) (int, []byte) {
	d.t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, d.base+path, r)
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (d *daemon) call(method, path string, body any, want int) map[string]any {
	d.t.Helper()
	status, data := d.request(method, path, body, nil)
	if status != want {
		d.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, data)
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

func (d *daemon) list(path string) []map[string]any {
	d.t.Helper()
	status, data := d.request("GET", path, nil, nil)
	if status != 200 {
		d.t.Fatalf("GET %s: %d %s", path, status, data)
	}
	var out []map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

func (d *daemon) waitStatus(id string, want ...string) map[string]any {
	d.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var x map[string]any
	for time.Now().Before(deadline) {
		x = d.call("GET", "/executions/"+id, nil, 200)
		for _, w := range want {
			if x["status"] == w {
				return x
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.t.Fatalf("execution %s is %v, want %v", id, x["status"], want)
	return nil
}

func task(id, prompt, extra string) string {
	return fmt.Sprintf(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: %s
  name: Task %s
spec:
  type: general
  prompt: %q
  harness:
    preferred:
      provider: claude-code
%s`, id, id, prompt, extra)
}

// FS-009 FR-004, FR-005: the token is required everywhere; a web page's Origin and a non-loopback Host are refused.
// FS-009 FR-003, AC-003: each refusal is recorded in daemon.log with its reason, never with the token it presented.
func TestAPIProtection(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	saved := d.token
	d.token = ""
	if s, _ := d.request("GET", "/tasks", nil, nil); s != 401 {
		t.Errorf("no token: %d", s)
	}
	d.token = "wrong"
	if s, _ := d.request("GET", "/tasks", nil, nil); s != 401 {
		t.Errorf("wrong token: %d", s)
	}
	if s, _ := d.request("GET", "/events/stream", nil, nil); s != 401 {
		t.Errorf("the SSE stream accepted a wrong token: %d", s)
	}
	d.token = saved
	if s, _ := d.request("POST", "/tasks/x/run", nil, map[string]string{"Origin": "https://example.com"}); s != 403 {
		t.Errorf("web Origin: %d", s)
	}
	if s, _ := d.request("GET", "/health", nil, map[string]string{"Host": "attacker.example:80"}); s != 403 {
		t.Errorf("rebinding Host: %d", s)
	}
	info, err := os.Stat(filepath.Join(d.home, "run", "daemon.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("runtime file %v %v", err, info)
	}

	// The second wrong token comes within a minute of the first, so it waits to be counted in a later entry.
	data, err := os.ReadFile(filepath.Join(d.home, "logs", "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"missing token": "GET /api/v1/tasks", "invalid token": "GET /api/v1/tasks",
		"forbidden Origin": "POST /api/v1/tasks/x/run", "forbidden Host": "GET /api/v1/health"}
	got := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		m := rejectionEntry.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if strings.Contains(line, "wrong") {
			t.Errorf("an entry holds the presented token: %s", line)
		}
		path, _ := strconv.Unquote(m[2])
		if _, seen := got[m[3]]; seen {
			t.Errorf("a second entry for %s: %s", m[3], line)
		}
		got[m[3]] = m[1] + " " + path
		if m[4] != "1" {
			t.Errorf("%s: count %s, want 1", m[3], m[4])
		}
	}
	for reason, request := range want {
		if got[reason] != request {
			t.Errorf("%s: entry for %q, want %q\n%s", reason, got[reason], request, data)
		}
	}
	if len(got) != len(want) {
		t.Errorf("entries %v, want one for each of %v", got, want)
	}
}

// rejectionEntry is a line of daemon.log recording rejected requests: the last one's method and path, the reason, and
// how many requests the entry stands for since when.
var rejectionEntry = regexp.MustCompile(`local API rejected (\S+) ("(?:[^"\\]|\\.)*"): ([^(]+) \((\d+) requests? since (\S+)\)$`)

// GET /health names the commit go build stamped in the binary, and leaves it out when go build stamped none: outside
// a checkout, or in a git worktree.
func TestHealthNamesTheCommit(t *testing.T) {
	out, err := exec.Command("go", "version", "-m", binary).Output()
	if err != nil {
		t.Fatal(err)
	}
	var revision string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "build" && strings.HasPrefix(f[1], "vcs.revision=") {
			revision = strings.TrimPrefix(f[1], "vcs.revision=")
		}
	}
	d := startDaemon(t, t.TempDir())
	health := d.call("GET", "/health", nil, 200)
	commit, present := health["commit"].(string)
	switch {
	case revision == "" && present:
		t.Errorf("commit %q, but go build stamped none", commit)
	case revision != "" && commit != revision[:7] && commit != revision[:7]+"-dirty":
		t.Errorf("commit %q, want %s with or without -dirty", commit, revision[:7])
	}
}

// GET /health names the build time stamped with -X main.BuiltAt, as make stamps it.
func TestHealthNamesTheBuildTime(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	if got := d.call("GET", "/health", nil, 200)["builtAt"]; got != builtAt {
		t.Errorf("builtAt %v, want %s", got, builtAt)
	}
}

// FS-001 AC-001, AC-003, AC-004, AC-005, AC-006, AC-007, AC-008: the daemon is the only writer; form and YAML go
// through one validation; files and dashboard converge; conflicts are reported.
func TestTaskLifecycle(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	doc := map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": "notes", "name": "Notes"},
		"spec": map[string]any{"type": "general", "prompt": "Write notes.",
			"harness":  map[string]any{"preferred": map[string]any{"provider": "claude-code"}},
			"schedule": map[string]any{"cron": "0 9 * * 1-5", "timezone": "America/Sao_Paulo"}}}
	created := d.call("POST", "/tasks", map[string]any{"document": doc}, 201)
	file := filepath.Join(d.home, "tasks", "notes.yaml")
	onDisk, err := os.ReadFile(file)
	if err != nil || string(onDisk) != created["yaml"] {
		t.Fatalf("file and API differ: %v", err)
	}
	if created["nextFireAt"] == nil {
		t.Error("no next firing")
	}
	// An empty list the form sends is a choice the file keeps, not a key dropped for its default (FS-011 FR-002).
	empty := map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": "form-empty", "name": "Form empty"},
		"spec": map[string]any{"type": "general", "prompt": "p",
			"harness": map[string]any{"preferred": map[string]any{"provider": "claude-code"},
				"fallback": map[string]any{"on": []any{}}},
			"notifications": map[string]any{"native": map[string]any{"events": []any{}}}}}
	d.call("POST", "/tasks", map[string]any{"document": empty}, 201)
	if data, err := os.ReadFile(filepath.Join(d.home, "tasks", "form-empty.yaml")); err != nil ||
		!strings.Contains(string(data), "events: []") {
		t.Errorf("the file dropped the empty events list (%v):\n%s", err, data)
	}
	// The same schema error from the form and from the editor.
	bad := map[string]any{"document": map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": "Bad ID", "name": "x"}, "spec": doc["spec"]}}
	v1 := d.call("POST", "/tasks/validate", bad, 200)
	v2 := d.call("POST", "/tasks/validate", map[string]any{"yaml": strings.Replace(string(onDisk), "id: notes", "id: Bad ID", 1)}, 200)
	e1 := v1["errors"].([]any)[0].(map[string]any)
	e2 := v2["errors"].([]any)[0].(map[string]any)
	if v1["valid"] != false || e1["path"] != e2["path"] || e1["message"] != e2["message"] || e2["line"] == nil {
		t.Errorf("form and editor disagree: %v / %v", e1, e2)
	}
	// The desktop translates the error from its key and params (FS-012 FR-005).
	if e1["messageKey"] != "schema_pattern" || e2["messageKey"] != e1["messageKey"] || e1["params"] == nil {
		t.Errorf("no translatable key: %v / %v", e1, e2)
	}
	// An invalid save keeps the valid file.
	rev := created["revision"].(string)
	invalid := d.call("PUT", "/tasks/notes",
		map[string]any{"yaml": "apiVersion: harmflow/v1alpha1\nkind: Task\n", "baseRevision": rev}, 422)
	if e, _ := invalid["error"].(map[string]any); e["messageKey"] == nil || e["message"] == nil {
		t.Errorf("an invalid save's error has no translatable key: %v", invalid)
	}
	if again, _ := os.ReadFile(file); !bytes.Equal(again, onDisk) {
		t.Error("an invalid edit changed the file")
	}
	// An external edit converges and makes the old revision a conflict.
	_ = os.WriteFile(file, bytes.Replace(onDisk, []byte("name: Notes"), []byte("name: Edited outside"), 1), 0o600)
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if got := d.call("GET", "/tasks/notes", nil, 200); got["name"] == "Edited outside" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := d.call("GET", "/tasks/notes", nil, 200); got["name"] != "Edited outside" {
		t.Fatalf("the external edit did not converge: %v", got["name"])
	}
	conflict := d.call("PUT", "/tasks/notes", map[string]any{"yaml": string(onDisk), "baseRevision": rev}, 409)
	if conflict["error"].(map[string]any)["code"] != "conflict" {
		t.Errorf("conflict body %v", conflict)
	}
	// Export carries the definition only; import makes an editable task.
	status, exported := d.request("GET", "/tasks/notes/export", nil, nil)
	if status != 200 || strings.Contains(string(exported), "state") || !strings.Contains(string(exported), "Edited outside") {
		t.Errorf("export %d %s", status, exported)
	}
	d.call("POST", "/tasks/import", map[string]any{"yaml": strings.Replace(string(exported), "id: notes", "id: imported", 1)}, 201)
	dup := d.call("POST", "/tasks/notes/duplicate", nil, 201)
	if dup["id"] != "notes-copy" || dup["enabled"] != false {
		t.Errorf("duplicate %v %v", dup["id"], dup["enabled"])
	}
	if s, _ := d.request("DELETE", "/tasks/notes", nil, nil); s != 204 {
		t.Errorf("delete %d", s)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("the file survived deletion")
	}
	preview := d.call("POST", "/schedules/preview", map[string]any{"schedule": map[string]any{"cron": "0 9 * * *",
		"timezone": "America/Sao_Paulo"}, "count": 3}, 200)
	occ := preview["occurrences"].([]any)
	if len(occ) != 3 || !strings.Contains(occ[0].(string), "T09:00:00-03:00") {
		t.Errorf("preview %v", preview)
	}
}

// FS-001 AC-001, AC-005 (FR-004, ADR-005): a form document that sets every section is written as a file equal to it,
// which passes validation and is listed; after an edit outside the daemon, the task the API returns is the file.
func TestFormDocumentIsTheFile(t *testing.T) {
	home := t.TempDir()
	// Codex's models cache, read when the harness is detected, makes the candidate's model and effort ones the daemon
	// checks rather than only declares.
	codexHome := filepath.Join(home, "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-5.6-terra","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := startDaemon(t, home)
	repo := t.TempDir()
	complete := completeTask("complete", repo)
	// An empty list of failover triggers or notified events is a choice the file keeps (harmflow/harmflow-app#83).
	empty := completeTask("empty-lists", repo)
	spec := empty["spec"].(map[string]any)
	spec["harness"].(map[string]any)["fallback"].(map[string]any)["on"] = []any{}
	spec["notifications"].(map[string]any)["native"].(map[string]any)["events"] = []any{}
	for _, doc := range []map[string]any{complete, empty} {
		id := doc["metadata"].(map[string]any)["id"].(string)
		created := d.call("POST", "/tasks", map[string]any{"document": doc}, 201)
		data, err := os.ReadFile(filepath.Join(home, "tasks", id+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != created["yaml"] {
			t.Errorf("%s: the file is not the yaml the API returned:\n%s", id, data)
		}
		if file, sent := yamlAsJSON(t, data), jsonShape(t, doc); !reflect.DeepEqual(file, sent) {
			t.Errorf("%s: the file is not the document sent:\nfile     %s\ndocument %s", id, mustJSON(file), mustJSON(sent))
		}
		if v := d.call("POST", "/tasks/validate", map[string]any{"yaml": string(data)}, 200); v["valid"] != true {
			t.Errorf("%s: the file fails validation: %v", id, v["errors"])
		}
	}
	listed := map[string]bool{}
	for _, x := range d.list("/tasks") {
		listed[fmt.Sprint(x["id"])] = true
	}
	if !listed["complete"] || !listed["empty-lists"] {
		t.Errorf("GET /tasks lists %v", listed)
	}

	// An editor adds a comment and moves apiVersion from the top of the file to its end.
	file := filepath.Join(home, "tasks", "complete.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	const top = "apiVersion: harmflow/v1alpha1\n"
	if !bytes.HasPrefix(data, []byte(top)) {
		t.Fatalf("the file does not start with apiVersion:\n%s", data)
	}
	edited := "# Edited outside HarmFlow: this comment, and apiVersion moved to the end.\n" + string(data[len(top):]) + top
	if err := os.WriteFile(file, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if got = d.call("GET", "/tasks/complete", nil, 200); got["yaml"] == edited {
			break
		}
	}
	if got["yaml"] != edited {
		t.Fatalf("GET /tasks/complete did not converge on the edited file; its yaml:\n%v", got["yaml"])
	}
	if doc, file := jsonShape(t, got["document"]), yamlAsJSON(t, []byte(edited)); !reflect.DeepEqual(doc, file) {
		t.Errorf("the document is not the edited file:\ndocument %s\nfile     %s", mustJSON(doc), mustJSON(file))
	}
}

// completeTask is a form document that sets every section to a value other than its default, with a non-empty list
// wherever the field is a list, and agents, skills, models, and efforts that resolve on the stand-in harnesses.
func completeTask(id, repo string) map[string]any {
	return map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": id, "name": "Complete " + id, "description": "Every section of the form, set."},
		"spec": map[string]any{
			"enabled": false,
			"type":    "github-issue-implementation",
			"githubIssueImplementation": map[string]any{
				"repositories": []any{map[string]any{"name": "harmflow/example", "localPath": repo, "baseBranch": "develop"}},
				"queue": map[string]any{"ordering": "newest_first", "maxDiscoveredPerCycle": 5, "maxItemsPerCycle": 2,
					"resumeStrategy": "alternate", "maxResumeAttempts": 1, "maxFailedRetries": 0},
				"delivery": map[string]any{"testRepairAttempts": 0},
			},
			"prompt":      "Implement the oldest issue labelled ready-for-agent.",
			"agents":      []any{"harmflow-github-issue-implementer"},
			"skills":      []any{"harmflow-github-issue-queue", "harmflow-github-draft-pr"},
			"workspace":   map[string]any{"path": "~/harmflow-work"},
			"schedule":    map[string]any{"cron": "30 6 * * 1-5", "timezone": "America/Sao_Paulo", "missedRun": map[string]any{"policy": "skip"}},
			"concurrency": map[string]any{"overlappingRuns": "skip"},
			"harness": map[string]any{
				"preferred": map[string]any{"provider": "claude-code", "model": "sonnet", "effort": "high"},
				"fallback": map[string]any{"on": []any{"harness_unavailable"},
					"candidates":  []any{map[string]any{"provider": "codex", "model": "gpt-5.6-terra", "effort": "high"}},
					"maxSwitches": 2},
			},
			"permissions": map[string]any{"profile": "custom", "restrictToWorkspace": false, "network": "deny",
				"custom": map[string]any{"allowedTools": []any{"Read", "Grep"}, "deniedTools": []any{"WebFetch"},
					"writablePaths": []any{repo}}},
			"execution": map[string]any{"timeoutMinutes": 45, "cancelGraceSeconds": 10,
				"rateLimit": map[string]any{"maxRetries": 2, "initialWaitSeconds": 30, "maxWaitSeconds": 600}},
			"notifications": map[string]any{"native": map[string]any{"enabled": false,
				"events": []any{"execution.failed", "test.failed"}}},
		}}
}

// FS-001 AC-013 (FR-011, BR-001): one file that breaks a schema rule is refused the same way by the form, the YAML
// editor's create and save, and import, and nothing is written; the file with the rule fixed is accepted by each.
func TestOneSchemaForEveryEntryPoint(t *testing.T) {
	home := t.TempDir()
	d := startDaemon(t, home)
	rev := d.call("POST", "/tasks", map[string]any{"yaml": task("shared", "p", "")}, 201)["revision"].(string)
	invalid := task("shared", "Write notes.", "  execution:\n    timeoutMinutes: 0\n")
	line := 1 + strings.Count(invalid[:strings.Index(invalid, "timeoutMinutes: 0")], "\n")
	before := taskFiles(t, home)
	var form map[string]any
	if err := yaml.Unmarshal([]byte(invalid), &form); err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	for _, tc := range []struct {
		name, method, path string
		body               map[string]any
	}{
		{"form", "POST", "/tasks", map[string]any{"document": form}},
		{"editor create", "POST", "/tasks", map[string]any{"yaml": invalid}},
		{"editor save", "PUT", "/tasks/shared", map[string]any{"yaml": invalid, "baseRevision": rev}},
		{"import", "POST", "/tasks/import", map[string]any{"yaml": invalid}},
	} {
		e, _ := d.call(tc.method, tc.path, tc.body, 422)["error"].(map[string]any)
		validation, _ := e["validation"].(map[string]any)
		errs, _ := validation["errors"].([]any)
		if e["code"] != "validation_failed" || len(errs) == 0 {
			t.Errorf("%s: error %v", tc.name, e)
			continue
		}
		first := errs[0].(map[string]any)
		if first["path"] != "/spec/execution/timeoutMinutes" || first["messageKey"] != "schema_minimum" || first["message"] == "" {
			t.Errorf("%s: first error %v", tc.name, first)
		}
		if want == nil {
			want = first
		} else if first["path"] != want["path"] || first["messageKey"] != want["messageKey"] || first["message"] != want["message"] {
			t.Errorf("%s disagrees with the form: %v / %v", tc.name, first, want)
		}
		if _, fromYAML := tc.body["yaml"]; fromYAML && first["line"] != float64(line) {
			t.Errorf("%s: line %v, want %d", tc.name, first["line"], line)
		}
	}
	if after := taskFiles(t, home); !reflect.DeepEqual(after, before) {
		t.Errorf("a refused task changed the task files: %v, was %v", after, before)
	}

	valid := strings.Replace(invalid, "timeoutMinutes: 0", "timeoutMinutes: 45", 1)
	withID := func(id string) string { return strings.Replace(valid, "id: shared", "id: "+id, 1) }
	var fixed map[string]any
	if err := yaml.Unmarshal([]byte(withID("form")), &fixed); err != nil {
		t.Fatal(err)
	}
	d.call("POST", "/tasks", map[string]any{"document": fixed}, 201)
	d.call("POST", "/tasks", map[string]any{"yaml": withID("editor")}, 201)
	d.call("PUT", "/tasks/shared", map[string]any{"yaml": valid, "baseRevision": rev}, 200)
	d.call("POST", "/tasks/import", map[string]any{"yaml": withID("imported")}, 201)
	for _, id := range []string{"form", "editor", "shared", "imported"} {
		eff, _ := d.call("GET", "/tasks/"+id, nil, 200)["effective"].(map[string]any)
		spec, _ := eff["spec"].(map[string]any)
		if x, _ := spec["execution"].(map[string]any); x["timeoutMinutes"] != float64(45) {
			t.Errorf("%s: the saved task's execution is %v", id, x)
		}
	}
}

// yamlAsJSON is a task file parsed as YAML, in the shape JSON gives it, so it compares with a document.
func yamlAsJSON(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		t.Fatalf("parsing %s: %v", data, err)
	}
	return jsonShape(t, v)
}

// jsonShape is the value as it reads after a round trip through JSON: numbers are float64, objects map[string]any.
func jsonShape(t *testing.T, v any) any {
	t.Helper()
	var out any
	if err := json.Unmarshal([]byte(mustJSON(v)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// taskFiles maps each file of the tasks directory to its content.
func taskFiles(t *testing.T, home string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(home, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(home, "tasks", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(data)
	}
	return out
}

// FS-001 FR-007, AC-009: a task file broken while the daemon is down is named, beside its line, once it restarts.
func TestRestartNamesTheBrokenFile(t *testing.T) {
	home := t.TempDir()
	startDaemon(t, home).stop()
	broken := "apiVersion: harmflow/v1alpha1\nkind: Task\nmetadata:\n  id: [unclosed\n"
	if err := os.WriteFile(filepath.Join(home, "tasks", "broken.yaml"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	d := startDaemon(t, home)
	var listed map[string]any
	for _, task := range d.list("/tasks") {
		if task["id"] == "broken" {
			listed = task
		}
	}
	warnings, _ := listed["warnings"].([]any)
	if listed["invalid"] != true || len(warnings) != 1 {
		t.Fatalf("listed %v", listed)
	}
	w := warnings[0].(map[string]any)
	params, _ := w["params"].(map[string]any)
	message, _ := w["message"].(string)
	if w["messageKey"] != "task_file_error" || params["file"] != "broken.yaml" || params["detailKey"] != "yaml_syntax" ||
		!strings.HasPrefix(message, "broken.yaml: ") || !strings.Contains(message, "line ") {
		t.Errorf("the warning does not name the file and its line: %v", w)
	}
	view := d.call("GET", "/tasks/broken", nil, 200)
	errs, _ := view["validation"].(map[string]any)["errors"].([]any)
	if len(errs) != 1 {
		t.Fatalf("validation %v", view["validation"])
	}
	first := errs[0].(map[string]any)
	if p, _ := first["params"].(map[string]any); p["file"] != "broken.yaml" || first["line"] == nil {
		t.Errorf("the validation error does not name the file and its line: %v", first)
	}
}

type sseEvent struct {
	ID   int64
	Type string
	Data map[string]any
}

func (d *daemon) stream(t *testing.T, lastID int64) (<-chan sseEvent, func()) {
	req, _ := http.NewRequest("GET", d.base+"/events/stream", nil)
	req.Header.Set("Authorization", "Bearer "+d.token)
	if lastID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(lastID, 10))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v", err)
	}
	ch := make(chan sseEvent, 1000)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<22)
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				cur.ID, _ = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			case strings.HasPrefix(line, "event: "):
				cur.Type = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &cur.Data)
			case line == "" && cur.Type != "":
				ch <- cur
				cur = sseEvent{}
			}
		}
	}()
	return ch, func() { resp.Body.Close() }
}

// FS-008 AC: live events over SSE, the same events on replay, and a reconnection that resumes after the last id.
func TestRunStreamAndReplay(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("streamed", "SCENARIO=report", "")}, 201)
	events, closeStream := d.stream(t, 0)
	run := d.call("POST", "/tasks/streamed/run", nil, 202)
	id := run["executionId"].(string)
	var seen []sseEvent
	timeout := time.After(20 * time.Second)
	for done := false; !done; {
		select {
		case e := <-events:
			if e.Data["executionId"] == id {
				seen = append(seen, e)
				done = e.Type == "execution.completed"
			}
		case <-timeout:
			t.Fatalf("no execution.completed; saw %d events", len(seen))
		}
	}
	closeStream()
	types := map[string]bool{}
	for _, e := range seen {
		types[e.Type] = true
	}
	for _, want := range []string{"execution.created", "execution.started", "harness.started", "tool.started", "tool.completed",
		"harness.message", "test.completed", "pull_request.created", "execution.completed"} {
		if !types[want] {
			t.Errorf("SSE lacked %s", want)
		}
	}
	x := d.waitStatus(id, "completed")
	if x["pullRequestUrl"] != "https://github.com/o/r/pull/4" || x["testsStatus"] != "passed" {
		t.Errorf("execution %v", x)
	}
	// harmflow-app#39: the record and the API carry the attempt's usage, at the cost the CLI reported.
	usage, _ := x["attempts"].([]any)[0].(map[string]any)["usage"].(map[string]any)
	if usage["status"] != "reported" || usage["costSource"] != "reported" || usage["costUsd"] != 0.0364015 ||
		tokensOf(usage)["output"] != float64(866) || usage["models"].([]any)[0].(map[string]any)["price"] == nil {
		t.Errorf("usage %v", usage)
	}
	replay := d.call("GET", "/executions/"+id+"/events", nil, 200)
	stored := replay["events"].([]any)
	if len(stored) < len(seen) {
		t.Errorf("replay has %d events, the stream delivered %d", len(stored), len(seen))
	}
	// Reconnecting after the third event delivers only the later ones.
	again, closeAgain := d.stream(t, seen[2].ID)
	defer closeAgain()
	first := <-again
	if first.ID <= seen[2].ID {
		t.Errorf("reconnect replayed %d, already received %d", first.ID, seen[2].ID)
	}
	// The JSONL log is one file per execution.
	if _, err := os.Stat(filepath.Join(d.home, "logs", id+".jsonl")); err != nil {
		t.Error(err)
	}
}

// FS-008 AC-008: the raw output of a completed execution is what the CLI printed, stdout and stderr, with the token
// it contained redacted.
func TestRawOutputRedacted(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("leaky", "SCENARIO=secret", "")}, 201)
	id := d.call("POST", "/tasks/leaky/run", nil, 202)["executionId"].(string)
	d.waitStatus(id, "completed")
	out := d.call("GET", "/executions/"+id+"/output", nil, 200)
	attempts, _ := out["attempts"].([]any)
	if len(attempts) != 1 || out["logExpired"] != false {
		t.Fatalf("output %v", out)
	}
	a := attempts[0].(map[string]any)
	text, _ := a["text"].(string)
	if a["attempt"] != float64(1) || a["harness"] != "claude-code" || a["offset"] != float64(0) {
		t.Errorf("attempt %v harness %v offset %v", a["attempt"], a["harness"], a["offset"])
	}
	// The result line becomes events, never harness.output: finding it proves this is the capture.
	for _, want := range []string{`"type":"result"`, "warning: signing in with [REDACTED]", "Pushed with [REDACTED]"} {
		if !strings.Contains(text, want) {
			t.Errorf("the capture lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "ghp_") {
		t.Errorf("the token survived:\n%s", text)
	}
	// FS-009 FR-005: the record the API serves keeps the command and the final text, redacted, in its detail and list.
	detail, _ := json.Marshal(d.call("GET", "/executions/"+id, nil, 200))
	status, list := d.request("GET", "/executions?taskId=leaky", nil, nil)
	if status != 200 {
		t.Fatalf("list: status %d: %s", status, list)
	}
	for name, body := range map[string]string{"detail": string(detail), "list": string(list)} {
		if strings.Contains(body, "ghp_") || !strings.Contains(body, "Pushed with [REDACTED]") {
			t.Errorf("the execution %s keeps the token or lost the summary:\n%s", name, body)
		}
	}
	if !strings.Contains(string(detail), "git push https://x-access-token:[REDACTED]@github.com/o/r") {
		t.Errorf("the detail lacks the redacted command:\n%s", detail)
	}
	if status, _ := d.request("GET", "/executions/no-such-execution/output", nil, nil); status != 404 {
		t.Errorf("an unknown execution answered %d", status)
	}
}

// FS-005 §5.1: Claude Code exhausts its quota; Codex finishes the same execution.
func TestFailoverAcrossHarnesses(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	_ = os.MkdirAll(filepath.Join(d.home, "codex"), 0o700)
	_ = os.WriteFile(filepath.Join(d.home, "codex", "config.toml"), []byte("model = \"gpt-from-config\"\n"), 0o600)
	d.call("POST", "/tasks", map[string]any{"yaml": task("failover", "SCENARIO=quota",
		"    fallback:\n      candidates:\n        - provider: codex\n")}, 201)
	run := d.call("POST", "/tasks/failover/run", nil, 202)
	x := d.waitStatus(run["executionId"].(string), "completed", "failed")
	attempts := x["attempts"].([]any)
	if x["status"] != "completed" || len(attempts) != 2 {
		t.Fatalf("status %v attempts %v error %v", x["status"], attempts, x["errorMessage"])
	}
	a1, a2 := attempts[0].(map[string]any), attempts[1].(map[string]any)
	if a1["harness"] != "claude-code" || a1["outcome"] != "quota_exhausted" || a2["harness"] != "codex" || a2["outcome"] != "succeeded" {
		t.Errorf("attempts %v %v", a1, a2)
	}
	// FS-005 AC-008 (D-105): Claude Code's init line names its model; Codex's stream names none, and neither names its
	// effort, so those are absent, and the configured default is kept apart, never in their place.
	if a1["model"] != "claude-haiku-4-5-20251001" || a2["model"] != nil || a1["effort"] != nil || a2["effort"] != nil ||
		a1["configuredModel"] != "default" || a2["configuredModel"] != "default" ||
		a2["configuredEffort"] != "default" {
		t.Errorf("models and efforts: %v %v", a1, a2)
	}
	if x["model"] != nil {
		t.Errorf("the execution's model %v is not its current attempt's", x["model"])
	}
	events := d.call("GET", "/executions/"+run["executionId"].(string)+"/events", nil, 200)["events"].([]any)
	var order []string
	for _, e := range events {
		switch typ := e.(map[string]any)["type"].(string); typ {
		case "harness.quota_exhausted", "harness.switched", "harness.started":
			order = append(order, typ)
		}
	}
	if strings.Join(order, ",") != "harness.started,harness.quota_exhausted,harness.switched,harness.started" {
		t.Errorf("visible switch: %v", order)
	}
	// harmflow-app#39: each attempt keeps its own usage. Claude Code stopped before its result line, so its count is
	// partial; Codex ran the model its configuration sets, which the price table does not know.
	u1, _ := a1["usage"].(map[string]any)
	u2, _ := a2["usage"].(map[string]any)
	if u1["status"] != "partial" || u1["costUsd"] != nil || tokensOf(u1)["cacheWriteLong"] != float64(11511) {
		t.Errorf("attempt 1 usage: %v", u1)
	}
	if u2["status"] != "reported" || u2["modelSource"] != "config" || u2["costSource"] != "unpriced" ||
		tokensOf(u2)["input"] != float64(160035-135680) || tokensOf(u2)["cacheRead"] != float64(135680) {
		t.Errorf("attempt 2 usage: %v", u2)
	}
	if m := u2["models"].([]any)[0].(map[string]any); m["model"] != "gpt-from-config" {
		t.Errorf("attempt 2 model: %v", m)
	}
	// FS-008 FR-010: each attempt keeps the prompt it sent and its own session context, under the contract's names.
	c1, _ := a1["sessionContext"].(string)
	c2, _ := a2["sessionContext"].(string)
	if a1["prompt"] != "SCENARIO=quota" || a2["prompt"] != "SCENARIO=quota" {
		t.Errorf("prompts %q and %q", a1["prompt"], a2["prompt"])
	}
	if !strings.HasPrefix(c1, "This session is run by HarmFlow") || strings.Contains(c1, "continuing the same execution") ||
		!strings.Contains(c2, "continuing the same execution") {
		t.Errorf("session contexts:\n%s\n---\n%s", c1, c2)
	}
}

// FS-008 AC-012: a failover whose attempts both reported their usage. Claude Code's cost is the one its CLI printed;
// Codex's, at the model its configuration sets, is estimated from the price table and stamped on the attempt.
func TestFailoverCostsReportedAndEstimated(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	_ = os.MkdirAll(filepath.Join(d.home, "codex"), 0o700)
	_ = os.WriteFile(filepath.Join(d.home, "codex", "config.toml"), []byte("model = \"gpt-5.6-terra\"\n"), 0o600)
	d.call("POST", "/tasks", map[string]any{"yaml": task("costs", "SCENARIO=quotausage",
		"    fallback:\n      candidates:\n        - provider: codex\n")}, 201)
	run := d.call("POST", "/tasks/costs/run", nil, 202)
	x := d.waitStatus(run["executionId"].(string), "completed", "failed")
	attempts := x["attempts"].([]any)
	if x["status"] != "completed" || len(attempts) != 2 {
		t.Fatalf("status %v attempts %v error %v", x["status"], attempts, x["errorMessage"])
	}
	a1, a2 := attempts[0].(map[string]any), attempts[1].(map[string]any)
	if a1["harness"] != "claude-code" || a1["outcome"] != "quota_exhausted" || a2["harness"] != "codex" {
		t.Fatalf("attempts %v %v", a1, a2)
	}
	u1, _ := a1["usage"].(map[string]any)
	if u1["status"] != "reported" || u1["costSource"] != "reported" || u1["costUsd"] != 0.0364015 ||
		tokensOf(u1)["cacheWriteLong"] != float64(12752) || tokensOf(u1)["output"] != float64(866) {
		t.Errorf("attempt 1 usage: %v", u1)
	}
	u2, _ := a2["usage"].(map[string]any)
	m2, _ := u2["models"].([]any)[0].(map[string]any)
	cost, _ := u2["costUsd"].(float64)
	if u2["status"] != "reported" || u2["costSource"] != "estimated" || cost <= 0 || u2["pricesAsOf"] == "" ||
		u2["modelSource"] != "config" || m2["model"] != "gpt-5.6-terra" || m2["price"] == nil ||
		tokensOf(u2)["input"] != float64(160035-135680) {
		t.Errorf("attempt 2 usage: %v", u2)
	}
}

// tokensOf is the first model's token counts of an attempt's usage.
func tokensOf(usage map[string]any) map[string]any {
	models, _ := usage["models"].([]any)
	if len(models) == 0 {
		return nil
	}
	tokens, _ := models[0].(map[string]any)["tokens"].(map[string]any)
	return tokens
}

// FS-003 AC-007, AC-006: cancelling ends the harness and its children; another task keeps running.
func TestCancelEndsTheTree(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("slow-a", "SCENARIO=slow", "  execution:\n    cancelGraceSeconds: 1\n")}, 201)
	d.call("POST", "/tasks", map[string]any{"yaml": task("slow-b", "SCENARIO=slow", "")}, 201)
	a := d.call("POST", "/tasks/slow-a/run", nil, 202)["executionId"].(string)
	b := d.call("POST", "/tasks/slow-b/run", nil, 202)["executionId"].(string)
	x := d.waitStatus(a, "running")
	d.waitStatus(b, "running")
	pidFile := filepath.Join(x["executionDir"].(string), "child.pid")
	var child int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && child == 0; time.Sleep(100 * time.Millisecond) {
		data, _ := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	if child == 0 {
		t.Fatal("the fake harness did not start its child")
	}
	d.call("POST", "/executions/"+a+"/cancel", nil, 202)
	x = d.waitStatus(a, "cancelled")
	if x["cancelRequestedAt"] == nil {
		t.Error("the cancellation was not recorded")
	}
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(child, 0); err == nil {
		t.Errorf("the harness's child %d survived the cancellation", child)
	}
	if got := d.call("GET", "/executions/"+b, nil, 200); got["status"] != "running" {
		t.Errorf("the other task's execution is %v", got["status"])
	}
}

// FS-003 AC-009: an execution running when the daemon is killed reads interrupted after the restart, with its log.
func TestRestartInterrupts(t *testing.T) {
	home := t.TempDir()
	d := startDaemon(t, home)
	d.call("POST", "/tasks", map[string]any{"yaml": task("crash", "SCENARIO=slow", "")}, 201)
	id := d.call("POST", "/tasks/crash/run", nil, 202)["executionId"].(string)
	d.waitStatus(id, "running")
	time.Sleep(1500 * time.Millisecond) // let the record reach the disk
	d.kill()
	d2 := startDaemon(t, home)
	x := d2.call("GET", "/executions/"+id, nil, 200)
	if x["status"] != "interrupted" {
		t.Fatalf("status %v", x["status"])
	}
	events := d2.call("GET", "/executions/"+id+"/events", nil, 200)
	if events["logExpired"] == true || len(events["events"].([]any)) == 0 {
		t.Error("the attempt's log was lost")
	}
	// The task can run again right away.
	run := d2.call("POST", "/tasks/crash/run", nil, 202)
	if run["outcome"] != "started" {
		t.Errorf("rerun %v", run)
	}
	d2.call("POST", "/executions/"+run["executionId"].(string)+"/cancel", nil, 202)
}

// FS-003 FR-007, AC-009; FS-001 FR-015 (D-083): a firing waiting when the daemon is killed starts after the restart;
// when its task file was disabled or deleted while the daemon was down, it is cancelled instead and never starts.
func TestWaitingFiringAcrossKill(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, path string)
		want   string
		reason string
	}{
		{"kept", func(*testing.T, string) {}, "running", ""},
		{"disabled", func(t *testing.T, path string) {
			data, err := os.ReadFile(path)
			if err == nil {
				err = os.WriteFile(path, []byte(strings.Replace(string(data), "spec:\n", "spec:\n  enabled: false\n", 1)), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
		}, "cancelled", "task_was_disabled"},
		{"deleted", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, "cancelled", "task_file_removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			d := startDaemon(t, home)
			d.call("POST", "/tasks", map[string]any{"yaml": task("waits", "SCENARIO=slow", "")}, 201)
			first := d.call("POST", "/tasks/waits/run", nil, 202)["executionId"].(string)
			x := d.waitStatus(first, "running")
			second := d.call("POST", "/tasks/waits/run", nil, 202)
			if second["outcome"] != "queued" {
				t.Fatalf("second run: %v", second)
			}
			waiting := second["executionId"].(string)
			killHarnessGroup(t, x["executionDir"].(string))
			waitRecord(t, home, first, "running")
			d.kill()
			tc.change(t, filepath.Join(home, "tasks", "waits.yaml"))
			d2 := startDaemon(t, home)
			if got := d2.call("GET", "/executions/"+first, nil, 200); got["status"] != "interrupted" {
				t.Errorf("the running execution is %v, want interrupted", got["status"])
			}
			got := d2.waitStatus(waiting, tc.want)
			if tc.want == "running" {
				killHarnessGroup(t, got["executionDir"].(string))
				d2.call("POST", "/executions/"+waiting+"/cancel", nil, 202)
				return
			}
			if got["errorKey"] != tc.reason || got["startedAt"] != nil || len(got["attempts"].([]any)) != 0 {
				t.Errorf("the dropped firing: reason %v, started at %v, attempts %v", got["errorKey"], got["startedAt"],
					got["attempts"])
			}
			if waits := d2.call("GET", "/queue", nil, 200)["waiting"].([]any); len(waits) != 0 {
				t.Errorf("GET /queue still lists %d waiting firings", len(waits))
			}
			if tc.name == "disabled" {
				if view := d2.call("GET", "/tasks/waits", nil, 200); view["enabled"] != false {
					t.Errorf("the task reads enabled %v", view["enabled"])
				}
			}
		})
	}
}

// killHarnessGroup ends the stand-in harness's process group when the test ends. A daemon killed with SIGKILL leaves
// its harness running, since it does not yet find or stop it after the restart (harmflow/harmflow-app#12).
func killHarnessGroup(t *testing.T, executionDir string) {
	t.Helper()
	var child int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && child == 0; time.Sleep(100 * time.Millisecond) {
		data, _ := os.ReadFile(filepath.Join(executionDir, "child.pid"))
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	if child == 0 {
		t.Fatal("the fake harness did not start its child")
	}
	pgid, err := syscall.Getpgid(child)
	if err != nil || pgid <= 1 || pgid == syscall.Getpgrp() {
		t.Fatalf("the fake harness's process group: %d, %v", pgid, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
}

// waitRecord waits until the execution's record on disk has the status: the daemon saves a running record at most
// every second, and a kill before that leaves it queued.
func waitRecord(t *testing.T, home, id, status string) {
	t.Helper()
	var rec struct {
		Status string `json:"status"`
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		data, err := os.ReadFile(filepath.Join(home, "state", "executions", id+".json"))
		if err == nil && json.Unmarshal(data, &rec) == nil && rec.Status == status {
			return
		}
	}
	t.Fatalf("the record of %s on disk is %q, want %q", id, rec.Status, status)
}
