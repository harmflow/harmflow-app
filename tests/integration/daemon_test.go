// Package integration runs the real harmflowd binary and talks to it over its local API, with stand-in harness CLIs
// that speak the real output formats. These are the tests across the daemon's process boundary.
package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
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
	if pid, err := strconv.Atoi(os.Getenv(watchdogEnv)); err == nil {
		os.Exit(runWatchdog(pid, os.Getenv(watchdogEnv+"_GROUP") != ""))
	}
	if bin := os.Getenv(starterEnv); bin != "" {
		os.Exit(runStarter(bin, os.Args[1:]))
	}
	if bin := os.Getenv(binaryEnv); bin != "" {
		binary = bin
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "harmflowd-bin")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "harmflowd")
	args := []string{"build", "-ldflags", "-X main.BuiltAt=" + builtAt, "-o", binary}
	// make coverage-daemon sets GOCOVERDIR: each daemon then writes its counters there when it exits, to be merged
	// with the unit tests' own, in the same counter mode.
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

// in is the daemon with its helpers reporting to t, for a subtest: a helper must not fail its parent test.
func (d *daemon) in(t *testing.T) *daemon {
	c := *d
	c.t = t
	return &c
}

// startDaemon starts harmflowd on the home, with env added to the test's own environment, a later entry winning.
func startDaemon(t *testing.T, home string, env ...string) *daemon {
	t.Helper()
	return startDaemonOn(t, home, "", env...)
}

// startDaemonOn is startDaemon with the default token store pointed at keychain, a throwaway keychain file, or with the
// file store, run/token, when keychain is empty. No test reaches the user's login keychain.
func startDaemonOn(t *testing.T, home, keychain string, env ...string) *daemon {
	t.Helper()
	store := []string{"HARMFLOW_TOKEN_STORE=file"}
	if keychain != "" {
		if _, err := os.Stat(keychain); err != nil || !strings.HasPrefix(keychain, os.TempDir()) {
			t.Fatalf("%s is not a throwaway keychain a test made (%v)", keychain, err)
		}
		store = []string{"HARMFLOW_TOKEN_STORE=", "HARMFLOW_KEYCHAIN=" + keychain}
	}
	_ = os.Remove(filepath.Join(home, "run", "daemon.json"))
	cmd := exec.Command(binary, "--home", home, "--builtins", repoRoot())
	// Codex's configuration is the test's own, never the user's.
	cmd.Env = append(append(append(os.Environ(), "CODEX_HOME="+filepath.Join(home, "codex")), store...), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	watchDaemon(t, cmd.Process.Pid, false)
	d := &daemon{t: t, home: home, cmd: cmd}
	if d.base, d.token = awaitRuntime(home, 20*time.Second); d.base != "" && keychain != "" {
		d.token = strings.TrimSpace(string(keychainToken(t, keychain))) // read as a client reads it
	}
	if d.base == "" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
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
	return d.waitStatusWithin(20*time.Second, id, want...)
}

// waitStatusWithin is waitStatus with a deadline of its own, for an execution that starts more processes than most.
func (d *daemon) waitStatusWithin(within time.Duration, id string, want ...string) map[string]any {
	d.t.Helper()
	deadline := time.Now().Add(within)
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

// The token is required everywhere; a web page's Origin and a non-loopback Host are refused. Each refusal is recorded
// in daemon.log with its reason, never with the token it presented.
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

// No request without the token changes anything. Every operation of the contract, the control routes, the read routes,
// /health, and the SSE stream, gets 401 with no Authorization header and with a wrong token. Each request would change
// something if the guard let it through; afterwards the task files, the tasks, the executions, the notices, the
// settings, the harness paths, and the token are as they were, the execution started before still runs, and the daemon
// still answers.
func TestAPIGuardOnEveryRoute(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("finished", "SCENARIO=ok", "")}, 201)
	d.call("POST", "/tasks", map[string]any{"yaml": task("guarded", "SCENARIO=slow", "")}, 201)
	d.call("POST", "/tasks", map[string]any{"yaml": task("dormant", "p", "  enabled: false\n")}, 201)
	finished := d.call("POST", "/tasks/finished/run", nil, 202)["executionId"].(string)
	d.waitStatus(finished, "completed")
	running := d.call("POST", "/tasks/guarded/run", nil, 202)["executionId"].(string)
	d.waitStatus(running, "running")
	var notices []any
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(notices) == 0; {
		for _, n := range d.list("/notifications?unread=true") {
			notices = append(notices, n["id"])
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(notices) == 0 {
		t.Fatal("the completed execution left no unread notice to mark read")
	}
	idle := d.call("GET", "/tasks/finished", nil, 200)
	renamed := strings.Replace(idle["yaml"].(string), "name: Task finished", "name: Renamed", 1)

	control := []guardedRoute{
		{"POST /harnesses/refresh", "/harnesses/refresh", nil},
		{"PUT /harnesses/{harnessId}", "/harnesses/claude-code", map[string]any{"path": nil}},
		{"POST /tasks", "/tasks", map[string]any{"yaml": task("intruder", "p", "")}},
		{"POST /tasks/validate", "/tasks/validate", map[string]any{"yaml": task("intruder", "p", "")}},
		{"POST /tasks/import", "/tasks/import", map[string]any{"yaml": task("imported", "p", "")}},
		{"PUT /tasks/{taskId}", "/tasks/finished", map[string]any{"yaml": renamed, "baseRevision": idle["revision"]}},
		{"DELETE /tasks/{taskId}", "/tasks/finished", nil},
		{"POST /tasks/{taskId}/run", "/tasks/finished/run", nil},
		{"POST /tasks/{taskId}/duplicate", "/tasks/finished/duplicate", nil},
		{"POST /tasks/{taskId}/enable", "/tasks/dormant/enable", nil},
		{"POST /tasks/{taskId}/disable", "/tasks/finished/disable", nil},
		{"POST /schedules/preview", "/schedules/preview",
			map[string]any{"schedule": map[string]any{"cron": "0 9 * * *", "timezone": "UTC"}, "count": 3}},
		{"POST /executions/{executionId}/cancel", "/executions/" + running + "/cancel", nil},
		{"PATCH /settings", "/settings", map[string]any{"language": "pt-BR", "trayEnabled": false, "logRetentionDays": 7}},
		{"POST /settings/token/rotate", "/settings/token/rotate", nil},
		{"POST /workspaces/clean", "/workspaces/clean", nil},
		{"POST /notifications/read", "/notifications/read", map[string]any{"ids": notices}},
		// Last: a stop the guard let through would leave no daemon to send the other requests to.
		{"POST /daemon/stop", "/daemon/stop", map[string]any{"force": true}},
	}
	read := []guardedRoute{
		{"GET /health", "/health", nil},
		{"GET /diagnostics", "/diagnostics", nil},
		{"GET /stats", "/stats", nil},
		{"GET /harnesses", "/harnesses", nil},
		{"GET /tools", "/tools", nil},
		{"GET /catalog", "/catalog", nil},
		{"GET /tasks", "/tasks", nil},
		{"GET /tasks/{taskId}", "/tasks/finished", nil},
		{"GET /tasks/{taskId}/export", "/tasks/finished/export", nil},
		{"GET /executions", "/executions", nil},
		{"GET /executions/{executionId}", "/executions/" + finished, nil},
		{"GET /executions/{executionId}/events", "/executions/" + finished + "/events", nil},
		{"GET /executions/{executionId}/output", "/executions/" + finished + "/output", nil},
		{"GET /events/stream", "/events/stream", nil},
		{"GET /queue", "/queue", nil},
		{"GET /settings", "/settings", nil},
		{"GET /notifications", "/notifications", nil},
	}
	// The tables are checked against the contract both ways, so an operation added to it cannot be left out.
	listed := map[string]bool{}
	for _, r := range control {
		listed[r.op] = true
		if r.method() == "GET" {
			t.Errorf("%s is a read route in the control table", r.op)
		}
	}
	for _, r := range read {
		listed[r.op] = true
		if r.method() != "GET" {
			t.Errorf("%s is a control route in the read table", r.op)
		}
	}
	inContract := map[string]bool{}
	for _, op := range contractOperations(t) {
		inContract[op] = true
		if !listed[op] {
			t.Errorf("%s of contracts/api/openapi.yaml is not tested", op)
		}
	}
	for op := range listed {
		if !inContract[op] {
			t.Errorf("%s is not an operation of contracts/api/openapi.yaml", op)
		}
	}

	pid := d.call("GET", "/health", nil, 200)["pid"]
	before := d.guardState()
	wrong := []byte(d.token)
	wrong[len(wrong)-1] ^= 1 // the token with its last character changed
	for _, r := range append(read, control...) {
		for _, c := range []struct{ name, token string }{{"no Authorization header", ""}, {"a wrong token", string(wrong)}} {
			t.Run(r.op+" with "+c.name, func(t *testing.T) {
				d := d.in(t)

				if status, kind, body := d.requestAs(c.token, r.method(), r.path, r.body); !unauthorized(status, kind, body) {
					t.Errorf("%s %s with %s = %d %s %s, want 401 unauthorized", r.method(), r.path, c.name, status, kind, body)
				}
			})
		}
	}
	after := d.guardState()
	for _, key := range sortedUnion(before, after) {
		if before[key] != after[key] {
			t.Errorf("%s changed:\n%s\nwas:\n%s", key, after[key], before[key])
		}
	}
	x := d.call("GET", "/executions/"+running, nil, 200)
	if x["status"] != "running" || x["cancelRequestedAt"] != nil {
		t.Errorf("the execution started before is %v, cancel requested at %v", x["status"], x["cancelRequestedAt"])
	}
	if got := d.call("GET", "/health", nil, 200)["pid"]; got != pid {
		t.Errorf("the daemon answering is pid %v, want %v", got, pid)
	}
}

// guardedRoute is one operation of the contract, named as contracts/api/openapi.yaml names it, and a request to it
// that would change something if the guard let it through.
type guardedRoute struct {
	op, path string
	body     any
}

func (r guardedRoute) method() string { return strings.Fields(r.op)[0] }

var (
	contractPath   = regexp.MustCompile(`^  (/\S*):$`)
	contractMethod = regexp.MustCompile(`^    (get|put|post|patch|delete):$`)
)

// contractOperations are the operations under paths: in contracts/api/openapi.yaml, as "METHOD /path/{param}".
func contractOperations(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(), "contracts", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	var path string
	inPaths := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case !inPaths:
		case line != "" && !strings.HasPrefix(line, " "):
			inPaths = false
		case contractPath.MatchString(line):
			path = contractPath.FindStringSubmatch(line)[1]
		case contractMethod.MatchString(line):
			ops = append(ops, strings.ToUpper(contractMethod.FindStringSubmatch(line)[1])+" "+path)
		}
	}
	if len(ops) == 0 {
		t.Fatal("no operation found in contracts/api/openapi.yaml")
	}
	return ops
}

// guardClient's timeout ends a request the guard let through to the SSE stream, which would otherwise never end.
var guardClient = &http.Client{Timeout: 5 * time.Second}

// requestAs sends a request with token as its bearer token, or with no Authorization header when token is empty, and
// returns its status, Content-Type, and body. A request that fails is an error of the test, not its end, so every
// route is reported.
func (d *daemon) requestAs(token, method, path string, body any) (int, string, string) {
	d.t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, d.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := guardClient.Do(req)
	if err != nil {
		d.t.Errorf("%s %s: %v", method, path, err)
		return 0, "", ""
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		d.t.Errorf("%s %s: status %d, reading the body: %v", method, path, resp.StatusCode, err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(data)
}

// unauthorized says whether a response is the guard's 401: its JSON error, never a stream.
func unauthorized(status int, kind, body string) bool {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return status == 401 && strings.HasPrefix(kind, "application/json") && json.Unmarshal([]byte(body), &e) == nil &&
		e.Error.Code == "unauthorized"
}

// guardState is what a request the guard let through would change: the task files and the harness paths byte for
// byte, the token, the settings, and the fields of the tasks, executions, and notices any change sets. A running
// execution's record and the harness inventory change on their own, so they are left out.
func (d *daemon) guardState() map[string]string {
	d.t.Helper()
	state := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(d.home, "tasks"))
	if err != nil {
		d.t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(d.home, "tasks", e.Name()))
		if err != nil {
			d.t.Fatal(err)
		}
		state["tasks/"+e.Name()] = string(data)
	}
	for _, name := range []string{"state/harnesses.json", "run/token"} {
		data, err := os.ReadFile(filepath.Join(d.home, name))
		if err != nil {
			d.t.Fatal(err)
		}
		state[name] = string(data)
	}
	// A read that fails is part of the state, so a token that no longer works is reported with every other change.
	for _, read := range []struct {
		path string
		keys []string // nil keeps the whole body
	}{
		{"/settings", nil},
		{"/tasks", []string{"id", "revision", "enabled", "running", "queued", "deletePending"}},
		{"/executions", []string{"id", "taskId", "status"}},
		{"/notifications", []string{"id", "read"}},
	} {
		status, body := d.request("GET", read.path, nil, nil)
		switch {
		case status != 200:
			state["GET "+read.path] = fmt.Sprintf("status %d: %s", status, body)
		case read.keys == nil:
			state["GET "+read.path] = string(body)
		default:
			var items []map[string]any
			_ = json.Unmarshal(body, &items)
			state["GET "+read.path] = fieldsOf(items, read.keys...)
		}
	}
	return state
}

// fieldsOf is one line per item, with the values of its keys.
func fieldsOf(items []map[string]any, keys ...string) string {
	lines := make([]string, 0, len(items))
	for _, it := range items {
		fields := make([]string, 0, len(keys))
		for _, k := range keys {
			fields = append(fields, fmt.Sprintf("%s=%v", k, it[k]))
		}
		lines = append(lines, strings.Join(fields, " "))
	}
	return strings.Join(lines, "\n")
}

func sortedUnion(a, b map[string]string) []string {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// The API listens on loopback only, and the SSE stream refuses a request with no Authorization header like any other
// route.
func TestAPILoopbackOnly(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	if status, kind, body := d.requestAs("", "GET", "/events/stream", nil); !unauthorized(status, kind, body) ||
		strings.HasPrefix(body, "retry:") {
		t.Errorf("the stream with no Authorization header: %d %s %s", status, kind, body)
	}
	data, err := os.ReadFile(filepath.Join(d.home, "run", "daemon.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rt struct {
		Address string `json:"address"`
		Port    int    `json:"port"`
	}
	if err := json.Unmarshal(data, &rt); err != nil || rt.Port == 0 || rt.Address != fmt.Sprintf("127.0.0.1:%d", rt.Port) {
		t.Fatalf("the runtime file's address %q and port %d (%v)", rt.Address, rt.Port, err)
	}
	if conn, err := net.DialTimeout("tcp", rt.Address, 2*time.Second); err != nil {
		t.Fatalf("the API's own address refused a connection: %v", err)
	} else {
		conn.Close()
	}
	// Each non-loopback address of the machine's interfaces must refuse the port. The kernel answers for an address it
	// delivers locally, so a listener on every interface would accept there; an address it does not deliver locally,
	// such as a tunnel's link-local one, times out, which cannot tell a listener from none, so it is left out.
	addrs := nonLoopbackAddresses(t)
	errs := make([]error, len(addrs))
	var wg sync.WaitGroup
	for i, addr := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(addr, strconv.Itoa(rt.Port)), 2*time.Second)
			if conn != nil {
				conn.Close()
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	var refused, unanswered []string
	for i, err := range errs {
		switch {
		case err == nil:
			t.Errorf("%s accepted a connection on the API's port", addrs[i])
		case errors.Is(err, syscall.ECONNREFUSED):
			refused = append(refused, addrs[i])
		default:
			unanswered = append(unanswered, addrs[i])
		}
	}
	if len(unanswered) > 0 {
		t.Logf("left out %v, which this host does not answer for", unanswered)
	}
	if len(refused) == 0 {
		t.Skipf("no non-loopback address this host answers for, of %v", addrs)
	}
	t.Logf("refused on %v", refused)
}

// nonLoopbackAddresses are the addresses of the machine's interfaces but the loopback one, link-local ones with their
// zone.
func nonLoopbackAddresses(t *testing.T) []string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range addrs {
			ip, ok := a.(*net.IPNet)
			if !ok || ip.IP.IsLoopback() {
				continue
			}
			host := ip.IP.String()
			if ip.IP.IsLinkLocalUnicast() {
				host += "%" + ifc.Name
			}
			out = append(out, host)
		}
	}
	return out
}

// A restart keeps the token, and only a rotation replaces it. A client holds the token it read from run/token, as a
// script does: it keeps working across a restart, and fails on a REST route and on the stream once the token is
// rotated, while the token read again works, also after another restart. The rotation's response never holds the new
// token.
func TestAPITokenAcrossRestartAndRotation(t *testing.T) {
	home := t.TempDir()
	tokenFile := filepath.Join(home, "run", "token")
	d := startDaemon(t, home)
	stored, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	held := strings.TrimSpace(string(stored))
	d.stop()

	d = startDaemon(t, home)
	d.token = held // the client's own token, not the one startDaemon read again
	if again, err := os.ReadFile(tokenFile); err != nil || !bytes.Equal(again, stored) {
		t.Fatalf("the restart changed run/token (%v)", err)
	}
	d.call("GET", "/tasks", nil, 200)
	_, closeStream := d.stream(t, 0)
	closeStream()

	status, answer := d.request("POST", "/settings/token/rotate", nil, nil)
	if status != 200 {
		t.Fatalf("rotation: %d %s", status, answer)
	}
	rotated, err := os.ReadFile(tokenFile)
	fresh := strings.TrimSpace(string(rotated))
	if err != nil || fresh == "" || fresh == held {
		t.Fatalf("run/token after the rotation is empty or unchanged (%v)", err)
	}
	if strings.Contains(string(answer), fresh) {
		t.Errorf("the rotation's response holds the new token: %s", answer)
	}
	for _, path := range []string{"/tasks", "/events/stream"} {
		if status, kind, body := d.requestAs(held, "GET", path, nil); !unauthorized(status, kind, body) {
			t.Errorf("GET %s with the rotated token: %d %s %s", path, status, kind, body)
		}
	}
	d.token = fresh
	d.call("GET", "/tasks", nil, 200)
	_, closeStream = d.stream(t, 0)
	closeStream()
	d.stop()

	d = startDaemon(t, home)
	if again, err := os.ReadFile(tokenFile); err != nil || !bytes.Equal(again, rotated) {
		t.Fatalf("the restart after the rotation changed run/token (%v)", err)
	}
	d.token = fresh
	d.call("GET", "/tasks", nil, 200)
	if status, kind, body := d.requestAs(held, "GET", "/tasks", nil); !unauthorized(status, kind, body) {
		t.Errorf("the rotated token after a restart: %d %s %s", status, kind, body)
	}
}

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

// A fresh install keeps autostart off and writes no user agent. A daemon started by hand, not by launchd, fires its
// scheduled task while autostart reads off, so "running now" and "starts at login" are two states. HOME is a throwaway
// directory: the daemon's user agent would lie under it, never in the developer's own LaunchAgents.
func TestHandStartedDaemonFiresWithAutostartOff(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	d := startDaemon(t, t.TempDir())
	plist := filepath.Join(userHome, "Library", "LaunchAgents", "com.harmflow.daemon.plist")
	autostartOff := func(step string) {
		t.Helper()
		a, _ := d.call("GET", "/settings", nil, 200)["autostart"].(map[string]any)
		if a["enabled"] != false || a["mechanism"] != "launchd" || a["plistPath"] != plist {
			t.Fatalf("%s: autostart reads %v, want off, through launchd, at %s", step, a, plist)
		}
		if _, err := os.Stat(plist); !os.IsNotExist(err) {
			t.Fatalf("%s: a user agent exists: %v", step, err)
		}
	}
	autostartOff("a fresh install")

	created := d.call("POST", "/tasks", map[string]any{"yaml": task("minutely", "Say hi.",
		"  schedule:\n    cron: \"* * * * *\"\n    timezone: UTC\n")}, 201)
	next, err := time.Parse(time.RFC3339, fmt.Sprint(created["nextFireAt"]))
	if err != nil {
		t.Fatalf("next firing %v: %v", created["nextFireAt"], err)
	}
	// Schedules have no seconds field, so the firing comes at the next minute boundary, at most 60 s away; the
	// run then has as long again to complete on a loaded host.
	deadline := next.Add(60 * time.Second)
	var x map[string]any
	for x == nil || x["status"] != "completed" {
		if time.Now().After(deadline) {
			t.Fatalf("by %s the task due at %s has not completed a scheduled run: %v", deadline.Format(time.TimeOnly),
				next.Format(time.TimeOnly), x)
		}
		time.Sleep(500 * time.Millisecond)
		d.call("GET", "/health", nil, 200)
		autostartOff("while the task fires")
		for _, e := range d.list("/executions?taskId=minutely") {
			if e["trigger"] == "schedule" && (x == nil || e["id"] == x["id"]) {
				x = e
			}
		}
		if x != nil {
			switch x["status"] {
			case "no_work", "failed", "cancelled", "interrupted", "needs_attention":
				t.Fatalf("the scheduled run ended %v", x["status"])
			}
		}
	}
	// The next minute's firing must not run while the test checks.
	d.call("POST", "/tasks/minutely/disable", nil, 200)
	got := d.call("GET", "/executions/"+x["id"].(string), nil, 200)
	if got["trigger"] != "schedule" || got["status"] != "completed" {
		t.Errorf("the run reads trigger %v, status %v", got["trigger"], got["status"])
	}
	autostartOff("after the run")
}

// The daemon is the only writer; form and YAML go through one validation; files and dashboard converge; conflicts are
// reported.
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
	// An empty list the form sends is a choice the file keeps, not a key dropped for its default.
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
	// The desktop translates the error from its key and params.
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

// A form document that sets every section is written as a file equal to it, which passes validation and is listed;
// after an edit outside the daemon, the task the API returns is the file.
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
	// The task's subagent is the user's, in the test's own configuration of each harness, so the form covers a user's
	// agent beside the built-in skills.
	claudeConfig := filepath.Join(home, "claude")
	for _, dir := range []string{claudeConfig, codexHome} {
		if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "agents", "issue-reviewer.md"),
			[]byte("---\nname: issue-reviewer\ndescription: Reviews an issue.\n---\nReview the issue.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := startDaemon(t, home, "CLAUDE_CONFIG_DIR="+claudeConfig)
	repo := t.TempDir()
	// Outside the home folder, so an allowed folder.
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{repo}}, 200)
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
			"type":    "github",
			"github": map[string]any{
				"repositories": []any{map[string]any{"name": "harmflow/example", "localPath": repo, "baseBranch": "develop"}},
				"queue": map[string]any{"ordering": "newest_first", "maxDiscoveredPerCycle": 5, "maxItemsPerCycle": 2,
					"resumeStrategy": "alternate", "maxResumeAttempts": 1, "maxFailedRetries": 0},
				"delivery": map[string]any{"testRepairAttempts": 0},
			},
			"prompt":      "Implement the oldest issue labelled ready-for-agent.",
			"agents":      []any{"issue-reviewer"},
			"skills":      []any{"hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"},
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

// A new GitHub task selects the built-in issue implementer of this checkout, an agent and not a skill: it is copied
// whole, with no field left out, it brings the built-in skills it uses, and a task made of its copy, with the agent as
// its origin, is valid.
func TestGitHubTypeSelectsTheBuiltinImplementer(t *testing.T) {
	home := t.TempDir()
	d := startDaemon(t, home)
	catalog := d.call("GET", "/catalog", nil, 200)
	var selected string
	for _, x := range catalog["taskTypes"].([]any) {
		if tt := x.(map[string]any); tt["id"] == "github" {
			selected, _ = tt["selectedAgent"].(string)
		}
	}
	if selected != "hf-github-issue-implementer" {
		t.Fatalf("GET /catalog: the GitHub type selects %q, want hf-github-issue-implementer", selected)
	}
	copied := d.builtinAgentCopy(selected)
	builtinSkills := map[string]bool{}
	for _, x := range catalog["skills"].([]any) {
		if s := x.(map[string]any); s["source"] == "builtin" {
			builtinSkills[s["name"].(string)] = true
		}
	}
	if builtinSkills[selected] {
		t.Errorf("GET /catalog lists %s as a built-in skill too", selected)
	}
	skills, _ := copied["skills"].([]any)
	if want := []any{"hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"}; !reflect.DeepEqual(skills, want) {
		t.Errorf("the copy of %s brings the skills %v, want %v", selected, skills, want)
	}
	for _, s := range skills {
		if !builtinSkills[s.(string)] {
			t.Errorf("the copy of %s brings %v, which is no built-in skill", selected, s)
		}
	}
	if omitted, _ := copied["omittedFields"].([]any); len(omitted) != 0 {
		t.Errorf("the copy of %s leaves out %v, want nothing", selected, omitted)
	}
	prompt, _ := copied["prompt"].(string)
	if !strings.Contains(prompt, "\n## This task\n") {
		t.Errorf("the copy of %s has no This task section to customize:\n%s", selected, prompt)
	}

	repo := t.TempDir()
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{repo}}, 200)
	doc := map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": "implement", "name": "Implement GitHub issues", "description": copied["description"]},
		"spec": map[string]any{
			"type":    "github",
			"prompt":  prompt,
			"origin":  map[string]any{"agent": selected, "hash": copied["hash"]},
			"skills":  skills,
			"harness": map[string]any{"preferred": map[string]any{"provider": "claude-code"}},
			"github": map[string]any{
				"repositories": []any{map[string]any{"name": "harmflow/example", "localPath": repo}},
			},
		}}
	if v := d.call("POST", "/tasks/validate", map[string]any{"document": doc}, 200); v["valid"] != true {
		t.Errorf("a task made of the copy of %s fails validation: %v", selected, v["errors"])
	}
}

// builtinAgentCopy is what selecting the built-in agent copies into a task, as GET /catalog gives it.
func (d *daemon) builtinAgentCopy(name string) map[string]any {
	d.t.Helper()
	for _, x := range d.call("GET", "/catalog", nil, 200)["agents"].([]any) {
		if a := x.(map[string]any); a["name"] == name && a["source"] == "builtin" {
			if c, ok := a["copy"].(map[string]any); ok {
				return c
			}
		}
	}
	d.t.Fatalf("GET /catalog lists no built-in agent %s with a copy", name)
	return nil
}

// One file that breaks a schema rule is refused the same way by the form, the YAML editor's create and save, and
// import, and nothing is written; the file with the rule fixed is accepted by each.
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
		t.Run(tc.name, func(t *testing.T) {
			d := d.in(t)

			e, _ := d.call(tc.method, tc.path, tc.body, 422)["error"].(map[string]any)
			validation, _ := e["validation"].(map[string]any)
			errs, _ := validation["errors"].([]any)
			if e["code"] != "validation_failed" || len(errs) == 0 {
				t.Errorf("%s %s error = %v, want validation_failed with errors", tc.method, tc.path, e)
				return
			}
			first := errs[0].(map[string]any)
			if first["path"] != "/spec/execution/timeoutMinutes" || first["messageKey"] != "schema_minimum" || first["message"] == "" {
				t.Errorf("%s %s first error = %v, want schema_minimum at /spec/execution/timeoutMinutes", tc.method, tc.path, first)
			}
			if want == nil {
				want = first
			} else if first["path"] != want["path"] || first["messageKey"] != want["messageKey"] || first["message"] != want["message"] {
				t.Errorf("%s %s first error = %v, want the form's %v", tc.method, tc.path, first, want)
			}
			if _, fromYAML := tc.body["yaml"]; fromYAML && first["line"] != float64(line) {
				t.Errorf("%s %s first error line = %v, want %d", tc.method, tc.path, first["line"], line)
			}
		})
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

// A task file broken while the daemon is down is named, beside its line, once it restarts.
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

// A save accepts a 5-field cron or a simple interval, and rejects a cron with a seconds field without writing a task
// file.
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

// The schedule preview and a save agree on each cron macro, on a macro in another case, and on a 5-field expression:
// both accept it, or both refuse it.
func TestCronMacrosAgreeOnPreviewAndSave(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	for i, c := range []struct {
		cron     string
		accepted bool
	}{
		{"@yearly", true}, {"@annually", true}, {"@monthly", true}, {"@weekly", true}, {"@daily", true},
		{"@midnight", true}, {"@hourly", true}, {"@DAILY", false}, {"0 9 * * 1-5", true},
	} {
		t.Run(c.cron, func(t *testing.T) {
			d := d.in(t)

			preview := d.call("POST", "/schedules/preview",
				map[string]any{"schedule": map[string]any{"cron": c.cron, "timezone": "UTC"}}, 200)
			occurrences, _ := preview["occurrences"].([]any)
			previewed := preview["error"] == nil && len(occurrences) > 0
			status, body := d.request("POST", "/tasks", map[string]any{"yaml": task(fmt.Sprintf("cron-%d", i), "Say hi.",
				"  enabled: false\n  schedule:\n    cron: \""+c.cron+"\"\n    timezone: UTC\n")}, nil)
			saved := status == 201
			if previewed != c.accepted || saved != c.accepted {
				t.Errorf("cron %q: preview accepted = %v (%v), save status %d (%s); want both accepted = %v",
					c.cron, previewed, preview, status, body, c.accepted)
			}
			if !c.accepted && status != 422 {
				t.Errorf("cron %q: save status %d, want 422", c.cron, status)
			}
		})
	}
}

// A task whose cron is @daily is saved, imported, and loaded from its file at start, and each fires next at the coming
// midnight in its time zone.
func TestDailyMacroFiresAtMidnight(t *testing.T) {
	const zone = "America/Sao_Paulo"
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	daily := "  schedule:\n    cron: \"@daily\"\n    timezone: " + zone + "\n"
	home := t.TempDir()
	before := time.Now()
	d := startDaemon(t, home)
	d.call("POST", "/tasks", map[string]any{"yaml": task("saved", "Say hi.", daily)}, 201)
	d.call("POST", "/tasks/import", map[string]any{"yaml": task("imported", "Say hi.", daily)}, 201)
	d.stop()
	if err := os.WriteFile(filepath.Join(home, "tasks", "loaded.yaml"), []byte(task("loaded", "Say hi.", daily)), 0o600); err != nil {
		t.Fatal(err)
	}
	d = startDaemon(t, home)
	after := time.Now()
	// The midnight that follows an instant, in the task's time zone.
	midnightAfter := func(at time.Time) time.Time {
		y, m, day := at.In(loc).Date()
		return time.Date(y, m, day+1, 0, 0, 0, 0, loc)
	}
	for _, id := range []string{"saved", "imported", "loaded"} {
		got := d.call("GET", "/tasks/"+id, nil, 200)
		next, err := time.Parse(time.RFC3339, fmt.Sprint(got["nextFireAt"]))
		// The midnight after the test began, or the one after it if a midnight passed while the test ran.
		if err != nil || (!next.Equal(midnightAfter(before)) && !next.Equal(midnightAfter(after))) {
			t.Errorf("%s: next firing %v (%v), want %v in %s", id, got["nextFireAt"], err, midnightAfter(before), zone)
		}
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

// checkEvent checks one event's JSON against event.schema.json and returns its id and type.
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

// Live events over SSE, and the same events, in the same order, in the replay the API serves and in the JSONL log; a
// reconnection resumes after the last id.
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

// A client that disconnects while an execution runs, and reconnects with Last-Event-ID set to the last event it
// received, receives every event emitted while it was away, in order, once each and with no replay_gap, and then live
// ones. What it received in the end is the stored log.
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

// A running execution's detail and its row in the list give its current step and when it started, and no field of
// either speaks of a percentage or of progress.
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

// The raw output of a completed execution is what the CLI printed, stdout and stderr, with the token it contained
// redacted.
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
	// The record the API serves keeps the command and the final text, redacted, in its detail and list.
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

// The daemon applies log retention on its own when it starts, and deletes a log older than the default 30 days. The
// execution is still served with its pull request and its attempt, and its events read as expired.
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
		t.Fatalf("the record lost what outlives an expired log: %v", x)
	}
	if a, _ := attempts[0].(map[string]any); a["harness"] != "claude-code" || a["outcome"] != "succeeded" {
		t.Errorf("the record lost its attempt: %v", a)
	}
	events := d2.call("GET", "/executions/"+id+"/events", nil, 200)
	if list, ok := events["events"].([]any); !ok || len(list) != 0 || events["logExpired"] != true {
		t.Errorf("the expired execution's events: %v", events)
	}
}

// Claude Code exhausts its quota; Codex finishes the same execution.
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
	// Claude Code's init line names its model; Codex's stream names none, and neither names its effort, so those are
	// absent, and the configured default is kept apart, never in their place.
	if a1["model"] != "claude-haiku-4-5-20251001" || a2["model"] != nil || a1["effort"] != nil || a2["effort"] != nil ||
		a1["configuredModel"] != "default" || a2["configuredModel"] != "default" ||
		a2["configuredEffort"] != "default" {
		t.Errorf("models and efforts: %v %v", a1, a2)
	}
	if x["model"] != nil {
		t.Errorf("the execution's model %v is not its current attempt's", x["model"])
	}
	// The replay and the JSONL log hold the same events, each fitting event.schema.json.
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
	// Each attempt keeps the prompt it sent and its own session context, under the contract's names.
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

// harmflow-app#214: Claude Code writes partial.txt from the shell, which its stream does not report, then exhausts its
// quota. The first attempt's checkpoint lists the file, found in the plain workspace by its modification time, though
// the execution's changedFiles, which the harness reported, do not. Codex's session context, composed before its
// attempt started, names the file and says what the scan cannot see.
func TestFailoverCheckpointListsAShellWrittenFile(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("checkpoint", "SCENARIO=quota PARTIAL=1",
		"    fallback:\n      candidates:\n        - provider: codex\n")}, 201)
	run := d.call("POST", "/tasks/checkpoint/run", nil, 202)
	x := d.waitStatus(run["executionId"].(string), "completed", "failed")
	attempts, _ := x["attempts"].([]any)
	if x["status"] != "completed" || len(attempts) != 2 {
		t.Fatalf("status %v attempts %v error %v", x["status"], attempts, x["errorMessage"])
	}
	a1, a2 := attempts[0].(map[string]any), attempts[1].(map[string]any)
	cp, _ := a1["checkpoint"].(map[string]any)
	files, _ := cp["files"].([]any)
	if len(files) != 1 || cp["scanned"] != true || cp["scanLimited"] != nil || cp["more"] != nil {
		t.Fatalf("the first attempt's checkpoint = %v, want partial.txt alone, scanned", cp)
	}
	if f, _ := files[0].(map[string]any); f["path"] != "partial.txt" || f["status"] != "modified" {
		t.Errorf("the checkpoint's file = %v, want partial.txt modified", f)
	}
	if a2["checkpoint"] != nil {
		t.Errorf("the last attempt's checkpoint = %v, want none: no switch followed it", a2["checkpoint"])
	}
	if changed, _ := x["changedFiles"].([]any); slices.Contains(changed, any("partial.txt")) {
		t.Errorf("changedFiles %v hold partial.txt, which the fake harness never reports", changed)
	}
	c2, _ := a2["sessionContext"].(string)
	for _, s := range []string{"partial.txt (modified)", "a file it deleted, or one whose modification time was kept, is not listed"} {
		if !strings.Contains(c2, s) {
			t.Errorf("Codex's session context does not hold %q:\n%s", s, c2)
		}
	}
}

// A failover whose attempts both reported their usage. Claude Code's cost is the one its CLI printed; Codex's, at the
// model its configuration sets, is estimated from the price table and stamped on the attempt.
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
	// The replay and the JSONL log hold the same events, each fitting event.schema.json.
	id := run["executionId"].(string)
	events, jsonl := d.storedLog(t, id)
	sameEvents(t, id+".jsonl", throughCompletion(t, id+".jsonl", jsonl), throughCompletion(t, "the replay", events))
}

// A harness that exits on its own with a quota failure leaves a background process that redirects its output, and the
// execution fails over. The process is ended before the second attempt starts, and the log names it as left running.
func TestFailoverEndsWhatTheFirstAttemptLeft(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("leftover", "SCENARIO=leftover",
		"    fallback:\n      candidates:\n        - provider: codex\n  execution:\n    cancelGraceSeconds: 1\n")}, 201)
	run := d.call("POST", "/tasks/leftover/run", nil, 202)
	id := run["executionId"].(string)
	// The execution starts eleven processes one after another: eight checks of the two harnesses before their attempts,
	// the two stand-ins, and the mv that writes the leftover's pid. On a loaded host each start can take seconds: 20
	// seconds did not always cover them, and a heavier load took them up to 75.
	x := d.waitStatusWithin(120*time.Second, id, "completed", "failed")
	left := readPid(t, filepath.Join(x["executionDir"].(string), "leftover.pid"))
	t.Cleanup(func() {
		if !gone(left) {
			_ = syscall.Kill(left, syscall.SIGKILL)
		}
	})
	attempts := x["attempts"].([]any)
	if x["status"] != "completed" || len(attempts) != 2 {
		t.Fatalf("status %v attempts %v error %v", x["status"], attempts, x["errorMessage"])
	}
	if a1 := attempts[0].(map[string]any); a1["harness"] != "claude-code" || a1["outcome"] != "quota_exhausted" {
		t.Errorf("attempt 1 %v", a1)
	}
	if !gone(left) {
		t.Errorf("the background process %d the first attempt left survived the execution", left)
	}
	// The ended process is named before the second harness starts: the daemon names only a process it saw gone.
	var order []string
	for _, raw := range d.waitEvent(id, "execution.completed")["events"].([]any) {
		ev := raw.(map[string]any)
		p, _ := ev["payload"].(map[string]any)
		msg, _ := ev["message"].(string)
		switch {
		case ev["type"] == "harness.started":
			order = append(order, "harness.started")
		case ev["type"] == "process.ended" && p["pid"] == float64(left):
			if p["executable"] != "sleep" || ev["messageKey"] != "process_left_ended" ||
				!strings.Contains(msg, "sleep (pid "+strconv.Itoa(left)+")") {
				t.Errorf("the log names the background process as %v", ev)
			}
			order = append(order, "process.ended")
		}
	}
	if strings.Join(order, ",") != "harness.started,process.ended,harness.started" {
		t.Errorf("the log's order: %v, want the background process ended between the two harnesses", order)
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

// Cancelling ends the harness and its children, the one in its group and the one in a session of its own, as Claude
// Code runs a tool command, and the log names the second; another task keeps running. The execution ends cancelled, and
// its record, log, raw output, workspace, and execution directory are kept.
func TestCancelEndsTheTree(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("slow-a", "SCENARIO=session PARTIAL=1", "  execution:\n    cancelGraceSeconds: 1\n")}, 201)
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
	session := readPid(t, filepath.Join(x["executionDir"].(string), "session.pid"))
	// Named in the log as what it runs when the cancel finds it.
	waitPs(t, session, "ucomm", "sleep")
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
	if !gone(session) {
		t.Errorf("the harness's child %d in a session of its own survived the cancellation", session)
	}
	named := false
	for _, raw := range d.call("GET", "/executions/"+a+"/events", nil, 200)["events"].([]any) {
		ev := raw.(map[string]any)
		p, _ := ev["payload"].(map[string]any)
		msg, _ := ev["message"].(string)
		if ev["type"] == "process.ended" && p["pid"] == float64(session) && p["executable"] == "sleep" &&
			ev["messageKey"] == "process_ended" && strings.Contains(msg, "sleep (pid "+strconv.Itoa(session)+")") {
			named = true
		}
		if ev["type"] == "process.ended" && p["pid"] == float64(child) {
			t.Errorf("the log names the child %d in the harness's group as ended outside it", child)
		}
	}
	if !named {
		t.Errorf("the log does not name the child %d in a session of its own", session)
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

// A process that keeps the harness's output, and that no search for the execution's processes finds, holds up neither
// the cancellation nor the task's next run nor the daemon's stop.
func TestAStrayKeepingTheOutputHoldsNothingUp(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	d.call("POST", "/tasks", map[string]any{"yaml": task("stray", "SCENARIO=stray", "  execution:\n    cancelGraceSeconds: 1\n")}, 201)
	stray := func(id string) int {
		x := d.waitStatus(id, "running")
		pid := readPid(t, filepath.Join(x["executionDir"].(string), "stray.pid"))
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		// launchd adopted it, and it runs sleep without the execution's id: nothing can find it.
		waitPs(t, pid, "ppid", "1")
		waitPs(t, pid, "ucomm", "sleep")
		return pid
	}
	a := d.call("POST", "/tasks/stray/run", nil, 202)["executionId"].(string)
	first := stray(a)
	d.call("POST", "/executions/"+a+"/cancel", nil, 202)
	d.waitStatus(a, "cancelled")
	if syscall.Kill(first, 0) != nil {
		t.Fatalf("the stray %d was ended, so the bound went untested", first)
	}
	// The task is free: it runs again. The record reads cancelled a moment before the task is released, so the run
	// may wait for that, and then it starts.
	run := d.call("POST", "/tasks/stray/run", nil, 202)
	if run["outcome"] != "started" && run["outcome"] != "queued" {
		t.Fatalf("rerun %v", run)
	}
	second := stray(run["executionId"].(string))
	// SIGTERM stops the daemon while the stray keeps the running execution's output.
	_ = d.cmd.Process.Signal(syscall.SIGTERM)
	exited := make(chan struct{})
	go func() { _ = d.cmd.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		_ = d.cmd.Process.Kill()
		<-exited
		t.Fatal("the daemon did not stop while the stray kept the output")
	}
	if syscall.Kill(second, 0) != nil {
		t.Errorf("the stray %d was ended, so the bound went untested", second)
	}
}

// waitPs waits until ps shows the value for the process's field.
func waitPs(t *testing.T, pid int, field, want string) {
	t.Helper()
	var got string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		out, _ := exec.Command("ps", "-o", field+"=", "-p", strconv.Itoa(pid)).Output()
		if got = strings.TrimSpace(string(out)); got == want {
			return
		}
	}
	t.Fatalf("process %d: %s is %q, want %q", pid, field, got, want)
}

// An execution running when the daemon is killed reads interrupted after the restart, with its log. Its harness, in a
// process group of its own, outlives the daemon; the restart ends it before the execution reads interrupted, with its
// child in its group and a command in a session of its own, as Claude Code runs a tool command, and the log names each.
func TestRestartInterrupts(t *testing.T) {
	home := t.TempDir()
	d := startDaemon(t, home)
	d.call("POST", "/tasks", map[string]any{"yaml": task("crash", "SCENARIO=session", "  execution:\n    cancelGraceSeconds: 1\n")}, 201)
	id := d.call("POST", "/tasks/crash/run", nil, 202)["executionId"].(string)
	dir := d.waitStatus(id, "running")["executionDir"].(string)
	// Whatever the restart leaves, no stand-in outlives the test.
	killHarnessGroup(t, dir)
	child := readPid(t, filepath.Join(dir, "child.pid"))
	session := readPid(t, filepath.Join(dir, "session.pid"))
	waitPs(t, session, "ucomm", "sleep")
	// The record names the harness as soon as it starts.
	var harness int
	for deadline := time.Now().Add(15 * time.Second); harness == 0 && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		var rec struct {
			Attempts []struct {
				Process *struct {
					Pid int `json:"pid"`
				} `json:"process"`
			} `json:"attempts"`
		}
		data, err := os.ReadFile(filepath.Join(home, "state", "executions", id+".json"))
		if err == nil && json.Unmarshal(data, &rec) == nil && len(rec.Attempts) == 1 && rec.Attempts[0].Process != nil {
			harness = rec.Attempts[0].Process.Pid
		}
	}
	if harness == 0 {
		t.Fatal("the record on disk never named the harness process")
	}
	d.kill()
	if gone(harness) {
		t.Fatalf("the harness %d ended with the daemon, so the restart had nothing to end", harness)
	}
	d2 := startDaemon(t, home)
	x := d2.call("GET", "/executions/"+id, nil, 200)
	if x["status"] != "interrupted" {
		t.Fatalf("status %v", x["status"])
	}
	events := d2.call("GET", "/executions/"+id+"/events", nil, 200)
	if events["logExpired"] == true || len(events["events"].([]any)) == 0 {
		t.Error("the attempt's log was lost")
	}
	ended := map[int]bool{}
	started := 0
	for _, raw := range events["events"].([]any) {
		ev := raw.(map[string]any)
		p, _ := ev["payload"].(map[string]any)
		pid, _ := p["pid"].(float64)
		switch {
		case ev["type"] == "harness.started":
			started = int(pid)
		case ev["type"] == "process.ended" && ev["messageKey"] == "process_interrupted_ended":
			ended[int(pid)] = true
		}
	}
	if started != harness {
		t.Errorf("the record names the harness %d, harness.started %d", harness, started)
	}
	for name, pid := range map[string]int{"harness": harness, "child in its group": child, "command in a session": session} {
		if !gone(pid) {
			t.Errorf("the %s, %d, survived the restart", name, pid)
		}
		if !ended[pid] {
			t.Errorf("the log does not name the %s, %d, as ended", name, pid)
		}
	}
	// The task can run again right away.
	run := d2.call("POST", "/tasks/crash/run", nil, 202)
	if run["outcome"] != "started" {
		t.Errorf("rerun %v", run)
	}
	d2.call("POST", "/executions/"+run["executionId"].(string)+"/cancel", nil, 202)
}

// While Claude Code is busy in one task, a Codex task runs and completes; running the busy task again queues its firing
// instead of starting a second execution, and the first one keeps running.
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

// The session selects a work item, Claude Code exhausts its quota, and Codex takes over; the daemon is killed while
// Codex runs. After the restart, the execution is interrupted with both its attempts, and the log and raw capture of
// each; the interruption counts once against the item. Starting again over that recovered state changes nothing.
func TestKillDuringTheSecondAttempt(t *testing.T) {
	home := t.TempDir()
	d := startDaemon(t, home)
	d.call("POST", "/tasks", map[string]any{"yaml": task("failover-crash", "ITEM=o/r#5 SCENARIO=quota CODEX=slow",
		"    fallback:\n      candidates:\n        - provider: codex\n")}, 201)
	id := d.call("POST", "/tasks/failover-crash/run", nil, 202)["executionId"].(string)
	d.waitLogUntil(id, func(events []map[string]any) bool {
		for _, e := range events {
			if e["type"] == "tool.started" && e["attempt"] == float64(2) {
				return true
			}
		}
		return false
	})
	killHarnessGroup(t, d.call("GET", "/executions/"+id, nil, 200)["executionDir"].(string))
	before := d.events(id)
	d.kill()

	d2 := startDaemon(t, home)
	x := d2.call("GET", "/executions/"+id, nil, 200)
	attempts, _ := x["attempts"].([]any)
	if x["status"] != "interrupted" || len(attempts) != 2 {
		t.Fatalf("status %v, attempts %v", x["status"], attempts)
	}
	a1, a2 := attempts[0].(map[string]any), attempts[1].(map[string]any)
	if a1["harness"] != "claude-code" || a1["outcome"] != "quota_exhausted" || a2["harness"] != "codex" ||
		a2["outcome"] != "interrupted" {
		t.Errorf("attempts %v %v", a1, a2)
	}
	// The log keeps every event the API showed before the kill, in its order, with the events of both attempts.
	after := d2.events(id)
	if len(after) < len(before) {
		t.Fatalf("the log has %d events, the API showed %d before the kill", len(after), len(before))
	}
	for i, e := range before {
		if after[i]["id"] != e["id"] || after[i]["type"] != e["type"] {
			t.Fatalf("event %d is %v %v, the API showed %v %v before the kill", i, after[i]["id"], after[i]["type"],
				e["id"], e["type"])
		}
	}
	perAttempt := map[any]int{}
	for _, e := range after {
		perAttempt[e["attempt"]]++
	}
	if perAttempt[float64(1)] == 0 || perAttempt[float64(2)] == 0 {
		t.Errorf("events per attempt %v", perAttempt)
	}
	// The raw capture of each attempt holds a line its CLI printed.
	out := d2.call("GET", "/executions/"+id+"/output", nil, 200)
	captures, _ := out["attempts"].([]any)
	if len(captures) != 2 {
		t.Fatalf("output %v", out)
	}
	for i, want := range []struct{ harness, line string }{
		{"claude-code", `"type":"rate_limit_event"`}, {"codex", `"type":"item.started"`}} {
		t.Run(want.harness, func(t *testing.T) {

			c := captures[i].(map[string]any)
			if text, _ := c["text"].(string); c["attempt"] != float64(i+1) || c["harness"] != want.harness ||
				!strings.Contains(text, want.line) {
				t.Errorf("GET /executions/%s/output attempt %d = %v, want %s with %s", id, i+1, c, want.harness, want.line)
			}
		})
	}
	d2.interruptedOnce(id, "o/r#5")

	// A second start recovers nothing more: the record, the log, and the item stay as the first one left them.
	d2.kill()
	d3 := startDaemon(t, home)
	if again := d3.call("GET", "/executions/"+id, nil, 200); again["status"] != "interrupted" ||
		!reflect.DeepEqual(again["attempts"], x["attempts"]) || again["endedAt"] != x["endedAt"] {
		t.Errorf("the second start changed the execution:\n%v\nwas\n%v", again, x)
	}
	d3.interruptedOnce(id, "o/r#5")
}

// interruptedOnce checks that recovery recorded the execution's interruption once, in its log and against its work
// item, which keeps its key, has one entry in the queue, and reads failed after its one failure.
func (d *daemon) interruptedOnce(id, key string) {
	d.t.Helper()
	n := 0
	for _, e := range d.events(id) {
		if e["type"] == "execution.interrupted" {
			n++
		}
	}
	if n != 1 {
		d.t.Errorf("the log has %d execution.interrupted events, want 1", n)
	}
	items, _ := d.call("GET", "/queue", nil, 200)["workItems"].([]any)
	if len(items) != 1 {
		d.t.Fatalf("GET /queue lists %d work items, want 1: %v", len(items), items)
	}
	w := items[0].(map[string]any)
	if w["key"] != key || w["status"] != "failed" || w["failures"] != float64(1) || w["lastExecutionId"] != id {
		d.t.Errorf("work item %v; want %s failed once by %s", w, key, id)
	}
}

// The daemon's side: the session selects a work item, completes a git push, and waits; the daemon is killed as soon as
// the push's tool.completed is in the log. After the restart, the execution is interrupted and never continued, the
// item is failed with the push as its last operation, and the next run is a new execution whose context names both.
func TestKillAfterAPush(t *testing.T) {
	const item, push = "o/r#7", "git push -u origin harmflow/7-crash"
	home := t.TempDir()
	d := startDaemon(t, home)
	_, repo := pushRepository(t)
	id, _ := d.runUntilPushed("pusher", repo, push)

	d2 := startDaemon(t, home)
	x := d2.call("GET", "/executions/"+id, nil, 200)
	if attempts, _ := x["attempts"].([]any); x["status"] != "interrupted" || len(attempts) != 1 ||
		attempts[0].(map[string]any)["outcome"] != "interrupted" {
		t.Fatalf("status %v, attempts %v", x["status"], x["attempts"])
	}
	items, _ := d2.call("GET", "/queue", nil, 200)["workItems"].([]any)
	if len(items) != 1 {
		t.Fatalf("GET /queue lists %d work items, want 1: %v", len(items), items)
	}
	if w := items[0].(map[string]any); w["key"] != item || w["status"] != "failed" || w["failures"] != float64(1) ||
		w["lastOperation"] != push || w["lastExecutionId"] != id {
		t.Errorf("work item %v; want %s failed once by %s, after %q", w, item, id, push)
	}

	run := d2.call("POST", "/tasks/pusher/run", nil, 202)
	next := d2.waitStatus(run["executionId"].(string), "completed", "failed", "interrupted")
	if next["id"] == id || next["status"] != "completed" {
		t.Fatalf("the next run: %v %v, error %v", next["id"], next["status"], next["errorMessage"])
	}
	ctx := readContext(t, next)
	if want := (interruptedEntry{id, item, push}); len(ctx.Interrupted) != 1 || ctx.Interrupted[0] != want {
		t.Errorf("the next context's interruptedExecutions %+v, want [%+v]", ctx.Interrupted, want)
	}
	if again := d2.call("GET", "/executions/"+id, nil, 200); again["status"] != "interrupted" ||
		len(again["attempts"].([]any)) != 1 {
		t.Errorf("the interrupted execution was continued: %v, attempts %v", again["status"], again["attempts"])
	}
}

// The session commits on a branch, pushes it to the workspace repository's origin, a local bare repository, and waits;
// the daemon is killed and restarted, and the task's next execution runs to its end. Nothing the interrupted one did is
// undone: the pushed branch and its commit, the local branch, and the workspace's files, the uncommitted one included.
func TestNothingUndoneAfterAKill(t *testing.T) {
	const branch = "refs/heads/harmflow/7-crash"
	home := t.TempDir()
	d := startDaemon(t, home)
	remote, repo := pushRepository(t)
	_, pushed := d.runUntilPushed("no-undo", repo, "git push -u origin harmflow/7-crash")

	d2 := startDaemon(t, home)
	run := d2.call("POST", "/tasks/no-undo/run", nil, 202)
	if next := d2.waitStatus(run["executionId"].(string), "completed", "failed", "interrupted"); next["status"] != "completed" {
		t.Fatalf("the next execution: %v, error %v", next["status"], next["errorMessage"])
	}
	if got := git(t, remote, "rev-parse", branch); got != pushed {
		t.Errorf("the remote's branch is at %s, was pushed at %s", got, pushed)
	}
	if got := git(t, repo, "rev-parse", branch); got != pushed {
		t.Errorf("the local branch is at %s, was pushed at %s", got, pushed)
	}
	if got := git(t, repo, "symbolic-ref", "HEAD"); got != branch {
		t.Errorf("the workspace is on %s, was on %s", got, branch)
	}
	if got := git(t, repo, "log", "--format=%s", "main.."+branch); got != "fix: end the crash" {
		t.Errorf("the branch's commits since main: %q", got)
	}
	if got := git(t, repo, "status", "--porcelain"); got != "?? notes.txt" {
		t.Errorf("the workspace's status: %q", got)
	}
	for name, want := range map[string]string{"fix.txt": "the fix\n", "notes.txt": "work in progress\n"} {
		if data, err := os.ReadFile(filepath.Join(repo, name)); err != nil || string(data) != want {
			t.Errorf("%s: %q, %v; want %q", name, data, err, want)
		}
	}
}

// The daemon's side: the session selects a work item, opens a pull request, and waits; the daemon is killed once the
// execution names the pull request. The next execution's context lists the interrupted execution with its work item and
// the pull request as its last operation, and its recent executions carry the pull request's URL: what the next session
// reconciles with, instead of opening another.
func TestKillAfterAPullRequest(t *testing.T) {
	const item, url = "o/r#8", "https://github.com/o/r/pull/8"
	home := t.TempDir()
	d := startDaemon(t, home)
	d.call("POST", "/tasks", map[string]any{"yaml": task("opener", "ITEM="+item+" SCENARIO=pullrequest", "")}, 201)
	id := d.call("POST", "/tasks/opener/run", nil, 202)["executionId"].(string)
	// The record is saved before the pull request's event is logged, so once the log has the event, the record on
	// disk names the pull request too.
	d.waitLogUntil(id, func(events []map[string]any) bool {
		for _, e := range events {
			if p, _ := e["payload"].(map[string]any); e["type"] == "pull_request.created" && p["url"] == url {
				return true
			}
		}
		return false
	})
	x := d.call("GET", "/executions/"+id, nil, 200)
	if x["pullRequestUrl"] != url {
		t.Fatalf("the execution's pullRequestUrl is %v once the log has the pull request", x["pullRequestUrl"])
	}
	killHarnessGroup(t, x["executionDir"].(string))
	d.kill()

	d2 := startDaemon(t, home)
	if x := d2.call("GET", "/executions/"+id, nil, 200); x["status"] != "interrupted" || x["pullRequestUrl"] != url {
		t.Fatalf("status %v, pullRequestUrl %v", x["status"], x["pullRequestUrl"])
	}
	run := d2.call("POST", "/tasks/opener/run", nil, 202)
	next := d2.waitStatus(run["executionId"].(string), "completed", "failed", "interrupted")
	if next["status"] != "completed" {
		t.Fatalf("the next execution: %v, error %v", next["status"], next["errorMessage"])
	}
	ctx := readContext(t, next)
	if want := (interruptedEntry{id, item, "pull_request.created " + url}); len(ctx.Interrupted) != 1 ||
		ctx.Interrupted[0] != want {
		t.Errorf("the next context's interruptedExecutions %+v, want [%+v]", ctx.Interrupted, want)
	}
	var recent *recentEntry
	for i := range ctx.Recent {
		if ctx.Recent[i].ExecutionID == id {
			recent = &ctx.Recent[i]
		}
	}
	if recent == nil || recent.Status != "interrupted" || recent.WorkItem != item || recent.PullRequestURL != url {
		t.Errorf("the next context's recentExecutions %+v; want %s interrupted, on %s, with %s", ctx.Recent, id, item, url)
	}
}

// pushRepository makes a local bare repository and a workspace repository on main whose origin it is.
func pushRepository(t *testing.T) (remote, repo string) {
	t.Helper()
	root := t.TempDir()
	remote, repo = filepath.Join(root, "remote.git"), filepath.Join(root, "repo")
	git(t, root, "init", "-q", "--bare", remote)
	git(t, root, "init", "-q", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("A repository.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "-q", "-m", "chore: start")
	git(t, repo, "remote", "add", "origin", remote)
	git(t, repo, "push", "-q", "origin", "main")
	return remote, repo
}

// runUntilPushed runs a task whose session selects o/r#7, commits on a branch of repo, pushes it, and waits. It kills
// the daemon as soon as the push's tool.completed is in the log, and returns the execution and the commit the session
// reported pushing.
func (d *daemon) runUntilPushed(taskID, repo, push string) (id, pushed string) {
	t := d.t
	t.Helper()
	// The repository is outside the home folder, so an allowed folder.
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{repo}}, 200)
	d.call("POST", "/tasks", map[string]any{"yaml": task(taskID, "ITEM=o/r#7 SCENARIO=push",
		"  workspace:\n    path: "+strconv.Quote(repo)+"\n")}, 201)
	id = d.call("POST", "/tasks/"+taskID+"/run", nil, 202)["executionId"].(string)
	d.waitLogUntil(id, func(events []map[string]any) bool {
		var tool any
		for _, e := range events {
			p, _ := e["payload"].(map[string]any)
			switch {
			case e["type"] == "tool.started" && p["command"] == push:
				tool = p["toolId"]
			case e["type"] == "tool.completed" && tool != nil && p["toolId"] == tool && p["status"] == "succeeded":
				output, _ := p["output"].(string)
				pushed = strings.TrimPrefix(output, "pushed ")
				return true
			}
		}
		return false
	})
	d.kill()
	// The daemon is gone, and the stand-in waits where it wrote its child's pid.
	killHarnessGroup(t, filepath.Join(d.home, "runs", id))
	if len(pushed) != 40 {
		t.Fatalf("the push reported %q, not a commit", pushed)
	}
	return id, pushed
}

// git runs git in dir with none of the user's or the system's configuration, and returns its trimmed output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=HarmFlow", "-c", "user.email=harmflow@example.invalid"},
		args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// events returns the execution's log, as GET /executions/{id}/events serves it.
func (d *daemon) events(id string) []map[string]any {
	d.t.Helper()
	list, _ := d.call("GET", "/executions/"+id+"/events", nil, 200)["events"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		m, _ := e.(map[string]any)
		out = append(out, m)
	}
	return out
}

// waitLogUntil polls the execution's log until done holds for the whole of it: unlike waitEvent, the condition can
// name an attempt or a tool, and the wait leaves room for a loaded host and the stand-ins' progress tick.
func (d *daemon) waitLogUntil(id string, done func([]map[string]any) bool) {
	d.t.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if done(d.events(id)) {
			return
		}
	}
	d.t.Fatalf("the log of %s never reached the event awaited:\n%v", id, d.events(id))
}

// interruptedEntry and recentEntry are entries of context.json's interruptedExecutions and recentExecutions.
type interruptedEntry struct {
	ExecutionID   string `json:"executionId"`
	WorkItem      string `json:"workItem"`
	LastOperation string `json:"lastOperation"`
}

type recentEntry struct {
	ExecutionID    string `json:"executionId"`
	Status         string `json:"status"`
	WorkItem       string `json:"workItem"`
	PullRequestURL string `json:"pullRequestUrl"`
}

// readContext reads the context.json the daemon gave the execution's session.
func readContext(t *testing.T, x map[string]any) (ctx struct {
	Interrupted []interruptedEntry `json:"interruptedExecutions"`
	Recent      []recentEntry      `json:"recentExecutions"`
}) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(x["executionDir"].(string), "context.json"))
	if err == nil {
		err = json.Unmarshal(data, &ctx)
	}
	if err != nil {
		t.Fatalf("context.json of %v: %v", x["id"], err)
	}
	return ctx
}

// A firing waiting when the daemon is killed starts after the restart; when its task file was disabled or deleted while
// the daemon was down, it is cancelled instead and never starts.
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

// Firings missed while the daemon was down follow each task's missed-run policy once after it starts again: one
// catch-up, or none. One more restart runs nothing.
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

// Firings survive the daemon being killed. The firing it was running reads interrupted and is not caught up again; the
// one waiting keeps its ID and starts, the task's only new execution; the scheduler's record of the last and next
// firings is what it was before the kill. One more restart runs nothing.
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

// A scheduled task runs from its firing to completed while no client is connected. On the daemon's side, a client that
// connects only afterwards lists the execution, recovers its whole history, and resumes the stream after it.
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
	// have no seconds field, so the firing comes at the next minute boundary, then within a scheduler tick.
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
	// The scheduler fired the occurrence the save announced, on a minute boundary.
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

// A client that closes its stream during an execution stops nothing. The execution goes on to completed with no client
// connected, and GET /health answers throughout from the same daemon.
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

// The daemon outlives the process that started it. A short-lived parent starts harmflowd as the desktop does, in a new
// process group with its standard streams closed. The test ends the parent's whole process group during an execution;
// the daemon keeps answering and completes the execution.
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
	// From running, the execution has the stand-in's ticks scenario left: ten ticks half a second apart, each pause a
	// sleep the stand-in starts, so eleven process starts with its own, then the end of the attempt. The harness checks
	// run before running, outside this wait. On a loaded host each start can take seconds: with a full run and other
	// process starts beside it, the ticks came up to 4.4 seconds apart, and 20 seconds from running did not cover them.
	// The wait gives the pauses their five seconds and each start 10 seconds, as
	// TestFailoverEndsWhatTheFirstAttemptLeft gives its eleven starts.
	const pauses, starts, perStart = 10 * 500 * time.Millisecond, 11, 10 * time.Second
	x := d.waitStatusWithin(pauses+starts*perStart, id, "completed", "failed", "cancelled", "interrupted")
	if x["status"] != "completed" {
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
	watchDaemon(t, pid, true)
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

// A firing that came due while the daemon was down follows the missed-run policy, even when the daemon is back a minute
// later.
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

// The record of misfires skipped per the policy survives the daemon being killed.
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

// throwawayKeychain creates an unlocked keychain file for one test, with no lock timeout, and deletes it at cleanup with
// security delete-keychain, which also takes it off the search list. Its directory's name has a space, as the daemon
// passes the path quoted to security's interactive mode.
func throwawayKeychain(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("the Keychain is macOS's")
	}
	dir := filepath.Join(t.TempDir(), "throwaway keychain")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "harmflow-test.keychain-db")
	runSecurity(t, "create-keychain", "-p", "harmflow-test", path)
	// Registered after t.TempDir, so it runs before the directory is removed, and after the daemons stop.
	t.Cleanup(func() {
		if out, err := exec.Command("/usr/bin/security", "delete-keychain", path).CombinedOutput(); err != nil {
			t.Errorf("security delete-keychain %s: %v %s", path, err, out)
		}
	})
	runSecurity(t, "unlock-keychain", "-p", "harmflow-test", path)
	runSecurity(t, "set-keychain-settings", path) // no lock after a timeout or on sleep
	return path
}

func runSecurity(t *testing.T, args ...string) []byte {
	t.Helper()
	out, err := exec.Command("/usr/bin/security", args...).Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("security %s: %v %s", args[0], err, stderr)
	}
	return out
}

// keychainToken is what a client reads from keychain with security find-generic-password -s com.harmflow.local-api -a
// token -w, byte for byte.
func keychainToken(t *testing.T, keychain string) []byte {
	t.Helper()
	return runSecurity(t, "find-generic-password", "-s", "com.harmflow.local-api", "-a", "token", "-w", keychain)
}

// checkKeychainStore checks that run/daemon.json names the Keychain as the token store, and that no run/token exists.
func checkKeychainStore(t *testing.T, home string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "run", "daemon.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rt struct {
		TokenStore string `json:"tokenStore"`
	}
	if err := json.Unmarshal(data, &rt); err != nil || rt.TokenStore != "keychain" {
		t.Errorf("run/daemon.json names the token store %q (%v), want keychain", rt.TokenStore, err)
	}
	if _, err := os.Stat(filepath.Join(home, "run", "token")); !os.IsNotExist(err) {
		t.Errorf("run/token: %v, want no file", err)
	}
}

// With the default store pointed at a throwaway keychain, the daemon keeps the token it makes on its first start in
// that keychain and writes no run/token. The token security reads from the keychain opens the API; a request without
// it, or with another, gets the guard's 401 (#79).
func TestKeychainTokenOpensTheAPI(t *testing.T) {
	keychain := throwawayKeychain(t)
	home := t.TempDir()
	d := startDaemonOn(t, home, keychain)
	checkKeychainStore(t, home)
	token := strings.TrimSpace(string(keychainToken(t, keychain)))
	if token == "" {
		t.Fatal("the keychain holds an empty token")
	}
	if status, kind, body := d.requestAs(token, "GET", "/health", nil); status != 200 {
		t.Fatalf("GET /health with the keychain's token: %d %s %s", status, kind, body)
	}
	for _, c := range []struct{ name, token string }{{"no Authorization header", ""}, {"another token", token + "x"}} {
		t.Run(c.name, func(t *testing.T) {

			if status, kind, body := d.requestAs(c.token, "GET", "/health", nil); !unauthorized(status, kind, body) {
				t.Errorf("GET /health with %s = %d %s %s, want 401 unauthorized", c.name, status, kind, body)
			}
		})
	}
}

// A restart on the same home and keychain keeps the token byte for byte. A rotation replaces it in the keychain: the
// new token works and the old one gets 401, also after another restart (#79).
func TestKeychainTokenAcrossRestartAndRotation(t *testing.T) {
	keychain := throwawayKeychain(t)
	home := t.TempDir()
	d := startDaemonOn(t, home, keychain)
	stored := keychainToken(t, keychain)
	old := strings.TrimSpace(string(stored))
	// health sends GET /health with a token the client holds, not the one startDaemonOn read again.
	health := func(name, token string, want int) {
		t.Helper()
		status, kind, body := d.requestAs(token, "GET", "/health", nil)
		if want == 401 && !unauthorized(status, kind, body) || want != 401 && status != want {
			t.Errorf("GET /health with the %s token: %d %s %s, want %d", name, status, kind, body, want)
		}
	}
	d.stop()
	d = startDaemonOn(t, home, keychain)
	if again := keychainToken(t, keychain); !bytes.Equal(again, stored) {
		t.Fatalf("the restart changed the keychain's token from %q to %q", stored, again)
	}
	checkKeychainStore(t, home)
	health("first", old, 200)

	if status, kind, body := d.requestAs(old, "POST", "/settings/token/rotate", nil); status != 200 {
		t.Fatalf("POST /settings/token/rotate: %d %s %s", status, kind, body)
	}
	rotated := keychainToken(t, keychain)
	fresh := strings.TrimSpace(string(rotated))
	if fresh == "" || fresh == old {
		t.Fatalf("the keychain holds %q after the rotation, was %q", fresh, old)
	}
	checkKeychainStore(t, home)
	health("rotated", old, 401)
	health("new", fresh, 200)

	d.stop()
	d = startDaemonOn(t, home, keychain)
	if again := keychainToken(t, keychain); !bytes.Equal(again, rotated) {
		t.Fatalf("the restart after the rotation changed the keychain's token from %q to %q", rotated, again)
	}
	health("new", fresh, 200)
	health("rotated", old, 401)
}
