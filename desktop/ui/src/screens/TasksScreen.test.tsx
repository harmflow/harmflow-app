// @vitest-environment happy-dom
// Drives the Tasks screen's commands on a task against a stand-in for the daemon's local API: each one sends its
// request, and the screen shows the answer. What the daemon does with each request is itest › TestTaskLifecycle's,
// and app › TestDuplicate's and TestDisableAndDeleteWhileRunning's. After the task's file changes outside HarmFlow, the
// list and the editor opened next show the file's definition, and a save builds on its revision (harmflow-app#304):
// that the daemon answers with the file as it is then is itest › TestTaskLifecycle's and TestFormDocumentIsTheFile's,
// and app › TestExternalEdits'. Each row shows its task type's glyph and tone (harmflow-app#471).
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { native } from "../api/native";
import type { StreamHandlers } from "../api/transport";
import type { HarmflowEvent, Task, TaskSummary } from "../api/types";
import { Toasts } from "../components/toast";
import i18n from "../i18n";
import { patchStateForTests, startSync } from "../state/store";
import { TasksScreen, type TaskView } from "./TasksScreen";

// The daemon as the client sees it: an answer per method and route, every request it received with its body, and the
// stream it feeds.
const daemon = vi.hoisted(() => ({
  answers: new Map<string, { status: number; body: unknown }>(),
  requests: [] as { method: string; path: string; body: unknown }[],
  stream: undefined as StreamHandlers | undefined,
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
    stream: (handlers: StreamHandlers) => {
      daemon.stream = handlers;
      return () => {};
    },
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
  patchStateForTests({ tasks: [], tasksLoaded: false, externalChanges: {} });
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

describe("the Tasks screen's rows", () => {
  it("show each task type's glyph and tone, the New task dialog's, and one fallback for a type it does not know", async () => {
    const typed = (type: string): TaskSummary => ({ ...standup, id: `${type}-task`, name: `A ${type} task`, type });
    await open(["github", "general", "script", "webhook"].map(typed));
    const glyph = (id: string) => {
      const span = row(id)!.querySelector<HTMLElement>(".task-row__glyph")!;
      return {
        icon: span.querySelector("svg")!.dataset.icon,
        tones: [...span.classList].filter((c) => c.startsWith("type-glyph--")),
      };
    };
    expect(glyph("github-task")).toEqual({ icon: "code", tones: ["type-glyph--blue"] });
    expect(glyph("general-task")).toEqual({ icon: "sparkle", tones: ["type-glyph--violet"] });
    expect(glyph("script-task")).toEqual({ icon: "terminal", tones: ["type-glyph--blue"] });
    expect(glyph("webhook-task")).toEqual({ icon: "dot", tones: ["type-glyph--blue"] });
  });
});

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

describe("the Tasks screen after the task's file changes outside HarmFlow", () => {
  // The file as the dashboard first reads it, and as a text editor leaves it: another name and prompt, a comment, and
  // the prompt moved above the schedule.
  const before = [
    "apiVersion: harmflow/v1alpha1",
    "kind: Task",
    "metadata:",
    "  id: standup",
    "  name: Standup notes",
    "spec:",
    "  type: general",
    "  schedule:",
    '    cron: "0 9 * * 1-5"',
    "  prompt: Write the standup notes.",
    "",
  ].join("\n");
  const after = [
    "apiVersion: harmflow/v1alpha1",
    "kind: Task",
    "metadata:",
    "  id: standup",
    "  name: Standup digest",
    "spec:",
    "  type: general",
    "  # Read out at the start of the call.",
    "  prompt: Write the standup digest.",
    "  schedule:",
    '    cron: "0 9 * * 1-5"',
    "",
  ].join("\n");

  const documentOf = (name: string, prompt: string) => ({
    apiVersion: "harmflow/v1alpha1",
    kind: "Task",
    metadata: { id: "standup", name },
    spec: { type: "general", schedule: { cron: "0 9 * * 1-5" }, prompt },
  });
  /** The task as `GET /tasks/standup` answers it: its file's text, and the document parsed from it. */
  const stored = (revision: string, name: string, prompt: string, yaml: string): Task => ({
    ...standup,
    name,
    revision,
    document: documentOf(name, prompt),
    effective: documentOf(name, prompt),
    yaml,
    path: "standup.yaml",
    validation: { valid: true, errors: [], warnings: [] },
  });

  /** The event the daemon's watcher emits once it loaded the changed file. */
  const changed: HarmflowEvent = {
    id: 41,
    schemaVersion: "1",
    timestamp: "2026-10-29T09:00:00Z",
    taskId: "standup",
    type: "task.changed_externally",
    severity: "info",
    message: "the task file was changed outside HarmFlow",
    messageKey: "task_changed_externally",
    payload: { revision: "r2" },
  };

  /** The screen as the app shows it: the view a command asks for is the one shown next. */
  function Screen() {
    const [view, setView] = useState<TaskView>({ kind: "list" });
    return <TasksScreen view={view} onView={setView} onOpenExecution={() => {}} />;
  }

  // The store's sync starts timers of its own, which a fake clock keeps inside this test.
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  });

  const name = (id: string) => row(id)!.querySelector(".task-row__title strong")!.textContent;
  const field = (label: string) =>
    [...host!.querySelectorAll<HTMLElement>("label.field")].find((f) => f.querySelector(".field__label")?.textContent === label)!;
  const editorButton = (label: string) =>
    [...host!.querySelectorAll<HTMLButtonElement>(".editor__actions button")].find((b) => b.textContent === label)!;

  /** Types into a text field as React sees it. */
  async function type(input: HTMLInputElement, value: string) {
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  // The store's sync starts once per module, as the app starts it once: this is the one test of this file that starts it.
  it("reads the list again, opens the editor on the file's definition, and saves on the file's revision", async () => {
    answer("GET", "/tasks", [standup]);
    answer("GET", "/tasks/standup", stored("r1", "Standup notes", "Write the standup notes.", before));
    answer("POST", "/schedules/preview", { occurrences: [] });
    startSync();
    await settle();
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () =>
      root!.render(
        <>
          <Screen />
          <Toasts />
        </>,
      ),
    );
    expect(name("standup")).toBe("Standup notes");

    // The dashboard reads the task at r1 in its editor, and leaves it before the file changes.
    await click(button("standup", "Edit"));
    expect(field("Name").querySelector("input")!.value).toBe("Standup notes");
    await click(editorButton("Close"));
    expect(rows()).toHaveLength(1);

    // The change: the stand-in answers with the file at r2, and the stream delivers the daemon's event.
    answer("GET", "/tasks", [{ ...standup, name: "Standup digest", revision: "r2" }]);
    answer("GET", "/tasks/standup", stored("r2", "Standup digest", "Write the standup digest.", after));
    daemon.requests.length = 0;
    act(() => daemon.stream!.onEvent(changed));

    // The list: read again once the store's debounce ends, and its row shows the new name.
    await act(async () => vi.advanceTimersByTime(400));
    await settle();
    expect(daemon.requests).toEqual([req("GET", "/tasks")]);
    expect(name("standup")).toBe("Standup digest");

    // The editor opened afterwards: it reads the task again, and its form shows the file's values.
    await click(button("standup", "Edit"));
    expect(daemon.requests.filter((r) => r.path === "/tasks/standup")).toEqual([req("GET", "/tasks/standup")]);
    expect(field("Name").querySelector("input")!.value).toBe("Standup digest");
    expect(field("Prompt").querySelector("textarea")!.value).toBe("Write the standup digest.");

    // The next save: one update, on the revision the file had when the editor read it.
    answer("PUT", "/tasks/standup", stored("r3", "Standup summary", "Write the standup digest.", after.replace("Standup digest", "Standup summary")));
    await type(field("Name").querySelector("input")!, "Standup summary");
    await click(editorButton("Save"));
    expect(daemon.requests.filter((r) => r.method === "PUT")).toEqual([
      req("PUT", "/tasks/standup", { document: documentOf("Standup summary", "Write the standup digest."), baseRevision: "r2" }),
    ]);
    expect(toasts()).toEqual([{ tone: "success", text: "Saved." }]);
  });
});
