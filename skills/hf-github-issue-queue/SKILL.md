---
name: hf-github-issue-queue
description: Discovers and orders the work of a HarmFlow GitHub issue task — managed draft pull requests to resume, failed issues to retry, and new eligible issues — applying the task's prompt criteria, the already-processed check, and the queue limits from context.json. Use it before choosing what to implement.
---

# Build the issue queue

The queue is rebuilt from GitHub and from `context.json` every time the task fires; nothing is reserved between
executions. Read `task.parameters.queue` from `context.json`:

| Parameter | Default | Meaning |
|---|---|---|
| `maxDiscoveredPerCycle` | 10 | How many eligible new issues a cycle discovers, across all the task's repositories together (§2); excluded and already-processed issues do not count |
| `maxItemsPerCycle` | 1 | How many work items this execution processes |
| `ordering` | `oldest_first` | `oldest_first` or `newest_first`, by issue creation — the order discovery reads issues in (§2) |
| `resumeStrategy` | `resume_first` | `resume_first`, `new_first`, or `alternate` between the two lists |
| `maxResumeAttempts` | 3 | Resumptions of one item before it needs a human |
| `maxFailedRetries` | 3 | Operational failures of one item before it needs a human |

## 1. Work to resume and to retry

For each repository in `task.parameters.repositories`:

```bash
gh api repos/OWNER/REPO --jq .default_branch
gh pr list --repo OWNER/REPO --state open --draft --json number,url,title,headRefName,baseRefName,labels,body,closingIssuesReferences --limit 100
```

A pull request is **managed** — and may be resumed — only when it carries the marking the task's prompt defines
(by default the label `harmflow`). Never resume a pull request without that marking: it is someone else's work.

A draft's issue is the one its `closingIssuesReferences` holds. GitHub links a pull request's `Closes #<n>` to its
issue only when the pull request goes into the repository's default branch, which the first command prints, so a draft
whose `baseRefName` is another branch — the draft of a task whose base branch is not the default one — holds none
there. Its issue is then the number its `headRefName` carries: the number that starts the last segment of the name,
followed by a hyphen or by nothing, which hf-github-worktree §2 puts in every work branch. `harmflow/42-retry-limit`
carries 42; `harmflow/142-retry-limit` does not. hf-github-draft-pr §3 matches a pull request to its issue the same
way.

Resume a managed draft pull request when its work item (`OWNER/REPO#<issue>`) in `context.json` `workItems` has
status `in_progress` or `failed` — tests failed, or the work was left incomplete or interrupted. Skip it when the item
reads `needs_attention`, or when its `resumes` reached `maxResumeAttempts` or its `failures` exceeded
`maxFailedRetries`; report it with outcome `needs_attention` instead, and give it no `kind`, `pullRequest`, or
`tests`: the execution neither resumed nor updated it, and HarmFlow would count a `kind` `resume` as a resumption
and record a `pullRequest` as one this execution updated. A resumed item's branch is the pull request's `headRefName`
from the listing above, which hf-github-worktree §2 works on.

An issue whose processing failed before any pull request linked it — a push that failed, `gh` unreachable, a daemon
restart mid-run — is **work to retry**. Take as work to retry each work item in `context.json` `workItems` that
reads `failed`, belongs to a repository in `task.parameters.repositories`, and no managed draft pull request above
resumes, when all of these hold:

- no pull request links its issue, in any state — neither one GitHub links to the issue nor one from the item's
  `branch`, when it records one:

  ```bash
  gh api graphql -F owner=OWNER -F repo=REPO -F n=<issue> -f query='query($owner: String!, $repo: String!, $n: Int!) {
    repository(owner: $owner, name: $repo) { issue(number: $n) { state
      closedByPullRequestsReferences(first: 10, includeClosedPrs: true) { nodes { number } } } } }'
  gh pr list --repo OWNER/REPO --state all --head <branch> --json number
  ```

- the issue is still `OPEN`, and still meets the prompt's criteria;
- its `failures` have not exceeded `maxFailedRetries`.

A failed or interrupted execution in `recentExecutions` does not keep it out. An item that reads `needs_attention`
is never retried. Work to retry is not new work: it never counts against `maxDiscoveredPerCycle`, and §2 never takes it.
Report it with `kind` `new` — no pull request is resumed, so it counts toward `maxFailedRetries` only.

Finding work to resume or to retry here selects nothing yet: build §2's list too, and let §3 decide which comes first.

## 2. New issues

New work is the cycle's first `maxDiscoveredPerCycle` **eligible** issues across all the task's repositories together,
in the task's `ordering`: the oldest ones for `oldest_first`, the newest ones for `newest_first`. The limit is the
cycle's, not each repository's: the eligible issues of every repository form one list, by creation date, never one
repository's issues after another's. Eligibility is decided before the list is cut — an issue the prompt's criteria
exclude, or one already processed, does not count against the limit, so read past it.

When the prompt names one specific issue, read it with
`gh issue view <n> --repo OWNER/REPO --json number,title,url,labels,state,createdAt` instead of listing. Otherwise,
read each repository in `task.parameters.repositories` the same way: its open issues one page at a time, from page 1,
in the task's order:

```bash
gh api "repos/OWNER/REPO/issues?state=open&sort=created&direction=<asc|desc>&per_page=100&page=<n>" \
  --jq '{fetched: length, issues: [.[] | select(.pull_request | not) | {number, title, url: .html_url, labels: [.labels[].name], createdAt: .created_at}]}'
```

- `direction` is `asc` for `oldest_first` and `desc` for `newest_first`. Always set it: the endpoint's default is
  newest first.
- The endpoint lists pull requests too. The `--jq` filter drops them; `fetched` counts what the page held before that.
- Narrow the listing with the prompt's criteria where the endpoint can: `labels=a,b` (an issue must carry every label
  listed; URL-encode the names), `milestone=`, `assignee=`, `creator=`, `mentioned=`. Check the other criteria on
  each issue yourself — one label out of several, an excluded label, words in the title or the body (add `body` to
  the `--jq` projection).
- Do not list with `gh issue list`: it cannot return the oldest issues first, and with `--label` or `--search` it
  reads GitHub's search index, which can lag behind or return nothing for issues that exist. Search only for a
  criterion the listing cannot check, such as words in comments, and put the order in the query
  (`sort:created-desc` for `newest_first`):

  ```bash
  gh issue list --repo OWNER/REPO --state open --search "<terms> sort:created-asc" --limit 1000 --json number,title,url,labels,createdAt
  ```

  Read its result in order as below. An empty result does not prove that no issue matches.

Go through each repository's issues in the order they came, page after page, and keep each one that meets the prompt's
criteria and is not already processed (below). Stop reading a repository when you have kept `maxDiscoveredPerCycle`
of its issues, the most the cycle's list can take from one repository; when a page's `fetched` is below 100 (it was
the last page); or after 10 pages — 1,000 open issues and pull requests. If you stop at that cap, go on with what you
kept and say so in the report's `summary`.

Then put the issues kept from every repository in one list by their `createdAt`, oldest first for `oldest_first`,
newest first for `newest_first`, and keep its first `maxDiscoveredPerCycle`: those are the cycle's new work, in that
order. Issues of two repositories created at the same time keep the order of `task.parameters.repositories`.

An issue is **already processed**, and is not new work, when any of these holds:

- it appears in `context.json` `workItems` or `recentExecutions`, whatever its status. A `failed` item §1 takes as
  work to retry is retried there, never taken here as new work.
- a pull request links it, in any state: an open draft, an open pull request ready for review, a closed one, or a
  merged one, whoever opened it. GitHub links a pull request to the issue its body closes with `Closes #<n>`,
  `Fixes #<n>`, or `Resolves #<n>`, or that its Development sidebar names. §1's listing holds only open drafts, so
  never infer the link from it: read the issue's links with the query of §1's retry lookup, which reads closed pull
  requests too, and count the issue processed when `nodes` holds any pull request.

  ```bash
  gh api graphql -F owner=OWNER -F repo=REPO -F n=<issue> -f query='query($owner: String!, $repo: String!, $n: Int!) {
    repository(owner: $owner, name: $repo) { issue(number: $n) { state
      closedByPullRequestsReferences(first: 10, includeClosedPrs: true) { nodes { number } } } } }'
  ```

Exclusions the prompt states apply on top of that. No issue is safe just because it is the first one open.

Check an issue in this order: the prompt's criteria, then the history, then the query, so that the query runs only on
the few issues that could become new work. Neither exception to this check is new work: a managed draft pull request
is resumed under §1, and a `failed` item no pull request links is retried under §1.

## 3. Order and select

The work of §1 is one list: the managed draft pull requests to resume, then the work to retry. New issues are the one
list of §2, across all the task's repositories, in the task's `ordering` — never one repository's issues after
another's. Build both lists before you select anything, then merge them by `resumeStrategy` and take the first
`maxItemsPerCycle` items:

- `resume_first`: every item of §1 — the drafts to resume, then the work to retry — and then the new issues.
- `new_first`: every new issue, and then every item of §1. Work to retry is an item of §1 like a draft to resume: under
  `new_first`, a new eligible issue comes before it, whatever their ages.

For example, `context.json` lists `OWNER/REPO#12` as `failed`, with `failures: 1`, no pull request links it, and
`maxFailedRetries` is 3, so it is work to retry. `OWNER/REPO#15`, opened later, is a new eligible issue, and nothing
is left to resume. With `maxItemsPerCycle: 1`, `resume_first` selects `#12`, and `new_first` selects `#15`.

For each selected item, append one line to `progress.jsonl`. Its `message` is the issue's title, then ` — from `
and the list the item came from: `drafts to resume`, `work to retry`, or `new issues`.

```json
{"type":"work_item.selected","workItem":"OWNER/REPO#123","repository":"OWNER/REPO","message":"<issue title> — from new issues"}
```

Then read its issue's assignees, the logins it prints, and keep them: hf-github-draft-pr §3 reads them again before it
publishes, and an assignee the issue did not have here is activity that conflicts with the work.

```bash
gh api repos/OWNER/REPO/issues/<issue> --jq '[.assignees[].login]'
```

If nothing is eligible, the execution's outcome is `no_work`: write `report.json` with
`{"outcome":"no_work","summary":"<what was checked>"}`, its `workItems` holding the items whose worktree
hf-github-worktree §3 found ended, and stop. That is a normal outcome, not a failure.
