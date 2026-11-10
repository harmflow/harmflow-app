# Schema contracts

## Overview

This package holds the authored JSON Schema documents consumed by the daemon and UI generation flow.

## Interface

- Task, template, event, execution-report, and context schema documents expose their versioned schema identifiers. `[task.schema.json:$id]` `[template.schema.json:$id]` `[event.schema.json:$id]` `[execution-report.schema.json:$id]` `[context.schema.json:$id]`
- The documents identify their contract subjects through schema titles. `[task.schema.json:title]` `[template.schema.json:title]` `[event.schema.json:title]` `[execution-report.schema.json:title]` `[context.schema.json:title]`

## Invariants

- These authored files are the inputs copied into the daemon's embedded-contract package. `[../../Makefile:generate]`

## Side Effects

- The schema files themselves do not perform runtime I/O; regeneration consumes them to update derived copies. `[../../Makefile:generate]`

## Verification

- Run: `make check-generated`
