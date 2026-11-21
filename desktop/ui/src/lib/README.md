# Interface helpers

## Overview

This package holds deterministic presentation, task-editing, scheduling, timeline, formatting, and browser-preference helpers.

## Interface

- Task-document and task-layout transformations are exported by `taskForm` and `taskLayout`. `[taskForm.ts:getIn]` `[taskForm.ts:setIn]` `[taskForm.ts:slugify]` `[taskLayout.ts:parseLayout]` `[taskLayout.ts:moveTask]`
- The task form's simple and advanced modes are exported by `editorMode`: what simple mode shows of each task type, read from the task schema's `required` lists at any depth, and how many of a task's fields it hides. `[editorMode.ts:simpleFields]` `[editorMode.ts:shownInSimple]` `[editorMode.ts:hiddenFieldCount]` `[editorMode.ts:simpleParameters]`
- Each screen's metric-card choice is read, held to the app's cards, and reordered by `metricCards`. `[metricCards.ts:parseMetricCards]` `[metricCards.ts:resolveCards]` `[metricCards.ts:moveCard]`
- Cost, schedule, timeline, schema, translation, formatting, shortcut, and zoom helpers expose the presentation domain. `[costs.ts:executionCost]` `[schedule.ts:cronOf]` `[timeline.ts:buildTimeline]` `[schema.ts:schemaAt]` `[daemonText.ts:daemonText]` `[format.ts:formatDateTime]` `[shortcuts.ts:SHORTCUTS]` `[zoom.ts:zoomed]`
- React timing and draft-state helpers are exported from `hooks.ts`. `[hooks.ts:useNow]` `[hooks.ts:useDebounced]` `[hooks.ts:useDraft]`

## Invariants

- Zoom is constrained to the ordered `ZOOM_STEPS` range and resets to 100%. `[zoom.ts:ZOOM_STEPS]` `[zoom.ts:zoomed]`
- Keyboard matching rejects Control/Option chords and held keys unless the shortcut explicitly repeats. `[shortcuts.ts:matches]`
- Layout parsing returns `null` for invalid persisted content rather than treating it as a layout. `[taskLayout.ts:parseLayout]`

## Side Effects

- Layout, zoom, spend-view, spend-split, metric-card, and editor-mode helpers read or write browser storage; React helpers schedule intervals and timeouts. `[taskLayout.ts:storedLayout]` `[taskLayout.ts:storeLayout]` `[zoom.ts:storedZoom]` `[zoom.ts:storeZoom]` `[spend.ts:keptSpendView]` `[spend.ts:keepSpendView]` `[spend.ts:keptSpendSplit]` `[spend.ts:keepSpendSplit]` `[metricCards.ts:storedMetricCards]` `[metricCards.ts:storeMetricCards]` `[editorMode.ts:keptEditorMode]` `[editorMode.ts:keepEditorMode]` `[hooks.ts:useNow]` `[hooks.ts:useDebounced]`

## Verification

- Run: `pnpm -C desktop/ui test`
