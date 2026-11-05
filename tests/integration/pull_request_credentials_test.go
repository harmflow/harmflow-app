package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"testing"
)

// namedText is a text of a pull request, with what names it in a failure: never its content, which may hold a secret.
type namedText struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// noCredentialInPullRequest fails the test for each text of the pull request pr of queueRepo that holds a secret or a
// piece of one of eight characters or more: its title, its body, the message of each of its commits, its diff, each
// comment, review, and review comment on it, and each comment on the issue it works. A failure names the text — a
// commit by its hash, a comment by its URL — and the secret by what, never by its value or any piece of it. It reads
// every comment the two have: a test calls it on a pull request and an issue its own run opened, whose comments are all
// the run's.
func noCredentialInPullRequest(t *testing.T, pr, issue int, secrets map[string]string) {
	t.Helper()
	for _, text := range pullRequestTexts(t, pr, issue) {
		for what, secret := range secrets {
			if at := leakedPieces([]byte(text.Text), secret); len(at) != 0 {
				t.Errorf("%s holds pieces of %s, at offsets %v of it", text.Name, what, at)
			}
		}
	}
}

// pullRequestTexts reads, through gh, every text noCredentialInPullRequest searches. A read that fails stops the test,
// naming the call alone: what gh printed may hold the secret.
func pullRequestTexts(t *testing.T, pr, issue int) []namedText {
	t.Helper()
	repo := "repos/" + queueRepo
	n := strconv.Itoa(pr)
	texts := ghTexts(t, "api", repo+"/pulls/"+n, "--jq",
		`{name: "the title of \(.html_url)", text: .title}, {name: "the body of \(.html_url)", text: (.body // "")}`)
	texts = append(texts, ghTexts(t, "api", "--paginate", repo+"/pulls/"+n+"/commits?per_page=100", "--jq",
		`.[] | {name: "the message of the commit \(.sha)", text: .commit.message}`)...)
	for _, path := range []string{"/issues/" + n + "/comments", "/pulls/" + n + "/comments",
		"/issues/" + strconv.Itoa(issue) + "/comments"} {
		texts = append(texts, ghTexts(t, "api", "--paginate", repo+path+"?per_page=100", "--jq",
			`.[] | {name: "the comment \(.html_url)", text: (.body // "")}`)...)
	}
	texts = append(texts, ghTexts(t, "api", "--paginate", repo+"/pulls/"+n+"/reviews?per_page=100", "--jq",
		`.[] | {name: "the review \(.html_url)", text: (.body // "")}`)...)
	diff, err := exec.Command("gh", "pr", "diff", n, "--repo", queueRepo).Output()
	if err != nil {
		t.Fatalf("gh pr diff %s --repo %s: %v", n, queueRepo, err)
	}
	return append(texts, namedText{Name: fmt.Sprintf("the diff of %s#%s", queueRepo, n), Text: string(diff)})
}

// ghTexts runs gh with the arguments, whose --jq prints one namedText a line, and decodes them.
func ghTexts(t *testing.T, args ...string) []namedText {
	t.Helper()
	out, err := exec.Command("gh", args...).Output()
	if err != nil {
		t.Fatalf("gh %s: %v", args[:len(args)-2], err)
	}
	var texts []namedText
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		var text namedText
		if err := dec.Decode(&text); errors.Is(err, io.EOF) {
			return texts
		} else if err != nil {
			t.Fatalf("gh %s printed no list of texts: %v", args[:len(args)-2], err)
		}
		texts = append(texts, text)
	}
}
