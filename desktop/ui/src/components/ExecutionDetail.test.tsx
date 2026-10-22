// @vitest-environment happy-dom
// Drives the execution detail in a DOM, against a stand-in for the daemon's local API: every view FS-008 FR-008 asks
// for, and the raw output read from the daemon's route, again while the execution runs (FS-008 FR-008, AC-008).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { Execution, ExecutionOutput, HarmflowEvent } from "../api/types";
import i18n from "../i18n";
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
  // Claude Code reports its model; Codex reports none, and neither its effort (D-105).
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

async function open(x: Execution, events: HarmflowEvent[], output: ExecutionOutput) {
  daemon.answers.set(`/executions/${x.id}`, x);
  daemon.answers.set(`/executions/${x.id}/events`, { events, logExpired: false });
  daemon.answers.set(`/executions/${x.id}/output`, output);
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () => root!.render(<ExecutionDetail executionId={x.id} summary={x} />));
  await settle();
}

const body = () => host!.querySelector(".detail__body")!.textContent ?? "";

async function choose(label: string): Promise<string> {
  const tab = [...host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((b) => b.textContent?.startsWith(label));
  expect(tab, label).toBeDefined();
  await act(async () => tab!.click());
  await settle();
  return body();
}

describe("execution detail", () => {
  it("offers every view FR-008 names, the raw output as the daemon redacted it (FS-008 AC-008)", async () => {
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
      expect.stringMatching(/^Events/),
      "Raw output",
      "Changed files(1)",
      "Commands(1)",
      "Results",
      "Attempts(2)",
      "Costs",
    ]);

    expect(host!.querySelectorAll(".tl-item").length).toBeGreaterThanOrEqual(3);
    expect(body()).toContain("./gradlew test");

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

  it("names the model and effort each attempt's harness reported, and the rest as not reported (FS-005 AC-008)", async () => {
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

  it("shows a failover's switch events in its timeline, in order, each with its own tone (FS-005 AC-008)", async () => {
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

  it("shows the daemon's texts in the interface's language, and the agent's and harness's as they are (FS-012 AC-005)", async () => {
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
      const timeline = body();
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
  it("shows the prompt and the session context each attempt sent, collapsed, and says when a record has none (FS-008 AC-013)", async () => {
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

  // FS-008 AC-012: a completed failover from Claude Code to Codex whose attempts both reported their usage.
  it("shows a failover's costs, the CLI's and the table's, each with its source, and their sum (FS-008 AC-012)", async () => {
    const haiku = { input: 1, cacheWriteShort: 1.25, cacheWriteLong: 2, cacheRead: 0.1, output: 5 };
    const terra = { input: 2, cacheWriteShort: 2.5, cacheWriteLong: 5, cacheRead: 0.2, output: 12 };
    const failover: Execution = {
      ...completed,
      id: "exec-ac-012",
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

  // FS-008 FR-012: while an attempt runs, its tokens so far and no cost; the cost the attempt ended with, once it ends.
  it("shows a running attempt's tokens so far without a cost, and its cost once it ends (FS-008 FR-012)", async () => {
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

  it("reads the raw output from the daemon's route again every 2 s while the execution runs (FS-008 FR-008)", async () => {
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

describe("a script task's execution (FS-013 FR-012, AC-012)", () => {
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
    for (const absent of ["Attempts", "Costs", "Changed files", "Commands"]) {
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
});
