// @vitest-environment happy-dom
// Drives the execution detail's chat view in a DOM, against a stand-in for the daemon's local API: a harness task's
// conversation alone, read only, and the view its detail opens on.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { StreamHandlers } from "../../api/transport";
import type { Execution, ExecutionOutput, HarmflowEvent } from "../../api/types";
import i18n from "../../i18n";
import { formatTime } from "../../lib/format";
import { getState, patchStateForTests } from "../../state/store";
import { ExecutionDetail } from "../ExecutionDetail";

// The daemon as the client sees it: an answer per route, every request it received, and the stream it feeds.
const daemon = vi.hoisted(() => ({
  answers: new Map<string, unknown>(),
  requests: [] as string[],
  stream: undefined as StreamHandlers | undefined,
}));
vi.mock("../../api/transport", () => ({
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

// A harness task's conversation alone — each attempt's prompt, then the session messages it received — read only, and
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

  it("opens on the chat view: each attempt's prompt, then its session messages, each with its time, and nothing else", async () => {
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
          ["Session","I will read the issue first.", at(1)],
          ["Session","The tests pass.", at(4)],
        ],
      },
      {
        header: expect.stringMatching(/^Attempt 2Codex/),
        turns: [
          ["Prompt", "Implement org/core-banking#123, from the last commit.", at(5)],
          ["Session","Picking up from the last commit.", at(6)],
          ["Session","Pushed the branch.", at(7)],
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
          ["Session","Hello.", at(1)],
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

  it("adds a session message at the end of the running attempt's conversation as it arrives, with no reload", async () => {
    const attempt = { ...base.attempts[0], startedAt: at(0), endedAt: undefined, outcome: "running" as const, prompt: "Implement org/core-banking#123." };
    const running: Execution = { ...base, id: "exec-chat-live", harness: "claude-code", attempts: [attempt] };
    const one = { executionId: running.id, attempt: 1, harness: "claude-code" };
    await open(running, [event(1, "harness.started", { ...one, timestamp: at(0) })], capture());
    expect(notes()).toEqual(["No session messages yet."]);

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
          ["Session","Reading the issue.", at(1)],
          ["Session","Running the tests.", at(3)],
        ],
      },
    ]);
    expect(notes()).toEqual([]);
    expect(selectedTab()).toBe("Chat(2)");

    // A failover: the next attempt's message shows under its own number before the record is read again, and its
    // prompt joins it once it is.
    await arrive(event(5, "harness.message", { executionId: running.id, attempt: 2, harness: "codex", timestamp: at(5), message: "Taking over." }));
    expect(conversation()[1]).toEqual({ header: "Attempt 2Codex", turns: [["Session","Taking over.", at(5)]] });
    expect(reads()).toBe(before);

    const second = { ...base.attempts[1], startedAt: at(4), endedAt: undefined, outcome: "running" as const, prompt: "Implement org/core-banking#123, resuming." };
    await act(async () => patchStateForTests({ details: { ...getState().details, [running.id]: { ...running, attempts: [{ ...attempt, outcome: "quota_exhausted", endedAt: at(4) }, second] } } }));
    expect(conversation()[1].turns).toEqual([
      ["Prompt", "Implement org/core-banking#123, resuming.", at(4)],
      ["Session","Taking over.", at(5)],
    ]);
  });

  it("keeps each attempt's prompt once the log has expired, and says the session's messages left with it", async () => {
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
    expect(notes()).toEqual(["The session's messages were deleted with the expired log.", "The session's messages were deleted with the expired log."]);
  });
});
