// @vitest-environment happy-dom
// Drives the dashboard's shell in a DOM: ⌘B and the sidebar's own control collapse the sidebar to its icons and expand
// it again, the choice outlives a restart, and the daemon's connection state is the topbar's alone. A render error
// never leaves the window empty, and the interface tells the shell when it has painted (harmflow-app#96).
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act, createElement } from "react";
import { flushSync } from "react-dom";
import { createRoot, type Root as ReactRoot } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";
import { native } from "./api/native";
import type { Settings } from "./api/types";
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

vi.mock("./api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async () => ({ status: 404, body: null }),
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
});

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
