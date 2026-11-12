// @vitest-environment happy-dom
// Drives the task form in a DOM, against a stand-in for the daemon's local API; the tests of a single part of the form
// sit beside that part, in taskform/. A new GitHub task with every section
// filled through its controls saves the document tests/integration/testdata/task-form-document.json holds, which
// itest › TestFormDocumentIsTheFile writes as a file (harmflow-app#299), and a new script task the document
// script-task-form-document.json holds, which itest › TestScriptFormDocumentIsTheFile writes (harmflow-app#707).
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { Catalog, Harness, SchedulePreview, Task } from "../api/types";
import i18n from "../i18n";
import type { Doc } from "../lib/taskForm";
import { TasksScreen, type TaskView } from "../screens/TasksScreen";
import { patchStateForTests } from "../state/store";
import { TaskForm } from "./TaskForm";

// The daemon as the client sees it: an answer per method and route, or per route alone, and every request it received
// with its body as JSON carries it, where a field left undefined is not sent.
const daemon = vi.hoisted(() => ({
  answers: new Map<string, unknown>(),
  requests: [] as { method: string; path: string; body: unknown }[],
}));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string, body?: unknown) => {
      daemon.requests.push({ method, path, body: body === undefined ? undefined : JSON.parse(JSON.stringify(body)) });
      const key = [`${method} ${path}`, path].find((k) => daemon.answers.has(k));
      return key ? { status: 200, body: daemon.answers.get(key) } : { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

let root: Root | undefined;
let host: HTMLElement | undefined;

// A host in São Paulo's zone would list the occurrences at 09:00 whichever zone the form used, so the host is UTC.
beforeAll(async () => {
  vi.stubEnv("TZ", "UTC");
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

afterAll(() => {
  vi.unstubAllEnvs();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
  daemon.answers.clear();
  daemon.requests.length = 0;
});

/** Lets the stand-in daemon's answers arrive and React render them. */
const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

const task = (schedule: Record<string, string>): Doc => ({
  apiVersion: "harmflow/v1alpha1",
  kind: "Task",
  metadata: { id: "standup", name: "Standup notes" },
  spec: { type: "general", prompt: "Write the standup notes.", harness: { preferred: { provider: "claude-code" } }, schedule },
});

async function open(doc: Doc, mode: "create" | "edit" = "edit") {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () => root!.render(<TaskForm doc={doc} onChange={() => {}} mode={mode} harnesses={[]} errorPaths={new Set()} />));
  await settle();
}

// The type is chosen in the template picker and fixed from then on; the form shows it and follows it. The daemon's
// refusal of a changed type is app › TestUpdateKeepsTheType's.
describe("task form type", () => {
  const typeInput = () =>
    [...host!.querySelectorAll("label.field")]
      .find((field) => field.querySelector(".field__label")?.textContent === "Task type")
      ?.querySelector("input");
  const sections = () => [...host!.querySelectorAll("section.form-section")].map((s) => s.id);

  it.each(["create", "edit"] as const)("shows the type read-only in %s mode", async (mode) => {
    await open(task({}), mode);
    expect(typeInput()?.value).toBe("General");
    expect(typeInput()?.disabled).toBe(true);
    expect(host!.querySelector("#section-general select")).toBeNull();
  });

  it("shows the sections of the document's type", async () => {
    await open(
      { apiVersion: "harmflow/v1alpha1", kind: "Task", metadata: { id: "audit", name: "Audit" }, spec: { type: "script", script: { command: ["/bin/date"] } } },
      "create",
    );
    expect(typeInput()?.value).toBe("Script");
    expect(sections()).toContain("section-script");
    expect(sections()).not.toContain("section-prompt");
    expect(sections()).not.toContain("section-harness");
  });
});

// The task form on the Tasks screen, reached as the user reaches it: each control by the label it shows, filled through
// the control itself. The "task form document" tests of a GitHub task and of a script task share these.

/** A document kept in tests/integration/testdata, which an integration test writes as a file. */
const fixture = (name: string): Doc =>
  JSON.parse(readFileSync(join(__dirname, "..", "..", "..", "..", "tests", "integration", "testdata", name), "utf8"));

/** Opens the Tasks screen on the view, against the stand-in daemon's answers set before. */
async function openScreen(view: TaskView) {
  daemon.answers.set("/schedules/preview", { occurrences: [] } satisfies SchedulePreview);
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () => root!.render(<TasksScreen view={view} onView={() => {}} onOpenExecution={() => {}} />));
  await settle();
}

// A group of checkboxes is a `div.field`; any other field is a `label.field`.
const field = (label: string) =>
  [...host!.querySelectorAll<HTMLElement>(".field")].find((f) => f.querySelector(".field__label")?.textContent === label)!;
const control = <E extends Element = HTMLInputElement>(label: string) => field(label).querySelector<E>("input, select, textarea, output")!;
const toggle = (label: string) =>
  [...host!.querySelectorAll("label.toggle")].find((t) => t.querySelector(".toggle__label")?.textContent === label)!.querySelector("input")!;
const boxes = (fieldLabel: string) => [...field(fieldLabel).querySelectorAll("label.checkbox")];
const checked = (fieldLabel: string) => boxes(fieldLabel).filter((c) => c.querySelector("input")!.checked).map((c) => c.textContent);
const button = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label)!;
/** A name picker's remove buttons, one per chip, each the last child of its chip. */
const removeButtons = (within: HTMLElement) => [...within.querySelectorAll<HTMLButtonElement>('button[aria-label="Remove"]')];
const radio =(label: string) => [...host!.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find((b) => b.textContent === label)!;
const sent = (method: string, path: string) => daemon.requests.filter((r) => r.method === method && r.path === path).map((r) => r.body);

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
}
/** Clicks one of a field's checkboxes. */
async function tick(fieldLabel: string, label: string) {
  await click(boxes(fieldLabel).find((c) => c.textContent === label)!.querySelector("input")!);
}

// A new GitHub task, every section filled through its controls, saves the document task-form-document.json holds, which
// itest › TestFormDocumentIsTheFile writes as an equal file: the two tests cover the way from the form to the file. That
// document opens back in its controls, and clearing a control removes its key from what is sent, so the default applies.
describe("task form document", () => {
  const FORM_DOCUMENT = fixture("task-form-document.json");
  const entry = (name: string, kind: "agent" | "skill", source: "builtin" | "user") => ({ name, kind, source, harnesses: ["claude-code", "codex"] });
  // The template's draft selects no agent, so it brings no agent, skill, or origin of its own and every list is filled
  // through its control. The catalog's entries let a whole name typed in a picker add the chip.
  const catalog: Catalog = {
    agents: [entry("issue-reviewer", "agent", "user")],
    skills: ["hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"].map((name) => entry(name, "skill", "builtin")),
    problems: [],
    taskTypes: [],
  };
  // The Effort selects list the efforts of the model typed.
  const harness = (id: Harness["id"], name: string, model: string): Harness => ({
    id,
    name,
    detected: true,
    installations: [],
    authentication: "valid",
    readiness: "ready",
    models: [{ id: model, efforts: ["low", "high"] }],
    efforts: ["low", "high"],
    lastCheckedAt: "2026-10-27T12:00:00Z",
  });
  const stored = (document: Doc): Task => ({
    id: "complete",
    name: "Every section",
    type: "github",
    enabled: false,
    revision: "r1",
    harness: "codex",
    warnings: [],
    document,
    effective: document,
    yaml: JSON.stringify(document, null, 2) + "\n",
    path: "complete.yaml",
    validation: { valid: true, errors: [], warnings: [] },
  });

  beforeAll(() => patchStateForTests({ catalog, harnesses: [harness("claude-code", "Claude Code", "sonnet"), harness("codex", "Codex", "gpt-5.6-terra")] }));
  afterAll(() => patchStateForTests({ catalog: undefined, harnesses: [], tasks: [], tasksLoaded: false }));

  async function openTask(view: TaskView) {
    daemon.answers.set("POST /tasks", stored(FORM_DOCUMENT));
    daemon.answers.set("GET /tasks", []);
    daemon.answers.set("GET /tasks/complete", stored(FORM_DOCUMENT));
    daemon.answers.set("PUT /tasks/complete", stored(FORM_DOCUMENT));
    await openScreen(view);
  }

  const chips = (label: string) => removeButtons(field(label)).map((b) => b.parentElement!.querySelector(".mono")!.textContent);
  /** The first repository row's fields, each by its column's name. */
  const repository = () =>
    ["Repository (owner/repo)", "Local path", "Base branch"].map((name) => host!.querySelector<HTMLInputElement>(`input[aria-label="${name}"]`)!);
  /** The preferred harness, then each fallback: its harness, model, and effort controls. */
  const profiles = () =>
    [...host!.querySelectorAll("#section-harness .grid-profile")].map((p) => {
      const [harness, effort] = p.querySelectorAll("select");
      return { harness: harness!, model: p.querySelector("input")!, effort: effort! };
    });

  it("sends, once every section is filled through its controls, the document the daemon writes as a file", async () => {
    // The daemon's draft of a GitHub template that selects no agent: one repository row for the editor to ask for.
    daemon.answers.set("POST /templates/hf-github-issues/draft", {
      document: {
        apiVersion: "harmflow/v1alpha1",
        kind: "Task",
        metadata: { id: "implement-github-issues", name: "Implement GitHub issues" },
        spec: {
          enabled: true,
          type: "github",
          harness: { preferred: { provider: "claude-code" } },
          github: { repositories: [{ baseBranch: "main" }] },
        },
      },
    });
    await openTask({ kind: "new", template: "hf-github-issues" });

    // General. The name writes an identifier of its own until the identifier is typed.
    await enter(control("Name"), "Every section");
    expect(control("Identifier").value).toBe("every-section");
    await enter(control("Identifier"), "complete");
    await enter(control<HTMLTextAreaElement>("Description"), "Every section of the form, set.");
    await click(toggle("Enabled"));

    // Prompt.
    await enter(control<HTMLTextAreaElement>("Prompt"), "Implement the oldest issue labelled ready-for-agent.");
    await enter(control("Other agents (subagents)"), "issue-reviewer");
    for (const skill of ["hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"]) await enter(control("Skills"), skill);
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
    await tick("Fail over on", "Quota exhausted");
    await enter(control("Maximum switches"), "2");

    // Schedule: weekdays at 06:30 in São Paulo, skipping missed and overlapping firings.
    await click(radio("Cron"));
    await enter(control("Time"), "06:30");
    await enter(control("Time zone"), "America/Sao_Paulo");
    await enter(control<HTMLSelectElement>("Missed runs"), "skip");
    await enter(control<HTMLSelectElement>("Overlapping firings"), "skip");

    // Permissions: a new task's Profile control offers exactly the three profiles, after its first option, which leaves
    // the default. The custom profile's lists show once it is chosen, its read paths and subprocess controls among them.
    const profileOptions = [...control<HTMLSelectElement>("Profile").options].map((o) => o.value);
    expect(profileOptions).toEqual(["", "read_only", "workspace_write", "custom"]);
    expect(field("Read paths")).toBeUndefined();
    expect(field("Allowed programs")).toBeUndefined();
    expect(field("Denied programs")).toBeUndefined();
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

    await click(button("Save"));
    await settle();
    // Strictly equal: a number sent as a string, or a key the form left in, fails.
    expect(sent("POST", "/tasks")).toStrictEqual([{ document: FORM_DOCUMENT }]);
  });

  it("shows the document in its controls, and sends no key of a cleared control, so its default applies", async () => {
    await openTask({ kind: "edit", id: "complete" });
    expect(control("Identifier").disabled).toBe(true);
    const values = (...labels: string[]) => labels.map((label) => control(label).value);
    const [preferred, fallback] = profiles().map((p) => [p.harness.value, p.model.value, p.effort.value]);
    expect({
      general: [...values("Name", "Identifier", "Description"), toggle("Enabled").checked],
      prompt: [control("Prompt").value, chips("Other agents (subagents)"), chips("Skills"), control("Workspace").value],
      repository: repository().map((input) => input.value),
      queue: values(
        "Order",
        "Items discovered per cycle",
        "Items processed per execution",
        "Resume priority",
        "Resumptions before needs attention",
        "Retries of a failed item",
        "Test-repair attempts",
      ),
      harness: [preferred, fallback, checked("Fail over on"), control("Maximum switches").value],
      schedule: [...values("Repeats", "Time"), control("Cron expression (5 fields)").textContent, ...values("Time zone", "Missed runs", "Overlapping firings")],
      permissions: [
        ...values("Profile", "Network"),
        toggle("Restrict writes to the workspace").checked,
        ...values("Allowed tools", "Denied tools", "Writable paths", "Read paths", "Allowed programs", "Denied programs"),
      ],
      execution: values(
        "Timeout (minutes), shared by all attempts",
        "Grace before a cancellation is forced (seconds)",
        "Rate-limit retries",
        "First wait (seconds)",
        "Longest wait (seconds)",
      ),
      notifications: [toggle("Native notifications").checked, checked("Notify me about")],
    }).toEqual({
      general: ["Every section", "complete", "Every section of the form, set.", false],
      prompt: [
        "Implement the oldest issue labelled ready-for-agent.",
        ["issue-reviewer"],
        ["hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"],
        "~/harmflow-work",
      ],
      repository: ["harmflow/example", "${FORM_REPO}", "develop"],
      queue: ["newest_first", "5", "2", "alternate", "1", "0", "0"],
      harness: [["codex", "gpt-5.6-terra", "high"], ["claude-code", "sonnet", "high"], ["AI tool unavailable"], "2"],
      schedule: ["weekdays", "06:30", "30 6 * * 1-5", "America/Sao_Paulo", "skip", "skip"],
      permissions: ["custom", "deny", false, "Read\nGrep", "WebFetch", "${FORM_REPO}", "${FORM_REPO}", "git\ngh", "curl"],
      execution: ["45", "10", "2", "30", "600"],
      notifications: [false, ["Operational failure", "Tests failed"]],
    });

    // One control of each section cleared. The two lists emptied show their defaults again, as the daemon applies them.
    await enter(control<HTMLTextAreaElement>("Description"), "");
    await enter(control("Workspace"), "");
    await enter(control("Items discovered per cycle"), "");
    await tick("Fail over on", "AI tool unavailable");
    expect(checked("Fail over on")).toEqual(["Quota exhausted", "AI tool unavailable"]);
    await enter(control("Time zone"), "");
    await enter(control<HTMLSelectElement>("Network"), "");
    await enter(control<HTMLTextAreaElement>("Read paths"), "");
    await enter(control<HTMLTextAreaElement>("Denied programs"), "");
    await enter(control("Timeout (minutes), shared by all attempts"), "");
    for (const event of ["Operational failure", "Tests failed"]) await tick("Notify me about", event);
    expect(checked("Notify me about")).toEqual(["Execution completed", "Operational failure", "Needs attention", "Draft PR created", "No alternative AI tool"]);
    await click(button("Save"));
    await settle();

    const expected = structuredClone(FORM_DOCUMENT);
    for (const path of [
      ["metadata", "description"],
      // The workspace held its path alone, so it goes with it.
      ["spec", "workspace"],
      ["spec", "github", "queue", "maxDiscoveredPerCycle"],
      ["spec", "harness", "fallback", "on"],
      ["spec", "schedule", "timezone"],
      ["spec", "permissions", "network"],
      ["spec", "permissions", "custom", "readablePaths"],
      ["spec", "permissions", "custom", "deniedPrograms"],
      ["spec", "execution", "timeoutMinutes"],
      ["spec", "notifications", "native", "events"],
    ]) {
      const parent = path.slice(0, -1).reduce((node, key) => node[key] as Doc, expected);
      delete parent[path.at(-1)!];
    }
    expect(sent("PUT", "/tasks/complete")).toStrictEqual([{ document: expected, baseRevision: "r1" }]);
  });
});

// A new script task, every section of its form filled through its controls, saves the document
// script-task-form-document.json holds, which itest › TestScriptFormDocumentIsTheFile writes as an equal file. The form
// hides a harness task's Prompt and Harness sections and shows the Script section, so it has a fixture of its own. Its
// program is a command or an inline program, never both, and the document opens back in its controls.
describe("script task form document", () => {
  const SCRIPT_DOCUMENT = fixture("script-task-form-document.json");
  const COMMAND = "/bin/date\n-u\n+%Y-%m-%d %H:%M";
  const stored = (document: Doc): Task => ({
    id: "script-complete",
    name: "Every script section",
    type: "script",
    enabled: false,
    revision: "r1",
    harness: "",
    warnings: [],
    document,
    effective: document,
    yaml: JSON.stringify(document, null, 2) + "\n",
    path: "script-complete.yaml",
    validation: { valid: true, errors: [], warnings: [] },
  });
  /** The fixture with its command replaced by an inline program. */
  const inline = (interpreter: string[], source: string): Doc => {
    const doc = structuredClone(SCRIPT_DOCUMENT);
    const script = (doc.spec as Doc).script as Doc;
    delete script.command;
    script.inline = { interpreter, source };
    return doc;
  };

  async function openTask(view: TaskView, document = SCRIPT_DOCUMENT) {
    daemon.answers.set("POST /tasks", stored(document));
    daemon.answers.set("GET /tasks", []);
    daemon.answers.set("GET /tasks/script-complete", stored(document));
    daemon.answers.set("PUT /tasks/script-complete", stored(document));
    await openScreen(view);
  }

  it("sends, once every section is filled through its controls, the document the daemon writes as a file", async () => {
    // The daemon's draft of the script template: a program for the editor to ask for.
    daemon.answers.set("POST /templates/hf-script/draft", {
      document: {
        apiVersion: "harmflow/v1alpha1",
        kind: "Task",
        metadata: { id: "script-task", name: "Script task" },
        spec: { enabled: true, type: "script", script: {} },
      },
    });
    await openTask({ kind: "new", template: "hf-script" });
    expect(host!.querySelector("#section-prompt")).toBeNull();
    expect(host!.querySelector("#section-harness")).toBeNull();

    // General. The name writes an identifier of its own until the identifier is typed.
    await enter(control("Name"), "Every script section");
    expect(control("Identifier").value).toBe("every-script-section");
    await enter(control("Identifier"), "script-complete");
    await enter(control<HTMLTextAreaElement>("Description"), "Every section of the script form, set.");
    await click(toggle("Enabled"));

    // Script: a command of three arguments, the last with a space inside it, kept whole.
    expect(radio("A program").getAttribute("aria-checked")).toBe("true");
    await enter(control<HTMLTextAreaElement>("Program and arguments"), COMMAND);
    await enter(control("Working directory"), "${FORM_REPO}");
    await tick("Runs on", "macOS");
    await tick("Runs on", "Linux");
    await enter(control<HTMLTextAreaElement>("Environment variables"), "AUDIT_LABEL=Daily audit\nAUDIT_LEVEL=full");

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

    // Execution: a script task has no rate-limit retries and no extra usage.
    expect(field("Rate-limit retries")).toBeUndefined();
    expect([...host!.querySelectorAll(".toggle__label")].map((l) => l.textContent)).not.toContain("Allow extra usage");
    await enter(control("Timeout (minutes), shared by all attempts"), "45");
    await enter(control("Grace before a cancellation is forced (seconds)"), "10");

    // Notifications: off, and of the default events only operational failures kept, with cancellations added.
    await click(toggle("Native notifications"));
    for (const event of ["Execution completed", "Needs attention", "Draft PR created", "No alternative AI tool"]) await tick("Notify me about", event);
    await tick("Notify me about", "Cancellation");

    await click(button("Save"));
    await settle();
    // Strictly equal: a number sent as a string, the command split on its space, or a key the form left in, fails.
    expect(sent("POST", "/tasks")).toStrictEqual([{ document: SCRIPT_DOCUMENT }]);
  });

  it("sends an inline program and no command once the program is switched to inline", async () => {
    await openTask({ kind: "edit", id: "script-complete" });
    await click(radio("An inline script"));
    expect(field("Program and arguments")).toBeUndefined();
    await enter(control<HTMLTextAreaElement>("Interpreter"), "/bin/sh\n-e");
    await enter(control<HTMLTextAreaElement>("Script"), "date -u\necho done\n");
    await click(button("Save"));
    await settle();
    expect(sent("PUT", "/tasks/script-complete")).toStrictEqual([
      { document: inline(["/bin/sh", "-e"], "date -u\necho done\n"), baseRevision: "r1" },
    ]);
  });

  it("sends the command and no inline program once the program is switched back to a command", async () => {
    await openTask({ kind: "edit", id: "script-complete" }, inline(["/bin/sh"], "date -u\n"));
    expect(radio("An inline script").getAttribute("aria-checked")).toBe("true");
    await click(radio("A program"));
    expect(field("Interpreter")).toBeUndefined();
    await enter(control<HTMLTextAreaElement>("Program and arguments"), COMMAND);
    await click(button("Save"));
    await settle();
    expect(sent("PUT", "/tasks/script-complete")).toStrictEqual([{ document: SCRIPT_DOCUMENT, baseRevision: "r1" }]);
  });

  it("shows the document in its controls, and sends no key of a cleared control, so its default applies", async () => {
    await openTask({ kind: "edit", id: "script-complete" });
    expect(control("Identifier").disabled).toBe(true);
    expect(control("Task type").disabled).toBe(true);
    const values = (...labels: string[]) => labels.map((label) => control(label).value);
    expect({
      general: [...values("Name", "Identifier", "Task type", "Description"), toggle("Enabled").checked],
      script: [
        radio("A program").getAttribute("aria-checked"),
        ...values("Program and arguments", "Working directory"),
        checked("Runs on"),
        control("Environment variables").value,
      ],
      schedule: [...values("Repeats", "Time"), control("Cron expression (5 fields)").textContent, ...values("Time zone", "Missed runs", "Overlapping firings")],
      permissions: [...values("Profile", "Network"), toggle("Restrict writes to the workspace").checked, ...values("Allowed tools", "Denied tools", "Writable paths", "Read paths")],
      execution: values("Timeout (minutes), shared by all attempts", "Grace before a cancellation is forced (seconds)"),
      notifications: [toggle("Native notifications").checked, checked("Notify me about")],
    }).toEqual({
      general: ["Every script section", "script-complete", "Script", "Every section of the script form, set.", false],
      script: ["true", COMMAND, "${FORM_REPO}", ["macOS", "Linux"], "AUDIT_LABEL=Daily audit\nAUDIT_LEVEL=full"],
      schedule: ["weekdays", "06:30", "30 6 * * 1-5", "America/Sao_Paulo", "skip", "skip"],
      permissions: ["custom", "deny", false, "Read\nGrep", "WebFetch", "${FORM_REPO}", "${FORM_REPO}"],
      execution: ["45", "10"],
      notifications: [false, ["Operational failure", "Cancellation"]],
    });

    // One control of each section cleared; the events emptied show their defaults again, as the daemon applies them.
    await enter(control<HTMLTextAreaElement>("Description"), "");
    await enter(control("Working directory"), "");
    await enter(control("Time zone"), "");
    await enter(control<HTMLSelectElement>("Network"), "");
    await enter(control("Timeout (minutes), shared by all attempts"), "");
    for (const event of ["Operational failure", "Cancellation"]) await tick("Notify me about", event);
    expect(checked("Notify me about")).toEqual(["Execution completed", "Operational failure", "Needs attention", "Draft PR created", "No alternative AI tool"]);
    await click(button("Save"));
    await settle();

    const expected = structuredClone(SCRIPT_DOCUMENT);
    for (const path of [
      ["metadata", "description"],
      ["spec", "script", "workingDirectory"],
      ["spec", "schedule", "timezone"],
      ["spec", "permissions", "network"],
      ["spec", "execution", "timeoutMinutes"],
      ["spec", "notifications", "native", "events"],
    ]) {
      const parent = path.slice(0, -1).reduce((node, key) => node[key] as Doc, expected);
      delete parent[path.at(-1)!];
    }
    expect(sent("PUT", "/tasks/script-complete")).toStrictEqual([{ document: expected, baseRevision: "r1" }]);
  });
});

// A field that holds a group of checkboxes is a group its caption names, not a label around them: the caption labels
// no checkbox, so clicking it changes none, and each checkbox is named by its own text alone (harmflow-app#438).
describe("task form checkbox groups", () => {
  const onChange = vi.fn();

  function Form({ doc }: { doc: Doc }) {
    const [value, setValue] = useState(doc);
    return (
      <TaskForm
        doc={value}
        onChange={(d) => {
          onChange(d);
          setValue(d);
        }}
        mode="edit"
        harnesses={[]}
        errorPaths={new Set()}
      />
    );
  }

  const script: Doc = {
    apiVersion: "harmflow/v1alpha1",
    kind: "Task",
    metadata: { id: "audit", name: "Audit" },
    spec: { type: "script", script: { command: ["/bin/date"] } },
  };

  /** The group whose name is the caption: the text of the element its aria-labelledby names. */
  const group = (caption: string) =>
    [...host!.querySelectorAll<HTMLElement>('[role="group"]')].find(
      (g) => document.getElementById(g.getAttribute("aria-labelledby") ?? "")?.textContent === caption,
    );
  /**
   * Every `<label>` around a control, nearest first. A browser names a checkbox by each of them whose first control it
   * is; happy-dom's `labels` lists the nearest alone, so the outer ones are found here.
   */
  const labelsAround = (el: Element) => {
    const found: HTMLLabelElement[] = [];
    for (let l = el.closest("label"); l; l = l.parentElement?.closest("label") ?? null) found.push(l);
    return found;
  };

  it.each([
    { caption: "Fail over on", doc: task({}), options: ["Quota exhausted", "AI tool unavailable"] },
    {
      caption: "Notify me about",
      doc: task({}),
      options: [
        "Execution started",
        "Execution completed",
        "Operational failure",
        "Cancellation",
        "Needs attention",
        "Draft PR created",
        "Draft PR updated",
        "Tests failed",
        "AI tool unavailable",
        "No alternative AI tool",
      ],
    },
    { caption: "Runs on", doc: script, options: ["macOS", "Linux", "Windows"] },
  ])("names the $caption group by its caption, each checkbox by its own text, and toggles none from the caption", async ({ caption, doc, options }) => {
    onChange.mockClear();
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Form doc={doc} />));
    await settle();

    const found = group(caption)!;
    expect(found).toBeDefined();
    const boxes = [...found.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')];
    // A checkbox's name is the text of its labels: its own label alone, with no name set beside it.
    expect(boxes.map((box) => labelsAround(box).map((l) => l.textContent))).toEqual(options.map((option) => [option]));
    for (const box of boxes) {
      expect(box.hasAttribute("aria-label") || box.hasAttribute("aria-labelledby")).toBe(false);
    }

    const states = boxes.map((box) => box.checked);
    await act(async () => document.getElementById(found.getAttribute("aria-labelledby")!)!.click());
    expect(boxes.map((box) => box.checked)).toEqual(states);
    expect(onChange).not.toHaveBeenCalled();
  });
});

// A task that runs a harness shows in its Execution section, after the rate-limit fields, whether it allows extra
// usage: off unless its document allows it. Off removes the key, so the default, which allows none, applies. A script
// task runs no harness and shows no such control (harmflow-app#300). What the daemon does with the setting is app ›
// TestExhaustedQuotaHoldsTheAttemptBack's, TestExtraUsageEndsTheAttempt's, and TestExtraUsageAllowed's.
describe("task form extra usage", () => {
  let current: Doc = {};

  function Form({ doc }: { doc: Doc }) {
    const [value, setValue] = useState(doc);
    current = value;
    return <TaskForm doc={value} onChange={(d) => setValue((current = d))} mode="edit" harnesses={[]} errorPaths={new Set()} />;
  }

  async function openForm(doc: Doc) {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Form doc={doc} />));
    await settle();
  }

  /** A task of a type that runs a harness, with the execution limits given. */
  const harnessTask = (type: "general" | "github", execution?: Doc): Doc => {
    const doc = task({});
    return { ...doc, spec: { ...(doc.spec as Doc), type, ...(execution ? { execution } : {}) } };
  };
  const script = (execution?: Doc): Doc => ({
    apiVersion: "harmflow/v1alpha1",
    kind: "Task",
    metadata: { id: "audit", name: "Audit" },
    spec: { type: "script", script: { command: ["/bin/date"] }, ...(execution ? { execution } : {}) },
  });

  const spec = () => current.spec as Doc;
  const section = () => host!.querySelector("#section-execution")!;
  const toggle = () => section().querySelector<HTMLInputElement>("label.toggle input");
  const label = () => section().querySelector("label.toggle")?.textContent;
  const hint = () => section().querySelector(".field--toggle .field__hint")?.textContent;
  const input = (text: string) =>
    [...section().querySelectorAll("label.field")].find((f) => f.querySelector(".field__label")?.textContent === text)!.querySelector("input")!;
  async function click(el: HTMLElement) {
    await act(async () => el.click());
  }
  async function type(el: HTMLInputElement, value: string) {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  it.each(["general", "github"] as const)("shows a %s task's extra usage off when its document sets nothing, after the rate-limit fields", async (kind) => {
    await openForm(harnessTask(kind));
    expect(toggle()?.checked).toBe(false);
    // Named by its label alone: the hint sits beside the toggle's label, not in it.
    expect(label()).toBe("Allow extra usage");
    expect(hint()).toBe(
      "Usage billed beyond what the AI tool's plan includes. Off, an attempt does not start on an AI tool whose plan is used up, and a Claude Code attempt that turns to extra usage is ended. On, neither check holds an attempt back.",
    );
    expect(input("Longest wait (seconds)").compareDocumentPosition(toggle()!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(spec().execution).toBeUndefined();
  });

  it.each([
    ["no other limit", undefined],
    ["another limit", { timeoutMinutes: 45 }],
  ])("writes true when turned on, and removes the key when turned off, with %s", async (_, execution) => {
    await openForm(harnessTask("general", execution));
    await click(toggle()!);
    expect(toggle()!.checked).toBe(true);
    expect(spec().execution).toStrictEqual({ ...execution, allowExtraUsage: true });

    await click(toggle()!);
    expect(toggle()!.checked).toBe(false);
    // An execution left with no limit goes with its last key.
    expect(spec().execution).toStrictEqual(execution);
  });

  it.each([true, false])("opens a task whose file sets allowExtraUsage: %s with the toggle at that value", async (allowed) => {
    await openForm(harnessTask("github", { allowExtraUsage: allowed }));
    expect(toggle()!.checked).toBe(allowed);
  });

  it("turns a task whose file sets allowExtraUsage: false on, then off without the key", async () => {
    await openForm(harnessTask("general", { allowExtraUsage: false }));
    await click(toggle()!);
    expect(spec().execution).toStrictEqual({ allowExtraUsage: true });
    await click(toggle()!);
    expect(spec().execution).toBeUndefined();
  });

  it("shows a script task no extra-usage control, and its document never carries the key", async () => {
    await openForm(script({ timeoutMinutes: 5 }));
    expect(section().querySelector(".toggle")).toBeNull();
    expect(section().textContent).not.toContain("Allow extra usage");
    await type(input("Timeout (minutes), shared by all attempts"), "10");
    expect(spec().execution).toStrictEqual({ timeoutMinutes: 10 });
  });

  it.each([
    ["pt-BR", "Permitir uso extra", "Uso cobrado além do que o plano da ferramenta de IA inclui."],
    ["es-ES", "Permitir uso extra", "Uso facturado más allá de lo que incluye el plan de la herramienta de IA."],
  ])("labels the toggle and its hint in %s", async (language, text, hintStart) => {
    await i18n.changeLanguage(language);
    try {
      await openForm(harnessTask("general"));
      expect(label()).toBe(text);
      expect(hint()?.startsWith(hintStart)).toBe(true);
    } finally {
      await i18n.changeLanguage("en-US");
    }
  });
});

// The name picker, the model, the time zone, and the schedule's time are TextInputs set in the monospaced face, each
// keeping its suggestions, its type, and its keys.
describe("task form text fields", () => {
  let current: Doc = {};
  const catalog: Catalog = {
    agents: [],
    skills: [{ name: "review-notes", kind: "skill", source: "user", harnesses: ["claude-code"] }],
    problems: [],
    taskTypes: [],
  };

  function Form({ doc }: { doc: Doc }) {
    const [value, setValue] = useState(doc);
    current = value;
    return <TaskForm doc={value} onChange={(d) => setValue((current = d))} mode="edit" catalog={catalog} harnesses={[]} errorPaths={new Set()} />;
  }

  async function openForm(doc: Doc) {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Form doc={doc} />));
    await settle();
  }

  const spec = () => current.spec as Doc;
  const input = (label: string) =>
    [...host!.querySelectorAll("label.field")]
      .find((f) => f.querySelector(".field__label")?.textContent === label)
      ?.querySelector<HTMLInputElement>("input") ?? null;
  async function type(el: HTMLInputElement, value: string) {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  beforeEach(() => {
    daemon.answers.set("/schedules/preview", { occurrences: [], timezone: "America/Sao_Paulo" });
  });

  it.each([
    ["Skills", "text"],
    ["Model", "text"],
    ["Time zone", "text"],
    ["Time", "time"],
  ])("draws %s as a monospaced TextInput of type %s", async (label, kind) => {
    await openForm(task({ cron: "0 9 * * *", timezone: "America/Sao_Paulo" }));
    const el = input(label)!;
    expect(el.classList.contains("input")).toBe(true);
    expect(el.classList.contains("mono")).toBe(true);
    expect(el.classList.contains("input--error")).toBe(false);
    expect(el.type).toBe(kind);
  });

  it("keeps each field's suggestions", async () => {
    await openForm(task({ cron: "0 9 * * *", timezone: "America/Sao_Paulo" }));
    for (const label of ["Skills", "Model", "Time zone"]) {
      const list = input(label)!.getAttribute("list");
      expect(list).toBeTruthy();
      expect(host!.querySelector(`datalist[id="${list}"]`)).not.toBeNull();
    }
    expect(input("Time")!.hasAttribute("list")).toBe(false);
  });

  it("adds a name it suggests at once, and a name it does not on Enter", async () => {
    await openForm(task({}));
    await type(input("Skills")!, "review-notes");
    expect(spec().skills).toEqual(["review-notes"]);

    await type(input("Skills")!, "my-skill");
    expect(spec().skills).toEqual(["review-notes"]);
    await act(async () => {
      input("Skills")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(spec().skills).toEqual(["review-notes", "my-skill"]);
    expect(input("Skills")!.value).toBe("");
  });

  it("writes the model, the time zone, and the time", async () => {
    await openForm(task({ cron: "0 9 * * *", timezone: "America/Sao_Paulo" }));
    await type(input("Model")!, "opus");
    expect((spec().harness as Doc).preferred).toEqual({ provider: "claude-code", model: "opus" });
    await type(input("Time zone")!, "Europe/Lisbon");
    expect((spec().schedule as Doc).timezone).toBe("Europe/Lisbon");
    await type(input("Time")!, "10:30");
    expect((spec().schedule as Doc).cron).toBe("30 10 * * *");
  });
});
