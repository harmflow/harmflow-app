// @vitest-environment happy-dom
// Renders each screen from a known state, to catch rendering errors without a browser or a daemon; the executions
// screen's cards are also driven in a DOM, against a stand-in for the daemon's local API.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";
import { native } from "../api/native";
import type { Execution, ExecutionSummary, HarmflowEvent, Harness, Health, Notification, Settings, TaskSummary, WorkItem } from "../api/types";
import { ExecutionCard } from "../components/ExecutionCard";
import { RawCaptures } from "../components/ExecutionDetail";
import { HarnessInventory } from "../components/Harnesses";
import { Icon } from "../components/Icon";
import { ValidationPanel } from "../components/TaskEditor";
import { TaskForm } from "../components/TaskForm";
import i18n from "../i18n";
import { formatBytes, formatDateTime } from "../lib/format";
import { slugify, type Doc } from "../lib/taskForm";
import { getState, patchStateForTests } from "../state/store";
import { ExecutionsScreen } from "./ExecutionsScreen";
import { HistoryScreen } from "./HistoryScreen";
import { OnboardingScreen } from "./OnboardingScreen";
import { OverviewScreen } from "./OverviewScreen";
import { SettingsScreen } from "./SettingsScreen";
import { TasksScreen } from "./TasksScreen";

/** A new task's document as the daemon drafts it from a built-in template of type, before the user fills it in. */
function newDoc(type: "github" | "general" | "script", name: string): Doc {
  const spec: Doc =
    type === "script"
      ? { enabled: true, type, script: {} }
      : { enabled: true, type, harness: { preferred: { provider: "claude-code" } } };
  if (type === "github") spec.github = { repositories: [{ baseBranch: "main" }] };
  return { apiVersion: "harmflow/v1alpha1", kind: "Task", metadata: { id: slugify(name), name }, spec };
}

// The daemon as the client sees it: an answer per route, and every request it received, with its body.
const daemon = vi.hoisted(() => ({ answers: new Map<string, unknown>(), requests: [] as string[], bodies: [] as unknown[] }));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string, body?: unknown) => {
      daemon.requests.push(`${method} ${path}`);
      daemon.bodies.push(body);
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
  taskType: "github",
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
  type: "github",
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
  keepTrayOnQuit: false,
  notificationLevel: "all",
  logRetentionDays: 30,
  theme: "harmflow-dark",
  autostart: { enabled: false, mechanism: "launchd" },
  pathVariables: { PROJECTS_ROOT: "/Users/me/Projects" },
  allowedFolders: ["/srv/repos"],
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
    expect(html).not.toMatch(/\d+%/); // no invented progress percentage
  });

  it("lists in the queue the work items waiting to be resumed or retried", () => {
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
    expect(renderToString(<SettingsScreen />)).toContain("Ferramentas de IA");
    expect(renderToString(<OnboardingScreen onFinish={() => {}} onCreateTask={() => {}} onImport={() => {}} />)).toContain("Boas-vindas");
  });

  it("lists the allowed folders of Settings beside the path variables", () => {
    const html = renderToString(<SettingsScreen section="paths" />);
    expect(html).toContain("Pastas permitidas");
    expect(html).toContain('value="/srv/repos"');
    expect(html).toContain("Adicionar pasta");
  });

  it("names in About the commit each build comes from, and shows the version alone without one", () => {
    const health: Health = { status: "ok", version: "0.1.0", apiVersion: "v1", pid: 7, startedAt: "2026-10-20T19:00:00-03:00", acceptingFirings: true };
    const { appVersion, appCommit, appBuiltAt } = getState();
    patchStateForTests({ appVersion: "0.1.0", appCommit: "45a57bb", appBuiltAt: "", health: { ...health, commit: "ca16cd7-dirty" } });
    try {
      const html = renderToString(<SettingsScreen section="about" />);
      expect(html).toContain('<span class="mono">0.1.0 (45a57bb)</span>');
      expect(html).toContain('<span class="mono">0.1.0 (ca16cd7-dirty)</span>');
      patchStateForTests({ appCommit: "", health });
      const bare = renderToString(<SettingsScreen section="about" />);
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
      const html = renderToString(<SettingsScreen section="about" />);
      expect(html).toContain(`<span class="mono">0.1.0 (45a57bb) · ${at(desktopBuilt)}</span>`);
      expect(html).toContain(`<span class="mono">0.1.0 (ca16cd7-dirty) · ${at(daemonBuilt)}</span>`);
      // A time without a commit has no empty parentheses; a daemon built without make has no time.
      patchStateForTests({ appCommit: "", health: { ...health, commit: "ca16cd7" } });
      const partial = renderToString(<SettingsScreen section="about" />);
      expect(partial).toContain(`<span class="mono">0.1.0 · ${at(desktopBuilt)}</span>`);
      expect(partial).toContain('<span class="mono">0.1.0 (ca16cd7)</span>');
      patchStateForTests({ appBuiltAt: "not a time", health });
      expect(renderToString(<SettingsScreen section="about" />).match(/<span class="mono">0\.1\.0<\/span>/g)).toHaveLength(2);
    } finally {
      patchStateForTests({ appVersion, appCommit, appBuiltAt, health: undefined });
    }
  });

  it("shows the harness's capture attempt by attempt, saying when its start is left out", () => {
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

  it("shows the daemon's validation errors in the interface's language", () => {
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

  it("shows a harness whose first check runs as checking, in its card, its details, and the task editor", () => {
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
      <TaskForm doc={newDoc("general", "A")} onChange={() => {}} mode="create" harnesses={[harness, checking]} errorPaths={new Set()} />,
    ).replace(/<!-- -->/g, "");
    expect(form).toContain("Codex — Verificando…");
  });

  it("shows a harness's diagnostic and an execution's error in the interface's language", () => {
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

  it("reads a failed execution's notice with its reason in the interface's language", () => {
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

  it("asks for repositories only for the GitHub type", () => {
    const form = (doc: Record<string, unknown>) =>
      renderToString(<TaskForm doc={doc} onChange={() => {}} mode="create" harnesses={[harness]} errorPaths={new Set()} />);
    expect(form(newDoc("github", "A"))).toContain("Repositórios");
    expect(form(newDoc("general", "B"))).not.toContain("Repositórios");
  });

  it("labels the type GitHub and presents its repositories and limits as any GitHub task's", () => {
    // No catalog: the task selects no built-in implementer, and its form is the same.
    const html = renderToString(
      <TaskForm doc={newDoc("github", "Merge")} onChange={() => {}} mode="create" harnesses={[harness]} errorPaths={new Set()} />,
    );
    for (const shown of ["Parâmetros do tipo — GitHub", "Itens descobertos por ciclo", "Itens processados por execução", "Novas tentativas de um item que falhou"]) {
      expect(html).toContain(shown);
    }
    for (const hidden of ["Implementar issues do GitHub", "Issues descobertas", "Issues processadas", "uma issue que falhou", "Quais issues são elegíveis"]) {
      expect(html).not.toContain(hidden);
    }
  });

  it("asks a script task for its program, and for no prompt, agents, or harness", () => {
    const html = renderToString(
      <TaskForm doc={newDoc("script", "Auditoria")} onChange={() => {}} mode="create" harnesses={[harness]} errorPaths={new Set()} />,
    );
    for (const shown of ["Programa e argumentos", "Diretório de trabalho", "Variáveis de ambiente", "Executa em", "macOS", "Windows"]) {
      expect(html).toContain(shown);
    }
    for (const hidden of ["O que a tarefa faz vive aqui", "Agentes", "Harness preferido", "Cadeia de fallback", "Novas tentativas por limite de taxa"]) {
      expect(html).not.toContain(hidden);
    }
  });
});

// The card's Cancel sends the same single cancel as the detail's, and a finished execution's card cannot cancel it.
describe("the executions screen's cards", () => {
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

  it("names what runs the execution, an AI tool or a program, under one label", async () => {
    const script: ExecutionSummary = { ...running, id: "exec-script", taskType: "script", harness: "", model: undefined };
    await render(
      <>
        <ExecutionCard execution={running} selected={false} onSelect={() => {}} onCancel={() => {}} cancelling={false} />
        <ExecutionCard execution={script} selected={false} onSelect={() => {}} onCancel={() => {}} cancelling={false} />
      </>,
    );
    const runBy = [...host!.querySelectorAll(".exec-card__fields > div")]
      .filter((field) => field.querySelector("dt")!.textContent === "Executada por")
      .map((field) => field.querySelector("dd")!.textContent);
    expect(runBy).toEqual(["Claude Code", "Programa"]);
  });

  it("disables the card's Cancel for a finished execution", async () => {
    const onCancel = vi.fn();
    await render(<ExecutionCard execution={finished} selected={false} onSelect={() => {}} onCancel={onCancel} cancelling={false} />);
    const cancel = host!.querySelector<HTMLButtonElement>(".exec-card__cancel")!;
    expect(cancel.disabled).toBe(true);
    await act(async () => cancel.click());
    expect(onCancel).not.toHaveBeenCalled();
    expect(writes()).toEqual([]);
  });
});

// Settings → General: keeping HarmFlow in the menu bar on ⌘Q sits under the tray option, and needs the tray.
describe("the choice to keep HarmFlow in the menu bar on ⌘Q", () => {
  let root: Root | undefined;
  let host: HTMLElement | undefined;

  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = undefined;
    daemon.answers.clear();
    daemon.requests.length = 0;
    patchStateForTests({ settings });
  });

  const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));
  const rows = () => [...host!.querySelectorAll<HTMLLabelElement>("label.setting-row__label")];
  const keepToggle = () => document.getElementById(rows().find((l) => l.textContent === "Manter o HarmFlow na barra de menus ao sair com ⌘Q")!.htmlFor) as HTMLInputElement;

  async function render() {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<SettingsScreen />));
    await settle();
  }

  it("is off under the tray option, says what ⌘Q does, and saves once switched on", async () => {
    daemon.answers.set("/settings", settings);
    await render();
    const labels = rows().map((l) => l.textContent);
    expect(labels.indexOf("Manter o HarmFlow na barra de menus ao sair com ⌘Q")).toBe(labels.indexOf("Mostrar o HarmFlow na barra de menus") + 1);
    expect(host!.textContent).toContain("Ativado, ⌘Q apenas oculta a janela, como fechá-la");
    expect(keepToggle().checked).toBe(false);
    expect(keepToggle().disabled).toBe(false);

    // The daemon stores the patch, and the settings read after it hold it.
    daemon.answers.set("/settings", { ...settings, keepTrayOnQuit: true });
    daemon.requests.length = 0;
    await act(async () => keepToggle().click());
    await settle();
    expect(daemon.requests.filter((r) => !r.startsWith("GET "))).toEqual(["PATCH /settings"]);
    expect(keepToggle().checked).toBe(true);
  });

  it("is disabled while the tray is off", async () => {
    patchStateForTests({ settings: { ...settings, trayEnabled: false, keepTrayOnQuit: true } });
    daemon.answers.set("/settings", { ...settings, trayEnabled: false, keepTrayOnQuit: true });
    await render();
    expect(keepToggle().disabled).toBe(true);
    await act(async () => keepToggle().click());
    await settle();
    expect(daemon.requests.filter((r) => !r.startsWith("GET "))).toEqual([]);
  });
});

// Settings shows its sections in a list, in three groups and About, and one section at a time (harmflow-app#251).
describe("settings sections", () => {
  let root: Root | undefined;
  let host: HTMLElement | undefined;

  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = undefined;
    vi.unstubAllGlobals();
  });

  async function render(node: ReactNode) {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(node));
  }

  const items = () => [...host!.querySelectorAll<HTMLButtonElement>(".settings-nav__item")];
  const tooltip = () => document.body.querySelector(".tooltip")?.textContent ?? null;

  it("lists three groups under their headings, then About alone under a divider, and marks the open section", async () => {
    await render(<SettingsScreen section="storage" />);
    const nav = host!.querySelector("nav.settings-nav")!;
    const groups = [...nav.querySelectorAll('[role="group"]')].map((g) => ({
      heading: g.querySelector(".settings-nav__heading")!.textContent,
      sections: [...g.querySelectorAll(".settings-nav__item")].map((b) => b.textContent),
    }));
    expect(groups).toEqual([
      { heading: "Interface", sections: ["Geral", "Notificações"] },
      { heading: "Execução", sections: ["Ferramentas de IA", "Caminhos"] },
      { heading: "Este computador", sections: ["Daemon", "Armazenamento", "Segurança"] },
    ]);
    expect(nav.lastElementChild!.textContent).toBe("Sobre");
    expect(nav.lastElementChild!.previousElementSibling!.tagName).toBe("HR");
    for (const item of items()) expect(item.querySelector("svg")).not.toBeNull();

    const open = items().filter((b) => b.getAttribute("aria-current") === "page");
    expect(open.map((b) => b.textContent)).toEqual(["Armazenamento"]);
    expect(open[0].className).toContain("nav__item--active");
    expect(host!.querySelector(".settings__title")!.textContent).toBe("Armazenamento");
  });

  it("opens a section from the list with a click, Enter, or Space, which a button gives", async () => {
    const opened: string[] = [];
    await render(<SettingsScreen section="general" onSection={(s) => opened.push(s)} />);
    for (const item of items()) {
      expect(item.tagName).toBe("BUTTON");
      expect(item.tabIndex).toBe(0);
    }
    await act(async () => items().find((b) => b.textContent === "Segurança")!.click());
    expect(opened).toEqual(["security"]);
  });

  // Everything the single page showed is still there, each in the section that holds it.
  it.each([
    ["general", ["Idioma", "Barra de menus"], ["Mostrar o HarmFlow na barra de menus", "Manter o HarmFlow na barra de menus ao sair com ⌘Q"]],
    ["notifications", ["Avisos"], ["Nível de notificação"]],
    ["agents", ["Ferramentas de IA", "Ferramentas de linha de comando"], ["Detectar de novo", "Claude Code", "Definir caminho"]],
    ["paths", ["Variáveis de caminho", "Pastas permitidas"], ["PROJECTS_ROOT", "/srv/repos", "Adicionar variável", "Adicionar pasta"]],
    ["daemon", ["Estado", "Inicialização"], ["Em execução agora", "Parar o daemon", "Iniciar o daemon automaticamente com o sistema"]],
    ["storage", ["Logs", "Workspaces", "Diretórios"], ["Manter logs de execução por (dias)", "Limpar workspaces", "Disco usado pelo HarmFlow"]],
    ["security", ["API local"], ["Endereço", "Armazenamento do token", "Substituir o token da API"]],
    ["about", ["Versões"], ["Versão do painel", "Versão do daemon"]],
  ] as const)("shows %s with its panels and settings, and no other section's", (section, panels, shown) => {
    const html = renderToString(<SettingsScreen section={section} />).replace(/<!-- -->/g, "");
    const titles = [...html.matchAll(/<h2 class="panel__title">(.*?)<\/h2>/g)].map((m) => m[1].replace(/<[^>]+>/g, ""));
    expect(titles).toEqual(panels);
    for (const text of shown) expect(html).toContain(text);
    if (section !== "general") expect(html).not.toContain("Mostrar o HarmFlow na barra de menus");
  });

  it("keeps only the icons below 640 pixels of content box, with each name as its accessible name and tooltip", async () => {
    // happy-dom lays nothing out: this stand-in reports the width a test gives.
    let resize: (width: number) => void = () => {};
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(callback: ResizeObserverCallback) {
          resize = (width) => callback([{ contentRect: { width } } as ResizeObserverEntry], this as unknown as ResizeObserver);
        }
        observe() {}
        unobserve() {}
        disconnect() {}
      },
    );
    await render(<SettingsScreen section="general" />);
    const security = () => items().find((b) => b.textContent === "Segurança")!;
    const hidden = () => items().map((b) => b.querySelector(".nav__label")!.classList.contains("sr-only"));

    await act(async () => resize(640));
    expect(hidden().every((h) => !h)).toBe(true);
    await act(async () => void security().dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    expect(tooltip()).toBeNull(); // the name is in view

    await act(async () => resize(639));
    expect(host!.querySelector(".settings")!.className).toContain("settings--icons");
    expect(hidden().every((h) => h)).toBe(true);
    expect([...host!.querySelectorAll(".settings-nav__heading")].every((h) => h.classList.contains("sr-only"))).toBe(true);
    expect(security().textContent).toBe("Segurança");
    await act(async () => void security().dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    expect(tooltip()).toBe("Segurança");
    await act(async () => void security().dispatchEvent(new MouseEvent("mouseout", { bubbles: true })));
    await act(async () => security().focus());
    expect(tooltip()).toBe("Segurança");

    await act(async () => resize(900));
    expect(hidden().every((h) => !h)).toBe(true);
    expect(tooltip()).toBeNull(); // the list moved under it
  });
});

// The app's hidden login item starts the tray with the daemon at sign-in, so notices work with no window open: it is
// registered exactly while autostart is on and the tray is enabled, through every control that changes either one.
describe("the app's hidden login item", () => {
  let root: Root | undefined;
  let host: HTMLElement | undefined;
  let loginItem: MockInstance<typeof native.setLoginItem>;

  beforeEach(() => {
    daemon.requests.length = 0;
    daemon.bodies.length = 0;
    loginItem = vi.spyOn(native, "setLoginItem");
  });

  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = undefined;
    daemon.answers.clear();
    daemon.requests.length = 0;
    daemon.bodies.length = 0;
    loginItem.mockRestore();
    patchStateForTests({ settings });
  });

  const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));
  /** The requests that change something, with what each sent. */
  const writes = () => daemon.requests.map((request, i) => ({ request, body: daemon.bodies[i] })).filter((w) => !w.request.startsWith("GET "));
  const toggle = (id: "settings-autostart" | "settings-tray") => document.getElementById(id) as HTMLInputElement;

  async function render(node: ReactNode) {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(node));
    await settle();
  }

  async function click(input: HTMLElement) {
    await act(async () => input.click());
    await settle();
  }

  /** The settings the daemon holds: the screen reads them as it opens, and once it saved a patch. */
  const holds = (s: Partial<Settings>) => daemon.answers.set("/settings", { ...settings, ...s });

  /** Opens a section of Settings on the settings before, then has the daemon store the change as after. */
  async function open(section: "daemon" | "general", before: Partial<Settings>, after: Partial<Settings>) {
    patchStateForTests({ settings: { ...settings, ...before } });
    holds(before);
    await render(<SettingsScreen section={section} />);
    holds(after);
  }

  const autostart = (enabled: boolean) => ({ autostart: { enabled, mechanism: "launchd" as const } });

  it("turns on with autostart while the tray is enabled", async () => {
    await open("daemon", { trayEnabled: true, ...autostart(false) }, { trayEnabled: true, ...autostart(true) });
    await click(toggle("settings-autostart"));
    expect(writes()).toEqual([{ request: "PATCH /settings", body: { autostart: true } }]);
    expect(loginItem.mock.calls).toEqual([[true]]);
  });

  it("stays off when autostart turns on while the tray is disabled", async () => {
    await open("daemon", { trayEnabled: false, ...autostart(false) }, { trayEnabled: false, ...autostart(true) });
    await click(toggle("settings-autostart"));
    expect(writes()).toEqual([{ request: "PATCH /settings", body: { autostart: true } }]);
    expect(loginItem.mock.calls).toEqual([[false]]);
  });

  it("turns off with autostart", async () => {
    await open("daemon", { trayEnabled: true, ...autostart(true) }, { trayEnabled: true, ...autostart(false) });
    await click(toggle("settings-autostart"));
    expect(writes()).toEqual([{ request: "PATCH /settings", body: { autostart: false } }]);
    expect(loginItem.mock.calls).toEqual([[false]]);
  });

  it("follows the tray while autostart is on", async () => {
    await open("general", { trayEnabled: true, ...autostart(true) }, { trayEnabled: false, ...autostart(true) });
    await click(toggle("settings-tray"));
    expect(writes()).toEqual([{ request: "PATCH /settings", body: { trayEnabled: false } }]);
    expect(loginItem.mock.calls).toEqual([[false]]);

    holds({ trayEnabled: true, ...autostart(true) });
    await click(toggle("settings-tray"));
    expect(writes()).toEqual([
      { request: "PATCH /settings", body: { trayEnabled: false } },
      { request: "PATCH /settings", body: { trayEnabled: true } },
    ]);
    expect(loginItem.mock.calls).toEqual([[false], [true]]);
  });

  it("is left alone when the tray changes while autostart is off", async () => {
    await open("general", { trayEnabled: true, ...autostart(false) }, { trayEnabled: false, ...autostart(false) });
    await click(toggle("settings-tray"));
    holds({ trayEnabled: true, ...autostart(false) });
    await click(toggle("settings-tray"));
    expect(writes().map((w) => w.body)).toEqual([{ trayEnabled: false }, { trayEnabled: true }]);
    expect(loginItem).not.toHaveBeenCalled();
  });

  it.each([
    ["autostart", "daemon", "settings-autostart", { trayEnabled: true, ...autostart(false) }, { autostart: true }],
    ["the tray", "general", "settings-tray", { trayEnabled: true, ...autostart(true) }, { trayEnabled: false }],
  ] as const)("is left alone when the daemon refuses the patch of %s", async (_, section, id, before, patch) => {
    await open(section, before, before);
    // With no answer for /settings, the stand-in daemon refuses the patch with a 404.
    daemon.answers.delete("/settings");
    await click(toggle(id));
    expect(writes()).toEqual([{ request: "PATCH /settings", body: patch }]);
    expect(loginItem).not.toHaveBeenCalled();
  });

  it.each([true, false])("follows the tray (%s) when onboarding finishes with the autostart box checked", async (trayEnabled) => {
    daemon.answers.set("/settings", { ...settings, trayEnabled, ...autostart(true) });
    const onFinish = vi.fn();
    await render(<OnboardingScreen onFinish={onFinish} onCreateTask={() => {}} onImport={() => {}} />);
    // The footer's last button moves on: Next on steps 1 to 3, Skip on the first task, then the last step's finish.
    const forward = () => click([...host!.querySelectorAll<HTMLButtonElement>(".onboarding__footer button")].at(-1)!);
    await forward();
    await forward();
    await click(host!.querySelector<HTMLInputElement>(".onboarding__step .checkbox input")!);
    await forward();
    await forward();
    expect(loginItem).not.toHaveBeenCalled();
    await forward();
    expect(onFinish).toHaveBeenCalledTimes(1);
    expect(writes()).toEqual([{ request: "PATCH /settings", body: { onboardingCompleted: true, autostart: true } }]);
    expect(loginItem.mock.calls).toEqual([[trayEnabled]]);
  });
});
