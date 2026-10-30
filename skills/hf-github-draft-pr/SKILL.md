---
name: hf-github-draft-pr
description: Tests a HarmFlow work item, repairs failing tests within the task's limit, commits and pushes only the work branch, and opens or updates exactly one draft pull request that documents the work — even when tests fail. Never merges. Use it after implementing an issue in its worktree.
---

# Test, publish, and document

Run each command on its own, one per call, as this skill writes it: reach the worktree with `git -C <worktree>` and
full paths, never with a `cd` chained before other commands, and keep paths out of shell variables. Claude Code asks
for approval before it runs a `cd` chained with a redirection such as `2>&1`, or a command whose variable it cannot
check, whatever the task's permissions allow. An unattended session has nobody to give it, so the command is refused:
a push refused that way leaves the work with no pull request.

## 1. Test

Find how the repository runs its tests — its README, CONTRIBUTING, CI workflow, `Makefile`, `package.json`,
`pyproject.toml`, `go.mod`, `build.gradle` — and run them in the worktree. Before and after each run, append to
`progress.jsonl`:

```json
{"type":"test.started","workItem":"OWNER/REPO#123","message":"<test command>"}
{"type":"test.completed","workItem":"OWNER/REPO#123","status":"passed|failed|not_run","message":"<one-line result>"}
```

If tests fail, try to fix them up to `task.parameters.delivery.testRepairAttempts` times (default 2), running them
again after each fix. Fix the code, never the tests' intent: do not delete, skip, or weaken a test to make it pass.
The run after a fix is a run of its own, with its own `test.started` and `test.completed` lines, so the log shows each
run: at most `testRepairAttempts` + 1 before you publish. When no test suite exists, the status is `not_run`.

## 2. Commit the work

Append `{"type":"step.started","phase":"publishing","message":"Publishing OWNER/REPO#123"}`, then:

```bash
git -C <worktree> add -A
git -C <worktree> commit -m "<type>: <summary> (#<issue>)"
```

Follow the repository's commit conventions.

## 3. Look for the item's pull request

Before you push, look for an open pull request of the item in two ways — this is what makes a repeated or resumed
execution safe. The branch name alone is not enough: a session that names the branch differently from an earlier one,
or a task whose prompt changed its branch naming, would find nothing from its branch and open a second pull request
for the same issue. Each call prints the open pull requests it finds, as a JSON array, `[]` when there is none:

```bash
gh api "repos/OWNER/REPO/pulls?head=OWNER:<branch>&state=open" --jq '[.[] | {number, url: .html_url, headRefName: .head.ref}]'
gh api graphql -F owner=OWNER -F repo=REPO -f query='query($owner: String!, $repo: String!) {
  repository(owner: $owner, name: $repo) { defaultBranchRef { name } pullRequests(states: OPEN, first: 100) { nodes {
    number url headRefName baseRefName closingIssuesReferences(first: 25) { nodes { number } } } } } }' --jq '.data.repository | .defaultBranchRef.name as $default | [.pullRequests.nodes[] | select(any(.closingIssuesReferences.nodes[]; .number == <issue>) or (.baseRefName != $default and (.headRefName | split("/") | last | test("^<issue>(-|$)")))) | {number, url, headRefName}]'
```

The first reads the pull requests from the work branch. The second reads the open pull requests of the item's issue,
from any branch, as hf-github-issue-queue §1 matches a managed draft to its issue, so that selecting and publishing
agree on what the item's pull request is. A pull request is the issue's when its `closingIssuesReferences` holds the
issue. GitHub links a pull request's `Closes #<n>` to its issue only when the pull request goes into the repository's
default branch, so one into another branch — the pull request of a task whose base branch is not the default one — is
also the issue's when its `headRefName` carries the issue's number: the number that starts the last segment of the
name, followed by a hyphen or by nothing, which hf-github-worktree §2 puts in every work branch.
`harmflow/42-retry-limit` carries 42; `harmflow/142-retry-limit` does not.

- **The first finds one:** it is the item's pull request. Push, and update it (§4). Another pull request the second
  finds is a second one already open for the issue: name it in the report's warnings (`conflicting_activity`).
- **Only the second finds one:** an open pull request from another branch is the issue's, so the item already has
  its pull request, on a branch this session did not work on. Push nothing, open no pull request, and never move your
  commits onto that branch, which could overwrite or tangle work a person or another firing put there. The item's
  outcome is `needs_attention` (§5), and the report's warnings get a `conflicting_activity` entry that names the work
  branch, that pull request's branch, and its URL. Your commits stay on the local work branch, in its worktree, for a
  person to decide.
- **Neither finds one:** push, and open the pull request (§4).

A call that fails decides nothing: push nothing and open no pull request, and the item's outcome is `failed`, as when
the pull request could not be opened.

## 4. Push, and open or update exactly one draft pull request

```bash
git -C <worktree> push -u origin <branch>
```

Push only the work branch — never the base branch, never with `--force` over commits you did not make. If the push is
rejected because the branch moved, fetch and rebase your commits on it, and record the conflict in the report's
warnings (`conflicting_activity`).

- **§3 found one from the work branch:** update it — `gh pr edit <number> --repo OWNER/REPO --body-file <file>`.
- **§3 found none:** `gh pr create --repo OWNER/REPO --draft --base BASE --head <branch> --title "<title>" --body-file <file>`,
  adding the label of the task's marking (by default `--label harmflow`; create the label first with
  `gh label create harmflow --repo OWNER/REPO --color 7C3AED` if it does not exist).

Always draft, also when tests fail. **Never run `gh pr merge`, `gh pr ready`, or enable auto-merge.**

The body has these sections:

```markdown
Closes #<issue>

## Summary
What was implemented, and why this way.

## Tests
The commands run and their result — what passed and what failed.

## Pending
What is still missing or needs a reviewer's decision.

<marking line>
```

`<marking line>` is the line the task's prompt marks its pull requests with, as the prompt states it: by default
`Managed by HarmFlow task <task-id>.`, where `<task-id>` is `task.id` in `context.json`. When the prompt's marking
names no line, leave it out. The body carries no other line that marks it, and names no execution: the execution's
report and events already link the execution to its pull request. Then append:

```json
{"type":"pull_request.created","workItem":"OWNER/REPO#123","url":"<url>","number":<n>,"branch":"<branch>"}
```

(`pull_request.updated` when it already existed).

## 5. Report

Add the item to `report.json` `workItems`:

```json
{"key":"OWNER/REPO#123","kind":"new|resume","repository":"OWNER/REPO","issueNumber":123,"title":"...",
 "branch":"...","worktreePath":"...","outcome":"completed|failed|needs_attention",
 "pullRequest":{"number":45,"url":"...","draft":true,"created":true},
 "tests":{"status":"passed|failed|not_run","summary":"..."},
 "commits":["<sha>"],"changedFiles":["path"]}
```

Write every field the example shows. In `pullRequest`, `draft` is true, since the pull request is always a draft, and
`created` is true when this execution opened the pull request, false when it updated one already open: HarmFlow
records which of the two happened from `created`, and reads a pull request without it as updated.

`kind` is `resume` for a managed draft pull request resumed, and `new` for a new issue or a failed one retried
(hf-github-issue-queue §1). The item's outcome is `completed` when its draft pull request was created or updated,
even with failing tests; `failed` when the pull request could not be opened (keep the branch and the worktree);
`needs_attention` when a human must decide, as when §3 finds the item's pull request only from another branch, and
then the item has no `pullRequest`. Set the report's `lastOperation` to the last external operation applied, such as
`pushed harmflow/123-fix to OWNER/REPO` or `opened draft PR OWNER/REPO#45`.
