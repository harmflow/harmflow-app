package skills

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// queueSkill is the built-in skill whose already-processed check these tests run.
const queueSkill = "../../skills/hf-github-issue-queue/SKILL.md"

const (
	retryHeading = "## 1. Work to resume and to retry\n"
	newHeading   = "## 2. New issues\n"
)

// pullRequest is one that links the issue: its number, its state as GitHub reads it (OPEN, CLOSED, or MERGED), and
// whether it is a draft.
type pullRequest struct {
	number int
	state  string
	draft  bool
}

var (
	includeClosedPrs = regexp.MustCompile(`includeClosedPrs:\s*true`)
	nodeFields       = regexp.MustCompile(`nodes\s*\{([^}]*)\}`)
)

// The already-processed check of hf-github-issue-queue §2, run as a session runs it: the gh api graphql command the
// skill writes, through the real gh, against a stub that answers as GitHub does for an issue's linked pull requests.
// GitHub answers an open pull request, draft or ready for review, and a merged one, and a closed one only when the
// query asks for closed pull requests. An issue that any of them links is already processed; only one that none links
// is new work.
func TestAlreadyProcessed(t *testing.T) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatalf("the check runs gh api graphql, and gh is not on PATH: %v", err)
	}
	command := graphqlCommand(t, queueSection(t, newHeading))

	for _, c := range []struct {
		name  string
		links []pullRequest
		want  bool // whether the issue is new work
	}{
		{"an open draft links it", []pullRequest{{50, "OPEN", true}}, false},
		{"an open pull request ready for review links it", []pullRequest{{50, "OPEN", false}}, false},
		{"a pull request closed without merging links it", []pullRequest{{50, "CLOSED", false}}, false},
		{"a merged pull request links it", []pullRequest{{50, "MERGED", false}}, false},
		{"no pull request links it", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
					t.Errorf("gh asked for %s %s, which the check does not read", r.Method, r.URL)
					http.NotFound(w, r)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("gh sent a body that is not JSON: %v", err)
					http.Error(w, `{"message":"Problems parsing JSON"}`, http.StatusBadRequest)
					return
				}
				// gh groups its fields into a query and its variables only for the endpoint graphql. To a full URL, as
				// here, it sends them side by side.
				query, _ := body["query"].(string)
				variables := body
				if v, ok := body["variables"].(map[string]any); ok {
					variables = v
				}
				if variables["owner"] != "harmflow" || variables["repo"] != "example" || variables["n"] != float64(42) {
					t.Errorf("gh asked for owner %v, repo %v, issue %v, want issue 42 of harmflow/example",
						variables["owner"], variables["repo"], variables["n"])
				}
				fields := nodeFields.FindStringSubmatch(query)
				if !strings.Contains(query, "closedByPullRequestsReferences") || fields == nil {
					t.Errorf("the query %q does not read the nodes of the issue's closedByPullRequestsReferences", query)
					http.Error(w, `{"message":"Bad query"}`, http.StatusBadRequest)
					return
				}
				nodes := []map[string]any{}
				for _, pr := range c.links {
					if pr.state == "CLOSED" && !includeClosedPrs.MatchString(query) {
						continue
					}
					node := map[string]any{}
					for _, field := range strings.Fields(fields[1]) {
						switch field {
						case "number":
							node[field] = pr.number
						case "state":
							node[field] = pr.state
						case "isDraft":
							node[field] = pr.draft
						default:
							t.Errorf("the query asks for a pull request's %s, which the stub does not answer", field)
						}
					}
					nodes = append(nodes, node)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
					"issue": map[string]any{"state": "OPEN", "closedByPullRequestsReferences": map[string]any{"nodes": nodes}},
				}}})
			}))
			defer srv.Close()

			n := linkedPullRequests(t, gh, srv.URL, command)
			if got := n == 0; got != c.want {
				t.Errorf("the check reads %d linked pull requests, so the issue is new work: %v, want %v", n, got, c.want)
			}
		})
	}
}

// The check reads an issue's links with the query of §1's retry lookup, so that the skill's two lookups of one fact
// cannot drift apart.
func TestAlreadyProcessedRunsTheRetryLookup(t *testing.T) {
	retry := collapse(graphqlCommand(t, queueSection(t, retryHeading)))
	check := collapse(graphqlCommand(t, queueSection(t, newHeading)))
	if !slices.Equal(check, retry) {
		t.Errorf("§2 runs gh %q, want §1's retry lookup, gh %q", check, retry)
	}
}

// linkedPullRequests runs the check's command for issue 42 of harmflow/example against the stub at base, and returns
// how many pull requests its answer names. gh reads a configuration of its own, so the user's is never touched.
func linkedPullRequests(t *testing.T, gh, base string, command []string) int {
	t.Helper()
	args := append([]string{}, command...)
	args[1] = base + "/" + args[1]
	for i, arg := range args {
		switch arg {
		case "owner=OWNER":
			args[i] = "owner=harmflow"
		case "repo=REPO":
			args[i] = "repo=example"
		case "n=<issue>":
			args[i] = "n=42"
		}
	}
	for _, arg := range args {
		if strings.Contains(arg, "OWNER") || strings.Contains(arg, "REPO") || strings.Contains(arg, "<issue>") {
			t.Fatalf("gh %s keeps a placeholder the test does not fill: %q", strings.Join(args, " "), arg)
		}
	}
	cmd := exec.Command(gh, args...)
	cmd.Env = append(os.Environ(), "GH_CONFIG_DIR="+t.TempDir(), "GH_TOKEN=stub", "GH_HOST=github.com",
		"GH_NO_UPDATE_NOTIFIER=1", "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("gh %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	var out struct {
		Data struct {
			Repository struct {
				Issue struct {
					Linked struct {
						Nodes []json.RawMessage `json:"nodes"`
					} `json:"closedByPullRequestsReferences"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("gh %s printed %q, which is not the query's answer: %v", strings.Join(args, " "), stdout.String(), err)
	}
	return len(out.Data.Repository.Issue.Linked.Nodes)
}

// queueSection is the section of the skill that starts at heading, up to the next section.
func queueSection(t *testing.T, heading string) string {
	t.Helper()
	data, err := os.ReadFile(queueSkill)
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(data), heading)
	if !ok {
		t.Fatalf("%s has no heading %q", queueSkill, strings.TrimSpace(heading))
	}
	section, _, _ = strings.Cut(section, "\n## ")
	return section
}

// graphqlCommand is the arguments after gh of the one gh api graphql command in the section's bash blocks. Its query
// is quoted across lines, so the command goes on until its quote closes.
func graphqlCommand(t *testing.T, section string) []string {
	t.Helper()
	var commands []string
	var command strings.Builder
	inBlock := false
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case command.Len() > 0:
			command.WriteString("\n" + line)
		case trimmed == "```bash":
			inBlock = true
			continue
		case trimmed == "```":
			inBlock = false
			continue
		case inBlock && strings.HasPrefix(trimmed, "gh api graphql "):
			command.WriteString(trimmed)
		default:
			continue
		}
		if strings.Count(command.String(), "'")%2 == 0 {
			commands = append(commands, command.String())
			command.Reset()
		}
	}
	if command.Len() > 0 {
		t.Fatalf("the section of %s leaves the quote of %q open", queueSkill, command.String())
	}
	if len(commands) != 1 {
		t.Fatalf("the section of %s has %d gh api graphql commands, want 1: the lookup of an issue's linked pull requests",
			queueSkill, len(commands))
	}
	return words(t, commands[0])[1:]
}

// collapse turns each run of white space in the arguments into one space, which a GraphQL query reads alike.
func collapse(args []string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = strings.Join(strings.Fields(arg), " ")
	}
	return out
}
