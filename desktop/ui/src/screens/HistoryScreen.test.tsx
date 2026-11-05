// @vitest-environment happy-dom
// Drives the History screen, the task's history, in a DOM against a stand-in for the daemon's local API: an execution
// whose log expired under the retention period is still listed for its task, with its whole record, and opening it says
// its log is gone. That the daemon keeps the record and serves it so is app › TestRetentionDeletesOnlyExpiredLogs's and
// itest › TestRetentionExpiresTheLogAtStart's. An execution that found no work is listed as No work, in the neutral
// tone, and one that was interrupted as Interrupted, in the warning tone, neither as a failure. Its filter bar gives
// the date fields the class that keeps them on the filters' line.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { native } from "../api/native";
import type { Execution, ExecutionOutput, ExecutionSummary, HarmflowEvent, TaskSummary } from "../api/types";
import i18n from "../i18n";
import { patchStateForTests } from "../state/store";
import { HistoryScreen } from "./HistoryScreen";

// The daemon as the client sees it: `GET /executions` lists the executions it holds, of one task, fired by one trigger,
// and holding a search text in the task's name when the query names them, newest first, a page of at most `limit`
// created before `before`, with how many the filters match in its total; every other route has its answer; and every
// request it received.
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
        const params = new URLSearchParams(search);
        const taskId = params.get("taskId");
        const trigger = params.get("trigger");
        const text = params.get("search")?.toLowerCase();
        const before = params.get("before");
        const matched = daemon.executions.filter(
          (x) => (!taskId || x.taskId === taskId) && (!trigger || x.trigger === trigger) && (!text || x.taskName.toLowerCase().includes(text)),
        );
        const listed = matched.filter((x) => !before || x.createdAt < before);
        return { status: 200, body: listed.slice(0, Number(params.get("limit") ?? listed.length)), total: matched.length };
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
  vi.useRealTimers();
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

describe("an execution that found no work", () => {
  it("shows its row with the No work chip in the neutral tone, apart from a failed one's", async () => {
    const noWork: ExecutionSummary = { ...other, id: "exec-no-work", status: "no_work" };
    const failed: ExecutionSummary = { ...aged, id: "exec-failed", status: "failed", pullRequestUrl: undefined, testsStatus: undefined };
    daemon.executions = [noWork, failed];
    await open();
    expect(rows()).toHaveLength(2);

    const status = (id: string) => {
      const chip = cell(rowOf(id)!, 1).querySelector(".chip")!;
      return { label: cell(rowOf(id)!, 1).textContent, tones: [...chip.classList].filter((c) => /^chip--(neutral|success|danger|warning|blue|magenta)$/.test(c)) };
    };
    expect(status(noWork.id)).toEqual({ label: "No work", tones: ["chip--neutral"] });
    expect(status(failed.id)).toEqual({ label: "Failed", tones: ["chip--danger"] });
  });
});

describe("an execution that was interrupted", () => {
  it("shows its row with the Interrupted chip in the warning tone, apart from a failed one's", async () => {
    const interrupted: ExecutionSummary = { ...other, id: "exec-interrupted", status: "interrupted" };
    const failed: ExecutionSummary = { ...aged, id: "exec-failed", status: "failed", pullRequestUrl: undefined, testsStatus: undefined };
    daemon.executions = [interrupted, failed];
    await open();
    expect(rows()).toHaveLength(2);

    const status = (id: string) => {
      const chip = cell(rowOf(id)!, 1).querySelector(".chip")!;
      return { label: cell(rowOf(id)!, 1).textContent, tones: [...chip.classList].filter((c) => /^chip--(neutral|success|danger|warning|blue|magenta)$/.test(c)) };
    };
    expect(status(interrupted.id)).toEqual({ label: "Interrupted", tones: ["chip--warning"] });
    expect(status(failed.id)).toEqual({ label: "Failed", tones: ["chip--danger"] });
  });
});

describe("the automatic refresh, after Load more", () => {
  /** The instant `minutes` after 09:00, as the daemon writes it. */
  const at = (minutes: number) => new Date(Date.UTC(2026, 6, 1, 9, minutes)).toISOString();
  /** The standup's nth execution counting back from the newest: 80 of them make a full page and part of a second. */
  const nth = (n: number): ExecutionSummary => ({ ...other, id: `exec-${String(n).padStart(3, "0")}`, createdAt: at(100 - n), startedAt: at(100 - n), endedAt: at(100 - n) });

  it("keeps the pages Load more added, shows a changed row's new state, and adds the new execution at the top", async () => {
    const running: ExecutionSummary = { ...nth(60), status: "running", endedAt: undefined };
    daemon.executions = Array.from({ length: 80 }, (_, n) => (n === 60 ? running : nth(n)));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await open();
    expect(rows()).toHaveLength(50);

    // Load more: the second page, the last one.
    const more = [...host!.querySelectorAll<HTMLButtonElement>(".table__footer button")].find((b) => b.textContent === "Load older executions")!;
    await act(async () => more.click());
    await settle();
    expect(rows()).toHaveLength(80);
    expect(cell(rowOf(running.id)!, 1).textContent).toBe("Running");

    // Meanwhile the running execution ends, and a new one starts: an event reaches the app.
    const fresh: ExecutionSummary = { ...other, id: "exec-new", createdAt: at(101), startedAt: at(101), endedAt: undefined, status: "running" };
    daemon.executions = [fresh, ...daemon.executions.map((x) => (x.id === running.id ? nth(60) : x))];
    daemon.requests.length = 0;
    act(() => patchStateForTests({ lastEventId: 1 }));
    await act(() => vi.advanceTimersByTimeAsync(1500));
    await settle();

    // The refresh fetches again, page by page, as many as were shown: the first page, then the rest from its last row.
    expect(daemon.requests.map((r) => Object.fromEntries(new URLSearchParams(r.split("?")[1])))).toEqual([
      { limit: "50" },
      { limit: "50", before: nth(48).createdAt },
    ]);
    expect(rows()).toHaveLength(81);
    expect(rows()[0]).toBe(rowOf(fresh.id));
    expect(rowOf(nth(79).id)).toBeDefined();
    expect(cell(rowOf(running.id)!, 1).textContent).toBe("Completed");
    expect(host!.querySelector(".table__footer")!.textContent).toBe("Showing 81 / 81No older executions.");

    // A change of a filter still starts again from the first page.
    daemon.requests.length = 0;
    await choose("Task", standup.id);
    expect(daemon.requests).toEqual([`GET /executions?taskId=${standup.id}&limit=50`]);
    expect(rows()).toHaveLength(50);
  });
});

describe("the count of the executions loaded out of those that match", () => {
  /** The instant `minutes` after 09:00, as the daemon writes it. */
  const at = (minutes: number) => new Date(Date.UTC(2026, 6, 1, 9, minutes)).toISOString();
  /** The standup's nth execution counting back from the newest, fired by the schedule or, one in two, by hand. */
  const nth = (n: number): ExecutionSummary => ({
    ...other,
    id: `exec-${String(n).padStart(3, "0")}`,
    trigger: n % 2 ? "manual" : "schedule",
    createdAt: at(100 - n),
    startedAt: at(100 - n),
    endedAt: at(100 - n),
  });
  const footer = () => host!.querySelector(".table__footer")!.textContent;
  const query = (request: string) => Object.fromEntries(new URLSearchParams(request.split("?")[1]));

  /** Types a search text as the user does, then lets the screen's pause after typing pass. */
  async function type(text: string) {
    const input = host!.querySelector<HTMLInputElement>('input[aria-label="Search"]')!;
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(input, text);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(() => new Promise<void>((resolve) => setTimeout(resolve, 350)));
    await settle();
  }

  it("shows the count, follows Load more and each filter, and sends the trigger and the search text to the daemon", async () => {
    daemon.executions = [...Array.from({ length: 80 }, (_, n) => nth(n)), { ...aged, createdAt: at(0) }];
    await open();
    expect(rows()).toHaveLength(50);
    expect(footer()).toBe("Showing 50 / 81Load older executions");

    // Load more: the second page, and the count of what is loaded with it.
    const more = [...host!.querySelectorAll<HTMLButtonElement>(".table__footer button")].find((b) => b.textContent === "Load older executions")!;
    await act(async () => more.click());
    await settle();
    expect(rows()).toHaveLength(81);
    expect(footer()).toBe("Showing 81 / 81No older executions.");

    // The trigger filter is the daemon's: the page and the total are of the executions fired by hand alone.
    daemon.requests.length = 0;
    await choose("Trigger", "manual");
    expect(daemon.requests.map(query)).toEqual([{ trigger: "manual", limit: "50" }]);
    expect(rows()).toHaveLength(40);
    expect(rows().every((r) => cell(r, 5).textContent === "Manual")).toBe(true);
    expect(footer()).toBe("Showing 40 / 40No older executions.");

    // So is the search text, sent trimmed once the user stops typing, beside the trigger.
    daemon.requests.length = 0;
    await type("  Implement ");
    expect(daemon.requests.map(query)).toEqual([{ trigger: "manual", search: "Implement", limit: "50" }]);
    expect(rows()).toHaveLength(0);
    expect(host!.querySelector(".empty__title")!.textContent).toBe("No executions yet.");

    await choose("Trigger", "");
    expect(rows().map((r) => r.querySelector("td:nth-child(2) .mono")!.textContent)).toEqual([aged.id]);
    expect(footer()).toBe("Showing 1 / 1No older executions.");
  });

  it("writes the count in the interface's language", async () => {
    daemon.executions = Array.from({ length: 60 }, (_, n) => nth(n));
    await i18n.changeLanguage("pt-BR");
    try {
      await open();
      expect(footer()).toBe("Exibindo 50 / 60Carregar execuções mais antigas");
    } finally {
      await i18n.changeLanguage("en-US");
    }
  });
});

describe("the filter bar", () => {
  // The DOM here lays nothing out: design.test.ts checks that the class keeps a date field only as wide as its date.
  it("gives both date fields the class that keeps them on the filters' line", async () => {
    await open();
    const dates = [...host!.querySelectorAll<HTMLInputElement>('.filters input[type="date"]')];
    expect(dates.map((d) => d.getAttribute("aria-label"))).toEqual(["From", "To"]);
    for (const date of dates) {
      expect(date.classList.contains("input"), date.getAttribute("aria-label")!).toBe(true);
      expect(date.classList.contains("filters__date"), date.getAttribute("aria-label")!).toBe(true);
    }
  });
});
