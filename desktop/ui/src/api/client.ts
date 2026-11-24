// Typed calls to the local API, one per operation of contracts/api/openapi.yaml.
import { daemonText } from "../lib/daemonText";
import { transport } from "./transport";
import type {
  Accepted,
  ApprovalDecision,
  ApiErrorBody,
  ApiErrorCode,
  Catalog,
  CleanResult,
  Diagnostics,
  Execution,
  ExecutionOutput,
  ExecutionSummary,
  FiringResult,
  GitHubRateLimit,
  HarmflowEvent,
  Harness,
  HarnessId,
  Health,
  NewTask,
  NewTemplateRequest,
  Notification,
  Queue,
  SaveAsTemplateRequest,
  ScheduleInput,
  SchedulePreview,
  Settings,
  SettingsPatch,
  Spend,
  Stats,
  SystemGauges,
  Task,
  TaskInput,
  TaskSummary,
  Template,
  TemplateImportRequest,
  TemplateInput,
  TemplateLanguage,
  Tool,
  ValidationResult,
} from "./types";

export class ApiError extends Error {
  readonly status: number;
  readonly code: ApiErrorCode | "network";
  readonly validation?: ValidationResult;
  readonly currentRevision?: string;
  /** The params of the daemon's text, such as the count of an executions_running refusal. */
  readonly params?: Record<string, string>;
  /** The key of the daemon's text, such as custom_entry_other_harness. */
  readonly messageKey?: string;

  constructor(status: number, body: unknown) {
    const err = (body as ApiErrorBody | null)?.error;
    super(err ? daemonText(err) : typeof body === "string" && body ? body : `HTTP ${status}`);
    this.status = status;
    this.code = err?.code ?? (status === 0 ? "network" : "internal");
    this.validation = err?.validation;
    this.currentRevision = err?.currentRevision;
    this.params = err?.params;
    this.messageKey = err?.messageKey;
  }
}

/** The harnesses that take custom entries, in the order the add form tries them. */
const entryHarnesses: HarnessId[] = ["claude-code", "codex"];

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
  trigger?: string;
  /** A text the task's name or ID, the repository, the work item, the execution's ID, or the harness holds. */
  search?: string;
  limit?: number;
  before?: string;
  /** The period, as instants: from createdFrom, inclusive, to createdUntil, exclusive. */
  createdFrom?: string;
  createdUntil?: string;
}

export const api = {
  health: () => call<Health>("GET", "/health"),
  /** Stops the daemon; with drain, once its running executions end, at the deadline the daemon gives by default. */
  stopDaemon: (force = false, drain = false) => call<Accepted>("POST", "/daemon/stop", drain ? { drain: true } : { force }),
  diagnostics: () => call<Diagnostics>("GET", "/diagnostics"),
  stats: () => call<Stats>("GET", "/stats"),
  /** The current month's spend per day, for the Overview's band. */
  spend: () => call<Spend>("GET", "/spend"),
  system: () => call<SystemGauges>("GET", "/system"),
  /** GitHub's count of the gh login's hourly limits, REST and GraphQL, for the sidebar's GitHub gauges. */
  githubRateLimit: () => call<GitHubRateLimit>("GET", "/github/rate-limit"),

  harnesses: () => call<Harness[]>("GET", "/harnesses"),
  refreshHarnesses: () => call<Harness[]>("POST", "/harnesses/refresh"),
  setHarnessPath: (id: HarnessId, path: string | null) => call<Harness>("PUT", `/harnesses/${id}`, { path }),
  /**
   * Starts the own sign-in of an installation of the harness, which the user approves in the browser, or joins the one
   * that runs for it: the installation in use with no installationId, otherwise the item of homes or customEntries it
   * names, which signs in with its own environment.
   */
  signInHarness: (id: HarnessId, installationId?: string) =>
    call<Harness>("POST", `/harnesses/${id}/sign-in`, installationId ? { installationId } : undefined),
  /** Saves the harness's long-lived token in the credential store; the answer never carries it back. */
  setHarnessToken: (id: HarnessId, token: string) => call<Harness>("PUT", `/harnesses/${id}/token`, { token }),
  removeHarnessToken: (id: HarnessId) => call<Harness>("DELETE", `/harnesses/${id}/token`),
  /**
   * Adds an installation from a command line, which the daemon reads with no shell and whose harness it identifies:
   * under each harness in turn, until one does not answer that the line runs the other harness. The answer is that
   * harness, the entry among its custom entries.
   */
  addCustomEntry: async (line: string, name?: string): Promise<Harness> => {
    const body = name ? { line, name } : { line };
    for (const [i, id] of entryHarnesses.entries()) {
      try {
        return await call<Harness>("POST", `/harnesses/${id}/installations`, body);
      } catch (err) {
        if (i === entryHarnesses.length - 1 || !(err instanceof ApiError) || err.messageKey !== "custom_entry_other_harness") throw err;
      }
    }
    throw new ApiError(0, null);
  },
  /** Renames a custom entry of the harness or gives it another command line; an absent field keeps what it had. */
  updateCustomEntry: (harness: HarnessId, id: string, change: { line?: string; name?: string }) =>
    call<Harness>("PATCH", `/harnesses/${harness}/installations/${encodeURIComponent(id)}`, change),
  /** Removes a custom entry of the harness; the installations detection finds stay. */
  removeCustomEntry: (harness: HarnessId, id: string) =>
    call<Harness>("DELETE", `/harnesses/${harness}/installations/${encodeURIComponent(id)}`),
  tools: () => call<Tool[]>("GET", "/tools"),
  /**
   * What a task can cite. Claude Code's and Codex's skills are the installation's, the same for every workspace; a
   * harness that finds the user's items per workspace lists the ones of the task's workspace path, or of the folder
   * HarmFlow runs the task in when it sets none.
   */
  catalog: (workspace?: string, task?: string, addDir?: string[]) =>
    call<Catalog>("GET", `/catalog${query({ workspace, task, addDir })}`),
  templates: () => call<Template[]>("GET", "/templates"),
  /** The document of a new task from a template, named in language; the daemon writes nothing. */
  draftTask: (template: string, language: TemplateLanguage) =>
    call<NewTask>("POST", `/templates/${enc(template)}/draft`, { language }),
  /** A template with its document, its file's text, and, for one of the user's, the revision an update starts from. */
  template: (id: string) => call<Template>("GET", `/templates/${enc(id)}`),
  /** Creates an empty template of the user's of a task type, named and described as the user writes it. */
  createTemplate: (template: NewTemplateRequest) => call<Template>("POST", "/templates", template),
  updateTemplate: (id: string, input: TemplateInput) => call<Template>("PUT", `/templates/${enc(id)}`, input),
  deleteTemplate: (id: string) => call<void>("DELETE", `/templates/${enc(id)}`),
  /** Copies a template, built in or the user's, into a new template of the user's. */
  duplicateTemplate: (id: string) => call<Template>("POST", `/templates/${enc(id)}/duplicate`),
  validateTemplate: (id: string, input: TemplateInput) => call<ValidationResult>("POST", `/templates/${enc(id)}/validate`, input),
  /** Imports a portable template as one of the user's; a dry run checks it as the import would and writes nothing. */
  importTemplate: (request: TemplateImportRequest) => call<Template>("POST", "/templates/import", request),
  /** A template of the user's as portable YAML, with no secrets, logs, or state. */
  exportTemplate: async (id: string): Promise<string> => {
    const res = await transport.requestText(`/templates/${enc(id)}/export`);
    if (res.status >= 400 || res.status === 0) throw new ApiError(res.status, res.text);
    return res.text;
  },

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
  /** Saves the task as a new template of the user's, named and described as the user writes it. */
  saveAsTemplate: (id: string, template: SaveAsTemplateRequest) => call<Template>("POST", `/tasks/${enc(id)}/template`, template),
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
  /** A page of the executions, and how many the query's filters match in all, whatever the page. */
  executionPage: async (q: ExecutionQuery = {}): Promise<{ rows: ExecutionSummary[]; total?: number }> => {
    const res = await transport.request("GET", `/executions${query({ ...q })}`);
    if (res.status === 0 || res.status >= 400) throw new ApiError(res.status, res.body);
    return { rows: res.body as ExecutionSummary[], total: res.total };
  },
  execution: (id: string) => call<Execution>("GET", `/executions/${enc(id)}`),
  cancelExecution: (id: string) => call<Accepted>("POST", `/executions/${enc(id)}/cancel`),
  answerApproval: (id: string, requestId: string, decision: ApprovalDecision) =>
    call<Accepted>("POST", `/executions/${enc(id)}/approvals/${enc(requestId)}`, { decision }),
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
