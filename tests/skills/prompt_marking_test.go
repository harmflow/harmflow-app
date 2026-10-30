package skills

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// implementerAgent is the built-in agent whose This task section states how the task marks its pull requests.
const implementerAgent = "../../agents/hf-github-issue-implementer.md"

// markingLine is the placeholder §4 of hf-github-draft-pr puts in a pull request's body for the line of the task's
// marking.
const markingLine = "<marking line>"

// managedBy is a marking line a skill or an agent quotes.
var managedBy = regexp.MustCompile("`(Managed by [^`]*)`")

// The body of hf-github-draft-pr §4, as a session fills it in to open or update a draft pull request: the sections
// every body documents, in order, then the placeholder of the line the task's prompt marks its pull requests with. No
// other line of the template marks the pull request or names the execution, so that a prompt whose marking names no
// line gets a body with none, and one whose marking names a line gets that line alone.
func TestBodyMarking(t *testing.T) {
	_, block, ok := strings.Cut(pullRequestSection(t), "```markdown\n")
	if !ok {
		t.Fatalf("§4 of %s has no markdown block for the pull request's body", draftPRSkill)
	}
	block, _, ok = strings.Cut(block, "```")
	if !ok {
		t.Fatalf("§4 of %s leaves the markdown block of the body open", draftPRSkill)
	}
	var lines []string
	for _, line := range strings.Split(block, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		t.Fatalf("the body of §4 of %s is empty", draftPRSkill)
	}

	at := 0
	for _, want := range []string{"Closes #<issue>", "## Summary", "## Tests", "## Pending"} {
		i := slices.Index(lines[at:], want)
		if i < 0 {
			t.Errorf("the body of §4 of %s has the lines %q, want %q after %q", draftPRSkill, lines, want, lines[:at])
			continue
		}
		at += i + 1
	}
	if got := lines[len(lines)-1]; got != markingLine {
		t.Errorf("the body of §4 of %s ends with %q, want the marking's placeholder %q", draftPRSkill, got, markingLine)
	}
	if n := strings.Count(block, markingLine); n != 1 {
		t.Errorf("the body of §4 of %s holds %q %d times, want once", draftPRSkill, markingLine, n)
	}
	for _, line := range lines[:len(lines)-1] {
		for _, word := range []string{"managed by", "harmflow", "execution"} {
			if strings.Contains(strings.ToLower(line), word) {
				t.Errorf("the body of §4 of %s has the line %q, which names %q: want the marking only in %q",
					draftPRSkill, line, word, markingLine)
			}
		}
	}
}

// What §4 of hf-github-draft-pr says its marking placeholder holds: the line the task's prompt states, whose default is
// the line the built-in agent's This task section states, word for word. No other marking line appears in the skill,
// and the skill names no execution ID, so that a task that keeps the agent's marking gets the line its prompt states
// and nothing more.
func TestDefaultMarkingLine(t *testing.T) {
	agent := agentMarking(t)
	data, err := os.ReadFile(draftPRSkill)
	if err != nil {
		t.Fatal(err)
	}
	skill := string(data)

	_, prose, ok := strings.Cut(pullRequestSection(t), "\n`"+markingLine+"` is ")
	if !ok {
		t.Fatalf("§4 of %s says nowhere what `%s` is", draftPRSkill, markingLine)
	}
	paragraph, _, _ := strings.Cut(prose, "\n\n")
	if !strings.Contains(paragraph, "the task's prompt") {
		t.Errorf("§4 of %s says `%s` is %q, want the line the task's prompt states", draftPRSkill, markingLine, paragraph)
	}
	if got := managedBy.FindAllStringSubmatch(paragraph, -1); len(got) != 1 || got[0][1] != agent {
		t.Errorf("§4 of %s gives `%s` the default %q, want the built-in agent's %q", draftPRSkill, markingLine, got, agent)
	}
	for _, m := range managedBy.FindAllStringSubmatch(skill, -1) {
		if m[1] != agent {
			t.Errorf("%s quotes the marking line %q, want only the built-in agent's %q", draftPRSkill, m[1], agent)
		}
	}
	for _, id := range []string{"<execution-id>", "execution.id"} {
		if strings.Contains(skill, id) {
			t.Errorf("%s names %s, want a body that names no execution", draftPRSkill, id)
		}
	}
}

// numberedHeading starts a section of a skill. The body's own headings, such as ## Summary, carry no number.
var numberedHeading = regexp.MustCompile(`(?m)^## \d+\. `)

// pullRequestSection is §4 of hf-github-draft-pr, up to the next numbered section.
func pullRequestSection(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(draftPRSkill)
	if err != nil {
		t.Fatal(err)
	}
	const heading = "## 4. Push, and open or update exactly one draft pull request\n"
	_, section, ok := strings.Cut(string(data), heading)
	if !ok {
		t.Fatalf("%s has no heading %q", draftPRSkill, strings.TrimSpace(heading))
	}
	if next := numberedHeading.FindStringIndex(section); next != nil {
		section = section[:next[0]]
	}
	return section
}

// agentMarking is the marking line the Pull request marking item of the built-in agent's This task section quotes.
func agentMarking(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(implementerAgent)
	if err != nil {
		t.Fatal(err)
	}
	const item = "- **Pull request marking:**"
	_, bullet, ok := strings.Cut(string(data), item)
	if !ok {
		t.Fatalf("%s has no item %q", implementerAgent, item)
	}
	bullet, _, _ = strings.Cut(bullet, "\n- ")
	bullet, _, _ = strings.Cut(bullet, "\n\n")
	m := managedBy.FindAllStringSubmatch(bullet, -1)
	if len(m) != 1 {
		t.Fatalf("the item %q of %s quotes %d marking lines, want 1", item, implementerAgent, len(m))
	}
	return m[0][1]
}
