# Task form parts

## Overview

This folder contains the parts that `TaskForm` renders inside its sections: the agent the prompt is copied from, the script fields, the agent and skill pickers, the repository list, the harness profile and its fallback list, the custom permission lists, the whole-number and closed-set fields, and the schedule builder.

## Interface

- Each part is exported by its named module. `[AgentOrigin.tsx:AgentOrigin]` `[ScriptFields.tsx:ScriptFields]` `[NamePicker.tsx:NamePicker]` `[RepositoryList.tsx:RepositoryList]` `[HarnessProfile.tsx:HarnessProfile]` `[HarnessProfile.tsx:FallbackList]` `[ListArea.tsx:ListArea]` `[IntField.tsx:IntField]` `[EnumField.tsx:EnumField]` `[ScheduleFields.tsx:ScheduleFields]`
- What several modules share lives in `formModel.ts`: the list row type, the setter type, the closed sets, and the row cleaner. `OPTION_FIELDS` is also re-exported by `TaskForm.tsx` for its existing importers. `[formModel.ts:OPTION_FIELDS]` `[formModel.ts:cleanRow]` `[formModel.ts:SetFn]` `[formModel.ts:Item]`

## Invariants

- No module here imports `TaskForm.tsx`; shared code flows down from the parent only. `[formModel.ts:OPTION_FIELDS]`
- A cleared field writes `undefined`, so the key is removed and its default applies. `[EnumField.tsx:EnumField]` `[ListArea.tsx:ListArea]`
- The text areas keep their text in sync with YAML edits of the document. `[ListArea.tsx:ListArea]` `[ScriptFields.tsx:ScriptFields]`
- A cron schedule is described only once the daemon accepted the same expression. `[ScheduleFields.tsx:ScheduleFields]`

## Side Effects

- `ScheduleFields` asks the daemon, debounced, for a preview of the next runs. `[ScheduleFields.tsx:ScheduleFields]`
- `AgentOrigin` asks for a native confirmation before a copy replaces a prompt the user wrote. `[AgentOrigin.tsx:AgentOrigin]`

## Verification

- Run: `pnpm -C desktop/ui test`
