# API contract

## Overview

This package is the handwritten description of the local API boundary.

## Interface

- OpenAPI 3.1 document for the versioned local API. `[openapi.yaml:openapi]`
- Declares task, execution, settings, harness, and event-stream paths. `[openapi.yaml:/tasks]` `[openapi.yaml:/executions]` `[openapi.yaml:/settings]` `[openapi.yaml:/events/stream]`
- Defines shared request and response schemas, including `TaskDocument`, `Execution`, and `Event`. `[openapi.yaml:TaskDocument]` `[openapi.yaml:Execution]` `[openapi.yaml:Event]`

## Invariants

- Requests use the declared bearer-token security scheme. `[openapi.yaml:bearerToken]`
- The API version prefix is represented by the documented path set. `[openapi.yaml:/health]`

## Side Effects

- Mutation endpoints are explicitly described as `POST`, `PUT`, `PATCH`, or `DELETE` operations. `[openapi.yaml:/tasks]` `[openapi.yaml:/settings]`

## Verification

- Run: `make check-generated`
