# Schema-validation adapter

## Overview

This package compiles embedded schemas and maps validation results to useful diagnostics.

## Interface

- `Validator` and `New` expose compiled task, template, report, event, and context validators. `[schema.go:Validator]` `[schema.go:New]`
- YAML/JSON parsing, validation, and issue-location helpers are provided here. `[schema.go:ParseYAML]` `[schema.go:FromJSON]` `[schema.go:ValidateTask]` `[schema.go:ValidateTemplate]` `[schema.go:ValidateReport]` `[schema.go:ValidateEvent]` `[schema.go:ValidateContext]` `[schema.go:Locate]`

## Invariants

- `New` compiles the embedded schema resources and treats compilation failure as an initialization error. `[schema.go:New]`
- YAML parsing returns JSON-compatible values and location-aware syntax issues. `[schema.go:ParseYAML]`

## Side Effects

- Validation and location helpers operate on supplied in-memory documents; schema bytes come from the embedded-contract package. `[schema.go:New]` `[schema.go:ParseYAML]`

## Verification

- Run: `go -C daemon test ./...`
