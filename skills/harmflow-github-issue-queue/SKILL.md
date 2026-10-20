---
name: harmflow-github-issue-queue
description: Discovers and orders the work of a HarmFlow GitHub issue task — managed draft pull requests to resume and new eligible issues — applying the task's prompt criteria, the already-processed check, and the queue limits from context.json. Use it before choosing what to implement.
---

# Build the issue queue

The queue is rebuilt from GitHub and from `context.json` every time the task fires; nothing is reserved between
executions. Read `task.parameters.queue` from `context.json`:

| Parameter | Default | Meaning |
|---|---|---|
| `maxDiscoveredPerCycle` | 10 | How many new issues to look at, per repository |
| `maxItemsPerCycle` | 1 | How many work items this execution processes |
| `ordering` | `oldest_first` | `oldest_first` or `newest_first`, by issue creation |
| `resumeStrategy` | `resume_first` | `resume_first`, `new_first`, or `alternate` between the two lists |
| `maxResumeAttempts` | 3 | Resumptions of one item before it needs a human |
| `maxFailedRetries` | 3 | Operational failures of one item before it needs a human |

## 1. Work to resume

For each repository in `task.parameters.repositories`:

```bash
gh pr list --repo OWNER/REPO --state open --draft --json number,url,title,headRefName,labels,body,closingIssuesReferences --limit 100
```

A pull request is **managed** — and may be resumed — only when it carries the marking the task's prompt defines
(by default the label `harmflow`). Never resume a pull request without that marking: it is someone else's work.

Resume a managed draft pull request when its work item (`OWNER/REPO#<issue>`) in `context.json` `workItems` has
status `in_progress` or `failed` — tests failed, or the work was left incomplete or interrupted. Skip it when the item
reads `needs_attention`, or when its `resumes` reached `maxResumeAttempts` or its `failures` exceeded
`maxFailedRetries`; report it with outcome `needs_attention` instead.

## 2. New issues

For each repository, list open issues with the criteria of the task's prompt (labels with `--label`, a search with
`--search`, or one specific issue with `gh issue view`):

```bash
gh issue list --repo OWNER/REPO --state open --limit <maxDiscoveredPerCycle> --json number,title,url,labels,createdAt
```

An issue is **already processed**, and is not new work, when any of these holds:

- a pull request links it — it appears in a pull request's `closingIssuesReferences`, or a pull request body says
  `Closes #<n>`, `Fixes #<n>`, or `Resolves #<n>`;
- it appears in `context.json` `workItems` or `recentExecutions`, whatever its status.

Exclusions the prompt states apply on top of that. No issue is safe just because it is the first one open.

## 3. Order and select

Sort new issues by `ordering`. Merge the two lists by `resumeStrategy`, then take the first `maxItemsPerCycle` items.

For each selected item, append one line to `progress.jsonl`:

```json
{"type":"work_item.selected","workItem":"OWNER/REPO#123","repository":"OWNER/REPO","message":"<issue title>"}
```

If nothing is eligible, the execution's outcome is `no_work`: write `report.json` with
`{"outcome":"no_work","summary":"<what was checked>"}` and stop. That is a normal outcome, not a failure.
