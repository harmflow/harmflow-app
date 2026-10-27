package skills

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// worktreeSkill is the built-in skill whose base-branch check these tests run.
const worktreeSkill = "../../skills/hf-github-worktree/SKILL.md"

// answer is what the stub of the GitHub API replies to one path.
type answer struct {
	status int
	body   string
}

var (
	notProtected = answer{http.StatusNotFound, `{"message":"Branch not protected"}`}
	noRules      = answer{http.StatusOK, `[]`}
	proPlan      = answer{http.StatusForbidden, `{"message":"Upgrade to GitHub Pro or make this repository public to enable this feature."}`}
)

// The check of hf-github-worktree §1, run as a session runs it: each gh api command the skill writes, through the real
// gh, against a stub that answers as a branch's classic protection and its rules would. The stronger of the two
// answers picks the message from the skill's table, with only the branch's name filled in.
func TestBaseBranchProtection(t *testing.T) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatalf("the check runs gh api, and gh is not on PATH: %v", err)
	}
	section := protectionSection(t)
	commands := ghCommands(t, section)
	messages := tableMessages(t, section)

	const (
		reviewless  = "main requires a pull request but no approving review; merges without review are possible"
		unprotected = "main has no branch protection; merges without review are possible"
	)
	for _, c := range []struct {
		name    string
		classic answer
		rules   answer
		want    string // the message, empty when nothing is reported
	}{
		{"a ruleset requires an approving review", notProtected,
			answer{http.StatusOK, `[{"type":"deletion"},{"type":"pull_request","parameters":{"required_approving_review_count":1}}]`}, ""},
		{"a ruleset requires a pull request but no review", notProtected,
			answer{http.StatusOK, `[{"type":"deletion"},{"type":"non_fast_forward"},` +
				`{"type":"pull_request","parameters":{"required_approving_review_count":0}},` +
				`{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"CI"}]}}]`}, reviewless},
		{"neither classic protection nor rules", notProtected, noRules, unprotected},
		{"classic protection requires an approving review", answer{http.StatusOK,
			`{"required_pull_request_reviews":{"required_approving_review_count":2}}`}, noRules, ""},
		{"classic protection requires a pull request but no review", answer{http.StatusOK,
			`{"required_pull_request_reviews":{"required_approving_review_count":0}}`}, noRules, reviewless},
		{"classic protection requires no pull request", answer{http.StatusOK,
			`{"required_status_checks":{"contexts":["CI"]}}`}, noRules, unprotected},
		{"the strongest of two rulesets decides", notProtected, answer{http.StatusOK,
			`[{"type":"pull_request","parameters":{"required_approving_review_count":0}},` +
				`{"type":"pull_request","parameters":{"required_approving_review_count":2}}]`}, ""},
		{"the review classic protection requires outweighs a reviewless ruleset", answer{http.StatusOK,
			`{"required_pull_request_reviews":{"required_approving_review_count":1}}`}, answer{http.StatusOK,
			`[{"type":"pull_request","parameters":{"required_approving_review_count":0}}]`}, ""},
		{"the plan cannot read the protection", proPlan, proPlan, unprotected},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				a, ok := map[string]answer{
					"/repos/harmflow/example/branches/main/protection": c.classic,
					"/repos/harmflow/example/rules/branches/main":      c.rules,
				}[r.URL.Path]
				if !ok {
					t.Errorf("gh asked for %s, which the check does not read", r.URL)
					a = answer{http.StatusNotFound, `{"message":"Not Found"}`}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(a.status)
				_, _ = w.Write([]byte(a.body))
			}))
			defer srv.Close()

			strongest := "none"
			for _, args := range commands {
				if got := run(t, gh, srv.URL, args); strength[got] > strength[strongest] {
					strongest = got
				}
			}
			if got := strings.ReplaceAll(messages[strongest], "BASE", "main"); got != c.want {
				t.Errorf("the check answers %s, whose message is %q, want %q", strongest, got, c.want)
			}
		})
	}
}

// strength orders the answers: the stronger of the two calls' answers decides.
var strength = map[string]int{"none": 0, "pull_request": 1, "review": 2}

// run runs one of the skill's gh api commands on the repository harmflow/example and its branch main, against the stub
// at base, and returns its answer: what it printed, or none when it failed. gh reads a configuration of its own, so
// the user's is never touched.
func run(t *testing.T, gh, base string, args []string) string {
	t.Helper()
	args = append([]string{}, args...)
	endpoint := strings.ReplaceAll(strings.ReplaceAll(args[1], "OWNER/REPO", "harmflow/example"), "BASE", "main")
	args[1] = base + "/" + endpoint
	cmd := exec.Command(gh, args...)
	cmd.Env = append(os.Environ(), "GH_CONFIG_DIR="+t.TempDir(), "GH_TOKEN=stub", "GH_HOST=github.com",
		"GH_NO_UPDATE_NOTIFIER=1", "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("gh %s: %v", strings.Join(args, " "), err)
		}
		return "none"
	}
	got := strings.TrimSpace(stdout.String())
	if _, ok := strength[got]; !ok {
		t.Fatalf("gh %s printed %q, want review, pull_request, or none\n%s", strings.Join(args, " "), got, stderr.String())
	}
	return got
}

// protectionSection is §1 of the skill, up to the next section.
func protectionSection(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(worktreeSkill)
	if err != nil {
		t.Fatal(err)
	}
	const heading = "## 1. Check the base branch's protection\n"
	_, section, ok := strings.Cut(string(data), heading)
	if !ok {
		t.Fatalf("%s has no heading %q", worktreeSkill, strings.TrimSpace(heading))
	}
	section, _, _ = strings.Cut(section, "\n## ")
	return section
}

// ghCommands are the arguments after gh of each gh api command in the section's bash block, in order: one call for
// classic protection, one for the rules.
func ghCommands(t *testing.T, section string) [][]string {
	t.Helper()
	_, block, ok := strings.Cut(section, "```bash\n")
	if !ok {
		t.Fatalf("§1 of %s has no bash block", worktreeSkill)
	}
	block, _, _ = strings.Cut(block, "```")
	var commands [][]string
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "gh api ") {
			commands = append(commands, words(t, line)[1:])
		}
	}
	if len(commands) != 2 {
		t.Fatalf("§1 of %s has %d gh api commands, want 2: classic protection and the rules", worktreeSkill, len(commands))
	}
	return commands
}

// words splits a command line as the shell does for the skill's commands, which quote with ' and " and escape nothing.
func words(t *testing.T, line string) []string {
	t.Helper()
	var out []string
	var w strings.Builder
	in, quote := false, rune(0)
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			w.WriteRune(r)
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ':
			if in {
				out = append(out, w.String())
				w.Reset()
				in = false
			}
		case r == '\\':
			t.Fatalf("%q escapes a character, which words does not read", line)
		default:
			w.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		t.Fatalf("%q leaves a quote open", line)
	}
	if in {
		out = append(out, w.String())
	}
	return out
}

var tableRow = regexp.MustCompile("^\\| `([a-z_]+)` \\| (.*) \\|$")

// tableMessages are the messages of the section's table by answer, empty for an answer that reports nothing: a cell
// that is not one quoted message.
func tableMessages(t *testing.T, section string) map[string]string {
	t.Helper()
	messages := map[string]string{}
	for _, line := range strings.Split(section, "\n") {
		m := tableRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cell := m[2]
		if strings.HasPrefix(cell, "`") && strings.HasSuffix(cell, "`") && strings.Count(cell, "`") == 2 {
			messages[m[1]] = strings.Trim(cell, "`")
		} else {
			messages[m[1]] = ""
		}
	}
	for answer := range strength {
		if _, ok := messages[answer]; !ok {
			t.Fatalf("the table of §1 of %s has no row for %s", worktreeSkill, answer)
		}
	}
	return messages
}
