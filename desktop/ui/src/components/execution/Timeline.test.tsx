// @vitest-environment happy-dom
// Drives the execution detail's Events timeline in a DOM, against a stand-in for the daemon's local API: the list is
// one tab stop, whose arrow keys, Home, and End move the selection, and its Follow option is the compact Checkbox.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { StreamHandlers } from "../../api/transport";
import type { Execution, ExecutionOutput, HarmflowEvent } from "../../api/types";
import i18n from "../../i18n";
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

async function choose(label: string): Promise<string> {
  const tab = [...host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((b) => b.textContent?.startsWith(label));
  expect(tab, label).toBeDefined();
  await act(async () => tab!.click());
  await settle();
  return body();
}

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
  /** The rows, and the elements inside them, that are a button, link, or form control, or have a tabIndex of 0 or more. */
  const tabStopsInRows = () =>
    options()
      .flatMap((o) => [o, ...o.querySelectorAll<HTMLElement>("*")])
      .filter((el) => el.matches("button, a[href], input, select, textarea") || Number(el.getAttribute("tabindex") ?? -1) >= 0 || el.tabIndex >= 0);

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

  it("is one tab stop: no row, and nothing inside a row, is in the tab order, before or after a key moves the selection", async () => {
    await open(x, steps, capture());
    await choose("Events");
    expect(list().tabIndex).toBe(0);
    expect(options()).toHaveLength(4);
    // Each row holds elements of its own, so the check below reaches inside every one of them.
    expect(options().every((o) => o.querySelector("*") !== null)).toBe(true);
    expect(tabStopsInRows()).toEqual([]);
    expect(press("Home")).toBe(true);
    expect(selected()).toEqual(at(0));
    expect(tabStopsInRows()).toEqual([]);
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

  it("leaves ↑, ↓, Home, and End alone with Alt held", async () => {
    await open(x, steps, capture());
    await choose("Events");
    act(() => list().focus());
    expect(document.activeElement).toBe(list());
    for (const key of ["ArrowUp", "ArrowDown", "Home", "End"]) expect(press(key, list(), { altKey: true }), key).toBe(false);
    expect(selected()).toEqual(at(3));
    expect(follow().checked).toBe(true);
    expect(scrolled).toEqual([]);
  });

  // harmflow-app#992: the Follow option is the shared Checkbox, at its compact size, with its label and default kept.
  it("draws Follow with the compact Checkbox, on by default, and turns it back on with a click once a key turned it off", async () => {
    await open(x, steps, capture());
    await choose("Events");
    const option = follow().closest("label")!;
    expect([...option.classList]).toEqual(["checkbox", "checkbox--compact"]);
    expect(option.querySelector(".checkbox__box")).not.toBeNull();
    expect(option.textContent).toBe("Follow");
    expect(host!.querySelector(".mini-toggle")).toBeNull();
    expect(follow().checked).toBe(true);

    press("ArrowUp");
    expect(follow().checked).toBe(false);
    await act(async () => follow().click());
    expect(follow().checked).toBe(true);
    await act(async () => follow().click());
    expect(follow().checked).toBe(false);
  });
});
