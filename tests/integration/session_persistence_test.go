package integration

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The variables that sign each CLI in on a home of the test's own, so that what a run writes is told apart from what
// the user's own home already holds: Claude Code's long-lived token, the one `claude setup-token` prints, and an API
// key Codex's exec takes in CODEX_API_KEY. The person running the test provides them; the repository never holds them.
const (
	persistenceClaudeTokenEnv = "HARMFLOW_TEST_CLAUDE_TOKEN"
	persistenceCodexKeyEnv    = "HARMFLOW_TEST_CODEX_API_KEY"
)

// Claude Code's --no-session-persistence and Codex's --ephemeral behave alike when, without its flag, each CLI leaves
// its session in its home, where it can be read and resumed, and, with its flag, writes no session that can be resumed.
// Whether the task's ephemeral toggle may also drive Claude Code's flag rests on that, so these two tests compare them,
// each CLI run twice, with its flag and without it, on a temporary home of the test's own. Any other file that holds
// the session's content is listed in the test's log and does not change the outcome.
//
// Each test logs the CLI's version and, for each run, its files, so that its output is the comparison; CI skips them,
// so the pull request that adds them carries that output from a real run. They were written against Claude Code
// 2.1.309 and Codex CLI 0.163.0, whose help describes the two flags.

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

// TestCodexEphemeralFlag runs the real Codex with --ephemeral and without it, and checks that only the run without it
// leaves a rollout under sessions/ that a later `codex exec resume` continues.
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
				"HOME="+home, "CODEX_HOME="+filepath.Join(home, ".codex"), "CODEX_API_KEY="+key)
			if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			marker := "persistence-" + runSuffix()
			args := []string{"exec", "--json", "--skip-git-repo-check", "--cd", home}
			if c.flag {
				args = append(args, "--ephemeral")
			}
			args = append(args, "Reply with the single word "+marker+".")
			out := runCLI(t, env, home, codex, args...)
			session := jsonField(out, "thread_id")
			if session == "" {
				t.Fatalf("codex %q printed no thread_id:\n%s", args, out)
			}
			resume := exec.Command(codex, "exec", "--json", "--skip-git-repo-check", "--cd", home, "resume", session,
				"Reply with the single word ok.")
			resume.Env, resume.Dir = env, home
			resumed, err := resume.CombinedOutput()
			r := persistenceRun{session: session, resumable: err == nil}
			r.files, r.content = filesUnder(t, home, marker)
			logPersistence(t, "codex", c.flag, r, resumed)

			rollout := slices.ContainsFunc(r.files, func(f string) bool {
				return strings.HasPrefix(f, ".codex/sessions/") && strings.HasSuffix(f, session+".jsonl")
			})
			switch {
			case !c.flag && (!rollout || !r.resumable):
				t.Errorf("codex without --ephemeral: rollout %t, resumable %t; want both", rollout, r.resumable)
			case c.flag && r.resumable:
				t.Errorf("codex --ephemeral: session %s resumable, want it not", session)
			}
		})
	}
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
