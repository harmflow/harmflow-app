// Typed calls to the local API, one per operation of contracts/api/openapi.yaml.
import { daemonText } from "../lib/daemonText";
import { transport } from "./transport";
import type {
  Accepted,
  ApiErrorBody,
  ApiErrorCode,
  Catalog,
  CleanResult,
  Diagnostics,
  Execution,
  ExecutionOutput,
  ExecutionSummary,
  FiringResult,
  HarmflowEvent,
  Harness,
  HarnessId,
  Health,
  NewTask,
  Notification,
  Queue,
  ScheduleInput,
  SchedulePreview,
  Settings,
  SettingsPatch,
  Stats,
  Task,
  TaskInput,
  TaskSummary,
  Template,
  TemplateLanguage,
  Tool,
  ValidationResult,
} from "./types";

export class ApiError extends Error {
  readonly status: number;
  readonly code: ApiErrorCode | "network";
  readonly validation?: ValidationResult;
  readonly currentRevision?: string;

  constructor(status: number, body: unknown) {
    const err = (body as ApiErrorBody | null)?.error;
    super(err ? daemonText(err) : typeof body === "string" && body ? body : `HTTP ${status}`);
    this.status = status;
    this.code = err?.code ?? (status === 0 ? "network" : "internal");
    this.validation = err?.validation;
    this.currentRevision = err?.currentRevision;
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await transport.request(method, path, body);
  if (res.status === 0 || res.status >= 400) throw new ApiError(res.status, res.body);
  return res.body as T;
}

const enc = encodeURIComponent;

// query writes the parameters that have a value; a list is one parameter per entry.
function query(params: Record<string, string | number | boolean | string[] | undefined>): string {
  const parts = Object.entries(params)
    .flatMap(([k, v]) => (Array.isArray(v) ? v.map((x) => [k, x] as const) : [[k, v] as const]))
    .filter(([, v]) => v !== undefined && v !== "")
    .map(([k, v]) => `${k}=${enc(String(v))}`);
  return parts.length ? `?${parts.join("&")}` : "";
}

export interface ExecutionQuery {
  taskId?: string;
  status?: string;
  active?: boolean;
  limit?: number;
  before?: string;
}

export const api = {
  health: () => call<Health>("GET", "/health"),
  stopDaemon: (force = false) => call<Accepted>("POST", "/daemon/stop", { force }),
  diagnostics: () => call<Diagnostics>("GET", "/diagnostics"),
  stats: () => call<Stats>("GET", "/stats"),

  harnesses: () => call<Harness[]>("GET", "/harnesses"),
  refreshHarnesses: () => call<Harness[]>("POST", "/harnesses/refresh"),
  setHarnessPath: (id: HarnessId, path: string | null) => call<Harness>("PUT", `/harnesses/${id}`, { path }),
  /** Starts the harness's own sign-in, which the user approves in the browser, or joins the one that runs. */
  signInHarness: (id: HarnessId) => call<Harness>("POST", `/harnesses/${id}/sign-in`),
  tools: () => call<Tool[]>("GET", "/tools"),
  /**
   * What a task can cite; a harness that finds the user's items per workspace, as Codex and Claude Code their skills,
   * lists the ones of the task's workspace path, or of the folder HarmFlow runs the task in when it sets none.
   */
  catalog: (workspace?: string, task?: string, addDir?: string[]) =>
    call<Catalog>("GET", `/catalog${query({ workspace, task, addDir })}`),
  templates: () => call<Template[]>("GET", "/templates"),
  /** The document of a new task from a template, named in language; the daemon writes nothing. */
  draftTask: (template: string, language: TemplateLanguage) =>
    call<NewTask>("POST", `/templates/${enc(template)}/draft`, { language }),

  tasks: () => call<TaskSummary[]>("GET", "/tasks"),
  task: (id: string) => call<Task>("GET", `/tasks/${enc(id)}`),
  createTask: (input: TaskInput) => call<Task>("POST", "/tasks", input),
  updateTask: (id: string, input: TaskInput) => call<Task>("PUT", `/tasks/${enc(id)}`, input),
  deleteTask: async (id: string): Promise<"deleted" | "pending"> => {
    const res = await transport.request("DELETE", `/tasks/${enc(id)}`);
    if (res.status >= 400 || res.status === 0) throw new ApiError(res.status, res.body);
    return res.status === 202 ? "pending" : "deleted";
  },
  validateTask: (input: TaskInput) => call<ValidationResult>("POST", "/tasks/validate", input),
  importTask: (yaml: string, overwrite = false) => call<Task>("POST", "/tasks/import", { yaml, overwrite }),
  runTask: (id: string) => call<FiringResult>("POST", `/tasks/${enc(id)}/run`),
  duplicateTask: (id: string) => call<Task>("POST", `/tasks/${enc(id)}/duplicate`),
  enableTask: (id: string) => call<Task>("POST", `/tasks/${enc(id)}/enable`),
  disableTask: (id: string) => call<Task>("POST", `/tasks/${enc(id)}/disable`),
  exportTask: async (id: string): Promise<string> => {
    const res = await transport.requestText(`/tasks/${enc(id)}/export`);
    if (res.status >= 400 || res.status === 0) throw new ApiError(res.status, res.text);
    return res.text;
  },
  previewSchedule: (schedule: ScheduleInput, count = 5) =>
    call<SchedulePreview>("POST", "/schedules/preview", { schedule, count }),

  executions: (q: ExecutionQuery = {}) => call<ExecutionSummary[]>("GET", `/executions${query({ ...q })}`),
  execution: (id: string) => call<Execution>("GET", `/executions/${enc(id)}`),
  cancelExecution: (id: string) => call<Accepted>("POST", `/executions/${enc(id)}/cancel`),
  executionEvents: (id: string, after = 0, limit = 5000) =>
    call<{ events: HarmflowEvent[]; logExpired: boolean }>(
      "GET",
      `/executions/${enc(id)}/events${query({ after: after || undefined, limit })}`,
    ),
  executionOutput: (id: string, limit?: number) =>
    call<ExecutionOutput>("GET", `/executions/${enc(id)}/output${query({ limit })}`),

  queue: () => call<Queue>("GET", "/queue"),

  settings: () => call<Settings>("GET", "/settings"),
  updateSettings: (patch: SettingsPatch) => call<Settings>("PATCH", "/settings", patch),
  rotateToken: () => call<Accepted>("POST", "/settings/token/rotate"),
  cleanWorkspaces: () => call<CleanResult>("POST", "/workspaces/clean"),

  notifications: (unread?: boolean) => call<Notification[]>("GET", `/notifications${query({ unread })}`),
  markNotificationsRead: (ids?: string[]) => call<Accepted>("POST", "/notifications/read", ids ? { ids } : {}),
};
