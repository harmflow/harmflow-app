# contracts

Written by hand; the single source of truth for every key, enum, event, and route name.

| File | Contract |
|---|---|
| `api/openapi.yaml` | The local API under `/api/v1`, with the SSE stream |
| `schemas/task.schema.json` | A task document, `harmflow/v1alpha1`, one YAML file per task |
| `schemas/template.schema.json` | A task template, `harmflow/v1alpha1`, one YAML file per template: the built-in ones are `templates/` |
| `schemas/event.schema.json` | An event, in a JSONL log and on the SSE stream |
| `schemas/execution-report.schema.json` | The report and progress lines a harness session leaves in its execution directory |
| `schemas/context.schema.json` | `context.json`, the parameters and history the daemon gives a harness session in its execution directory |

Each part keeps a generated copy: `make generate` refreshes them and `make check-generated` fails when a copy is stale.
