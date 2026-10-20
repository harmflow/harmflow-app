# harmflow-app

HarmFlow is a local-first desktop application that schedules, runs, and monitors automations carried out by the agent
harnesses already installed on your machine — Claude Code and Codex. This monorepo holds the daemon, the desktop, the
installers, and the tests. Specifications, ADRs, and architecture live in `harmflow/harmflow-docs`.

> **Status: MVP in progress, macOS first.** Nothing here is a signed release.

## Layout

| Folder | Part |
|---|---|
| `contracts/` | The local API (`api/openapi.yaml`) and the JSON Schemas of tasks, events, and execution reports — the single source of truth for every name |
| `daemon/` | `harmflowd`, in Go: domain, use cases, ports, and adapters (Claude Code, Codex, YAML files, JSON state, JSONL events, HTTP + SSE, Keychain, launchd) |
| `desktop/` | The desktop shell: `src-tauri` (Tauri 2, Rust — tray, notifications, the daemon connection) and `ui` (React, TypeScript) |
| `agents/`, `skills/` | The built-in agent and skills of the GitHub issue implementation type, prefixed `harmflow-` |
| `packaging/` | What Tauri's bundle does not cover: signing and per-OS packaging notes |
| `tests/` | Tests across parts: the real daemon binary driven over its API with stand-in harness CLIs |

## Requirements

Go 1.24, Node 24 with pnpm 10, Rust stable, and, to run tasks, Claude Code or Codex signed in. The GitHub issue type also
needs Git and the GitHub CLI (`gh`) signed in. HarmFlow never installs, updates, or signs in to any of them.

## Common commands

```bash
make help              # every target
make deps              # the desktop's JavaScript dependencies
make test              # daemon, desktop, and integration tests
make dev-daemon        # the daemon on .harmflow-dev/home
make dev-ui            # the interface in a browser, proxied to that daemon (http://localhost:1420)
make dev-desktop       # the desktop shell against that daemon
make app               # HarmFlow.app and the DMG, unsigned
```

`HARMFLOW_HOME` selects the data directory (default `~/Library/Application Support/HarmFlow`), and
`HARMFLOW_TOKEN_STORE=file` keeps the local API token in a user-only file instead of the Keychain.

## How a task runs

A task is one YAML file (`harmflow/v1alpha1`). When it fires, the daemon starts the task's harness non-interactively
with the task's prompt, agents, and skills, in a workspace, under the task's permission profile translated to the
harness's own controls. The session leaves progress lines and a report in its execution directory; the daemon records
them, streams every event to the dashboard, fails over to the next harness on an exhausted quota or an unavailable
harness, and notifies. HarmFlow itself implements no task behavior.
