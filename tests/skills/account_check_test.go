package skills

import (
	"os"
	"strings"
	"testing"
)

// accountCheck is the first sentence of step 2 of the built-in agent's procedure: the check that the session's own
// GitHub account and git work before it builds the queue.
const accountCheck = "2. Check that `gh api user` and `git --version` succeed."

// Step 2 of the built-in agent checks the account the session uses, with gh api user, and not gh auth status, which
// exits 1 when gh keeps another login the session cannot read, such as a keyring login under the Claude Code
// sandbox, though the token the session uses works. A check that fails still ends the run with a failed report.
func TestAccountCheck(t *testing.T) {
	data, err := os.ReadFile(implementerAgent)
	if err != nil {
		t.Fatal(err)
	}
	_, step, ok := strings.Cut(string(data), "\n2. ")
	if !ok {
		t.Fatalf("%s has no step 2", implementerAgent)
	}
	step, _, ok = strings.Cut(step, "\n3. ")
	if !ok {
		t.Fatalf("%s has no step 3 after step 2", implementerAgent)
	}
	step = "2. " + strings.Join(strings.Fields(step), " ")

	if !strings.HasPrefix(step, accountCheck+" ") {
		t.Errorf("step 2 of %s reads %q, want it to begin %q", implementerAgent, step, accountCheck)
	}
	for _, sentence := range strings.SplitAfter(strings.TrimPrefix(step, "2. "), ". ") {
		if strings.Contains(sentence, "`gh auth status`") && !strings.Contains(sentence, "is not the check") {
			t.Errorf("step 2 of %s has the sentence %q, want `gh auth status` named only as not the check",
				implementerAgent, sentence)
		}
	}
	if !strings.Contains(step, "If either fails, write the report with outcome `failed` and stop.") {
		t.Errorf("step 2 of %s reads %q, want a failed check to write the report with outcome `failed` and stop",
			implementerAgent, step)
	}
}
