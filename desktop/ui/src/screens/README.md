# Dashboard screens

## Overview

This package contains the top-level interface screens and import dialog.

## Interface

- Overview, execution, task, history, settings, onboarding, and daemon-down views are exported as screen components. `[OverviewScreen.tsx:OverviewScreen]` `[ExecutionsScreen.tsx:ExecutionsScreen]` `[TasksScreen.tsx:TasksScreen]` `[HistoryScreen.tsx:HistoryScreen]` `[SettingsScreen.tsx:SettingsScreen]` `[OnboardingScreen.tsx:OnboardingScreen]` `[OnboardingScreen.tsx:DaemonDownScreen]`
- `ImportDialog`, `TaskView`, `SettingsSection`, and `isSettingsSection` define screen-facing contracts; `TasksScreen.tsx` re-exports `TaskView`. `[ImportDialog.tsx:ImportDialog]` `[taskView.ts:TaskView]` `[SettingsScreen.tsx:SettingsSection]` `[SettingsScreen.tsx:isSettingsSection]`
- The task list, its handles' move menu, and the group name field live beside the task screen. `[TaskList.tsx:TaskList]` `[MoveMenu.tsx:MoveMenu]` `[MoveMenu.tsx:MenuChoice]` `[GroupName.tsx:GroupName]`
- The settings screen's sections live one per module in `settings/`, with the row layout, the settings patch hook, and `DIRECTORIES` they share; `SettingsScreen.tsx` re-exports `DIRECTORIES`. `[settings/GeneralSection.tsx:GeneralSection]` `[settings/SettingRow.tsx:SettingRow]` `[settings/usePatch.ts:usePatch]` `[settings/directories.ts:DIRECTORIES]`

## Invariants

- Settings sections are constrained to the declared `SettingsSection` union and validated through `isSettingsSection`. `[SettingsScreen.tsx:SettingsSection]` `[SettingsScreen.tsx:isSettingsSection]`
- Task screen modes are represented by the discriminated `TaskView` union. `[taskView.ts:TaskView]`

## Side Effects

- The task list can inspect DOM targets and schedule event-handling cleanup for task layout gestures. `[TaskList.tsx:TaskList]`

## Verification

- Run: `pnpm -C desktop/ui test`
