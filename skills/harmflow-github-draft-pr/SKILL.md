---
name: harmflow-github-draft-pr
description: Tests a HarmFlow work item, repairs failing tests within the task's limit, commits and pushes only the work branch, and opens or updates exactly one draft pull request that documents the work — even when tests fail. Never merges. Use it after implementing an issue in its worktree.
---

# Test, publish, and document

## 1. Test

Find how the repository runs its tests — its README, CONTRIBUTING, CI workflow, `Makefile`, `package.json`,
`pyproject.toml`, `go.mod`, `build.gradle` — and run them in the worktree. Before and after, append to
`progress.jsonl`:

```json
{"type":"test.started","workItem":"OWNER/REPO#123","message":"<test command>"}
{"type":"test.completed","workItem":"OWNER/REPO#123","status":"passed|failed|not_run","message":"<one-line result>"}
```

If tests fail, try to fix them up to `task.parameters.delivery.testRepairAttempts` times (default 2), running them
again after each fix. Fix the code, never the tests' intent: do not delete, skip, or weaken a test to make it pass.
When no test suite exists, the status is `not_run`.

## 2. Commit and push the work branch

Append `{"type":"step.started","phase":"publishing","message":"Publishing OWNER/REPO#123"}`, then:

```bash
git -C <worktree> add -A
git -C <worktree> commit -m "<type>: <summary> (#<issue>)"
git -C <worktree> push -u origin <branch>
```

Follow the repository's commit conventions. Push only the work branch — never the base branch, never with `--force`
over commits you did not make. If the push is rejected because the branch moved, fetch and rebase your commits on it,
and record the conflict in the report's warnings (`conflicting_activity`).

## 3. Open or update exactly one draft pull request

First look for one that already exists for the branch — this is what makes a repeated or resumed execution safe:

```bash
gh pr list --repo OWNER/REPO --head <branch> --state open --json number,url,isDraft
```

- **One exists:** update it — push the new commits, then `gh pr edit <number> --repo OWNER/REPO --body-file <file>`.
- **None exists:** `gh pr create --repo OWNER/REPO --draft --base BASE --head <branch> --title "<title>" --body-file <file>`,
  adding the task's marking (by default `--label harmflow`; create the label first with
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

Managed by HarmFlow task <task-id> (execution <execution-id>).
```

Then append:

```json
{"type":"pull_request.created","workItem":"OWNER/REPO#123","url":"<url>","number":<n>,"branch":"<branch>"}
```

(`pull_request.updated` when it already existed).

## 4. Report

Add the item to `report.json` `workItems`:

```json
{"key":"OWNER/REPO#123","kind":"new|resume","repository":"OWNER/REPO","issueNumber":123,"title":"...",
 "branch":"...","worktreePath":"...","outcome":"completed|failed|needs_attention",
 "pullRequest":{"number":45,"url":"...","draft":true,"created":true},
 "tests":{"status":"passed|failed|not_run","summary":"..."},
 "commits":["<sha>"],"changedFiles":["path"]}
```

The item's outcome is `completed` when its draft pull request was created or updated, even with failing tests;
`failed` when the pull request could not be opened (keep the branch and the worktree); `needs_attention` when a human
must decide. Set the report's `lastOperation` to the last external operation applied, such as
`pushed harmflow/123-fix to OWNER/REPO` or `opened draft PR OWNER/REPO#45`.
