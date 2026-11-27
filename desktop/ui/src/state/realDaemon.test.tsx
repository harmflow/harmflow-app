// @vitest-environment happy-dom
// The Tasks screen, its import dialog, and the task editor, with the store, against a real harmflowd (realDaemon.ts),
// with nothing standing in for the local API or its event stream:
// - A general task exported by a second daemon, as from another machine, is imported in the dashboard, listed, opened
//   in its editor from its row, edited, and saved to its file. ImportDialog.test and TasksScreen.test cover the
//   dashboard's half against a stand-in for the transport; itest › TestTaskLifecycle and TestExportAfterARunIsTheFile
//   cover the daemon's half.
// - A new GitHub task filled through the form's controls to the values of
//   tests/integration/testdata/task-form-document.json, as TaskForm.test › "task form document" fills it, is saved as a
//   file that holds that document and is listed; once a text editor changes that file, the list, the editor, and its
//   YAML tab show the file, and the next save builds on the file's revision. TaskForm.test and TasksScreen.test cover
//   each half against a stand-in for the transport; itest › TestFormDocumentIsTheFile covers the daemon's half.
// - A GitHub task the daemon already holds, saved with that document's queue through the dashboard's own transport, is
//   opened in its editor, which shows the saved queue in its controls; its order and limits changed there and saved
//   are one update on the task's revision, and the task's file then holds them, the rest of the document unchanged.
//   TaskForm.test covers the editor's half against a stand-in for the transport; itest ›
//   TestEditedQueueReachesTheNextExecution covers the daemon's half, from the saved file to the next execution.
// - A user's agent in the test's own configuration of Claude Code, selected in a new task's form, is copied into it
//   once: its instructions, description, model, and skills, its tools left out and named, and the saved file holds its
//   name and hash as the task's origin. After an edit of the prompt, choosing another agent asks first, and declining
//   keeps the edit. Once the agent's file changes, the reopened task shows a notice, and copying again shows the
//   difference and replaces the copy only once confirmed. AgentOrigin.test › "task form agent" and agentCopy.test cover
//   the form's half against a stand-in for the transport; catalog_test and app_test cover the daemon's copy and hash.
//
// All run on one daemon and one dashboard, in this order: the store is the module's own and holds one daemon's state,
// and the import starts from a daemon that holds no task.
//
// `make test-desktop-daemon` runs it; it stays out of `pnpm test` and its coverage (vite.config.ts, DAEMON_TESTS). Its
// name follows the file-name rules: ImportDialog.test.tsx, TasksScreen.test.tsx, and TaskEditor.test.tsx are the unit
// tests of the screens and the editor.
import { EditorView } from "@codemirror/view";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { request as httpRequest } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import type { Catalog, Task, TaskSummary } from "../api/types";
import { Toasts } from "../components/toast";
import i18n from "../i18n";
import type { AgentCopy } from "../lib/agentCopy";
import type { Doc } from "../lib/taskForm";
import { TasksScreen, type TaskView } from "../screens/TasksScreen";
import { call, serveDashboard, shutDown, startDaemon, until, type Dashboard, type RealDaemon } from "./realDaemon";
import { getState, startSync } from "./store";

// What the screen waits for: the daemon's answers, its watcher's report of a changed file, and the store's reads and
// debounce.
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

const FORM_DOCUMENT: Doc = JSON.parse(
  readFileSync(join(__dirname, "..", "..", "..", "..", "tests", "integration", "testdata", "task-form-document.json"), "utf8"),
);

/** The daemon of another machine, which exports the task. */
let elsewhere: RealDaemon;
/** The daemon the dashboard talks to, which imports it. */
let daemon: RealDaemon;
let dashboard: Dashboard;
/** The folder the form's task's ${FORM_REPO} names: outside the home folder, so an allowed folder. */
let repo: string | undefined;
let root: Root | undefined;
let host: HTMLElement | undefined;
/** Shows another view of the Tasks screen, as the app's navigation does. */
let show: (view: TaskView) => void = () => {};

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
  elsewhere = await startDaemon();
  // As itest › TestFormDocumentIsTheFile sets the daemon up: Codex's models cache, so that the preferred harness's model
  // and effort are ones it checks, and the form's task's subagent and skill in the test's own configuration of each
  // harness.
  daemon = await startDaemon((home) => {
    const codexHome = join(home, "codex");
    const claudeConfig = join(home, "claude");
    for (const dir of [codexHome, claudeConfig]) {
      mkdirSync(join(dir, "agents"), { recursive: true });
      writeFileSync(join(dir, "agents", "issue-reviewer.md"), "---\nname: issue-reviewer\ndescription: Reviews an issue.\n---\nReview the issue.\n");
      mkdirSync(join(dir, "skills", "review-notes"), { recursive: true });
      writeFileSync(join(dir, "skills", "review-notes", "SKILL.md"), "---\nname: review-notes\ndescription: Notes a review.\n---\nNote the review.\n");
    }
    writeFileSync(
      join(codexHome, "models_cache.json"),
      JSON.stringify({ models: [{ slug: "gpt-5.6-terra", supported_reasoning_levels: [{ effort: "low" }, { effort: "high" }] }] }),
    );
    return { CLAUDE_CONFIG_DIR: claudeConfig };
  });
  dashboard = await serveDashboard(daemon);
}, 120_000);

afterAll(async () => {
  act(() => root?.unmount());
  host?.remove();
  await shutDown(dashboard, daemon);
  await shutDown(undefined, elsewhere);
  if (repo) rmSync(repo, { recursive: true, force: true });
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
 * requests, since the store drops a failed read without a word; a refused import shows in the dialog, as a banner or
 * its validation, not in a toast.
 */
function held(): string {
  const { connection, tasksLoaded, tasks, harnesses, catalog } = getState();
  return JSON.stringify({
    connection,
    tasksLoaded,
    tasks: tasks.map((t) => t.id),
    harnesses: harnesses.map((h) => `${h.id}: ${h.models.length} models`),
    agents: catalog?.agents.map((a) => a.name) ?? null,
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

// The controls, reached as the user reaches them: each by the label it shows. A group of checkboxes is a `div.field`;
// any other field is a `label.field`.
const field = (label: string) =>
  [...host!.querySelectorAll<HTMLElement>(".field")].find((f) => f.querySelector(".field__label")?.textContent === label);
const control = <E extends Element = HTMLInputElement>(label: string) => field(label)!.querySelector<E>("input, select, textarea, output")!;
const toggle = (label: string) =>
  [...host!.querySelectorAll("label.toggle")].find((t) => t.querySelector(".toggle__label")?.textContent === label)!.querySelector("input")!;
const boxes = (fieldLabel: string) => [...field(fieldLabel)!.querySelectorAll("label.checkbox")];
const button = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label)!;
const radio = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find((b) => b.textContent === label)!;
/** The names a picker shows: each chip holds the name and the button named Remove. */
const chips = (label: string) =>
  [...field(label)!.querySelectorAll('button[aria-label="Remove"]')].map((b) => b.parentElement!.querySelector(".mono")!.textContent);
/** The first repository row's fields, each by its column's name. */
const repository = () =>
  ["Repository (owner/repo)", "Local path", "Base branch"].map((name) => host!.querySelector<HTMLInputElement>(`input[aria-label="${name}"]`)!);
/**
 * The preferred harness, then each fallback: its harness, model, and effort controls, each by its label within the
 * profile's row, since a harness with homes adds an Installation select to the row.
 */
const profiles = () =>
  [...host!.querySelectorAll("#section-harness .grid-profile")].map((p) => {
    const within = <E extends Element>(label: string) =>
      [...p.querySelectorAll(".field")].find((f) => f.querySelector(".field__label")?.textContent === label)!.querySelector<E>("input, select")!;
    return { harness: within<HTMLSelectElement>("AI tool"), model: within<HTMLInputElement>("Model"), effort: within<HTMLSelectElement>("Effort") };
  });
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const dialogButton = (label: string) => [...dialog()!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label);
const filtersButton = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>(".filters button")].find((b) => b.textContent === label)!;
const editorTab = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((b) => b.textContent === label)!;
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
/** The JSON bodies the dashboard sent to the daemon with this method and path. */
const sent = (method: string, path: string) => sentTo(method, path).map((r) => r.sent);

/** Types into a field or chooses an option, as React sees it. */
async function enter(el: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement, value: string) {
  const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
  });
}
async function click(el: HTMLElement) {
  await act(async () => el.click());
  await pause();
}
/** Clicks one of a field's checkboxes. */
async function tick(fieldLabel: string, label: string) {
  await click(boxes(fieldLabel).find((c) => c.textContent === label)!.querySelector("input")!);
}

/** The Tasks screen as the app shows it: the view a command asks for is the one shown next. */
function Screen({ initial }: { initial: TaskView }) {
  const [view, setView] = useState<TaskView>(initial);
  show = setView;
  return <TasksScreen view={view} onView={setView} onOpenExecution={() => {}} />;
}

/** Shows the Tasks screen on the view, in place of what an earlier test showed. */
async function mount(initial: TaskView) {
  act(() => root?.unmount());
  host?.remove();
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <>
        <Screen initial={initial} />
        <Toasts />
      </>,
    ),
  );
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
    await mount({ kind: "list" });
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

describe("the task form and the Tasks screen against the real daemon", () => {
  beforeAll(async () => {
    // The folder the form's task names by the FORM_REPO path variable, allowed, as itest › TestFormDocumentIsTheFile
    // allows it.
    repo = mkdtempSync(join(tmpdir(), "harmflow-ui-repo-"));
    await call(daemon, "PATCH", "/settings", { allowedFolders: [repo], pathVariables: { FORM_REPO: repo } });
  });

  it("save the form's document as the file the list shows, and show the file once it changes outside HarmFlow", async () => {
    startSync();
    // The form's pickers and Effort selects read the catalog and the harnesses the daemon detected.
    await until(
      "the store holds the catalog and both harnesses",
      () => {
        const { catalog, harnesses } = getState();
        const reviewer = catalog?.agents.some((a) => a.name === "issue-reviewer");
        const both = ["claude-code", "codex"].every((id) => harnesses.some((h) => h.id === id && h.models.length > 0));
        return reviewer && both ? true : undefined;
      },
      SHOWN_MS,
    ).catch((err: Error) => {
      throw new Error(`${err.message}; ${held()}`);
    });

    await mount({ kind: "new", template: "hf-github-issues" });
    // The daemon's draft of the GitHub template: no agent, prompt, or skills, and one repository row.
    await shown("the template's draft in the form", () => field("Name") && control("Name").value === "Blank GitHub task");

    // General. The template selects no agent, so the document has no origin. The name writes an identifier of its own
    // until the identifier is typed.
    await enter(control("Name"), "Every section");
    expect(control("Identifier").value).toBe("every-section");
    await enter(control("Identifier"), "complete");
    await enter(control<HTMLTextAreaElement>("Description"), "Every section of the form, set.");
    expect(toggle("Enabled").checked).toBe(true);
    await click(toggle("Enabled"));

    // Prompt. The template lists no skill: the fixture's one is the user's.
    await enter(control<HTMLTextAreaElement>("Prompt"), "Implement the oldest issue labelled ready-for-agent.");
    await enter(control("Other agents (subagents)"), "issue-reviewer");
    expect(chips("Other agents (subagents)")).toEqual(["issue-reviewer"]);
    expect(chips("Skills")).toEqual([]);
    await enter(control("Skills"), "review-notes");
    expect(chips("Skills")).toEqual(["review-notes"]);
    await enter(control("Workspace"), "~/harmflow-work");

    // The GitHub type: the template's repository row, then the queue and delivery.
    const [name, localPath, baseBranch] = repository();
    await enter(name!, "harmflow/example");
    await enter(localPath!, "${FORM_REPO}");
    await enter(baseBranch!, "develop");
    await enter(control<HTMLSelectElement>("Order"), "newest_first");
    await enter(control("Items discovered per cycle"), "5");
    await enter(control("Items processed per execution"), "2");
    await enter(control<HTMLSelectElement>("Resume priority"), "alternate");
    await enter(control("Resumptions before needs attention"), "1");
    await enter(control("Retries of a failed item"), "0");
    await enter(control("Test-repair attempts"), "0");

    // Harness: Codex preferred, then Claude Code, switched to only when a harness is unavailable.
    await enter(profiles()[0]!.harness, "codex");
    await enter(profiles()[0]!.model, "gpt-5.6-terra");
    await enter(profiles()[0]!.effort, "high");
    await click(button("Add fallback"));
    await enter(profiles()[1]!.harness, "claude-code");
    await enter(profiles()[1]!.model, "sonnet");
    await enter(profiles()[1]!.effort, "high");
    expect(profiles().map((p) => [p.harness.value, p.model.value, p.effort.value])).toEqual([
      ["codex", "gpt-5.6-terra", "high"],
      ["claude-code", "sonnet", "high"],
    ]);
    await tick("Fail over on", "Quota exhausted");
    await enter(control("Maximum switches"), "2");

    // Schedule: weekdays at 06:30 in São Paulo, skipping missed and overlapping firings.
    await click(radio("Cron"));
    await enter(control("Time"), "06:30");
    await enter(control("Time zone"), "America/Sao_Paulo");
    await enter(control<HTMLSelectElement>("Missed runs"), "skip");
    await enter(control<HTMLSelectElement>("Overlapping firings"), "skip");

    // Permissions: the custom profile's lists show once it is chosen.
    await enter(control<HTMLSelectElement>("Profile"), "custom");
    await enter(control<HTMLSelectElement>("Network"), "deny");
    await click(toggle("Restrict writes to the workspace"));
    await enter(control<HTMLTextAreaElement>("Allowed tools"), "Read\nGrep");
    await enter(control<HTMLTextAreaElement>("Denied tools"), "WebFetch");
    await enter(control<HTMLTextAreaElement>("Writable paths"), "${FORM_REPO}");
    await enter(control<HTMLTextAreaElement>("Read paths"), "${FORM_REPO}");
    await enter(control<HTMLTextAreaElement>("Allowed programs"), "git\ngh");
    await enter(control<HTMLTextAreaElement>("Denied programs"), "curl");

    // Execution.
    await enter(control("Timeout (minutes), shared by all attempts"), "45");
    await enter(control("Grace before a cancellation is forced (seconds)"), "10");
    await enter(control("Rate-limit retries"), "2");
    await enter(control("First wait (seconds)"), "30");
    await enter(control("Longest wait (seconds)"), "600");

    // Notifications: off, and of the default events only operational failures kept, with failed tests added.
    await click(toggle("Native notifications"));
    for (const event of ["Execution completed", "Needs attention", "Draft PR created", "No alternative AI tool"]) await tick("Notify me about", event);
    await tick("Notify me about", "Tests failed");

    // Saved from the form: one creation, of the fixture's document strictly, and the daemon's answer opens the task.
    await click(button("Save"));
    await shown("the editor on the saved task", () => sent("POST", "/tasks").length > 0 && field("Identifier") && control("Identifier").disabled);
    expect(sent("POST", "/tasks")).toStrictEqual([{ document: FORM_DOCUMENT }]);

    // The task's file, under the daemon's tasks directory, holds the fixture's document, as the daemon reads the file.
    const file = join(daemon.home, "tasks", "complete.yaml");
    const saved = readFileSync(file, "utf8");
    expect(await documentOf(saved)).toStrictEqual(FORM_DOCUMENT);
    // The task the daemon holds is that file, and valid: not marked invalid, and with no failed validation. A task read
    // back may carry no validation at all, so a missing one is no failure.
    const stored = (await call(daemon, "GET", "/tasks/complete")) as Task;
    expect(stored.invalid ?? false).toBe(false);
    expect(stored.validation?.valid ?? true).toBe(true);
    expect(stored.document).toStrictEqual(FORM_DOCUMENT);
    expect(stored.yaml).toBe(saved);

    // The list, on the store's next read, shows the task by its name and identifier.
    act(() => show({ kind: "list" }));
    await shown("the saved task in the list", () => rowName("complete") === "Every section");

    // Changed outside HarmFlow: a text editor gives the task another name and prompt, and adds a comment.
    const edited = saved
      .replace("kind: Task\n", "kind: Task\n# Edited in a text editor.\n")
      .replace("name: Every section\n", "name: Every section, edited\n")
      .replace("the oldest issue", "the newest issue");
    for (const part of ["# Edited in a text editor.\n", "name: Every section, edited\n", "the newest issue"]) expect(edited).toContain(part);
    writeFileSync(file, edited);

    // The list: the daemon's watcher reports the change on the event stream, with no stand-in delivering it, and the
    // store reads the list again.
    await shown("the file's new name in the list", () => rowName("complete") === "Every section, edited");
    const changed = (await call(daemon, "GET", "/tasks/complete")) as Task;
    expect(changed.yaml).toBe(edited);
    expect((await call(daemon, "GET", "/tasks")) as TaskSummary[]).toContainEqual(expect.objectContaining({ id: "complete", revision: changed.revision }));

    // The editor opened next: its form shows the file's name and prompt.
    await click(rowButton("complete", "Edit"));
    await shown("the file's definition in the editor", () => field("Name") && control("Name").value === "Every section, edited");
    expect(control<HTMLTextAreaElement>("Prompt").value).toBe("Implement the newest issue labelled ready-for-agent.");

    // Its YAML tab: the file's text byte for byte, the comment included.
    await click(editorTab("YAML"));
    const yaml = await shown("the YAML tab", () => host!.querySelector<HTMLElement>(".cm-editor"));
    expect(EditorView.findFromDOM(yaml)!.state.doc.toString()).toBe(edited);
    // Back to the form, which shows once the daemon has read the YAML tab's text into a document.
    await click(editorTab("Form"));
    await shown("the form again", () => field("Name") && control("Name").value === "Every section, edited");

    // An edit of the name and a Save: one update, on the file's current revision, and the file then holds that name.
    await enter(control("Name"), "Every section, edited twice");
    await click(editorButton("Save"));
    await shown("the update", () => sent("PUT", "/tasks/complete").length > 0);
    expect(sent("PUT", "/tasks/complete")).toEqual([expect.objectContaining({ baseRevision: changed.revision })]);
    await shown("the edited name in the file", () => readFileSync(file, "utf8").includes("name: Every section, edited twice\n"));
  });

  it("show an existing task's saved queue in the editor, and save an edit of its order and limits to its file", async () => {
    // The task the daemon holds before the editor opens: the form's document under another identifier, saved through
    // the dashboard's own transport, with the queue newest first, five discovered, and two processed.
    const original: Doc = {
      ...FORM_DOCUMENT,
      metadata: { ...(FORM_DOCUMENT.metadata as Record<string, unknown>), id: "queue-edit", name: "Queue edit" },
    };
    const queueOf = (doc: Doc) => (doc.spec as { github: { queue: Record<string, unknown> } }).github.queue;
    expect(queueOf(original)).toMatchObject({ ordering: "newest_first", maxDiscoveredPerCycle: 5, maxItemsPerCycle: 2 });
    const created = await api.createTask({ document: original });
    expect(created.revision).toBeTruthy();
    const file = join(daemon.home, "tasks", "queue-edit.yaml");
    expect(await documentOf(readFileSync(file, "utf8"))).toStrictEqual(original);

    // Opened in its editor from its row: the queue's controls show the saved order and limits.
    startSync();
    await mount({ kind: "list" });
    await shown("the saved task in the list", () => rowName("queue-edit") === "Queue edit");
    await click(rowButton("queue-edit", "Edit"));
    await shown("the saved task in the editor", () => field("Name") && control("Name").value === "Queue edit");
    expect(control<HTMLSelectElement>("Order").value).toBe("newest_first");
    expect(control("Items discovered per cycle").value).toBe("5");
    expect(control("Items processed per execution").value).toBe("2");

    // Each changed to a valid value other than the saved one, and saved.
    await enter(control<HTMLSelectElement>("Order"), "oldest_first");
    await enter(control("Items discovered per cycle"), "8");
    await enter(control("Items processed per execution"), "3");
    await click(editorButton("Save"));
    const answer = await shown("the update's answer", () => sentTo("PUT", "/tasks/queue-edit")[0]?.status);
    expect(answer).toBe(200);

    // One update, on the revision the creation gave, of the saved document with only the edited order and limits.
    const edited: Doc = {
      ...original,
      spec: {
        ...(original.spec as Record<string, unknown>),
        github: {
          ...(original.spec as { github: Record<string, unknown> }).github,
          queue: { ...queueOf(original), ordering: "oldest_first", maxDiscoveredPerCycle: 8, maxItemsPerCycle: 3 },
        },
      },
    };
    const updates = sentTo("PUT", "/tasks/queue-edit");
    expect(updates).toHaveLength(1);
    expect(updates[0]!.sent).toEqual(expect.objectContaining({ document: edited, baseRevision: created.revision }));

    // The task's file, as the daemon reads it, and the task the daemon answers hold the edited queue, the rest of the
    // document unchanged, and the task valid.
    const saved = readFileSync(file, "utf8");
    expect(await documentOf(saved)).toStrictEqual(edited);
    const stored = (await call(daemon, "GET", "/tasks/queue-edit")) as Task;
    expect(stored.invalid ?? false).toBe(false);
    expect(stored.validation?.valid ?? true).toBe(true);
    expect(stored.document).toStrictEqual(edited);
    expect(stored.yaml).toBe(saved);
    expect(stored.revision).not.toBe(created.revision);
  });
});

describe("a user's agent selected in the task form against the real daemon", () => {
  const agents = () => join(daemon.home, "claude", "agents");
  /** The agent the form copies: every field the copy takes, and tools, which a task has no place for. */
  const REVIEWER = [
    "---",
    "name: change-reviewer",
    "description: Reviews a change.",
    "model: sonnet",
    "skills: review-notes",
    "tools: Read, Grep",
    "---",
    "Review the change, and say what it breaks.",
    "",
  ].join("\n");

  beforeAll(() => {
    // Beside issue-reviewer, in the test's own configuration of Claude Code, which the daemon reads on each catalog read:
    // the agent the form copies, and another agent to choose once the prompt is edited.
    writeFileSync(join(agents(), "change-reviewer.md"), REVIEWER);
    writeFileSync(join(agents(), "change-tester.md"), "---\nname: change-tester\ndescription: Tries a change.\n---\nTry the change.\n");
  });

  /** What selecting the user's agent copies, as the daemon's catalog gives it to the form. */
  async function copyOf(name: string): Promise<AgentCopy> {
    const catalog = (await call(daemon, "GET", "/catalog")) as Catalog;
    const copy = catalog.agents.find((a) => a.name === name && a.source === "user")?.copy;
    if (!copy) throw new Error(`GET /catalog gives no copy of the user's agent ${name}: ${JSON.stringify(catalog.agents.map((a) => a.name))}`);
    return copy;
  }

  const agent = () => host!.querySelector<HTMLSelectElement>('select[aria-label="Agent"]');
  const offered = (name: string) => [...(agent()?.options ?? [])].some((o) => o.value === name);
  const copyAgain = () => [...host!.querySelectorAll<HTMLButtonElement>(".banner button")].find((b) => b.textContent === "Copy again from the agent");

  it("copies the agent once, asks before another replaces an edited prompt, and copies it again only once confirmed", async () => {
    const copied = await copyOf("change-reviewer");
    expect(copied).toMatchObject({
      prompt: "Review the change, and say what it breaks.",
      description: "Reviews a change.",
      model: "sonnet",
      skills: ["review-notes"],
      omittedFields: ["tools"],
    });
    expect(copied.modelHarnesses).toContain("claude-code");

    // Outside the desktop shell, the form asks through the page's own confirm, which the test DOM lacks.
    const confirm = vi.fn((_message?: string) => false);
    Object.assign(window, { confirm });
    try {
      // A new general task: the template has no prompt, so the first agent's copy replaces nothing of the user's.
      startSync();
      await mount({ kind: "new", template: "hf-general" });
      await shown("both agents in the form's Agent select", () => offered("change-reviewer") && offered("change-tester") && field("Name"));
      expect(control<HTMLTextAreaElement>("Prompt").value).toBe("");
      await enter(control("Name"), "Agent copy");
      expect(control("Identifier").value).toBe("agent-copy");

      // Selected: the prompt, description, model, and skills are the agent's, and tools is named as left out.
      await enter(agent()!, "change-reviewer");
      expect(confirm).not.toHaveBeenCalled();
      expect(control<HTMLTextAreaElement>("Prompt").value).toBe(copied.prompt);
      expect(control<HTMLTextAreaElement>("Description").value).toBe("Reviews a change.");
      expect(profiles()[0]!.model.value).toBe("sonnet");
      expect(chips("Skills")).toEqual(["review-notes"]);
      expect(host!.querySelector(".agent-left-out")?.textContent).toContain("Not copied from the agent: tools.");

      // Saved: the file holds the copy and the agent's name and hash as its origin, and nothing of its tools.
      const creations = sentTo("POST", "/tasks").length;
      await click(button("Save"));
      await shown("the editor on the saved task", () => sentTo("POST", "/tasks").length > creations && field("Identifier") && control("Identifier").disabled);
      const file = join(daemon.home, "tasks", "agent-copy.yaml");
      const first = await documentOf(readFileSync(file, "utf8"));
      expect(first).toMatchObject({
        metadata: { id: "agent-copy", name: "Agent copy", description: "Reviews a change." },
        spec: {
          type: "general",
          prompt: copied.prompt,
          harness: { preferred: { provider: "claude-code", model: "sonnet" } },
          skills: ["review-notes"],
          origin: { agent: "change-reviewer", hash: copied.hash },
        },
      });
      expect(JSON.stringify(first)).not.toContain("Grep");
      const stored = (await call(daemon, "GET", "/tasks/agent-copy")) as Task;
      expect(stored.invalid ?? false).toBe(false);
      expect(stored.validation?.valid ?? true).toBe(true);

      // Edited, then another agent chosen: the form asks first, and declining keeps the edited prompt and the origin.
      const edited = `${copied.prompt}\nThen say why.`;
      await enter(control<HTMLTextAreaElement>("Prompt"), edited);
      await enter(agent()!, "change-tester");
      expect(confirm).toHaveBeenCalledTimes(1);
      expect(confirm.mock.calls[0]![0]).toContain("Copying change-tester replaces the prompt you edited");
      expect(control<HTMLTextAreaElement>("Prompt").value).toBe(edited);
      expect(agent()!.value).toBe("change-reviewer");

      // The agent's file changes on disk: the catalog then gives another copy, with another hash.
      writeFileSync(join(agents(), "change-reviewer.md"), REVIEWER.replace("say what it breaks.\n", "say what it breaks.\nRun its tests.\n"));
      const changed = await copyOf("change-reviewer");
      expect(changed.prompt).toBe(`${copied.prompt}\nRun its tests.`);
      expect(changed.hash).not.toBe(copied.hash);

      // Reopened from its row, the edit above left unsaved: the task shows the notice that the agent has changed.
      await mount({ kind: "list" });
      await shown("the saved task in the list", () => rowName("agent-copy") === "Agent copy");
      await click(rowButton("agent-copy", "Edit"));
      await shown("the notice of the changed agent", () =>
        [...host!.querySelectorAll(".banner")].some((b) => b.textContent?.includes("The agent change-reviewer has changed")) && copyAgain(),
      );
      expect(control<HTMLTextAreaElement>("Prompt").value).toBe(copied.prompt);

      // Copy again shows the difference; the description, model, and skills are the agent's still, so only the prompt
      // changes. Cancel keeps the copy.
      await click(copyAgain()!);
      await shown("the difference", () => dialog()?.querySelector(".prompt-diff"));
      expect([...dialog()!.querySelectorAll(".prompt-diff__line")].map((l) => l.textContent)).toEqual([
        "  Review the change, and say what it breaks.\n",
        "+ Run its tests.\n",
      ]);
      expect(dialog()!.querySelector(".copy-changes")).toBeNull();
      await click(dialogButton("Cancel")!);
      expect(dialog()).toBeNull();
      expect(control<HTMLTextAreaElement>("Prompt").value).toBe(copied.prompt);
      expect(copyAgain()).toBeTruthy();

      // Confirmed: the form holds the new copy, and the notice is gone; the file holds the first copy until the save.
      await click(copyAgain()!);
      await click(dialogButton("Replace the copy")!);
      expect(dialog()).toBeNull();
      expect(control<HTMLTextAreaElement>("Prompt").value).toBe(changed.prompt);
      expect(copyAgain()).toBeUndefined();
      expect(await documentOf(readFileSync(file, "utf8"))).toMatchObject({ spec: { prompt: copied.prompt, origin: { hash: copied.hash } } });

      const updates = sentTo("PUT", "/tasks/agent-copy").length;
      await click(editorButton("Save"));
      await shown("the update", () => sentTo("PUT", "/tasks/agent-copy").length > updates);
      await shown("the new copy in the file", () => readFileSync(file, "utf8").includes("Run its tests."));
      expect(await documentOf(readFileSync(file, "utf8"))).toMatchObject({
        metadata: { description: "Reviews a change." },
        spec: { prompt: changed.prompt, skills: ["review-notes"], origin: { agent: "change-reviewer", hash: changed.hash } },
      });
    } finally {
      Reflect.deleteProperty(window, "confirm");
    }
  });
});
