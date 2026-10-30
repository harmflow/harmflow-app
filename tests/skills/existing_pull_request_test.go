package skills

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// draftPRSkill is the built-in skill whose lookup of an item's pull request these tests run.
const draftPRSkill = "../../skills/hf-github-draft-pr/SKILL.md"

// workBranch is the branch the session works issue 42 of harmflow/example on.
const workBranch = "harmflow/42-retry-limit"

// defaultBranch is harmflow/example's default branch, the only one into which GitHub links a pull request's closing
// references to their issues.
const defaultBranch = "main"

// stubPullRequest is a pull request of harmflow/example as the stub holds it: its number, its head branch, the branch
// it goes into, whether it is open, and the issues its body closes. GitHub links those issues only into defaultBranch.
type stubPullRequest struct {
	number int
	branch string
	base   string // defaultBranch when empty
	open   bool
	closes []int
}

// found is a pull request a lookup prints.
type found struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	HeadRefName string `json:"headRefName"`
}

var openOnly = regexp.MustCompile(`states:\s*\[?\s*OPEN\s*\]?`)

// The lookup of hf-github-draft-pr §3, run as a session runs it before it publishes issue 42 from its work branch: the
// two gh api commands the skill writes, through the real gh, against a stub that answers as GitHub does for the pull
// requests from a branch and for a repository's open pull requests with the issues they close. Each case needs one of
// the two: only the lookup from the work branch finds a pull request that closes no issue, and only the lookup by issue
// finds one from another branch. A pull request into a branch other than the default one closes no issue on GitHub,
// whatever its body says, so the lookup by issue finds it by the issue's number at the start of its branch's last
// segment, and finds no pull request into the default branch that way. Only the cases named none answer that the item
// has no pull request, so that the session opens one.
func TestExistingPullRequest(t *testing.T) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatalf("the lookup runs gh api, and gh is not on PATH: %v", err)
	}
	byBranch, byIssue := lookupCommands(t)

	for _, c := range []struct {
		name  string
		pulls []stubPullRequest
		want  found // the item's pull request, zero when it has none
	}{
		{"an open pull request from the work branch",
			[]stubPullRequest{{number: 50, branch: workBranch, open: true}},
			found{50, "https://github.com/harmflow/example/pull/50", workBranch}},
		{"an open pull request from another branch closes the issue",
			[]stubPullRequest{{number: 51, branch: "harmflow/42-add-retry-limit", open: true, closes: []int{42}}},
			found{51, "https://github.com/harmflow/example/pull/51", "harmflow/42-add-retry-limit"}},
		// The repository holds pull requests, none of them the item's: one from the work branch and one from another
		// branch that close the issue but were closed, and an open one that closes another issue.
		{"none",
			[]stubPullRequest{
				{number: 48, branch: workBranch, closes: []int{42}},
				{number: 49, branch: "harmflow/42-add-retry-limit", closes: []int{42}},
				{number: 52, branch: "harmflow/43-log-level", open: true, closes: []int{43}},
			},
			found{}},
		{"an open pull request from the work branch into another branch",
			[]stubPullRequest{{number: 53, branch: workBranch, base: "develop", open: true, closes: []int{42}}},
			found{53, "https://github.com/harmflow/example/pull/53", workBranch}},
		{"an open pull request from another branch into another branch carries the issue's number",
			[]stubPullRequest{{number: 54, branch: "fix/42-add-retry-limit", base: "develop", open: true, closes: []int{42}}},
			found{54, "https://github.com/harmflow/example/pull/54", "fix/42-add-retry-limit"}},
		// Into another branch, a pull request is the issue's only by a branch whose last segment starts with 42 and a
		// hyphen, or is 42: not 142, not 42 later in the name, not 42 in a segment before the last, and never a closed
		// one.
		{"none into another branch",
			[]stubPullRequest{
				{number: 55, branch: "harmflow/142-retry-limit", base: "develop", open: true, closes: []int{142}},
				{number: 56, branch: "harmflow/42retry-limit", base: "develop", open: true},
				{number: 57, branch: "harmflow/retry-limit-42", base: "develop", open: true},
				{number: 58, branch: "42/add-retry-limit", base: "develop", open: true},
				{number: 59, branch: "fix/42-add-retry-limit", base: "develop", closes: []int{42}},
			},
			found{}},
		// Into the default branch, GitHub's link decides: a pull request whose body closes no issue is not the issue's,
		// though its branch carries the issue's number.
		{"none into the default branch from a branch that carries the issue's number",
			[]stubPullRequest{{number: 60, branch: "fix/42-add-retry-limit", open: true}},
			found{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var answer any
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/harmflow/example/pulls":
					answer = branchAnswer(t, r, c.pulls)
				case r.Method == http.MethodPost && r.URL.Path == "/graphql":
					answer = issueAnswer(t, r, c.pulls)
				default:
					t.Errorf("gh asked for %s %s, which the lookup does not read", r.Method, r.URL)
				}
				if answer == nil {
					http.Error(w, `{"message":"Bad request"}`, http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(answer)
			}))
			defer srv.Close()

			var got []found
			for _, command := range [][]string{byBranch, byIssue} {
				got = append(got, lookup(t, gh, srv.URL, command)...)
			}
			if c.want == (found{}) {
				if len(got) != 0 {
					t.Errorf("the lookup finds %v, want no pull request", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("the lookup finds no pull request, want %v", c.want)
			}
			for _, pr := range got {
				if pr != c.want {
					t.Errorf("the lookup finds %v, want %v", pr, c.want)
				}
			}
		})
	}
}

// branchAnswer is what the pulls endpoint answers: the pull requests whose head is the head the query names, in the
// state it names, open by default. GitHub reads a head only as owner:branch.
func branchAnswer(t *testing.T, r *http.Request, pulls []stubPullRequest) any {
	t.Helper()
	owner, branch, ok := strings.Cut(r.URL.Query().Get("head"), ":")
	if !ok || owner != "harmflow" {
		t.Errorf("gh asked for the pull requests of head %q, want harmflow:<branch>", r.URL.Query().Get("head"))
		return nil
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	answer := []map[string]any{}
	for _, pr := range pulls {
		prState := "closed"
		if pr.open {
			prState = "open"
		}
		if pr.branch != branch || (state != "all" && state != prState) {
			continue
		}
		answer = append(answer, map[string]any{
			"number":   pr.number,
			"html_url": pullURL(pr),
			"state":    prState,
			"draft":    true,
			"head":     map[string]any{"ref": pr.branch, "label": "harmflow:" + pr.branch},
		})
	}
	return answer
}

// issueAnswer is what the GraphQL endpoint answers to the query of the repository's pull requests: open ones only
// when the query asks for that state, with each field the query asks for, the closing references only of a pull
// request into the default branch, the default branch's name when the query asks for it, and an error for a field the
// stub does not know.
func issueAnswer(t *testing.T, r *http.Request, pulls []stubPullRequest) any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("gh sent a body that is not JSON: %v", err)
		return nil
	}
	// gh groups its fields into a query and its variables only for the endpoint graphql. To a full URL, as here, it
	// sends them side by side.
	query, _ := body["query"].(string)
	variables := body
	if v, ok := body["variables"].(map[string]any); ok {
		variables = v
	}
	if variables["owner"] != "harmflow" || variables["repo"] != "example" {
		t.Errorf("gh asked for owner %v and repo %v, want harmflow/example", variables["owner"], variables["repo"])
	}
	root, err := parseQuery(query)
	if err != nil {
		t.Errorf("the query %q does not parse: %v", query, err)
		return nil
	}
	prs := root.path("repository", "pullRequests")
	nodes := prs.path("nodes")
	if nodes == nil {
		t.Errorf("the query %q does not read the nodes of the repository's pullRequests", query)
		return nil
	}
	answer := []map[string]any{}
	for _, pr := range pulls {
		if !pr.open && openOnly.MatchString(prs.args) {
			continue
		}
		node := map[string]any{}
		for name, f := range nodes.fields {
			switch name {
			case "number":
				node[name] = pr.number
			case "url":
				node[name] = pullURL(pr)
			case "headRefName":
				node[name] = pr.branch
			case "baseRefName":
				node[name] = cmp.Or(pr.base, defaultBranch)
			case "closingIssuesReferences":
				issueFields := f.path("nodes")
				if issueFields == nil {
					t.Errorf("the query %q does not read the nodes of a pull request's closingIssuesReferences", query)
					return nil
				}
				issues := []map[string]any{}
				var closes []int
				if cmp.Or(pr.base, defaultBranch) == defaultBranch {
					closes = pr.closes
				}
				for _, n := range closes {
					issue := map[string]any{}
					for name := range issueFields.fields {
						if name != "number" {
							t.Errorf("the query asks for a closing issue's %s, which the stub does not answer", name)
						}
						issue[name] = n
					}
					issues = append(issues, issue)
				}
				node[name] = map[string]any{"nodes": issues}
			default:
				t.Errorf("the query asks for a pull request's %s, which the stub does not answer", name)
			}
		}
		answer = append(answer, node)
	}
	repository := map[string]any{"pullRequests": map[string]any{"nodes": answer}}
	for name, f := range root.path("repository").fields {
		switch name {
		case "pullRequests":
		case "defaultBranchRef":
			for refField := range f.fields {
				if refField != "name" {
					t.Errorf("the query asks for the default branch's %s, which the stub does not answer", refField)
				}
			}
			repository[name] = map[string]any{"name": defaultBranch}
		default:
			t.Errorf("the query asks for a repository's %s, which the stub does not answer", name)
		}
	}
	return map[string]any{"data": map[string]any{"repository": repository}}
}

// pullURL is the pull request's page on GitHub.
func pullURL(pr stubPullRequest) string {
	return "https://github.com/harmflow/example/pull/" + strconv.Itoa(pr.number)
}

// field is a field a GraphQL query selects: what its parentheses hold, and the fields it selects in turn, nil for a
// scalar.
type field struct {
	args   string
	fields map[string]*field
}

// path is the field the names select one inside another, nil when the query does not select it.
func (f *field) path(names ...string) *field {
	for _, name := range names {
		if f == nil {
			return nil
		}
		f = f.fields[name]
	}
	return f
}

var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)

// parseQuery reads the selection set of a GraphQL operation, the part after the operation's variables.
func parseQuery(query string) (*field, error) {
	_, selections, ok := strings.Cut(query, "{")
	if !ok {
		return nil, errors.New("it selects nothing")
	}
	root, _, err := parseSelection(selections)
	return root, err
}

// parseSelection reads a selection set from just after its opening brace, and returns it with what follows its
// closing brace.
func parseSelection(s string) (*field, string, error) {
	set := &field{fields: map[string]*field{}}
	var last *field
	for {
		s = strings.TrimLeft(s, " \t\n,")
		switch {
		case s == "":
			return nil, "", errors.New("it leaves a selection set open")
		case s[0] == '}':
			return set, s[1:], nil
		case s[0] == '{' && last != nil:
			sub, rest, err := parseSelection(s[1:])
			if err != nil {
				return nil, "", err
			}
			last.fields, s = sub.fields, rest
		case s[0] == '(' && last != nil:
			args, rest, ok := strings.Cut(s[1:], ")")
			if !ok {
				return nil, "", fmt.Errorf("it leaves the arguments %q open", s)
			}
			last.args, s = args, rest
		default:
			n := fieldName.FindString(s)
			if n == "" {
				return nil, "", fmt.Errorf("it has %q where a field's name should be", s)
			}
			last = &field{}
			set.fields[n], s = last, s[len(n):]
		}
	}
}

// lookup runs one of the lookup's commands for issue 42 of harmflow/example and its work branch against the stub at
// base, and returns the pull requests it prints. gh reads a configuration of its own, so the user's is never touched.
func lookup(t *testing.T, gh, base string, command []string) []found {
	t.Helper()
	fill := strings.NewReplacer("OWNER/REPO", "harmflow/example", "owner=OWNER", "owner=harmflow",
		"repo=REPO", "repo=example", "OWNER:", "harmflow:", "<branch>", workBranch, "<issue>", "42")
	args := make([]string, len(command))
	for i, arg := range command {
		args[i] = fill.Replace(arg)
		for _, placeholder := range []string{"OWNER", "REPO", "<branch>", "<issue>"} {
			if strings.Contains(args[i], placeholder) {
				t.Fatalf("gh %s keeps a placeholder the test does not fill: %q", strings.Join(command, " "), placeholder)
			}
		}
	}
	args[1] = base + "/" + args[1]
	cmd := exec.Command(gh, args...)
	cmd.Env = append(os.Environ(), "GH_CONFIG_DIR="+t.TempDir(), "GH_TOKEN=stub", "GH_HOST=github.com",
		"GH_NO_UPDATE_NOTIFIER=1", "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("gh %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	var out []found
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("gh %s printed %q, which is not a JSON array of pull requests: %v", strings.Join(args, " "),
			stdout.String(), err)
	}
	return out
}

// lookupCommands are the arguments after gh of the two commands in the bash block of §3 of the skill: the lookup from
// the work branch, then the lookup by issue. The second quotes its query across lines, so a command goes on until its
// quote closes.
func lookupCommands(t *testing.T) (byBranch, byIssue []string) {
	t.Helper()
	data, err := os.ReadFile(draftPRSkill)
	if err != nil {
		t.Fatal(err)
	}
	const heading = "## 3. Look for the item's pull request\n"
	_, section, ok := strings.Cut(string(data), heading)
	if !ok {
		t.Fatalf("%s has no heading %q", draftPRSkill, strings.TrimSpace(heading))
	}
	section, _, _ = strings.Cut(section, "\n## ")
	_, block, ok := strings.Cut(section, "```bash\n")
	if !ok {
		t.Fatalf("§3 of %s has no bash block", draftPRSkill)
	}
	block, _, _ = strings.Cut(block, "```")
	var commands [][]string
	var command strings.Builder
	for _, line := range strings.Split(block, "\n") {
		if command.Len() > 0 {
			command.WriteString("\n" + line)
		} else if strings.HasPrefix(line, "gh ") {
			command.WriteString(line)
		}
		if command.Len() > 0 && strings.Count(command.String(), "'")%2 == 0 {
			commands = append(commands, words(t, command.String())[1:])
			command.Reset()
		}
	}
	if command.Len() > 0 {
		t.Fatalf("§3 of %s leaves the quote of %q open", draftPRSkill, command.String())
	}
	if len(commands) != 2 || !slices.Equal(commands[0][:1], []string{"api"}) || !strings.HasPrefix(commands[0][1], "repos/") ||
		!slices.Equal(commands[1][:2], []string{"api", "graphql"}) {
		t.Fatalf("§3 of %s has the commands %q, want two: gh api repos/… from the work branch, then gh api graphql by issue",
			draftPRSkill, commands)
	}
	return commands[0], commands[1]
}
