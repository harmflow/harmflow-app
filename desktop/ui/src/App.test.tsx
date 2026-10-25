// @vitest-environment happy-dom
// Drives the dashboard's shell in a DOM: ⌘B and the sidebar's own control collapse the sidebar to its icons and expand
// it again, the choice outlives a restart, and the daemon's connection state is the topbar's alone. ⌘N opens the
// template picker as the topbar's New task does (harmflow-app#169). ⌘S saves only in a task editor (harmflow-app#230).
// Every way out of a task editor with unsaved changes asks first, and each template pick opens an editor of its own
// (harmflow-app#170). ⌘+, ⌘−, and ⌘0 zoom the dashboard everywhere, and the level outlives a restart (harmflow-app#55).
// A render error never leaves the window empty, and the interface tells the shell when it has painted (harmflow-app#96).
import { EditorView } from "@codemirror/view";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act, createElement } from "react";
import { flushSync } from "react-dom";
import { createRoot, type Root as ReactRoot } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";
import { native } from "./api/native";
import type { Settings, Task, TaskSummary } from "./api/types";
import { Root } from "./App";
import { rootOptions } from "./components/ErrorBoundary";
import { YamlEditor } from "./components/YamlEditor";
import i18n from "./i18n";
import { patchStateForTests } from "./state/store";

// Which component throws during render; none unless a test says so.
const boom = vi.hoisted(() => ({ screen: false, sidebar: false }));

vi.mock("./screens/ExecutionsScreen", async (importOriginal) => {
  const original = await importOriginal<typeof import("./screens/ExecutionsScreen")>();
  const { createElement } = await import("react");
  return {
    ...original,
    ExecutionsScreen: (props: Parameters<typeof original.ExecutionsScreen>[0]) => {
      if (boom.screen) throw new Error("Boom in a screen");
      return createElement(original.ExecutionsScreen, props);
    },
  };
});

vi.mock("./components/Shell", async (importOriginal) => {
  const original = await importOriginal<typeof import("./components/Shell")>();
  const { createElement } = await import("react");
  return {
    ...original,
    Sidebar: (props: Parameters<typeof original.Sidebar>[0]) => {
      if (boom.sidebar) throw new Error("Boom in the sidebar");
      return createElement(original.Sidebar, props);
    },
  };
});

// The daemon answers the paths a test sets here, and nothing else.
const daemon = vi.hoisted(() => ({ answers: new Map<string, unknown>() }));

vi.mock("./api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (_method: string, path: string) =>
      daemon.answers.has(path) ? { status: 200, body: daemon.answers.get(path) } : { status: 404, body: null },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

// No event stream or periodic refresh: each test sets the state it needs.
vi.mock("./state/store", async (importOriginal) => ({ ...(await importOriginal<typeof import("./state/store")>()), startSync: () => {} }));

const settings: Settings = {
  language: "en-US",
  trayEnabled: true,
  notificationLevel: "all",
  logRetentionDays: 30,
  theme: "harmflow-dark",
  autostart: { enabled: false, mechanism: "launchd" },
  pathVariables: {},
  allowedFolders: [],
  onboardingCompleted: true,
};

let root: ReactRoot | undefined;
let host: HTMLElement | undefined;

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
  dropSelectionChange();
});

/**
 * Drops selectionchange, which these tests never read. happy-dom fires it while the selection changes, where a browser
 * schedules it as a task (Selection API). CodeMirror writes the selection inside its own update and reads it back on
 * that event, so fired at once it re-enters the update, which throws "Calls to EditorView.update are not allowed while
 * an update is in progress" whenever the editor's focus timer runs more than 200 ms after the focus, as on a loaded CI
 * runner. Queued as a task instead, it never settles under happy-dom: each read is taken for a change and leads to
 * another write.
 */
function dropSelectionChange() {
  const dispatch = document.dispatchEvent.bind(document);
  document.dispatchEvent = (event: Event) => (event.type === "selectionchange" ? true : dispatch(event));
}

beforeEach(() => {
  localStorage.clear();
  patchStateForTests({
    settings,
    daemonRunning: true,
    connection: "connected",
    stats: {
      running: 2,
      queued: 0,
      completedToday: 0,
      failedToday: 0,
      activity: { running: [], queued: [], completed: [], failed: [] },
      system: { cpuPercent: 12, memoryUsedBytes: 2.1e9, memoryTotalBytes: 16e9, diskUsedBytes: 48e9, diskTotalBytes: 500e9 },
    },
  });
});

afterEach(() => {
  quit();
  daemon.answers.clear();
});

/** Mounts the interface as main.tsx does, with the same root options. */
async function launch(element = createElement(Root)) {
  host ??= document.body.appendChild(document.createElement("div"));
  root = createRoot(host, rootOptions);
  await act(async () => root!.render(element));
}

function quit() {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
}

const layout = () => host!.querySelector(".app")?.getAttribute("data-sidebar");
const sidebar = () => host!.querySelector<HTMLElement>(".sidebar")!;
const toggle = () => sidebar().querySelector<HTMLButtonElement>(".sidebar__toggle")!;
const navButton = (label: string) => [...sidebar().querySelectorAll<HTMLButtonElement>(".nav__item")].find((b) => b.textContent?.startsWith(label))!;
const tooltip = () => document.body.querySelector(".tooltip")?.textContent ?? null;

/** Presses ⌘B (or the given keys) where focus is; returns whether the app took the key. */
async function press(target: EventTarget = document.activeElement ?? document.body, keys: KeyboardEventInit = {}): Promise<boolean> {
  const event = new KeyboardEvent("keydown", { key: "b", metaKey: true, bubbles: true, cancelable: true, ...keys });
  await act(async () => void target.dispatchEvent(event));
  return event.defaultPrevented;
}

describe("sidebar", () => {
  it("collapses to its icons with ⌘B and expands again, from a text field too", async () => {
    await launch();
    expect(layout()).toBe("expanded");
    expect(sidebar().textContent).toContain("Local agents, real results.");
    expect(sidebar().querySelectorAll('[role="meter"]')).toHaveLength(3); // CPU, RAM, and disk
    expect(sidebar().textContent).toMatch(/v\d/);

    expect(await press()).toBe(true);
    expect(layout()).toBe("collapsed");
    expect(sidebar().querySelector(".logo")).not.toBeNull();
    expect(sidebar().textContent).not.toContain("Local agents, real results.");
    expect(sidebar().querySelectorAll('[role="meter"]')).toHaveLength(0);
    expect(sidebar().textContent).not.toMatch(/v\d/);
    // Labels stay as accessible names, the active screen stays marked, and the running count stays as a badge.
    const executions = navButton("Executions");
    expect(executions.querySelector(".nav__label")!.className).toContain("sr-only");
    expect(executions.getAttribute("aria-current")).toBe("page");
    expect(executions.querySelector(".nav__badge")?.textContent).toBe("2");

    await act(async () => navButton("History").click());
    const search = host!.querySelector<HTMLInputElement>("input")!;
    search.focus();
    expect(await press(search)).toBe(true);
    expect(layout()).toBe("expanded");
    expect(navButton("History").querySelector(".nav__label")!.className).not.toContain("sr-only");
  });

  it("takes ⌘B from the YAML editor, whose keymap lets it through", async () => {
    await launch();
    const editorHost = host!.querySelector(".app__content")!.appendChild(document.createElement("div"));
    const editorRoot = createRoot(editorHost);
    await act(async () => editorRoot.render(<YamlEditor value={"name: Core\n"} onChange={() => {}} />));
    try {
      const content = editorHost.querySelector<HTMLElement>(".cm-content")!;
      content.focus();
      expect(await press(content)).toBe(true);
      expect(layout()).toBe("collapsed");
    } finally {
      act(() => editorRoot.unmount());
    }
  });

  it("toggles from its own control, whose tooltip names the shortcut", async () => {
    await launch();
    expect(toggle().getAttribute("aria-label")).toBe("Collapse sidebar");
    expect(toggle().getAttribute("aria-keyshortcuts")).toBe("Meta+B");
    await act(async () => void toggle().dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    expect(tooltip()).toBe("Collapse sidebar⌘B");

    await act(async () => toggle().click());
    expect(layout()).toBe("collapsed");
    expect(toggle().getAttribute("aria-label")).toBe("Expand sidebar");
    expect(tooltip()).toBeNull(); // the layout moved under it

    await act(async () => toggle().focus());
    expect(tooltip()).toBe("Expand sidebar⌘B");
    await act(async () => toggle().click());
    expect(layout()).toBe("expanded");
  });

  it("names each collapsed icon in a tooltip on hover and on keyboard focus", async () => {
    await launch();
    await act(async () => void navButton("Tasks").dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    expect(tooltip()).toBeNull(); // expanded, the label is in view

    await press();
    await act(async () => void navButton("Tasks").dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    expect(tooltip()).toBe("Tasks");
    await act(async () => void navButton("Tasks").dispatchEvent(new MouseEvent("mouseout", { bubbles: true })));
    expect(tooltip()).toBeNull();

    await act(async () => navButton("Settings").focus());
    expect(tooltip()).toBe("Settings");
    await act(async () => navButton("Settings").blur());
    expect(tooltip()).toBeNull();
  });

  it("keeps the choice across a restart of the app", async () => {
    await launch();
    await press();
    expect(layout()).toBe("collapsed");
    quit();

    await launch();
    expect(layout()).toBe("collapsed");
    await press();
    quit();

    await launch();
    expect(layout()).toBe("expanded");
  });

  it("leaves ⌘B alone while a modal dialog is open, and on the onboarding screen", async () => {
    await launch();
    const newTask = [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "New task")!;
    await act(async () => newTask.click());
    expect(host!.querySelector('[aria-modal="true"]')).not.toBeNull();
    expect(await press()).toBe(false);
    expect(layout()).toBe("expanded");
    quit();

    patchStateForTests({ settings: { ...settings, onboardingCompleted: false } });
    await launch();
    expect(host!.querySelector(".sidebar")).toBeNull();
    expect(await press()).toBe(false);
    await act(async () => patchStateForTests({ settings }));
    expect(layout()).toBe("expanded");
  });

  it("takes ⌘B alone, not with another modifier or as a held key", async () => {
    await launch();
    expect(await press(undefined, { shiftKey: true })).toBe(false);
    expect(await press(undefined, { ctrlKey: true })).toBe(false);
    expect(await press(undefined, { repeat: true })).toBe(false);
    expect(await press(undefined, { metaKey: false })).toBe(false);
    expect(layout()).toBe("expanded");
  });

  it("shows no connection state, which the topbar shows alone", async () => {
    patchStateForTests({ connection: "disconnected" });
    try {
      await launch();
      expect(sidebar().textContent).not.toContain("Connection to the daemon lost");
      expect(sidebar().textContent).not.toContain("The connection to the daemon was lost");
      expect(host!.querySelector(".topbar")!.textContent).toContain("Connection to the daemon lost");
    } finally {
      await act(async () => patchStateForTests({ connection: "connected" }));
    }
  });
});

describe("new task", () => {
  const cmdN = { key: "n" };
  const pickers = () => host!.querySelectorAll('[role="dialog"]').length;
  const title = () => host!.querySelector(".topbar__title")!.textContent;
  const newTaskButton = () => host!.querySelector<HTMLButtonElement>('.topbar__actions button[aria-keyshortcuts="Meta+N"]')!;
  const pick = (label: string) =>
    act(async () => [...host!.querySelectorAll<HTMLButtonElement>(".template")].find((b) => b.textContent?.includes(label))!.click());

  it("opens the template picker with ⌘N on every screen, from a text field too", async () => {
    await launch();
    for (const screen of ["Overview", "Executions", "Tasks", "History", "Settings"]) {
      await act(async () => navButton(screen).click());
      expect(await press(undefined, cmdN)).toBe(true);
      expect(pickers()).toBe(1);
      expect(host!.querySelector('[role="dialog"] h2')!.textContent).toBe("New task");
      await act(async () => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
      expect(pickers()).toBe(0);
    }

    await act(async () => navButton("History").click());
    const search = host!.querySelector<HTMLInputElement>(".app__content input")!;
    search.focus();
    expect(await press(search, cmdN)).toBe(true);
    expect(pickers()).toBe(1);
  });

  it("opens the new task's editor when a template is picked, as the topbar's button does", async () => {
    await launch();
    await press(undefined, cmdN);
    await pick("General task");
    expect(pickers()).toBe(0);
    expect(title()).toBe("Tasks");
    expect([...host!.querySelectorAll<HTMLInputElement>("input")].some((i) => i.value === "General task")).toBe(true);
  });

  it("takes ⌘N from the YAML editor, whose keymap lets it through", async () => {
    await launch();
    const editorHost = host!.querySelector(".app__content")!.appendChild(document.createElement("div"));
    const editorRoot = createRoot(editorHost);
    await act(async () => editorRoot.render(<YamlEditor value={"name: Core\n"} onChange={() => {}} />));
    try {
      const content = editorHost.querySelector<HTMLElement>(".cm-content")!;
      content.focus();
      expect(await press(content, cmdN)).toBe(true);
      expect(pickers()).toBe(1);
    } finally {
      act(() => editorRoot.unmount());
    }
  });

  it("leaves ⌘N alone while a modal dialog is open, the picker included, and on the onboarding screen", async () => {
    await launch();
    await act(async () => newTaskButton().click());
    expect(pickers()).toBe(1);
    expect(await press(undefined, cmdN)).toBe(false);
    expect(pickers()).toBe(1);
    quit();

    patchStateForTests({ settings: { ...settings, onboardingCompleted: false } });
    await launch();
    expect(host!.querySelector(".sidebar")).toBeNull();
    expect(await press(undefined, cmdN)).toBe(false);
    expect(pickers()).toBe(0);
    await act(async () => patchStateForTests({ settings }));
    expect(pickers()).toBe(0);
  });

  it("takes ⌘N alone, not with another modifier or as a held key", async () => {
    await launch();
    expect(await press(undefined, { ...cmdN, shiftKey: true })).toBe(false);
    expect(await press(undefined, { ...cmdN, altKey: true })).toBe(false);
    expect(await press(undefined, { ...cmdN, ctrlKey: true })).toBe(false);
    expect(await press(undefined, { ...cmdN, repeat: true })).toBe(false);
    expect(await press(undefined, { ...cmdN, metaKey: false })).toBe(false);
    expect(pickers()).toBe(0);
  });

  it("names ⌘N in the topbar button's tooltip, in each language", async () => {
    await launch();
    for (const [language, label] of [
      ["en-US", "New task"],
      ["pt-BR", "Nova tarefa"],
      ["es-ES", "Nueva tarea"],
    ]) {
      await act(async () => void i18n.changeLanguage(language));
      await act(async () => void newTaskButton().dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
      expect(tooltip()).toBe(`${label}⌘N`);
      await act(async () => void newTaskButton().dispatchEvent(new MouseEvent("mouseout", { bubbles: true })));
      expect(tooltip()).toBeNull();
    }
    await act(async () => void i18n.changeLanguage("en-US"));

    await act(async () => newTaskButton().focus());
    expect(tooltip()).toBe("New task⌘N");
    await act(async () => newTaskButton().click());
    expect(tooltip()).toBeNull(); // the picker covers the button
  });
});

describe("save", () => {
  it("leaves ⌘S alone outside the task editor, and the editor takes it", async () => {
    await launch();
    for (const screen of ["Overview", "Executions", "Tasks", "History", "Settings"]) {
      await act(async () => navButton(screen).click());
      expect(await press(undefined, { key: "s" })).toBe(false);
    }
    await act(async () => host!.querySelector<HTMLButtonElement>('.topbar__actions button[aria-keyshortcuts="Meta+N"]')!.click());
    await act(async () => [...host!.querySelectorAll<HTMLButtonElement>(".template")].find((b) => b.textContent?.includes("General task"))!.click());
    expect(host!.querySelector(".editor")).not.toBeNull();
    expect(await press(undefined, { key: "s" })).toBe(true);
  });
});

describe("unsaved changes", () => {
  let confirm: MockInstance<typeof native.confirm>;

  beforeEach(() => {
    confirm = vi.spyOn(native, "confirm");
  });

  afterEach(() => {
    confirm.mockRestore();
  });

  const title = () => host!.querySelector(".topbar__title")!.textContent;
  const editor = () => host!.querySelector(".editor");
  const nameField = () => host!.querySelector<HTMLInputElement>(".editor input")!;
  const unsaved = () => host!.querySelector(".editor__dirty")?.textContent ?? null;
  const button = (scope: ParentNode, label: string) => [...scope.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label)!;
  const newTaskButton = () => host!.querySelector<HTMLButtonElement>('.topbar__actions button[aria-keyshortcuts="Meta+N"]')!;
  const asked = () => confirm.mock.calls.length;

  /** Lets an answered question reach the screen. */
  const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));
  const click = async (target: HTMLElement) => {
    await act(async () => target.click());
    await settle();
  };

  /** Picks a template in the topbar's New task picker. */
  async function newTask(template: string) {
    await click(newTaskButton());
    await click([...host!.querySelectorAll<HTMLButtonElement>(".template")].find((b) => b.textContent?.includes(template))!);
  }

  /** Types into a text field as React sees it. */
  async function type(input: HTMLInputElement, value: string) {
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  /** A new task's editor with an unsaved name. */
  async function editing() {
    await launch();
    await newTask("General task");
    await type(nameField(), "Release notes");
    expect(unsaved()).toBe("Unsaved changes");
  }

  it("asks before the sidebar leaves, keeps the editor and its tab on Cancel, and leaves on Discard", async () => {
    daemon.answers.set("/tasks/validate", { valid: true, errors: [], warnings: [], yaml: "metadata:\n  name: Release notes\n" });
    await editing();
    await click(button(host!.querySelector(".tabs")!, "YAML"));
    expect(host!.querySelector(".cm-content")).not.toBeNull();

    confirm.mockResolvedValue(false);
    await click(navButton("Executions"));
    expect(confirm).toHaveBeenLastCalledWith("Leaving the editor discards the changes that were not saved.", "Discard unsaved changes?", "Discard", "Cancel");
    // Tasks resets the screen to its list, so it asks too.
    await click(navButton("Tasks"));
    expect(asked()).toBe(2);
    expect(title()).toBe("Tasks");
    expect(unsaved()).toBe("Unsaved changes");
    expect(host!.querySelector('[role="tab"][aria-selected="true"]')!.textContent).toBe("YAML");
    expect(host!.querySelector(".cm-content")!.textContent).toContain("Release notes");

    confirm.mockResolvedValue(true);
    await click(navButton("Executions"));
    expect(asked()).toBe(3);
    expect(title()).toBe("Live executions");
    expect(editor()).toBeNull();

    // The editor is gone, and so is its question.
    await click(navButton("Tasks"));
    await click(navButton("History"));
    expect(asked()).toBe(3);
    expect(title()).toBe("History");
  });

  it("asks before a picked template replaces the editor, with ⌘N too, and Discard opens the picked one", async () => {
    await editing();

    confirm.mockResolvedValue(false);
    await newTask("Script task");
    expect(asked()).toBe(1);
    expect(nameField().value).toBe("Release notes");
    await press(undefined, { key: "n" });
    await click([...host!.querySelectorAll<HTMLButtonElement>(".template")].find((b) => b.textContent?.includes("Script task"))!);
    expect(asked()).toBe(2);
    expect(nameField().value).toBe("Release notes");
    expect(host!.querySelector('[role="dialog"]')).toBeNull();

    confirm.mockResolvedValue(true);
    await newTask("Script task");
    expect(asked()).toBe(3);
    expect(nameField().value).toBe("Script task");
    expect(unsaved()).toBeNull();
  });

  it("asks from the notification bell, the More menu, and a notice or tray target", async () => {
    patchStateForTests({
      unread: [
        {
          id: "n1",
          createdAt: new Date().toISOString(),
          eventType: "execution.failed",
          executionId: "exec-1",
          title: "Nightly failed",
          body: "Exit code 1",
          severity: "error",
          read: false,
        },
      ],
    });
    try {
      await editing();
      confirm.mockResolvedValue(false);

      await click(host!.querySelector<HTMLButtonElement>('button[aria-label="Notifications"]')!);
      await click(host!.querySelector<HTMLButtonElement>(".notice")!);
      expect(asked()).toBe(1);

      await click(host!.querySelector<HTMLButtonElement>('button[aria-label="More actions"]')!);
      await click(button(host!.querySelector(".popover")!, "Open settings"));
      expect(asked()).toBe(2);

      await act(async () => patchStateForTests({ navigation: { executionId: "exec-1", nonce: 1 } }));
      await settle();
      expect(asked()).toBe(3);
      expect(title()).toBe("Tasks");
      expect(nameField().value).toBe("Release notes");

      confirm.mockResolvedValue(true);
      await act(async () => patchStateForTests({ navigation: { executionId: "exec-1", nonce: 2 } }));
      await settle();
      expect(asked()).toBe(4);
      expect(title()).toBe("Live executions");
    } finally {
      patchStateForTests({ unread: [], navigation: undefined });
    }
  });

  it("leaves an editor without unsaved changes at once, and a second pick opens the second template", async () => {
    await launch();
    await newTask("General task");
    expect(nameField().value).toBe("General task");
    await newTask("Script task");
    expect(nameField().value).toBe("Script task");
    await newTask("Script task");
    expect(nameField().value).toBe("Script task");

    // The YAML tab's text, put in place by the editor, is no change of the user's.
    daemon.answers.set("/tasks/validate", { valid: true, errors: [], warnings: [], yaml: "metadata:\n  name: Script task\n" });
    await click(button(host!.querySelector(".tabs")!, "YAML"));
    expect(host!.querySelector(".cm-content")).not.toBeNull();
    expect(unsaved()).toBeNull();

    await click(navButton("Executions"));
    expect(title()).toBe("Live executions");
    expect(asked()).toBe(0);
  });

  it("asks before leaving an existing task's editor with an unsaved edit", async () => {
    const summary: TaskSummary = { id: "standup", name: "Standup notes", type: "general", enabled: true, revision: "r1", harness: "claude-code", warnings: [] };
    const document = { apiVersion: "harmflow/v1alpha1", kind: "Task", metadata: { id: "standup", name: "Standup notes" }, spec: { type: "general", prompt: "Write the standup notes." } };
    daemon.answers.set("/tasks/standup", { ...summary, document, effective: document, yaml: "", path: "standup.yaml" } satisfies Task);
    patchStateForTests({ tasks: [summary], tasksLoaded: true });
    try {
      await launch();
      await click(navButton("Tasks"));
      await click(host!.querySelector<HTMLElement>(".task-row__main")!);
      expect(nameField().value).toBe("Standup notes");
      await click(navButton("History"));
      expect(asked()).toBe(0);

      await click(navButton("Tasks"));
      await click(host!.querySelector<HTMLElement>(".task-row__main")!);
      await type(nameField(), "Standup summary");
      confirm.mockResolvedValue(false);
      await click(navButton("History"));
      expect(asked()).toBe(1);
      expect(nameField().value).toBe("Standup summary");

      confirm.mockResolvedValue(true);
      await click(navButton("History"));
      expect(title()).toBe("History");
    } finally {
      patchStateForTests({ tasks: [], tasksLoaded: false });
    }
  });

  it("reports the user's YAML edits alone, not the text its value puts in place", async () => {
    const changes: string[] = [];
    const editorHost = document.body.appendChild(document.createElement("div"));
    const editorRoot = createRoot(editorHost);
    const render = (value: string) => act(async () => editorRoot.render(<YamlEditor value={value} onChange={(v) => changes.push(v)} />));
    try {
      await render("name: Core\n");
      await render("name: Core saved\n");
      expect(editorHost.querySelector(".cm-content")!.textContent).toBe("name: Core saved");
      expect(changes).toEqual([]);

      const view = EditorView.findFromDOM(editorHost.querySelector<HTMLElement>(".cm-editor")!)!;
      await act(async () => view.dispatch({ changes: { from: view.state.doc.length, insert: "id: core\n" } }));
      expect(changes).toEqual(["name: Core saved\nid: core\n"]);
    } finally {
      act(() => editorRoot.unmount());
      editorHost.remove();
    }
  });
});

describe("zoom", () => {
  const zoomIn = { key: "=" };
  const zoomInWithShift = { key: "+", shiftKey: true };
  const zoomOut = { key: "-" };
  const actualSize = { key: "0" };
  let setZoom: MockInstance<typeof native.setZoom>;

  beforeEach(() => {
    setZoom = vi.spyOn(native, "setZoom");
  });

  afterEach(() => {
    setZoom.mockRestore();
  });

  /** The level the web view was last set to, in percent. */
  const level = () => setZoom.mock.lastCall?.[0];

  it("zooms in with ⌘+ and ⌘=, out with ⌘−, and back to 100% with ⌘0, on every screen, from a text field too", async () => {
    await launch();
    expect(level()).toBe(100);
    for (const screen of ["Overview", "Executions", "Tasks", "History", "Settings"]) {
      await act(async () => navButton(screen).click());
      expect(await press(undefined, zoomIn)).toBe(true);
      expect(level()).toBe(110);
      expect(await press(undefined, zoomInWithShift)).toBe(true);
      expect(level()).toBe(125);
      expect(await press(undefined, zoomOut)).toBe(true);
      expect(level()).toBe(110);
      expect(await press(undefined, actualSize)).toBe(true);
      expect(level()).toBe(100);
    }

    await act(async () => navButton("History").click());
    const search = host!.querySelector<HTMLInputElement>(".app__content input")!;
    search.focus();
    expect(await press(search, zoomOut)).toBe(true);
    expect(level()).toBe(90);
  });

  it("takes the keys from the YAML editor, whose keymap lets them through", async () => {
    await launch();
    const editorHost = host!.querySelector(".app__content")!.appendChild(document.createElement("div"));
    const editorRoot = createRoot(editorHost);
    await act(async () => editorRoot.render(<YamlEditor value={"name: Core\n"} onChange={() => {}} />));
    try {
      const content = editorHost.querySelector<HTMLElement>(".cm-content")!;
      content.focus();
      expect(await press(content, zoomInWithShift)).toBe(true);
      expect(level()).toBe(110);
      expect(await press(content, actualSize)).toBe(true);
      expect(level()).toBe(100);
    } finally {
      act(() => editorRoot.unmount());
    }
  });

  it("takes the keys while a dialog is open, and from a control in it", async () => {
    await launch();
    await act(async () => [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "New task")!.click());
    expect(host!.querySelector('[aria-modal="true"]')).not.toBeNull();
    expect(await press(undefined, zoomIn)).toBe(true);
    expect(level()).toBe(110);
    const template = host!.querySelector<HTMLElement>('[aria-modal="true"] .template')!;
    template.focus();
    expect(await press(template, zoomOut)).toBe(true);
    expect(level()).toBe(100);
    expect(host!.querySelector('[aria-modal="true"]')).not.toBeNull(); // the dialog stays open
  });

  it("takes the keys on the onboarding screen", async () => {
    patchStateForTests({ settings: { ...settings, onboardingCompleted: false } });
    await launch();
    expect(host!.querySelector(".onboarding")).not.toBeNull();
    expect(await press(undefined, zoomIn)).toBe(true);
    expect(level()).toBe(110);
    expect(await press(undefined, actualSize)).toBe(true);
    expect(level()).toBe(100);
  });

  it("takes the keys on the daemon-down screen", async () => {
    // The screen shows inside the desktop shell alone; everything else here stays a plain browser's.
    native.available = true;
    localStorage.setItem("harmflow.onboarded", "1");
    patchStateForTests({ daemonRunning: false });
    try {
      await launch();
      expect(host!.querySelector(".daemon-down")).not.toBeNull();
      expect(await press(undefined, zoomOut)).toBe(true);
      expect(level()).toBe(90);
      expect(await press(undefined, actualSize)).toBe(true);
      expect(level()).toBe(100);
    } finally {
      native.available = false;
    }
  });

  it("stops at 150% and at 80% without wrapping, and the keypad's + and − work too", async () => {
    await launch();
    for (let i = 0; i < 6; i++) await press(undefined, { key: "+", code: "NumpadAdd" });
    expect(level()).toBe(150);
    expect(setZoom.mock.calls.map(([percent]) => percent)).toEqual([100, 110, 125, 150]);
    for (let i = 0; i < 8; i++) await press(undefined, { key: "-", code: "NumpadSubtract" });
    expect(level()).toBe(80);
    expect(await press(undefined, { key: "-", code: "NumpadSubtract" })).toBe(true); // taken, though it moves nothing
    expect(level()).toBe(80);
  });

  it("keeps a held key zooming, up to the end of the range", async () => {
    await launch();
    await press(undefined, zoomIn);
    expect(await press(undefined, { ...zoomIn, repeat: true })).toBe(true);
    expect(await press(undefined, { ...zoomIn, repeat: true })).toBe(true);
    expect(await press(undefined, { ...zoomIn, repeat: true })).toBe(true);
    expect(level()).toBe(150);
  });

  it("leaves the keys alone with Control or Option, and − and 0 with Shift", async () => {
    await launch();
    expect(await press(undefined, { ...zoomIn, ctrlKey: true })).toBe(false);
    expect(await press(undefined, { ...zoomIn, altKey: true })).toBe(false);
    expect(await press(undefined, { ...zoomOut, shiftKey: true })).toBe(false);
    expect(await press(undefined, { ...actualSize, shiftKey: true })).toBe(false);
    expect(await press(undefined, { ...zoomIn, metaKey: false })).toBe(false);
    expect(setZoom.mock.calls.map(([percent]) => percent)).toEqual([100]);
  });

  it("keeps the level across a restart of the app", async () => {
    await launch();
    await press(undefined, zoomIn);
    await press(undefined, zoomIn);
    expect(level()).toBe(125);
    quit();

    setZoom.mockClear();
    await launch();
    expect(setZoom.mock.calls.map(([percent]) => percent)).toEqual([125]);
    await press(undefined, actualSize);
    quit();

    setZoom.mockClear();
    await launch();
    expect(setZoom.mock.calls.map(([percent]) => percent)).toEqual([100]);
  });

  it("opens at 100% when the kept level is not one of the steps", async () => {
    localStorage.setItem("harmflow.zoom", "175");
    await launch();
    expect(level()).toBe(100);
  });

  it("applies the kept level before it tells the shell the interface is ready, even when the zoom fails", async () => {
    const ready = vi.spyOn(native, "interfaceReady");
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      let applied!: () => void;
      setZoom.mockImplementationOnce(() => new Promise<void>((resolve) => (applied = resolve)));
      localStorage.setItem("harmflow.zoom", "125");
      await launch();
      expect(setZoom).toHaveBeenCalledWith(125);
      expect(ready).not.toHaveBeenCalled();
      await act(async () => applied());
      expect(ready).toHaveBeenCalledTimes(1);
      quit();

      ready.mockClear();
      setZoom.mockRejectedValueOnce(new Error("not allowed"));
      await launch();
      expect(ready).toHaveBeenCalledTimes(1);
      expect(logged).toHaveBeenCalledWith("HarmFlow: zoom not applied", expect.objectContaining({ message: "not allowed" }));
    } finally {
      ready.mockRestore();
      logged.mockRestore();
    }
  });
});

describe("daemon restart", () => {
  it("keeps the shell while the daemon restarts, and shows the daemon-down screen once it is down for good", async () => {
    // The daemon-down screen shows inside the desktop shell alone.
    native.available = true;
    localStorage.setItem("harmflow.onboarded", "1");
    patchStateForTests({ daemonRunning: false, daemonRestart: "restarting" });
    try {
      await launch();
      expect(host!.querySelector(".app")).not.toBeNull();
      expect(host!.querySelector(".daemon-down")).toBeNull();
      await act(async () => patchStateForTests({ daemonRestart: "idle" }));
      expect(host!.querySelector(".daemon-down")).not.toBeNull();
    } finally {
      native.available = false;
      patchStateForTests({ daemonRestart: "idle" });
    }
  });
});

describe("render errors", () => {
  let logged: MockInstance<typeof console.error>;

  beforeEach(() => {
    logged = vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    boom.screen = boom.sidebar = false;
    logged.mockRestore();
  });

  const alert = () => host!.querySelector<HTMLElement>('[role="alert"]');
  const button = (label: string) => [...alert()!.querySelectorAll("button")].find((b) => b.textContent === label);

  it("replaces only the screen that throws, below the sidebar and the header", async () => {
    boom.screen = true;
    await launch();
    expect(alert()!.closest(".app__content")).not.toBeNull();
    expect(alert()!.textContent).toContain("This screen could not be shown");
    expect(alert()!.textContent).toContain("Boom in a screen");
    expect(button("Reload")).toBeDefined();
    expect(alert()!.querySelector('button[aria-label="Copy"]')).not.toBeNull();
    // The copied details hold the stack and React's component stack too.
    expect(alert()!.querySelector("pre")!.textContent).toMatch(/Boom in a screen[\s\S]+ExecutionsScreen/);

    expect(sidebar().querySelectorAll(".nav__item")).toHaveLength(5);
    expect(host!.querySelector(".topbar")!.textContent).toContain("Daemon connected");
  });

  it("leaves the error behind when the sidebar opens another screen", async () => {
    boom.screen = true;
    await launch();
    await act(async () => navButton("Settings").click());
    expect(alert()).toBeNull();
    expect(host!.querySelector(".screen.settings")).not.toBeNull();
  });

  it("shows the message and a reload over the whole window when the sidebar throws", async () => {
    boom.sidebar = true;
    await launch();
    expect(host!.querySelector(".sidebar")).toBeNull();
    expect(alert()!.closest(".crash-root")).not.toBeNull();
    expect(alert()!.textContent).toContain("The interface could not be shown");
    expect(alert()!.textContent).toContain("Boom in the sidebar");
    expect(button("Reload")).toBeDefined();
    expect(host!.textContent).not.toBe("");
  });

  it("logs a caught error with its component stack", async () => {
    boom.screen = true;
    await launch();
    expect(logged).toHaveBeenCalledWith(
      "HarmFlow: render error",
      expect.objectContaining({ message: "Boom in a screen" }),
      expect.stringContaining("ExecutionsScreen"),
    );
  });

  it("logs an error that no boundary catches", async () => {
    function Thrower(): never {
      throw new Error("Boom outside every boundary");
    }
    // Inside act(), React rethrows an uncaught error instead of passing it to the root's handler.
    const scope = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
    scope.IS_REACT_ACT_ENVIRONMENT = false;
    try {
      host = document.body.appendChild(document.createElement("div"));
      root = createRoot(host, rootOptions);
      flushSync(() => root!.render(createElement(Thrower)));
    } finally {
      scope.IS_REACT_ACT_ENVIRONMENT = true;
    }
    expect(host.textContent).toBe("");
    expect(logged).toHaveBeenCalledWith(
      "HarmFlow: uncaught render error",
      expect.objectContaining({ message: "Boom outside every boundary" }),
      expect.stringContaining("Thrower"),
    );
  });
});

describe("startup", () => {
  it("tells the shell once, after the first commit, that the interface is ready", async () => {
    const ready = vi.spyOn(native, "interfaceReady");
    try {
      await launch();
      expect(ready).toHaveBeenCalledTimes(1);
      await press(); // a render after the first one
      expect(ready).toHaveBeenCalledTimes(1);
    } finally {
      ready.mockRestore();
    }
  });

  it("paints a text-free placeholder with no inline style, which the first render replaces", async () => {
    const html = readFileSync(join(__dirname, "..", "index.html"), "utf8");
    expect(html).not.toMatch(/<style|\sstyle=/i);
    const placeholder = new DOMParser().parseFromString(html, "text/html").getElementById("root")!;
    expect(placeholder.querySelector(".boot svg")).not.toBeNull();
    expect(placeholder.textContent!.trim()).toBe("");

    host = document.body.appendChild(document.createElement("div"));
    host.innerHTML = placeholder.innerHTML;
    await launch();
    expect(host.querySelector(".boot")).toBeNull();
    expect(host.querySelector(".app")).not.toBeNull();
  });
});
