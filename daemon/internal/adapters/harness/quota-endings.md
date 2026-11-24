# Quota endings

What each supported release of Claude Code and Codex prints when its quota or usage limit is reached, and how the
adapter classifies it. A failover starts only when the adapter reads the ending as an exhausted quota
(`domain.ClassQuotaExhausted`), so a form it does not read that way ends the execution as failed.

Studied on 2026-11-20. No finding here comes from a run that exhausted a real quota: each comes from the CLI's source
at a pinned tag, its published package, or its changelog, as the Source column says. A fixture marked *constructed*
reproduces the printed text from that source, in the shape of the output the adapter reads; it is not a captured run. An ending no source
shows is recorded as **not determined** and left to the real-CLI failover run (#217).

## Codex

Releases covered: the tested release, the newest release when the study ran, and the first release whose `exec`
offers `--json`, `--sandbox`, `--cd`, and `--skip-git-repo-check`, which was the floor the readiness check admitted when
the study ran.

The floor is now 0.48.0, the adapter's `MinVersion` (#1122): a Codex older than it reads `below_minimum` and not
ready. It is the later of two releases. 0.43.0 is the first whose output has the thread and turn schema the adapter
reads (openai/codex commit 7fc3edf8). 0.48.0 is the first whose app server answers `account/rateLimits/read`, the quota
read every attempt makes first. openai/codex#5302 (commit 5c680c6587e5) added it, and `rust-v0.48.0` is the first tag
whose `codex-rs/app-server-protocol/src/protocol.rs` defines it; `rust-v0.47.0` does not.

Every attempt runs through `codex app-server` (#465), so the adapter reads the app server's JSON-RPC notifications, not
`exec --json`'s stream. The classification reads the ending's text alone: the app server ends when the adapter closes
its stdin, and its exit code says nothing of the quota. The exec column stays as the study found it.

| Release | App-server lines | Stream lines (`exec --json`) | Exit code of `exec` | Pre-attempt quota read | Source | Adapter's classification |
|---|---|---|---|---|---|---|
| 0.8.0 (first with the four flags) | None: no app server, so no attempt starts (`codex_app_server_missing`) | Old raw-event format, `{"id":…,"msg":{"type":"error","message":…}}`. No usage-limit error exists: a 429 is retried, then ends as `exceeded retry limit, last status: …` | 0 | None: no app server | openai/codex `rust-v0.8.0` (671f91d3ea7f): `codex-rs/core/src/error.rs`, `codex-rs/exec/src/lib.rs`, `codex-rs/exec/src/event_processor_with_json_output.rs` | **Not ready.** The adapter parses neither the old format nor a success-coded failure, so since #1122 a release below the 0.48.0 floor reads `below_minimum` and runs no attempt. The current schema starts at 0.43.0 (commit 7fc3edf8). |
| 0.163.0 (tested) | `{"method":"error","params":{"willRetry":false,"error":{"message":M}}}` then `{"method":"turn/completed","params":{"turn":{"status":"failed","error":{"message":M}}}}` | `{"type":"error","message":M}` then `{"type":"turn.failed","error":{"message":M}}`. M is one of the usage-limit texts below | 1 | App server `account/rateLimits/read`: `rateLimits` with `primary`/`secondary` windows (`usedPercent`, `resetsAt`) and `rateLimitReachedType` | `rust-v0.163.0` (83086188ae06): `codex-rs/protocol/src/error.rs`, `codex-rs/exec/src/exec_events.rs`, `codex-rs/exec/src/lib.rs`, `codex-rs/app-server-protocol/src/protocol/v2/account.rs` | Quota exhausted for every text below: each `You’ve hit your usage limit…` text, and, since #1120, the workspace-credit, spend-cap, and `Quota exceeded…` texts |
| 0.171.0 (newest) | Not determined | Same as 0.163.0 | 1 | Same as 0.163.0 | `rust-v0.171.0` (ccf9be8cc458), same files | Same as 0.163.0 |

The usage-limit texts of 0.163.0 and 0.171.0, with the curly apostrophe (U+2019) Codex prints:

| Text | Classified as quota | Fixture |
|---|---|---|
| `You’ve hit your usage limit. Upgrade to Pro (…), visit … to purchase more credits or try again at <time>.` (Plus) | yes | `codex/testdata/quota-plus.jsonl`, constructed |
| `You’ve hit your usage limit. … send a request to your admin …` (Team, Business), `… Upgrade to Plus …` (Free, Go), `… purchase more credits …` (Pro) | yes, on `usage limit` | `codex/testdata/quota.jsonl` |
| `You’ve hit your usage limit. Try again later.` (Enterprise, Edu, unknown plan) | yes | `codex/testdata/quota-no-plan.jsonl`, constructed |
| `You’ve hit your usage limit for <model>. Switch to another model now, or try again later.` | yes | `codex/testdata/quota-model-limit.jsonl`, constructed |
| `Your workspace is out of credits. …` (two variants) | yes, since #1120 | `codex/testdata/quota-workspace-owner-credits.jsonl`, `codex/testdata/quota-workspace-member-credits.jsonl`, constructed |
| `You hit your spend cap set … Increase your spend cap to continue.` (two variants) | yes, since #1120 | `codex/testdata/quota-workspace-owner-spend-cap.jsonl`, `codex/testdata/quota-workspace-member-spend-cap.jsonl`, constructed |
| `Quota exceeded. Check your plan and billing details.` | yes, since #1120 | `codex/testdata/quota-exceeded.jsonl`, constructed |

Each fixture is replayed by one test: `TestParseQuota` replays `quota.jsonl` and the fixtures of #1120, and
`TestQuotaEndings` the others.

Not determined: at each tag, which API error maps to `Quota exceeded.` (`insufficient_quota` is the likely one; the
mapping was not traced).

## Claude Code

Release lines covered: 2.0 and 2.1, from `MinVersion` to `TestedMinor`.

| Release | Stream lines (`--output-format stream-json`) | Exit code | Pre-attempt quota read | Source | Adapter's classification |
|---|---|---|---|---|---|
| 2.0.0 | No `rate_limit_event`. The result line `{"type":"result","subtype":"success","is_error":true,"result":T}`, T one of `Session limit reached ∙ resets <t>`, `Weekly limit reached ∙ resets <t>`, `Opus weekly limit reached ∙ resets <t>`, `Usage limit reached ∙ resets <t>`, `Spending cap reached resets <t>`, or the fallback `Claude AI usage limit reached\|<epoch>` | 1 (`is_error`), print path; stream-json path not confirmed | `/usage` is an interactive dialog: not determined whether `-p "/usage"` prints text | `@anthropic-ai/claude-code` 2.0.0 npm bundle; CHANGELOG | Quota exhausted for every text, `Spending cap reached …` since #1121. Fixtures `quota-2.0-session.jsonl`, `quota-2.0-legacy.jsonl`, and `spending-cap.jsonl`, constructed |
| 2.0.77 | No `rate_limit_event`. T is `You've hit your {session limit\|weekly limit\|Opus limit\|Sonnet limit\|usage limit} · resets <t>`, or `You're out of extra usage` | 1, as 2.0.0 | Not determined | 2.0.77 npm bundle; CHANGELOG 2.0.49–2.0.51 | Quota exhausted. Fixtures `quota-2.0.77.jsonl` (`You've hit your weekly limit …`) and `quota-2.0.77-extra-usage.jsonl` (`You're out of extra usage`), constructed |
| 2.1.0–2.1.44 | As 2.0.77 | Not determined | Not determined | 2.1.0, 2.1.20, 2.1.28, 2.1.42, 2.1.44 npm bundles | Quota exhausted, on the result text |
| 2.1.45 on | A `rate_limit_event` with `rate_limit_info` (`status`, `rateLimitType`, `resetsAt`, `overageStatus`, `isUsingOverage`, …) before the result line; a rejected plan window is the limit | Not determined | `claude -p "/usage"` prints `Current session: N% used · resets …` and the week lines in 2.1.329; the first release that prints it headless is not determined | 2.1.45 npm bundle and CHANGELOG 2.1.45; 2.1.329 native package | Quota exhausted from the rejected plan window (`quota.jsonl`), unless the session goes on with extra usage |
| 2.1.312 on | As 2.1.45; the five-hour window's text becomes `You've hit your 5-hour limit · resets <t>`; 2.1.329 adds `You've hit your monthly spend limit.` and `You're out of usage credits` | Not determined | As 2.1.45 | CHANGELOG 2.1.312; 2.1.329 native package | Quota exhausted from the rejected `five_hour` window (`quota-five-hour.jsonl`, constructed), and, since #1121, from the `5-hour limit` text alone (`five-hour-limit.jsonl`, constructed). The spend-limit and usage-credit texts match, each read alone (`quota-monthly-spend-limit.jsonl`, `quota-usage-credits.jsonl`, constructed); the stream lines around them in 2.1.329 are not determined. |

Each fixture is replayed by one test: `TestParseQuota` replays `quota.jsonl`, `TestParseQuotaTextWithNoRateLimitEvent`
the fixtures of #1121 (`spending-cap.jsonl`, `five-hour-limit.jsonl`), and `TestQuotaEndings` the others.

Not determined, left to the real-CLI run (#217): the exact result line and exit code of a 2.1.x session that hits its
limit, and the first release where `claude -p "/usage"` prints the usage report.
