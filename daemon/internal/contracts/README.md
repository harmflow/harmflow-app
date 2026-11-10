# Embedded contracts

## Overview

This package provides generated schema copies to the daemon validator.

## Interface

- Embedded task, template, event, execution-report, and context schema byte slices are exported. `[contracts.go:TaskSchema]` `[contracts.go:TemplateSchema]` `[contracts.go:EventSchema]` `[contracts.go:ExecutionReportSchema]` `[contracts.go:ContextSchema]`
- The corresponding versioned schema URLs are exported as constants. `[contracts.go:TaskSchemaURL]`

## Invariants

- The package embeds generated copies; the source explicitly directs maintainers to regenerate from the authored contracts instead of editing files here. `[contracts.go:TaskSchema]`

## Side Effects

- Go embeds the schema files into the daemon binary at build time. `[contracts.go:TaskSchema]`

## Verification

- Run: `make check-generated`
