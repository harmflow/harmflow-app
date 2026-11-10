# Dashboard screens

## Overview

This package contains the top-level interface screens and import dialog.

## Interface

- Overview, execution, task, history, settings, onboarding, and daemon-down views are exported as screen components. `[OverviewScreen.tsx:OverviewScreen]` `[ExecutionsScreen.tsx:ExecutionsScreen]` `[TasksScreen.tsx:TasksScreen]` `[HistoryScreen.tsx:HistoryScreen]` `[SettingsScreen.tsx:SettingsScreen]` `[OnboardingScreen.tsx:OnboardingScreen]` `[OnboardingScreen.tsx:DaemonDownScreen]`
- `ImportDialog`, `TaskView`, `SettingsSection`, and `isSettingsSection` define screen-facing contracts. `[ImportDialog.tsx:ImportDialog]` `[TasksScreen.tsx:TaskView]` `[SettingsScreen.tsx:SettingsSection]` `[SettingsScreen.tsx:isSettingsSection]`

## Invariants

- Settings sections are constrained to the declared `SettingsSection` union and validated through `isSettingsSection`. `[SettingsScreen.tsx:SettingsSection]` `[SettingsScreen.tsx:isSettingsSection]`
- Task screen modes are represented by the discriminated `TaskView` union. `[TasksScreen.tsx:TaskView]`

## Side Effects

- Screen interaction can inspect DOM targets and schedule event-handling cleanup for task layout gestures. `[TasksScreen.tsx:TasksScreen]`

## Verification

- Run: `pnpm -C desktop/ui test`
