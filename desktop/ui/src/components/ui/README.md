# Shared primitives

## Overview

This folder contains the small building blocks every screen draws with, one module per primitive, each with its test
beside it when it has one. A primitive's small private helpers and its types stay in its module.

## Interface

- `index.ts` re-exports every primitive and helper, so the rest of the interface imports them from `components/ui`.
  `[index.ts:Button]`
- The primitives, one per line:
  - `[Banner.tsx:Banner]`
  - `[Button.tsx:Button]`
  - `[Checkbox.tsx:Checkbox]`
  - `[Chip.tsx:Chip]`
  - `[CopyButton.tsx:CopyButton]`
  - `[Drawer.tsx:Drawer]`
  - `[Elapsed.tsx:Elapsed]`
  - `[EmptyState.tsx:EmptyState]`
  - `[Field.tsx:Field]`
  - `[FieldGroup.tsx:FieldGroup]`
  - `[Gauge.tsx:Gauge]`
  - `[IconButton.tsx:IconButton]`
  - `[KeyValue.tsx:KeyValue]`
  - `[ListRow.tsx:ListRow]`
  - `[ListRows.tsx:ListRows]`
  - `[Modal.tsx:Modal]`
  - `[NumberInput.tsx:NumberInput]`
  - `[Panel.tsx:Panel]`
  - `[ScanBar.tsx:ScanBar]`
  - `[SectionTitle.tsx:SectionTitle]`
  - `[SegmentedControl.tsx:SegmentedControl]`
  - `[SegmentedMultiSelect.tsx:SegmentedMultiSelect]`
  - `[Select.tsx:Select]`
  - `[Sparkline.tsx:Sparkline]`
  - `[StatusChip.tsx:StatusChip]`
  - `[StatusMark.tsx:StatusMark]`
  - `[Tabs.tsx:Tabs]`
  - `[TextArea.tsx:TextArea]`
  - `[TextInput.tsx:TextInput]`
  - `[Toggle.tsx:Toggle]`
- What several primitives share, not exported by `index.ts`:
  - `[Overlay.tsx:Overlay]`
  - `[SegmentedItem.tsx:SegmentedItem]`
  - `[styleHelpers.ts:statusIcon]`
- The class and status helpers, exported by `index.ts`:
  - `[styleHelpers.ts:cx]`
  - `[styleHelpers.ts:statusTone]`

## Invariants

- A module here imports another primitive or the helpers from that module, never from `index.ts`, so the folder has no
  import cycle through its own index. `[index.ts:Button]`
- `Button` is the only text-styled action, and `Checkbox` the only checkbox input of the interface's components besides
  the switch `Toggle` draws on one of its own; their tests fail on one drawn anywhere else. `[Button.tsx:Button]`
  `[Checkbox.tsx:Checkbox]` `[Toggle.tsx:Toggle]`

## Verification

- Run: `pnpm -C desktop/ui test`
