<p align="center">
  <img src=".github/assets/banner.svg"
       alt="HarmFlow — the daemon, the desktop, and the installers, in one monorepo" width="100%">
</p>

<p align="center">
  <img alt="Status: MVP in progress"
       src="https://img.shields.io/badge/status-MVP%20in%20progress-f59e0b?style=flat-square&labelColor=0b1020">
  <img alt="Platform: macOS first"
       src="https://img.shields.io/badge/platform-macOS%20first-22d3ee?style=flat-square&labelColor=0b1020">
  <img alt="Harnesses: Claude Code and Codex"
       src="https://img.shields.io/badge/harnesses-Claude%20Code%20%C2%B7%20Codex-8b5cf6?style=flat-square&labelColor=0b1020">
  <a href="https://github.com/harmflow/harmflow-app/actions/workflows/ci.yml"><img alt="CI"
       src="https://img.shields.io/github/actions/workflow/status/harmflow/harmflow-app/ci.yml?branch=main&style=flat-square&label=CI&labelColor=0b1020"></a>
  <a href="LICENSE"><img alt="License: PolyForm Noncommercial 1.0.0"
       src="https://img.shields.io/badge/license-PolyForm%20Noncommercial-e040fb?style=flat-square&labelColor=0b1020"></a>
</p>

<p align="center">
  <img alt="Go 1.24"
       src="https://img.shields.io/badge/Go-1.24-00ADD8?style=flat-square&logo=go&logoColor=white&labelColor=0b1020">
  <img alt="Tauri 2"
       src="https://img.shields.io/badge/Tauri-2-24C8DB?style=flat-square&logo=tauri&logoColor=white&labelColor=0b1020">
  <img alt="Rust stable"
       src="https://img.shields.io/badge/Rust-stable-f74c00?style=flat-square&logo=rust&logoColor=white&labelColor=0b1020">
  <img alt="React and TypeScript"
       src="https://img.shields.io/badge/React-TypeScript-61DAFB?style=flat-square&logo=react&logoColor=white&labelColor=0b1020">
</p>

<p align="center">
  <b>The engine room of HarmFlow.</b><br>
  The daemon that schedules and runs tasks on the agent harnesses already on your machine — Claude Code and Codex —<br>
  the desktop that follows every execution, and the installers that ship them.
</p>

<p align="center">
  <a href="#architecture">Architecture</a> ·
  <a href="#layout">Layout</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#commands">Commands</a> ·
  <a href="#a-task-is-a-file">Tasks</a> ·
  <a href="#how-a-task-runs">How a task runs</a>
</p>

> [!IMPORTANT]
> **MVP in progress, macOS first.** Nothing here is a signed release: until the Developer ID exists, builds are
> unsigned and Gatekeeper asks for confirmation on first open. Specifications, ADRs, and architecture live in a
> private repository; the `FS-`, `FR-`, `AC-`, `D-`, and `ADR-` numbers in code, tests, and schemas name its records.

## Architecture

<p align="center">
  <img src=".github/assets/architecture.svg"
       alt="The desktop, a Tauri 2 shell with a React interface, talks to the daemon harmflowd through a local HTTP and
            SSE API on the loopback. The daemon starts Claude Code or Codex, which reports its progress back, and keeps
            tasks, state, event logs, and execution directories in its data directory. The contracts generate the
            copies the daemon and the desktop use." width="100%">
</p>

## Layout

<table>
  <tr>
    <td width="33%" valign="top">
      <code>01</code> <a href="contracts"><b>contracts/</b></a><br>
      The local API (<code>api/openapi.yaml</code>) and the JSON Schemas of tasks, events, and execution reports —
      the single source of truth for every name.
    </td>
    <td width="33%" valign="top">
      <code>02</code> <a href="daemon"><b>daemon/</b></a><br>
      <code>harmflowd</code>, in Go: domain, use cases, ports, and adapters — Claude Code, Codex, YAML files, JSON
      state, JSONL events, HTTP + SSE, Keychain, launchd.
    </td>
    <td width="33%" valign="top">
      <code>03</code> <a href="desktop"><b>desktop/</b></a><br>
      The desktop shell: <code>src-tauri</code> (Tauri 2, Rust — tray, notifications, the daemon connection) and
      <code>ui</code> (React, TypeScript).
    </td>
  </tr>
  <tr>
    <td width="33%" valign="top">
      <code>04</code> <a href="agents"><b>agents/</b></a> · <a href="skills"><b>skills/</b></a><br>
      The built-in agent and skills of the GitHub issue implementation type, prefixed <code>harmflow-</code>.
    </td>
    <td width="33%" valign="top">
      <code>05</code> <a href="packaging"><b>packaging/</b></a><br>
      What Tauri's bundle does not cover: signing and per-OS packaging notes.
    </td>
    <td width="33%" valign="top">
      <code>06</code> <a href="tests"><b>tests/</b></a><br>
      Tests across parts: the real daemon binary driven over its API, with stand-in harness CLIs.
    </td>
  </tr>
</table>

## Quick start

| You need | Version | For |
|---|---|---|
| Go | 1.24 | the daemon |
| Node with pnpm | 24 · 10 | the interface |
| Rust | stable | the desktop shell |
| Claude Code or Codex | signed in | running tasks |
| Git and the GitHub CLI (`gh`) | signed in | the GitHub issue type |
| `rsvg-convert` (`brew install librsvg`) | any | `make icons` only — the rendered icons are committed |
| `cargo-llvm-cov`, with Rust's `llvm-tools-preview`, and `jq` | any | `make coverage` only |

HarmFlow never installs, updates, or signs in to any of them. With those in place:

```bash
make deps          # once: the desktop's JavaScript dependencies
make dev-daemon    # the daemon, on .harmflow-dev/home
make dev-ui        # in a second terminal: the interface at http://localhost:1420
```

Dependabot proposes monthly updates to the Go, pnpm, and Cargo dependencies and to the CI actions; a major update comes
in its own pull request and is reviewed on its own.

## Commands

| Command | What it does |
|---|---|
| `make help` | List every target |
| `make deps` | Install the desktop's JavaScript dependencies |
| `make test` | Run the daemon, desktop, and integration tests |
| `make coverage` | Read the tests' coverage of the daemon, the interface, and the shell, and fail below a floor; the floors and their reading are in [`COVERAGE.md`](COVERAGE.md) |
| `make dev-daemon` | Run the daemon on `.harmflow-dev/home` |
| `make dev-ui` | Serve the interface in a browser, proxied to that daemon (`http://localhost:1420`) |
| `make dev-desktop` | Run the desktop shell against that daemon |
| `make generate` | Refresh the copies generated from `contracts/`; `make check-generated` fails when one is stale |
| `make app` | Build `HarmFlow.app` and the DMG, unsigned |
| `make update-app` | On `main`, fast-forward and run `make app`, then restart on the new build the daemon and the window that were running; refused while executions run (see [`packaging/`](packaging/README.md#updating-the-local-app)) |
| `make icons` | Render every app and tray icon from the SVGs in `desktop/src-tauri/icons/source` |

Three environment variables change where the daemon keeps its data and its token:

| Variable | Default | Effect |
|---|---|---|
| `HARMFLOW_HOME` | `~/Library/Application Support/HarmFlow` | The data directory |
| `HARMFLOW_TOKEN_STORE` | the Keychain | `file` keeps the local API token in a user-only file instead |
| `HARMFLOW_KEYCHAIN` | the default keychain | The keychain file that holds the token, for tests; the desktop reads the token from it too |

## A task is a file

One YAML file per task, in the data directory's `tasks/`, validated by
[`contracts/schemas/task.schema.json`](contracts/schemas/task.schema.json):

```yaml
apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: nightly-issues
  name: Implement ready issues
spec:
  type: github-issue-implementation           # or general, with no type parameters
  prompt: |
    Select one open issue labeled ready-for-agent, oldest first.
    Implement it following the repository's conventions and run its tests.
    Open a draft pull request that reports what passed and what failed.
  agents: [harmflow-github-issue-implementer]
  skills: [harmflow-github-issue-queue, harmflow-github-worktree, harmflow-github-draft-pr]
  schedule:
    cron: "0 2 * * 1-5"                       # weeknights at 02:00
    missedRun: {policy: run_on_resume}        # asleep at 02:00? run on wake
  harness:
    preferred: {provider: claude-code}
    fallback:
      candidates: [{provider: codex}]         # quota exhausted or unavailable → Codex
  permissions: {profile: workspace_write, network: allow}
  githubIssueImplementation:
    repositories:
      - {name: acme/api, localPath: ~/code/acme/api}
```

## How a task runs

<table>
  <tr>
    <td width="33%" valign="top">
      <code>01</code> <b>Fire</b><br>
      On its cron or interval, on wake for a run the machine slept through, or by hand.
    </td>
    <td width="33%" valign="top">
      <code>02</code> <b>Start</b><br>
      The daemon starts the task's harness non-interactively, with the task's prompt, agents, and skills, in a
      workspace.
    </td>
    <td width="33%" valign="top">
      <code>03</code> <b>Govern</b><br>
      The task's permission profile is translated to the harness's own controls; one it cannot enforce runs with a
      warning.
    </td>
  </tr>
  <tr>
    <td width="33%" valign="top">
      <code>04</code> <b>Report</b><br>
      The session leaves progress lines and a report in its execution directory, under <code>runs/</code>.
    </td>
    <td width="33%" valign="top">
      <code>05</code> <b>Stream</b><br>
      The daemon records them and streams every event to the dashboard over SSE.
    </td>
    <td width="33%" valign="top">
      <code>06</code> <b>Fail over &amp; notify</b><br>
      An exhausted quota or an unavailable harness moves the task to the next harness. Then the daemon notifies.
    </td>
  </tr>
</table>

HarmFlow itself implements no task behavior: the harness, its agents, and its skills do the work.

## Part of HarmFlow

- **Product overview** — the [HarmFlow organization](https://github.com/harmflow) page.
- **Website** — [`harmflow-site`](https://github.com/harmflow/harmflow-site).
- **Ask or report a bug** — [open an issue](https://github.com/harmflow/harmflow-app/issues) here; a vulnerability
  goes privately through the [security policy](https://github.com/harmflow/.github/blob/main/SECURITY.md), never in
  a public issue.
- **Contribute** — pull requests are open to authorized collaborators only for now; see the
  [contribution guide](https://github.com/harmflow/.github/blob/main/CONTRIBUTING.md).

<p align="center"><sub>Automate the repetitive. Amplify what matters.</sub></p>
