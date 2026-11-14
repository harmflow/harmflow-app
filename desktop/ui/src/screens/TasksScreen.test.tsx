// @vitest-environment happy-dom
// Drives the Tasks screen's commands on a task against a stand-in for the daemon's local API: each one sends its
// request, and the screen shows the answer. What the daemon does with each request is itest › TestTaskLifecycle's,
// and app › TestDuplicate's and TestDisableAndDeleteWhileRunning's. After the task's file changes outside HarmFlow, the
// list and the editor opened next show the file's definition, its YAML tab the file's text byte for byte
// (harmflow-app#461), and a save builds on its revision (harmflow-app#304):
// that the daemon answers with the file as it is then is itest › TestTaskLifecycle's and TestFormDocumentIsTheFile's,
// and app › TestExternalEdits'. Each row shows its task type's glyph and tone (harmflow-app#471). A script task that
// declares only other systems shows the daemon's warning on its row, in the interface's language, and Run now shows
// the daemon's refusal (harmflow-app#559): that the daemon warns and refuses is app › TestScriptDoesNotRunOnAnUndeclaredSystem's.
// The user's groups and order (harmflow-app#346) are the desktop's own, kept in localStorage: no request carries them.
// Save as template sends the name, description, and identifier the dialog asks for, and the New task dialog lists the
// user's templates after HarmFlow's (harmflow-app#179): what the daemon writes is app › TestSaveTaskAsTemplate's.
import { EditorView } from "@codemirror/view";
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
let onOpenExecution = vi.fn<(id: string) => void>();

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

// A toast stays 3.5 seconds in a list the module keeps; a fake clock for its timeout ends each test's toasts with the
// test, so that none shows in the next.
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  localStorage.clear();
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
  onOpenExecution = vi.fn();
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <>
        <TasksScreen view={{ kind: "list" }} onView={onView} onOpenExecution={onOpenExecution} />
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

describe("the Tasks screen on a script task declared for another system", () => {
  // The daemon's text on macOS for a script task that declares only Windows: the task's warning and Run now's refusal.
  const notHere = {
    message: "this task runs only on windows, not on macos",
    messageKey: "script_system_not_declared",
    params: { systems: "windows", system: "macos" },
  } as const;
  /** The script tasks as the daemon lists them on macOS: only the one that does not declare macOS has a warning. */
  const script = (id: string, warnings: TaskSummary["warnings"]): TaskSummary => ({
    id,
    name: `Audit (${id})`,
    type: "script",
    enabled: true,
    revision: "r1",
    harness: "",
    schedule: "1h",
    warnings,
  });
  const windowsOnly = script("windows-only", [{ code: "other", subject: "/spec/script/systems", ...notHere }]);
  const macosToo = script("macos-too", []);
  const anySystem = script("any-system", []);

  // The interface's language, the daemon's text translated from its key and parameters; the daemon's English stays
  // only in en-US.
  const languages = [
    { language: "en-US", count: "1 warning", text: "this task runs only on windows, not on macos", label: "Warning" },
    { language: "pt-BR", count: "1 aviso", text: "esta tarefa só executa em windows, não em macos", label: "Aviso" },
  ];

  afterEach(async () => {
    await act(async () => void (await i18n.changeLanguage("en-US")));
  });

  const chips = (id: string) => [...row(id)!.querySelectorAll<HTMLElement>(".task-row__title .chip")];

  it.each(languages)("shows in $language the row's warning chip, whose title names the declared systems and this one", async ({ language, count, text, label }) => {
    await act(async () => void (await i18n.changeLanguage(language)));
    await open([windowsOnly, macosToo, anySystem]);

    expect(chips("windows-only").map((c) => ({ text: c.textContent, title: c.title, warning: c.classList.contains("chip--warning") }))).toEqual([
      { text: count, title: `${label}: ${text}`, warning: true },
    ]);
    // A task that declares this system, or none, has no warning from the daemon, and its row shows none.
    expect(chips("macos-too")).toEqual([]);
    expect(chips("any-system")).toEqual([]);
  });

  it.each(languages)("shows in $language Run now's refusal, naming the declared systems and this one, and opens no execution", async ({ language, text }) => {
    answer("POST", "/tasks/windows-only/run", { error: { code: "conflict", ...notHere } }, 409);
    await act(async () => void (await i18n.changeLanguage(language)));
    await open([windowsOnly]);

    await click(button("windows-only", i18n.t("common.runNow")));
    expect(daemon.requests).toEqual([req("POST", "/tasks/windows-only/run")]);
    expect(toasts()).toEqual([{ tone: "danger", text }]);
    expect(onOpenExecution).not.toHaveBeenCalled();
    expect(onView).not.toHaveBeenCalled();
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

describe("the Tasks screen's Save as template", () => {
  /** Types into a text field or area as React sees it. */
  async function type(field: HTMLInputElement | HTMLTextAreaElement, value: string) {
    const proto = field instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    await act(async () => {
      Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(field, value);
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }
  const dialog = () => host!.querySelector<HTMLElement>(".modal")!;
  const fields = () => [...dialog().querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("input, textarea")];
  const save = () => [...dialog().querySelectorAll<HTMLButtonElement>(".modal__footer button")].find((b) => b.textContent === "Save as template")!;

  it("asks for a name and a description, proposes an identifier from the name, and sends the three", async () => {
    answer("POST", "/tasks/standup/template", { id: "weekly-standup", source: "user", path: "/t/weekly-standup.yaml", validation: { valid: true, errors: [], warnings: [] } }, 201);
    answer("GET", "/templates", []);
    await open([standup]);

    await click(button("standup", "Save as template"));
    const [name, id, description] = fields();
    expect([name.value, id.value, description.value]).toEqual(["Standup notes", "standup-notes", ""]);
    await type(name, "Weekly standup");
    expect(id.value).toBe("weekly-standup");
    expect(save().disabled).toBe(true);
    await type(description, "Sums up the week's standups.");
    expect(save().disabled).toBe(false);

    await click(save());
    expect(daemon.requests).toEqual([
      req("POST", "/tasks/standup/template", { id: "weekly-standup", name: "Weekly standup", description: "Sums up the week's standups." }),
      req("GET", "/templates"),
    ]);
    expect(host!.querySelector(".modal")).toBeNull();
    expect(toasts()).toEqual([{ tone: "success", text: "Template Weekly standup saved" }]);
  });

  it("keeps an identifier the user wrote, and shows the daemon's refusal in the dialog, which stays open", async () => {
    answer(
      "POST",
      "/tasks/standup/template",
      { error: { code: "conflict", message: "a template with the identifier mine already exists", messageKey: "template_exists", params: { template: "mine" } } },
      409,
    );
    await open([standup]);

    await click(button("standup", "Save as template"));
    const [name, id, description] = fields();
    await type(id, "mine");
    await type(name, "Another name");
    await type(description, "Notes.");
    expect(id.value).toBe("mine");
    await click(save());
    expect(daemon.requests).toEqual([req("POST", "/tasks/standup/template", { id: "mine", name: "Another name", description: "Notes." })]);
    expect(dialog().querySelector(".banner")?.textContent).toContain("a template with the identifier mine already exists");
  });

  it("is offered on no task whose file fails validation", async () => {
    await open([{ ...standup, invalid: true }]);
    expect(button("standup", "Save as template").disabled).toBe(true);
  });
});

describe("the New task dialog's templates", () => {
  const valid = { valid: true, errors: [], warnings: [] };
  const named = (text: string) => ({ "pt-BR": text, "en-US": text, "es-ES": text });

  afterEach(() => patchStateForTests({ templates: undefined }));

  it("lists the user's templates after HarmFlow's, marked as the user's, named as the user wrote them", async () => {
    patchStateForTests({
      templates: [
        { id: "hf-general", source: "builtin", path: "/b/hf-general.yaml", type: "general", name: { "pt-BR": "Tarefa geral", "en-US": "General task", "es-ES": "Tarea general" }, description: named("Runs a prompt."), validation: valid },
        { id: "notas", source: "user", path: "/t/notas.yaml", type: "general", name: named("Notas semanais"), description: named("Resume a semana."), validation: valid },
      ],
    });
    await open([standup]);
    await click([...host!.querySelectorAll<HTMLButtonElement>(".filters button")].find((b) => b.textContent === "New task")!);
    const offered = [...host!.querySelectorAll<HTMLElement>(".modal .template")].map((b) => ({
      name: b.querySelector("strong")?.textContent,
      yours: b.querySelector(".chip")?.textContent ?? null,
    }));
    expect(offered).toEqual([
      { name: "General task", yours: null },
      { name: "Notas semanais", yours: "Yours" },
    ]);
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

  it("shows the bridge's message when the file cannot be written (harmflow-app#725)", async () => {
    answer("GET", "/tasks/standup/export", yaml);
    // The shell answers a failed command with its message alone, not an Error.
    vi.spyOn(native, "saveTextFile").mockRejectedValue("permission denied: /Volumes/locked/standup.yaml");
    await open([standup]);

    await click(button("standup", "Export"));
    expect(toasts()).toEqual([{ tone: "danger", text: "permission denied: /Volumes/locked/standup.yaml" }]);
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

describe("the Tasks screen's groups", () => {
  // The user's arrangement is visual only: the desktop keeps it, and no request reaches the daemon for it.
  const task = (id: string): TaskSummary => ({ ...standup, id, name: `Task ${id}` });
  const [a, b, c, d] = ["a", "b", "c", "d"].map(task);
  /** Reports holds a and b, Chores holds c, and d is in no group, as a past session left them. */
  const kept = { groups: [{ id: "g1", name: "Reports", collapsed: false, tasks: ["a", "b"] }, { id: "g2", name: "Chores", collapsed: false, tasks: ["c"] }], ungrouped: ["d"] };
  const keep = (layout: unknown) => localStorage.setItem("harmflow.taskLayout", JSON.stringify(layout));
  const stored = () => JSON.parse(localStorage.getItem("harmflow.taskLayout")!);

  const idOf = (r: Element) => r.querySelector(".task-row__title .mono")!.textContent;
  /** Each group's heading and its rows' task IDs, in the screen's order; one list with no heading when there's no group. */
  const arrangement = () => {
    const sections = [...host!.querySelectorAll<HTMLElement>(".task-group")];
    if (!sections.length) return [[null, ...rows().map(idOf)]];
    return sections.map((s) => [s.querySelector(".task-group__name")?.textContent ?? null, ...[...s.querySelectorAll("article.task-row")].map(idOf)]);
  };
  const section = (name: string) => [...host!.querySelectorAll<HTMLElement>(".task-group")].find((s) => s.querySelector(".task-group__name")?.textContent === name)!;
  const inSection = (name: string, label: string) => [...section(name).querySelectorAll<HTMLButtonElement>(".task-group__head button")].find((x) => x.getAttribute("aria-label") === label)!;
  const handle = (id: string) => row(id)!.querySelector<HTMLButtonElement>(".drag-handle")!;
  const menu = () => [...host!.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].map((m) => ({ label: m.textContent, disabled: m.disabled }));
  const choose = (label: string) => click([...host!.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((m) => m.textContent === label)!);
  const status = () => host!.querySelector('[role="status"]')!.textContent;
  const screenButton = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>(".filters button")].find((x) => x.textContent === label)!;
  const key = (target: EventTarget, k: string) => act(async () => void target.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));
  async function type(input: HTMLInputElement, value: string) {
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }
  async function remount(tasks: TaskSummary[]) {
    act(() => root!.unmount());
    host!.remove();
    await open(tasks);
  }

  it("shows the daemon's list as before, with no heading and no handle, when there's no group", async () => {
    await open([a, b, c]);
    expect(arrangement()).toEqual([[null, "a", "b", "c"]]);
    expect(host!.querySelectorAll(".drag-handle")).toHaveLength(0);
    expect(localStorage.getItem("harmflow.taskLayout")).toBeNull();
  });

  it("creates and names a group, moves a task into it from its handle's menu, announces it, and keeps it across a remount", async () => {
    await open([a, b, c]);
    await click(screenButton("New group"));
    const name = host!.querySelector<HTMLInputElement>('input[aria-label="Group name"]')!;
    expect(name.value).toBe("New group");
    expect(document.activeElement).toBe(name);
    await type(name, "Reports");
    await key(name, "Enter");
    expect(arrangement()).toEqual([["Reports"], ["Ungrouped", "a", "b", "c"]]);
    expect(section("Reports").querySelector(".task-group__count")!.textContent).toBe("0 tasks");
    expect(section("Reports").textContent).toContain("Drop a task here");

    await click(handle("b"));
    expect(handle("b").getAttribute("aria-expanded")).toBe("true");
    expect(menu()).toEqual([
      { label: "Move up", disabled: false },
      { label: "Move down", disabled: false },
      { label: "Move to Reports", disabled: false },
    ]);
    await choose("Move to Reports");
    expect(arrangement()).toEqual([["Reports", "b"], ["Ungrouped", "a", "c"]]);
    expect(section("Reports").querySelector(".task-group__count")!.textContent).toBe("1 task");
    expect(status()).toBe("Task b moved to Reports, position 1 of 1.");
    expect(document.activeElement).toBe(handle("b"));
    expect(menu()).toEqual([]);

    await remount([a, b, c]);
    expect(arrangement()).toEqual([["Reports", "b"], ["Ungrouped", "a", "c"]]);
    expect(daemon.requests).toEqual([]);
  });

  it("moves a task up and down in its group, and to Ungrouped, and a group among the groups", async () => {
    keep(kept);
    await open([a, b, c, d]);
    expect(arrangement()).toEqual([["Reports", "a", "b"], ["Chores", "c"], ["Ungrouped", "d"]]);

    await click(handle("a"));
    expect(menu()).toEqual([
      { label: "Move up", disabled: true },
      { label: "Move down", disabled: false },
      { label: "Move to Chores", disabled: false },
      { label: "Move to Ungrouped", disabled: false },
    ]);
    await choose("Move down");
    expect(arrangement()).toEqual([["Reports", "b", "a"], ["Chores", "c"], ["Ungrouped", "d"]]);
    expect(status()).toBe("Task a moved to Reports, position 2 of 2.");

    await click(handle("b"));
    await choose("Move to Ungrouped");
    expect(arrangement()).toEqual([["Reports", "a"], ["Chores", "c"], ["Ungrouped", "d", "b"]]);

    await click(inSection("Chores", "Move group Chores"));
    expect(menu()).toEqual([
      { label: "Move up", disabled: false },
      { label: "Move down", disabled: true },
    ]);
    await choose("Move up");
    expect(arrangement()).toEqual([["Chores", "c"], ["Reports", "a"], ["Ungrouped", "d", "b"]]);
    expect(status()).toBe("Group Chores moved to position 1 of 2.");
    expect(stored().groups.map((g: { name: string }) => g.name)).toEqual(["Chores", "Reports"]);
  });

  it("closes a handle's menu with Escape, moving nothing, and gives the focus back to the handle", async () => {
    keep(kept);
    await open([a, b, c, d]);
    await click(handle("a"));
    expect(document.activeElement?.textContent).toBe("Move down");
    await key(document.activeElement!, "Escape");
    expect(menu()).toEqual([]);
    expect(document.activeElement).toBe(handle("a"));
    expect(arrangement()).toEqual([["Reports", "a", "b"], ["Chores", "c"], ["Ungrouped", "d"]]);
  });

  it("collapses and expands a group, keeping it collapsed across a remount, and deletes one without deleting a task", async () => {
    keep(kept);
    await open([a, b, c, d]);
    await click(inSection("Reports", "Collapse group"));
    expect(arrangement()).toEqual([["Reports"], ["Chores", "c"], ["Ungrouped", "d"]]);
    expect(section("Reports").querySelector(".task-group__count")!.textContent).toBe("2 tasks");
    await remount([a, b, c, d]);
    expect(arrangement()).toEqual([["Reports"], ["Chores", "c"], ["Ungrouped", "d"]]);
    await click(inSection("Reports", "Expand group"));
    expect(arrangement()).toEqual([["Reports", "a", "b"], ["Chores", "c"], ["Ungrouped", "d"]]);

    const confirm = vi.spyOn(native, "confirm");
    await click(inSection("Reports", "Delete group"));
    expect(arrangement()).toEqual([["Chores", "c"], ["Ungrouped", "d", "a", "b"]]);
    expect(confirm).not.toHaveBeenCalled();
    expect(daemon.requests).toEqual([]);
  });

  it("renames a group, and refuses a name another group has, ignoring case", async () => {
    keep(kept);
    await open([a, b, c, d]);
    await click(inSection("Chores", "Rename group"));
    const name = host!.querySelector<HTMLInputElement>('input[aria-label="Group name"]')!;
    await type(name, "reports");
    await key(name, "Enter");
    expect(toasts()).toEqual([{ tone: "danger", text: "A group named reports already exists." }]);
    expect(arrangement()).toEqual([["Reports", "a", "b"], ["Chores", "c"], ["Ungrouped", "d"]]);

    await click(inSection("Chores", "Rename group"));
    await type(host!.querySelector<HTMLInputElement>('input[aria-label="Group name"]')!, "Home");
    await key(host!.querySelector('input[aria-label="Group name"]')!, "Enter");
    expect(arrangement()).toEqual([["Reports", "a", "b"], ["Home", "c"], ["Ungrouped", "d"]]);
  });

  it("keeps the arrangement when the list is read again, drops a task deleted outside, and puts a new task at the end of Ungrouped", async () => {
    keep(kept);
    await open([a, b, c, d]);
    // The daemon's list after another program deleted c's file and added a task whose ID sorts first.
    await act(async () => patchStateForTests({ tasks: [task("0-new"), a, b, d] }));
    expect(arrangement()).toEqual([["Reports", "a", "b"], ["Chores"], ["Ungrouped", "d", "0-new"]]);
    expect(stored()).toEqual({ ...kept, groups: [kept.groups[0], { ...kept.groups[1], tasks: [] }], ungrouped: ["d", "0-new"] });
  });

  it("drops a task deleted in the dashboard from the arrangement", async () => {
    keep(kept);
    answer("DELETE", "/tasks/a", null, 204);
    answer("GET", "/tasks", [b, c, d]);
    vi.spyOn(native, "confirm").mockResolvedValue(true);
    await open([a, b, c, d]);
    await click(button("a", "Delete"));
    expect(arrangement()).toEqual([["Reports", "b"], ["Chores", "c"], ["Ungrouped", "d"]]);
    expect(stored().groups[0].tasks).toEqual(["b"]);
  });

  it("puts a duplicate right after its original, in the same group", async () => {
    keep(kept);
    const copyOfA = task("a-copy");
    answer("POST", "/tasks/a/duplicate", copyOfA);
    answer("GET", "/tasks", [a, copyOfA, b, c, d]);
    await open([a, b, c, d]);
    await click(button("a", "Duplicate"));
    expect(arrangement()).toEqual([["Reports", "a", "a-copy", "b"], ["Chores", "c"], ["Ungrouped", "d"]]);
  });

  it("hides the handles and the groups with no task shown while searching or filtering", async () => {
    keep(kept);
    await open([a, b, { ...c, enabled: false }, d]);
    await type(host!.querySelector<HTMLInputElement>(".filters__search")!, "Task c");
    expect(arrangement()).toEqual([["Chores", "c"]]);
    expect(host!.querySelectorAll(".drag-handle")).toHaveLength(0);

    await type(host!.querySelector<HTMLInputElement>(".filters__search")!, "");
    expect(host!.querySelectorAll(".drag-handle")).toHaveLength(6);
    const select = host!.querySelector<HTMLSelectElement>(".filters select")!;
    await act(async () => {
      select.value = "enabled";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(arrangement()).toEqual([["Reports", "a", "b"], ["Ungrouped", "d"]]);
    expect(host!.querySelectorAll(".drag-handle")).toHaveLength(0);
  });

  it("ignores an arrangement it can't read, and shows the daemon's list", async () => {
    localStorage.setItem("harmflow.taskLayout", "{groups");
    await open([a, b, c]);
    expect(arrangement()).toEqual([[null, "a", "b", "c"]]);
  });

  it("drags a task by its handle to where it is dropped, and opens no editor and no menu", async () => {
    keep(kept);
    await open([a, b, c, d]);
    // The pointer is released over the lower half of c's row: a lands after c.
    vi.spyOn(document, "elementFromPoint").mockImplementation(() => row("c")!.querySelector(".task-row__main"));
    const pointer = (type: string, target: EventTarget, y: number) =>
      act(async () => void target.dispatchEvent(new PointerEvent(type, { bubbles: true, button: 0, clientX: 10, clientY: y })));
    await pointer("pointerdown", handle("a"), 0);
    await pointer("pointermove", window, 30);
    expect(row("c")!.className).toContain("task-row--drop-after");
    await pointer("pointerup", window, 30);
    await click(handle("a"));
    expect(arrangement()).toEqual([["Reports", "b"], ["Chores", "c", "a"], ["Ungrouped", "d"]]);
    expect(status()).toBe("Task a moved to Chores, position 2 of 2.");
    expect(menu()).toEqual([]);
    expect(onView).not.toHaveBeenCalled();
  });

  it("cancels a drag with Escape", async () => {
    keep(kept);
    await open([a, b, c, d]);
    vi.spyOn(document, "elementFromPoint").mockImplementation(() => row("d")!);
    const pointer = (type: string, target: EventTarget, y: number) =>
      act(async () => void target.dispatchEvent(new PointerEvent(type, { bubbles: true, button: 0, clientX: 10, clientY: y })));
    await pointer("pointerdown", handle("a"), 0);
    await pointer("pointermove", window, 40);
    expect(row("d")!.className).toContain("task-row--drop-after");
    await key(window, "Escape");
    expect(host!.querySelector(".task-row--drop-after, .task-row--drop-before")).toBeNull();
    await pointer("pointerup", window, 40);
    expect(arrangement()).toEqual([["Reports", "a", "b"], ["Chores", "c"], ["Ungrouped", "d"]]);
    expect(status()).toBe("Move cancelled.");
  });

  it("leaves a task where it was when it is released over its own row", async () => {
    keep(kept);
    await open([a, b, c, d]);
    // The drag passes over b's row, then the user brings it back over a's own row and releases it there.
    let under: Element | null = row("b")!;
    vi.spyOn(document, "elementFromPoint").mockImplementation(() => under);
    const pointer = (type: string, target: EventTarget, y: number) =>
      act(async () => void target.dispatchEvent(new PointerEvent(type, { bubbles: true, button: 0, clientX: 10, clientY: y })));
    await pointer("pointerdown", handle("a"), 0);
    await pointer("pointermove", window, 30);
    expect(row("b")!.className).toContain("task-row--drop-after");
    under = row("a")!.querySelector(".task-row__main");
    await pointer("pointermove", window, 5);
    expect(host!.querySelector(".task-row--drop-after, .task-row--drop-before, .task-group--drop")).toBeNull();
    await pointer("pointerup", window, 5);
    await click(handle("a"));
    expect(arrangement()).toEqual([["Reports", "a", "b"], ["Chores", "c"], ["Ungrouped", "d"]]);
    expect(stored()).toEqual(kept);
    expect(status()).toBe("");
    expect(menu()).toEqual([]);
    expect(onView).not.toHaveBeenCalled();
  });

  it("drops a task released in the gap between two rows between them", async () => {
    keep(kept);
    await open([a, b, c, d]);
    // happy-dom lays nothing out: here a's row spans 100–140 and b's 152–192, with the group's gap between them.
    const tops: Record<string, number> = { a: 100, b: 152, c: 260, d: 360 };
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      return new DOMRect(0, tops[this.getAttribute("data-task") ?? ""] ?? 0, 400, 40);
    });
    // The pointer is released at 146, in that gap: over Reports' body, not over a row.
    vi.spyOn(document, "elementFromPoint").mockImplementation(() => section("Reports").querySelector(".task-group__body"));
    const pointer = (type: string, target: EventTarget, y: number) =>
      act(async () => void target.dispatchEvent(new PointerEvent(type, { bubbles: true, button: 0, clientX: 10, clientY: y })));
    await pointer("pointerdown", handle("d"), 0);
    await pointer("pointermove", window, 146);
    expect(row("b")!.className).toContain("task-row--drop-before");
    expect(section("Reports").className).not.toContain("task-group--drop");
    await pointer("pointerup", window, 146);
    expect(arrangement()).toEqual([["Reports", "a", "d", "b"], ["Chores", "c"], ["Ungrouped"]]);
    expect(status()).toBe("Task d moved to Reports, position 2 of 3.");
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

  /**
   * `POST /tasks/validate` as the daemon answers it for the changed file's text or its document: the document
   * serialized anew, in the schema's key order and without the file's comment.
   */
  const validated = {
    valid: true,
    errors: [],
    warnings: [],
    document: documentOf("Standup digest", "Write the standup digest."),
    effective: documentOf("Standup digest", "Write the standup digest."),
    yaml: before.replace("Standup notes", "Standup digest").replace("standup notes", "standup digest"),
  };

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
  const editorTab = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((b) => b.textContent === label)!;
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

    // Its YAML tab, with nothing changed in the form: the file's text byte for byte, its comment and key order
    // included, though the daemon's validation of the document would serialize it anew (harmflow-app#461).
    answer("POST", "/tasks/validate", validated);
    await click(editorTab("YAML"));
    expect(EditorView.findFromDOM(host!.querySelector<HTMLElement>(".cm-editor")!)!.state.doc.toString()).toBe(after);
    expect(daemon.requests.filter((r) => r.path === "/tasks/validate")).toEqual([]);
    await click(editorTab("Form"));

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
