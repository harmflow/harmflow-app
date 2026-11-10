# Interface state

## Overview

This package maintains the dashboard's external store and daemon synchronization loop.

## Interface

- `State` defines the complete dashboard view state. `[store.ts:State]`
- `getState`, `useStore`, `actions`, notification registration, and `startSync` are the public state API. `[store.ts:getState]` `[store.ts:useStore]` `[store.ts:actions]` `[store.ts:setNotificationHandler]` `[store.ts:startSync]`

## Invariants

- `useStore` subscribes through React's external-store API and returns selector snapshots. `[store.ts:useStore]`
- `startSync` is idempotent and creates a single synchronization setup. `[store.ts:startSync]`
- Per-execution event storage is bounded by the declared maximum. `[store.ts:MAX_EVENTS_PER_EXECUTION]`

## Side Effects

- Synchronization opens the selected transport stream, refreshes daemon data, subscribes to native navigation, and starts periodic refresh timers. `[store.ts:startSync]`
- Actions issue local API requests and update in-memory subscribers. `[store.ts:actions]`

## Verification

- Run: `pnpm -C desktop/ui test`
