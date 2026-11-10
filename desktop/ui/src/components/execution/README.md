# Execution detail tabs

## Overview

This folder contains the tab panels that `ExecutionDetail` renders for the selected execution: the chat, the timeline and its event details, the raw and script output, the script program, the changed files, the commands, the results, and the attempts.

## Interface

- Each tab panel is exported by its named module. `[Chat.tsx:Chat]` `[Timeline.tsx:Timeline]` `[Timeline.tsx:EventDetails]` `[RawOutput.tsx:RawOutput]` `[ScriptOutput.tsx:ScriptOutput]` `[ScriptProgram.tsx:ScriptProgram]` `[FileList.tsx:FileList]` `[CommandList.tsx:CommandList]` `[Results.tsx:Results]` `[Attempts.tsx:Attempts]`
- `RawCaptures` and `SCRIPT_STREAMS` are also re-exported by `ExecutionDetail.tsx` for its existing importers. `[RawOutput.tsx:RawCaptures]` `[ScriptOutput.tsx:SCRIPT_STREAMS]`

## Invariants

- No module here imports `ExecutionDetail.tsx`; each imports only `../ui`, `../Icon`, `../Shell`, and the `../../` API, library, and state modules.
- The timeline's arrow keys go through the shortcuts table and do not wrap. `[Timeline.tsx:Timeline]`
- Message and prompt text renders as React text, never as markup. `[Chat.tsx:ChatTurn]`

## Side Effects

- `RawOutput` schedules refresh polling while an execution remains active. `[RawOutput.tsx:RawOutput]`
- `Results` opens pull request URLs through the native bridge. `[Results.tsx:Results]`

## Verification

- Run: `pnpm -C desktop/ui test`
