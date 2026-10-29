// @vitest-environment happy-dom
// Drives the Tasks screen's commands on a task against a stand-in for the daemon's local API: each one sends its
// request, and the screen shows the answer. What the daemon does with each request is itest › TestTaskLifecycle's,
// and app › TestDuplicate's and TestDisableAndDeleteWhileRunning's.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { native } from "../api/native";
import type { TaskSummary } from "../api/types";
import { Toasts } from "../components/toast";
import i18n from "../i18n";
import { patchStateForTests } from "../state/store";
import { TasksScreen, type TaskView } from "./TasksScreen";

// The daemon as the client sees it: an answer per method and route, and every request it received with its body.
const daemon = vi.hoisted(() => ({
  answers: new Map<string, { status: number; body: unknown }>(),
  requests: [] as { method: string; path: string; body: unknown }[],
}));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string, body?: unknown) => {
      daemon.requests.push({ method, path, body });
      return daemon.answers.get(`${method} ${path}`) ?? { status: 404, body: null };
    },
    requestText: async (path: string) => {
      daemon.requests.push({ method: "GET", path, body: undefined });
      const answer = daemon.answers.get(`GET ${path}`);
      return answer ? { status: answer.status, text: answer.body as string } : { status: 404, text: "" };
    },
    stream: () => () => {},
  },
}));

const answer = (method: string, path: string, body: unknown, status = 200) => daemon.answers.set(`${method} ${path}`, { status, body });
const req = (method: string, path: string, body?: unknown) => ({ method, path, body });

const standup: TaskSummary = {
  id: "standup",
  name: "Standup notes",
  type: "general",
  enabled: true,
  revision: "r1",
  harness: "claude-code",
  schedule: "0 9 * * 1-5",
  nextFireAt: "2026-10-30T12:00:00Z",
  warnings: [],
};
const copy: TaskSummary = { ...standup, id: "standup-copy", name: "Standup notes (copy)" };

let root: Root | undefined;
let host: HTMLElement | undefined;
let onView = vi.fn<(v: TaskView) => void>();

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

// A toast stays 3.5 seconds in a list the module keeps; a fake clock for its timeout ends each test's toasts with the
// test, so that none shows in the next.
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
  vi.restoreAllMocks();
  daemon.answers.clear();
  daemon.requests.length = 0;
  patchStateForTests({ tasks: [], tasksLoaded: false });
});

/** Lets the stand-in daemon's answers arrive and React render them. */
const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

/** The list as the dashboard last read it, with the toasts the commands show. */
async function open(tasks: TaskSummary[]) {
  patchStateForTests({ tasks, tasksLoaded: true });
  onView = vi.fn();
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <>
        <TasksScreen view={{ kind: "list" }} onView={onView} onOpenExecution={() => {}} />
        <Toasts />
      </>,
    ),
  );
}

const rows = () => [...host!.querySelectorAll<HTMLElement>("article.task-row")];
const row = (id: string) => rows().find((r) => r.querySelector(".task-row__title .mono")?.textContent === id);
const toggle = (id: string) => row(id)!.querySelector<HTMLInputElement>("label.toggle input")!;
const toggleLabel = (id: string) => row(id)!.querySelector(".toggle__label")?.textContent;
const button = (id: string, label: string) =>
  [...row(id)!.querySelectorAll<HTMLButtonElement>("button")].find((b) => (b.getAttribute("aria-label") ?? b.textContent) === label)!;
const toasts = () =>
  [...host!.querySelectorAll(".toast")].map((t) => ({
    tone: [...t.classList].find((c) => c.startsWith("toast--"))?.slice("toast--".length),
    text: t.textContent,
  }));

async function click(target: HTMLElement) {
  await act(async () => target.click());
  await settle();
}

describe("the Tasks screen's toggle", () => {
  it("disables a task, reads the list again to show it disabled, and enables it", async () => {
    answer("POST", "/tasks/standup/disable", { ...standup, enabled: false });
    // The list keeps the next firing; the row hides it for a disabled task.
    answer("GET", "/tasks", [{ ...standup, enabled: false, revision: "r2" }]);
    await open([standup]);
    expect(toggleLabel("standup")).toBe("Enabled");
    expect(row("standup")!.textContent).toContain("Next firing");
    expect(button("standup", "Run now").disabled).toBe(false);

    await click(toggle("standup"));
    expect(daemon.requests).toEqual([req("POST", "/tasks/standup/disable"), req("GET", "/tasks")]);
    expect(toggle("standup").checked).toBe(false);
    expect(toggleLabel("standup")).toBe("Disabled");
    expect(row("standup")!.className).toContain("task-row--disabled");
    expect(button("standup", "Run now").disabled).toBe(true);
    expect(row("standup")!.textContent).not.toContain("Next firing");

    daemon.requests.length = 0;
    answer("POST", "/tasks/standup/enable", standup);
    answer("GET", "/tasks", [{ ...standup, revision: "r3" }]);
    await click(toggle("standup"));
    expect(daemon.requests).toEqual([req("POST", "/tasks/standup/enable"), req("GET", "/tasks")]);
    expect(toggle("standup").checked).toBe(true);
    expect(toggleLabel("standup")).toBe("Enabled");
    expect(button("standup", "Run now").disabled).toBe(false);
    expect(toasts()).toEqual([]);
  });

  it("shows the daemon's refusal as an error toast, and keeps the state the list reports", async () => {
    answer("POST", "/tasks/standup/enable", { error: { code: "validation_failed", message: "/spec/schedule: the schedule sets both cron and interval" } }, 422);
    await open([{ ...standup, enabled: false }]);

    await click(toggle("standup"));
    expect(daemon.requests).toEqual([req("POST", "/tasks/standup/enable")]);
    expect(toasts()).toEqual([{ tone: "danger", text: "/spec/schedule: the schedule sets both cron and interval" }]);
    expect(toggle("standup").checked).toBe(false);
    expect(toggleLabel("standup")).toBe("Disabled");
    expect(button("standup", "Run now").disabled).toBe(true);
  });
});

describe("the Tasks screen's Duplicate", () => {
  it("sends one duplicate and opens the editor of the identifier the answer returns", async () => {
    answer("POST", "/tasks/standup/duplicate", { ...copy, enabled: false });
    answer("GET", "/tasks", [standup, { ...copy, enabled: false }]);
    await open([standup]);

    await click(button("standup", "Duplicate"));
    expect(daemon.requests).toEqual([req("POST", "/tasks/standup/duplicate"), req("GET", "/tasks")]);
    expect(onView.mock.calls).toEqual([[{ kind: "edit", id: "standup-copy" }]]);
  });
});

describe("the Tasks screen's Export", () => {
  const yaml = 'apiVersion: harmflow/v1alpha1\nkind: Task\nmetadata:\n  id: standup\n  name: Standup notes # the team\'s\nspec:\n  prompt: "Écris les notes."\n';

  it("saves the exported text unchanged as <id>.yaml, and says so once the file is saved", async () => {
    answer("GET", "/tasks/standup/export", yaml);
    const save = vi.spyOn(native, "saveTextFile").mockResolvedValue(true);
    await open([standup]);

    await click(button("standup", "Export"));
    expect(daemon.requests).toEqual([req("GET", "/tasks/standup/export")]);
    expect(save.mock.calls).toEqual([["standup.yaml", yaml]]);
    expect(toasts()).toEqual([{ tone: "success", text: "Task exported." }]);
  });

  it("says nothing when the save dialog is cancelled", async () => {
    answer("GET", "/tasks/standup/export", yaml);
    const save = vi.spyOn(native, "saveTextFile").mockResolvedValue(false);
    await open([standup]);

    await click(button("standup", "Export"));
    expect(save.mock.calls).toEqual([["standup.yaml", yaml]]);
    expect(toasts()).toEqual([]);
  });
});

describe("the Tasks screen's Delete", () => {
  const question = "The task's YAML file is removed. Its execution records stay in history.";
  const waits = "The task is running; it will be deleted once the execution finishes.";

  it("asks first, naming the task, and sends nothing when declined", async () => {
    const confirm = vi.spyOn(native, "confirm").mockResolvedValue(false);
    await open([standup]);

    await click(button("standup", "Delete"));
    expect(confirm.mock.calls).toEqual([[question, "Delete Standup notes?", "Delete", "Cancel"]]);
    expect(daemon.requests).toEqual([]);
    expect(row("standup")).toBeDefined();
    expect(toasts()).toEqual([]);
  });

  it("sends one delete once confirmed, says the task was deleted, and reads the list again", async () => {
    answer("DELETE", "/tasks/standup", null, 204);
    answer("GET", "/tasks", []);
    vi.spyOn(native, "confirm").mockResolvedValue(true);
    await open([standup]);

    await click(button("standup", "Delete"));
    expect(daemon.requests).toEqual([req("DELETE", "/tasks/standup"), req("GET", "/tasks")]);
    expect(toasts()).toEqual([{ tone: "success", text: "Task deleted." }]);
    expect(row("standup")).toBeUndefined();
  });

  it("says, for a running task, that the deletion waits for its execution, and shows a 202 as waiting", async () => {
    const running = { ...standup, running: true };
    answer("DELETE", "/tasks/standup", { ok: true }, 202);
    answer("GET", "/tasks", [{ ...running, deletePending: true }]);
    const confirm = vi.spyOn(native, "confirm").mockResolvedValue(true);
    await open([running]);

    await click(button("standup", "Delete"));
    expect(confirm.mock.calls).toEqual([[`${question}\n\n${waits}`, "Delete Standup notes?", "Delete", "Cancel"]]);
    expect(daemon.requests).toEqual([req("DELETE", "/tasks/standup"), req("GET", "/tasks")]);
    expect(toasts()).toEqual([{ tone: "warning", text: waits }]);
    expect(row("standup")!.textContent).toContain("Deleted when the running execution ends");
  });
});

describe("the Tasks screen's commands on two tasks", () => {
  // The screen's parent decides what shows; this one keeps the list, so that the three commands run on one screen.
  // The daemon saves a copy disabled, which app › TestDuplicate holds: this stand-in reports it enabled, so that the
  // copy's toggle disables it.
  it("duplicates a task, disables the copy, and deletes the original, each by its own identifier", async () => {
    answer("POST", "/tasks/standup/duplicate", copy);
    answer("GET", "/tasks", [standup, copy]);
    vi.spyOn(native, "confirm").mockResolvedValue(true);
    await open([standup]);

    await click(button("standup", "Duplicate"));
    expect(onView.mock.calls).toEqual([[{ kind: "edit", id: "standup-copy" }]]);
    expect(rows()).toHaveLength(2);

    answer("POST", "/tasks/standup-copy/disable", { ...copy, enabled: false });
    answer("GET", "/tasks", [standup, { ...copy, enabled: false }]);
    await click(toggle("standup-copy"));
    expect(toggleLabel("standup")).toBe("Enabled");
    expect(toggleLabel("standup-copy")).toBe("Disabled");

    answer("DELETE", "/tasks/standup", null, 204);
    answer("GET", "/tasks", [{ ...copy, enabled: false }]);
    await click(button("standup", "Delete"));

    expect(daemon.requests.filter((r) => r.method !== "GET")).toEqual([
      req("POST", "/tasks/standup/duplicate"),
      req("POST", "/tasks/standup-copy/disable"),
      req("DELETE", "/tasks/standup"),
    ]);
    expect(rows().map((r) => r.querySelector(".task-row__title .mono")?.textContent)).toEqual(["standup-copy"]);
    expect(toggleLabel("standup-copy")).toBe("Disabled");
  });
});
