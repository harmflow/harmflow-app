# Application ports

## Overview

This package specifies the application-facing interfaces implemented by adapters.

## Interface

- Persistence, event, secret, service-management, and workspace contracts are declared as interfaces. `[ports.go:TaskRepository]` `[ports.go:StateStore]` `[ports.go:ExecutionRepository]` `[ports.go:EventLog]` `[ports.go:SecretStore]` `[ports.go:HostServiceManager]` `[ports.go:WorkspaceReader]`
- Harness discovery, invocation, parsing, sign-in, and quota contracts are declared here. `[ports.go:HarnessAdapter]` `[ports.go:Invocation]` `[ports.go:OutputParser]` `[ports.go:SignInDriver]` `[ports.go:QuotaReader]`
- Common storage errors and the sanitized environment helpers are exported. `[errors.go:ErrConflict]` `[session_env.go:SessionEnv]` `[session_env.go:WithoutCodexHome]`

## Invariants

- A task save with an expected revision must report `ErrConflict` when on-disk content differs. `[ports.go:TaskRepository]` `[errors.go:ErrConflict]`
- `SessionEnv` removes nested-Claude, `HARMFLOW_*`, and `CODEX_HOME` variables before a session or a script task's program starts. `[session_env.go:SessionEnv]`
- `WithoutCodexHome` removes `CODEX_HOME` from the environment of every process that is no Codex probe or attempt; the Codex adapter sets each installation's own home on those. `[session_env.go:WithoutCodexHome]`
- A `SecretStore` contract exposes only read, write, delete, and store-kind operations; inventory token values are excluded from `TokenInfo`. `[ports.go:SecretStore]` `[ports.go:TokenInfo]`

## Side Effects

- The interfaces define effects delegated to adapters: persistence, process invocation, event streaming, credential storage, and service management. `[ports.go:TaskRepository]` `[ports.go:Invocation]` `[ports.go:EventLog]` `[ports.go:SecretStore]` `[ports.go:HostServiceManager]`

## Verification

- Run: `go -C daemon test ./...`
