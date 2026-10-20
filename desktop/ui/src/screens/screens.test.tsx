// Renders each screen from a known state, to catch rendering errors without a browser or a daemon.
import { renderToString } from "react-dom/server";
import { beforeAll, describe, expect, it } from "vitest";
import type { Execution, ExecutionSummary, HarmflowEvent, Harness, Settings, TaskSummary } from "../api/types";
import { TaskForm } from "../components/TaskForm";
import i18n from "../i18n";
import { newTaskDocument } from "../lib/taskForm";
import { patchStateForTests } from "../state/store";
import { ExecutionsScreen } from "./ExecutionsScreen";
import { HistoryScreen } from "./HistoryScreen";
import { OnboardingScreen } from "./OnboardingScreen";
import { OverviewScreen } from "./OverviewScreen";
import { SettingsScreen } from "./SettingsScreen";
import { TasksScreen } from "./TasksScreen";

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
  attempts: [{ number: 1, harness: "claude-code", model: "default", effort: "high", startedAt: running.startedAt!, outcome: "running" }],
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
      workItems: [],
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
    expect(html).toContain("Fila de execução (2)");
    expect(html).not.toMatch(/\d+%/); // no invented progress percentage (FS-008 FR-009)
  });

  it("renders the other screens", () => {
    expect(renderToString(<OverviewScreen onOpenExecution={() => {}} onNewTask={() => {}} onOpenTask={() => {}} />)).toContain("Daemon");
    expect(renderToString(<TasksScreen view={{ kind: "list" }} onView={() => {}} onOpenExecution={() => {}} />)).toContain("Implement GitHub issues");
    expect(renderToString(<HistoryScreen openId={null} onOpen={() => {}} />)).toContain("Estado");
    expect(renderToString(<SettingsScreen />)).toContain("Agentes instalados");
    expect(renderToString(<OnboardingScreen onFinish={() => {}} onCreateTask={() => {}} onImport={() => {}} />)).toContain("Boas-vindas");
  });

  it("asks for repositories only for the GitHub type (FS-001 FR-008)", () => {
    const form = (doc: Record<string, unknown>) =>
      renderToString(<TaskForm doc={doc} onChange={() => {}} mode="create" harnesses={[harness]} errorPaths={new Set()} />);
    expect(form(newTaskDocument("github-issue-implementation", undefined, "A"))).toContain("Repositórios");
    expect(form(newTaskDocument("general", undefined, "B"))).not.toContain("Repositórios");
  });
});
