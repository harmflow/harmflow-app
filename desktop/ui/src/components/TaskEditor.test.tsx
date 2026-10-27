// @vitest-environment happy-dom
// Drives the task editor in a DOM, against a stand-in for the daemon's local API (harmflow-app#183). Its Form and YAML
// tabs carry the task across through the daemon's validation, both ways. A save from the YAML tab sends the text with
// the revision of the last successful save; a refused one keeps the text and shows the daemon's error at its line, the
// same error a save from the form shows on its field. The stand-in decides which error the daemon gives: that the daemon
// gives a document and its YAML the same one is itest › TestOneSchemaForEveryEntryPoint's. ⌘S saves as the Save button
// does, from any focus in the editor (harmflow-app#230). A save shows the warnings the daemon gives, such as a fallback
// that cannot load the task's skill (harmflow-app#216).
import { EditorView } from "@codemirror/view";
import { isDeepStrictEqual } from "node:util";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi, type Mock } from "vitest";
import type { Task, TaskInput, ValidationIssue, ValidationResult } from "../api/types";
import i18n from "../i18n";
import type { Doc } from "../lib/taskForm";
import { TaskEditor } from "./TaskEditor";
import { Toasts } from "./toast";

// The daemon as the client sees it: an answer per route, which may come later, and every request it received with its
// body as sent.
type Answer = { status: number; body: unknown };
const daemon = vi.hoisted(() => ({
  routes: new Map<string, (body: unknown) => Answer | Promise<Answer>>(),
  requests: [] as { method: string; path: string; body: unknown }[],
}));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string, body?: unknown) => {
      // As JSON carries it: a field left undefined is not sent.
      const sent: unknown = body === undefined ? undefined : JSON.parse(JSON.stringify(body));
      daemon.requests.push({ method, path, body: sent });
      const route = daemon.routes.get(`${method} ${path}`);
      return route ? route(sent) : { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

const CRON = "0 9 * * 1-5";
/** A cron the daemon refuses, a rule the form marks on the Cron field: minute 61. */
const BAD_CRON = "61 9 * * 1-5";

/** The one task the stand-in knows, as a document. */
const taskDoc = (name: string, cron = CRON): Doc => ({
  apiVersion: "harmflow/v1alpha1",
  kind: "Task",
  metadata: { id: "standup", name },
  spec: { type: "general", schedule: { cron }, prompt: "Write the standup notes." },
});

/** The same task as the daemon writes it in YAML; the cron is on line 9, among the lines CodeMirror draws. */
const taskYaml = (name: string, cron = CRON) =>
  [
    "apiVersion: harmflow/v1alpha1",
    "kind: Task",
    "metadata:",
    "  id: standup",
    `  name: ${name}`,
    "spec:",
    "  type: general",
    "  schedule:",
    `    cron: "${cron}"`,
    "  prompt: Write the standup notes.",
    "",
  ].join("\n");

/** The document of this task's YAML; the stand-in parses no other text. */
function parse(yaml: string): Doc | undefined {
  const name = /^ {2}name: (.*)$/m.exec(yaml)?.[1];
  const cron = /^ {4}cron: "(.*)"$/m.exec(yaml)?.[1];
  return name !== undefined && cron !== undefined && yaml === taskYaml(name, cron) ? taskDoc(name, cron) : undefined;
}

/** The YAML of a document of this task; a document the stand-in cannot write fails the request. */
function write(doc: Doc): string {
  const name = (doc.metadata as { name: string }).name;
  const cron = (doc.spec as { schedule: { cron: string } }).schedule.cron;
  if (!isDeepStrictEqual(doc, taskDoc(name, cron))) throw new Error(`The stand-in cannot write ${JSON.stringify(doc)}`);
  return taskYaml(name, cron);
}

/** The daemon's refusal of BAD_CRON, at the field's path and at its key's line and column in the YAML. */
const cronError: ValidationIssue = {
  path: "/spec/schedule/cron",
  message: 'minute field "61" is out of range 0-59',
  messageKey: "cron_value_out_of_range",
  params: { field: "minute", value: "61", min: "0", max: "59" },
  line: 9,
  column: 5,
};
const cronErrorText = 'minute field "61" is out of range 0-59 — line 9, column 5 /spec/schedule/cron';

const syntaxError: ValidationIssue = {
  message: "YAML syntax error: found unexpected end of stream",
  messageKey: "yaml_syntax",
  params: { detail: "found unexpected end of stream" },
};

/** A task named REVIEW_NOTES cites a skill only Claude Code has, which its Codex fallback cannot load. */
const REVIEW_NOTES = "Review notes";

/** The daemon's warning that Codex is not a valid fallback for that task, as the daemon sends it. */
const fallbackWarning: ValidationIssue = {
  path: "/spec/harness/fallback/candidates/0",
  code: "invalid_fallback",
  message: 'Codex is not a valid fallback: skill "review-notes" cannot be found for it (the Codex configuration)',
  messageKey: "fallback_skill_missing",
  params: { name: "review-notes", harness: "Codex", where: "the Codex configuration", source: "harness" },
};
const fallbackWarningText =
  'Invalid fallback: Codex is not a valid fallback: skill "review-notes" cannot be found for it (the Codex configuration) ' +
  "/spec/harness/fallback/candidates/0";

const warningsFor = (document: Doc) => ((document.metadata as { name: string }).name === REVIEW_NOTES ? [fallbackWarning] : []);

/**
 * The daemon's validation of an input, the same for the editor's live checks, its tab switches, and its saves, so the
 * order the debounced live checks arrive in changes nothing a test reads.
 */
function validate(input: TaskInput): ValidationResult {
  const document = input.document ?? parse(input.yaml ?? "");
  if (!document) return { valid: false, errors: [syntaxError], warnings: [] };
  const errors = (document.spec as { schedule: { cron: string } }).schedule.cron === BAD_CRON ? [cronError] : [];
  return { valid: errors.length === 0, errors, warnings: warningsFor(document), document, effective: document, yaml: write(document) };
}

const storedTask = (document: Doc, revision: string): Task => ({
  id: "standup",
  name: (document.metadata as { name: string }).name,
  type: "general",
  enabled: true,
  revision,
  harness: "claude-code",
  warnings: [],
  document,
  effective: document,
  yaml: write(document),
  path: "standup.yaml",
  validation: { valid: true, errors: [], warnings: warningsFor(document) },
});

/** Answers the editor's routes; a save the validation passes is stored under the next revision. */
function serve(revision: number) {
  const save = (status: number) => (input: unknown) => {
    const validation = validate(input as TaskInput);
    if (!validation.valid) {
      return { status: 422, body: { error: { code: "validation_failed", message: "the task fails validation", messageKey: "task_invalid", validation } } };
    }
    return { status, body: storedTask(validation.document as Doc, `r${++revision}`) };
  };
  daemon.routes.set("POST /tasks/validate", (input) => ({ status: 200, body: validate(input as TaskInput) }));
  daemon.routes.set("PUT /tasks/standup", save(200));
  daemon.routes.set("POST /tasks", save(201));
  daemon.routes.set("GET /tasks", () => ({ status: 200, body: [] }));
  daemon.routes.set("POST /schedules/preview", () => ({ status: 200, body: { occurrences: [] } }));
}

let root: Root | undefined;
let host: HTMLElement | undefined;
let onSaved: Mock<(task: Task) => void>;

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

function close() {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
  daemon.routes.clear();
  daemon.requests.length = 0;
}

afterEach(close);

/** Lets the stand-in daemon's answers arrive and React render them. */
const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

const click = async (target: HTMLElement) => {
  await act(async () => target.click());
  await settle();
};

/** Opens the editor on the stored task at r1, or on a new one from its template, with the app's toasts beside it. */
async function open(mode: "create" | "edit") {
  serve(mode === "edit" ? 1 : 0);
  onSaved = vi.fn();
  const from = mode === "edit" ? { task: storedTask(taskDoc("Standup notes"), "r1") } : { initial: taskDoc("Standup notes") };
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <>
        <TaskEditor mode={mode} {...from} onClose={() => {}} onSaved={onSaved} />
        <Toasts />
      </>,
    ),
  );
  await settle();
}

const tab = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((b) => b.textContent === label)!;
const openTab = () => host!.querySelector('[role="tab"][aria-selected="true"]')!.textContent;
const save = () => click([...host!.querySelectorAll<HTMLButtonElement>(".editor__actions button")].find((b) => b.textContent === "Save")!);
const saveButton = () => host!.querySelector<HTMLButtonElement>('.editor__actions button[aria-keyshortcuts="Meta+S"]')!;
const tooltip = () => document.body.querySelector(".tooltip")?.textContent ?? null;
const unsaved = () => host!.querySelector(".editor__dirty")?.textContent ?? null;
const toasts = (tone: string) => [...host!.querySelectorAll(`.toast--${tone}`)].map((t) => t.textContent);

const field = (label: string) =>
  [...host!.querySelectorAll<HTMLElement>("label.field")].find((f) => f.querySelector(".field__label")?.textContent === label)!;
const nameField = () => field("Name");
const cronField = () => field("Cron expression (5 fields)");

/** The validation panel, and the errors it lists. */
const panel = () => host!.querySelector(".editor__side .validation")!;
const errors = () => [...panel().querySelectorAll(".validation__item")].map((li) => li.textContent);
/** The warnings the editor's side lists. */
const warnings = () => [...host!.querySelectorAll(".editor__side .validation__item--warning")].map((li) => li.textContent);

const editor = () => EditorView.findFromDOM(host!.querySelector<HTMLElement>(".cm-editor")!)!;
const yamlText = () => editor().state.doc.toString();
const markedLines = () => [...host!.querySelectorAll(".cm-line.cm-error-line")].map((line) => line.textContent);

/** The bodies of the requests sent to one route, in order. */
const sent = (method: string, path: string) => daemon.requests.filter((r) => r.method === method && r.path === path).map((r) => r.body);

/** Types into a text field as React sees it. */
async function type(input: HTMLInputElement, value: string) {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

/** Chooses an option of a select as React sees it. */
async function choose(select: HTMLSelectElement, value: string) {
  const setValue = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(select, value);
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

/** Replaces the YAML editor's text as typing would: a change of the user's, which reaches the editor's onChange. */
async function typeYaml(text: string) {
  const view = editor();
  await act(async () => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } }));
}

/** Presses ⌘S (or the given keys) on a target, where focus is by default; returns whether the editor took the key. */
async function press(target: EventTarget = document.activeElement ?? document.body, keys: KeyboardEventInit = {}): Promise<boolean> {
  const event = new KeyboardEvent("keydown", { key: "s", metaKey: true, bubbles: true, cancelable: true, ...keys });
  await act(async () => void target.dispatchEvent(event));
  await settle();
  return event.defaultPrevented;
}

describe("task editor tabs", () => {
  it("carries an edit in the form to the YAML tab, as the daemon writes it", async () => {
    await open("edit");
    await type(nameField().querySelector("input")!, "Standup summary");
    await click(tab("YAML"));
    expect(sent("POST", "/tasks/validate")).toContainEqual({ document: taskDoc("Standup summary") });
    expect(openTab()).toBe("YAML");
    expect(yamlText()).toBe(taskYaml("Standup summary"));
  });

  it("carries an edit in the YAML editor to the form", async () => {
    await open("edit");
    await click(tab("YAML"));
    await typeYaml(taskYaml("Standup summary"));
    expect(unsaved()).toBe("Unsaved changes");
    await click(tab("Form"));
    expect(sent("POST", "/tasks/validate")).toContainEqual({ yaml: taskYaml("Standup summary") });
    expect(openTab()).toBe("Form");
    expect(nameField().querySelector("input")!.value).toBe("Standup summary");
  });

  it("stays on the YAML tab with the user's text when the YAML does not convert, and says why", async () => {
    await open("edit");
    await click(tab("YAML"));
    const unterminated = taskYaml("Standup notes").replace(`cron: "${CRON}"`, `cron: "${CRON}`);
    await typeYaml(unterminated);
    await click(tab("Form"));
    expect(sent("POST", "/tasks/validate")).toContainEqual({ yaml: unterminated });
    expect(openTab()).toBe("YAML");
    expect(yamlText()).toBe(unterminated);
    expect(unsaved()).toBe("Unsaved changes");
    expect(toasts("warning")).toContain("The YAML could not be converted to the form; fix the errors first.");
    expect(errors()).toEqual(["YAML syntax error: found unexpected end of stream"]);
  });
});

describe("task editor save from the YAML tab", () => {
  it("sends the YAML with the revision the task was loaded with, and the form shows the saved change", async () => {
    await open("edit");
    await click(tab("YAML"));
    await typeYaml(taskYaml("Standup summary"));
    await save();
    expect(sent("PUT", "/tasks/standup")).toEqual([{ yaml: taskYaml("Standup summary"), baseRevision: "r1" }]);
    expect(unsaved()).toBeNull();
    expect(onSaved).toHaveBeenCalledExactlyOnceWith(storedTask(taskDoc("Standup summary"), "r2"));

    await click(tab("Form"));
    expect(nameField().querySelector("input")!.value).toBe("Standup summary");
  });

  it("refuses an invalid change with the daemon's error at its line, and keeps the text and the last saved revision", async () => {
    await open("edit");
    await click(tab("YAML"));
    await typeYaml(taskYaml("Standup summary"));
    await save();

    const invalid = taskYaml("Standup summary", BAD_CRON);
    await typeYaml(invalid);
    await save();
    expect(errors()).toEqual([cronErrorText]);
    expect(markedLines()).toEqual([`    cron: "${BAD_CRON}"`]);
    expect(yamlText()).toBe(invalid);
    expect(unsaved()).toBe("Unsaved changes");
    expect(onSaved).toHaveBeenCalledTimes(1);

    const fixed = taskYaml("Standup summary", "30 9 * * 1-5");
    await typeYaml(fixed);
    await save();
    expect(sent("PUT", "/tasks/standup")).toEqual([
      { yaml: taskYaml("Standup summary"), baseRevision: "r1" },
      { yaml: invalid, baseRevision: "r2" },
      { yaml: fixed, baseRevision: "r2" },
    ]);
    expect(unsaved()).toBeNull();
    expect(errors()).toEqual([]);
    expect(markedLines()).toEqual([]);
  });

  it("shows the same error as a save of the same change from the form, which marks the Cron field", async () => {
    await open("edit");
    await click(tab("YAML"));
    const invalid = taskYaml("Standup notes", BAD_CRON);
    await typeYaml(invalid);
    await save();
    expect(sent("PUT", "/tasks/standup")).toEqual([{ yaml: invalid, baseRevision: "r1" }]);
    expect(errors()).toEqual([cronErrorText]);
    const fromYaml = panel().textContent;
    close();

    // A fresh editor, whose panel reads Valid until the form's save is answered. The task's expression opens as
    // Weekdays, whose controls cannot write minute 61, so it is typed under Custom.
    await open("edit");
    await choose(field("Repeats").querySelector("select")!, "custom");
    await type(cronField().querySelector("input")!, BAD_CRON);
    await save();
    expect(sent("PUT", "/tasks/standup")).toEqual([{ document: taskDoc("Standup notes", BAD_CRON), baseRevision: "r1" }]);
    expect(panel().textContent).toBe(fromYaml);
    expect(cronField().classList).toContain("field--error");
    expect(cronField().querySelector("input")!.value).toBe(BAD_CRON);
    expect(unsaved()).toBe("Unsaved changes");
  });

  it("creates a new task from the YAML alone, refusing an invalid one as an edit does", async () => {
    await open("create");
    await click(tab("YAML"));
    expect(yamlText()).toBe(taskYaml("Standup notes"));

    const invalid = taskYaml("Release notes", BAD_CRON);
    await typeYaml(invalid);
    await save();
    expect(errors()).toEqual([cronErrorText]);
    expect(markedLines()).toEqual([`    cron: "${BAD_CRON}"`]);
    expect(yamlText()).toBe(invalid);
    expect(unsaved()).toBe("Unsaved changes");
    expect(onSaved).not.toHaveBeenCalled();

    const valid = taskYaml("Release notes");
    await typeYaml(valid);
    await save();
    expect(sent("POST", "/tasks")).toEqual([{ yaml: invalid }, { yaml: valid }]);
    expect(sent("PUT", "/tasks/standup")).toEqual([]);
    expect(onSaved).toHaveBeenCalledExactlyOnceWith(storedTask(taskDoc("Release notes"), "r1"));
    expect(unsaved()).toBeNull();
  });
});

describe("task editor warnings", () => {
  it("shows the daemon's warning that a fallback cannot load the task's skill once the task is saved", async () => {
    await open("edit");
    expect(warnings()).toEqual([]);
    await type(nameField().querySelector("input")!, REVIEW_NOTES);
    await save();
    expect(sent("PUT", "/tasks/standup")).toEqual([{ document: taskDoc(REVIEW_NOTES), baseRevision: "r1" }]);
    expect(toasts("success")).toContain("Saved.");
    expect(warnings()).toEqual([fallbackWarningText]);
  });
});

describe("task editor ⌘S", () => {
  it("updates an existing task from a text field of the form, as Save does, with the revision of the last save", async () => {
    await open("edit");
    const name = nameField().querySelector("input")!;
    await type(name, "Standup summary");
    name.focus();
    expect(await press(name)).toBe(true);
    expect(sent("PUT", "/tasks/standup")).toEqual([{ document: taskDoc("Standup summary"), baseRevision: "r1" }]);
    expect(toasts("success")).toContain("Saved.");
    expect(unsaved()).toBeNull();
    expect(onSaved).toHaveBeenCalledExactlyOnceWith(storedTask(taskDoc("Standup summary"), "r2"));

    await type(name, "Standup digest");
    expect(await press(name)).toBe(true);
    expect(sent("PUT", "/tasks/standup")[1]).toEqual({ document: taskDoc("Standup digest"), baseRevision: "r2" });
  });

  it("creates a new task from the YAML editor, refusing an invalid one with the errors Save shows", async () => {
    await open("create");
    await click(tab("YAML"));
    const content = host!.querySelector<HTMLElement>(".cm-content")!;
    const invalid = taskYaml("Release notes", BAD_CRON);
    await typeYaml(invalid);
    expect(await press(content)).toBe(true);
    expect(errors()).toEqual([cronErrorText]);
    expect(markedLines()).toEqual([`    cron: "${BAD_CRON}"`]);
    expect(unsaved()).toBe("Unsaved changes");
    expect(onSaved).not.toHaveBeenCalled();

    const valid = taskYaml("Release notes");
    await typeYaml(valid);
    expect(await press(content)).toBe(true);
    expect(sent("POST", "/tasks")).toEqual([{ yaml: invalid }, { yaml: valid }]);
    expect(toasts("success")).toContain("Saved.");
    expect(onSaved).toHaveBeenCalledExactlyOnceWith(storedTask(taskDoc("Release notes"), "r1"));
  });

  it("saves with no unsaved change too, and shows the conflict banner when the task changed on disk", async () => {
    await open("edit");
    daemon.routes.set("PUT /tasks/standup", () => ({
      status: 409,
      body: { error: { code: "revision_conflict", message: "the task changed since it was read", currentRevision: "r7" } },
    }));
    expect(await press()).toBe(true);
    expect(sent("PUT", "/tasks/standup")).toEqual([{ document: taskDoc("Standup notes"), baseRevision: "r1" }]);
    expect(host!.querySelector(".banner--danger")!.textContent).toContain("The file changed on disk");
    expect(onSaved).not.toHaveBeenCalled();
  });

  it("sends one request while a save is in progress, however fast the presses come", async () => {
    await open("edit");
    let answer!: () => void;
    daemon.routes.set(
      "PUT /tasks/standup",
      () => new Promise<Answer>((resolve) => (answer = () => resolve({ status: 200, body: storedTask(taskDoc("Standup notes"), "r2") }))),
    );
    // Two presses in one moment, before the first one's saving state renders, then one more once the button shows it.
    await act(async () => {
      for (let i = 0; i < 2; i++) window.dispatchEvent(new KeyboardEvent("keydown", { key: "s", metaKey: true, cancelable: true }));
    });
    expect(saveButton().disabled).toBe(true);
    expect(await press()).toBe(true);
    expect(sent("PUT", "/tasks/standup")).toHaveLength(1);

    await act(async () => answer());
    await settle();
    expect(saveButton().disabled).toBe(false);
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(await press()).toBe(true);
    expect(sent("PUT", "/tasks/standup")).toHaveLength(2);
  });

  it("takes ⌘S alone, not with another modifier or as a held key", async () => {
    await open("edit");
    for (const keys of [{ shiftKey: true }, { altKey: true }, { ctrlKey: true }, { repeat: true }, { metaKey: false }]) {
      expect(await press(undefined, keys)).toBe(false);
    }
    expect(sent("PUT", "/tasks/standup")).toEqual([]);
  });

  it("leaves ⌘S alone while a modal dialog is open", async () => {
    await open("edit");
    const dialog = document.body.appendChild(document.createElement("div"));
    dialog.setAttribute("role", "dialog");
    dialog.setAttribute("aria-modal", "true");
    try {
      expect(await press()).toBe(false);
      expect(sent("PUT", "/tasks/standup")).toEqual([]);
    } finally {
      dialog.remove();
    }
    expect(await press()).toBe(true);
    expect(sent("PUT", "/tasks/standup")).toHaveLength(1);
  });

  it("names ⌘S in the Save button's tooltip, in each language", async () => {
    await open("edit");
    expect(saveButton().getAttribute("aria-keyshortcuts")).toBe("Meta+S");
    try {
      for (const [language, label] of [
        ["en-US", "Save"],
        ["pt-BR", "Salvar"],
        ["es-ES", "Guardar"],
      ]) {
        await act(async () => void i18n.changeLanguage(language));
        await act(async () => void saveButton().dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
        expect(tooltip()).toBe(`${label}⌘S`);
        await act(async () => void saveButton().dispatchEvent(new MouseEvent("mouseout", { bubbles: true })));
        expect(tooltip()).toBeNull();
      }
    } finally {
      await act(async () => void i18n.changeLanguage("en-US"));
    }
  });
});
