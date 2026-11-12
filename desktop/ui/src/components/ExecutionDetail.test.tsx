// @vitest-environment happy-dom
// Drives the execution detail in a DOM, against a stand-in for the daemon's local API: every view the specification
// asks for, and the raw output read from the daemon's route, again while the execution runs. The tests of a single
// tab's panel sit beside that panel, in execution/.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { StreamHandlers } from "../api/transport";
import type { Execution, ExecutionOutput, HarmflowEvent } from "../api/types";
import i18n from "../i18n";
import { getState, patchStateForTests, startSync } from "../state/store";
import { ExecutionDetail } from "./ExecutionDetail";

// The daemon as the client sees it: an answer per route, every request it received, and the stream it feeds.
const daemon = vi.hoisted(() => ({
  answers: new Map<string, unknown>(),
  requests: [] as string[],
  stream: undefined as StreamHandlers | undefined,
}));
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
    stream: (handlers: StreamHandlers) => {
      daemon.stream = handlers;
      return () => {};
    },
  },
}));

const started = "2026-10-21T09:00:00Z";
const ended = "2026-10-21T09:20:00Z";

const base: Execution = {
  id: "exec-done",
  taskId: "core-issues",
  taskName: "Implement GitHub issues",
  taskType: "github",
  status: "running",
  trigger: "manual",
  createdAt: started,
  startedAt: started,
  harness: "codex",
  repository: "org/core-banking",
  workItem: "org/core-banking#123",
  // Claude Code reports its model; Codex reports none, and neither its effort.
  attempts: [
    { number: 1, harness: "claude-code", model: "claude-sonnet-4-5", configuredModel: "default", configuredEffort: "high", startedAt: started, endedAt: started, outcome: "quota_exhausted" },
    { number: 2, harness: "codex", configuredModel: "default", configuredEffort: "high", startedAt: started, endedAt: ended, outcome: "succeeded" },
  ],
  workItems: [
    {
      key: "org/core-banking#123",
      issueNumber: 123,
      branch: "feature/123-agent",
      tests: { status: "passed" },
      pullRequest: { number: 9, url: "https://github.com/org/core-banking/pull/9", draft: true },
      commits: ["a1b2c3d4e5f6"],
    },
  ],
  warnings: [],
  changedFiles: ["src/auth/token.ts"],
  commands: [{ id: "c1", command: "./gradlew test", status: "succeeded", exitCode: 0, startedAt: started, endedAt: ended }],
};

const completed: Execution = { ...base, status: "completed", endedAt: ended, pullRequestUrl: "https://github.com/org/core-banking/pull/9" };

const event = (id: number, type: string, extra: Partial<HarmflowEvent> = {}): HarmflowEvent => ({
  id,
  schemaVersion: "1",
  timestamp: started,
  executionId: completed.id,
  type,
  severity: "info",
  ...extra,
});

const capture = (...texts: string[]): ExecutionOutput => ({
  logExpired: false,
  attempts: texts.map((text, i) => ({ attempt: i + 1, harness: i === 0 ? "claude-code" : "codex", size: text.length, offset: 0, text })),
});

let root: Root | undefined;
let host: HTMLElement | undefined;

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
  daemon.answers.clear();
  daemon.requests.length = 0;
  vi.useRealTimers();
});

/** Lets the stand-in daemon's answers arrive and React render them; setImmediate is never faked here. */
const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

async function open(x: Execution, events: HarmflowEvent[], output: ExecutionOutput, logExpired = false) {
  daemon.answers.set(`/executions/${x.id}`, x);
  daemon.answers.set(`/executions/${x.id}/events`, { events, logExpired });
  daemon.answers.set(`/executions/${x.id}/output`, output);
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () => root!.render(<ExecutionDetail executionId={x.id} summary={x} />));
  await settle();
}

const body = () => host!.querySelector(".detail__body")!.textContent ?? "";
const selectedTab = () => host!.querySelector('[role="tab"][aria-selected="true"]')?.textContent ?? "";

async function choose(label: string): Promise<string> {
  const tab = [...host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((b) => b.textContent?.startsWith(label));
  expect(tab, label).toBeDefined();
  await act(async () => tab!.click());
  await settle();
  return body();
}

describe("execution detail", () => {
  it("offers every view the specification names, the raw output as the daemon redacted it", async () => {
    // The daemon redacts the capture before serving it; itest › TestRawOutputRedacted checks that side.
    await open(
      completed,
      [
        event(1, "execution.started", { message: "Loading configuration" }),
        event(2, "tool.started", { payload: { toolUseId: "t1", tool: "Bash", input: { command: "./gradlew test" } } }),
        event(3, "execution.completed", { message: "Opened a draft pull request" }),
      ],
      capture("Claude AI usage limit reached\n", "git push with [REDACTED]\n"),
    );

    const tabs = [...host!.querySelectorAll('[role="tab"]')].map((t) => t.textContent);
    expect(tabs).toEqual([
      "Chat(0)",
      expect.stringMatching(/^Events/),
      "Raw output",
      "Changed files(1)",
      "Commands(1)",
      "Results",
      "Attempts(2)",
      "Costs",
    ]);

    expect(await choose("Events")).toContain("./gradlew test");
    expect(host!.querySelectorAll(".tl-item").length).toBeGreaterThanOrEqual(3);

    const raw = await choose("Raw output");
    expect(raw).toContain("Attempt 1 · Claude Code");
    expect(raw).toContain("Claude AI usage limit reached");
    expect(raw).toContain("Attempt 2 · Codex");
    expect(raw).toContain("git push with [REDACTED]");
    expect(daemon.requests).toContain(`GET /executions/${completed.id}/output`);

    expect(await choose("Changed files")).toContain("src/auth/token.ts");
    expect(await choose("Commands")).toContain("./gradlew test");

    const results = await choose("Results");
    for (const shown of ["feature/123-agent", "Tests passed", "#9", "a1b2c3d"]) expect(results).toContain(shown);

    const attempts = await choose("Attempts");
    for (const shown of ["Attempt 1", "Claude Code", "Attempt 2", "Codex"]) expect(attempts).toContain(shown);
  });

  it("shows a command the harness declined apart from a failed one", async () => {
    const declined: Execution = {
      ...completed,
      commands: [{ id: "c2", command: "python3 script.py", status: "declined", startedAt: started, endedAt: ended }],
    };
    await open(declined, [], capture());
    expect(await choose("Commands")).toContain("Declined");
    const chip = host!.querySelector(".table .chip");
    expect(chip?.className).toContain("chip--warning");
    expect(chip?.textContent).not.toContain("Failed");
  });

  it("shows a call a later end corrects to declined as one declined line, with the first end's output", async () => {
    // Claude Code ends a refused call as failed while the session runs, and says it declined it at the session's end.
    const refused = "cd /tmp && ls missing 2>&1";
    const corrected: Execution = { ...completed, id: "exec-corrected" };
    const on = { executionId: corrected.id };
    await open(
      corrected,
      [
        event(1, "execution.started", { ...on, message: "Loading configuration" }),
        event(2, "tool.started", { ...on, message: refused, payload: { toolId: "t9", tool: "Bash", command: refused } }),
        event(3, "tool.completed", {
          ...on,
          severity: "warning",
          message: "Bash failed",
          messageKey: "tool_failed",
          params: { tool: "Bash" },
          payload: { toolId: "t9", tool: "Bash", status: "failed", output: "This command requires approval" },
        }),
        event(4, "tool.completed", {
          ...on,
          severity: "warning",
          message: "Bash was declined",
          messageKey: "tool_declined",
          params: { tool: "Bash" },
          payload: { toolId: "t9", tool: "Bash", status: "declined", output: "" },
        }),
      ],
      capture(),
    );
    await choose("Events");

    const rows = [...host!.querySelectorAll<HTMLElement>('[role="option"]')];
    expect(rows).toHaveLength(2);
    const call = rows.find((r) => r.textContent?.includes(refused))!;
    expect(call).toBeDefined();
    expect(call.querySelector(".tl-item__state .tone-warning")).not.toBeNull();
    expect(call.querySelector(".tl-item__state .tone-danger")).toBeNull();

    await act(async () => call.click());
    await settle();
    expect(host!.querySelector(".event-details .codebox pre")?.textContent).toBe("This command requires approval");
  });

  it("names the model and effort each attempt's harness reported, and the rest as not reported", async () => {
    await open(completed, [], capture());
    await choose("Attempts");
    const [claude, codex] = [...host!.querySelectorAll(".attempt")].map((li) => li.textContent ?? "");
    expect(claude).toContain("Modelclaude-sonnet-4-5 · configured: default");
    expect(claude).toContain("EffortNot reported · configured: high");
    expect(codex).toContain("ModelNot reported · configured: default");
    expect(codex).toContain("EffortNot reported · configured: high");
    // The configured value is never shown as the one used.
    expect(codex).not.toMatch(/Model(default|high)/);
  });

  it.each([
    ["en-US", "Attempts", "Network failure"],
    ["es-ES", "Intentos", "Fallo de red"],
    ["pt-BR", "Tentativas", "Falha de rede"],
  ])("labels an attempt that ended on a network failure as one in %s (harmflow-app#212)", async (lang, tab, label) => {
    await i18n.changeLanguage(lang);
    try {
      const offline: Execution = { ...base, id: "exec-offline", status: "failed", endedAt: ended, attempts: [{ ...base.attempts[0], outcome: "network_failed" }] };
      await open(offline, [], capture());
      await choose(tab);
      expect(host!.querySelector(".attempt__header")?.textContent).toContain(label);
    } finally {
      await i18n.changeLanguage("en-US");
    }
  });

  it("writes an attempt's start time and duration as the interface's language does, and again when it changes (harmflow-app#642)", async () => {
    // The start time is written in the machine's time zone: the test sets one, so it reads the same on any machine.
    vi.stubEnv("TZ", "UTC");
    try {
      await open(completed, [], capture());
      await choose("Attempts");
      // The second attempt ran from 09:00 to 09:20.
      const shown = (key: "executions.detail.started" | "executions.detail.duration") =>
        [...host!.querySelectorAll(".attempt")[1].querySelectorAll(".kv__row")]
          .find((row) => row.querySelector("dt")!.textContent === i18n.t(key))!
          .querySelector("dd")!.textContent;
      const read = () => ({ started: shown("executions.detail.started"), duration: shown("executions.detail.duration") });

      const english = read();
      expect(english).toEqual({ started: "Oct 21, 2026, 9:00 AM", duration: "20m" });

      await act(async () => void (await i18n.changeLanguage("pt-BR")));
      await settle();
      const portuguese = read();
      expect(portuguese).toEqual({ started: "21 de out. de 2026, 09:00", duration: "20 min" });

      expect(portuguese.started).not.toBe(english.started);
      expect(portuguese.duration).not.toBe(english.duration);
    } finally {
      vi.unstubAllEnvs();
      await act(async () => void (await i18n.changeLanguage("en-US")));
    }
  });

  it("shows a failover's switch events in its timeline, in order, each with its own tone", async () => {
    // A quota failover's events as the daemon emits them, in the order itest › TestFailoverAcrossHarnesses reads.
    const failedOver: Execution = { ...completed, id: "exec-failed-over" };
    const on = { executionId: failedOver.id };
    await open(
      failedOver,
      [
        event(1, "harness.started", {
          ...on,
          harness: "claude-code",
          attempt: 1,
          message: "Claude Code 2.1.239 started",
          messageKey: "harness_started",
          params: { harness: "Claude Code", version: "2.1.239" },
        }),
        event(2, "harness.quota_exhausted", {
          ...on,
          harness: "claude-code",
          attempt: 1,
          severity: "warning",
          message: "Claude Code exhausted its quota",
          messageKey: "harness_quota_exhausted",
          params: { harness: "Claude Code" },
        }),
        event(3, "harness.switched", {
          ...on,
          attempt: 1,
          severity: "warning",
          message: "switching from Claude Code to Codex",
          messageKey: "harness_switched",
          params: { from: "Claude Code", to: "Codex" },
        }),
        event(4, "harness.started", {
          ...on,
          harness: "codex",
          attempt: 2,
          message: "Codex 0.163.0 started",
          messageKey: "harness_started",
          params: { harness: "Codex", version: "0.163.0" },
        }),
      ],
      capture(),
    );

    await choose("Events");
    const items = [...host!.querySelectorAll<HTMLElement>(".tl-item")].map((b) => ({
      title: b.querySelector(".tl-item__title")?.textContent, // the event's label, and #n from the second attempt on
      detail: b.querySelector(".tl-item__detail")?.textContent,
      tone: /tl-item--([a-z]+)/.exec(b.className)?.[1],
    }));
    expect(items).toEqual([
      { title: "AI tool started", detail: "Claude Code 2.1.239 started", tone: "violet" },
      { title: "AI tool quota exhausted", detail: "Claude Code exhausted its quota", tone: "warning" },
      { title: "Switching AI tool", detail: "switching from Claude Code to Codex", tone: "magenta" },
      { title: "AI tool started#2", detail: "Codex 0.163.0 started", tone: "violet" },
    ]);
  });

  // The store's sync starts once per module, as the app starts it once: this is the one test of this file that starts it.
  it("shows a switch the stream delivers in the alerts and the timeline as it arrives, with no new read of the events", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    const attempt = { ...base.attempts[0], endedAt: undefined, outcome: "running" as const };
    const running: Execution = { ...base, id: "exec-switch-live", harness: "claude-code", attempts: [attempt] };
    const on = { executionId: running.id };
    startSync();
    await settle();
    await open(
      running,
      [
        event(1, "harness.started", {
          ...on,
          harness: "claude-code",
          attempt: 1,
          message: "Claude Code 2.1.239 started",
          messageKey: "harness_started",
          params: { harness: "Claude Code", version: "2.1.239" },
        }),
      ],
      capture(),
    );
    await choose("Events");

    const eventReads = () => daemon.requests.filter((r) => r.startsWith(`GET /executions/${running.id}/events`)).length;
    const recordReads = () => daemon.requests.filter((r) => r === `GET /executions/${running.id}`).length;
    const opened = eventReads();
    expect(opened).toBe(1);
    const alerts = () => [...host!.querySelectorAll(".detail__alerts > *")].map((b) => b.textContent ?? "");
    // The events' items; a running execution's timeline ends on a pending item, which is none.
    const titles = () => [...host!.querySelectorAll(".tl-item:not(.tl-item--pending) .tl-item__title")].map((t) => t.textContent);
    expect(alerts()).toEqual([]);

    // Each event reaches the store as a streamed one does, through the handlers startSync gave the transport.
    const deliver = (e: HarmflowEvent) => act(async () => daemon.stream!.onEvent(e));
    await deliver(
      event(2, "harness.quota_exhausted", {
        ...on,
        harness: "claude-code",
        attempt: 1,
        severity: "warning",
        message: "Claude Code exhausted its quota",
        messageKey: "harness_quota_exhausted",
        params: { harness: "Claude Code" },
      }),
    );
    await deliver(
      event(3, "harness.switched", {
        ...on,
        attempt: 1,
        severity: "warning",
        message: "switching from Claude Code to Codex",
        messageKey: "harness_switched",
        params: { from: "Claude Code", to: "Codex" },
      }),
    );

    // Before Codex starts: the switch's banner, and its item in the timeline.
    expect(alerts()).toEqual([expect.stringContaining("switching from Claude Code to Codex")]);
    expect(titles()).toEqual(["AI tool started", "AI tool quota exhausted", "Switching AI tool"]);

    await deliver(
      event(4, "harness.started", {
        ...on,
        harness: "codex",
        attempt: 2,
        message: "Codex 0.163.0 started",
        messageKey: "harness_started",
        params: { harness: "Codex", version: "0.163.0" },
      }),
    );
    expect(titles()).toEqual(["AI tool started", "AI tool quota exhausted", "Switching AI tool", "AI tool started#2"]);
    expect(alerts()).toEqual([expect.stringContaining("switching from Claude Code to Codex")]);

    // The reads the store schedules on these events run once their debounce ends: the record, never the events.
    const records = recordReads();
    await act(() => vi.advanceTimersByTimeAsync(1000));
    await settle();
    expect(recordReads()).toBeGreaterThan(records);
    expect(eventReads()).toBe(opened);
  });

  it("shows the daemon's texts in the interface's language, and the agent's and harness's as they are", async () => {
    await i18n.changeLanguage("pt-BR");
    try {
      // It failed before any result: the Results tab still shows why.
      const failed: Execution = {
        ...completed,
        id: "exec-failed",
        status: "failed",
        workItems: [],
        pullRequestUrl: undefined,
        errorMessage: "Codex cannot continue (quota exhausted) and no alternative harness is available: You've hit your usage limit",
        errorKey: "no_alternative_harness_detail",
        errorParams: { harness: "Codex", reason: "quota exhausted", errorClass: "quota_exhausted", detail: "You've hit your usage limit" },
        attempts: [
          {
            ...base.attempts[0],
            reason: "first attempt",
            reasonKey: "attempt_first",
            errorMessage: "the execution timeout elapsed",
            errorKey: "execution_timed_out",
          },
          {
            ...base.attempts[1],
            outcome: "quota_exhausted",
            reason: "failover after quota exhausted",
            reasonKey: "attempt_failover",
            reasonParams: { reason: "quota exhausted", errorClass: "quota_exhausted" },
            errorMessage: "You've hit your usage limit",
          },
        ],
      };
      await open(
        failed,
        [
          event(1, "harness.switched", {
            message: "switching from Claude Code to Codex",
            messageKey: "harness_switched",
            params: { from: "Claude Code", to: "Codex" },
          }),
          event(2, "harness.message", { message: "I could not finish the change." }),
          event(3, "execution.failed", { message: failed.errorMessage, messageKey: failed.errorKey, params: failed.errorParams }),
        ],
        capture("You've hit your usage limit\n"),
      );

      const alerts = host!.querySelector(".detail__alerts")!.textContent;
      expect(alerts).toContain("trocando de Claude Code para Codex");
      const timeline = await choose("Eventos");
      expect(timeline).toContain("trocando de Claude Code para Codex");
      expect(timeline).toContain("I could not finish the change.");
      expect(timeline).toContain(
        "Codex não pode continuar (cota esgotada) e nenhuma ferramenta de IA alternativa está disponível: You've hit your usage limit",
      );

      expect(await choose("Resultados")).toContain(
        "Codex não pode continuar (cota esgotada) e nenhuma ferramenta de IA alternativa está disponível: You've hit your usage limit",
      );
      const attempts = await choose("Tentativas");
      for (const shown of [
        "primeira tentativa",
        "o tempo limite da execução se esgotou",
        "troca de ferramenta de IA após cota esgotada",
        "You've hit your usage limit",
      ]) {
        expect(attempts).toContain(shown);
      }
      expect(attempts).not.toContain("first attempt");
    } finally {
      await i18n.changeLanguage("en-US");
    }
  });

  it("shows a conflict the agent met as a warning above the timeline, in it, and in the results", async () => {
    const said = "someone pushed to feature/123-agent; rebased and continued";
    await open(
      { ...completed, id: "exec-conflict", warnings: [{ code: "conflicting_activity", message: said, subject: "org/core-banking" }] },
      [
        event(1, "execution.started"),
        event(2, "work_item.conflicting_activity", {
          severity: "warning",
          message: said,
          payload: { code: "conflicting_activity", subject: "org/core-banking" },
        }),
        event(3, "execution.completed"),
      ],
      capture(""),
    );

    expect(host!.querySelector(".detail__alerts")!.textContent).toBe(`Conflicting activity — ${said}`);
    expect(await choose("Events")).toContain("Conflicting activity");
    expect(await choose("Results")).toContain(`Conflicting activity — ${said}`);
  });

  it("shows an unprotected base branch as a warning above the timeline, in it, and in the results", async () => {
    const said = "main requires a pull request but no approving review; merges without review are possible";
    await open(
      { ...completed, id: "exec-unprotected", warnings: [{ code: "repository_unprotected", message: said, subject: "org/core-banking" }] },
      [
        event(1, "execution.started"),
        event(2, "repository.unprotected", { severity: "warning", message: said, payload: { repository: "org/core-banking" } }),
        event(3, "execution.completed"),
      ],
      capture(""),
    );

    expect(host!.querySelector(".detail__alerts")!.textContent).toBe(`Base branch not protected — ${said}`);
    const timeline = await choose("Events");
    expect(timeline).toContain("Base branch not protected");
    expect(timeline).toContain(said);
    expect(await choose("Results")).toContain(`Base branch not protected — ${said}`);
  });

  // A draft opened with failing tests ends the execution completed, its item resumable (app ›
  // TestDraftWithFailingTestsCompletesTheExecution): its results read as a completed execution's, never as a failure.
  it("shows a draft opened with failing tests as its work item's Tests failed and pull request, with no error banner", async () => {
    const pullRequestUrl = "https://github.com/org/core-banking/pull/9";
    const draft: Execution = {
      ...completed,
      id: "exec-draft-failing",
      testsStatus: "failed",
      workItems: [{ ...base.workItems[0], tests: { status: "failed" } }],
    };
    const on = { executionId: draft.id, workItemId: "org/core-banking#123" };
    await open(
      draft,
      [
        event(1, "execution.started", { executionId: draft.id }),
        event(2, "pull_request.created", { ...on, message: pullRequestUrl, payload: { url: pullRequestUrl, number: 9, branch: "feature/123-agent" } }),
        event(3, "test.failed", {
          ...on,
          severity: "warning",
          message: "tests failed; the draft pull request documents them",
          messageKey: "tests_failed",
          payload: { url: pullRequestUrl },
        }),
        event(4, "execution.completed", { executionId: draft.id }),
      ],
      capture(""),
    );

    expect(host!.querySelector(".detail__title-row .chip")!.textContent).toBe("Completed");
    await choose("Results");
    const items = [...host!.querySelectorAll(".results__item")];
    expect(items).toHaveLength(1);
    const row = (label: string) =>
      [...items[0].querySelectorAll(".kv__row")].find((r) => r.querySelector("dt")!.textContent === i18n.t(label as never))!.querySelector("dd")!;
    const tests = row("executions.detail.tests").querySelector(".chip")!;
    expect(tests.textContent).toBe("Tests failed");
    expect(tests.className).toContain("chip--danger");
    expect(row("executions.detail.pullRequest").textContent!.trim()).toBe("#9");
    expect(host!.querySelectorAll(".banner--danger")).toHaveLength(0);
  });

  // harmflow-app#477: the warning the daemon records when Codex says its managed requirements override a setting
  // HarmFlow runs it with: code other, no message key, Codex's notice as its text (app › TestSettingOverriddenIsAWarning).
  // The notice is the approval_policy line of the fixture codex › TestParseRequirementsOverride replays.
  describe("a harness's setting-override notice", () => {
    const fixture = join(__dirname, "..", "..", "..", "..", "daemon", "internal", "adapters", "harness", "codex", "testdata", "requirements-override.jsonl");
    const notice = readFileSync(fixture, "utf8")
      .split("\n")
      .filter((line) => line.includes("`approval_policy`"))
      .map((line) => (JSON.parse(line) as { item: { message: string } }).item.message)[0];
    const overridden: Execution = { ...completed, id: "exec-overridden", warnings: [{ code: "other", message: notice }] };
    const banners = () => [...host!.querySelectorAll<HTMLElement>(".detail__body .banner")];

    it("shows Codex's notice verbatim in the results, as one warning", async () => {
      expect(notice).toMatch(/^Configured value for `approval_policy` is disallowed by requirements;/);
      await open(overridden, [], capture(""));
      await choose("Results");
      const shown = banners();
      expect(shown).toHaveLength(1);
      expect(shown[0].className).toContain("banner--warning");
      expect(shown[0].querySelector("strong")?.textContent).toBe("Warning");
      expect(shown[0].textContent).toBe(`Warning — ${notice}`);
    });

    it("keeps Codex's words as Codex wrote them in pt-BR, under the pt-BR label", async () => {
      await i18n.changeLanguage("pt-BR");
      try {
        await open(overridden, [], capture(""));
        await choose("Resultados");
        const shown = banners();
        expect(shown).toHaveLength(1);
        expect(shown[0].className).toContain("banner--warning");
        expect(shown[0].querySelector("strong")?.textContent).toBe("Aviso");
        expect(shown[0].textContent).toBe(`Aviso — ${notice}`);
      } finally {
        await i18n.changeLanguage("en-US");
      }
    });

    it("shows no warning in the results of an execution with none", async () => {
      await open(completed, [], capture(""));
      await choose("Results");
      expect(banners().filter((b) => b.className.includes("banner--warning"))).toEqual([]);
    });
  });

  // harmflow-app#39: a failover's attempts and their sum, each figure with its source, the estimate said as such, and
  // the itemized receipt inside the tab.
  it("shows each attempt's token use and estimated cost, their sum, and the receipt", async () => {
    const tokens = (input: number, cacheWriteLong: number, cacheRead: number, output: number) => ({ input, cacheWriteShort: 0, cacheWriteLong, cacheRead, output });
    const costly: Execution = {
      ...completed,
      id: "exec-costly",
      attempts: [
        {
          ...base.attempts[0],
          usage: { status: "partial", modelSource: "harness", models: [{ model: "claude-haiku-4-5-20251001", tokens: tokens(9, 11511, 14129, 0) }] },
        },
        {
          ...base.attempts[1],
          usage: {
            status: "reported",
            modelSource: "config",
            costSource: "estimated",
            costUsd: 0.0477,
            pricesAsOf: "2026-10-22",
            models: [{ model: "gpt-test", tokens: tokens(24355, 0, 135680, 1194), price: { input: 1, cacheWriteShort: 1, cacheWriteLong: 1, cacheRead: 0.1, output: 10 }, costUsd: 0.0477 }],
          },
        },
      ],
    };
    await open(costly, [], capture());
    const summary = await choose("Costs");
    for (const shown of [
      "Estimated cost",
      "$0.0477",
      "Claude Code → Codex",
      "partial · ended before the final count",
      "estimated · price table",
      "Quota exhausted",
      "Total",
      "24,355",
      "149,809", // the cache reads of both attempts
      "An estimate at API list prices of Oct 22, 2026",
      "not billed one by one",
      "Attempt 1 ended before Claude Code reported its final count",
      "Attempt 2 is priced as gpt-test, the model the Codex configuration sets.",
    ]) {
      expect(summary).toContain(shown);
    }

    const receiptButton = [...host!.querySelectorAll<HTMLButtonElement>(".detail__body button")].find((b) => b.textContent === "Receipt");
    await act(async () => receiptButton!.click());
    const receipt = body();
    for (const shown of ["HarmFlow · token estimate", "prices as of Oct 22, 2026", "Attempt 1", "Attempt 2", "Cache write (1 h)", "Cache read", "135,680", "$0.10/M", "$0.0136", "Total (estimate)", "+ unpriced"]) {
      expect(receipt).toContain(shown);
    }
    const back = [...host!.querySelectorAll<HTMLButtonElement>(".detail__body button")].find((b) => b.textContent === "← Summary");
    await act(async () => back!.click());
    expect(body()).toContain("By attempt");
  });

  // The daemon redacts the input before recording it; app › TestAttemptKeepsItsInput checks that side.
  it("shows the prompt and the session context each attempt sent, collapsed, and says when a record has none", async () => {
    const prompt = "Fix the issue, then push with [REDACTED].";
    const context = "This session is run by HarmFlow, a local scheduler.\nWorkspace: /workspaces/core-banking\n";
    const sent: Execution = {
      ...completed,
      id: "exec-input",
      // The first attempt comes from a record older than the input.
      attempts: [base.attempts[0], { ...base.attempts[1], prompt, sessionContext: context }],
    };
    await open(sent, [], capture());
    await choose("Attempts");
    const [older, newer] = [...host!.querySelectorAll(".attempt")];

    expect(older.querySelectorAll("details")).toHaveLength(0);
    expect([...older.querySelectorAll(".attempt-input--empty")].map((e) => e.textContent)).toEqual([
      "PromptNot recorded",
      "Session contextNot recorded",
    ]);

    const blocks = [...newer.querySelectorAll<HTMLDetailsElement>("details.attempt-input")];
    expect(blocks.map((d) => d.querySelector("summary")!.textContent)).toEqual(["Prompt", "Session context"]);
    expect(blocks.map((d) => d.open)).toEqual([false, false]);
    expect(blocks.map((d) => d.querySelector("pre")!.textContent)).toEqual([prompt, context]);

    const writeText = vi.fn(async (_text: string) => {});
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    try {
      for (const d of blocks) await act(async () => d.querySelector<HTMLButtonElement>('button[aria-label="Copy"]')!.click());
      expect(writeText.mock.calls.map(([text]) => text)).toEqual([prompt, context]);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("says when an execution's harness reported no usage, instead of showing zero", async () => {
    await open({ ...completed, id: "exec-unreported" }, [], capture());
    expect(await choose("Costs")).toContain("No token usage for this execution");
  });

  // A completed failover from Claude Code to Codex whose attempts both reported their usage.
  it("shows a failover's costs, the CLI's and the table's, each with its source, and their sum", async () => {
    const haiku = { input: 1, cacheWriteShort: 1.25, cacheWriteLong: 2, cacheRead: 0.1, output: 5 };
    const terra = { input: 2, cacheWriteShort: 2.5, cacheWriteLong: 5, cacheRead: 0.2, output: 12 };
    const failover: Execution = {
      ...completed,
      id: "exec-failover-costs",
      attempts: [
        {
          ...base.attempts[0],
          usage: {
            status: "reported",
            modelSource: "harness",
            costSource: "reported",
            costUsd: 0.0364015,
            pricesAsOf: "2026-10-22",
            models: [
              {
                model: "claude-haiku-4-5-20251001",
                tokens: { input: 25, cacheWriteShort: 0, cacheWriteLong: 12752, cacheRead: 65425, output: 866 },
                price: haiku,
                costUsd: 0.0364015,
              },
            ],
          },
        },
        {
          ...base.attempts[1],
          usage: {
            status: "reported",
            modelSource: "config",
            costSource: "estimated",
            costUsd: 0.090174,
            pricesAsOf: "2026-10-22",
            models: [
              {
                model: "gpt-5.6-terra",
                tokens: { input: 24355, cacheWriteShort: 0, cacheWriteLong: 0, cacheRead: 135680, output: 1194 },
                price: terra,
                costUsd: 0.090174,
              },
            ],
          },
        },
      ],
    };
    await open(failover, [], capture());
    const summary = await choose("Costs");
    const rows = [...host!.querySelectorAll(".costs-table tbody tr")].map((r) => r.textContent ?? "");
    // Each attempt: its harness, its model, its tokens, its cost, and the source of that cost.
    for (const shown of ["Claude Code", "claude-haiku-4-5-20251001", "12,777", "65,425", "866", "$0.0364", "reported by Claude Code"]) {
      expect(rows[0]).toContain(shown);
    }
    for (const shown of ["Codex", "gpt-5.6-terra", "24,355", "135,680", "1,194", "$0.0902", "estimated · price table"]) {
      expect(rows[1]).toContain(shown);
    }
    // The total is their sum, $0.0364015 + $0.090174, with no part left out.
    const total = host!.querySelector(".costs-table__total")!.textContent ?? "";
    expect(total).toContain("$0.1266");
    expect(total).not.toContain("+ ?");
    expect(host!.querySelector(".costs-tile--hero .costs-tile__value")!.textContent).toBe("$0.1266");
    expect(summary).toContain("An estimate at API list prices of Oct 22, 2026");

    // The token mix's measure is a single choice, read as a radio group as every other one is: by cost while priced.
    const measure = host!.querySelector<HTMLElement>('[role="radiogroup"][aria-label="Measure"]')!;
    const option = (label: string) => [...measure.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find((b) => b.textContent === label)!;
    expect(measure.classList.contains("segmented")).toBe(true);
    expect(["by cost", "by tokens"].map((l) => option(l).getAttribute("aria-checked"))).toEqual(["true", "false"]);
    await act(async () => option("by tokens").click());
    expect(["by cost", "by tokens"].map((l) => option(l).getAttribute("aria-checked"))).toEqual(["false", "true"]);

    // The receipt itemizes each attempt's tokens by class, at the price that made its amount.
    const receiptButton = [...host!.querySelectorAll<HTMLButtonElement>(".detail__body button")].find((b) => b.textContent === "Receipt");
    await act(async () => receiptButton!.click());
    const sections = [...host!.querySelectorAll(".costs-receipt__section")].map((s) => s.textContent ?? "");
    for (const line of ["Input25×$1.00/M=< $0.0001", "Cache write (1 h)12,752×$2.00/M=$0.0255", "Cache read65,425×$0.10/M=$0.0065", "Output866×$5.00/M=$0.0043"]) {
      expect(sections[0]).toContain(line);
    }
    expect(sections[0]).toContain("matches the $0.0364 Claude Code reported");
    for (const line of ["Input24,355×$2.00/M=$0.0487", "Cache read135,680×$0.20/M=$0.0271", "Output1,194×$12.00/M=$0.0143"]) {
      expect(sections[1]).toContain(line);
    }
    const receiptTotal = host!.querySelector(".costs-receipt__total")!.textContent ?? "";
    expect(receiptTotal).toContain("Total (estimate)");
    expect(receiptTotal).toContain("$0.1266");
    expect(receiptTotal).not.toContain("unpriced");
  });

  // While an attempt runs, its tokens so far and no cost; the cost the attempt ended with, once it ends.
  it("shows a running attempt's tokens so far without a cost, and its cost once it ends", async () => {
    const tokens = (input: number, cacheWriteLong: number, cacheRead: number, output: number) => ({ input, cacheWriteShort: 0, cacheWriteLong, cacheRead, output });
    const attempt = { ...base.attempts[0], outcome: "running" as const, endedAt: undefined };
    const running: Execution = {
      ...base,
      id: "exec-running-costs",
      harness: "claude-code",
      attempts: [{ ...attempt, usage: { status: "partial", modelSource: "harness", models: [{ model: "claude-haiku-4-5-20251001", tokens: tokens(9, 10857, 14129, 0) }] } }],
    };
    await open(running, [], capture());
    const first = await choose("Costs");
    const hero = () => host!.querySelector(".costs-tile--hero")!.textContent ?? "";
    const row = () => host!.querySelector(".costs-table tbody tr")!.textContent ?? "";
    expect(hero()).toContain("Estimated cost so far");
    expect(host!.querySelector(".costs-tile--hero .costs-tile__value")!.textContent).toBe("—");
    for (const shown of ["10,866", "14,129", "so far · priced when it ends"]) expect(row()).toContain(shown);
    expect(row()).not.toContain("$");
    expect(first).toContain("the cost appears when the attempt ends");
    // With no cost yet, the measure is by tokens, and by cost cannot be chosen.
    const option = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>('[aria-label="Measure"] [role="radio"]')].find((b) => b.textContent === label)!;
    expect(option("by tokens").getAttribute("aria-checked")).toBe("true");
    expect(option("by cost").disabled).toBe(true);

    // A later message adds to the count; still no cost.
    const grown = { ...running, attempts: [{ ...attempt, usage: { status: "partial" as const, modelSource: "harness" as const, models: [{ model: "claude-haiku-4-5-20251001", tokens: tokens(25, 12752, 65425, 0) }] } }] };
    await act(async () => patchStateForTests({ details: { ...getState().details, [running.id]: grown } }));
    for (const shown of ["12,777", "65,425"]) expect(row()).toContain(shown);
    expect(row()).not.toContain("$");

    // The attempt ends: the cost stamped on it appears, from its source.
    const finished: Execution = {
      ...completed,
      id: running.id,
      harness: "claude-code",
      attempts: [
        {
          ...base.attempts[0],
          outcome: "succeeded",
          endedAt: ended,
          usage: {
            status: "reported",
            modelSource: "harness",
            costSource: "reported",
            costUsd: 0.0364015,
            pricesAsOf: "2026-10-22",
            models: [{ model: "claude-haiku-4-5-20251001", tokens: tokens(25, 12752, 65425, 866), costUsd: 0.0364015 }],
          },
        },
      ],
    };
    await act(async () => patchStateForTests({ details: { ...getState().details, [running.id]: finished } }));
    expect(hero()).not.toContain("so far");
    expect(host!.querySelector(".costs-tile--hero .costs-tile__value")!.textContent).toBe("$0.0364");
    for (const shown of ["866", "$0.0364", "reported by Claude Code"]) expect(row()).toContain(shown);
  });

  it("reads the raw output from the daemon's route again every 2 s while the execution runs", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    const live: Execution = { ...base, id: "exec-live" };
    const route = `GET /executions/${live.id}/output`;
    const reads = () => daemon.requests.filter((r) => r === route).length;

    await open(live, [], capture("first line\n"));
    expect(reads()).toBe(0); // read only once the tab is open
    expect(await choose("Raw output")).toContain("first line");
    expect(reads()).toBe(1);

    daemon.answers.set(`/executions/${live.id}/output`, capture("first line\nsecond line\n"));
    await act(() => vi.advanceTimersByTimeAsync(2000));
    await settle();
    expect(reads()).toBe(2);
    expect(body()).toContain("second line");

    // The execution ends: one last read, then no more.
    await act(async () => patchStateForTests({ details: { ...getState().details, [live.id]: { ...live, status: "completed", endedAt: ended } } }));
    await settle();
    expect(reads()).toBe(3);
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(reads()).toBe(3);
  });
});

// The detail offers Cancel on a running or queued execution, and nothing that writes to the
// session; across every tab, the one request that is not a read is that cancel.
describe("observe and cancel, no conversation", () => {
  // Toggles that change only the view, such as the timeline's follow checkbox, are not text boxes.
  const editable = () =>
    [...host!.querySelectorAll<HTMLElement>("input, textarea, select, [contenteditable], [role='textbox']")]
      .filter((el) => !(el instanceof HTMLInputElement && (el.type === "checkbox" || el.type === "radio")))
      .filter((el) => el.getAttribute("contenteditable") !== "false")
      .map((el) => el.outerHTML);

  /** Opens every tab in turn, checking that none renders a text box or an editable region. */
  async function everyTab() {
    const tabs = [...host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')];
    expect(tabs.length).toBeGreaterThan(1);
    for (const tab of tabs) {
      await act(async () => tab.click());
      await settle();
      expect(editable(), tab.textContent ?? "").toEqual([]);
    }
  }

  const cancelButton = () =>
    [...host!.querySelectorAll<HTMLButtonElement>(".detail__header button")].find((b) => b.textContent === "Cancel");
  const writes = () => daemon.requests.filter((r) => !r.startsWith("GET "));

  const running: Execution = { ...base, id: "exec-cancel-running" };
  const queued: Execution = { ...base, id: "exec-cancel-queued", status: "queued", startedAt: undefined, attempts: [], commands: [] };

  it.each([
    { status: "running", x: running },
    { status: "queued", x: queued },
  ])("offers Cancel on a $status execution and sends one cancel once confirmed", async ({ x }) => {
    // Outside the Tauri shell, the confirmation is the browser's.
    const confirm = vi.fn((_message?: string) => false);
    vi.stubGlobal("confirm", confirm);
    try {
      await open(x, [event(1, "execution.created", { executionId: x.id })], capture("working\n"));
      daemon.answers.set(`/executions/${x.id}/cancel`, { executionId: x.id });
      await everyTab();

      // Declined, it sends nothing.
      await act(async () => cancelButton()!.click());
      await settle();
      expect(writes()).toEqual([]);

      confirm.mockReturnValue(true);
      await act(async () => cancelButton()!.click());
      await settle();
      expect(confirm).toHaveBeenCalledTimes(2);
      expect(writes()).toEqual([`POST /executions/${x.id}/cancel`]);
      // Pending, the button takes no second click.
      const pending = [...host!.querySelectorAll<HTMLButtonElement>(".detail__header button")].find((b) => b.textContent === "Cancelling…");
      expect(pending?.disabled).toBe(true);

      await everyTab();
      expect(writes()).toEqual([`POST /executions/${x.id}/cancel`]);
      expect(daemon.requests.every((r) => r.startsWith("GET ") || r === `POST /executions/${x.id}/cancel`)).toBe(true);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it.each(["completed", "no_work", "failed", "cancelled", "interrupted", "needs_attention"] as const)(
    "shows no Cancel on a %s execution, and sends only reads",
    async (status) => {
      const id = `exec-ended-${status}`;
      await open({ ...completed, id, status }, [event(1, "execution.started", { executionId: id })], capture("done\n"));
      expect(cancelButton()).toBeUndefined();
      await everyTab();
      expect(cancelButton()).toBeUndefined();
      expect(writes()).toEqual([]);
    },
  );
});

describe("a script task's execution", () => {
  const script: Execution = {
    ...completed,
    id: "exec-script",
    taskId: "weekly-audit",
    taskName: "Weekly audit",
    taskType: "script",
    status: "failed",
    harness: "",
    repository: undefined,
    workItem: undefined,
    pullRequestUrl: undefined,
    errorMessage: "the program exited with code 3",
    errorKey: "script_exit_code",
    errorParams: { code: "3" },
    attempts: [{ number: 1, harness: "", configuredModel: "", configuredEffort: "", startedAt: started, endedAt: ended, outcome: "failed", exitCode: 3 }],
    workItems: [],
    changedFiles: [],
    commands: [],
    script: {
      interpreter: ["/bin/sh"],
      source: "echo audit\nexit 3",
      scriptPath: "/runs/exec-script/script.sh",
      program: "/bin/sh",
      workingDirectory: "/Users/me/jobs",
      environment: { TZ: "America/Sao_Paulo" },
      exitCode: 3,
    },
  };
  const lines = [
    event(1, "script.started", { executionId: script.id, message: "sh started", messageKey: "script_started", params: { program: "sh" } }),
    event(2, "script.output", { executionId: script.id, message: "audit", payload: { stream: "stdout" } }),
    event(3, "script.output", { executionId: script.id, message: "token [REDACTED]", payload: { stream: "stderr" } }),
    event(4, "script.exited", { executionId: script.id, severity: "warning", message: "the program exited with code 3", messageKey: "script_exit_code", params: { code: "3" } }),
  ];

  it("shows its output by stream and the program as it ran, and no harness, attempts, or costs", async () => {
    await open(script, lines, capture("audit\ntoken [REDACTED]\n"));
    const tabs = [...host!.querySelectorAll('[role="tab"]')].map((b) => b.textContent ?? "");
    for (const absent of ["Chat", "Attempts", "Costs", "Changed files", "Commands"]) {
      expect(tabs.some((t) => t.startsWith(absent)), absent).toBe(false);
    }
    expect(host!.querySelector(".detail__meta")!.textContent).toContain("Program");

    // The output lines stay out of the timeline, and the exit reads there.
    expect(body()).not.toContain("audit");
    expect(body()).toContain("the program exited with code 3");

    await choose("Output");
    const streams = [...host!.querySelectorAll<HTMLElement>(".script-line")].map((l) => [l.dataset.stream, l.textContent]);
    expect(streams).toEqual([
      ["stdout", "outaudit\n"],
      ["stderr", "errtoken [REDACTED]\n"],
    ]);

    const program = await choose("Program");
    for (const shown of ["code 3", "/bin/sh", "echo audit", "/Users/me/jobs", "TZ=America/Sao_Paulo", "/runs/exec-script/script.sh"]) {
      expect(program).toContain(shown);
    }
  });

  it("adds each output line of a running program as it arrives, with its stream, and no reload", async () => {
    const running: Execution = {
      ...script,
      id: "exec-script-live",
      status: "running",
      endedAt: undefined,
      errorMessage: undefined,
      errorKey: undefined,
      errorParams: undefined,
      attempts: [{ ...script.attempts[0], endedAt: undefined, outcome: "running", exitCode: undefined }],
      script: { ...script.script!, exitCode: undefined },
    };
    const on = { executionId: running.id };
    await open(running, [event(1, "script.started", { ...on, message: "sh started", messageKey: "script_started", params: { program: "sh" } })], capture());
    await choose("Output");
    const shown = () => [...host!.querySelectorAll<HTMLElement>(".script-line")].map((l) => [l.dataset.stream, l.textContent]);
    expect(shown()).toEqual([]);

    const reads = () => daemon.requests.filter((r) => r.startsWith(`GET /executions/${running.id}`)).length;
    const before = reads();
    // The stream hands each event to the store, which the view reads.
    const arrive = (e: HarmflowEvent) =>
      act(async () => patchStateForTests({ events: { ...getState().events, [running.id]: [...getState().events[running.id], e] } }));

    await arrive(event(2, "script.output", { ...on, message: "one", payload: { stream: "stdout" } }));
    expect(shown()).toEqual([["stdout", "outone\n"]]);
    await arrive(event(3, "script.output", { ...on, message: "token [REDACTED]", payload: { stream: "stderr" } }));
    expect(shown()).toEqual([
      ["stdout", "outone\n"],
      ["stderr", "errtoken [REDACTED]\n"],
    ]);
    await arrive(event(4, "script.output", { ...on, message: "two", payload: { stream: "stdout" } }));
    expect(shown()).toEqual([
      ["stdout", "outone\n"],
      ["stderr", "errtoken [REDACTED]\n"],
      ["stdout", "outtwo\n"],
    ]);
    expect(selectedTab()).toMatch(/^Output/);
    expect(reads()).toBe(before);
  });

  it("offers no chat view and opens on its events timeline", async () => {
    const x: Execution = { ...script, id: "exec-script-no-chat" };
    await open(x, lines, capture("audit\n"));
    const tabs = [...host!.querySelectorAll('[role="tab"]')].map((b) => b.textContent ?? "");
    expect(tabs.some((t) => t.startsWith("Chat"))).toBe(false);
    expect(selectedTab()).toMatch(/^Events/);
    expect(host!.querySelectorAll(".tl-item").length).toBeGreaterThan(0);
    expect(host!.querySelector(".chat")).toBeNull();
  });

  // ExecutionsScreen and HistoryScreen keep one detail mounted as the selection changes.
  it("opens another execution on its own first tab, Events for a script after a harness task's Chat", async () => {
    const prompted = (id: string): Execution => ({ ...completed, id, attempts: [{ ...base.attempts[0], prompt: "Implement org/core-banking#123." }] });
    const [first, second, other] = [prompted("exec-switch-first"), prompted("exec-switch-second"), { ...script, id: "exec-switch-script" }];
    for (const x of [second, other]) {
      daemon.answers.set(`/executions/${x.id}`, x);
      daemon.answers.set(`/executions/${x.id}/events`, { events: x === other ? lines : [], logExpired: false });
    }
    const show = async (x: Execution) => {
      await act(async () => root!.render(<ExecutionDetail executionId={x.id} summary={x} />));
      await settle();
    };

    await open(first, [], capture());
    expect(selectedTab()).toBe("Chat(0)");
    await show(other);
    expect(selectedTab()).toMatch(/^Events/);
    expect(body()).toContain("the program exited with code 3");

    // A tab chosen on one harness task does not carry over to the next.
    await show(first);
    await choose("Attempts");
    await show(second);
    expect(selectedTab()).toBe("Chat(0)");
    expect(body()).toContain("Implement org/core-banking#123.");
  });
});

describe("the detail header's glyph", () => {
  const glyph = () => {
    const span = host!.querySelector<HTMLElement>(".detail__header .exec-card__glyph")!;
    return { icon: span.querySelector("svg")!.dataset.icon, tones: [...span.classList].filter((c) => c.startsWith("type-glyph--")) };
  };
  const typed = (taskType: string): Execution => ({ ...completed, id: `exec-glyph-${taskType}`, taskType, workItem: undefined, workItems: [] });

  it("shows each task type's glyph and tone, the New task dialog's, never the settings gear", async () => {
    for (const [taskType, expected] of [
      ["github", { icon: "code", tones: ["type-glyph--blue"] }],
      ["general", { icon: "sparkle", tones: ["type-glyph--violet"] }],
      ["script", { icon: "terminal", tones: ["type-glyph--blue"] }],
      ["webhook", { icon: "dot", tones: ["type-glyph--blue"] }],
    ] as const) {
      await open(typed(taskType), [], capture());
      expect(glyph(), taskType).toEqual(expected);
      act(() => root!.unmount());
      host!.remove();
    }
    root = host = undefined;
  });
});
