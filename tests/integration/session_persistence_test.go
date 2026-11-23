package integration

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The variables that sign each CLI in on a home of the test's own, so that what a run writes is told apart from what
// the user's own home already holds: Claude Code's long-lived token, the one `claude setup-token` prints, and an API
// key Codex's app server signs in with. The person running the test provides them; the repository never holds them.
const (
	persistenceClaudeTokenEnv = "HARMFLOW_TEST_CLAUDE_TOKEN"
	persistenceCodexKeyEnv    = "HARMFLOW_TEST_CODEX_API_KEY"
)

// Claude Code's --no-session-persistence and the ephemeral param of a Codex thread behave alike when, without it, each
// CLI leaves its session in its home, where it can be read and resumed, and, with it, writes no session that can be
// resumed. Whether the task's ephemeral toggle may also drive Claude Code's flag rests on that, so these two tests
// compare them, each CLI run twice, with its flag and without it, on a temporary home of the test's own. Any other
// file that holds the session's content is listed in the test's log and does not change the outcome.
//
// Each test logs the CLI's version and, for each run, its files, so that its output is the comparison; CI skips them,
// so the pull request that changes them carries that output from a real run. They were written against Claude Code
// 2.1.309, whose help describes its flag, and Codex CLI 0.163.0, whose app server's protocol schema
// (`codex app-server generate-json-schema`) describes the thread's ephemeral param and thread/resume.

// persistenceRun is one run of a CLI on a fresh home: the session it started, the files it left there, and whether the
// session could be resumed afterwards.
type persistenceRun struct {
	session   string   // the session's id, from the CLI's own output
	files     []string // every file under the home, relative to it
	content   []string // the files among them that hold the run's marker
	resumable bool
}

// TestClaudeCodeSessionPersistenceFlag runs the real Claude Code with --no-session-persistence and without it, and
// checks that only the run without it leaves a transcript under projects/ that a later --resume continues.
func TestClaudeCodeSessionPersistenceFlag(t *testing.T) {
	claude := realClaude(t)
	token := os.Getenv(persistenceClaudeTokenEnv)
	if token == "" {
		t.Skipf("signs Claude Code in on a home of the test's own: set %s to a token claude setup-token printed",
			persistenceClaudeTokenEnv)
	}
	t.Logf("claude --version: %s", versionOf(t, nil, claude))
	for _, c := range []struct {
		name string
		flag bool
	}{{"kept", false}, {"no_session_persistence", true}} {
		t.Run(c.name, func(t *testing.T) {
			home := persistenceHome(t)
			config := filepath.Join(home, ".claude")
			env := append(append(os.Environ(), noCredentialEnv()...),
				"HOME="+home, "CLAUDE_CONFIG_DIR="+config, "CLAUDE_CODE_OAUTH_TOKEN="+token)
			marker := "persistence-" + runSuffix()
			args := []string{"-p", "Reply with the single word " + marker + ".", "--output-format", "stream-json",
				"--verbose", "--model", "haiku"}
			if c.flag {
				args = append(args, "--no-session-persistence")
			}
			out := runCLI(t, env, home, claude, args...)
			session := jsonField(out, "session_id")
			if session == "" {
				t.Fatalf("claude %q printed no session_id:\n%s", args, out)
			}
			resume := exec.Command(claude, "-p", "--resume", session, "Reply with the single word ok.",
				"--output-format", "json", "--model", "haiku")
			resume.Env, resume.Dir = env, home
			resumed, err := resume.CombinedOutput()
			r := persistenceRun{session: session, resumable: err == nil}
			r.files, r.content = filesUnder(t, home, marker)
			logPersistence(t, "claude", c.flag, r, resumed)

			transcript := slices.ContainsFunc(r.files, func(f string) bool {
				return strings.HasPrefix(f, ".claude/projects/") && strings.HasSuffix(f, "/"+session+".jsonl")
			})
			switch {
			case !c.flag && (!transcript || !r.resumable):
				t.Errorf("claude without --no-session-persistence: transcript %t, resumable %t; want both", transcript,
					r.resumable)
			case c.flag && r.resumable:
				t.Errorf("claude --no-session-persistence: session %s resumable, want it not", session)
			}
		})
	}
}

// TestCodexEphemeralFlag drives the real Codex's app server, `codex app-server --listen stdio://`, the transport every
// attempt runs on: it starts a thread with the ephemeral param and one without it, each in a server of its own, then
// resumes each through a new server. It checks that only the thread without the param leaves a rollout under
// sessions/ that the resume continues.
func TestCodexEphemeralFlag(t *testing.T) {
	codex := realCodex(t)
	key := os.Getenv(persistenceCodexKeyEnv)
	if key == "" {
		t.Skipf("signs Codex in on a home of the test's own: set %s to an API key", persistenceCodexKeyEnv)
	}
	t.Logf("codex --version: %s", versionOf(t, nil, codex))
	for _, c := range []struct {
		name string
		flag bool
	}{{"kept", false}, {"ephemeral", true}} {
		t.Run(c.name, func(t *testing.T) {
			home := persistenceHome(t)
			env := append(append(os.Environ(), noCredentialEnv()...),
				"HOME="+home, "CODEX_HOME="+filepath.Join(home, ".codex"))
			if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			// The thread runs as an attempt's does, with no approval to wait for, and in a read-only sandbox, since its
			// turn only replies.
			thread := map[string]any{"cwd": home, "approvalPolicy": "never", "sandbox": "read-only"}
			marker := "persistence-" + runSuffix()

			first := startAppServer(t, codex, env, home, key)
			// The sign-in stays in the test's home: an auth file there, and nothing in the keychain.
			if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); err != nil {
				t.Fatalf("account/login/start left no auth file in the test's Codex home: %v", err)
			}
			start := map[string]any{"ephemeral": c.flag}
			for k, v := range thread {
				start[k] = v
			}
			started, err := first.call("thread/start", start)
			if err != nil {
				t.Fatalf("thread/start with ephemeral %t: %v", c.flag, err)
			}
			session := threadID(started)
			if session == "" {
				t.Fatalf("thread/start answered no thread id: %s", started)
			}
			if status := first.turn(session, "Reply with the single word "+marker+"."); status != "completed" {
				t.Fatalf("turn of thread %s ended %q, want completed", session, status)
			}
			first.close()

			// A new server holds no thread in memory, so a resume reads the thread from what the first left on disk.
			second := startAppServer(t, codex, env, home, key)
			resume := map[string]any{"threadId": session}
			for k, v := range thread {
				resume[k] = v
			}
			r := persistenceRun{session: session}
			var said string
			resumed, err := second.call("thread/resume", resume)
			switch {
			case err != nil:
				said = "thread/resume: " + err.Error()
			case threadID(resumed) != session:
				said = fmt.Sprintf("thread/resume answered thread %q, want %s", threadID(resumed), session)
			default:
				status := second.turn(session, "Reply with the single word ok.")
				r.resumable = status == "completed"
				said = "the resumed turn ended " + status
			}
			second.close()
			r.files, r.content = filesUnder(t, home, marker)
			logPersistence(t, "codex", c.flag, r, []byte(said))

			rollout := slices.ContainsFunc(r.files, func(f string) bool {
				return strings.HasPrefix(f, ".codex/sessions/") && strings.HasSuffix(f, session+".jsonl")
			})
			switch {
			case !c.flag && (!rollout || !r.resumable):
				t.Errorf("codex thread without ephemeral: rollout %t, resumable %t (%s); want both", rollout,
					r.resumable, said)
			case c.flag && r.resumable:
				t.Errorf("codex thread with ephemeral: thread %s resumable, want it not", session)
			}
		})
	}
}

// appServerWait bounds each wait for the app server: an answer, a turn's end, or the server's own end.
const appServerWait = 5 * time.Minute

// appServer is one `codex app-server --listen stdio://` a test speaks to over its stdin and stdout, in the JSON lines
// the adapter speaks.
type appServer struct {
	t      *testing.T
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  chan []byte // the server's stdout, one line each, closed once it ends
	stderr strings.Builder
	next   int64
	once   sync.Once
}

// appServerLine is one line the app server writes: an answer (id, result or error), a notification (method, params),
// or a request of its own (id, method, params).
type appServerLine struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// startAppServer starts the real Codex's app server in dir with env, says hello as the adapter does, and signs it in
// with the API key. Codex keeps the key in the auth file of the home env names, never in the keychain, and the test
// never writes the key to its log.
func startAppServer(t *testing.T, codex string, env []string, dir, key string) *appServer {
	t.Helper()
	s := &appServer{t: t, lines: make(chan []byte), next: 1}
	s.cmd = exec.Command(codex, "app-server", "-c", `cli_auth_credentials_store="file"`, "--listen", "stdio://")
	s.cmd.Env, s.cmd.Dir, s.cmd.Stderr = env, dir, &s.stderr
	in, err := s.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cmd.Start(); err != nil {
		t.Fatalf("codex app-server: %v", err)
	}
	s.in = in
	go func() {
		defer close(s.lines)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			s.lines <- slices.Clone(sc.Bytes())
		}
	}()
	t.Cleanup(s.close)
	if _, err := s.call("initialize", map[string]any{
		"clientInfo": map[string]string{"name": "harmflow", "title": "HarmFlow", "version": "0.0.0"}}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	s.send(map[string]any{"method": "initialized"})
	if _, err := s.call("account/login/start", map[string]any{"type": "apiKey", "apiKey": key}); err != nil {
		t.Fatalf("account/login/start with the API key: %v", err)
	}
	return s
}

// send writes one line to the server.
func (s *appServer) send(v any) {
	s.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.in.Write(append(data, '\n')); err != nil {
		s.t.Fatalf("writing to codex app-server: %v", err)
	}
}

// read is the server's next line. It answers a request of the server's own with an error, since the test decides none
// and its thread asks for no approval, and fails the test when the server ends or says nothing within appServerWait.
func (s *appServer) read() appServerLine {
	s.t.Helper()
	timeout := time.NewTimer(appServerWait)
	defer timeout.Stop()
	for {
		select {
		case raw, ok := <-s.lines:
			if !ok {
				s.t.Fatalf("codex app-server ended before its answer")
			}
			var m appServerLine
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			if m.Method != "" && len(m.ID) > 0 {
				s.send(map[string]any{"id": m.ID,
					"error": map[string]any{"code": -32601, "message": "the test answers no request"}})
				continue
			}
			return m
		case <-timeout.C:
			s.t.Fatalf("codex app-server said nothing for %s", appServerWait)
		}
	}
}

// call sends a request and returns its answer's result, or the server's error; the notifications before the answer
// are dropped.
func (s *appServer) call(method string, params any) (json.RawMessage, error) {
	s.t.Helper()
	id := s.next
	s.next++
	s.send(map[string]any{"id": id, "method": method, "params": params})
	for {
		m := s.read()
		if m.Method != "" || string(m.ID) != strconv.FormatInt(id, 10) {
			continue
		}
		if m.Error != nil {
			return nil, errors.New(m.Error.Message)
		}
		return m.Result, nil
	}
}

// turn starts one turn of the thread with text as its input, and returns the status its turn/completed gives, or the
// error that kept it from starting.
func (s *appServer) turn(thread, text string) string {
	s.t.Helper()
	if _, err := s.call("turn/start", map[string]any{"threadId": thread,
		"input": []map[string]any{{"type": "text", "text": text}}}); err != nil {
		return "unstarted: " + err.Error()
	}
	for {
		m := s.read()
		if m.Method != "turn/completed" {
			continue
		}
		var p struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(m.Params, &p) != nil || p.ThreadID != thread {
			continue
		}
		if p.Turn.Error != nil {
			s.t.Logf("turn of thread %s: %s", thread, p.Turn.Error.Message)
		}
		return p.Turn.Status
	}
}

// close ends the server by closing its stdin, as the adapter does, and waits for it; one that has not ended within
// appServerWait is killed.
func (s *appServer) close() {
	s.once.Do(func() {
		_ = s.in.Close()
		kill := time.AfterFunc(appServerWait, func() { _ = s.cmd.Process.Kill() })
		defer kill.Stop()
		for range s.lines {
		}
		if err := s.cmd.Wait(); err != nil {
			s.t.Logf("codex app-server: %v\n%s", err, s.stderr.String())
		}
	})
}

// threadID is the id of the thread an answer of thread/start or thread/resume gives.
func threadID(result json.RawMessage) string {
	var r struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(result, &r)
	return r.Thread.ID
}

// persistenceHome is a fresh home for one run, with its links resolved, as a CLI names its folders by the real path.
func persistenceHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return home
}

// runCLI runs the CLI at path in dir with env, and returns its standard output; it fails the test when the CLI fails.
func runCLI(t *testing.T, env []string, dir, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Env, cmd.Dir = env, dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %q: %v\n%s\n%s", filepath.Base(path), args, err, out, stderr.String())
	}
	return string(out)
}

// jsonField is the first non-empty string value of key among the JSON lines of out.
func jsonField(out, key string) string {
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var line map[string]any
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if v, ok := line[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// filesUnder lists every file under home, relative to it, and the ones among them that hold marker.
func filesUnder(t *testing.T, home, marker string) (files, content []string) {
	t.Helper()
	err := filepath.WalkDir(home, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		files = append(files, rel)
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), marker) {
			content = append(content, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files, content
}

// logPersistence writes what one run left in the test's log, with paths relative to its home, so that the comparison
// can be read from a run's output.
func logPersistence(t *testing.T, cli string, flag bool, r persistenceRun, resumed []byte) {
	t.Helper()
	t.Logf("%s, flag %t: session %s, resumable %t", cli, flag, r.session, r.resumable)
	for _, f := range r.files {
		mark := ""
		if slices.Contains(r.content, f) {
			mark = " (holds the session's content)"
		}
		t.Logf("  %s%s", f, mark)
	}
	if !r.resumable {
		t.Logf("  resume: %s", strings.TrimSpace(string(resumed)))
	}
}
