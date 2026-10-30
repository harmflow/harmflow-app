// @vitest-environment happy-dom
// Drives the History screen, the task's history, in a DOM against a stand-in for the daemon's local API: an execution
// whose log expired under the retention period is still listed for its task, with its whole record, and opening it says
// its log is gone. That the daemon keeps the record and serves it so is app › TestRetentionDeletesOnlyExpiredLogs's and
// itest › TestRetentionExpiresTheLogAtStart's.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { native } from "../api/native";
import type { Execution, ExecutionOutput, ExecutionSummary, HarmflowEvent, TaskSummary } from "../api/types";
import i18n from "../i18n";
import { patchStateForTests } from "../state/store";
import { HistoryScreen } from "./HistoryScreen";

// The daemon as the client sees it: `GET /executions` lists the executions it holds, of one task when the query names
// it, newest first; every other route has its answer; and every request it received.
const daemon = vi.hoisted(() => ({
  executions: [] as ExecutionSummary[],
  answers: new Map<string, unknown>(),
  requests: [] as string[],
}));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string) => {
      daemon.requests.push(`${method} ${path}`);
      const [route, search] = path.split("?");
      if (method === "GET" && route === "/executions") {
        const taskId = new URLSearchParams(search).get("taskId");
        return { status: 200, body: daemon.executions.filter((x) => !taskId || x.taskId === taskId) };
      }
      return daemon.answers.has(route) ? { status: 200, body: daemon.answers.get(route) } : { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

const started = "2026-07-01T09:00:00Z";
const switched = "2026-07-01T09:04:00Z";
const ended = "2026-07-01T09:20:00Z";
const pullRequestUrl = "https://github.com/org/core-banking/pull/9";

const issues: TaskSummary = {
  id: "core-issues",
  name: "Implement GitHub issues",
  type: "github",
  enabled: true,
  revision: "r1",
  harness: "claude-code",
  schedule: "0 9 * * 1-5",
  warnings: [],
};
const standup: TaskSummary = { ...issues, id: "standup", name: "Standup notes", type: "general" };

/** The execution as `GET /executions` lists it: Claude Code ran out of quota, and Codex finished the work. */
const aged: ExecutionSummary = {
  id: "exec-aged",
  taskId: issues.id,
  taskName: issues.name,
  taskType: "github",
  status: "completed",
  trigger: "schedule",
  createdAt: started,
  startedAt: started,
  endedAt: ended,
  harness: "codex",
  attemptCount: 2,
  repository: "org/core-banking",
  workItem: "org/core-banking#123",
  pullRequestUrl,
  testsStatus: "passed",
};

/** Its record as `GET /executions/{executionId}` serves it once the log expired. */
const record: Execution = {
  ...aged,
  logExpired: true,
  attempts: [
    { number: 1, harness: "claude-code", configuredModel: "default", configuredEffort: "default", startedAt: started, endedAt: switched, outcome: "quota_exhausted" },
    { number: 2, harness: "codex", configuredModel: "default", configuredEffort: "default", startedAt: switched, endedAt: ended, outcome: "succeeded" },
  ],
  workItems: [
    {
      key: "org/core-banking#123",
      title: "Session tokens expire too early",
      issueNumber: 123,
      branch: "fix/123-session-tokens",
      tests: { status: "passed" },
      pullRequest: { number: 9, url: pullRequestUrl, draft: true },
    },
  ],
  warnings: [],
  changedFiles: [],
  commands: [],
};

/** Another task's execution, which the history lists until the task filter leaves it out. */
const other: ExecutionSummary = {
  id: "exec-standup",
  taskId: standup.id,
  taskName: standup.name,
  taskType: "general",
  status: "completed",
  trigger: "schedule",
  createdAt: switched,
  startedAt: switched,
  endedAt: ended,
  harness: "claude-code",
  attemptCount: 1,
};

/** With the log gone, the events are none and the captures are empty, as the daemon serves them. */
const expiredEvents: { events: HarmflowEvent[]; logExpired: boolean } = { events: [], logExpired: true };
const expiredOutput: ExecutionOutput = {
  logExpired: true,
  attempts: [
    { attempt: 1, harness: "claude-code", size: 0, offset: 0, text: "" },
    { attempt: 2, harness: "codex", size: 0, offset: 0, text: "" },
  ],
};

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
  vi.restoreAllMocks();
  daemon.executions = [];
  daemon.answers.clear();
  daemon.requests.length = 0;
  patchStateForTests({ tasks: [], details: {}, events: {}, logExpired: {} });
});

/** Lets the stand-in daemon's answers arrive and React render them. */
const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

/** The screen as the app shows it: the execution a row opens is the one shown in the drawer. */
function Screen() {
  const [openId, setOpenId] = useState<string | null>(null);
  return <HistoryScreen openId={openId} onOpen={setOpenId} />;
}

async function open() {
  patchStateForTests({ tasks: [issues, standup] });
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () => root!.render(<Screen />));
  await settle();
}

/** Chooses an option of a filter as the user does, by its label. */
async function choose(filter: string, value: string) {
  const select = host!.querySelector<HTMLSelectElement>(`select[aria-label="${filter}"]`)!;
  const setValue = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(select, value);
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await settle();
}

const rows = () => [...host!.querySelectorAll<HTMLTableRowElement>("tbody tr")];
const rowOf = (id: string) => rows().find((r) => r.querySelector("td:nth-child(2) .mono")?.textContent === id);
const cell = (row: HTMLTableRowElement, column: number) => row.querySelector(`td:nth-child(${column})`)!;
const drawer = () => host!.querySelector<HTMLElement>('.drawer[role="dialog"]');
const body = () => drawer()!.querySelector(".detail__body")!.textContent ?? "";

async function tab(label: string) {
  const button = [...drawer()!.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((b) => b.textContent?.startsWith(label));
  expect(button, label).toBeDefined();
  await act(async () => button!.click());
  await settle();
  return button!;
}

describe("the task's history, after an execution's log expired", () => {
  it("still lists the execution for its task with its whole record, and opening it says its log is gone", async () => {
    daemon.executions = [other, aged];
    daemon.answers.set(`/executions/${aged.id}`, record);
    daemon.answers.set(`/executions/${aged.id}/events`, expiredEvents);
    daemon.answers.set(`/executions/${aged.id}/output`, expiredOutput);
    const openUrl = vi.spyOn(native, "openUrl").mockResolvedValue();
    await open();
    expect(rows()).toHaveLength(2);

    // The task's history: the screen reads the executions of the task chosen in its filter.
    daemon.requests.length = 0;
    await choose("Task", issues.id);
    expect(daemon.requests).toEqual([`GET /executions?taskId=${issues.id}&limit=50`]);

    // Still listed: its status, work item, harness with its two attempts, and its PR chip.
    expect(rows()).toHaveLength(1);
    const row = rowOf(aged.id)!;
    expect(row).toBeDefined();
    expect(cell(row, 1).textContent).toBe("Completed");
    expect(cell(row, 2).querySelector("strong")!.textContent).toBe(issues.name);
    expect(cell(row, 3).textContent).toBe("org/core-banking#123");
    expect(cell(row, 4).querySelector(".harness")!.textContent).toBe("Codex");
    expect(cell(row, 4).textContent).toBe("Codex ×2");
    expect([...cell(row, 8).querySelectorAll(".chip")].map((c) => c.textContent)).toContain("PR");

    // Opening the row: the execution's detail, with the banner that its log expired and its record is kept.
    daemon.requests.length = 0;
    await act(async () => row.click());
    await settle();
    expect(drawer()).not.toBeNull();
    expect(drawer()!.querySelector(".detail__title")!.textContent).toBe(issues.name);
    expect(drawer()!.querySelector(".detail__title-row .chip")!.textContent).toBe("Completed");
    expect(daemon.requests).toEqual([`GET /executions/${aged.id}`, `GET /executions/${aged.id}/events?limit=5000`]);
    const banners = () => [...drawer()!.querySelectorAll(".detail__body .banner")].map((b) => b.textContent);
    const expired = "This execution's log has expired under the retention period; its record is kept.";
    expect(banners()).toEqual([expired]);

    // Its log is gone: an Events view with no event, and no raw output.
    expect((await tab("Events")).textContent).toBe("Events(0)");
    expect(drawer()!.querySelectorAll(".tl-item")).toHaveLength(0);
    expect(drawer()!.querySelector(".timeline--empty")!.textContent).toBe("No events yet.");
    expect(body()).toBe(`${expired}No events yet.Select an event to see its details.`);

    await tab("Raw output");
    expect(daemon.requests).toContain(`GET /executions/${aged.id}/output`);
    expect(banners()).toEqual([expired]);
    expect(drawer()!.querySelector(".detail__body pre")).toBeNull();
    expect(body()).toBe(expired);

    // The record is whole: both attempts, each with its harness and its outcome.
    expect((await tab("Attempts")).textContent).toBe("Attempts(2)");
    const attempts = [...drawer()!.querySelectorAll(".attempt__header")].map((h) => ({
      attempt: h.querySelector("strong")!.textContent,
      harness: h.querySelector(".harness")!.textContent,
      outcome: h.querySelector(".chip")!.textContent,
    }));
    expect(attempts).toEqual([
      { attempt: "Attempt 1", harness: "Claude Code", outcome: "Quota exhausted" },
      { attempt: "Attempt 2", harness: "Codex", outcome: "Succeeded" },
    ]);

    // And the work item with its pull request, which opens at the record's URL.
    await tab("Results");
    const items = [...drawer()!.querySelectorAll(".results__item")];
    expect(items.map((i) => i.querySelector("h4")!.textContent)).toEqual(["org/core-banking#123 — Session tokens expire too early"]);
    const pr = items[0].querySelector<HTMLButtonElement>("button.link")!;
    expect(pr.textContent!.trim()).toBe("#9");
    await act(async () => pr.click());
    expect(openUrl.mock.calls).toEqual([[pullRequestUrl]]);
    expect(banners()).toEqual([expired]);
  });
});
