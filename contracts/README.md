# contracts

Written by hand; the single source of truth for every key, enum, event, and route name.

| File | Contract |
|---|---|
| `api/openapi.yaml` | The local API under `/api/v1`, with the SSE stream |
| `schemas/task.schema.json` | A task document, `harmflow/v1alpha1`, one YAML file per task |
| `schemas/template.schema.json` | A task template, `harmflow/v1alpha1`, one YAML file per template: the built-in ones are `templates/`, and the user's are in the `templates/` directory of HarmFlow's data directory, beside `tasks/` |
| `schemas/event.schema.json` | An event, in a JSONL log and on the SSE stream |
| `schemas/execution-report.schema.json` | The report and progress lines a harness session leaves in its execution directory |
| `schemas/context.schema.json` | `context.json`, the parameters and history the daemon gives a harness session in its execution directory |

Each part keeps a generated copy: `make generate` refreshes them and `make check-generated` fails when a copy is stale.

## EGM Blueprint

### Overview

This directory is the authored contract boundary. Generated consumers live outside this directory.

### Subdirectories & Boundaries

- [api](./api/README.md) — local HTTP and SSE API description.
- [schemas](./schemas/README.md) — versioned task, event, report, template, and session-context schemas.

### Invariants

- The generation target copies these schema files into the daemon and regenerates the UI API types; do not hand-edit those outputs. `[../Makefile:generate]`

### Side Effects

- `make generate` overwrites generated contract copies and TypeScript API types. `[../Makefile:generate]`

### Verification

- Run: `make check-generated`
