---
name: hf-github-worktree
description: Prepares an isolated Git worktree and work branch for one HarmFlow work item — new or resumed — under the execution's workspace, checks that the base branch is protected against merging without review, and removes worktrees of items that ended. Use it before changing any code for an issue.
---

# Prepare the worktree

Each work item gets its own worktree on its own branch, so parallel work never collides and the base branch is never
modified directly. The repository's local path and base branch are in `context.json`
(`task.parameters.repositories[]`, and `paths.repositories` for the resolved local path). Worktrees live under the
execution's workspace (`paths.workspace`).

## 1. Check the base branch's protection

A branch is protected by classic branch protection, by the rules of a ruleset, or by both, and either one alone may
require the review. Read both; each call prints `review`, `pull_request`, or `none`:

```bash
gh api repos/OWNER/REPO/branches/BASE/protection --jq 'if .required_pull_request_reviews == null then "none" elif .required_pull_request_reviews.required_approving_review_count >= 1 then "review" else "pull_request" end'
gh api "repos/OWNER/REPO/rules/branches/BASE?per_page=100" --jq '[.[] | select(.type == "pull_request") | .parameters.required_approving_review_count] | if length == 0 then "none" elif max >= 1 then "review" else "pull_request" end'
```

- `review`: a merge needs a pull request and at least one approving review.
- `pull_request`: a merge needs a pull request, but no approving review.
- `none`: no pull request is required. A call that fails answers `none`, whatever it printed: a 404 from the first is a
  branch with no classic protection, never an unprotected branch by itself, and a 403 is a plan or a token that cannot
  read the protection.

The stronger of the two answers decides — `review`, then `pull_request`, then `none` — and gives the message:

| Answer | Message |
|---|---|
| `review` | None: the branch is protected against merging without review, and nothing is reported |
| `pull_request` | `BASE requires a pull request but no approving review; merges without review are possible` |
| `none` | `BASE has no branch protection; merges without review are possible` |

With a message, "never merge" depends only on this session's discipline. Continue, and append to `progress.jsonl` the
message as the table writes it, with only BASE replaced by the branch's name:

```json
{"type":"repository.unprotected","repository":"OWNER/REPO","message":"<message>"}
```

Also add it to the report's `warnings` with the code `repository_unprotected` and the same message. Never word the
message yourself, nor add to it what the calls printed.

## 2. Create or reuse the worktree

Name a new item's branch as the task's prompt specifies; by default `harmflow/<issue-number>-<short-slug>`. A resumed
managed draft pull request is worked on its own head branch, the `headRefName` hf-github-issue-queue §1 lists for it,
never on a branch named afresh, even when the prompt would name it differently now: a branch of another name finds no
pull request and starts from the base branch, as a new item's would. The worktree path is
`<paths.workspace>/<repo-name>-<issue-number>`.

```bash
cd <local path>
git fetch origin BASE
```

- **New item, branch not on the remote, worktree missing:**
  `git worktree add -b <branch> <worktree path> origin/BASE`
- **Resumed or retried item, branch on the remote:** `git fetch origin <branch>`, then reuse the worktree if it
  exists (`git -C <worktree path> pull --ff-only origin <branch>`), or recreate it with
  `git worktree add <worktree path> <branch>` (tracking `origin/<branch>`).
- **Retried item, branch not on the remote:** the failed attempt may have left the branch and its worktree, kept for
  diagnosis (§3). Reuse the worktree if it exists, keeping its commits; recreate it with
  `git worktree add <worktree path> <branch>` if only the local branch exists; otherwise create it as a new item.
- **The worktree exists but is broken:** `git worktree prune`, then recreate it. Never delete a branch that exists on
  the remote.

Then append to `progress.jsonl`:

```json
{"type":"workspace.changed","workItem":"OWNER/REPO#123","branch":"<branch>","message":"worktree ready at <worktree path>"}
```

and `{"type":"step.started","phase":"running","message":"Implementing OWNER/REPO#123"}` before you start changing
code. Work only inside the worktree.

## 3. Remove worktrees whose item ended

A worktree is removed when its item ended — its pull request was merged or closed, or its issue was closed. Keep the
worktree of a draft pull request still in progress, and of an item that failed, for diagnosis: **Clean workspaces**
removes that one once its execution's log expires.

Every execution does this, whether or not it selects work, so a worktree never outlives its item while the task finds
nothing to do. Go through the task's work items in `context.json` `workItems` whose repository is one of
`task.parameters.repositories`, whether or not their worktree is still on disk. Skip an item that reads `closed`,
whose end is already recorded, and one that reads `failed`. For each other item, read its pull requests from its
`branch`, when it records one, and its issue. Each call prints one word:

```bash
gh api "repos/OWNER/REPO/pulls?head=OWNER:BRANCH&state=all" --jq 'if length == 0 then "none" elif any(.[]; .state == "open") then "open" elif any(.[]; .merged_at != null) then "merged" else "closed" end'
gh api repos/OWNER/REPO/issues/ISSUE --jq .state
```

The item ended when the first prints `merged` or `closed`, or the second prints `closed`. A call that fails says
nothing, and the worktree stays. Remove the worktree of an item that ended, when it is on disk, at the path §2 gives
it; the work branch stays, local and remote:

```bash
git -C "<local path>" worktree remove --force "<worktree path>"
git -C "<local path>" worktree prune
```

Report each item that ended in `report.json` `workItems` with the outcome `closed`, and only its `key`,
`repository`, and `worktreePath`. Give it no `kind`, `pullRequest`, or `tests`, which would count a resumption or
make its pull request the execution's, and list it after any item the execution selected, whose record takes the
first item as its own. Write no `work_item.selected` line for it, which would put the item back in progress.
