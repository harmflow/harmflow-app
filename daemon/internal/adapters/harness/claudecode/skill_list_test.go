package claudecode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// listingClaude is a stand-in Claude Code for the skill list: each run appends its folder and arguments to the file
// $LOG, one line each, then runs body. Its folder is read with pwd -P, so a test compares it resolved.
func listingClaude(t *testing.T, body string) (path, log string) {
	t.Helper()
	dir := t.TempDir()
	path, log = filepath.Join(dir, "claude"), filepath.Join(dir, "runs.log")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$(pwd -P)\" \"$*\" >> \"$LOG\"\n" + body
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("os.WriteFile(%s) = %v, want no error", path, err)
	}
	return path, log
}

// runs are the stand-in's runs so far, each its folder and its arguments.
func runs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want no error", log, err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%s) = %v, want no error", path, err)
	}
	return real
}

func names(items []ports.ResolvedItem) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

// The skills a task can cite are the ones Claude Code's init line lists for the workspace and the added folders, sorted
// and each once: its bundled skills and a plugin's among them, and a skill on disk it does not list not found. The
// listing session runs in the workspace, as an execution does, with stream-json output and each added folder, and is
// stopped at the init line, before the model would answer. Agents are still read from the configuration directory.
func TestSkillsComeFromTheInitLine(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeSettings(t, filepath.Join(config, "agents", "reviewer.md"), "---\nname: reviewer\n---\n")
	marker := filepath.Join(t.TempDir(), "answered")
	path, log := listingClaude(t, `cat > /dev/null
echo '{"type":"system","subtype":"hook_started"}'
echo '{"type":"system","subtype":"init","skills":["simplify","review-notes","pdf:pdf","simplify"]}'
sleep 1
touch "$MARKER"
`)
	ws, added := t.TempDir(), t.TempDir()
	writeSettings(t, filepath.Join(ws, ".claude", "skills", "on-disk", "SKILL.md"), "---\nname: on-disk\n---\n")
	a := &Adapter{Home: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "LOG=" + log, "MARKER=" + marker}}
	info := ports.HarnessInfo{Detected: true, Path: path, Features: map[string]bool{"--no-session-persistence": true}}
	ctx := t.Context()

	items, problems, err := a.UserItems(ctx, info, "skill", ws, []string{added})
	if want := []string{"pdf:pdf", "review-notes", "simplify"}; !slices.Equal(names(items), want) || problems != nil || err != nil {
		t.Errorf("UserItems(skill, %s, [%s]) = %q, %v, %v; want %q", ws, added, names(items), problems, err, want)
	}
	want := resolved(t, ws) + "|-p --output-format stream-json --verbose --no-session-persistence --add-dir " + added
	if got := runs(t, log); !slices.Equal(got, []string{want}) {
		t.Errorf("the listing ran as %q, want %q", got, want)
	}
	for _, c := range []struct {
		name  string
		found bool
	}{{"simplify", true}, {"pdf:pdf", true}, {"review-notes", true}, {"on-disk", false}} {
		if _, found, _, err := a.UserItem(ctx, info, "skill", c.name, ws, []string{added}); found != c.found || err != nil {
			t.Errorf("UserItem(skill %s) = %v, %v; want %v", c.name, found, err, c.found)
		}
	}
	if agents, _, err := a.UserItems(ctx, info, "agent", ws, nil); !slices.Equal(names(agents), []string{"reviewer"}) || err != nil {
		t.Errorf("UserItems(agent) = %q, %v; want [reviewer] from the configuration directory", names(agents), err)
	}
	if skill, agent := a.ListsPerWorkspace("skill"), a.ListsPerWorkspace("agent"); !skill || agent {
		t.Errorf("ListsPerWorkspace(skill), ListsPerWorkspace(agent) = %v, %v; want true, false", skill, agent)
	}
	if skill, agent := a.AllowsUnverified("skill"), a.AllowsUnverified("agent"); !skill || agent {
		t.Errorf("AllowsUnverified(skill), AllowsUnverified(agent) = %v, %v; want true, false", skill, agent)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("the listing session wrote %s after its init line, want it stopped there", marker)
	}
}

// A list is kept per workspace and set of added folders, in any order, so a second lookup starts no session; another
// set of folders, another workspace, and a lookup after ReloadUserItems ask again. A workspace not made yet is listed
// in the user's home folder, and an added folder not made yet is left out.
func TestSkillListIsCachedPerWorkspaceAndAddedFolders(t *testing.T) {
	path, log := listingClaude(t, `echo '{"type":"system","subtype":"init","skills":["simplify"]}'`)
	home, ws, other, x, y := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	a := &Adapter{Home: home, Env: []string{"PATH=/usr/bin:/bin", "LOG=" + log}}
	info := ports.HarnessInfo{Detected: true, Path: path}
	ctx := t.Context()
	lookups := []struct {
		workspace string
		added     []string
		runs      int
	}{
		{ws, []string{x, y}, 1},
		{ws, []string{y, x}, 1},
		{ws, []string{x}, 2},
		{ws, []string{x, filepath.Join(x, "not-made")}, 2},
		{other, []string{x}, 3},
		{filepath.Join(ws, "not-made"), nil, 4},
	}
	for _, l := range lookups {
		if _, _, err := a.UserItems(ctx, info, "skill", l.workspace, l.added); err != nil {
			t.Fatalf("UserItems(skill, %s, %q) error %v, want none", l.workspace, l.added, err)
		}
		if got := len(runs(t, log)); got != l.runs {
			t.Errorf("after UserItems(skill, %s, %q), %d listing sessions ran, want %d", l.workspace, l.added, got, l.runs)
		}
	}
	if got := runs(t, log); !strings.HasPrefix(got[len(got)-1], resolved(t, home)+"|") {
		t.Errorf("the listing for a workspace not made ran as %q, want in the home folder %s", got[len(got)-1], home)
	}
	a.ReloadUserItems()
	if _, _, err := a.UserItems(ctx, info, "skill", ws, []string{x, y}); err != nil || len(runs(t, log)) != 5 {
		t.Errorf("UserItems after ReloadUserItems: %v, %d sessions; want a new session", err, len(runs(t, log)))
	}
}

// When Claude Code cannot answer — not detected, ending before its init line with its error or silently, printing an
// init line with no skill list, or reaching the timeout — the lookup fails with an error, never an empty list, and
// nothing is kept, so the next lookup asks again.
func TestSkillListThatClaudeCodeCannotGive(t *testing.T) {
	for _, c := range []struct {
		name, body, wantKey, wantDetail string
		detected                        bool
	}{
		{"not detected", "", "harness_not_detected", "", false},
		{"a failing CLI", "echo 'Not logged in' >&2\nexit 1", "command_failed", "Not logged in", true},
		{"a silent exit", "exit 3", "harness_exit_code", "", true},
		{"no skill list", `echo '{"type":"system","subtype":"init","model":"x"}'`, "claude_skill_list_unrecognized", "", true},
		{"no init line in time", "sleep 30", "claude_skill_list_timed_out", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			path, log := listingClaude(t, c.body)
			a := &Adapter{Home: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "LOG=" + log},
				skillListTimeout: 3 * time.Second}
			info := ports.HarnessInfo{Detected: c.detected, Path: path}
			for i := range 2 {
				items, _, err := a.UserItems(t.Context(), info, "skill", t.TempDir(), nil)
				text := domain.TextOf(err)
				if items != nil || text.Key != c.wantKey || !strings.Contains(text.Params["detail"], c.wantDetail) {
					t.Errorf("UserItems(skill) #%d = %q, %+v; want no list and %s with %q", i+1, names(items), text,
						c.wantKey, c.wantDetail)
				}
				if _, found, _, err := a.UserItem(t.Context(), info, "skill", "simplify", t.TempDir(), nil); found || err == nil {
					t.Errorf("UserItem(skill simplify) #%d = %v, %v; want not found and an error", i+1, found, err)
				}
			}
			if want := map[bool]int{true: 4, false: 0}[c.detected]; len(runs(t, log)) != want {
				t.Errorf("%d listing sessions ran, want %d: a failed list is not kept", len(runs(t, log)), want)
			}
		})
	}
}

// waitBound bounds every wait of the tests of listings in flight; none of them waits for it unless one fails.
const waitBound = 10 * time.Second

// heldClaude is a stand-in Claude Code whose listing in the folder held waits until release is called, then runs then;
// a listing in any other folder prints its init line at once. A test that ends releases it, so no session outlives
// the test.
func heldClaude(t *testing.T, held, then string) (a *Adapter, info ports.HarnessInfo, log string, release func()) {
	t.Helper()
	flag := filepath.Join(t.TempDir(), "released")
	path, log := listingClaude(t, `case "$(pwd -P)" in
"$HELD") until [ -e "$RELEASED" ]; do sleep 0.05; done
`+then+` ;;
esac
echo '{"type":"system","subtype":"init","skills":["simplify"]}'
`)
	a = &Adapter{Home: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "LOG=" + log, "HELD=" + resolved(t, held),
		"RELEASED=" + flag}, skillListTimeout: 30 * time.Second}
	release = func() {
		if err := os.WriteFile(flag, nil, 0o600); err != nil {
			t.Errorf("os.WriteFile(%s) = %v, want no error", flag, err)
		}
	}
	t.Cleanup(release)
	return a, ports.HarnessInfo{Detected: true, Path: path}, log, release
}

// waitForRuns waits until the stand-in has started n listing sessions.
func waitForRuns(t *testing.T, log string, n int) {
	t.Helper()
	for deadline := time.Now().Add(waitBound); len(runs(t, log)) < n; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d listing sessions started within %v, want %d", len(runs(t, log)), waitBound, n)
		}
	}
}

// waitForWaiters waits until n lookups wait for the one listing in flight.
func waitForWaiters(t *testing.T, a *Adapter, n int) {
	t.Helper()
	waiting := func() int {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, l := range a.listings {
			return l.waiters
		}
		return 0
	}
	for deadline := time.Now().Add(waitBound); waiting() != n; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d lookups waited for the listing within %v, want %d", waiting(), waitBound, n)
		}
	}
}

type lookupResult struct {
	items []ports.ResolvedItem
	err   error
}

// lookUp looks up the skills of workspace in a goroutine of its own, which ends once the lookup returns.
func lookUp(ctx context.Context, a *Adapter, info ports.HarnessInfo, workspace string) <-chan lookupResult {
	out := make(chan lookupResult, 1)
	go func() {
		items, _, err := a.UserItems(ctx, info, "skill", workspace, nil)
		out <- lookupResult{items, err}
	}()
	return out
}

// result waits for a lookup to return.
func result(t *testing.T, what string, lookup <-chan lookupResult) lookupResult {
	t.Helper()
	select {
	case r := <-lookup:
		return r
	case <-time.After(waitBound):
		t.Fatalf("%s did not return within %v", what, waitBound)
		return lookupResult{}
	}
}

// A lookup waits only for the listing of its own key: while one workspace's listing is held, another workspace's
// lookup starts its own and returns.
func TestSkillListingOfAnotherWorkspaceRunsAlongside(t *testing.T) {
	defer goleak.VerifyNone(t)
	held, other := t.TempDir(), t.TempDir()
	a, info, log, release := heldClaude(t, held, "")

	slow := lookUp(t.Context(), a, info, held)
	waitForRuns(t, log, 1)
	if r := result(t, "the lookup of another workspace", lookUp(t.Context(), a, info, other)); r.err != nil ||
		!slices.Equal(names(r.items), []string{"simplify"}) {
		t.Errorf("UserItems(skill, %s) = %q, %v while %s is listed; want [simplify]", other, names(r.items), r.err, held)
	}
	select {
	case r := <-slow:
		t.Errorf("UserItems(skill, %s) = %q, %v before its listing was released, want it still waiting", held,
			names(r.items), r.err)
	default:
	}
	release()
	if r := result(t, "the held lookup", slow); r.err != nil || !slices.Equal(names(r.items), []string{"simplify"}) {
		t.Errorf("UserItems(skill, %s) = %q, %v; want [simplify]", held, names(r.items), r.err)
	}
}

// A second lookup of a key shares the listing already running, its failure included: one session answers both, though
// a failed list is not kept.
func TestSkillListingOfOneWorkspaceIsShared(t *testing.T) {
	defer goleak.VerifyNone(t)
	ws := t.TempDir()
	a, info, log, release := heldClaude(t, ws, "echo 'Not logged in' >&2; exit 1")

	first := lookUp(t.Context(), a, info, ws)
	waitForRuns(t, log, 1)
	second := lookUp(t.Context(), a, info, ws)
	waitForWaiters(t, a, 2)
	release()
	for _, c := range []struct {
		what   string
		lookup <-chan lookupResult
	}{{"the first lookup", first}, {"the second lookup", second}} {
		r := result(t, c.what, c.lookup)
		if text := domain.TextOf(r.err); r.items != nil || text.Key != "command_failed" ||
			!strings.Contains(text.Params["detail"], "Not logged in") {
			t.Errorf("%s: UserItems(skill, %s) = %q, %+v; want command_failed with Not logged in", c.what, ws,
				names(r.items), text)
		}
	}
	if got := len(runs(t, log)); got != 1 {
		t.Errorf("%d listing sessions ran for two lookups of one workspace, want 1", got)
	}
}

// A lookup whose context ends returns its context's error at once, and the listing goes on for the lookup that still
// waits for it.
func TestSkillLookupWhoseContextEndsReturns(t *testing.T) {
	defer goleak.VerifyNone(t)
	ws := t.TempDir()
	a, info, log, release := heldClaude(t, ws, "")

	ctx, cancel := context.WithCancel(t.Context())
	gone := lookUp(ctx, a, info, ws)
	waitForRuns(t, log, 1)
	stays := lookUp(t.Context(), a, info, ws)
	waitForWaiters(t, a, 2)
	cancel()
	if r := result(t, "the lookup whose context ended", gone); r.items != nil || !errors.Is(r.err, context.Canceled) {
		t.Errorf("UserItems(skill, %s) with its context cancelled = %q, %v; want %v", ws, names(r.items), r.err,
			context.Canceled)
	}
	release()
	if r := result(t, "the lookup still waiting", stays); r.err != nil || !slices.Equal(names(r.items), []string{"simplify"}) {
		t.Errorf("UserItems(skill, %s) = %q, %v; want [simplify]", ws, names(r.items), r.err)
	}
	if got := len(runs(t, log)); got != 1 {
		t.Errorf("%d listing sessions ran, want 1 that went on after a lookup left it", got)
	}
}

// A listing that no lookup waits for any more is stopped and kept by no one: the next lookup of its key starts its own.
func TestSkillListingNobodyWaitsForStops(t *testing.T) {
	defer goleak.VerifyNone(t)
	ws := t.TempDir()
	a, info, log, release := heldClaude(t, ws, "")

	ctx, cancel := context.WithCancel(t.Context())
	gone := lookUp(ctx, a, info, ws)
	waitForRuns(t, log, 1)
	cancel()
	if r := result(t, "the lookup whose context ended", gone); !errors.Is(r.err, context.Canceled) {
		t.Errorf("UserItems(skill, %s) with its context cancelled = %q, %v; want %v", ws, names(r.items), r.err,
			context.Canceled)
	}
	next := lookUp(t.Context(), a, info, ws)
	waitForRuns(t, log, 2)
	release()
	if r := result(t, "the next lookup", next); r.err != nil || !slices.Equal(names(r.items), []string{"simplify"}) {
		t.Errorf("UserItems(skill, %s) = %q, %v; want [simplify] from a listing of its own", ws, names(r.items), r.err)
	}
}

// ReloadUserItems does not wait for a listing in flight, and that listing, which started before the reload, does not
// keep its list: the next lookup asks Claude Code again.
func TestSkillReloadDoesNotWaitForAListing(t *testing.T) {
	defer goleak.VerifyNone(t)
	ws := t.TempDir()
	a, info, log, release := heldClaude(t, ws, "")

	first := lookUp(t.Context(), a, info, ws)
	waitForRuns(t, log, 1)
	reloaded := make(chan struct{})
	go func() {
		a.ReloadUserItems()
		close(reloaded)
	}()
	select {
	case <-reloaded:
	case <-time.After(waitBound):
		t.Fatalf("ReloadUserItems did not return within %v while a listing was held", waitBound)
	}
	release()
	if r := result(t, "the lookup that started before the reload", first); r.err != nil ||
		!slices.Equal(names(r.items), []string{"simplify"}) {
		t.Errorf("UserItems(skill, %s) = %q, %v; want [simplify]", ws, names(r.items), r.err)
	}
	if _, _, err := a.UserItems(t.Context(), info, "skill", ws, nil); err != nil || len(runs(t, log)) != 2 {
		t.Errorf("UserItems after ReloadUserItems: %v, %d sessions; want a new session", err, len(runs(t, log)))
	}
}
