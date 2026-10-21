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
  model: "default",
  repository: "org/core-banking",
  workItem: "org/core-banking#123",
  attempts: [
    { number: 1, harness: "claude-code", model: "default", effort: "high", startedAt: started, endedAt: started, outcome: "quota_exhausted" },
    { number: 2, harness: "codex", model: "default", effort: "high", startedAt: started, endedAt: ended, outcome: "succeeded" },
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
