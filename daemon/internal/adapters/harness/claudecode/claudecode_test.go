package claudecode

import (
	"bufio"
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

func replay(t *testing.T, file string) (*parser, []ports.HarnessEvent) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := &parser{tools: map[string]toolUse{}}
	var events []ports.HarnessEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		events = append(events, p.Line(sc.Bytes())...)
	}
	return p, events
}

func TestParseSuccess(t *testing.T) {
	p, events := replay(t, "success.jsonl")
	kinds := map[ports.HarnessEventKind]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	if kinds[ports.HarnessStarted] != 1 || kinds[ports.HarnessToolStarted] != 2 || kinds[ports.HarnessToolDone] != 2 ||
		kinds[ports.HarnessFileChanged] != 1 || kinds[ports.HarnessMessage] != 1 {
		t.Fatalf("unexpected events: %v", kinds)
	}
	// The init line names the model the session runs, which the attempt records.
	if e := events[0]; e.Kind != ports.HarnessStarted || e.Model != "claude-haiku-4-5-20251001" || e.Effort != "" {
		t.Errorf("start: %+v", e)
	}
	if events[1].Command != "go test ./..." || events[1].Message != "Run the tests" {
		t.Errorf("tool start: %+v", events[1])
	}
	end := p.Finish(0, "", false, false)
	if end.Class != domain.ClassNone || end.FinalText != "Done: the draft PR is open." || end.SessionID == "" {
		t.Errorf("end: %+v", end)
	}
}

// A rejected plan window is a quota exhaustion, from the stream's own signal.
func TestParseQuota(t *testing.T) {
	p, _ := replay(t, "quota.jsonl")
	if end := p.Finish(1, "", false, false); end.Class != domain.ClassQuotaExhausted {
		t.Errorf("got %s (%s), want quota_exhausted", end.Class, end.Error.Message)
	}
}

// harmflow-app#306: a plan window rejected while the session goes on with extra usage is no exhausted quota, so a
// session that then fails for a reason of its own ends as the error its text gives, and does not fail over. The same
// report without isUsingOverage is an exhausted quota, as before.
func TestFinishReadsAFailedSessionOnExtraUsageAsItsOwnError(t *testing.T) {
	const overage = `,"isUsingOverage":true,"overageInUse":true`
	data, err := os.ReadFile(filepath.Join("testdata", "extra-usage-error.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), overage) {
		t.Fatalf("the fixture's rate-limit report lacks %s", overage)
	}
	cases := []struct {
		name, report, stderr string
		want                 domain.ErrorClass
	}{
		{"on extra usage", overage, "", domain.ClassTaskError},
		{"on extra usage, until it ran out", overage, "You're out of extra usage · resets 9pm", domain.ClassQuotaExhausted},
		{"not on extra usage", `,"isUsingOverage":false,"overageInUse":false`, "", domain.ClassQuotaExhausted},
		{"isUsingOverage absent", "", "", domain.ClassQuotaExhausted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &parser{tools: map[string]toolUse{}}
			for _, line := range strings.Split(strings.ReplaceAll(string(data), overage, c.report), "\n") {
				p.Line([]byte(line))
			}
			if end := p.Finish(1, c.stderr, false, false); end.Class != c.want {
				t.Errorf("Finish(1, %q, false, false).Class = %s (%s), want %s", c.stderr, end.Class, end.Error.Message, c.want)
			}
		})
	}
}

// A signed-out CLI, as Claude Code 2.1.309 printed it with an empty configuration directory, is an
// unavailable harness.
func TestParseNotLoggedIn(t *testing.T) {
	p, _ := replay(t, "not-logged-in.jsonl")
	if end := p.Finish(1, "", false, false); end.Class != domain.ClassHarnessUnavailable {
		t.Errorf("Finish(1, \"\", false, false).Class = %s (%s), want harness_unavailable", end.Class, end.Error.Message)
	}
}

// harmflow-app#212: an unreachable API, as Claude Code 2.1.309 printed it against a closed local port, is a network
// failure.
func TestParseNetwork(t *testing.T) {
	p, _ := replay(t, "network.jsonl")
	if end := p.Finish(1, "", false, false); end.Class != domain.ClassNetwork {
		t.Errorf("Finish(1, \"\", false, false).Class = %s (%s), want network", end.Class, end.Error.Message)
	}
}

// harmflow-app#213: a failover follows only the CLI's own quota and sign-in texts, without the stream's rate-limit
// signal; a task's error that mentions a quota, a limit, or another tool's sign-in is the task's own failure.
func TestFinishClassifiesTheCLIsOwnTexts(t *testing.T) {
	cases := []struct {
		text string
		want domain.ErrorClass
	}{
		{"Claude AI usage limit reached|1792533600", domain.ClassQuotaExhausted},
		{"Usage limit reached · limit resets 3pm", domain.ClassQuotaExhausted},
		{"You've hit your weekly limit · resets Oct 27", domain.ClassQuotaExhausted},
		{"You’ve hit your monthly spend limit.", domain.ClassQuotaExhausted},
		{"Session limit reached ∙ resets 7pm", domain.ClassQuotaExhausted},
		{"Opus weekly limit reached ∙ resets Oct 27 at 10am", domain.ClassQuotaExhausted},
		{"You're out of extra usage · resets 9pm", domain.ClassQuotaExhausted},
		{"Your credit balance is too low to access the Anthropic API.", domain.ClassQuotaExhausted},
		{"Not logged in · Please run /login", domain.ClassHarnessUnavailable},
		{"Not logged in", domain.ClassHarnessUnavailable},
		{"the test fixture's disk quota helper failed: 3 tests failed", domain.ClassTaskError},
		{"retry limit reached while waiting for CI", domain.ClassTaskError},
		{"FAIL TestSessionLimit: the session limit was not enforced", domain.ClassTaskError},
		{"gh: You are not logged into any GitHub hosts. Run gh auth login", domain.ClassTaskError},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			p := &parser{tools: map[string]toolUse{}}
			if end := p.Finish(1, c.text, false, false); end.Class != c.want {
				t.Errorf("Finish(1, %q, false, false).Class = %s, want %s", c.text, end.Class, c.want)
			}
		})
	}
}

// The CLI's own error stays as it printed it, with no key; the daemon's text stands in, with one, when the CLI printed
// nothing, and for a timeout.
func TestFinishKeysOnlyTheDaemonsText(t *testing.T) {
	p := &parser{tools: map[string]toolUse{}}
	if end := p.Finish(2, "Error: boom", false, false); end.Error.Key != "" || end.Error.Message != "Error: boom" {
		t.Errorf("the CLI's error: %+v", end.Error)
	}
	if end := p.Finish(2, "", false, false); end.Error.Key != "harness_exit_code" || end.Error.Params["code"] != "2" ||
		end.Error.Message != "Claude Code exited with code 2" {
		t.Errorf("no output: %+v", end.Error)
	}
	if end := p.Finish(-1, "", true, false); end.Class != domain.ClassTimeout || end.Error.Key != "execution_timed_out" {
		t.Errorf("timeout: %+v", end)
	}
}

// The CLI's error is redacted before the 2000-character tail cuts it; cut first, a secret would keep part of itself,
// too little to match its pattern.
func TestFinishRedactsBeforeTheTail(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	end := (&parser{tools: map[string]toolUse{}}).Finish(2, "Error: "+token+" "+strings.Repeat("y", 1969), false, false)
	if strings.Contains(end.Error.Message, strings.Repeat("a", 20)) || !strings.Contains(end.Error.Message, "[REDACTED]") {
		t.Errorf("the error keeps part of the token: %q", end.Error.Message)
	}
}

func TestParseAuthAndOthers(t *testing.T) {
	p, _ := replay(t, "auth.jsonl")
	if end := p.Finish(1, "", false, false); end.Class != domain.ClassHarnessUnavailable {
		t.Errorf("auth: got %s", end.Class)
	}
	q := &parser{tools: map[string]toolUse{}}
	if end := q.Finish(1, "Error: the tests in issue #429 failed", false, false); end.Class != domain.ClassTaskError {
		t.Errorf("an issue number was taken for a rate limit: %s", end.Class)
	}
	if end := q.Finish(1, "API Error: 529 overloaded_error", false, false); end.Class != domain.ClassRateLimited {
		t.Errorf("overloaded: got %s", end.Class)
	}
	managed := "Your organization requires remote managed settings to load, but they could not be loaded. Run `claude auth login` to re-authenticate"
	if end := q.Finish(1, managed, false, false); end.Class != domain.ClassHarnessUnavailable {
		t.Errorf("managed settings: got %s", end.Class)
	}
	if end := q.Finish(143, "", false, true); end.Class != domain.ClassCancelled {
		t.Errorf("cancel: got %s", end.Class)
	}
	if end := q.Finish(143, "", true, false); end.Class != domain.ClassTimeout {
		t.Errorf("timeout: got %s", end.Class)
	}
}

// Each profile is translated to Claude Code's own flags, and what they cannot confine is named. workspace_write gives
// the editing tools no allow rule (harmflow-app#287): a bare one would allow them on any path.
func TestPrepareTranslatesPermissions(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "") // the user's settings are read from Home, which has none
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{"--no-session-persistence": true}}
	perm := domain.TaskDocument{}.Effective().Spec.Permissions
	exec := t.TempDir()

	ro := *perm
	ro.Profile = domain.ProfileReadOnly
	inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: ro, ExecutionDir: exec, Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(inv.Args, " ")
	if !strings.Contains(args, "--permission-mode dontAsk") || strings.Contains(args, "Bash") {
		t.Errorf("read_only args: %s", args)
	}
	for _, r := range inv.Restrictions {
		if !r.Enforced {
			t.Errorf("read_only should be fully enforced, %s is not", r.Name)
		}
	}
	if inv.Stdin != "hi" {
		t.Error("the prompt goes through stdin")
	}

	ww := *perm
	ww.Network = domain.NetworkDeny
	inv, _, _ = a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: ww, ExecutionDir: exec, AdditionalDirs: []string{"/repo"}})
	args = strings.Join(inv.Args, " ")
	if !strings.Contains(args, "--permission-mode acceptEdits") || !strings.Contains(args, "--add-dir /repo") ||
		!strings.Contains(args, "--disallowedTools WebFetch,WebSearch") {
		t.Errorf("workspace_write args: %s", args)
	}
	i := slices.Index(inv.Args, "--allowedTools")
	if i < 0 || i+1 == len(inv.Args) {
		t.Fatalf("workspace_write args = %s, want --allowedTools", args)
	}
	allowedTools := strings.Split(inv.Args[i+1], ",")
	for _, tool := range []string{"Edit", "Write", "MultiEdit", "NotebookEdit"} {
		if slices.Contains(allowedTools, tool) {
			t.Errorf("workspace_write --allowedTools = %s, want no bare %s rule", inv.Args[i+1], tool)
		}
	}
	if !slices.Contains(allowedTools, "Bash") || !slices.Contains(allowedTools, "Read") {
		t.Errorf("workspace_write --allowedTools = %s, want the reading tools and Bash", inv.Args[i+1])
	}
	if r, _ := restriction(inv.Restrictions, "edit_directories"); !r.Enforced {
		t.Errorf("edit_directories = %+v, want enforced with no settings files", r)
	}
	unenforced := map[string]bool{}
	for _, r := range inv.Restrictions {
		if !r.Enforced {
			unenforced[r.Name] = true
		}
	}
	if !unenforced["restrictToWorkspace"] || !unenforced["network"] {
		t.Errorf("the shell's freedom must be warned: %v", inv.Restrictions)
	}
}

// harmflow-app#211: Claude Code merges the allow rules of every settings source into a custom profile's tool list. A
// rule the task's allowed and denied lists do not carry, in a settings file HarmFlow can read, leaves the list not
// enforced, and the warning names the source; validation reads only the user's settings. A rule counts as carried only
// when it is identical to an entry, and a file that cannot be read or parsed widens the list.
func TestPrepareReportsAllowRulesThatWidenTheToolList(t *testing.T) {
	const bashAll = `{"permissions":{"allow":["Bash(*)"]}}`
	const python = `{"permissions":{"allow":["Bash(python3:*)"]}}`
	const dir = "<dir>" // a folder where the file would be: it exists, but cannot be read
	allowed := []string{"Read", "Bash(jq:*)"}
	cases := []struct {
		name                 string
		user, project, local string // a settings file's content; "" for no file
		allowed, denied      []string
		networkDenied        bool
		policyCheck          bool
		want                 string // the sources param of a list not enforced; "" for an enforced list
	}{
		{name: "a user rule at validation", user: bashAll, allowed: allowed, policyCheck: true, want: "user"},
		{name: "a user rule at an execution", user: bashAll, allowed: allowed, want: "user"},
		{name: "a project rule at an execution", project: python, allowed: allowed, want: "project"},
		{name: "a local rule at an execution", local: python, allowed: allowed, want: "local"},
		{name: "a project rule at validation", project: bashAll, local: bashAll, allowed: allowed, policyCheck: true},
		{name: "user and local rules", user: bashAll, local: python, allowed: allowed, want: "user_local"},
		{name: "rules in every source", user: bashAll, project: python, local: python, allowed: allowed,
			want: "user_project_local"},
		{name: "rules identical to allowed entries", user: `{"permissions":{"allow":["Bash(jq:*)","Read"]}}`,
			project: `{"permissions":{"allow":["Read"]}}`, allowed: allowed},
		{name: "a rule identical to a denied entry", user: `{"permissions":{"allow":["Bash(rm:*)"]}}`, allowed: allowed,
			denied: []string{"Bash(rm:*)"}},
		{name: "a web tool the denied network removes", local: `{"permissions":{"allow":["WebFetch"]}}`, allowed: allowed,
			networkDenied: true},
		{name: "a web tool the network allows", local: `{"permissions":{"allow":["WebFetch"]}}`, allowed: allowed,
			want: "local"},
		{name: "a reading tool of the default list", user: `{"permissions":{"allow":["Grep"]}}`},
		{name: "a rule a pattern of the list would cover", user: `{"permissions":{"allow":["Bash(git status:*)"]}}`,
			allowed: []string{"Bash(git:*)"}, want: "user"},
		{name: "no settings files", allowed: allowed},
		{name: "settings without allow rules", user: `{"model":"opus","permissions":{"deny":["Bash(*)"]}}`,
			project: `{}`, allowed: allowed},
		{name: "user settings that are not JSON", user: `{"permissions":`, allowed: allowed, want: "user"},
		{name: "local settings that are not JSON", local: `not json`, allowed: allowed, want: "local"},
		{name: "allow rules that are not a list", user: `{"permissions":{"allow":"Bash(*)"}}`, allowed: allowed,
			want: "user"},
		{name: "project settings that cannot be read", project: dir, allowed: allowed, want: "project"},
	}
	where := map[string]string{"user": "user settings", "project": "project settings", "local": "local settings",
		"user_local": "user and local settings", "user_project_local": "user, project, and local settings"}
	info := ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config, work := t.TempDir(), t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", config)
			writeSettings(t, filepath.Join(config, "settings.json"), c.user)
			writeSettings(t, filepath.Join(work, ".claude", "settings.json"), c.project)
			writeSettings(t, filepath.Join(work, ".claude", "settings.local.json"), c.local)
			perm := *domain.TaskDocument{}.Effective().Spec.Permissions
			perm.Profile = domain.ProfileCustom
			perm.Custom.AllowedTools, perm.Custom.DeniedTools = c.allowed, c.denied
			if c.networkDenied {
				perm.Network = domain.NetworkDeny
			}
			a := &Adapter{Home: t.TempDir()}
			inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: work, Permissions: perm, ExecutionDir: t.TempDir(),
				PolicyCheck: c.policyCheck})
			if err != nil {
				t.Fatal(err)
			}
			r, ok := restriction(inv.Restrictions, "tool_allowlist")
			switch {
			case !ok:
				t.Fatalf("Prepare restrictions = %+v, want one named tool_allowlist", inv.Restrictions)
			case c.want == "":
				if !r.Enforced || strings.Contains(r.Text.Message, "allows only") {
					t.Errorf("tool_allowlist = %+v, want enforced, with a text that does not claim only the listed tools run", r)
				}
			case r.Enforced || r.Text.Key != "restriction_claude_allow_rules" || r.Text.Params["sources"] != c.want:
				t.Errorf("tool_allowlist = %+v, want not enforced, restriction_claude_allow_rules naming %s", r, c.want)
			case !strings.Contains(r.Text.Message, "its "+where[c.want]+" ") ||
				!strings.Contains(r.Text.Message, "managed settings are not checked"):
				t.Errorf("tool_allowlist text = %q, want it to name the %s and say managed settings are not checked",
					r.Text.Message, where[c.want])
			}
		})
	}
}

// The user's settings are read from CLAUDE_CONFIG_DIR when it is set, as the user's agents and skills are, and from
// .claude in the home folder otherwise.
func TestPrepareReadsTheUserSettingsOfTheConfigurationDirectory(t *testing.T) {
	a := &Adapter{Home: t.TempDir()}
	writeSettings(t, filepath.Join(a.Home, ".claude", "settings.json"), `{"permissions":{"allow":["Bash(*)"]}}`)
	info := ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{}}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	perm.Profile = domain.ProfileCustom
	perm.Custom.AllowedTools = []string{"Read"}
	for _, c := range []struct {
		name, configDir string
		enforced        bool
	}{
		{"home", "", false},
		{"CLAUDE_CONFIG_DIR", t.TempDir(), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", c.configDir)
			inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: t.TempDir(), Permissions: perm, PolicyCheck: true})
			if err != nil {
				t.Fatal(err)
			}
			if r, _ := restriction(inv.Restrictions, "tool_allowlist"); r.Enforced != c.enforced {
				t.Errorf("tool_allowlist = %+v, want Enforced %t", r, c.enforced)
			}
		})
	}
}

// harmflow-app#287: under workspace_write, an editing allow rule in a settings source HarmFlow can read lifts the limit
// on file edits when it may reach outside the workspace and the added directories, and the warning names the source;
// validation reads only the user's settings, against the same directories (harmflow-app#427). Only a rule whose path
// lies within those directories stays inside: a bare rule, one Claude Code could read another way, and a file that
// cannot be read or parsed do not. The workspace is given resolved, as the daemon gives it, and a rule's path is
// resolved before the two are compared, so a rule naming the workspace through a link stays inside and one naming a
// folder outside through a link does not (harmflow-app#449).
func TestPrepareReportsEditRulesThatReachOutside(t *testing.T) {
	const bareWrite = `{"permissions":{"allow":["Write"]}}`
	const dir = "<dir>" // a folder where the file would be: it exists, but cannot be read
	cases := []struct {
		name                 string
		user, project, local string // settings content; <work>, <added>, <exec>, <link> name the folders; "" for no file
		policyCheck          bool
		want                 string // the sources param of edits not limited; "" for limited edits
	}{
		{name: "a Write rule outside the workspace", user: `{"permissions":{"allow":["Write(//etc/**)"]}}`, want: "user"},
		{name: "a bare Edit rule", project: `{"permissions":{"allow":["Read","Edit"]}}`, want: "project"},
		{name: "settings that are not JSON", local: `{"permissions":`, want: "local"},
		{name: "a rule within the workspace", user: `{"permissions":{"allow":["Edit(/<work>/src/**)"]}}`},
		{name: "rules within an added directory",
			user: `{"permissions":{"allow":["Write(/<added>/**)","MultiEdit(/<added>/docs/*.md)","Edit(~/repo/**)"]}}`},
		{name: "a rule within the execution folder", local: `{"permissions":{"allow":["Edit(/<exec>/**)"]}}`},
		{name: "no settings files"},
		{name: "paths from the working directory",
			project: `{"permissions":{"allow":["Edit(src/**)","Write(./*.go)","NotebookEdit(**)","Edit(docs/../README.md)"]}}`},
		{name: "a path from the working directory that climbs out", project: `{"permissions":{"allow":["Edit(../other/**)"]}}`,
			want: "project"},
		{name: "a climb after a wildcard", local: `{"permissions":{"allow":["Edit(src/*/../../../x)"]}}`, want: "local"},
		{name: "a folder beside the workspace", user: `{"permissions":{"allow":["Edit(/<work>-other/**)"]}}`, want: "user"},
		{name: "a rule on any path", user: `{"permissions":{"allow":["Edit(*)"]}}`, want: "user"},
		{name: "a home folder outside the added directories", user: `{"permissions":{"allow":["Edit(~/notes/**)"]}}`,
			want: "user"},
		{name: "another user's home folder", user: `{"permissions":{"allow":["Edit(~root/**)"]}}`, want: "user"},
		{name: "a / path in the workspace's settings", project: `{"permissions":{"allow":["Edit(/src/**)"]}}`},
		{name: "a / path in the user's settings", user: `{"permissions":{"allow":["Edit(/src/**)"]}}`, want: "user"},
		{name: "rules of other tools", user: `{"permissions":{"allow":["Bash(*)","Read(//etc/**)","WebFetch"]}}`},
		{name: "rules in every source", user: bareWrite, project: bareWrite, local: bareWrite, want: "user_project_local"},
		{name: "project settings that cannot be read", project: dir, want: "project"},
		{name: "a rule within the workspace at validation", user: `{"permissions":{"allow":["Edit(/<work>/src/**)"]}}`,
			policyCheck: true},
		{name: "a rule within an added directory at validation", user: `{"permissions":{"allow":["Write(/<added>/**)"]}}`,
			policyCheck: true},
		{name: "a rule outside the workspace at validation", user: `{"permissions":{"allow":["Write(//etc/**)"]}}`,
			policyCheck: true, want: "user"},
		{name: "a path from the working directory at validation", user: `{"permissions":{"allow":["Edit(src/**)"]}}`,
			policyCheck: true},
		{name: "project and local rules at validation", project: bareWrite, local: bareWrite, policyCheck: true},
		// <link> and ~/link lead to the workspace, ~/out to a folder outside it and the added directories.
		{name: "rules naming the workspace through a link",
			user: `{"permissions":{"allow":["Write(/<link>/**)","Edit(~/link/src/**)"]}}`},
		{name: "a rule naming a folder outside through a link", user: `{"permissions":{"allow":["Edit(~/out/**)"]}}`,
			want: "user"},
		{name: "rules naming the workspace through a link at validation",
			user: `{"permissions":{"allow":["Write(/<link>/**)","Edit(~/link/src/**)"]}}`, policyCheck: true},
		{name: "a rule naming a folder outside through a link at validation",
			user: `{"permissions":{"allow":["Edit(~/out/**)"]}}`, policyCheck: true, want: "user"},
	}
	where := map[string]string{"user": "user settings", "project": "project settings", "local": "local settings",
		"user_project_local": "user, project, and local settings"}
	info := ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config, home, exec := t.TempDir(), t.TempDir(), t.TempDir()
			work, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			added, link := filepath.Join(home, "repo"), filepath.Join(home, "link")
			if err := os.Symlink(work, link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Join(home, "out")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLAUDE_CONFIG_DIR", config)
			folders := strings.NewReplacer("<work>", work, "<added>", added, "<exec>", exec, "<link>", link)
			writeSettings(t, filepath.Join(config, "settings.json"), folders.Replace(c.user))
			writeSettings(t, filepath.Join(work, ".claude", "settings.json"), folders.Replace(c.project))
			writeSettings(t, filepath.Join(work, ".claude", "settings.local.json"), folders.Replace(c.local))
			perm := *domain.TaskDocument{}.Effective().Spec.Permissions
			perm.Profile = domain.ProfileWorkspaceWrite
			a := &Adapter{Home: home}
			inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: work, AdditionalDirs: []string{added}, Permissions: perm,
				ExecutionDir: exec, PolicyCheck: c.policyCheck})
			if err != nil {
				t.Fatal(err)
			}
			r, ok := restriction(inv.Restrictions, "edit_directories")
			switch {
			case !ok:
				t.Fatalf("Prepare restrictions = %+v, want one named edit_directories", inv.Restrictions)
			case c.want == "":
				if !r.Enforced {
					t.Errorf("edit_directories = %+v, want enforced", r)
				}
			case r.Enforced || r.Text.Key != "restriction_claude_edit_rules" || r.Text.Params["sources"] != c.want:
				t.Errorf("edit_directories = %+v, want not enforced, restriction_claude_edit_rules naming %s", r, c.want)
			case !strings.Contains(r.Text.Message, "its "+where[c.want]+" ") ||
				!strings.Contains(r.Text.Message, "managed settings are not checked"):
				t.Errorf("edit_directories text = %q, want it to name the %s and say managed settings are not checked",
					r.Text.Message, where[c.want])
			}
		})
	}
}

// Only the custom profile hands Claude Code a list for the user's rules to widen: read_only and workspace_write report
// no tool list, whatever rules the settings hold.
func TestPrepareChecksAllowRulesOnlyForTheCustomProfile(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeSettings(t, filepath.Join(config, "settings.json"), `{"permissions":{"allow":["Bash(*)"]}}`)
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{}}
	for _, profile := range []string{domain.ProfileReadOnly, domain.ProfileWorkspaceWrite} {
		perm := *domain.TaskDocument{}.Effective().Spec.Permissions
		perm.Profile = profile
		inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: t.TempDir(), Permissions: perm})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range inv.Restrictions {
			if r.Name == "tool_allowlist" || r.Text.Key == "restriction_claude_allow_rules" {
				t.Errorf("%s: Prepare restrictions = %+v, want none about the tool list", profile, inv.Restrictions)
			}
		}
	}
}

// A user's skill is found in Claude Code's configuration directory for every workspace, as before, and in the
// workspace's .claude/skills for that workspace only. A name in both is the configuration directory's, listed once, as
// Claude Code runs a personal skill over a project's. Agents are read from the configuration directory alone.
func TestUserSkillsComeFromTheConfigurationAndTheWorkspace(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	ws, other := t.TempDir(), t.TempDir()
	writeSettings(t, filepath.Join(config, "skills", "review-notes", "SKILL.md"), "---\nname: review-notes\ndescription: Mine.\n---\n")
	writeSettings(t, filepath.Join(config, "skills", "shared", "SKILL.md"), "---\nname: shared\ndescription: The user's.\n---\n")
	project := filepath.Join(ws, ".claude", "skills")
	writeSettings(t, filepath.Join(project, "release-notes", "SKILL.md"), "---\nname: release-notes\ndescription: The project's.\n---\n")
	writeSettings(t, filepath.Join(project, "shared", "SKILL.md"), "---\nname: shared\ndescription: The project's.\n---\n")
	writeSettings(t, filepath.Join(ws, ".claude", "agents", "project-agent.md"), "---\nname: project-agent\n---\n")
	a := &Adapter{Home: t.TempDir()}
	ctx := t.Context()

	for _, c := range []struct {
		name, workspace string
		want            []string
	}{
		{"the workspace with project skills", ws, []string{"release-notes", "review-notes", "shared"}},
		{"another workspace", other, []string{"review-notes", "shared"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			items, problems, err := a.UserItems(ctx, ports.HarnessInfo{}, "skill", c.workspace)
			var names []string
			for _, it := range items {
				names = append(names, it.Name)
				if it.Name == "shared" && (it.Path != filepath.Join(config, "skills", "shared") || it.Description != "The user's.") {
					t.Errorf("UserItems(skill, %s) shared = %+v, want the configuration directory's", c.workspace, it)
				}
			}
			if !slices.Equal(names, c.want) || problems != nil || err != nil {
				t.Errorf("UserItems(skill, %s) = %q, %v, %v; want %q", c.workspace, names, problems, err, c.want)
			}
		})
	}

	for _, c := range []struct {
		name, kind, item, workspace, wantPath string
	}{
		{"a project skill in its workspace", "skill", "release-notes", ws, filepath.Join(project, "release-notes")},
		{"a project skill in another workspace", "skill", "release-notes", other, ""},
		{"a configuration skill in any workspace", "skill", "review-notes", other, filepath.Join(config, "skills", "review-notes")},
		{"a shared name", "skill", "shared", ws, filepath.Join(config, "skills", "shared")},
		{"a project agent", "agent", "project-agent", ws, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			it, found, problems, err := a.UserItem(ctx, ports.HarnessInfo{}, c.kind, c.item, c.workspace)
			if found != (c.wantPath != "") || it.Path != c.wantPath || problems != nil || err != nil {
				t.Errorf("UserItem(%s %s, %s) = %+v, %v, %v, %v; want found at %q", c.kind, c.item, c.workspace, it, found,
					problems, err, c.wantPath)
			}
		})
	}
	if agents, _, _ := a.UserItems(ctx, ports.HarnessInfo{}, "agent", ws); len(agents) != 0 {
		t.Errorf("UserItems(agent, %s) = %+v, want none: the workspace's agents are not read", ws, agents)
	}
	if skill, agent := a.ReadsWorkspace("skill"), a.ReadsWorkspace("agent"); !skill || agent {
		t.Errorf("ReadsWorkspace(skill), ReadsWorkspace(agent) = %v, %v; want true, false", skill, agent)
	}
}

func restriction(rs []ports.Restriction, name string) (ports.Restriction, bool) {
	for _, r := range rs {
		if r.Name == name {
			return r, true
		}
	}
	return ports.Restriction{}, false
}

// writeSettings writes a settings file, or a folder in its place for "<dir>"; empty content writes nothing.
func writeSettings(t *testing.T, path, content string) {
	t.Helper()
	if content == "" {
		return
	}
	if content == "<dir>" {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Built-in skills are delivered as a plugin of the execution; built-in agents are defined for the session, as
// subagents: no agent is the session's main one, so the task's prompt runs as Claude Code's default agent.
func TestPrepareDeliversBuiltins(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "skills", "hf-x")
	_ = os.MkdirAll(skill, 0o700)
	_ = os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: hf-x\ndescription: d\n---\nbody"), 0o600)
	agent := filepath.Join(root, "agents", "hf-a.md")
	_ = os.MkdirAll(filepath.Dir(agent), 0o700)
	_ = os.WriteFile(agent, []byte("---\nname: hf-a\ndescription: an agent\n---\nYou implement."), 0o600)
	exec := t.TempDir()
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{"--agents": true}}
	inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", ExecutionDir: exec, Prompt: "Review the change.",
		Permissions: *domain.TaskDocument{}.Effective().Spec.Permissions,
		Skills:      []ports.ResolvedItem{{Name: "hf-x", Kind: "skill", Builtin: true, Path: skill}},
		Agents: []ports.ResolvedItem{{Name: "hf-a", Kind: "agent", Builtin: true, Path: agent},
			{Name: "tester", Kind: "agent", Path: filepath.Join(root, "user", "tester.md")}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(exec, "claude-plugin", "skills", "hf-x", "SKILL.md")); err != nil {
		t.Errorf("skill not delivered: %v", err)
	}
	args := strings.Join(inv.Args, " ")
	if !strings.Contains(args, "--plugin-dir "+filepath.Join(exec, "claude-plugin")) ||
		!strings.Contains(args, `"prompt":"You implement."`) {
		t.Errorf("Prepare args = %s, want --plugin-dir %s and the built-in agent defined with its prompt", args,
			filepath.Join(exec, "claude-plugin"))
	}
	if slices.Contains(inv.Args, "--agent") {
		t.Errorf("Prepare args = %s, want no --agent", args)
	}
	if want := "Review the change."; inv.Stdin != want {
		t.Errorf("Prepare stdin = %q, want %q, the task's prompt", inv.Stdin, want)
	}
	if _, err := os.Stat(filepath.Join(a.Home, ".claude")); err == nil {
		t.Error("the user's configuration was touched")
	}
}

// A task's model and effort reach Claude Code as --model and --effort. A task that sets neither, or sets default,
// passes neither flag, so the CLI's own default applies.
func TestPrepareModelAndEffort(t *testing.T) {
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{"--model": true, "--effort": true}}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	cases := []struct {
		name, model, effort   string
		wantModel, wantEffort string // "" for a flag that must be absent
	}{
		{"set", "sonnet", "high", "sonnet", "high"},
		{"default", domain.DefaultModel, domain.DefaultEffort, "", ""},
		{"unset", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: perm, ExecutionDir: t.TempDir(),
				Prompt: "hi", Model: c.model, Effort: c.effort})
			if err != nil {
				t.Fatal(err)
			}
			for flag, want := range map[string]string{"--model": c.wantModel, "--effort": c.wantEffort} {
				got, passed := flagValue(inv.Args, flag)
				switch {
				case want == "" && passed:
					t.Errorf("%s %q was passed: %q", flag, got, inv.Args)
				case want != "" && got != want:
					t.Errorf("%s %q (passed %t), want %q: %q", flag, got, passed, want, inv.Args)
				}
			}
		})
	}
}

// flagValue is the argument that follows flag, and whether flag was passed at all.
func flagValue(args []string, flag string) (string, bool) {
	for i, arg := range args {
		if arg == flag {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

// The Costs tab (harmflow-app#39), on a real Claude Code 2.1.239 run: the messages count once each while it runs,
// and the result line's modelUsage settles the output and the cost the CLI printed.
func TestUsageFromTheResultLine(t *testing.T) {
	p, events := replay(t, "usage.jsonl")
	var live []*domain.Usage
	for _, e := range events {
		if e.Kind == ports.HarnessUsage {
			live = append(live, e.Usage)
		}
	}
	// Three messages, each repeated on two or three lines: one update per message.
	if len(live) != 3 {
		t.Fatalf("%d live updates, want 3", len(live))
	}
	last := live[2]
	want := domain.Tokens{Input: 25, CacheWriteLong: 12752, CacheRead: 65425}
	if last.Status != domain.UsagePartial || len(last.Models) != 1 || last.Models[0].Tokens != want || last.CostUSD != nil {
		t.Errorf("live usage: %+v", last)
	}

	u := p.Finish(0, "", false, false).Usage
	want.Output = 866
	if u.Status != domain.UsageReported || u.ModelSource != domain.ModelFromHarness || len(u.Models) != 1 {
		t.Fatalf("final usage: %+v", u)
	}
	m := u.Models[0]
	if m.Model != "claude-haiku-4-5-20251001" || m.Tokens != want {
		t.Errorf("model usage: %+v", m)
	}
	if u.CostSource != domain.CostReported || u.CostUSD == nil || *u.CostUSD != 0.0364015 || u.PricesAsOf != Prices.AsOf {
		t.Errorf("cost: %+v", u)
	}
	// The price table reproduces the CLI's figure: it gives the split by token class.
	if m.Price == nil || math.Abs(m.Price.Cost(m.Tokens)-0.0364015) > 1e-9 {
		t.Errorf("the table's estimate for %+v is not the CLI's $0.0364015", m)
	}
}

// A cancelled attempt has no result line: its input and cache counts are partial, without output or cost.
func TestUsageOfACancelledAttempt(t *testing.T) {
	p, _ := replay(t, "cancelled.jsonl")
	u := p.Finish(143, "", false, true).Usage
	if u.Status != domain.UsagePartial || len(u.Models) != 1 || u.CostUSD != nil || u.CostSource != "" {
		t.Fatalf("usage: %+v", u)
	}
	if got := u.Models[0].Tokens; got != (domain.Tokens{Input: 9, CacheWriteLong: 11511, CacheRead: 14129}) {
		t.Errorf("tokens: %+v", got)
	}
	// A CLI that printed no usage at all reports none, which is not zero.
	q, _ := replay(t, "success.jsonl")
	if u := q.Finish(0, "", false, false).Usage; u.Status != domain.UsageNotReported || len(u.Models) != 0 {
		t.Errorf("no usage: %+v", u)
	}
	// An error's result line that counts nothing does not erase what the messages counted.
	p.Line([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"limit","total_cost_usd":0,` +
		`"usage":{"input_tokens":0,"output_tokens":0},"modelUsage":{}}`))
	if u := p.Finish(1, "", false, false).Usage; u.Status != domain.UsagePartial || u.Models[0].Tokens.CacheRead != 14129 {
		t.Errorf("an empty result line: %+v", u)
	}
}

// modelUsage keys each model by its full id; their cache writes are split by the result line's lifetimes, and a model
// the table has no price for keeps the cost the CLI reported for it.
func TestUsageAcrossModels(t *testing.T) {
	prices := domain.PriceTable{AsOf: "2026-01-02", Prices: map[string]domain.Price{
		"claude-main-1": {Input: 3, CacheWriteShort: 3.75, CacheWriteLong: 6, CacheRead: 0.3, Output: 15}}}
	p := &parser{tools: map[string]toolUse{}, prices: prices}
	for _, l := range []string{
		`{"type":"system","subtype":"init","session_id":"s","model":"claude-main-1-20260101"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"ok","total_cost_usd":0.5,
		  "usage":{"input_tokens":10,"cache_creation_input_tokens":4000,"cache_read_input_tokens":0,"output_tokens":10,
		    "cache_creation":{"ephemeral_5m_input_tokens":1000,"ephemeral_1h_input_tokens":3000}},
		  "modelUsage":{
		    "claude-aux-1":{"inputTokens":900,"outputTokens":40,"cacheReadInputTokens":0,"cacheCreationInputTokens":400,"costUSD":0.01},
		    "claude-main-1-20260101":{"inputTokens":10,"outputTokens":500,"cacheReadInputTokens":2000,"cacheCreationInputTokens":4000,"costUSD":0.49}}}`,
	} {
		p.Line([]byte(strings.ReplaceAll(l, "\n", "")))
	}
	u := p.Finish(0, "", false, false).Usage
	if len(u.Models) != 2 || u.Models[0].Model != "claude-main-1-20260101" || u.Models[1].Model != "claude-aux-1" {
		t.Fatalf("the session's model comes first: %+v", u.Models)
	}
	main, aux := u.Models[0], u.Models[1]
	if main.Tokens.CacheWriteLong != 3000 || main.Tokens.CacheWriteShort != 1000 || aux.Tokens.CacheWriteLong != 300 ||
		aux.Tokens.CacheWriteShort != 100 {
		t.Errorf("cache writes by lifetime: %+v %+v", main.Tokens, aux.Tokens)
	}
	if main.Price == nil || aux.Price != nil || aux.CostUSD == nil || *aux.CostUSD != 0.01 {
		t.Errorf("prices: %+v %+v", main, aux)
	}
	if u.CostSource != domain.CostReported || *u.CostUSD != 0.5 {
		t.Errorf("the CLI's total is the attempt's cost: %+v", u)
	}
}

// standIn writes a stand-in for the claude CLI whose --version and `auth status` run the given shell commands, and
// whose --help lists every flag the adapter needs.
func standIn(t *testing.T, version, auth string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/bash
case "$1" in
  --version) ` + version + ` ;;
  --help) echo "-p, --print  --output-format <format>  --permission-mode <mode>  --allowedTools <tools...>  --append-system-prompt <prompt>" ;;
  auth) ` + auth + ` ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The supported range is written in release lines: 2.0 is the minimum, 2.1 the newest tested line, and every patch
// inside a line counts as the same line.
func TestDetectSupportedRangeByMinor(t *testing.T) {
	signedIn := `echo '{"loggedIn": true}'`
	cases := []struct {
		version, wantSupport, wantReadiness, wantKey string
	}{
		{"1.9.9", "below_minimum", "not_ready", "harness_version_below_minimum"},
		{"2.0.0", "supported", "ready", ""},
		{"2.1.0", "supported", "ready", ""},
		{"2.1.239", "supported", "ready", ""},
		{"2.1.240", "supported", "ready", ""},
		{"2.2.0", "untested_newer", "ready", ""},
		{"2.10.0", "untested_newer", "ready", ""},
		{"3.0.0", "untested_newer", "ready", ""},
	}
	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			info := (&Adapter{Home: t.TempDir()}).Detect(context.Background(),
				standIn(t, `echo "`+c.version+` (Claude Code)"`, signedIn))
			if info.VersionSupport != c.wantSupport || info.Readiness != c.wantReadiness ||
				info.DiagnosticKey != c.wantKey {
				t.Errorf("version %s: support %s, readiness %s, diagnostic %q; want %s, %s, %q", c.version,
					info.VersionSupport, info.Readiness, info.DiagnosticKey, c.wantSupport, c.wantReadiness, c.wantKey)
			}
		})
	}
}

// harmflow-app#71: what the probes could not establish reads unknown, never a known state. A sign-in is invalid only
// when the CLI says it is signed out; a probe that is killed, as one is at its timeout, or that fails otherwise leaves
// the harness ready with a diagnostic, and the first execution proves the sign-in.
func TestDetectReadsOnlyWhatTheProbesEstablish(t *testing.T) {
	version, signedIn := `echo "2.1.239 (Claude Code)"`, `echo '{"loggedIn": true}'`
	cases := []struct {
		name, version, auth                         string
		wantVersion, wantSupport, wantAuth, wantKey string
		wantReadiness                               string
	}{
		{"signed in", version, signedIn, "2.1.239", "supported", "valid", "", "ready"},
		{"no version number", `echo "Claude Code (dev build)"`, signedIn, "", "unknown", "valid", "", "ready"},
		// The shell ends itself as the probe's timeout would end it, with no 20-second wait.
		{"sign-in probe killed", version, `kill -KILL $$`, "2.1.239", "supported", "unknown",
			"authentication_unverified", "ready"},
		{"sign-in probe fails", version, `echo "Error: could not reach the credential store" >&2; exit 1`, "2.1.239",
			"supported", "unknown", "authentication_unverified", "ready"},
		// What the CLI prints when it is signed out, exiting 1.
		{"signed out", version, `echo '{"loggedIn": false, "authMethod": "none"}'; exit 1`, "2.1.239", "supported",
			"invalid", "claude_not_signed_in", "not_ready"},
		{"signed out in text", version, `echo "Not logged in" >&2; exit 1`, "2.1.239", "supported", "invalid",
			"claude_not_signed_in", "not_ready"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info := (&Adapter{Home: t.TempDir()}).Detect(context.Background(), standIn(t, c.version, c.auth))
			if info.Version != c.wantVersion || info.VersionSupport != c.wantSupport || info.Authentication != c.wantAuth ||
				info.DiagnosticKey != c.wantKey || info.Readiness != c.wantReadiness {
				t.Errorf("version %q %s, authentication %s, diagnostic %q (%s), readiness %s; want version %q %s, "+
					"authentication %s, diagnostic %q, readiness %s", info.Version, info.VersionSupport, info.Authentication,
					info.DiagnosticKey, info.Diagnostic, info.Readiness, c.wantVersion, c.wantSupport, c.wantAuth, c.wantKey,
					c.wantReadiness)
			}
		})
	}
}

// helpStandIn writes a stand-in for the claude CLI at a supported version whose --help and `auth status` run the given
// shell commands.
func helpStandIn(t *testing.T, help, auth string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/bash
case "$1" in
  --version) echo "2.1.239 (Claude Code)" ;;
  --help) ` + help + ` ;;
  auth) ` + auth + ` ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// harmflow-app#329: a --help check that times out leaves Claude Code ready, with its flags unknown and a diagnostic
// saying the check timed out, which the dashboard shows as a warning; it never names a flag the version lacks. Help
// read in full that lacks a flag still reads as a version that lacks it.
func TestDetectReadsAHelpCheckThatTimesOutAsFlagsUnknown(t *testing.T) {
	signedIn := `echo '{"loggedIn": true}'`
	someFlags := `echo "-p, --print  --output-format <format>  --permission-mode <mode>  --allowedTools <tools...>"`
	allFlags := `echo "-p, --print  --output-format <format>  --permission-mode <mode>  --allowedTools <tools...>  ` +
		`--append-system-prompt <prompt>"`
	cases := []struct {
		name, help, auth           string
		timeout                    time.Duration // the check's limit; zero is its own
		wantUnknown                bool
		wantSupport, wantReadiness string
		wantKey                    string
	}{
		{"read in full", allFlags, signedIn, 0, false, "supported", "ready", ""},
		// exec, so the check's timeout ends the sleep itself, and no child keeps the help's output open.
		{"timed out", `exec sleep 30`, signedIn, time.Second, true, "supported", "ready", "harness_help_timed_out"},
		{"timed out after some flags", someFlags + `; exec sleep 30`, signedIn, time.Second, true, "supported", "ready",
			"harness_help_timed_out"},
		// The sign-in shows unknown in its own field, so the timed-out check keeps the diagnostic.
		{"timed out, sign-in unverified", `exec sleep 30`, `kill -KILL $$`, time.Second, true, "supported", "ready",
			"harness_help_timed_out"},
		{"read in full, lacking a flag", someFlags, signedIn, 0, false, "below_minimum", "not_ready",
			"harness_feature_missing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Adapter{Home: t.TempDir(), helpTimeout: c.timeout}
			info := a.Detect(context.Background(), helpStandIn(t, c.help, c.auth))
			if info.FeaturesUnknown != c.wantUnknown || info.VersionSupport != c.wantSupport ||
				info.Readiness != c.wantReadiness || info.DiagnosticKey != c.wantKey {
				t.Errorf("Detect: flags unknown %v, support %s, readiness %s, diagnostic %q (%s); want %v, %s, %s, %q",
					info.FeaturesUnknown, info.VersionSupport, info.Readiness, info.DiagnosticKey, info.Diagnostic,
					c.wantUnknown, c.wantSupport, c.wantReadiness, c.wantKey)
			}
			if c.wantKey == "harness_help_timed_out" && (info.DiagnosticParams["harness"] != Name ||
				info.DiagnosticParams["command"] != "--help") {
				t.Errorf("Detect: diagnostic params %v, want the harness %q and the command --help", info.DiagnosticParams,
					Name)
			}
		})
	}
}

// harmflow-app#329: with its flags unknown, Claude Code runs as if it had them, and the first attempt proves them: a
// profile's effort is not refused for a flag the help did not get to, and the optional flags are passed.
func TestUnknownFlagsAreTakenAsThere(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "") // the user's settings are read from Home, which has none
	a := &Adapter{Home: t.TempDir()}
	info := ports.HarnessInfo{Detected: true, Path: "/bin/claude", Version: "2.1.239",
		Efforts: []string{domain.DefaultEffort}, Features: map[string]bool{}, FeaturesUnknown: true}
	if err := a.ValidateProfile(info, "sonnet", "high"); err != nil {
		t.Errorf("ValidateProfile(sonnet, high) = %v, want nil", err)
	}
	perm := domain.TaskDocument{}.Effective().Spec.Permissions
	inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: *perm, ExecutionDir: t.TempDir(),
		Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(inv.Args, "--no-session-persistence") {
		t.Errorf("Prepare args %q, want --no-session-persistence", inv.Args)
	}
}

func TestValidateProfile(t *testing.T) {
	a := &Adapter{}
	info := ports.HarnessInfo{Detected: true, Version: "2.1.239", Efforts: []string{"default", "low", "high"},
		Features: map[string]bool{"--effort": true}}
	if err := a.ValidateProfile(info, "sonnet", "high"); err != nil {
		t.Error(err)
	}
	if err := a.ValidateProfile(info, "gpt-5", "default"); err == nil {
		t.Error("a model of another vendor was accepted")
	}
	if err := a.ValidateProfile(info, "default", "ultra"); err == nil {
		t.Error("an unsupported effort was accepted silently")
	}
}

// A harness is a file whose --version names it. Discovery skips one that does not and takes the next location; a path
// set by hand that does not is installed but not ready, saying why; Identify refuses both it and a file that cannot be
// executed.
func TestDetectSkipsAFileThatIsNotClaudeCode(t *testing.T) {
	impostor := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(impostor, []byte("#!/bin/bash\necho \"GNU bash, version 5.2.0\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Both on PATH, the impostor first, so no installation of this machine's comes before them.
	real := filepath.Join(t.TempDir(), "claude")
	data, err := os.ReadFile(standIn(t, `echo "2.1.239 (Claude Code)"`, `echo '{"loggedIn": true}'`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, data, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Home: t.TempDir(), PathEnv: filepath.Dir(impostor) + ":" + filepath.Dir(real)}
	info := a.Detect(context.Background(), "")
	if want, _ := filepath.EvalSymlinks(real); (info.Path != real && info.Path != want) || info.Readiness != "ready" {
		t.Errorf("discovery took %s (%s, %s), want %s", info.Path, info.Readiness, info.Diagnostic, real)
	}
	info = a.Detect(context.Background(), impostor)
	if info.Readiness != "installed" || info.DiagnosticKey != "harness_not_identified" || !info.Detected {
		t.Errorf("a path set by hand that is not ClaudeCode: %s, %s (%s)", info.Readiness, info.DiagnosticKey,
			info.Diagnostic)
	}
	plain := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(plain, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, path, key string }{
		{"a system program", "/bin/sh", "harness_not_identified"},
		{"an impostor", impostor, "harness_not_identified"},
		{"a file that is not executable", plain, "harness_path_not_executable"},
		{"ClaudeCode", real, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.path == "/bin/sh" && runtime.GOOS != "darwin" {
				t.Skip("macOS's /bin/sh is bash, which answers --version as a program that is not ClaudeCode; " +
					"elsewhere it is often dash, which fails --version")
			}
			got := ""
			if err := a.Identify(context.Background(), c.path); err != nil {
				got = domain.TextOf(err).Key
			}
			if got != c.key {
				t.Errorf("Identify(%s) = %q, want %q", c.path, got, c.key)
			}
		})
	}
}
