// @vitest-environment happy-dom
// The Tasks screen with its import dialog, and the task editor, with the store, against a real harmflowd
// (realDaemon.ts), with nothing standing in for the local API or its event stream. A general task exported by a second
// daemon, as from another machine, is imported in the dashboard, listed, opened in its editor from its row, edited, and
// saved to its file. ImportDialog.test and TasksScreen.test cover the dashboard's half against a stand-in for the
// transport; itest › TestTaskLifecycle and TestExportAfterARunIsTheFile cover the daemon's half.
//
// `make test-desktop-daemon` runs it; it stays out of `pnpm test` and its coverage (vite.config.ts, DAEMON_TESTS). Its
// name follows the file-name rules: ImportDialog.test.tsx and TasksScreen.test.tsx are the unit tests of the screens.
import { existsSync, readFileSync } from "node:fs";
import { request as httpRequest } from "node:http";
import { join } from "node:path";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import type { Task, TaskSummary } from "../api/types";
import { Toasts } from "../components/toast";
import i18n from "../i18n";
import { TasksScreen, type TaskView } from "../screens/TasksScreen";
import { call, serveDashboard, shutDown, startDaemon, type Dashboard, type RealDaemon } from "./realDaemon";
import { getState, startSync } from "./store";

// What the screen waits for: the daemon's answers and the store's reads.
const SHOWN_MS = 20_000;

/** A general task, the smallest that runs a harness, as the other machine holds it. */
const EXPORTED_TASK = [
  "apiVersion: harmflow/v1alpha1",
  "kind: Task",
  "metadata:",
  "  id: brought-over",
  "  name: Brought over",
  "  description: Exported on another machine.",
  "spec:",
  "  type: general",
  "  prompt: Summarize the week's notes.",
  "  harness:",
  "    preferred:",
  "      provider: claude-code",
  "",
].join("\n");

/** The daemon of another machine, which exports the task. */
let elsewhere: RealDaemon;
/** The daemon the dashboard talks to, which imports it. */
let daemon: RealDaemon;
let dashboard: Dashboard;
let root: Root | undefined;
let host: HTMLElement | undefined;
/** Shows another view of the Tasks screen, as the app's navigation does. */
let show: (view: TaskView) => void = () => {};

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
  elsewhere = await startDaemon();
  daemon = await startDaemon();
  dashboard = await serveDashboard(daemon);
}, 120_000);

afterAll(async () => {
  act(() => root?.unmount());
  host?.remove();
  await shutDown(dashboard, daemon);
  await shutDown(undefined, elsewhere);
});

/** The export of a task, the YAML text the daemon answers, through Node's HTTP client as call() reads the API. */
async function exported(d: RealDaemon, id: string): Promise<string> {
  const { status, text } = await new Promise<{ status: number; text: string }>((resolve, reject) => {
    const req = httpRequest(`${d.base}/tasks/${id}/export`, { headers: { Authorization: `Bearer ${d.token}` } }, (res) => {
      const chunks: Buffer[] = [];
      res.on("data", (c: Buffer) => chunks.push(c));
      res.on("end", () => resolve({ status: res.statusCode ?? 0, text: Buffer.concat(chunks).toString("utf8") }));
      res.on("error", reject);
    });
    req.on("error", reject);
    req.end();
  });
  if (status !== 200) throw new Error(`GET /tasks/${id}/export: ${status} ${text}`);
  return text;
}

/** The document a task file holds, as the daemon reads it. */
async function documentOf(yaml: string): Promise<Record<string, unknown>> {
  const parsed = (await call(daemon, "POST", "/tasks/validate", { yaml })) as { valid: boolean; document: Record<string, unknown> };
  expect(parsed.valid).toBe(true);
  return parsed.document;
}

/** Lets the daemon's answers arrive and React render them, for a moment. */
const pause = (ms = 20) => act(() => new Promise<void>((resolve) => setTimeout(resolve, ms)));

/**
 * What the store and the screen held when a wait timed out: a failed read, or one that never came back, shows in the
 * requests; a refused import shows in the dialog, as a banner or its validation, not in a toast.
 */
function held(): string {
  const { connection, tasksLoaded, tasks } = getState();
  return JSON.stringify({
    connection,
    tasksLoaded,
    tasks: tasks.map((t) => t.id),
    dialog: dialog()?.querySelector(".banner, .validation")?.textContent ?? null,
    requests: dashboard.requests.slice(-40).map((r) => `${r.method} ${r.path} ${r.status ?? "no answer"}`),
  });
}

/** Waits, within SHOWN_MS, until read() returns something, rendering as the answers arrive. */
async function shown<T>(what: string, read: () => T | undefined | null | false): Promise<T> {
  const deadline = Date.now() + SHOWN_MS;
  for (;;) {
    const value = read();
    if (value !== undefined && value !== null && value !== false) return value;
    if (Date.now() > deadline) throw new Error(`${what}: not within ${SHOWN_MS} ms; toasts: ${JSON.stringify(toasts())}; ${held()}`);
    await pause();
  }
}

// The controls, reached as the user reaches them: each by the label it shows.
const field = (label: string) =>
  [...host!.querySelectorAll<HTMLElement>(".field")].find((f) => f.querySelector(".field__label")?.textContent === label);
const control = <E extends Element = HTMLInputElement>(label: string) => field(label)!.querySelector<E>("input, select, textarea, output")!;
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const dialogButton = (label: string) => [...dialog()!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label);
const filtersButton = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>(".filters button")].find((b) => b.textContent === label)!;
const editorButton = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>(".editor__actions button")].find((b) => b.textContent === label)!;
const rows = () => [...host!.querySelectorAll<HTMLElement>("article.task-row")];
const row = (id: string) => rows().find((r) => r.querySelector(".task-row__title .mono")?.textContent === id);
const rowName = (id: string) => row(id)?.querySelector(".task-row__title strong")?.textContent;
const rowButton = (id: string, label: string) =>
  [...row(id)!.querySelectorAll<HTMLButtonElement>("button")].find((b) => (b.getAttribute("aria-label") ?? b.textContent) === label)!;
const scriptPreview = () => document.querySelector('[data-testid="script-import-preview"]');
const toasts = () => [...document.querySelectorAll(".toast")].map((t) => t.textContent);
/** The requests the dashboard sent to the daemon with this method and path. */
const sentTo = (method: string, path: string) => dashboard.requests.filter((r) => r.method === method && r.path === `/api/v1${path}`);

/** Types into a field, as React sees it. */
async function enter(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function click(el: HTMLElement) {
  await act(async () => el.click());
  await pause();
}

/** The Tasks screen as the app shows it: the view a command asks for is the one shown next. */
function Screen({ initial }: { initial: TaskView }) {
  const [view, setView] = useState<TaskView>(initial);
  show = setView;
  return <TasksScreen view={view} onView={setView} onOpenExecution={() => {}} />;
}

describe("a task imported in the dashboard against the real daemon", () => {
  it("is listed by its name and identifier, opens in its editor from its row, and saves an edit to its file", async () => {
    // Exported on another machine: the second daemon holds the task and answers its export.
    await call(elsewhere, "POST", "/tasks", { yaml: EXPORTED_TASK });
    const yaml = await exported(elsewhere, "brought-over");
    expect(await documentOf(yaml)).toMatchObject({ metadata: { id: "brought-over", name: "Brought over" }, spec: { type: "general" } });
    // The dashboard's daemon has no task of that identifier before the import.
    expect(((await call(daemon, "GET", "/tasks")) as TaskSummary[]).map((t) => t.id)).not.toContain("brought-over");
    const file = join(daemon.home, "tasks", "brought-over.yaml");
    expect(existsSync(file)).toBe(false);

    startSync();
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () =>
      root!.render(
        <>
          <Screen initial={{ kind: "list" }} />
          <Toasts />
        </>,
      ),
    );
    await shown("the empty task list", () => getState().tasksLoaded && host!.querySelector(".panel--empty"));

    // Imported in the dashboard: the dialog opened from the Tasks screen, the export entered, and Import clicked once.
    await click(filtersButton("Import"));
    const area = await shown("the import dialog", () => dialog()?.querySelector<HTMLTextAreaElement>("textarea"));
    await enter(area, yaml);
    await click(dialogButton("Import")!);
    // The dialog sends one import and shows no script preview: a general task needs no confirmation, so the dialog
    // closes on the daemon's answer and the screen opens the task's editor.
    await shown("the import's answer", () => sentTo("POST", "/tasks/import").length > 0 && !dialog());
    expect(scriptPreview()).toBeNull();
    const imports = sentTo("POST", "/tasks/import");
    expect(imports.map((r) => r.sent)).toEqual([{ yaml, overwrite: false }]);
    const imported = (await imports[0]!.body) as Task;
    expect(imported).toMatchObject({ id: "brought-over", name: "Brought over" });
    expect(imported.validation?.valid).toBe(true);
    expect(imported.revision).toBeTruthy();

    // The task's file exists under the daemon's tasks directory, and the daemon answers it as valid: a task read back
    // carries `invalid` and its validation only when its file on disk fails validation.
    expect(existsSync(file)).toBe(true);
    const stored = (await call(daemon, "GET", "/tasks/brought-over")) as Task;
    expect(stored.invalid ?? false).toBe(false);
    expect(stored.validation).toBeUndefined();
    expect(stored.revision).toBe(imported.revision);
    const importedDocument = await documentOf(readFileSync(file, "utf8"));
    expect(importedDocument).toMatchObject({
      metadata: { id: "brought-over", name: "Brought over" },
      spec: { type: "general", prompt: "Summarize the week's notes." },
    });

    // The Tasks screen, on the store's next read, lists the task by its name and identifier.
    act(() => show({ kind: "list" }));
    await shown("the imported task in the list", () => rowName("brought-over") === "Brought over");

    // Edited in the dashboard: its editor opens from its row, with the exported name and prompt in the form.
    await click(rowButton("brought-over", "Edit"));
    await shown("the imported task in the editor", () => field("Name") && control("Name").value === "Brought over");
    expect(control<HTMLTextAreaElement>("Prompt").value).toBe("Summarize the week's notes.");

    // An edit of the name and a Save: one update, on the revision the import gave, and the file then holds the edited
    // name, with the rest of the imported document unchanged.
    await enter(control("Name"), "Brought over, renamed");
    await click(editorButton("Save"));
    await shown("the update", () => sentTo("PUT", "/tasks/brought-over").length > 0);
    await shown("the edited name in the file", () => readFileSync(file, "utf8").includes("name: Brought over, renamed\n"));
    const updates = sentTo("PUT", "/tasks/brought-over");
    expect(updates).toHaveLength(1);
    expect(updates[0]!.sent).toEqual(expect.objectContaining({ baseRevision: imported.revision }));
    const editedDocument = await documentOf(readFileSync(file, "utf8"));
    expect(editedDocument).toStrictEqual({
      ...importedDocument,
      metadata: { ...(importedDocument.metadata as Record<string, unknown>), name: "Brought over, renamed" },
    });
  });
});
