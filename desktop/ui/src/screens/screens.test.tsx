// @vitest-environment happy-dom
// Renders each screen from a known state, to catch rendering errors without a browser or a daemon; the executions
// screen's cards are also driven in a DOM, against a stand-in for the daemon's local API.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act, useState, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";
import { native } from "../api/native";
import type { Execution, ExecutionSummary, HarmflowEvent, Harness, Health, Notification, Settings, Spend, Task, TaskSummary, WorkItem } from "../api/types";
import { ExecutionCard } from "../components/ExecutionCard";
import { ExecutionDetail, RawCaptures } from "../components/ExecutionDetail";
import { HarnessInventory } from "../components/Harnesses";
import { Icon } from "../components/Icon";
import { ValidationPanel } from "../components/TaskEditor";
import { TaskForm } from "../components/TaskForm";
import i18n, { resources } from "../i18n";
import { formatBytes, formatDateTime } from "../lib/format";
import { SHORTCUT_GROUPS, shortcutsByGroup } from "../lib/shortcuts";
import { slugify, type Doc } from "../lib/taskForm";
import { actions, getState, patchStateForTests } from "../state/store";
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

/** The rows of the Executions screen's recent history: the aside's buttons that lead with a status mark. */
const historyRows = (host: Element) => [...host.querySelectorAll(".aside-panel button")].filter((b) => b.querySelector(".status-mark"));
const rowTitle = (row: Element) => row.querySelector(".list-row__title")!.textContent;

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
  showRestrictionWarnings: true,
  showSystemWidget: true,
  showGitHubWidget: true,
  notificationLevel: "all",
  logRetentionDays: 30,
  theme: "harmflow-dark",
  autostart: { enabled: false, mechanism: "launchd" },
  pathVariables: { PROJECTS_ROOT: "/Users/me/Projects" },
  allowedFolders: ["/srv/repos"],
  onboardingCompleted: true,
  quota: {},
  systemPrompt: "",
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
      noWorkToday: 2,
      activity: { running: [1, 2, 3], queued: [0, 1], completed: [3, 4], failed: [0, 1], noWork: [1, 1] },
    },
    system: { cpuPercent: 12, memoryUsedBytes: 2.1e9, memoryTotalBytes: 16e9, diskUsedBytes: 48e9, diskTotalBytes: 500e9 },
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
    expect(html).toContain("Trocando de ferramenta de IA");
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

  it("shows the No work card after Failed today, with today's count, on the Overview and the Executions screen", () => {
    const screens = [
      renderToString(<OverviewScreen onOpenExecution={() => {}} onNewTask={() => {}} onOpenTask={() => {}} />),
      renderToString(<ExecutionsScreen selectedId={null} onSelect={() => {}} onGoToTasks={() => {}} onSeeHistory={() => {}} />),
    ];
    for (const html of screens) {
      const host = document.createElement("div");
      host.innerHTML = html;
      const cards = [...host.querySelectorAll(".counters .counter")];
      expect(cards.map((c) => c.querySelector(".counter__label")!.textContent)).toEqual([
        i18n.t("counters.running"),
        i18n.t("counters.queued"),
        i18n.t("counters.completedToday"),
        i18n.t("counters.failedToday"),
        i18n.t("counters.noWorkToday"),
      ]);
      const noWork = cards[4];
      expect(noWork.querySelector(".counter__value")!.textContent).toBe("2");
      expect(noWork.className).toBe("counter counter--violet");
      // Its tone is one no other card uses.
      expect(cards.slice(0, 4).filter((c) => c.classList.contains("counter--violet"))).toHaveLength(0);
    }
  });

  it("names the No work card in every language the app ships", () => {
    const labels = Object.entries(resources).map(([lang, bundle]) => [lang, bundle.translation.counters.noWorkToday]);
    expect(labels).toEqual([
      ["en-US", "No work today"],
      ["pt-BR", "Sem trabalho hoje"],
      ["es-ES", "Sin trabajo hoy"],
    ]);
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
    for (const hidden of ["O que a tarefa faz vive aqui", "Agentes", "Ferramenta de IA preferida", "Cadeia de fallback", "Novas tentativas por limite de taxa"]) {
      expect(html).not.toContain(hidden);
    }
  });
});

// An execution that found no work is no failure: the recent history marks it No work, in the neutral tone and with the
// circle, beside a failed one's danger mark and cross, and the Overview leaves it out of what needs attention. The
// labels are read in en-US, which these tests switch to from pt-BR, as the History screen's tests render.
describe("an execution that found no work", () => {
  const noWork: ExecutionSummary = { ...finished, id: "exec-no-work", taskName: "Look for new issues", status: "no_work", workItem: undefined, pullRequestUrl: undefined, testsStatus: undefined };
  const failed: ExecutionSummary = { ...finished, id: "exec-failed", taskName: "Write release notes", status: "failed", workItem: undefined, pullRequestUrl: undefined, testsStatus: undefined };

  /** The screen rendered in en-US from a recent history holding both executions, as a DOM to query. */
  async function show(node: ReactNode) {
    const { recent } = getState();
    await i18n.changeLanguage("en-US");
    patchStateForTests({ recent: [noWork, failed] });
    try {
      const host = document.createElement("div");
      host.innerHTML = renderToString(node);
      return host;
    } finally {
      patchStateForTests({ recent });
      await i18n.changeLanguage("pt-BR");
    }
  }

  /** The icon as the DOM writes it, to compare with the one a mark holds. */
  const icon = (name: "circle" | "x") => {
    const holder = document.createElement("div");
    holder.innerHTML = renderToString(<Icon name={name} size={16} />);
    return holder.innerHTML;
  };

  /** A status mark's tone, label, and icon. */
  const mark = (row: Element) => {
    const m = row.querySelector(".status-mark")!;
    const tones = [...m.classList].filter((c) => c.startsWith("status-mark--"));
    return { tones, title: m.getAttribute("title"), label: m.querySelector(".sr-only")!.textContent, icon: m.querySelector("svg")!.outerHTML };
  };

  it("lists the waiting work items in the queue, and marks the execution No work in the recent history, apart from a failed one", async () => {
    const host = await show(<ExecutionsScreen selectedId={null} onSelect={() => {}} onGoToTasks={() => {}} onSeeHistory={() => {}} />);
    const text = host.textContent!;
    expect(text).toContain("Draft PR with failing tests");
    expect(text).toContain("Issue to retry");
    expect(text).not.toContain("Completed issue");

    const rows = historyRows(host);
    const row = (name: string) => rows.find((r) => rowTitle(r) === name)!;
    expect(rows.map(rowTitle)).toEqual([noWork.taskName, failed.taskName]);

    expect(mark(row(noWork.taskName))).toEqual({ tones: ["status-mark--neutral"], title: "No work", label: "No work", icon: icon("circle") });
    expect(row(noWork.taskName).textContent).not.toContain("Failed");
    expect(mark(row(failed.taskName))).toEqual({ tones: ["status-mark--danger"], title: "Failed", label: "Failed", icon: icon("x") });
  });

  it("leaves it out of the Overview's executions that need attention, which hold the failed one", async () => {
    const host = await show(<OverviewScreen onOpenExecution={() => {}} onNewTask={() => {}} onOpenTask={() => {}} />);
    const panel = [...host.querySelectorAll("section.panel")].find((p) => p.querySelector(".panel__title")?.textContent === "Needs your attention")!;
    expect(panel).toBeDefined();
    const rows = [...panel.querySelectorAll("li")];
    expect(rows.map(rowTitle)).toEqual([failed.taskName]);
    expect(mark(rows[0])).toEqual({ tones: ["status-mark--danger"], title: "Failed", label: "Failed", icon: icon("x") });
    expect(panel.textContent).not.toContain(noWork.taskName);
    expect(panel.textContent).not.toContain("No work");
  });
});

// The Overview's Spend this month band is the element right after the four counters, a direct child of the screen
// ahead of the panels' grid, in the cost view and in the token view. The unit tests' DOM does no layout: the band's
// full width is read from that place, in the screen's flex column, and from its class, which no rule narrows.
describe("the Overview's spend band", () => {
  let root: Root | undefined;
  let host: HTMLElement | undefined;

  const none = { unpriced: 0, notReported: 0, partial: 0, running: 0 };
  const month: Spend = {
    month: "2026-10",
    daysInMonth: 31,
    days: [
      { date: "2026-10-01", costUsd: 2.5, tokens: 1_200_000, attempts: 3, undercount: none, harnesses: [{ harness: "claude-code", costUsd: 2.5, tokens: 1_200_000 }] },
      { date: "2026-10-02", costUsd: 0.75, tokens: 3_000_000, attempts: 2, undercount: none, harnesses: [{ harness: "codex", costUsd: 0.75, tokens: 3_000_000 }] },
      { date: "2026-10-03", costUsd: 1, tokens: 400_000, attempts: 1, undercount: none, harnesses: [{ harness: "claude-code", costUsd: 1, tokens: 400_000 }] },
    ],
    total: { costUsd: 4.25, tokens: 4_600_000 },
  };

  beforeAll(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  });

  // The band opens in the view the last launch kept: none kept, it opens in the cost view.
  beforeEach(() => localStorage.clear());

  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = undefined;
    daemon.answers.clear();
    daemon.requests.length = 0;
    localStorage.clear();
    patchStateForTests({ spend: undefined });
  });

  const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

  /** The Overview, the month's spend read from the stand-in daemon as the store reads it. */
  async function open() {
    daemon.answers.set("/spend", month);
    await act(() => actions.refreshSpend());
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<OverviewScreen onOpenExecution={() => {}} onNewTask={() => {}} onOpenTask={() => {}} />));
    await settle();
  }

  /** Checks the band's place in the screen, and answers the band. */
  function band() {
    const screen = host!.firstElementChild!;
    expect(screen.classList.contains("screen")).toBe(true);
    const bands = host!.querySelectorAll("section.spend");
    expect(bands).toHaveLength(1);
    const spend = bands[0];
    expect(spend.classList.contains("panel")).toBe(true);
    // A direct child of the screen, right after the counters, which are the screen's first element.
    expect(spend.parentElement).toBe(screen);
    const counters = spend.previousElementSibling!;
    expect(counters.className).toBe("counters");
    expect(counters.querySelectorAll(".counter")).toHaveLength(5);
    expect(counters.previousElementSibling).toBeNull();
    // Ahead of the panels' grid, and not inside it.
    const grid = spend.nextElementSibling!;
    expect(grid.className).toBe("overview-grid");
    expect(spend.closest(".overview-grid")).toBeNull();
    expect(grid.querySelector(".spend")).toBeNull();
    return spend;
  }

  const choice = (spend: Element, view: "cost" | "tokens") =>
    [...spend.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find((b) => b.textContent === i18n.t(`overview.spend.${view}`))!;

  it("is the element right after the counters, a direct child of the screen ahead of the panels' grid, in the cost view and in the token view", async () => {
    await open();
    expect(daemon.requests).toContain("GET /spend");

    const cost = band();
    expect(cost.querySelector('[role="radiogroup"]')!.getAttribute("aria-label")).toBe(i18n.t("overview.spend.view"));
    expect(choice(cost, "cost").getAttribute("aria-checked")).toBe("true");
    // The month the stand-in served: one column per day it holds.
    expect(cost.querySelectorAll(".spend__slot--day")).toHaveLength(month.days.length);

    await act(async () => choice(cost, "tokens").click());
    const tokens = band();
    expect(choice(tokens, "tokens").getAttribute("aria-checked")).toBe("true");
    expect(choice(tokens, "cost").getAttribute("aria-checked")).toBe("false");
    expect(tokens.querySelectorAll(".spend__slot--day")).toHaveLength(month.days.length);
  });

  it("takes the screen's full width, which no rule of the stylesheet narrows", () => {
    const css = readFileSync(join(__dirname, "..", "design", "app.css"), "utf8");
    // The screen is a flex column that stretches its children, the band among them, across its width.
    const screen = /(?:^|\n)\.screen \{([^}]*)\}/.exec(css)![1];
    expect(screen).toMatch(/display: flex;/);
    expect(screen).toMatch(/flex-direction: column;/);
    expect(screen).not.toMatch(/align-items:/);
    // No rule that names the band's own class sets its width or its place across the column.
    const rules = [...css.matchAll(/(?:^|\n)([^{}\n]*\.spend(?![\w-])[^{}\n]*)\{([^}]*)\}/g)];
    for (const [, selector, body] of rules) expect(body, selector).not.toMatch(/(?:^|[;\s])(?:width|max-width|align-self|margin(?:-left|-right)?)\s*:/);
  });
});

// An execution a daemon stop or a crash ended before it finished is no failure and no success: the recent history marks
// it Interrupted, in the warning tone and with the alert icon, beside a failed one's danger mark and cross. The labels
// are read in en-US, as above, and in pt-BR, the language these tests otherwise render in.
describe("an execution that was interrupted", () => {
  const interrupted: ExecutionSummary = { ...finished, id: "exec-interrupted", taskName: "Look for new issues", status: "interrupted", workItem: undefined, pullRequestUrl: undefined, testsStatus: undefined };
  const failed: ExecutionSummary = { ...finished, id: "exec-failed", taskName: "Write release notes", status: "failed", workItem: undefined, pullRequestUrl: undefined, testsStatus: undefined };

  /** The Executions screen rendered in `language` from a recent history holding both executions, as a DOM to query. */
  async function show(language: string) {
    const { recent } = getState();
    await i18n.changeLanguage(language);
    patchStateForTests({ recent: [interrupted, failed] });
    try {
      const host = document.createElement("div");
      host.innerHTML = renderToString(<ExecutionsScreen selectedId={null} onSelect={() => {}} onGoToTasks={() => {}} onSeeHistory={() => {}} />);
      return host;
    } finally {
      patchStateForTests({ recent });
      await i18n.changeLanguage("pt-BR");
    }
  }

  /** The icon as the DOM writes it, to compare with the one a mark holds. */
  const icon = (name: "alert" | "x") => {
    const holder = document.createElement("div");
    holder.innerHTML = renderToString(<Icon name={name} size={16} />);
    return holder.innerHTML;
  };

  /** Each row of the recent history by its task's name, with its status mark's tone, label, and icon. */
  const marks = (host: HTMLElement) =>
    historyRows(host).map((row) => {
      const m = row.querySelector(".status-mark")!;
      return {
        name: rowTitle(row),
        tones: [...m.classList].filter((c) => c.startsWith("status-mark--")),
        title: m.getAttribute("title"),
        label: m.querySelector(".sr-only")!.textContent,
        icon: m.querySelector("svg")!.outerHTML,
      };
    });

  it("marks it Interrupted in the recent history, in the warning tone with the alert icon, apart from a failed one", async () => {
    const host = await show("en-US");
    expect(marks(host)).toEqual([
      { name: interrupted.taskName, tones: ["status-mark--warning"], title: "Interrupted", label: "Interrupted", icon: icon("alert") },
      { name: failed.taskName, tones: ["status-mark--danger"], title: "Failed", label: "Failed", icon: icon("x") },
    ]);
    const row = historyRows(host)[0];
    expect(row.textContent).not.toContain("Failed");
    expect(row.querySelector(".status-mark--danger")).toBeNull();
  });

  it("writes its label in the interface's language", async () => {
    const host = await show("pt-BR");
    expect(marks(host).map(({ name, title, label }) => ({ name, title, label }))).toEqual([
      { name: interrupted.taskName, title: "Interrompida", label: "Interrompida" },
      { name: failed.taskName, title: "Falhou", label: "Falhou" },
    ]);
  });
});

// Every active execution has its own card, with its own fields and Cancel. The card's Cancel sends the same single
// cancel as the detail's, and a finished execution's card cannot cancel it.
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

  it("lists three running executions, each card with its own task, AI tool, repository, issue, elapsed time, state, and Cancel", async () => {
    // Three GitHub tasks running at once, as the local API lists them, each with its own task, AI tool, repository,
    // work item, start, and active state; then what each one's card must show, its elapsed time to the fixed clock.
    const three: ExecutionSummary[] = [
      { ...running, id: "exec-core", taskId: "core-issues", taskName: "Core issues", harness: "claude-code", status: "running", repository: "org/core-banking", workItem: "org/core-banking#123", createdAt: "2026-10-20T19:21:20-03:00", startedAt: "2026-10-20T19:21:28-03:00" },
      { ...running, id: "exec-payments", taskId: "payments-fixes", taskName: "Payments fixes", harness: "codex", status: "preparing", repository: "org/payments", workItem: "org/payments#45", createdAt: "2026-10-20T19:29:50-03:00", startedAt: "2026-10-20T19:29:55-03:00" },
      { ...running, id: "exec-handbook", taskId: "handbook-docs", taskName: "Handbook docs", harness: "codex", status: "switching_harness", repository: "org/handbook", workItem: "org/handbook#7", createdAt: "2026-10-20T18:14:58-03:00", startedAt: "2026-10-20T18:15:00-03:00" },
    ];
    const shown = [
      { title: "Core issues · core-banking#123", task: "core-issues", runBy: "Claude Code", repository: "org/core-banking", elapsed: "8 min 32 s", state: "Executando" },
      { title: "Payments fixes · payments#45", task: "payments-fixes", runBy: "Codex", repository: "org/payments", elapsed: "5 s", state: "Preparando" },
      { title: "Handbook docs · handbook#7", task: "handbook-docs", runBy: "Codex", repository: "org/handbook", elapsed: "1 h 15 min", state: "Trocando de ferramenta de IA" },
    ];
    for (const e of three) daemon.answers.set(`/executions/${e.id}/events`, { events: [], logExpired: false });
    daemon.answers.set(`/executions/${three[0].id}`, { ...detail, ...three[0] });
    const { active, details, events, stats } = getState();
    // Only the clock is fixed: the stand-in daemon's answers still arrive through real timers.
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    vi.setSystemTime(new Date("2026-10-20T19:30:00-03:00"));
    try {
      patchStateForTests({ active: three, stats: stats && { ...stats, running: 3 } });
      await render(<ExecutionsScreen selectedId={null} onSelect={() => {}} onGoToTasks={() => {}} onSeeHistory={() => {}} />);
      expect(host!.querySelector(".screen__main .section-title h2")!.textContent).toContain("Execuções ativas (3)");
      const cards = [...host!.querySelectorAll<HTMLElement>(".exec-card")];
      expect(cards).toHaveLength(3);

      cards.forEach((card, i) => {
        // Every query starts from this card's element, so another card's text cannot satisfy it.
        const field = (label: string) =>
          [...card.querySelectorAll(".exec-card__fields > div")].find((f) => f.querySelector("dt")!.textContent === label)!.querySelector("dd")!.textContent!.trim();
        expect(card.querySelector(".exec-card__title")!.textContent).toBe(shown[i].title);
        expect(field("Tarefa")).toBe(shown[i].task);
        expect(field("Executada por")).toBe(shown[i].runBy);
        expect(field("Repositório")).toBe(shown[i].repository);
        expect(card.querySelector(".exec-card__elapsed")!.textContent!.trim()).toBe(shown[i].elapsed);
        expect(card.querySelector(".exec-card__header .chip")!.textContent).toBe(shown[i].state);
        const cancel = card.querySelector<HTMLButtonElement>(".exec-card__cancel")!;
        expect(cancel.textContent).toBe("Cancelar");
        expect(cancel.disabled).toBe(false);
        for (const other of shown.filter((_, j) => j !== i)) expect(card.textContent).not.toContain(other.task);
      });
    } finally {
      vi.useRealTimers();
      patchStateForTests({ active, details, events, stats });
    }
  });

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

  describe("their row as a carousel", () => {
    // happy-dom lays nothing out, so every size reads 0: each test gives the row the sizes of a window 1440 px wide,
    // whose row shows three cards and a 48 px slice in 1000 px, and spies on the scrolls it asks for.
    const runningMany = (n: number): ExecutionSummary[] =>
      Array.from({ length: n }, (_, i) => ({ ...running, id: `exec-row-${i + 1}`, taskId: `row-${i + 1}`, taskName: `Row ${i + 1}` }));
    const PAGE = 1000 - 48;
    let reducedMotion = false;
    let select: (id: string | null) => void = () => {};
    const onSelect = vi.fn();

    /** The Executions screen with its own selection, which select changes as the queue or the history does. */
    function Screen() {
      const [selected, setSelected] = useState<string | null>(null);
      select = setSelected;
      return (
        <ExecutionsScreen
          selectedId={selected}
          onSelect={(id) => {
            onSelect(id);
            setSelected(id);
          }}
          onGoToTasks={() => {}}
          onSeeHistory={() => {}}
        />
      );
    }

    const row = () => host!.querySelector<HTMLElement>(".exec-cards")!;
    const wrapper = () => host!.querySelector<HTMLElement>(".exec-carousel")!;
    const controls = () => host!.querySelector(".exec-carousel__controls");
    const buttons = () => [...host!.querySelectorAll<HTMLButtonElement>(".exec-carousel__controls button")];
    const position = () => host!.querySelector(".exec-carousel__position")?.textContent;
    const rect = (left: number, width: number) => ({ left, right: left + width, width, top: 0, bottom: 200, height: 200, x: left, y: 0, toJSON: () => ({}) }) as DOMRect;

    /**
     * Renders n running cards, gives the row its sizes and the scroll position, and lets it measure on a scroll. The
     * row's width follows its cards, so a card starting or ending widens or narrows it as the layout would, and its
     * scroll position stays within its ends: a row that narrows has it clamped to its new end, as a browser does.
     */
    async function showRow(n: number, scrollLeft = 0) {
      for (const e of runningMany(n)) daemon.answers.set(`/executions/${e.id}/events`, { events: [], logExpired: false });
      patchStateForTests({ active: runningMany(n) });
      await render(<Screen />);
      const element = row();
      const step = PAGE / 3;
      Object.defineProperty(element, "clientWidth", { configurable: true, value: 1000 });
      Object.defineProperty(element, "scrollWidth", { configurable: true, get: () => Math.max(1000, element.children.length * step - 16 + 48) });
      const clamp = (left: number) => Math.min(Math.max(left, 0), element.scrollWidth - element.clientWidth);
      let left = scrollLeft;
      Object.defineProperty(element, "scrollLeft", {
        configurable: true,
        get: () => (left = clamp(left)),
        set: (value: number) => void (left = clamp(value)),
      });
      vi.spyOn(element, "getBoundingClientRect").mockReturnValue(rect(0, 1000));
      vi.spyOn(wrapper(), "getBoundingClientRect").mockReturnValue(rect(0, 1000));
      const scrollTo = vi.fn();
      element.scrollTo = scrollTo as never;
      await act(async () => void element.dispatchEvent(new Event("scroll")));
      return scrollTo;
    }

    beforeEach(() => {
      reducedMotion = false;
      onSelect.mockClear();
      vi.stubGlobal("matchMedia", (query: string) => ({
        matches: query === "(prefers-reduced-motion: reduce)" ? reducedMotion : false,
        media: query,
        addEventListener() {},
        removeEventListener() {},
      }));
    });

    let saved: ReturnType<typeof getState>;
    beforeEach(() => {
      saved = getState();
    });
    // This runs before the outer afterEach unmounts the screen, so React sees each change. The stand-in answers the
    // running and the recent executions from one route, so a refresh sets both.
    afterEach(async () => {
      await act(async () => {
        patchStateForTests({ active: saved.active, recent: saved.recent });
        await i18n.changeLanguage("pt-BR");
      });
    });

    it("shows no control while the cards fit the row, and is a labelled region announced as a carousel", async () => {
      await showRow(3);
      expect(host!.querySelectorAll(".exec-card")).toHaveLength(3);
      expect(controls()).toBeNull();
      expect(wrapper().className).toBe("exec-carousel");
      expect(row().getAttribute("role")).toBe("region");
      expect(row().getAttribute("aria-roledescription")).toBe("carousel");
      expect(row().getAttribute("aria-label")).toBe("Execuções em andamento");
    });

    it("with four cards at its start, enables Next, disables Previous, and reads which cards show, in each language", async () => {
      await showRow(4);
      expect(host!.querySelector(".screen__main .section-title")!.contains(controls())).toBe(true);
      const [previous, next] = buttons();
      expect(previous.disabled).toBe(true);
      expect(next.disabled).toBe(false);
      expect(wrapper().classList.contains("exec-carousel--more-end")).toBe(true);
      expect(wrapper().classList.contains("exec-carousel--more-start")).toBe(false);

      const shown: Record<string, { position: string; previous: string; next: string }> = {
        "en-US": { position: "1–3 of 4", previous: "Previous executions", next: "Next executions" },
        "pt-BR": { position: "1–3 de 4", previous: "Execuções anteriores", next: "Próximas execuções" },
        "es-ES": { position: "1–3 de 4", previous: "Ejecuciones anteriores", next: "Ejecuciones siguientes" },
      };
      for (const [language, expected] of Object.entries(shown)) {
        await act(async () => void i18n.changeLanguage(language));
        expect(position(), language).toBe(expected.position);
        expect(buttons().map((b) => b.getAttribute("aria-label")), language).toEqual([expected.previous, expected.next]);
      }
    });

    it("pages by the cards visible: Next asks for a scroll of one page, and the last page disables Next", async () => {
      const scrollTo = await showRow(6);
      await act(async () => buttons()[1].click());
      expect(scrollTo).toHaveBeenCalledWith({ left: expect.closeTo(PAGE), behavior: "smooth" });

      // At its end, the last three cards show, and Previous asks for a scroll back to the start.
      const element = row();
      element.scrollLeft = element.scrollWidth - element.clientWidth;
      await act(async () => void element.dispatchEvent(new Event("scroll")));
      expect(position()).toBe("4–6 de 6");
      expect(buttons().map((b) => b.disabled)).toEqual([false, true]);
      expect(wrapper().classList.contains("exec-carousel--more-start")).toBe(true);
      expect(wrapper().classList.contains("exec-carousel--more-end")).toBe(false);
      await act(async () => buttons()[0].click());
      expect(scrollTo).toHaveBeenLastCalledWith({ left: 0, behavior: "smooth" });
    });

    it("between its ends, shows a whole card at the left with no slice over it, and a slice at the left only at the end", async () => {
      // Seven cards, a page in: the fourth to the sixth show whole, the fourth flush left, and a slice of the seventh
      // at the right.
      const scrollTo = await showRow(7, PAGE);
      expect(position()).toBe("4–6 de 7");
      expect(buttons().map((b) => b.disabled)).toEqual([false, false]);
      expect(wrapper().classList.contains("exec-carousel--more-start")).toBe(false);
      expect(wrapper().classList.contains("exec-carousel--more-end")).toBe(true);

      // No overlay covers the whole card at the left: a click near the left edge pages nothing, and a click on that
      // card selects it and leaves the row where it is.
      await act(async () => void wrapper().dispatchEvent(new MouseEvent("click", { bubbles: true, clientX: 10 })));
      expect(scrollTo).not.toHaveBeenCalled();
      await act(async () => host!.querySelectorAll<HTMLElement>(".exec-card")[3].click());
      expect(onSelect).toHaveBeenCalledWith("exec-row-4");
      expect(scrollTo).not.toHaveBeenCalled();

      // At the end, a slice of the card before the visible ones shows at the left, and a click on it pages back.
      const element = row();
      element.scrollLeft = element.scrollWidth - element.clientWidth;
      await act(async () => void element.dispatchEvent(new Event("scroll")));
      expect(position()).toBe("5–7 de 7");
      expect(wrapper().classList.contains("exec-carousel--more-start")).toBe(true);
      expect(wrapper().classList.contains("exec-carousel--more-end")).toBe(false);
      await act(async () => void wrapper().dispatchEvent(new MouseEvent("click", { bubbles: true, clientX: 10 })));
      expect(scrollTo).toHaveBeenCalledWith({ left: expect.closeTo(PAGE / 3), behavior: "smooth" });
    });

    it("jumps instead of scrolling smoothly while the system asks for reduced motion", async () => {
      reducedMotion = true;
      const scrollTo = await showRow(4);
      await act(async () => buttons()[1].click());
      expect(scrollTo).toHaveBeenCalledWith({ left: expect.closeTo(PAGE), behavior: "auto" });
    });

    it("pages one page toward the right slice on a click there, cancelling and selecting nothing", async () => {
      const confirm = vi.fn((_message?: string) => true);
      vi.stubGlobal("confirm", confirm);
      const scrollTo = await showRow(4);
      // The slice's overlay is the wrapper's pseudo-element: a click on it targets the wrapper itself.
      await act(async () => void wrapper().dispatchEvent(new MouseEvent("click", { bubbles: true, clientX: 990 })));
      expect(scrollTo).toHaveBeenCalledWith({ left: expect.closeTo(PAGE), behavior: "smooth" });
      expect(confirm).not.toHaveBeenCalled();
      expect(writes()).toEqual([]);
      expect(onSelect).not.toHaveBeenCalled();

      // At the start, the left edge shows no slice: a click there moves nothing.
      scrollTo.mockClear();
      await act(async () => void wrapper().dispatchEvent(new MouseEvent("click", { bubbles: true, clientX: 10 })));
      expect(scrollTo).not.toHaveBeenCalled();
    });

    it("scrolls a card selected from the queue into view, and a click on a whole card moves nothing", async () => {
      const scrollTo = await showRow(5);
      const cards = [...host!.querySelectorAll<HTMLElement>(".exec-card")];
      const intoView = cards.map((card, i) => {
        vi.spyOn(card, "getBoundingClientRect").mockReturnValue(rect(i * (PAGE / 3), PAGE / 3 - 16));
        const spy = vi.fn();
        card.scrollIntoView = spy;
        return spy;
      });

      await act(async () => select("exec-row-5"));
      expect(intoView[4]).toHaveBeenCalledWith({ block: "nearest", inline: "nearest", behavior: "smooth" });

      // The second card shows whole: clicking it selects it and leaves the row where it is.
      await act(async () => cards[1].click());
      expect(onSelect).toHaveBeenCalledWith("exec-row-2");
      expect(cards[1].classList.contains("exec-card--selected")).toBe(true);
      expect(intoView[1]).not.toHaveBeenCalled();
      expect(scrollTo).not.toHaveBeenCalled();
    });

    it("keeps the cards in view where they were as executions start and end, and hides its controls once they fit", async () => {
      // The local API lists the running executions newest first, so one that starts comes before the others. Five run,
      // the row paged to its end: the third to the fifth show. A card in view is selected before the sixth starts, so
      // this proves what the row does for the cards in view while a selection is held. With nothing selected, the one
      // that starts becomes the default and the row scrolls it into view, which "with nothing selected, shows the card
      // of an execution that starts" proves.
      const STEP = PAGE / 3;
      const [one, two, three, four, five] = runningMany(5);
      const sixth: ExecutionSummary = { ...running, id: "exec-row-new", taskId: "row-new", taskName: "Row new" };
      daemon.answers.set(`/executions/${sixth.id}/events`, { events: [], logExpired: false });
      const scrollTo = await showRow(5, 5 * STEP + 32 - 1000);
      const element = row();
      // The row lands on each scroll it asks for, within its ends.
      scrollTo.mockImplementation(({ left = 0 }: ScrollToOptions) => {
        element.scrollLeft = Math.min(Math.max(left, 0), element.scrollWidth - element.clientWidth);
      });
      // Each card's box from where the row is scrolled, so a selected card reads in view only where it is; the row and
      // its wrapper keep their own.
      const box = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
        const i = [...element.children].indexOf(this);
        return i < 0 ? rect(0, 0) : rect(i * STEP - element.scrollLeft, STEP - 16);
      });
      const intoView = vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => {});

      /** Where each whole card in view sits from the row's left edge, by its task's name. */
      const inView = () => {
        const places = new Map<string, number>();
        host!.querySelectorAll(".exec-card__title").forEach((title, i) => {
          const left = i * STEP - element.scrollLeft;
          if (left > -1 && left + STEP - 16 < element.clientWidth + 1) places.set(title.getAttribute("title")!, left);
        });
        return places;
      };

      /**
       * Lists next as the running executions, lets the store read them through its own refresh, and checks that the
       * scroll the row asks for keeps each card that was in view and still runs where it was.
       */
      async function change(next: ExecutionSummary[]) {
        const before = inView();
        const from = element.scrollLeft;
        scrollTo.mockClear();
        daemon.answers.set("/executions", next);
        await act(async () => {
          await actions.refreshExecutions();
        });
        await settle();
        const asked = (scrollTo.mock.calls.at(-1)?.[0] as ScrollToOptions | undefined)?.left ?? from;
        const names = [...host!.querySelectorAll(".exec-card__title")].map((title) => title.getAttribute("title"));
        expect(names).toEqual(next.map((e) => e.taskName));
        for (const [name, left] of before) {
          if (names.includes(name)) expect(names.indexOf(name) * STEP - asked, name).toBeCloseTo(left);
        }
        expect(intoView).not.toHaveBeenCalled();
      }

      try {
        expect(position()).toBe("3–5 de 5");
        expect([...inView().keys()]).toEqual(["Row 3", "Row 4", "Row 5"]);

        // The fourth, whole in view, is selected: the row stays where it is.
        await act(async () => host!.querySelectorAll<HTMLElement>(".exec-card")[3].click());
        expect(onSelect).toHaveBeenLastCalledWith("exec-row-4");
        expect(scrollTo).not.toHaveBeenCalled();
        expect(intoView).not.toHaveBeenCalled();

        // A sixth starts, before the others, while the fourth stays selected: the row moves one card along, and the
        // same three show.
        await change([sixth, one, two, three, four, five]);
        expect(scrollTo).toHaveBeenCalledTimes(1);
        expect(position()).toBe("4–6 de 6");
        expect([...inView().keys()]).toEqual(["Row 3", "Row 4", "Row 5"]);
        expect(host!.querySelectorAll(".exec-card")[4].classList.contains("exec-card--selected")).toBe(true);

        // The first and the sixth end, both before the cards in view: the row moves back two cards.
        await change([two, three, four, five]);
        expect(position()).toBe("2–4 de 4");
        expect([...inView().keys()]).toEqual(["Row 3", "Row 4", "Row 5"]);

        // Two more end, one at a time. With three left the row fits: no Previous, no Next, no position.
        await change([three, four, five]);
        expect(controls()).toBeNull();
        expect(buttons()).toEqual([]);
        expect(position()).toBeUndefined();
        await change([three, four]);
        expect(controls()).toBeNull();
        expect(position()).toBeUndefined();
      } finally {
        intoView.mockRestore();
        box.mockRestore();
      }
    });

    it("at its end, keeps the cards in view and the selection where they were when two cards before them end", async () => {
      // Six run, the row paged to its end so the fourth to the sixth show, and the fifth selected. The first two end:
      // the row narrows, and its scroll position is clamped to the new end before the row scrolls back by them.
      const STEP = PAGE / 3;
      const six = runningMany(6);
      const scrollTo = await showRow(6, 6 * STEP + 32 - 1000);
      const element = row();
      scrollTo.mockImplementation(({ left = 0 }: ScrollToOptions) => {
        element.scrollLeft = left;
      });
      const intoView = vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => {});
      const names = () => [...host!.querySelectorAll(".exec-card__title")].map((title) => title.getAttribute("title"));
      /** Where a card sits from the row's left edge, by its task's name. */
      const place = (name: string) => names().indexOf(name) * STEP - element.scrollLeft;
      try {
        expect(position()).toBe("4–6 de 6");
        await act(async () => host!.querySelectorAll<HTMLElement>(".exec-card")[4].click());
        expect(onSelect).toHaveBeenLastCalledWith("exec-row-5");
        const before = ["Row 4", "Row 5", "Row 6"].map(place);

        daemon.answers.set("/executions", six.slice(2));
        await act(async () => {
          await actions.refreshExecutions();
        });
        await settle();

        expect(names()).toEqual(["Row 3", "Row 4", "Row 5", "Row 6"]);
        expect(scrollTo).toHaveBeenCalledTimes(1);
        expect(scrollTo).toHaveBeenCalledWith({ left: expect.closeTo(4 * STEP + 32 - 1000) });
        expect(element.scrollLeft).toBeCloseTo(element.scrollWidth - element.clientWidth);
        ["Row 4", "Row 5", "Row 6"].forEach((name, i) => expect(place(name), name).toBeCloseTo(before[i]));
        expect(position()).toBe("2–4 de 4");
        expect(host!.querySelectorAll(".exec-card")[2].classList.contains("exec-card--selected")).toBe(true);
        expect(intoView).not.toHaveBeenCalled();
      } finally {
        intoView.mockRestore();
      }
    });

    it("with nothing selected, shows the card of an execution that starts, which becomes the default", async () => {
      // Five run, nothing selected, the row at its end. A newer one starts, listed first: it becomes the default, its
      // card is the selected one, and the row scrolls it into view.
      const STEP = PAGE / 3;
      const sixth: ExecutionSummary = { ...running, id: "exec-row-new", taskId: "row-new", taskName: "Row new" };
      daemon.answers.set(`/executions/${sixth.id}/events`, { events: [], logExpired: false });
      await showRow(5, 5 * STEP + 32 - 1000);
      const element = row();
      // Each card's box from where the row is scrolled; the row and its wrapper keep their own.
      const box = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
        const i = [...element.children].indexOf(this);
        return i < 0 ? rect(0, 0) : rect(i * STEP - element.scrollLeft, STEP - 16);
      });
      const intoView = vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => {});
      try {
        expect(position()).toBe("3–5 de 5");
        daemon.answers.set("/executions", [sixth, ...runningMany(5)]);
        await act(async () => {
          await actions.refreshExecutions();
        });
        await settle();

        const cards = [...host!.querySelectorAll<HTMLElement>(".exec-card")];
        expect(cards[0].querySelector(".exec-card__title")!.getAttribute("title")).toBe("Row new");
        expect(cards[0].classList.contains("exec-card--selected")).toBe(true);
        expect(intoView).toHaveBeenCalledTimes(1);
        expect(intoView.mock.contexts[0]).toBe(cards[0]);
      } finally {
        intoView.mockRestore();
        box.mockRestore();
      }
    });

    it("never moves on its own: left untouched for five minutes at its start, it asks for no scroll", async () => {
      // Every kind of timer a rotation could use is fake from before the row mounts, so one it started would run
      // within the five minutes. setImmediate stays real: the stand-in's answers arrive through it.
      vi.useFakeTimers({
        toFake: ["Date", "setTimeout", "clearTimeout", "setInterval", "clearInterval", "requestAnimationFrame", "cancelAnimationFrame"],
      });
      const intoView = vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => {});
      try {
        const scrollTo = await showRow(6);
        expect(position()).toBe("1–3 de 6");
        await act(async () => void vi.advanceTimersByTime(5 * 60 * 1000));
        expect(scrollTo).not.toHaveBeenCalled();
        expect(intoView).not.toHaveBeenCalled();
        expect(row().scrollLeft).toBe(0);
        expect(position()).toBe("1–3 de 6");
      } finally {
        intoView.mockRestore();
        vi.useRealTimers();
      }
    });

    it("is laid out by the stylesheet: three cards, or two below 1240 px, and a 48 px slice fill it, each card snaps by its start, and a slice is dimmed only while its class is set", () => {
      // happy-dom applies no stylesheet, so the rules are read as app.css writes them. The snap after a partial scroll,
      // and Tab bringing a half-hidden card's Cancel into view, are the browser's and only a running app shows them.
      const css = readFileSync(join(__dirname, "..", "design", "app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
      const found: { selectors: string[]; media: string | null; body: string }[] = [];
      const open: string[] = [];
      for (const [, text, brace] of css.matchAll(/([^{}]*)([{}])/g)) {
        if (brace === "{") {
          open.push(text.trim());
          continue;
        }
        const head = open.pop()!;
        if (!head.startsWith("@")) found.push({ selectors: head.split(",").map((s) => s.trim()), media: open.at(-1) ?? null, body: text });
      }
      /** The declarations that apply to selector, at the top level or inside media, the later rule winning. */
      const declared = (selector: string, media: string | null = null) => {
        const values = new Map<string, string>();
        for (const rule of found.filter((r) => r.media === media && r.selectors.includes(selector))) {
          for (const declaration of rule.body.split(";")) {
            const colon = declaration.indexOf(":");
            if (colon > 0) values.set(declaration.slice(0, colon).trim(), declaration.slice(colon + 1).trim());
          }
        }
        return values;
      };

      // Three columns and their three gaps, the last one before the slice, leave 48 px of the row: whether or not the
      // cards overflow it, so a fourth that starts resizes none of them.
      const cards = declared(".exec-cards");
      expect(cards.get("display")).toBe("grid");
      expect(cards.get("grid-auto-flow")).toBe("column");
      expect(cards.get("gap")).toBe("var(--hf-space-4)");
      expect(cards.get("grid-auto-columns")).toBe("calc((100% - 3 * var(--hf-space-4) - 48px) / 3)");
      expect(cards.get("overflow-x")).toBe("auto");
      expect(declared(".exec-cards", "@media (max-width: 1240px)").get("grid-auto-columns")).toBe("calc((100% - 2 * var(--hf-space-4) - 48px) / 2)");

      // The row snaps to a whole card at its left.
      expect(cards.get("scroll-snap-type")).toBe("x mandatory");
      expect(declared(".exec-card").get("scroll-snap-align")).toBe("start");

      // Each slice's overlay sits at its edge, hidden until the class that says more cards lie that way is set.
      const before = declared(".exec-carousel::before");
      const after = declared(".exec-carousel::after");
      expect(before.get("left")).toBe("0");
      expect(after.get("right")).toBe("0");
      expect(before.get("opacity")).toBe("0");
      expect(after.get("opacity")).toBe("0");
      expect(declared(".exec-carousel--more-start::before").get("opacity")).toBe("1");
      expect(declared(".exec-carousel--more-end::after").get("opacity")).toBe("1");
      expect(declared(".exec-carousel--more-start::after").has("opacity")).toBe(false);
      expect(declared(".exec-carousel--more-end::before").has("opacity")).toBe(false);
    });
  });

  describe("its effort beside the model", () => {
    /** Renders the cards and reads each one's badges: the text, whether it is marked configured, and its tooltip. */
    async function badgesOf(executions: ExecutionSummary[]) {
      await render(
        <>
          {executions.map((execution) => (
            <ExecutionCard key={execution.id} execution={execution} selected={false} onSelect={() => {}} onCancel={() => {}} cancelling={false} />
          ))}
        </>,
      );
      return [...host!.querySelectorAll(".exec-card")].map((card) =>
        [...card.querySelectorAll<HTMLElement>(".harness__model")].map((badge) => ({
          text: badge.textContent,
          effort: badge.classList.contains("harness__effort"),
          configured: badge.classList.contains("harness__model--configured"),
          title: badge.title,
        })),
      );
    }

    it("shows the effort the harness reported beside its model, unmarked", async () => {
      const reported = { ...running, model: "claude-opus-5-5", effort: "xhigh", configuredModel: "claude-opus-5-5", configuredEffort: "xhigh" };
      expect(await badgesOf([reported])).toEqual([
        [
          { text: "claude-opus-5-5", effort: false, configured: false, title: "" },
          { text: "xhigh", effort: true, configured: false, title: "Esforço: xhigh" },
        ],
      ]);
    });

    it("shows the configured effort, marked as configured, when the harness reported none", async () => {
      const configured = { ...running, model: "claude-opus-5-5", configuredModel: "claude-opus-5-5", configuredEffort: "high" };
      expect(await badgesOf([configured])).toEqual([
        [
          { text: "claude-opus-5-5", effort: false, configured: false, title: "" },
          { text: "high", effort: true, configured: true, title: "Esforço configurado: high. O harness não informou o esforço que usou." },
        ],
      ]);
    });

    it("shows nothing for an effort of default, reported or configured", async () => {
      const configuredDefault = { ...running, id: "exec-a", model: "claude-opus-5-5", configuredModel: "default", configuredEffort: "default" };
      const reportedDefault = { ...running, id: "exec-b", model: "claude-opus-5-5", effort: "default", configuredModel: "default", configuredEffort: "high" };
      const onlyModel = [{ text: "claude-opus-5-5", effort: false, configured: false, title: "" }];
      expect(await badgesOf([configuredDefault, reportedDefault])).toEqual([onlyModel, onlyModel]);
    });

    it("shows the configured model, marked as configured, when the harness reported none", async () => {
      const codex = { ...running, harness: "codex", model: undefined, configuredModel: "gpt-5.5-codex", configuredEffort: "medium" };
      expect(await badgesOf([codex])).toEqual([
        [
          { text: "gpt-5.5-codex", effort: false, configured: true, title: "Modelo configurado: gpt-5.5-codex. O harness não informou o modelo que usou." },
          { text: "medium", effort: true, configured: true, title: "Esforço configurado: medium. O harness não informou o esforço que usou." },
        ],
      ]);
    });

    it("shows no effort on a script task's card", async () => {
      const script = { ...running, id: "exec-script", taskType: "script", harness: "", model: undefined };
      expect(await badgesOf([script])).toEqual([[]]);
      expect(host!.querySelector(".exec-card .harness--script")!.textContent).toBe("Programa");
    });
  });

  it("shows each task type's glyph and tone on the card, the New task dialog's, never the settings gear", async () => {
    const types = ["github", "general", "script", "webhook"];
    await render(
      <>
        {types.map((taskType) => (
          <ExecutionCard
            key={taskType}
            execution={{ ...running, id: `exec-${taskType}`, taskType }}
            selected={false}
            onSelect={() => {}}
            onCancel={() => {}}
            cancelling={false}
          />
        ))}
      </>,
    );
    const glyphs = [...host!.querySelectorAll<HTMLElement>(".exec-card__glyph")].map((span) => ({
      icon: span.querySelector("svg")!.dataset.icon,
      tones: [...span.classList].filter((c) => c.startsWith("type-glyph--")),
    }));
    expect(glyphs).toEqual([
      { icon: "code", tones: ["type-glyph--blue"] },
      { icon: "sparkle", tones: ["type-glyph--violet"] },
      { icon: "terminal", tones: ["type-glyph--blue"] },
      { icon: "dot", tones: ["type-glyph--blue"] },
    ]);
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

  // A running execution's card shows its current step, never a percentage: the latest timeline item of the events the
  // store holds, else the daemon's step in the interface's language, else the waiting text. The store's events come
  // from its own replay of the stand-in daemon's answers.
  describe("its step row", () => {
    /** Answers the execution's stored events as the local API does, lets the store read them, and renders its card. */
    async function showCard(execution: ExecutionSummary, events: HarmflowEvent[]) {
      daemon.answers.set(`/executions/${execution.id}/events`, { events, logExpired: false });
      await act(() => actions.loadEvents(execution.id));
      await render(<ExecutionCard execution={execution} selected={false} onSelect={() => {}} onCancel={() => {}} cancelling={false} />);
      return host!.querySelector(".exec-card")!;
    }
    // Only the row's own text, so that the rest of the card cannot satisfy an assertion.
    const stepOf = (card: Element) => card.querySelector("[data-testid='execution-step'] .exec-card__step-text")!.textContent;
    function expectNoPercentage(card: Element) {
      expect(card.textContent).not.toMatch(/\d\s*%/);
      expect(card.querySelector("progress, [role='progressbar'], [aria-valuenow]")).toBeNull();
    }

    let held: ReturnType<typeof getState>["events"];
    beforeAll(() => {
      held = getState().events;
    });
    afterEach(() => {
      patchStateForTests({ events: held });
    });

    it("shows the daemon's step when the store holds no events", async () => {
      const card = await showCard({ ...running, id: "exec-step-daemon", currentStep: "Running tests", currentStepKey: undefined }, []);
      expect(getState().events["exec-step-daemon"]).toEqual([]);
      expect(stepOf(card)).toBe("Running tests");
      expectNoPercentage(card);
    });

    it("shows a step the daemon names by key in the interface's language, with its parameters", async () => {
      const card = await showCard(
        {
          ...running,
          id: "exec-step-key",
          currentStep: "Starting Claude Code",
          currentStepKey: "step_starting_harness",
          currentStepParams: { harness: "Claude Code" },
        },
        [],
      );
      expect(stepOf(card)).toBe("Iniciando Claude Code");
      expectNoPercentage(card);
    });

    it("shows the latest timeline item of the events over the daemon's step, and follows a later event", async () => {
      const execution: ExecutionSummary = { ...running, id: "exec-step-events", currentStep: "Running tests" };
      const card = await showCard(execution, [
        ev("execution.started", { executionId: execution.id, message: "Loading configuration" }),
        ev("tool.started", { executionId: execution.id, payload: { toolUseId: "t1", tool: "Bash", input: { command: "./gradlew build" } } }),
      ]);
      expect(stepOf(card)).toBe("Bash · ./gradlew build");
      expectNoPercentage(card);

      // A later event, as the store's next read after the last one it holds gets it.
      const later = ev("test.started", { executionId: execution.id, message: "./gradlew test" });
      daemon.answers.set(`/executions/${execution.id}/events`, { events: [later], logExpired: false });
      await act(() => actions.loadEvents(execution.id));
      expect(daemon.requests.at(-1)).toBe(`GET /executions/${execution.id}/events?after=${later.id - 1}&limit=5000`);
      expect(stepOf(card)).toBe("Executando testes · ./gradlew test");
      expectNoPercentage(card);
    });

    it("shows the waiting text in the interface's language before any step", async () => {
      const card = await showCard({ ...running, id: "exec-step-none", currentStep: undefined }, []);
      expect(stepOf(card)).toBe("Aguardando a primeira etapa…");
      expectNoPercentage(card);
    });
  });
});

// Settings → General, harmflow-app#464: the tray option is on with the daemon's fresh settings, and switching it off
// saves it and tells the shell, which then lets a close of the window end the interface instead of hiding it.
describe("the tray option", () => {
  let root: Root | undefined;
  let host: HTMLElement | undefined;

  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = undefined;
    daemon.answers.clear();
    daemon.requests.length = 0;
    daemon.bodies.length = 0;
    patchStateForTests({ settings });
    vi.restoreAllMocks();
  });

  const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));
  const trayToggle = () => {
    const label = [...host!.querySelectorAll<HTMLLabelElement>("label.setting-row__label")].find((l) => l.textContent === "Mostrar o HarmFlow na barra de menus")!;
    return document.getElementById(label.htmlFor) as HTMLInputElement;
  };

  it("is on with the fresh settings, and switched off sends one patch and tells the shell", async () => {
    const shell = vi.spyOn(native, "setTrayEnabled").mockResolvedValue();
    daemon.answers.set("/settings", settings);
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<SettingsScreen />));
    await settle();
    expect(trayToggle().checked).toBe(true);
    expect(trayToggle().disabled).toBe(false);

    // The daemon stores the patch, and the settings read after it hold it.
    daemon.answers.set("/settings", { ...settings, trayEnabled: false });
    daemon.requests.length = 0;
    daemon.bodies.length = 0;
    shell.mockClear();
    await act(async () => trayToggle().click());
    await settle();
    expect(daemon.requests.filter((r) => !r.startsWith("GET "))).toEqual(["PATCH /settings"]);
    expect(daemon.bodies[daemon.requests.indexOf("PATCH /settings")]).toEqual({ trayEnabled: false });
    // A settings refresh sends the stored value to the shell too, so every call says false, and there is one at least.
    expect(shell).toHaveBeenCalledWith(false);
    expect(shell.mock.calls.every(([enabled]) => enabled === false)).toBe(true);
    expect(trayToggle().checked).toBe(false);
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
    daemon.bodies.length = 0;
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

  it("is off and cannot be turned on once the tray is switched off in Settings", async () => {
    // A first use: the settings of a fresh data folder, tray on and the preference off.
    daemon.answers.set("/settings", settings);
    await render();
    expect(keepToggle().checked).toBe(false);
    expect(keepToggle().disabled).toBe(false);

    // The daemon stores the patch, and the settings read after it hold the tray off.
    daemon.answers.set("/settings", { ...settings, trayEnabled: false });
    daemon.requests.length = 0;
    daemon.bodies.length = 0;
    await act(async () => (document.getElementById("settings-tray") as HTMLInputElement).click());
    await settle();
    expect(daemon.requests.filter((r) => !r.startsWith("GET "))).toEqual(["PATCH /settings"]);
    expect(daemon.bodies[daemon.requests.indexOf("PATCH /settings")]).toEqual({ trayEnabled: false });
    expect(keepToggle().checked).toBe(false);
    expect(keepToggle().disabled).toBe(true);

    daemon.requests.length = 0;
    await act(async () => keepToggle().click());
    await settle();
    expect(daemon.requests.filter((r) => !r.startsWith("GET "))).toEqual([]);
    expect(keepToggle().checked).toBe(false);
  });
});

// Settings → Notifications: the global level offers its three values, and the one chosen is what the daemon receives.
// What the daemon does with each level is app › TestNotificationLevelFiltersTheTasksEvents's.
describe("the global notification level", () => {
  let root: Root | undefined;
  let host: HTMLElement | undefined;

  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = undefined;
    daemon.answers.clear();
    daemon.requests.length = 0;
    daemon.bodies.length = 0;
    patchStateForTests({ settings });
  });

  const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));
  const level = () => host!.querySelector<HTMLSelectElement>("#settings-notification-level")!;

  async function render() {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<SettingsScreen section="notifications" />));
    await settle();
  }

  /** Picks an option as the user does, so that React sees the change. */
  async function choose(value: string) {
    const setValue = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(level(), value);
      level().dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
  }

  it("offers exactly all, failures and attention only, and none, with the saved level selected", async () => {
    daemon.answers.set("/settings", settings);
    await render();
    expect([...level().options].map((o) => [o.value, o.textContent])).toEqual([
      ["all", "Todas"],
      ["failures_and_attention", "Somente falhas e atenção"],
      ["none", "Nenhuma"],
    ]);
    expect(level().value).toBe("all");
  });

  it.each(["failures_and_attention", "none", "all"] as const)("sends notificationLevel %s once it is chosen", async (chosen) => {
    const before = { ...settings, notificationLevel: chosen === "all" ? "none" : "all" } as const;
    patchStateForTests({ settings: before });
    daemon.answers.set("/settings", before);
    await render();
    expect(level().value).toBe(before.notificationLevel);

    // The daemon stores the patch, and the settings read after it hold it.
    daemon.answers.set("/settings", { ...settings, notificationLevel: chosen });
    daemon.requests.length = 0;
    daemon.bodies.length = 0;
    await choose(chosen);
    const sent = daemon.requests.map((request, i) => ({ request, body: daemon.bodies[i] })).filter((r) => r.body !== undefined);
    expect(sent).toEqual([{ request: "PATCH /settings", body: { notificationLevel: chosen } }]);
    expect(daemon.requests.filter((r) => !r.startsWith("GET "))).toEqual(["PATCH /settings"]);
    expect(level().value).toBe(chosen);
  });
});

// Settings → General: the language select offers the three official languages and no other, and apart from them the
// choice to follow the system's language (harmflow-app#525).
describe("the language setting", () => {
  let root: Root | undefined;
  let host: HTMLElement | undefined;

  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = undefined;
    daemon.answers.clear();
    daemon.requests.length = 0;
  });

  it("offers pt-BR, en-US, and es-ES, each once, and the system's language apart from them", async () => {
    daemon.answers.set("/settings", settings);
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<SettingsScreen section="general" />));
    await act(() => new Promise<void>((resolve) => setImmediate(resolve)));

    const select = host!.querySelector<HTMLSelectElement>("select#settings-language")!;
    const options = [...select.options].map((o) => ({ value: o.value, label: o.textContent }));
    // Each language is named in its own language, whatever the interface's.
    expect(options.filter((o) => o.value !== "system")).toEqual([
      { value: "pt-BR", label: "Português (Brasil)" },
      { value: "en-US", label: "English (US)" },
      { value: "es-ES", label: "Español" },
    ]);
    // Following the system picks one of the three; it is a choice, not a fourth language.
    expect(options.filter((o) => o.value === "system")).toEqual([{ value: "system", label: "Idioma do sistema" }]);
    expect(select.value).toBe("pt-BR");
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
      { heading: "Interface", sections: ["Geral", "Aparência", "Notificações", "Atalhos"] },
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
    [
      "general",
      ["Idioma", "Barra de menus", "Execuções", "Barra lateral"],
      [
        "Mostrar o HarmFlow na barra de menus",
        "Manter o HarmFlow na barra de menus ao sair com ⌘Q",
        "Mostrar avisos de restrição",
        "Mostrar o widget Sistema",
        "Mostrar o widget GitHub",
      ],
    ],
    ["appearance", ["Zoom", "Fonte"], ["O mesmo nível de ⌘+, ⌘− e ⌘0", "HarmFlow", "Sistema"]],
    ["notifications", ["Avisos"], ["Nível de notificação"]],
    [
      "shortcuts",
      ["Em todo o painel", "Zoom, também nos diálogos", "Editor de tarefas", "Tela de tarefas", "Tela de execuções", "Linha do tempo da execução", "Diálogos e menus"],
      ["Nova tarefa", "Salvar a tarefa", "Evento anterior", "Fechar o diálogo ou o menu"],
    ],
    [
      "agents",
      ["Ferramentas de IA", "Cota", "Prompt de sistema geral", "Ferramentas de linha de comando"],
      ["Detectar de novo", "Claude Code", "Definir caminho", "parte de cada janela da cota", "Instruções para todas as sessões"],
    ],
    ["paths", ["Variáveis de caminho", "Pastas permitidas"], ["PROJECTS_ROOT", "/srv/repos", "Adicionar variável", "Adicionar pasta"]],
    ["daemon", ["Estado", "Inicialização"], ["Em execução agora", "Parar o daemon", "Iniciar o daemon automaticamente com o sistema"]],
    ["storage", ["Logs", "Workspaces", "Diretórios"], ["Manter logs de execução por (dias)", "Limpar workspaces", "Disco usado pelo HarmFlow"]],
    ["security", ["API local"], ["Armazenamento do token", "Substituir o token da API"]],
    ["about", ["Versões"], ["Versão do painel", "Versão do daemon"]],
  ] as const)("shows %s with its panels and settings, and no other section's", (section, panels, shown) => {
    const html = renderToString(<SettingsScreen section={section} />).replace(/<!-- -->/g, "");
    const titles = [...html.matchAll(/<h2 class="panel__title">(.*?)<\/h2>/g)].map((m) => m[1].replace(/<[^>]+>/g, ""));
    expect(titles).toEqual(panels);
    for (const text of shown) expect(html).toContain(text);
    if (section !== "general") expect(html).not.toContain("Mostrar o HarmFlow na barra de menus");
    // The typeface choice is Appearance's alone.
    if (section !== "appearance") expect(html).not.toContain('id="settings-typeface"');
  });

  // Appearance holds the zoom and the typeface and nothing else, the zoom at the level the shell gives and set through
  // it; no control has a reset action (harmflow-app#825).
  it("shows Zoom at the shell's level and Font in Appearance, with no reset action, and sets the zoom through the shell", async () => {
    const set: number[] = [];
    await render(<SettingsScreen section="appearance" zoom={110} onZoom={(level) => set.push(level)} />);
    const section = host!.querySelector(".settings__section")!;
    expect([...section.querySelectorAll("select")].map((s) => s.id)).toEqual(["settings-zoom", "settings-typeface"]);
    expect([...section.querySelectorAll("button, input, textarea")]).toEqual([]);

    const zoom = section.querySelector<HTMLSelectElement>("#settings-zoom")!;
    expect([...zoom.options].map((o) => o.textContent)).toEqual(["80%", "90%", "100%", "110%", "125%", "150%"]);
    expect(zoom.value).toBe("110");
    const setValue = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(zoom, "150");
      zoom.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(set).toEqual([150]);

    const typeface = section.querySelector<HTMLSelectElement>("#settings-typeface")!;
    expect([...typeface.options].map((o) => [o.value, o.textContent])).toEqual([
      ["harmflow", "HarmFlow"],
      ["system", "Sistema"],
    ]);
  });

  // Every shortcut of the table, in its group, as keycaps, in the interface's language; none it does not hold.
  it.each([
    ["en-US", "Anywhere in the dashboard", "New task", "Quit HarmFlow, or hide the window when it is kept in the menu bar"],
    ["pt-BR", "Em todo o painel", "Nova tarefa", "Encerrar o HarmFlow, ou ocultar a janela quando ele fica na barra de menus"],
    ["es-ES", "En todo el panel", "Nueva tarea", "Salir de HarmFlow, u ocultar la ventana cuando se mantiene en la barra de menús"],
  ])("lists every shortcut in its group with its keycaps, in %s", async (language, dashboard, newTask, quit) => {
    await act(async () => void i18n.changeLanguage(language));
    try {
      await render(<SettingsScreen section="shortcuts" />);
      const panels = [...host!.querySelectorAll("section.panel")];
      const listed = shortcutsByGroup().map(({ group }, i) => {
        const panel = panels[i];
        expect(panel.querySelector(".panel__title")!.textContent).toBe(i18n.t(`settings.shortcuts.groups.${group}`));
        return [...panel.querySelectorAll<HTMLElement>("[data-shortcut]")].map((row) => row.dataset.shortcut);
      });
      expect(panels).toHaveLength(SHORTCUT_GROUPS.length);
      expect(listed).toEqual(shortcutsByGroup().map(({ ids }) => ids));

      const row = (id: string) => host!.querySelector(`[data-shortcut="${id}"]`)!.closest(".setting-row")!;
      const caps = (id: string) => [...row(id).querySelectorAll("kbd.keycap")].map((k) => k.textContent);
      expect(panels[0].querySelector(".panel__title")!.textContent).toBe(dashboard);
      expect(row("newTask").querySelector(".setting-row__label")!.textContent).toBe(newTask);
      expect(row("quit").querySelector(".setting-row__label")!.textContent).toBe(quit);
      expect(caps("newTask")).toEqual(["⌘", "N"]);
      expect(caps("settings")).toEqual(["⌘", ","]);
      expect(caps("zoomIn")).toEqual(["⌘", "+", "⌘", "="]);
      expect(caps("zoomOut")).toEqual(["⌘", "−"]);
      expect(caps("previousEvent")).toEqual(["↑"]);
      expect(caps("lastEvent")).toEqual(["End"]);
      expect(caps("close")).toEqual(["Esc"]);

      // Read-only: the section holds nothing to press or type in.
      const section = host!.querySelector(".settings__section")!;
      expect([...section.querySelectorAll("button, input, select, textarea, a")]).toEqual([]);
      // No macOS standard key of the Edit or Window menus.
      const text = section.textContent!;
      for (const key of ["⌘C", "⌘V", "⌘Z", "⌘M", "⌘W"]) expect(text.replace(/\s/g, "")).not.toContain(key);
    } finally {
      await act(async () => void i18n.changeLanguage("pt-BR"));
    }
  });

  // The local API's address changes with every start of the daemon and is nothing the user calls, so Security shows
  // only the token's storage and its replacement.
  it("shows no Address row in Security", () => {
    const html = renderToString(<SettingsScreen section="security" />).replace(/<!-- -->/g, "");
    const labels = [...html.matchAll(/<span class="setting-row__label">(.*?)<\/span>/g)].map((m) => m[1]);
    expect(labels).toEqual(["Armazenamento do token", "Token da API"]);
    expect(labels).not.toContain("Endereço");
  });

  // The languages are the ones the app ships: nothing in the panel adds, imports, or downloads one.
  it("holds the language select alone in the language panel", async () => {
    await render(<SettingsScreen section="general" />);
    const panel = host!.querySelector("#settings-language")!.closest("section.panel")!;
    expect(panel.querySelector(".panel__title")!.textContent).toBe("Idioma");
    const rows = panel.querySelectorAll(".setting-row");
    expect(rows).toHaveLength(1);
    expect([...rows[0].querySelectorAll(".setting-row__control select")].map((s) => s.id)).toEqual(["settings-language"]);
    expect([...panel.querySelectorAll("button, input, textarea, a, [role='button'], [role='link']")].map((e) => e.outerHTML)).toEqual([]);
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

// The History screen's period: its start and end dates reach the daemon as the instants of the user's days, from 00:00
// of the start date to 00:00 of the day after the end date; an end before the start sends nothing.
describe("the History screen's period", () => {
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
    // Each test opens History as on a new launch, with nothing kept from the one before.
    patchStateForTests({ history: undefined });
  });

  /** Lets the stand-in daemon's answers arrive and React render them. */
  const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

  async function open() {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<HistoryScreen openId={null} onOpen={() => {}} />));
    await settle();
  }

  /** Picks a day in the date input of label, as the date picker does; an empty day clears it. */
  async function pick(label: string, day: string) {
    const input = host!.querySelector<HTMLInputElement>(`input[type="date"][aria-label="${label}"]`)!;
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(input, day);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await settle();
  }

  /** 00:00 of a day in the user's time zone, as the query writes the instant. */
  const midnight = (year: number, month: number, day: number) => encodeURIComponent(new Date(year, month - 1, day).toISOString());
  const banner = () => host!.querySelector(".banner")?.textContent ?? null;

  it("sends the chosen period's bounds, nothing for an end before the start, and no bounds once cleared", async () => {
    daemon.answers.set("/executions", []);
    await open();
    expect(daemon.requests).toEqual(["GET /executions?limit=50"]);

    daemon.requests.length = 0;
    await pick("De", "2026-10-20");
    expect(daemon.requests).toEqual([`GET /executions?limit=50&createdFrom=${midnight(2026, 10, 20)}`]);

    daemon.requests.length = 0;
    await pick("Até", "2026-10-22");
    expect(daemon.requests).toEqual([`GET /executions?limit=50&createdFrom=${midnight(2026, 10, 20)}&createdUntil=${midnight(2026, 10, 23)}`]);
    expect(banner()).toBeNull();

    // One day: from its 00:00 to the next day's.
    daemon.requests.length = 0;
    await pick("Até", "2026-10-20");
    expect(daemon.requests).toEqual([`GET /executions?limit=50&createdFrom=${midnight(2026, 10, 20)}&createdUntil=${midnight(2026, 10, 21)}`]);

    // An end before the start is not sent, and the screen says the period is invalid.
    daemon.requests.length = 0;
    await pick("Até", "2026-10-19");
    expect(daemon.requests).toEqual([]);
    expect(banner()).toBe("A data final é anterior à data inicial. Escolha um período válido.");

    // Cleared, each date's bound goes, and with both cleared the list is every execution again.
    await pick("Até", "");
    expect(banner()).toBeNull();
    expect(daemon.requests).toEqual([`GET /executions?limit=50&createdFrom=${midnight(2026, 10, 20)}`]);
    daemon.requests.length = 0;
    await pick("De", "");
    expect(daemon.requests).toEqual(["GET /executions?limit=50"]);
  });

  it("loads the next page of the same period", async () => {
    const page: ExecutionSummary[] = Array.from({ length: 50 }, (_, i) => ({
      ...finished,
      id: `exec-${i}`,
      createdAt: new Date(2026, 9, 22, 23, 50 - i).toISOString(),
    }));
    daemon.answers.set("/executions", page);
    await open();
    await pick("De", "2026-10-20");
    await pick("Até", "2026-10-22");

    daemon.requests.length = 0;
    const more = [...host!.querySelectorAll<HTMLButtonElement>(".table__footer button")].find((b) => b.textContent === "Carregar execuções mais antigas")!;
    await act(async () => more.click());
    await settle();
    expect(daemon.requests).toEqual([
      `GET /executions?limit=50&before=${encodeURIComponent(page[49].createdAt)}&createdFrom=${midnight(2026, 10, 20)}&createdUntil=${midnight(2026, 10, 23)}`,
    ]);
  });
});

// Settings can hide the banner an execution's detail shows for each restriction its harness does not enforce, on by
// default; the Events tab and the other banners stay.
describe("the restriction warnings setting", () => {
  let root: Root | undefined;
  let host: HTMLElement | undefined;

  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = undefined;
    daemon.answers.clear();
    daemon.requests.length = 0;
    daemon.bodies.length = 0;
    patchStateForTests({ settings });
  });

  const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));
  const toggle = () => document.getElementById("settings-restriction-warnings") as HTMLInputElement;
  const alerts = () => [...host!.querySelectorAll(".detail__alerts strong")].map((s) => s.textContent);

  async function render(node: ReactNode) {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(node));
    await settle();
  }

  async function showDetail() {
    const exec = { ...detail, id: "exec-restrictions" };
    daemon.answers.set(`/executions/${exec.id}`, exec);
    daemon.answers.set(`/executions/${exec.id}/events`, {
      events: [
        ev("policy.unenforced", { executionId: exec.id, severity: "warning", message: "network: deny" }),
        ev("repository.unprotected", { executionId: exec.id, severity: "warning", message: "main" }),
      ],
      logExpired: false,
    });
    await render(<ExecutionDetail executionId={exec.id} />);
  }

  it("is on by default, and saves once switched off", async () => {
    expect(settings.showRestrictionWarnings).toBe(true);
    daemon.answers.set("/settings", settings);
    await render(<SettingsScreen />);
    expect(toggle().checked).toBe(true);

    daemon.answers.set("/settings", { ...settings, showRestrictionWarnings: false });
    daemon.requests.length = 0;
    daemon.bodies.length = 0;
    await act(async () => toggle().click());
    await settle();
    expect(daemon.requests.filter((r) => !r.startsWith("GET "))).toEqual(["PATCH /settings"]);
    expect(daemon.bodies[daemon.requests.indexOf("PATCH /settings")]).toEqual({ showRestrictionWarnings: false });
    expect(toggle().checked).toBe(false);
  });

  it("shows the restriction banner while on, and hides only it, at once, while off", async () => {
    await showDetail();
    expect(alerts()).toEqual(["Restrição não aplicada", "Branch base sem proteção"]);

    act(() => patchStateForTests({ settings: { ...settings, showRestrictionWarnings: false } }));
    expect(alerts()).toEqual(["Branch base sem proteção"]);
    expect(host!.textContent).not.toContain("Restrição não aplicada");
    // The events the store holds keep the restriction: the Events tab still lists it.
    expect(getState().events["exec-restrictions"]?.some((e) => e.type === "policy.unenforced")).toBe(true);

    act(() => patchStateForTests({ settings }));
    expect(alerts()).toEqual(["Restrição não aplicada", "Branch base sem proteção"]);
  });
});

// The policy_unenforced warning the daemon puts on a task, its validation, and its execution, apart from the
// policy.unenforced event the banner above is built from: the task's row and its editor show it whatever the setting,
// and the execution's Results tab only while the setting is on.
describe("the unenforced-restriction warning", () => {
  let root: Root | undefined;
  let host: HTMLElement | undefined;

  const unenforced = { code: "policy_unenforced" as const, message: "network: deny is not enforced for shell commands" };
  const restricted: TaskSummary = { ...task, id: "restricted", name: "Restricted task", running: false, warnings: [unenforced] };
  const document_ = {
    apiVersion: "harmflow/v1alpha1",
    kind: "Task",
    metadata: { id: restricted.id, name: restricted.name },
    spec: { type: "general", schedule: { cron: "0 9 * * 1-5" }, prompt: "Fetch the release notes." },
  };
  const restrictedTask: Task = {
    ...restricted,
    document: document_,
    effective: document_,
    yaml: JSON.stringify(document_, null, 2) + "\n",
    path: "restricted.yaml",
    validation: { valid: true, errors: [], warnings: [unenforced] },
  };

  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = undefined;
    daemon.answers.clear();
    daemon.requests.length = 0;
    daemon.bodies.length = 0;
    patchStateForTests({ settings, tasks: [task], tasksLoaded: true });
  });

  const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

  async function render(node: ReactNode) {
    act(() => root?.unmount());
    host?.remove();
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(node));
    await settle();
  }

  /** The tasks screen, its tasks loaded from the stand-in daemon; answers the restricted task's warnings chip. */
  async function showRow() {
    daemon.answers.set("/tasks", [restricted]);
    await act(() => actions.refreshTasks());
    await render(<TasksScreen view={{ kind: "list" }} onView={() => {}} onOpenExecution={() => {}} />);
    const chip = host!.querySelector<HTMLElement>(`[data-task="${restricted.id}"] .chip[title]`);
    return chip && { text: chip.textContent, title: chip.title };
  }

  /** The editor of the restricted task, as the stand-in daemon serves it; answers the warnings its side panel lists. */
  async function showEditor() {
    daemon.answers.set(`/tasks/${restricted.id}`, restrictedTask);
    await render(<TasksScreen view={{ kind: "edit", id: restricted.id }} onView={() => {}} onOpenExecution={() => {}} />);
    return [...host!.querySelectorAll(".editor__side .validation")]
      .filter((panel) => panel.querySelector("h3")?.textContent === "Avisos")
      .flatMap((panel) => [...panel.querySelectorAll(".validation__item--warning")].map((li) => li.textContent));
  }

  /** The detail of an execution that holds the warning and no policy.unenforced event, on its Results tab; answers the
   * warnings listed under its heading. */
  async function showResults() {
    const exec: Execution = { ...detail, id: "exec-unenforced-warning", warnings: [unenforced] };
    daemon.answers.set(`/executions/${exec.id}`, exec);
    daemon.answers.set(`/executions/${exec.id}/events`, {
      events: [ev("execution.started", { executionId: exec.id, message: "Loading configuration" })],
      logExpired: false,
    });
    await render(<ExecutionDetail executionId={exec.id} />);
    const tab = [...host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((b) => b.textContent?.startsWith("Resultados"))!;
    await act(async () => tab.click());
    await settle();
    expect(host!.querySelector(".detail__alerts")?.textContent ?? "").not.toContain("Restrição não aplicada");
    return resultsWarnings();
  }

  const resultsWarnings = () =>
    [...host!.querySelectorAll(".results__block")]
      .filter((block) => block.querySelector("h4")?.textContent === "Avisos")
      .flatMap((block) => [...block.querySelectorAll(".banner__text")].map((b) => b.textContent));

  it("shows on the task's row a chip that counts it and names it with its message", async () => {
    expect(await showRow()).toEqual({ text: "1 aviso", title: "Restrição não aplicada: network: deny is not enforced for shell commands" });
    expect(daemon.requests).toContain("GET /tasks");
  });

  it("lists it, with its message, among the warnings of the editor's validation", async () => {
    expect(await showEditor()).toEqual(["Restrição não aplicada: network: deny is not enforced for shell commands"]);
    expect(daemon.requests).toContain(`GET /tasks/${restricted.id}`);
  });

  it("shows it on the execution's Results tab under its warnings heading, from the warning and not the event", async () => {
    expect(await showResults()).toEqual(["Restrição não aplicada — network: deny is not enforced for shell commands"]);
  });

  it("leaves the execution's Results tab while the setting is off, and stays on the task's row and in its editor", async () => {
    expect(await showResults()).toHaveLength(1);
    act(() => patchStateForTests({ settings: { ...settings, showRestrictionWarnings: false } }));
    expect(resultsWarnings()).toEqual([]);
    expect(host!.textContent).not.toContain("network: deny is not enforced for shell commands");

    expect(await showRow()).toEqual({ text: "1 aviso", title: "Restrição não aplicada: network: deny is not enforced for shell commands" });
    expect(await showEditor()).toEqual(["Restrição não aplicada: network: deny is not enforced for shell commands"]);
  });
});
