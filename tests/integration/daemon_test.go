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
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

var binary string

// builtAt is the build time the binary is stamped with, as make stamps it.
const builtAt = "2026-10-22T20:30:05Z"

// starterEnv names the daemon binary when the test binary runs as the short-lived parent of
// TestDaemonOutlivesItsStarter rather than as the tests; starterEnv+"_PID" names the file it writes the daemon's PID to.
const starterEnv = "HARMFLOW_TEST_STARTER"

func TestMain(m *testing.M) {
	if bin := os.Getenv(starterEnv); bin != "" {
		os.Exit(runStarter(bin, os.Args[1:]))
	}
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
	d.base, d.token = awaitRuntime(home, 20*time.Second)
	if d.base == "" {
		_ = cmd.Process.Kill()
		t.Fatalf("the daemon did not start:\n%s", out.String())
	}
	t.Cleanup(func() { d.stop() })
	d.useFakeHarnesses()
	return d
}

// awaitRuntime waits until the daemon at home has written its address and token, and returns its API's base URL and
// the token: empty when it has not within the timeout.
func awaitRuntime(home string, timeout time.Duration) (base, token string) {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		data, err := os.ReadFile(filepath.Join(home, "run", "daemon.json"))
		var rt struct {
			Address string `json:"address"`
		}
		if err == nil && json.Unmarshal(data, &rt) == nil && rt.Address != "" {
			tok, _ := os.ReadFile(filepath.Join(home, "run", "token"))
			return "http://" + rt.Address + "/api/v1", strings.TrimSpace(string(tok))
		}
	}
	return "", ""
}

// useFakeHarnesses points the daemon at the stand-in CLIs of testdata/.
func (d *daemon) useFakeHarnesses() {
	d.t.Helper()
	fakes := filepath.Join(repoRoot(), "tests", "integration", "testdata")
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": filepath.Join(fakes, "fake-claude")}, 200)
	d.call("PUT", "/harnesses/codex", map[string]any{"path": filepath.Join(fakes, "fake-codex")}, 200)
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

// FS-002 FR-009, AC-011: a save accepts a 5-field cron or a simple interval, and rejects a cron with a seconds field
// without writing a task file.
func TestScheduleFormatsOnSave(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	before := time.Now().Truncate(time.Minute)
	every := d.call("POST", "/tasks", map[string]any{"yaml": task("every-30m", "Check the inbox.",
		"  schedule:\n    interval: 30m\n    timezone: UTC\n")}, 201)
	after := time.Now().Truncate(time.Minute)
	// An interval counts from the minute the task was saved.
	next, err := time.Parse(time.RFC3339, fmt.Sprint(every["nextFireAt"]))
	if err != nil || next.Before(before.Add(30*time.Minute)) || next.After(after.Add(30*time.Minute)) {
		t.Errorf("interval: next firing %v (%v), want 30 minutes after the save", every["nextFireAt"], err)
	}

	weekdays := d.call("POST", "/tasks", map[string]any{"yaml": task("weekdays", "Write the standup notes.",
		"  schedule:\n    cron: \"0 9 * * 1-5\"\n    timezone: UTC\n")}, 201)
	next, err = time.Parse(time.RFC3339, fmt.Sprint(weekdays["nextFireAt"]))
	if day := next.UTC().Weekday(); err != nil || next.UTC().Hour() != 9 || next.Minute() != 0 ||
		day == time.Saturday || day == time.Sunday {
		t.Errorf("5-field cron: next firing %v (%v), want 09:00 UTC on a weekday", weekdays["nextFireAt"], err)
	}

	status, body := d.request("POST", "/tasks", map[string]any{"yaml": task("with-seconds", "Write the standup notes.",
		"  schedule:\n    cron: \"0 0 9 * * 1-5\"\n    timezone: UTC\n")}, nil)
	if status != 422 {
		t.Fatalf("6-field cron: status %d, want 422: %s", status, body)
	}
	var rejected struct {
		Error struct {
			Validation struct {
				Errors []struct {
					Path string `json:"path"`
				} `json:"errors"`
			} `json:"validation"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &rejected)
	var paths []string
	for _, e := range rejected.Error.Validation.Errors {
		paths = append(paths, e.Path)
	}
	if !slices.Contains(paths, "/spec/schedule/cron") {
		t.Errorf("6-field cron: errors at %v, want one at /spec/schedule/cron: %s", paths, body)
	}
	if _, err := os.Stat(filepath.Join(d.home, "tasks", "with-seconds.yaml")); !os.IsNotExist(err) {
		t.Errorf("a rejected save wrote the task file: %v", err)
	}
	if s, _ := d.request("GET", "/tasks/with-seconds", nil, nil); s != 404 {
		t.Errorf("a rejected save left a task: GET status %d, want 404", s)
	}
}

type sseEvent struct {
	ID   int64
	Type string
	Data map[string]any
	Raw  []byte // the data line as the daemon wrote it
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
				cur.Raw = []byte(strings.TrimPrefix(line, "data: "))
				_ = json.Unmarshal(cur.Raw, &cur.Data)
			case line == "" && cur.Type != "":
				ch <- cur
				cur = sseEvent{}
			}
		}
	}()
	return ch, func() { resp.Body.Close() }
}

// eventSchemaURL is the $id of contracts/schemas/event.schema.json.
const eventSchemaURL = "https://harmflow.com/schemas/v1alpha1/event.schema.json"

// eventSchema is contracts/schemas/event.schema.json, compiled once. Like the daemon's own validator, it checks the
// timestamp as a date-time.
var eventSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	f, err := os.Open(filepath.Join(repoRoot(), "contracts", "schemas", "event.schema.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(eventSchemaURL, doc); err != nil {
		return nil, err
	}
	return c.Compile(eventSchemaURL)
})

// loggedEvent is an event as one of its sources holds it: an SSE frame, the replay the API serves, or a JSONL line.
type loggedEvent struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// checkEvent checks one event's JSON against event.schema.json (FS-008 FR-007) and returns its id and type.
func checkEvent(t *testing.T, source string, raw []byte) loggedEvent {
	t.Helper()
	s, err := eventSchema()
	if err != nil {
		t.Fatalf("compiling event.schema.json: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err == nil {
		err = s.Validate(doc)
	}
	if err != nil {
		t.Errorf("%s: an event does not fit event.schema.json: %v\n%s", source, err, raw)
	}
	var e loggedEvent
	_ = json.Unmarshal(raw, &e)
	return e
}

// checkFrame checks an SSE frame's data against the schema, and its id and event lines against its data.
func checkFrame(t *testing.T, e sseEvent) loggedEvent {
	t.Helper()
	got := checkEvent(t, "SSE", e.Raw)
	if got.ID != e.ID || got.Type != e.Type {
		t.Errorf("the frame's id %d and event %s differ from its data's %d and %s", e.ID, e.Type, got.ID, got.Type)
	}
	return got
}

// storedLog returns an execution's events from GET /executions/{executionId}/events and from the lines of
// logs/<execution>.jsonl, once both hold its execution.completed, each checked against event.schema.json.
func (d *daemon) storedLog(t *testing.T, executionID string) (api, jsonl []loggedEvent) {
	t.Helper()
	var replay struct {
		Events []json.RawMessage `json:"events"`
	}
	var lines [][]byte
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		status, body := d.request("GET", "/executions/"+executionID+"/events", nil, nil)
		if status != 200 {
			t.Fatalf("GET /executions/%s/events: %d %s", executionID, status, body)
		}
		if err := json.Unmarshal(body, &replay); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(d.home, "logs", executionID+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lines = bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
		completed := []byte(`"type":"execution.completed"`)
		if bytes.Contains(body, completed) && bytes.Contains(data, completed) || time.Now().After(deadline) {
			break
		}
	}
	for _, raw := range replay.Events {
		api = append(api, checkEvent(t, "GET /executions/{executionId}/events", raw))
	}
	for _, line := range lines {
		jsonl = append(jsonl, checkEvent(t, executionID+".jsonl", line))
	}
	return api, jsonl
}

// throughCompletion is a source's events from its first, which must be execution.created, to its execution.completed.
// A notice of the completion may follow it, so the sources are compared up to there.
func throughCompletion(t *testing.T, source string, events []loggedEvent) []loggedEvent {
	t.Helper()
	end := slices.IndexFunc(events, func(e loggedEvent) bool { return e.Type == "execution.completed" })
	if len(events) == 0 || events[0].Type != "execution.created" || end < 0 {
		t.Errorf("%s does not run from execution.created to execution.completed: %v", source, events)
		return events
	}
	return events[:end+1]
}

// sameEvents reports a source whose events differ from what the client received, by id, type, or order.
func sameEvents(t *testing.T, source string, got, received []loggedEvent) {
	t.Helper()
	if !slices.Equal(got, received) {
		t.Errorf("%s holds other events than the client received:\n%v\nreceived:\n%v", source, got, received)
	}
}

// FS-008 FR-002, AC-001: live events over SSE, and the same events, in the same order, in the replay the API serves and
// in the JSONL log; a reconnection resumes after the last id.
func TestRunStreamAndReplay(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("streamed", "SCENARIO=report", "")}, 201)
	events, closeStream := d.stream(t, 0)
	run := d.call("POST", "/tasks/streamed/run", nil, 202)
	id := run["executionId"].(string)
	var seen []sseEvent
	var streamed []loggedEvent
	timeout := time.After(20 * time.Second)
	for done := false; !done; {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatalf("the stream closed; saw %d events", len(seen))
			}
			got := checkFrame(t, e)
			if e.Data["executionId"] == id {
				seen = append(seen, e)
				streamed = append(streamed, got)
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
	// The JSONL log is one file per execution; it and the replay agree with the stream event for event.
	api, jsonl := d.storedLog(t, id)
	received := throughCompletion(t, "the stream", streamed)
	sameEvents(t, "GET /executions/{executionId}/events", throughCompletion(t, "the replay", api), received)
	sameEvents(t, id+".jsonl", throughCompletion(t, id+".jsonl", jsonl), received)
	// Reconnecting after the third event delivers only the later ones.
	again, closeAgain := d.stream(t, seen[2].ID)
	defer closeAgain()
	first := <-again
	checkFrame(t, first)
	if first.ID <= seen[2].ID {
		t.Errorf("reconnect replayed %d, already received %d", first.ID, seen[2].ID)
	}
}

// FS-008 AC-007: a client that disconnects while an execution runs, and reconnects with Last-Event-ID set to the last
// event it received, receives every event emitted while it was away, in order, once each and with no replay_gap, and
// then live ones. What it received in the end is the stored log.
func TestReconnectionIsLossless(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	// With no notice, the stored log ends at execution.completed, so it can equal what the client received whole.
	d.call("POST", "/tasks", map[string]any{"yaml": task("ticking", "SCENARIO=ticks",
		"  notifications:\n    native:\n      events: []\n")}, 201)
	events, closeStream := d.stream(t, 0)
	id := d.call("POST", "/tasks/ticking/run", nil, 202)["executionId"].(string)
	var received []loggedEvent
	var lastID int64
	receive := func(e sseEvent) loggedEvent {
		if e.Type == "replay_gap" {
			t.Fatalf("replay_gap: %s", e.Raw)
		}
		got := checkFrame(t, e)
		lastID = max(lastID, e.ID)
		if e.Data["executionId"] == id {
			received = append(received, got)
		}
		return got
	}

	// 1. The client receives the execution's first events, up to its first message.
	timeout := time.After(20 * time.Second)
	for first := false; !first; {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("the stream closed")
			}
			got := receive(e)
			first = got.Type == "harness.message" && e.Data["executionId"] == id
		case <-timeout:
			t.Fatalf("no message of the execution; received %v", received)
		}
	}
	// 2. It disconnects, keeping what was already on its way, while the execution goes on emitting.
	closeStream()
	for e := range events {
		receive(e)
	}
	var missed []loggedEvent
	for deadline := time.Now().Add(20 * time.Second); len(missed) < 2; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the execution emitted %v after event %d, want at least two events", missed, lastID)
		}
		missed = d.eventsAfter(t, id, lastID)
	}
	if slices.ContainsFunc(missed, func(e loggedEvent) bool { return e.Type == "execution.completed" }) {
		t.Fatalf("the execution ended while the client was away, so no event can be live: %v", missed)
	}

	// 3. It reconnects. The daemon subscribed it before answering, so an event the log does not hold yet is live.
	again, closeAgain := d.stream(t, lastID)
	defer closeAgain()
	reconnectedAt := lastID
	if held := d.eventsAfter(t, id, lastID); len(held) > 0 {
		reconnectedAt = held[len(held)-1].ID
	}
	before := len(received)
	timeout = time.After(20 * time.Second)
	for done := false; !done; {
		select {
		case e, ok := <-again:
			if !ok {
				t.Fatal("the stream closed after the reconnection")
			}
			got := receive(e)
			done = got.Type == "execution.completed" && e.Data["executionId"] == id
		case <-timeout:
			t.Fatalf("no execution.completed after the reconnection; received %v", received[before:])
		}
	}

	// 4. Every event emitted while it was away arrived, in order and once each, then at least one live event.
	for i := 1; i < len(received); i++ {
		if received[i].ID <= received[i-1].ID {
			t.Errorf("event %d arrived after event %d", received[i].ID, received[i-1].ID)
		}
	}
	if again := received[before:]; len(again) < len(missed) || !slices.Equal(again[:len(missed)], missed) {
		t.Errorf("after the reconnection the client received %v, want the missed %v first", again, missed)
	}
	if received[len(received)-1].ID <= reconnectedAt {
		t.Errorf("no live event: the last one received, %d, was already in the log at %d", received[len(received)-1].ID,
			reconnectedAt)
	}
	api, jsonl := d.storedLog(t, id)
	sameEvents(t, "GET /executions/{executionId}/events", api, received)
	sameEvents(t, id+".jsonl", jsonl, received)
}

// eventsAfter is the execution's stored events after an id, by id and type.
func (d *daemon) eventsAfter(t *testing.T, executionID string, after int64) []loggedEvent {
	t.Helper()
	status, body := d.request("GET", fmt.Sprintf("/executions/%s/events?after=%d", executionID, after), nil, nil)
	var replay struct {
		Events []loggedEvent `json:"events"`
	}
	if err := json.Unmarshal(body, &replay); status != 200 || err != nil {
		t.Fatalf("GET /executions/%s/events: %d %v %s", executionID, status, err, body)
	}
	return replay.Events
}

// FS-008 FR-009, AC-009: a running execution's detail and its row in the list give its current step and when it
// started, and no field of either speaks of a percentage or of progress.
func TestRunningExecutionShowsStepAndStart(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("steady", "SCENARIO=slow", "  execution:\n    cancelGraceSeconds: 1\n")}, 201)
	id := d.call("POST", "/tasks/steady/run", nil, 202)["executionId"].(string)
	d.waitStatus(id, "running")
	// The execution reads running before its harness's first line names a step.
	views := map[string]map[string]any{}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		views["GET /executions/{executionId}"] = d.call("GET", "/executions/"+id, nil, 200)
		views["its row in GET /executions"] = nil
		for _, row := range d.list("/executions?taskId=steady") {
			if row["id"] == id {
				views["its row in GET /executions"] = row
			}
		}
		if stepOf(views["GET /executions/{executionId}"]) != "" && stepOf(views["its row in GET /executions"]) != "" {
			break
		}
	}
	for name, view := range views {
		if view["status"] != "running" || stepOf(view) == "" {
			t.Errorf("%s: status %v, current step %q", name, view["status"], stepOf(view))
		}
		if started, _ := view["startedAt"].(string); started == "" {
			t.Errorf("%s: no startedAt", name)
		} else if _, err := time.Parse(time.RFC3339Nano, started); err != nil {
			t.Errorf("%s: startedAt %q: %v", name, started, err)
		}
		if keys := progressKeys(view, ""); len(keys) > 0 {
			t.Errorf("%s has %v", name, keys)
		}
	}
	d.call("POST", "/executions/"+id+"/cancel", nil, 202)
	d.waitStatus(id, "cancelled")
}

// stepOf is the current step of an execution's view, empty when it has none.
func stepOf(view map[string]any) string {
	s, _ := view["currentStep"].(string)
	return s
}

// progressKeys lists the keys of a decoded JSON value, at any depth, whose name contains "percent" or "progress".
func progressKeys(v any, path string) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if name := strings.ToLower(k); strings.Contains(name, "percent") || strings.Contains(name, "progress") {
				out = append(out, path+"/"+k)
			}
			out = append(out, progressKeys(x, path+"/"+k)...)
		}
	case []any:
		for i, x := range t {
			out = append(out, progressKeys(x, path+"/"+strconv.Itoa(i))...)
		}
	}
	return out
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

// FS-008 FR-011, AC-011 (D-073): the daemon applies log retention on its own when it starts, and deletes a log older
// than the default 30 days. FR-010, AC-010: the execution is still served with its pull request and its attempt, and
// its events read as expired.
func TestRetentionExpiresTheLogAtStart(t *testing.T) {
	home := t.TempDir()
	d := startDaemon(t, home)
	d.call("POST", "/tasks", map[string]any{"yaml": task("aged", "SCENARIO=report", "")}, 201)
	id := d.call("POST", "/tasks/aged/run", nil, 202)["executionId"].(string)
	d.waitStatus(id, "completed")
	// A stopped daemon writes nothing more to the log, so it keeps the age set here.
	d.stop()
	logFile := filepath.Join(home, "logs", id+".jsonl")
	files := []string{logFile, filepath.Join(home, "logs", id, "attempt-1.raw"), filepath.Join(home, "runs", id)}
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("before retention: %v", err)
		}
	}
	aged := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(logFile, aged, aged); err != nil {
		t.Fatal(err)
	}
	d2 := startDaemon(t, home)
	// Retention runs in a goroutine at start, so the expiry shows up a little later.
	var x map[string]any
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if x = d2.call("GET", "/executions/"+id, nil, 200); x["logExpired"] == true {
			break
		}
	}
	if x["logExpired"] != true {
		t.Fatalf("the daemon did not expire the 31-day-old log: logExpired %v", x["logExpired"])
	}
	for _, f := range files {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s outlived retention: %v", f, err)
		}
	}
	attempts, _ := x["attempts"].([]any)
	if x["status"] != "completed" || x["workItem"] != "o/r#3" || x["pullRequestUrl"] != "https://github.com/o/r/pull/4" ||
		len(attempts) != 1 {
		t.Fatalf("the record lost what FR-010 keeps: %v", x)
	}
	if a, _ := attempts[0].(map[string]any); a["harness"] != "claude-code" || a["outcome"] != "succeeded" {
		t.Errorf("the record lost its attempt: %v", a)
	}
	events := d2.call("GET", "/executions/"+id+"/events", nil, 200)
	if list, ok := events["events"].([]any); !ok || len(list) != 0 || events["logExpired"] != true {
		t.Errorf("the expired execution's events: %v", events)
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
	// FS-008 FR-007: the replay and the JSONL log hold the same events, each fitting event.schema.json.
	id := run["executionId"].(string)
	events, jsonl := d.storedLog(t, id)
	sameEvents(t, id+".jsonl", throughCompletion(t, id+".jsonl", jsonl), throughCompletion(t, "the replay", events))
	var order []string
	for _, e := range events {
		switch e.Type {
		case "harness.quota_exhausted", "harness.switched", "harness.started":
			order = append(order, e.Type)
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
	// FS-008 FR-007: the replay and the JSONL log hold the same events, each fitting event.schema.json.
	id := run["executionId"].(string)
	events, jsonl := d.storedLog(t, id)
	sameEvents(t, id+".jsonl", throughCompletion(t, id+".jsonl", jsonl), throughCompletion(t, "the replay", events))
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
// FS-003 FR-005: the execution ends cancelled, and its record, log, raw output, workspace, and execution directory are
// kept.
func TestCancelEndsTheTree(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("slow-a", "SCENARIO=slow PARTIAL=1", "  execution:\n    cancelGraceSeconds: 1\n")}, 201)
	d.call("POST", "/tasks", map[string]any{"yaml": task("slow-b", "SCENARIO=slow", "")}, 201)
	a := d.call("POST", "/tasks/slow-a/run", nil, 202)["executionId"].(string)
	b := d.call("POST", "/tasks/slow-b/run", nil, 202)["executionId"].(string)
	x := d.waitStatus(a, "running")
	y := d.waitStatus(b, "running")
	// Whatever the cancellation leaves, no stand-in outlives the test.
	killHarnessGroup(t, x["executionDir"].(string))
	killHarnessGroup(t, y["executionDir"].(string))
	// The stand-in writes child.pid after its partial work, so both are there before the cancel.
	child := readPid(t, filepath.Join(x["executionDir"].(string), "child.pid"))
	d.call("POST", "/executions/"+a+"/cancel", nil, 202)
	x = d.waitStatus(a, "cancelled", "failed")
	if x["status"] != "cancelled" {
		t.Fatalf("the cancelled execution is %v: %v", x["status"], x["errorMessage"])
	}
	if x["cancelRequestedAt"] == nil {
		t.Error("the cancellation was not recorded")
	}
	if !gone(child) {
		t.Errorf("the harness's child %d survived the cancellation", child)
	}
	if got := d.call("GET", "/executions/"+b, nil, 200); got["status"] != "running" {
		t.Errorf("the other task's execution is %v", got["status"])
	}

	if attempts, _ := x["attempts"].([]any); len(attempts) != 1 || attempts[0].(map[string]any)["outcome"] != "cancelled" {
		t.Errorf("attempts %v, want one cancelled", attempts)
	}
	waitRecord(t, d.home, a, "cancelled")
	var rec map[string]any
	data, err := os.ReadFile(filepath.Join(d.home, "state", "executions", a+".json"))
	if err == nil {
		err = json.Unmarshal(data, &rec)
	}
	if err != nil || rec["cancelRequestedAt"] == nil {
		t.Errorf("the record on disk lacks cancelRequestedAt (%v): %s", err, data)
	}
	log := d.waitEvent(a, "execution.cancelled")
	events := log["events"].([]any)
	types := map[string]bool{}
	for _, e := range events {
		types[e.(map[string]any)["type"].(string)] = true
	}
	if last := events[len(events)-1].(map[string]any)["type"]; last != "execution.cancelled" || log["logExpired"] != false {
		t.Errorf("the log ends in %v, logExpired %v", last, log["logExpired"])
	}
	for _, want := range []string{"harness.started", "tool.started", "step.started", "execution.cancel_requested"} {
		if !types[want] {
			t.Errorf("the log lacks %s", want)
		}
	}
	if types["execution.failed"] {
		t.Error("the log reads the execution failed")
	}
	out := d.call("GET", "/executions/"+a+"/output", nil, 200)
	captures, _ := out["attempts"].([]any)
	if len(captures) != 1 || out["logExpired"] != false {
		t.Fatalf("output %v", out)
	}
	if text, _ := captures[0].(map[string]any)["text"].(string); !strings.Contains(text, `"subtype":"init"`) ||
		!strings.Contains(text, `"command":"sleep 300"`) {
		t.Errorf("the raw capture lacks what the stand-in printed:\n%s", text)
	}
	for _, kept := range []string{filepath.Join(x["workspacePath"].(string), "partial.txt"),
		filepath.Join(x["executionDir"].(string), "progress.jsonl")} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("the cancellation lost %s: %v", kept, err)
		}
	}
}

// waitEvent returns the execution's stored log once it holds an event of the type: the status can read final before
// the event that says so is appended.
func (d *daemon) waitEvent(id, eventType string) map[string]any {
	d.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		log := d.call("GET", "/executions/"+id+"/events", nil, 200)
		for _, e := range log["events"].([]any) {
			if e.(map[string]any)["type"] == eventType {
				return log
			}
		}
	}
	d.t.Fatalf("the log of %s has no %s", id, eventType)
	return nil
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

// FS-003 AC-003, AC-002: while Claude Code is busy in one task, a Codex task runs and completes; running the busy task
// again queues its firing instead of starting a second execution, and the first one keeps running.
func TestAnotherHarnessIsNotBlocked(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("busy", "SCENARIO=slow", "")}, 201)
	d.call("POST", "/tasks", map[string]any{"yaml": strings.Replace(task("other", "Say hello.", ""),
		"provider: claude-code", "provider: codex", 1)}, 201)
	busy := d.call("POST", "/tasks/busy/run", nil, 202)["executionId"].(string)
	x := d.waitStatus(busy, "running")
	killHarnessGroup(t, x["executionDir"].(string))

	run := d.call("POST", "/tasks/other/run", nil, 202)
	if run["outcome"] != "started" {
		t.Fatalf("the Codex task's run: %v", run)
	}
	done := d.waitStatus(run["executionId"].(string), "completed", "failed")
	attempts, _ := done["attempts"].([]any)
	if done["status"] != "completed" || len(attempts) != 1 || attempts[0].(map[string]any)["harness"] != "codex" {
		t.Errorf("the Codex task's execution: status %v, attempts %v, error %v", done["status"], attempts,
			done["errorMessage"])
	}

	again := d.call("POST", "/tasks/busy/run", nil, 202)
	if again["outcome"] != "queued" || again["executionId"] == busy {
		t.Errorf("running the busy task again: %v", again)
	}
	var running []any
	for _, e := range d.list("/executions?taskId=busy") {
		if e["status"] == "running" {
			running = append(running, e["id"])
		}
	}
	if len(running) != 1 || running[0] != busy {
		t.Errorf("the busy task's running executions: %v, want only %s", running, busy)
	}
	if got := d.call("GET", "/executions/"+busy, nil, 200); got["status"] != "running" {
		t.Errorf("the Claude Code execution is %v", got["status"])
	}
	// The waiting firing goes first, so that ending the running one starts no other stand-in.
	if id, _ := again["executionId"].(string); id != "" {
		d.call("POST", "/executions/"+id+"/cancel", nil, 202)
	}
	d.call("POST", "/executions/"+busy+"/cancel", nil, 202)
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

const daily = "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n"

// setNextFire moves tasks' next firing in state/scheduler.json while the daemon is stopped: the accepted way to
// simulate the daemon being down when a firing came due. The rest of the file is kept as it is.
func setNextFire(t *testing.T, home string, at time.Time, ids ...string) {
	t.Helper()
	path := filepath.Join(home, "state", "scheduler.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	tasks, _ := state["tasks"].(map[string]any)
	for _, id := range ids {
		s, ok := tasks[id].(map[string]any)
		if !ok {
			t.Fatalf("no scheduler state for %s: %s", id, data)
		}
		s["nextFireAt"] = at.UTC().Format(time.RFC3339)
	}
	if data, err = json.Marshal(state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitExecutions waits until a task has n executions and returns them.
func (d *daemon) waitExecutions(taskID string, n int) []map[string]any {
	d.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var runs []map[string]any
	for time.Now().Before(deadline) {
		if runs = d.list("/executions?taskId=" + taskID); len(runs) >= n {
			return runs
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.t.Fatalf("%s has %d executions, want %d", taskID, len(runs), n)
	return nil
}

// nextFireAt is the task's next firing, as the API reports it.
func (d *daemon) nextFireAt(id string) time.Time {
	d.t.Helper()
	next, err := time.Parse(time.RFC3339, fmt.Sprint(d.call("GET", "/tasks/"+id, nil, 200)["nextFireAt"]))
	if err != nil {
		d.t.Fatalf("%s's next firing: %v", id, err)
	}
	return next
}

// scheduleOf reads the scheduler's record of a task from state/scheduler.json, with its last and next firings.
func scheduleOf(t *testing.T, home, id string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "state", "scheduler.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Tasks map[string]map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state.Tasks[id]
}

// startCheck is how long a test gives the scheduler check a daemon runs as it starts, alongside its API, to fire what
// it finds due before the test asserts that it fired nothing: the check leaves no other trace.
const startCheck = time.Second

// FS-002 AC-004, AC-005, AC-008: firings missed while the daemon was down follow each task's missed-run policy once
// after it starts again: one catch-up, or none. One more restart runs nothing.
func TestMissedFiringsAcrossRestart(t *testing.T) {
	home := t.TempDir()
	d := startDaemon(t, home)
	d.call("POST", "/tasks", map[string]any{"yaml": task("daily", "Say hi.", daily)}, 201)
	d.call("POST", "/tasks", map[string]any{"yaml": task("daily-skip", "Say hi.", daily+"    missedRun:\n      policy: skip\n")}, 201)
	next := d.nextFireAt("daily")
	d.stop()
	setNextFire(t, home, next.Add(-3*24*time.Hour), "daily", "daily-skip") // down for three days
	d = startDaemon(t, home)
	// Both firings are decided in the same check, so the catch-up shows the skip was decided too.
	runs := d.waitExecutions("daily", 1)
	if len(runs) != 1 || runs[0]["trigger"] != "catch_up" {
		t.Fatalf("daily: %v", runs)
	}
	if runs := d.list("/executions?taskId=daily-skip"); len(runs) != 0 {
		t.Errorf("the skip policy ran %v", runs)
	}
	if next := d.nextFireAt("daily-skip"); !next.After(time.Now()) {
		t.Errorf("daily-skip's next firing is %v, in the past", next)
	}
	d.waitStatus(runs[0]["id"].(string), "completed")
	d.stop()
	d = startDaemon(t, home)
	time.Sleep(startCheck)
	if runs := d.list("/executions?taskId=daily"); len(runs) != 1 {
		t.Errorf("the second restart ran daily again: %v", runs)
	}
	if runs := d.list("/executions?taskId=daily-skip"); len(runs) != 0 {
		t.Errorf("the second restart ran daily-skip: %v", runs)
	}
}

// FS-002 AC-008; FS-003 FR-007, AC-009: firings survive the daemon being killed. The firing it was running reads
// interrupted and is not caught up again; the one waiting keeps its ID and starts, the task's only new execution; the
// scheduler's record of the last and next firings is what it was before the kill. One more restart runs nothing.
func TestFiringsSurviveAKill(t *testing.T) {
	home := t.TempDir()
	d := startDaemon(t, home)
	d.call("POST", "/tasks", map[string]any{"yaml": task("waits", "SCENARIO=slow", daily)}, 201)
	next := d.nextFireAt("waits")
	d.stop()
	// The firing running at the kill is a catch-up, so the scheduler has processed it.
	setNextFire(t, home, next.Add(-3*24*time.Hour), "waits")
	d = startDaemon(t, home)
	runs := d.waitExecutions("waits", 1)
	if runs[0]["trigger"] != "catch_up" {
		t.Fatalf("the first execution: %v", runs[0])
	}
	caughtUp := runs[0]["id"].(string)
	x := d.waitStatus(caughtUp, "running")
	killHarnessGroup(t, x["executionDir"].(string))
	second := d.call("POST", "/tasks/waits/run", nil, 202)
	if second["outcome"] != "queued" {
		t.Fatalf("second run: %v", second)
	}
	waiting := second["executionId"].(string)
	before := scheduleOf(t, home, "waits")
	if before["lastFiredAt"] == nil || before["nextFireAt"] == nil {
		t.Fatalf("the scheduler's record before the kill: %v", before)
	}
	waitRecord(t, home, caughtUp, "running")
	d.kill()
	d = startDaemon(t, home)
	if got := d.call("GET", "/executions/"+caughtUp, nil, 200); got["status"] != "interrupted" {
		t.Errorf("the running execution is %v, want interrupted", got["status"])
	}
	got := d.waitStatus(waiting, "running")
	killHarnessGroup(t, got["executionDir"].(string))
	ids := map[string]bool{}
	catchUps := 0
	for _, r := range d.list("/executions?taskId=waits") {
		ids[r["id"].(string)] = true
		if r["trigger"] == "catch_up" {
			catchUps++
		}
	}
	if len(ids) != 2 || !ids[caughtUp] || !ids[waiting] || catchUps != 1 {
		t.Errorf("executions after the restart: %v, %d catch-ups; want %s and %s, one catch-up", ids, catchUps, caughtUp,
			waiting)
	}
	after := scheduleOf(t, home, "waits")
	if after["lastFiredAt"] != before["lastFiredAt"] || after["nextFireAt"] != before["nextFireAt"] {
		t.Errorf("the scheduler's record changed across the kill:\n before %v\n after  %v", before, after)
	}
	d.stop()
	d = startDaemon(t, home)
	time.Sleep(startCheck)
	if runs := d.list("/executions?taskId=waits"); len(runs) != 2 {
		t.Errorf("the second restart created an execution: %v", runs)
	}
}

// FS-010 FR-004, §5.2 (FS-010-F04): a scheduled task runs from its firing to completed while no client is connected.
// FS-008 FR-003, AC-003 (FS-008-F03), the daemon's side: a client that connects only afterwards lists the execution,
// recovers its whole history, and resumes the stream after it.
func TestScheduledRunWithDashboardClosed(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	// The task the late client runs to cause a new event; with no schedule, it never fires.
	d.call("POST", "/tasks", map[string]any{"yaml": task("late", "SCENARIO=ok", "")}, 201)
	// Its notices are off, so no notification.created follows the completion in the log.
	scheduled := d.call("POST", "/tasks", map[string]any{"yaml": task("every-minute", "SCENARIO=ok",
		"  schedule:\n    cron: \"* * * * *\"\n    timezone: UTC\n  notifications:\n    native:\n      enabled: false\n")}, 201)
	if scheduled["nextFireAt"] == nil {
		t.Fatal("no next firing")
	}

	// From here until the execution ends, the test calls nothing and opens no stream: it reads logs/ only. Schedules
	// have no seconds field (D-071), so the firing comes at the next minute boundary, then within a scheduler tick.
	id, logged := waitLogEnd(t, d.home, "", 90*time.Second, nil)
	d.call("POST", "/tasks/every-minute/disable", nil, 200)
	if first, last := logged[0], logged[len(logged)-1]; first.Type != "execution.created" ||
		last.Type != "execution.completed" {
		t.Fatalf("the log runs from %s to %s, want execution.created to execution.completed", first.Type, last.Type)
	}
	x := d.call("GET", "/executions/"+id, nil, 200)
	if x["taskId"] != "every-minute" || x["trigger"] != "schedule" || x["status"] != "completed" {
		t.Errorf("execution %s: task %v, trigger %v, status %v", id, x["taskId"], x["trigger"], x["status"])
	}
	// FS-002 FR-001, AC-001 (FS-002-F01): the scheduler fired the occurrence the save announced, on a minute boundary.
	// Only the execution's own view carries scheduledFor; the list does not.
	scheduledFor, err := time.Parse(time.RFC3339Nano, fmt.Sprint(x["scheduledFor"]))
	announced, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(scheduled["nextFireAt"]))
	if err != nil || !scheduledFor.Equal(scheduledFor.Truncate(time.Minute)) || !scheduledFor.Equal(announced) {
		t.Errorf("execution %s: scheduledFor %v (%v), want %v, the minute boundary the save announced", id,
			x["scheduledFor"], err, scheduled["nextFireAt"])
	}

	// A client that connects only now finds the execution, with its task, trigger, status, and times.
	list := d.list("/executions")
	if len(list) != 1 || list[0]["id"] != id {
		t.Fatalf("GET /executions lists %v, want only %s", list, id)
	}
	row := list[0]
	if row["taskId"] != "every-minute" || row["trigger"] != "schedule" || row["status"] != "completed" {
		t.Errorf("the listed execution: task %v, trigger %v, status %v", row["taskId"], row["trigger"], row["status"])
	}
	var times [3]time.Time
	for i, key := range []string{"createdAt", "startedAt", "endedAt"} {
		s, _ := row[key].(string)
		var err error
		if times[i], err = time.Parse(time.RFC3339Nano, s); err != nil {
			t.Fatalf("the listed execution's %s is %v: %v", key, row[key], err)
		}
	}
	if times[1].Before(times[0]) || times[2].Before(times[1]) {
		t.Errorf("created %v, started %v, ended %v are out of order", times[0], times[1], times[2])
	}

	// It gets the whole history, identical by id, type, and order to the lines of the JSONL file.
	status, body := d.request("GET", "/executions/"+id+"/events", nil, nil)
	var replay struct {
		Events     []logLine `json:"events"`
		LogExpired bool      `json:"logExpired"`
	}
	if status != 200 || json.Unmarshal(body, &replay) != nil || replay.LogExpired {
		t.Fatalf("GET /executions/%s/events: %d %s", id, status, body)
	}
	logged = readLog(t, filepath.Join(d.home, "logs", id+".jsonl"))
	if !slices.Equal(replay.Events, logged) {
		t.Fatalf("the replay differs from the log:\n%v\n%v", replay.Events, logged)
	}
	replayed := map[int64]bool{}
	for _, e := range replay.Events {
		replayed[e.ID] = true
	}
	lastID := replay.Events[len(replay.Events)-1].ID

	// It resumes the stream after the last replayed event, then runs another task. Since that event the daemon logged
	// only the disabling, so the stream brings that first, then the new execution, and no replayed event again.
	events, closeStream := d.stream(t, lastID)
	defer closeStream()
	lateID := d.call("POST", "/tasks/late/run", nil, 202)["executionId"].(string)
	var before []string
	timeout := time.After(20 * time.Second)
	for prev, found := lastID, false; !found; {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("the stream ended")
			}
			if e.ID <= prev || replayed[e.ID] || e.Type == "replay_gap" {
				t.Fatalf("the stream brought %s %d after %d, the last replayed being %d", e.Type, e.ID, prev, lastID)
			}
			prev = e.ID
			if found = e.Type == "execution.created" && e.Data["executionId"] == lateID; !found {
				before = append(before, e.Type+" "+fmt.Sprint(e.Data["taskId"]))
			}
		case <-timeout:
			t.Fatalf("no execution.created for %s; the stream brought %v", lateID, before)
		}
	}
	if !slices.Equal(before, []string{"task.updated every-minute"}) {
		t.Errorf("before the new execution, the stream brought %v, want only the disabling", before)
	}
	d.waitStatus(lateID, "completed")
}

// FS-010 AC-006 (FS-010-F04): a client that closes its stream during an execution stops nothing. The execution goes on
// to completed with no client connected, and GET /health answers throughout from the same daemon.
func TestClosingTheStreamStopsNothing(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("ticks", "SCENARIO=ticks", "")}, 201)
	health := d.call("GET", "/health", nil, 200)
	events, closeStream := d.stream(t, 0)
	id := d.call("POST", "/tasks/ticks/run", nil, 202)["executionId"].(string)
	var received int64
	timeout := time.After(20 * time.Second)
	for received == 0 {
		select {
		case e := <-events:
			if e.Data["executionId"] != id {
				continue
			}
			if terminalTypes[e.Type] {
				t.Fatalf("the execution ended (%s) before the client closed", e.Type)
			}
			if e.Type == "harness.message" {
				received = e.ID
			}
		case <-timeout:
			t.Fatal("no harness.message within 20 s")
		}
	}
	closeStream()
	logPath := filepath.Join(d.home, "logs", id+".jsonl")
	for _, l := range readLog(t, logPath) {
		if terminalTypes[l.Type] {
			t.Fatalf("the execution had ended (%s) when the client closed", l.Type)
		}
	}

	_, logged := waitLogEnd(t, d.home, id, 30*time.Second, func() {
		if h := d.call("GET", "/health", nil, 200); h["pid"] != health["pid"] || h["startedAt"] != health["startedAt"] {
			t.Fatalf("GET /health answered for another daemon: %v, then %v", health, h)
		}
	})
	var messages int
	for _, l := range logged {
		switch l.Type {
		case "execution.interrupted", "execution.cancelled", "daemon.stopping":
			t.Errorf("the log holds %s", l.Type)
		case "harness.message":
			if l.ID > received {
				messages++
			}
		}
	}
	if messages == 0 {
		t.Error("the harness logged no message after the client closed")
	}
	if x := d.call("GET", "/executions/"+id, nil, 200); x["status"] != "completed" {
		t.Errorf("the execution is %v, want completed", x["status"])
	}
}

// FS-010 FR-004 (FS-010-F04): the daemon outlives the process that started it. A short-lived parent starts harmflowd as
// the desktop does, in a new process group with its standard streams closed. The test ends the parent's whole process
// group during an execution; the daemon keeps answering and completes the execution.
func TestDaemonOutlivesItsStarter(t *testing.T) {
	d, pid, endParent := startDetachedDaemon(t, t.TempDir())
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != pid || pgid == syscall.Getpgrp() {
		t.Fatalf("the daemon %d is in process group %d (%v), want its own", pid, pgid, err)
	}
	health := d.call("GET", "/health", nil, 200)
	if health["pid"] != float64(pid) {
		t.Fatalf("GET /health answers for %v, the parent started %d", health["pid"], pid)
	}
	d.call("POST", "/tasks", map[string]any{"yaml": task("outlives", "SCENARIO=ticks", "")}, 201)
	id := d.call("POST", "/tasks/outlives/run", nil, 202)["executionId"].(string)
	d.waitStatus(id, "running")
	endParent()
	if x := d.call("GET", "/executions/"+id, nil, 200); x["status"] != "running" {
		t.Fatalf("the execution is %v once the parent ended, want running", x["status"])
	}
	if h := d.call("GET", "/health", nil, 200); h["pid"] != health["pid"] || h["startedAt"] != health["startedAt"] {
		t.Fatalf("GET /health answered for another daemon: %v, then %v", health, h)
	}
	if x := d.waitStatus(id, "completed", "failed", "cancelled", "interrupted"); x["status"] != "completed" {
		t.Errorf("the execution is %v, want completed", x["status"])
	}
}

// runStarter is the test binary run as the short-lived parent of TestDaemonOutlivesItsStarter. It starts the daemon as
// the desktop's start command does (desktop/src-tauri/src/daemon.rs): in a process group of its own, with its standard
// streams on /dev/null, never waited for. It writes the daemon's PID, then stays until it is ended or its standard
// input closes.
func runStarter(bin string, args []string) int {
	pidFile := os.Getenv(starterEnv + "_PID")
	_ = os.Unsetenv(starterEnv)
	_ = os.Unsetenv(starterEnv + "_PID")
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(pidFile+".tmp", []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.Rename(pidFile+".tmp", pidFile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

// startDetachedDaemon starts harmflowd through runStarter, and returns the daemon, its PID, and a function that ends
// the parent with its whole process group. The daemon is no child of the test, so its cleanup stops it by its PID.
func startDetachedDaemon(t *testing.T, home string) (*daemon, int, func()) {
	t.Helper()
	_ = os.Remove(filepath.Join(home, "run", "daemon.json"))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")
	parent := exec.Command(self, "--home", home, "--builtins", repoRoot())
	parent.Env = append(os.Environ(), "HARMFLOW_TOKEN_STORE=file", "CODEX_HOME="+filepath.Join(home, "codex"),
		starterEnv+"="+binary, starterEnv+"_PID="+pidFile)
	// A process group of its own, as an app has, so ending it with its group leaves the test running.
	parent.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	parent.Stdout, parent.Stderr = &out, &out
	// The parent stays until this pipe closes, at the latest when the test process exits.
	if _, err := parent.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	endParent := func() {
		if parent.ProcessState == nil {
			_ = syscall.Kill(-parent.Process.Pid, syscall.SIGKILL)
			_ = parent.Wait()
		}
	}
	t.Cleanup(endParent)
	var pid int
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && pid == 0; time.Sleep(50 * time.Millisecond) {
		data, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(string(data))
	}
	if pid == 0 {
		t.Fatalf("the parent started no daemon:\n%s", out.String())
	}
	// The parent ends first, so that the daemon, once stopped, is reaped rather than left a zombie of the parent.
	t.Cleanup(func() {
		endParent()
		_ = syscall.Kill(pid, syscall.SIGTERM)
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if syscall.Kill(pid, 0) != nil {
				return
			}
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	})
	d := &daemon{t: t, home: home}
	if d.base, d.token = awaitRuntime(home, 20*time.Second); d.base == "" {
		t.Fatalf("the daemon %d wrote no runtime file:\n%s", pid, out.String())
	}
	d.useFakeHarnesses()
	return d, pid, endParent
}

// logLine is a line of an execution's JSONL log, or an event of its replay, as these tests compare them.
type logLine struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	ExecutionID string `json:"executionId"`
}

var terminalTypes = map[string]bool{"execution.completed": true, "execution.no_work": true, "execution.failed": true,
	"execution.cancelled": true, "execution.interrupted": true, "execution.needs_attention": true}

// readLog returns the complete lines of a JSONL log, in order; a last line still being written is left out.
func readLog(t *testing.T, path string) []logLine {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var lines []logLine
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return lines
		}
		var l logLine
		if err := json.Unmarshal(data[:i], &l); err != nil {
			t.Fatalf("%s: %v: %s", path, err, data[:i])
		}
		lines = append(lines, l)
		data = data[i+1:]
	}
}

// waitLogEnd reads logs/ under home, and calls nothing on the daemon, until an execution's JSONL log holds a terminal
// event; it returns the execution's id and the log's lines. An empty id waits for the only execution's log. each, when
// set, runs between two reads.
func waitLogEnd(t *testing.T, home, id string, timeout time.Duration, each func()) (string, []logLine) {
	t.Helper()
	dir := filepath.Join(home, "logs")
	pattern := "*.jsonl"
	if id != "" {
		pattern = id + ".jsonl"
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		files, _ := filepath.Glob(filepath.Join(dir, pattern))
		if len(files) > 1 {
			t.Fatalf("logs/ holds %d execution logs, want one: %v", len(files), files)
		}
		for _, f := range files {
			lines := readLog(t, f)
			for _, l := range lines {
				if terminalTypes[l.Type] {
					return strings.TrimSuffix(filepath.Base(f), ".jsonl"), lines
				}
			}
		}
		if each != nil {
			each()
		}
	}
	var names []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	daemonLog, _ := os.ReadFile(filepath.Join(dir, "daemon.log"))
	if len(daemonLog) > 4000 {
		daemonLog = daemonLog[len(daemonLog)-4000:]
	}
	t.Fatalf("no execution log in logs/ ended within %v; logs/ holds %v; daemon.log ends:\n%s", timeout, names, daemonLog)
	return "", nil
}

// FS-002 AC-004, AC-005, D-080: a firing that came due while the daemon was down follows the missed-run policy, even
// when the daemon is back a minute later.
func TestMisfireAcrossShortRestart(t *testing.T) {
	home := t.TempDir()
	d := startDaemon(t, home)
	d.call("POST", "/tasks", map[string]any{"yaml": task("short", "Say hi.", daily)}, 201)
	d.call("POST", "/tasks", map[string]any{"yaml": task("short-skip", "Say hi.", daily+"    missedRun:\n      policy: skip\n")}, 201)
	d.stop()
	setNextFire(t, home, time.Now().Add(-60*time.Second), "short", "short-skip")
	d = startDaemon(t, home)
	// Both firings are decided in the same check, so the catch-up shows the skip was decided too.
	runs := d.waitExecutions("short", 1)
	if len(runs) != 1 || runs[0]["trigger"] != "catch_up" {
		t.Errorf("short: %v", runs)
	}
	if runs := d.list("/executions?taskId=short-skip"); len(runs) != 0 {
		t.Errorf("the skip policy ran %v", runs)
	}
}

// firings reads a task's firing history from state/scheduler.json.
func firings(t *testing.T, home, taskID string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "state", "scheduler.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Firings map[string][]map[string]any `json:"firings"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state.Firings[taskID]
}

// FS-002 FR-006, AC-008: the record of misfires skipped per the policy survives the daemon being killed.
func TestSkippedMisfireSurvivesAKill(t *testing.T) {
	home := t.TempDir()
	d := startDaemon(t, home)
	d.call("POST", "/tasks", map[string]any{"yaml": task("daily-skip", "Say hi.", daily+"    missedRun:\n      policy: skip\n")}, 201)
	next, err := time.Parse(time.RFC3339, d.call("GET", "/tasks/daily-skip", nil, 200)["nextFireAt"].(string))
	if err != nil {
		t.Fatal(err)
	}
	d.stop()
	setNextFire(t, home, next.Add(-3*24*time.Hour), "daily-skip") // down for three days
	d = startDaemon(t, home)
	deadline := time.Now().Add(20 * time.Second)
	for len(firings(t, home, "daily-skip")) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	d.kill()
	d = startDaemon(t, home)
	h := firings(t, home, "daily-skip")
	if len(h) != 1 {
		t.Fatalf("firing history %v", h)
	}
	r := h[0]
	// Three misfires, or four if 09:00 UTC passed while the test ran.
	missed, _ := r["missed"].(float64)
	if r["outcome"] != "skipped" || r["policy"] != "skip" || r["trigger"] != "catch_up" || missed < 3 ||
		r["reasonKey"] != "firings_missed_skipped" {
		t.Errorf("record %v", r)
	}
	if runs := d.list("/executions?taskId=daily-skip"); len(runs) != 0 {
		t.Errorf("the skip policy ran %v", runs)
	}
}
