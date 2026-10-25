// @vitest-environment happy-dom
// Drives the execution detail in a DOM, against a stand-in for the daemon's local API: every view the specification
// asks for, and the raw output read from the daemon's route, again while the execution runs.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { Execution, ExecutionOutput, HarmflowEvent } from "../api/types";
import i18n from "../i18n";
import { formatTime } from "../lib/format";
import { getState, patchStateForTests } from "../state/store";
import { ExecutionDetail } from "./ExecutionDetail";

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

const started = "2026-10-21T09:00:00Z";
const ended = "2026-10-21T09:20:00Z";

const base: Execution = {
  id: "exec-done",
  taskId: "core-issues",
  taskName: "Implement GitHub issues",
  taskType: "github-issue-implementation",
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
      { title: "Harness started", detail: "Claude Code 2.1.239 started", tone: "violet" },
      { title: "Harness quota exhausted", detail: "Claude Code exhausted its quota", tone: "warning" },
      { title: "Switching harness", detail: "switching from Claude Code to Codex", tone: "magenta" },
      { title: "Harness started#2", detail: "Codex 0.163.0 started", tone: "violet" },
    ]);
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
      expect(timeline).toContain("Codex não pode continuar (cota esgotada) e nenhum harness alternativo está disponível: You've hit your usage limit");

      expect(await choose("Resultados")).toContain(
        "Codex não pode continuar (cota esgotada) e nenhum harness alternativo está disponível: You've hit your usage limit",
      );
      const attempts = await choose("Tentativas");
      for (const shown of ["primeira tentativa", "o tempo limite da execução se esgotou", "troca de harness após cota esgotada", "You've hit your usage limit"]) {
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

// harmflow-app#190: the Events timeline is one tab stop, a list whose arrow keys, Home, and End move the selection.
describe("the events timeline from the keyboard", () => {
  const x: Execution = { ...completed, id: "exec-keys" };
  const said = ["Loading configuration", "Reading the issue.", "Running the tests.", "Opened a draft pull request"];
  const steps = [
    event(1, "execution.started", { executionId: x.id, message: said[0] }),
    event(2, "harness.message", { executionId: x.id, message: said[1] }),
    event(3, "harness.message", { executionId: x.id, message: said[2] }),
    event(4, "execution.completed", { executionId: x.id, message: said[3] }),
  ];

  const list = () => host!.querySelector<HTMLElement>('[role="listbox"]')!;
  const options = () => [...host!.querySelectorAll<HTMLElement>('[role="option"]')];
  /** The selected event as the list exposes it, and as the details pane shows it. */
  const selected = () => {
    const chosen = options().filter((o) => o.getAttribute("aria-selected") === "true");
    expect(chosen).toHaveLength(1);
    expect(list().getAttribute("aria-activedescendant")).toBe(chosen[0].id);
    return { row: chosen[0].querySelector(".tl-item__detail")!.textContent, details: host!.querySelector(".event-details__text .mono")!.textContent };
  };
  const at = (i: number) => ({ row: said[i], details: said[i] });
  const follow = () => host!.querySelector<HTMLInputElement>(".timeline__tools input[type='checkbox']")!;
  /** Presses a key where focus is, as the browser would send it; it says whether the default action was prevented. */
  const press = (key: string, on: HTMLElement = list(), init: KeyboardEventInit = {}) => {
    const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
    act(() => void on.dispatchEvent(e));
    return e.defaultPrevented;
  };

  // happy-dom lays nothing out, so the rows the list scrolls into view are counted instead.
  let scrolled: Element[];
  beforeEach(() => {
    scrolled = [];
    vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(function (this: Element) {
      scrolled.push(this);
    });
  });
  afterEach(() => vi.restoreAllMocks());

  it("names the list and exposes the selected event, the latest with none chosen", async () => {
    await open(x, steps, capture());
    await choose("Events");
    expect(list().tabIndex).toBe(0);
    expect(list().getAttribute("aria-label")).toBe("Event timeline");
    expect(options()).toHaveLength(4);
    expect(selected()).toEqual(at(3));
  });

  it("moves the selection with ↑ and ↓, without wrapping, and turns Follow off once it moves", async () => {
    await open(x, steps, capture());
    await choose("Events");
    expect(follow().checked).toBe(true);

    // At the last event ↓ does nothing, and Follow stays on; the page still does not scroll.
    expect(press("ArrowDown")).toBe(true);
    expect(selected()).toEqual(at(3));
    expect(follow().checked).toBe(true);
    expect(scrolled).toEqual([]);

    expect(press("ArrowUp")).toBe(true);
    expect(selected()).toEqual(at(2));
    expect(follow().checked).toBe(false);
    expect(scrolled).toEqual([options()[2]]);

    press("ArrowUp");
    press("ArrowUp");
    expect(selected()).toEqual(at(0));
    // At the first event ↑ does nothing.
    expect(press("ArrowUp")).toBe(true);
    expect(selected()).toEqual(at(0));

    press("ArrowDown");
    expect(selected()).toEqual(at(1));
    expect(scrolled.at(-1)).toBe(options()[1]);
  });

  it("selects the first event with Home and the last with End, each scrolled into view", async () => {
    await open(x, steps, capture());
    await choose("Events");
    expect(press("Home")).toBe(true);
    expect(selected()).toEqual(at(0));
    expect(follow().checked).toBe(false);
    expect(scrolled).toEqual([options()[0]]);
    expect(press("End")).toBe(true);
    expect(selected()).toEqual(at(3));
    expect(scrolled).toEqual([options()[0], options()[3]]);
  });

  it("gives the list focus on a click, so the arrow keys work right after it", async () => {
    await open(x, steps, capture());
    await choose("Events");
    await act(async () => options()[1].click());
    expect(document.activeElement).toBe(list());
    expect(selected()).toEqual(at(1));
    expect(follow().checked).toBe(false);
    press("ArrowDown", document.activeElement as HTMLElement);
    expect(selected()).toEqual(at(2));
  });

  it("leaves other keys, keys with a modifier, and keys in another control alone", async () => {
    await open(x, steps, capture());
    await choose("Events");
    expect(press("ArrowDown", follow())).toBe(false);
    expect(press("ArrowUp", follow())).toBe(false);
    expect(press("ArrowUp", list(), { metaKey: true })).toBe(false);
    expect(press("Home", list(), { ctrlKey: true })).toBe(false);
    expect(press("Enter")).toBe(false);
    expect(selected()).toEqual(at(3));
    expect(follow().checked).toBe(true);
  });
});

// A harness task's conversation alone — each attempt's prompt, then the agent messages it received — read only, and
// the view its detail opens on.
describe("the chat view", () => {
  const at = (minute: number) => `2026-10-21T09:${String(minute).padStart(2, "0")}:00Z`;
  const context = "This session is run by HarmFlow, a local scheduler.\nWorkspace: /workspaces/core-banking\n";

  /** Each attempt of the view: its header, then each turn's label, text, and time. */
  const conversation = () =>
    [...host!.querySelectorAll(".chat__attempt")].map((li) => ({
      header: li.querySelector(".chat__header")!.textContent,
      turns: [...li.querySelectorAll(".chat-turn")].map((turn) => [
        turn.querySelector(".chat-turn__label")!.textContent,
        (turn.querySelector(".chat-turn__text") ?? turn.querySelector(".muted"))!.textContent,
        turn.querySelector("time")!.getAttribute("datetime"),
      ]),
    }));
  const notes = () => [...host!.querySelectorAll(".chat__note")].map((n) => n.textContent);

  it("opens on the chat view: each attempt's prompt, then its agent messages, each with its time, and nothing else", async () => {
    const x: Execution = {
      ...completed,
      id: "exec-chat",
      attempts: [
        { ...base.attempts[0], startedAt: at(0), prompt: "Implement org/core-banking#123.", sessionContext: context },
        { ...base.attempts[1], startedAt: at(5), prompt: "Implement org/core-banking#123, from the last commit.", sessionContext: context },
      ],
    };
    const on = { executionId: x.id };
    const one = { ...on, attempt: 1, harness: "claude-code" };
    const two = { ...on, attempt: 2, harness: "codex" };
    await open(
      x,
      [
        event(1, "execution.started", { ...on, timestamp: at(0), message: "Loading configuration" }),
        event(2, "harness.started", { ...one, timestamp: at(0), message: "Claude Code 2.1.239 started" }),
        event(3, "harness.message", { ...one, timestamp: at(1), message: "I will read the issue first." }),
        event(4, "tool.started", { ...one, timestamp: at(2), payload: { toolUseId: "t1", tool: "Bash", input: { command: "./gradlew test" } } }),
        event(5, "tool.completed", { ...one, timestamp: at(3), payload: { toolUseId: "t1", output: "BUILD SUCCESSFUL" } }),
        event(6, "harness.message", { ...one, timestamp: at(4), message: "The tests pass." }),
        event(7, "harness.quota_exhausted", { ...one, timestamp: at(4), severity: "warning", message: "Claude Code exhausted its quota" }),
        event(8, "harness.switched", { ...one, timestamp: at(5), severity: "warning", message: "switching from Claude Code to Codex" }),
        event(9, "harness.started", { ...two, timestamp: at(5), message: "Codex 0.163.0 started" }),
        event(10, "harness.message", { ...two, timestamp: at(6), message: "Picking up from the last commit." }),
        event(11, "harness.message", { ...two, timestamp: at(7), message: "Pushed the branch." }),
        event(12, "execution.completed", { ...on, timestamp: at(8), message: "Opened a draft pull request" }),
      ],
      capture("Claude AI usage limit reached\n", "git push\n"),
    );

    // The first tab, and the one shown with nothing clicked.
    const tabs = [...host!.querySelectorAll('[role="tab"]')].map((t) => t.textContent);
    expect(tabs.slice(0, 2)).toEqual(["Chat(4)", expect.stringMatching(/^Events/)]);
    expect(selectedTab()).toBe("Chat(4)");

    expect(conversation()).toEqual([
      {
        header: expect.stringMatching(/^Attempt 1Claude Code/),
        turns: [
          ["Prompt", "Implement org/core-banking#123.", at(0)],
          ["Agent", "I will read the issue first.", at(1)],
          ["Agent", "The tests pass.", at(4)],
        ],
      },
      {
        header: expect.stringMatching(/^Attempt 2Codex/),
        turns: [
          ["Prompt", "Implement org/core-banking#123, from the last commit.", at(5)],
          ["Agent", "Picking up from the last commit.", at(6)],
          ["Agent", "Pushed the branch.", at(7)],
        ],
      },
    ]);
    expect(host!.querySelector(".chat-turn--agent time")!.textContent).toBe(formatTime(at(1), "en-US"));

    // No tool, command, status, or warning event, and not the session context.
    const shown = body();
    for (const absent of ["Loading configuration", "Claude Code 2.1.239 started", "./gradlew test", "BUILD SUCCESSFUL", "exhausted its quota", "Codex 0.163.0 started", "Opened a draft pull request", "Session context", "This session is run by HarmFlow"]) {
      expect(shown, absent).not.toContain(absent);
    }
    // Read-only: no text box, and no read of the raw output it does not show.
    expect(host!.querySelectorAll(".detail__body :is(input, textarea, select, [contenteditable], [role='textbox'])")).toHaveLength(0);
    expect(daemon.requests).not.toContain(`GET /executions/${x.id}/output`);
  });

  it("shows the prompt and the messages as plain text, their line breaks kept and nothing in them read as markup", async () => {
    const prompt = "# Task\nFix **the bug** in <code>auth</code>.\n{{token}} $t(app.name)";
    const said = "Done:\n- [x] tests\n<b>not bold</b> <img src=x onerror=alert(1)> `code`";
    const x: Execution = { ...completed, id: "exec-chat-plain", attempts: [{ ...base.attempts[0], prompt }] };
    await open(x, [event(1, "harness.message", { executionId: x.id, attempt: 1, harness: "claude-code", message: said })], capture());

    const texts = [...host!.querySelectorAll(".chat-turn__text")];
    expect(texts.map((e) => e.textContent)).toEqual([prompt, said]);
    expect(texts.map((e) => e.children.length)).toEqual([0, 0]);
  });

  it("says an attempt from a record older than its prompt sent one not recorded", async () => {
    const x: Execution = { ...completed, id: "exec-chat-older", attempts: [base.attempts[0]] };
    await open(x, [event(1, "harness.message", { executionId: x.id, attempt: 1, harness: "claude-code", timestamp: at(1), message: "Hello." })], capture());
    expect(conversation()).toEqual([
      {
        header: expect.stringMatching(/^Attempt 1Claude Code/),
        turns: [
          ["Prompt", "Not recorded", started],
          ["Agent", "Hello.", at(1)],
        ],
      },
    ]);
  });

  it("shows an empty state before any attempt", async () => {
    const queued: Execution = { ...base, id: "exec-chat-queued", status: "queued", startedAt: undefined, attempts: [], commands: [] };
    await open(queued, [event(1, "execution.created", { executionId: queued.id })], capture());
    expect(selectedTab()).toBe("Chat(0)");
    expect(body()).toBe("No attempts yet.");
  });

  it("adds an agent message at the end of the running attempt's conversation as it arrives, with no reload", async () => {
    const attempt = { ...base.attempts[0], startedAt: at(0), endedAt: undefined, outcome: "running" as const, prompt: "Implement org/core-banking#123." };
    const running: Execution = { ...base, id: "exec-chat-live", harness: "claude-code", attempts: [attempt] };
    const one = { executionId: running.id, attempt: 1, harness: "claude-code" };
    await open(running, [event(1, "harness.started", { ...one, timestamp: at(0) })], capture());
    expect(notes()).toEqual(["No agent messages yet."]);

    const reads = () => daemon.requests.filter((r) => r.startsWith(`GET /executions/${running.id}`)).length;
    const before = reads();
    // The stream hands each event to the store, which the view reads.
    const arrive = (e: HarmflowEvent) =>
      act(async () => patchStateForTests({ events: { ...getState().events, [running.id]: [...getState().events[running.id], e] } }));

    await arrive(event(2, "harness.message", { ...one, timestamp: at(1), message: "Reading the issue." }));
    await arrive(event(3, "tool.started", { ...one, timestamp: at(2), payload: { toolUseId: "t1", tool: "Bash", input: { command: "./gradlew test" } } }));
    await arrive(event(4, "harness.message", { ...one, timestamp: at(3), message: "Running the tests." }));
    expect(conversation()).toEqual([
      {
        header: expect.stringMatching(/^Attempt 1Claude Code/),
        turns: [
          ["Prompt", "Implement org/core-banking#123.", at(0)],
          ["Agent", "Reading the issue.", at(1)],
          ["Agent", "Running the tests.", at(3)],
        ],
      },
    ]);
    expect(notes()).toEqual([]);
    expect(selectedTab()).toBe("Chat(2)");

    // A failover: the next attempt's message shows under its own number before the record is read again, and its
    // prompt joins it once it is.
    await arrive(event(5, "harness.message", { executionId: running.id, attempt: 2, harness: "codex", timestamp: at(5), message: "Taking over." }));
    expect(conversation()[1]).toEqual({ header: "Attempt 2Codex", turns: [["Agent", "Taking over.", at(5)]] });
    expect(reads()).toBe(before);

    const second = { ...base.attempts[1], startedAt: at(4), endedAt: undefined, outcome: "running" as const, prompt: "Implement org/core-banking#123, resuming." };
    await act(async () => patchStateForTests({ details: { ...getState().details, [running.id]: { ...running, attempts: [{ ...attempt, outcome: "quota_exhausted", endedAt: at(4) }, second] } } }));
    expect(conversation()[1].turns).toEqual([
      ["Prompt", "Implement org/core-banking#123, resuming.", at(4)],
      ["Agent", "Taking over.", at(5)],
    ]);
  });

  it("keeps each attempt's prompt once the log has expired, and says the agent's messages left with it", async () => {
    const x: Execution = {
      ...completed,
      id: "exec-chat-expired",
      attempts: [
        { ...base.attempts[0], prompt: "Implement org/core-banking#123." },
        { ...base.attempts[1], prompt: "Implement org/core-banking#123, from the last commit." },
      ],
    };
    await open(x, [], { logExpired: true, attempts: [] }, true);
    expect(selectedTab()).toBe("Chat(0)");
    expect(body()).toContain("This execution's log has expired under the retention period; its record is kept.");
    expect(conversation()).toEqual([
      { header: expect.stringMatching(/^Attempt 1Claude Code/), turns: [["Prompt", "Implement org/core-banking#123.", started]] },
      { header: expect.stringMatching(/^Attempt 2Codex/), turns: [["Prompt", "Implement org/core-banking#123, from the last commit.", started]] },
    ]);
    expect(notes()).toEqual(["The agent's messages were deleted with the expired log.", "The agent's messages were deleted with the expired log."]);
  });
});

// The detail offers Cancel on a running or queued execution, and nothing that writes to the
// agent; across every tab, the one request that is not a read is that cancel.
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
