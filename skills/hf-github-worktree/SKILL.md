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

```bash
gh api repos/OWNER/REPO/branches/BASE/protection --silent
```

If this fails with 404, the base branch is not protected: "never merge" then depends only on this session's
discipline. Continue, and append to `progress.jsonl`:

```json
{"type":"repository.unprotected","repository":"OWNER/REPO","message":"BASE has no branch protection; merges without review are possible"}
```

Also add it to the report's `warnings` with the code `repository_unprotected`.

## 2. Create or reuse the worktree

Name the branch as the task's prompt specifies; by default `harmflow/<issue-number>-<short-slug>`. The worktree path
is `<paths.workspace>/<repo-name>-<issue-number>`.

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
worktree of a draft pull request still in progress, and of an item that failed, for diagnosis.

```bash
gh pr view <branch> --repo OWNER/REPO --json state      # MERGED or CLOSED → ended
gh issue view <n> --repo OWNER/REPO --json state        # CLOSED → ended
git -C <local path> worktree remove --force <worktree path>
```

Report an ended item with outcome `closed`.
