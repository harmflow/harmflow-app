# Dashboard components

## Overview

This package contains reusable presentation, editing, shell, timeline, and feedback components.

## Interface

- Shared primitive controls and status presentation are exported from `ui.tsx`. `[ui.tsx:Button]` `[ui.tsx:StatusChip]` `[ui.tsx:Tabs]` `[ui.tsx:SegmentedControl]` `[ui.tsx:SegmentedMultiSelect]` `[ui.tsx:Modal]` `[ui.tsx:Drawer]`
- `RowListEditor` draws every editable list of rows of text fields, with a remove button on each row and an add button below: Settings' path variables and allowed folders, and the task form's repositories. `[RowListEditor.tsx:RowListEditor]`
- Screen-shell, error-boundary, task-editor, execution-detail, harness, and template components are exported by their named modules. `[Shell.tsx:Sidebar]` `[ErrorBoundary.tsx:RootBoundary]` `[TaskEditor.tsx:TaskEditor]` `[ExecutionDetail.tsx:ExecutionDetail]` `[Harnesses.tsx:HarnessInventory]` `[Templates.tsx:TemplatePicker]`
- The task form's parts live one per module in `taskform/`, with the row type, setter type, closed sets, and row cleaner they share in `formModel.ts`; `TaskForm.tsx` re-exports `OPTION_FIELDS`. `[taskform/ScheduleFields.tsx:ScheduleFields]` `[taskform/HarnessProfile.tsx:HarnessProfile]` `[taskform/AgentOrigin.tsx:AgentOrigin]` `[taskform/formModel.ts:OPTION_FIELDS]`
- The execution detail's tab panels live in `execution/`, one module per tab; `ExecutionDetail.tsx` re-exports `RawCaptures` and `SCRIPT_STREAMS` from there. `[execution/Chat.tsx:Chat]` `[execution/Timeline.tsx:Timeline]` `[execution/RawOutput.tsx:RawOutput]` `[execution/Results.tsx:Results]` `[execution/Attempts.tsx:Attempts]`

## Invariants

- The shared `Tabs` component receives the selected value and reports changes via its callback. `[ui.tsx:Tabs]`
- `Modal` and `Drawer` share one overlay, which closes on the close shortcut and on a click outside its card. `[ui.tsx:Modal]` `[ui.tsx:Drawer]`
- `RootBoundary` and `ScreenBoundary` establish the root and screen error boundaries used by the application. `[ErrorBoundary.tsx:RootBoundary]` `[ErrorBoundary.tsx:ScreenBoundary]`

## Side Effects

- `CopyButton` writes text to the browser clipboard and the toast module schedules expiration of in-memory notices. `[ui.tsx:CopyButton]` `[toast.tsx:toast]`
- `RawOutput` schedules refresh polling while an execution remains active. `[execution/RawOutput.tsx:RawOutput]`

## Verification

- Run: `pnpm -C desktop/ui test`
