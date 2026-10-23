// @vitest-environment happy-dom
// Renders each screen from a known state, to catch rendering errors without a browser or a daemon; the executions
// screen's cards are also driven in a DOM, against a stand-in for the daemon's local API.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { Execution, ExecutionSummary, HarmflowEvent, Harness, Health, Notification, Settings, TaskSummary, WorkItem } from "../api/types";
import { ExecutionCard } from "../components/ExecutionCard";
import { RawCaptures } from "../components/ExecutionDetail";
import { HarnessInventory } from "../components/Harnesses";
import { Icon } from "../components/Icon";
import { ValidationPanel } from "../components/TaskEditor";
import { TaskForm } from "../components/TaskForm";
import i18n from "../i18n";
import { formatBytes, formatDateTime } from "../lib/format";
import { newTaskDocument } from "../lib/taskForm";
import { getState, patchStateForTests } from "../state/store";
import { ExecutionsScreen } from "./ExecutionsScreen";
import { HistoryScreen } from "./HistoryScreen";
import { OnboardingScreen } from "./OnboardingScreen";
import { OverviewScreen } from "./OverviewScreen";
import { SettingsScreen } from "./SettingsScreen";
import { TasksScreen } from "./TasksScreen";

// The daemon as the client sees it: an answer per route, and every request it received.
const daemon = vi.hoisted(() => ({ answers: new Map<string, unknown>(), requests: [] as string[] }));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string) => {
      daemon.requests.push(`${method} ${path}`);
      const route = path.split("?")[0];
      return daemon.answers.has(route) ? { status: 200, body: daemon.answers.get(route) } : { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

const running: ExecutionSummary = {
  id: "exec-8f3a2c",
  taskId: "implementacao-core",
  taskName: "Implement GitHub issues",
  taskType: "github-issue-implementation",
  status: "running",
  trigger: "schedule",
  createdAt: "2026-10-20T19:23:10-03:00",
  startedAt: "2026-10-20T19:23:14-03:00",
  harness: "claude-code",
  model: "default",
  repository: "org/core-banking",
  workItem: "org/core-banking#123",
  currentStep: "Running tests",
};

const finished: ExecutionSummary = { ...running, id: "exec-1", status: "completed", endedAt: "2026-10-20T19:40:00-03:00", testsStatus: "failed", pullRequestUrl: "https://github.com/org/core-banking/pull/9" };

const detail: Execution = {
  ...running,
  attempts: [{ number: 1, harness: "claude-code", configuredModel: "default", configuredEffort: "high", startedAt: running.startedAt!, outcome: "running" }],
  workItems: [{ key: "org/core-banking#123", branch: "feature/123-agent", issueNumber: 123 }],
  warnings: [{ code: "policy_unenforced", message: "network: deny is not enforced for shell commands" }],
  changedFiles: ["src/auth/token.ts"],
  commands: [{ id: "c1", command: "./gradlew test", status: "running", startedAt: running.startedAt }],
};

let id = 1;
const ev = (type: string, extra: Partial<HarmflowEvent> = {}): HarmflowEvent => ({
  id: id++,
  schemaVersion: "1",
  timestamp: "2026-10-20T19:32:14-03:00",
  executionId: running.id,
  type,
  severity: "info",
  ...extra,
});

const harness: Harness = {
  id: "claude-code",
  name: "Claude Code",
  detected: true,
  path: "/opt/homebrew/bin/claude",
  pathSource: "path",
  installations: ["/opt/homebrew/bin/claude"],
  version: "2.1.239",
  versionSupport: "supported",
  authentication: "valid",
  readiness: "ready",
  models: [{ id: "sonnet", efforts: ["low", "high"] }],
  efforts: ["low", "medium", "high"],
  lastCheckedAt: "2026-10-20T19:00:00-03:00",
};

const task: TaskSummary = {
  id: "implementacao-core",
  name: "Implement GitHub issues",
  type: "github-issue-implementation",
  enabled: true,
  revision: "r1",
  harness: "claude-code",
  fallbackHarnesses: ["codex"],
  schedule: "0 9 * * 1-5",
  nextFireAt: "2026-10-21T09:00:00-03:00",
  warnings: [{ code: "repository_unprotected", message: "main is not protected" }],
  running: true,
  repositories: ["org/core-banking"],
};

const item = (key: string, status: WorkItem["status"], title: string): WorkItem => ({
  taskId: "implementacao-core",
  key: `org/core-banking#${key}`,
  title,
  repository: "org/core-banking",
  status,
  resumes: 0,
  failures: 0,
  lastExecutionId: "exec-1",
  updatedAt: "2026-10-20T19:40:00-03:00",
});

const workItems: WorkItem[] = [
  { ...item("124", "in_progress", "Draft PR with failing tests"), testsStatus: "failed", pullRequestUrl: "https://github.com/org/core-banking/pull/9" },
  { ...item("125", "failed", "Issue to retry"), failures: 1 },
  item("126", "completed", "Completed issue"),
  item("127", "closed", "Closed issue"),
  item("128", "needs_attention", "Issue past its limits"),
];

const settings: Settings = {
  language: "pt-BR",
  trayEnabled: true,
  notificationLevel: "all",
  logRetentionDays: 30,
  theme: "harmflow-dark",
  autostart: { enabled: false, mechanism: "launchd" },
  pathVariables: { PROJECTS_ROOT: "/Users/me/Projects" },
  onboardingCompleted: true,
};

beforeAll(async () => {
  await i18n.changeLanguage("pt-BR");
  patchStateForTests({
    connection: "connected",
    daemonRunning: true,
    active: [running],
    recent: [finished],
    details: { [running.id]: detail },
    events: {
      [running.id]: [
        ev("execution.started", { message: "Loading configuration" }),
        ev("harness.started", { harness: "claude-code" }),
        ev("tool.started", { payload: { toolUseId: "t1", tool: "Bash", input: { command: "./gradlew test" } } }),
        ev("harness.switched", { message: "claude-code → codex" }),
        ev("harness.output", { message: "> Task :compileJava" }),
      ],
    },
    stats: {
      running: 1,
      queued: 1,
      completedToday: 12,
      failedToday: 1,
      activity: { running: [1, 2, 3], queued: [0, 1], completed: [3, 4], failed: [0, 1] },
      system: { cpuPercent: 12, memoryUsedBytes: 2.1e9, memoryTotalBytes: 16e9, diskUsedBytes: 48e9, diskTotalBytes: 500e9 },
    },
    queue: {
      waiting: [{ ...running, id: "exec-q", status: "queued" }],
      upcoming: [{ taskId: "docs", taskName: "Generate docs", nextFireAt: "2026-10-21T10:00:00-03:00" }],
      workItems,
    },
    tasks: [task],
    tasksLoaded: true,
    harnesses: [harness],
    tools: [{ id: "gh", detected: true, version: "2.101.0", authentication: "valid" }],
    settings,
    unread: [
      { id: "n1", createdAt: "2026-10-20T19:00:00-03:00", eventType: "pull_request.created", title: "Draft PR created", body: "", severity: "info", read: false, params: { taskName: "Core", url: "https://x" } },
    ],
  });
});

describe("screens", () => {
  it("renders the live executions screen with cards, detail, queue, and history", () => {
    const html = renderToString(<ExecutionsScreen selectedId={null} onSelect={() => {}} onGoToTasks={() => {}} onSeeHistory={() => {}} />);
    expect(html).toContain("Execuções ativas (1)");
    expect(html).toContain("org/core-banking");
    expect(html).toContain("Trocando de harness");
    expect(html).toContain("Fila de execução (4)");
    expect(html).not.toMatch(/\d+%/); // no invented progress percentage (FS-008 FR-009)
  });

  it("lists in the queue the work items waiting to be resumed or retried (FS-008 FR-004)", () => {
    const html = renderToString(<ExecutionsScreen selectedId={null} onSelect={() => {}} onGoToTasks={() => {}} onSeeHistory={() => {}} />);
    expect(html).toContain("Draft PR with failing tests");
    expect(html).toContain("Para retomar");
    expect(html).toContain("Issue to retry");
    expect(html).toContain("Para tentar de novo");
    expect(html).not.toContain("Completed issue");
    expect(html).not.toContain("Closed issue");
    expect(html).not.toContain("Issue past its limits");
  });

  it("renders the other screens", () => {
    expect(renderToString(<OverviewScreen onOpenExecution={() => {}} onNewTask={() => {}} onOpenTask={() => {}} />)).toContain("Daemon");
    expect(renderToString(<TasksScreen view={{ kind: "list" }} onView={() => {}} onOpenExecution={() => {}} />)).toContain("Implement GitHub issues");
    expect(renderToString(<HistoryScreen openId={null} onOpen={() => {}} />)).toContain("Estado");
    expect(renderToString(<SettingsScreen />)).toContain("Agentes instalados");
    expect(renderToString(<OnboardingScreen onFinish={() => {}} onCreateTask={() => {}} onImport={() => {}} />)).toContain("Boas-vindas");
  });

  it("names in About the commit each build comes from, and shows the version alone without one", () => {
    const health: Health = { status: "ok", version: "0.1.0", apiVersion: "v1", pid: 7, startedAt: "2026-10-20T19:00:00-03:00", acceptingFirings: true };
    const { appVersion, appCommit, appBuiltAt } = getState();
    patchStateForTests({ appVersion: "0.1.0", appCommit: "45a57bb", appBuiltAt: "", health: { ...health, commit: "ca16cd7-dirty" } });
    try {
      const html = renderToString(<SettingsScreen />);
      expect(html).toContain('<span class="mono">0.1.0 (45a57bb)</span>');
      expect(html).toContain('<span class="mono">0.1.0 (ca16cd7-dirty)</span>');
      patchStateForTests({ appCommit: "", health });
      const bare = renderToString(<SettingsScreen />);
      expect(bare.match(/<span class="mono">0\.1\.0<\/span>/g)).toHaveLength(2);
      expect(bare).not.toContain("0.1.0 (");
    } finally {
      patchStateForTests({ appVersion, appCommit, appBuiltAt, health: undefined });
    }
  });

  it("shows in About, on each build's line, the date and time it was built, and no time without one", () => {
    const health: Health = { status: "ok", version: "0.1.0", apiVersion: "v1", pid: 7, startedAt: "2026-10-20T19:00:00-03:00", acceptingFirings: true };
    const { appVersion, appCommit, appBuiltAt } = getState();
    const desktopBuilt = "2026-10-22T20:30:05.123Z";
    const daemonBuilt = "2026-10-22T20:29:58Z";
    const at = (iso: string) => formatDateTime(iso, i18n.language);
    patchStateForTests({
      appVersion: "0.1.0",
      appCommit: "45a57bb",
      appBuiltAt: desktopBuilt,
      health: { ...health, commit: "ca16cd7-dirty", builtAt: daemonBuilt },
    });
    try {
      const html = renderToString(<SettingsScreen />);
      expect(html).toContain(`<span class="mono">0.1.0 (45a57bb) · ${at(desktopBuilt)}</span>`);
      expect(html).toContain(`<span class="mono">0.1.0 (ca16cd7-dirty) · ${at(daemonBuilt)}</span>`);
      // A time without a commit has no empty parentheses; a daemon built without make has no time.
      patchStateForTests({ appCommit: "", health: { ...health, commit: "ca16cd7" } });
      const partial = renderToString(<SettingsScreen />);
      expect(partial).toContain(`<span class="mono">0.1.0 · ${at(desktopBuilt)}</span>`);
      expect(partial).toContain('<span class="mono">0.1.0 (ca16cd7)</span>');
      patchStateForTests({ appBuiltAt: "not a time", health });
      expect(renderToString(<SettingsScreen />).match(/<span class="mono">0\.1\.0<\/span>/g)).toHaveLength(2);
    } finally {
      patchStateForTests({ appVersion, appCommit, appBuiltAt, health: undefined });
    }
  });

  it("shows the harness's capture attempt by attempt, saying when its start is left out (FS-008 FR-008)", () => {
    const MB = 1024 * 1024;
    const html = renderToString(
      <RawCaptures
        output={{
          logExpired: false,
          attempts: [
            { attempt: 1, harness: "claude-code", size: 3 * MB, offset: 2 * MB, text: "usage limit reached\n" },
            { attempt: 2, harness: "codex", size: 26, offset: 0, text: "pushed with [REDACTED]\n" },
          ],
        }}
      />,
    );
    expect(html).toContain("Tentativa 1 · Claude Code");
    expect(html).toContain(`mostrando os últimos ${formatBytes(MB, "pt-BR")} de ${formatBytes(3 * MB, "pt-BR")}`);
    expect(html).toContain("usage limit reached");
    expect(html).toContain("Tentativa 2 · Codex");
    expect(html).toContain("pushed with [REDACTED]");
    const empty = { logExpired: false, attempts: [{ attempt: 1, harness: "codex", size: 0, offset: 0, text: "" }] };
    expect(renderToString(<RawCaptures output={empty} />)).toContain("Nenhuma saída capturada ainda.");
  });

  it("shows the daemon's validation errors in the interface's language (FS-012 FR-005)", () => {
    const html = renderToString(
      <ValidationPanel
        validation={{
          valid: false,
          warnings: [],
          errors: [
            { path: "/spec", line: 7, column: 3, code: "schema", message: "property colour is not allowed", messageKey: "schema_additional_properties", params: { properties: "colour", count: "1" } },
            { path: "/spec/timeout", code: "semantic", message: "agent output, untouched" },
          ],
        }}
      />,
    );
    expect(html).toContain("a propriedade colour não é permitida");
    expect(html).toContain("agent output, untouched");
  });

  it("shows a harness whose first check runs as checking, in its card, its details, and the task editor (FS-004 AC-011)", () => {
    const checking: Harness = {
      id: "codex",
      name: "Codex",
      detected: false,
      installations: [],
      authentication: "unknown",
      readiness: "checking",
      models: [],
      efforts: [],
      lastCheckedAt: "0001-01-01T00:00:00Z",
      diagnostic: "detection is running",
      diagnosticKey: "detection_running",
    };
    const html = renderToString(<HarnessInventory harness={checking} locale="pt-BR" />).replace(/<!-- -->/g, "");
    // The row, its neutral chip with the diagnostic as its tooltip, and the Detected row; with no check time yet.
    expect(html.match(/Verificando…/g)).toHaveLength(3);
    expect(html).toContain(
      `<span class="chip chip--neutral" title="a detecção está em andamento">${renderToString(<Icon name="clock" size={12} />)}Verificando…</span>`,
    );
    expect(html).toContain("<dt>Última verificação</dt><dd>—</dd>");
    for (const hidden of ["Não detectado", "Não instalado", "<dd>Não</dd>"]) expect(html).not.toContain(hidden);
    const form = renderToString(
      <TaskForm doc={newTaskDocument("general", undefined, "A")} onChange={() => {}} mode="create" harnesses={[harness, checking]} errorPaths={new Set()} />,
    ).replace(/<!-- -->/g, "");
    expect(form).toContain("Codex — Verificando…");
  });

  it("shows a harness's diagnostic and an execution's error in the interface's language (FS-012 FR-005)", () => {
    const signedOut: Harness = {
      ...harness,
      authentication: "invalid",
      readiness: "not_ready",
      diagnostic: "Claude Code is not signed in; run `claude` in a terminal and sign in",
      diagnosticKey: "claude_not_signed_in",
    };
    expect(renderToString(<HarnessInventory harness={signedOut} locale="pt-BR" />)).toContain(
      "o Claude Code não está conectado; execute `claude` em um terminal e entre na conta",
    );
    patchStateForTests({
      recent: [
        {
          ...finished,
          id: "exec-timeout",
          status: "failed",
          errorMessage: "the execution timeout elapsed",
          errorKey: "execution_timed_out",
        },
      ],
    });
    try {
      expect(renderToString(<OverviewScreen onOpenExecution={() => {}} onNewTask={() => {}} onOpenTask={() => {}} />)).toContain(
        "o tempo limite da execução se esgotou",
      );
    } finally {
      patchStateForTests({ recent: [finished] });
    }
  });

  it("reads a failed execution's notice with its reason in the interface's language (FS-012 FR-005)", () => {
    const failed: Notification = {
      id: "n2",
      createdAt: "2026-10-20T19:05:00-03:00",
      eventType: "execution.failed",
      title: "Execution failed",
      body: "Core failed: the execution timeout elapsed",
      severity: "error",
      read: false,
      params: { taskName: "Core", error: "the execution timeout elapsed", errorKey: "execution_timed_out" },
    };
    const unread = getState().unread;
    patchStateForTests({ unread: [failed] });
    try {
      expect(renderToString(<OverviewScreen onOpenExecution={() => {}} onNewTask={() => {}} onOpenTask={() => {}} />)).toContain(
        "A execução falhou: o tempo limite da execução se esgotou",
      );
    } finally {
      patchStateForTests({ unread });
    }
  });

  it("asks for repositories only for the GitHub type (FS-001 FR-008)", () => {
    const form = (doc: Record<string, unknown>) =>
      renderToString(<TaskForm doc={doc} onChange={() => {}} mode="create" harnesses={[harness]} errorPaths={new Set()} />);
    expect(form(newTaskDocument("github-issue-implementation", undefined, "A"))).toContain("Repositórios");
    expect(form(newTaskDocument("general", undefined, "B"))).not.toContain("Repositórios");
  });

  it("asks a script task for its program, and for no prompt, agents, or harness (FS-013 FR-013, AC-013)", () => {
    const html = renderToString(
      <TaskForm doc={newTaskDocument("script", undefined, "Auditoria")} onChange={() => {}} mode="create" harnesses={[harness]} errorPaths={new Set()} />,
    );
    for (const shown of ["Programa e argumentos", "Diretório de trabalho", "Variáveis de ambiente", "Executa em", "macOS", "Windows"]) {
      expect(html).toContain(shown);
    }
    for (const hidden of ["O que a tarefa faz vive aqui", "Agentes", "Harness preferido", "Cadeia de fallback", "Novas tentativas por limite de taxa"]) {
      expect(html).not.toContain(hidden);
    }
  });
});

// FS-003 FR-003, AC-005: the card's Cancel sends the same single cancel as the detail's, and a finished execution's
// card cannot cancel it.
describe("the executions screen's cards (FS-003 AC-005)", () => {
  let root: Root | undefined;
  let host: HTMLElement | undefined;

  beforeAll(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = undefined;
    daemon.answers.clear();
    daemon.requests.length = 0;
    vi.unstubAllGlobals();
  });

  /** Lets the stand-in daemon's answers arrive and React render them. */
  const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));
  const writes = () => daemon.requests.filter((r) => !r.startsWith("GET "));

  async function render(node: ReactNode) {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(node));
    await settle();
  }

  it("sends one cancel from a running execution's card once confirmed, and drops the card once it ends", async () => {
    daemon.answers.set(`/executions/${running.id}`, detail);
    daemon.answers.set(`/executions/${running.id}/events`, { events: [], logExpired: false });
    daemon.answers.set(`/executions/${running.id}/cancel`, { executionId: running.id });
    // Outside the Tauri shell, the confirmation is the browser's.
    const confirm = vi.fn((_message?: string) => true);
    vi.stubGlobal("confirm", confirm);
    const { active, details } = getState();
    try {
      await render(<ExecutionsScreen selectedId={null} onSelect={() => {}} onGoToTasks={() => {}} onSeeHistory={() => {}} />);
      const cards = host!.querySelectorAll(".exec-card");
      expect(cards).toHaveLength(1);
      const cancel = cards[0].querySelector<HTMLButtonElement>(".exec-card__cancel")!;
      expect(cancel.disabled).toBe(false);

      await act(async () => cancel.click());
      await settle();
      expect(confirm).toHaveBeenCalledTimes(1);
      expect(writes()).toEqual([`POST /executions/${running.id}/cancel`]);
      // Pending, the button takes no second click.
      expect(cancel.disabled).toBe(true);
      await act(async () => cancel.click());
      await settle();
      expect(writes()).toEqual([`POST /executions/${running.id}/cancel`]);

      // Ended, the execution has no card, so no Cancel there.
      const ended = { ...running, status: "cancelled" as const, endedAt: "2026-10-20T19:33:00-03:00" };
      await act(async () => patchStateForTests({ active: [ended], details: { ...details, [running.id]: { ...detail, ...ended } } }));
      expect(host!.querySelectorAll(".exec-card")).toHaveLength(0);
    } finally {
      patchStateForTests({ active, details });
    }
  });

  it("disables the card's Cancel for a finished execution", async () => {
    const onCancel = vi.fn();
    await render(<ExecutionCard execution={finished} selected={false} onSelect={() => {}} now={Date.now()} onCancel={onCancel} cancelling={false} />);
    const cancel = host!.querySelector<HTMLButtonElement>(".exec-card__cancel")!;
    expect(cancel.disabled).toBe(true);
    await act(async () => cancel.click());
    expect(onCancel).not.toHaveBeenCalled();
    expect(writes()).toEqual([]);
  });
});
