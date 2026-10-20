// Short names for the schemas of contracts/api/openapi.yaml. generated.ts is produced by `pnpm generate` and never
// edited by hand.
import type { components } from "./generated";

type S = components["schemas"];

export type Health = S["Health"];
export type Diagnostics = S["Diagnostics"];
export type Stats = S["Stats"];
export type Harness = S["Harness"];
export type HarnessId = Harness["id"];
export type Tool = S["Tool"];
export type Catalog = S["Catalog"];
export type CatalogEntry = S["CatalogEntry"];
export type TaskDocument = S["TaskDocument"];
export type TaskInput = S["TaskInput"];
export type TaskSummary = S["TaskSummary"];
export type Task = S["Task"];
export type ValidationIssue = S["ValidationIssue"];
export type ValidationResult = S["ValidationResult"];
export type Warning = S["Warning"];
export type ScheduleInput = S["ScheduleInput"];
export type SchedulePreview = S["SchedulePreview"];
export type FiringResult = S["FiringResult"];
export type ExecutionStatus = S["ExecutionStatus"];
export type Trigger = S["Trigger"];
export type Attempt = S["Attempt"];
export type WorkItemResult = S["WorkItemResult"];
export type ExecutionSummary = S["ExecutionSummary"];
export type Execution = S["Execution"];
export type ExecutionCommand = Execution["commands"][number];
export type HarmflowEvent = S["Event"];
export type Queue = S["Queue"];
export type WorkItem = S["WorkItem"];
export type Settings = S["Settings"];
export type SettingsPatch = S["SettingsPatch"];
export type CleanResult = S["CleanResult"];
export type Notification = S["Notification"];
export type Accepted = S["Accepted"];
export type ApiErrorBody = S["Error"];
export type ApiErrorCode = ApiErrorBody["error"]["code"];

export const ACTIVE_STATUSES: readonly ExecutionStatus[] = [
  "preparing",
  "running",
  "switching_harness",
  "testing",
  "publishing",
];

export const TERMINAL_STATUSES: readonly ExecutionStatus[] = [
  "completed",
  "no_work",
  "failed",
  "cancelled",
  "interrupted",
  "needs_attention",
];

export function isActive(status: ExecutionStatus): boolean {
  return ACTIVE_STATUSES.includes(status);
}

export function isTerminal(status: ExecutionStatus): boolean {
  return TERMINAL_STATUSES.includes(status);
}
