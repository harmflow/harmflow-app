// @vitest-environment happy-dom
// Drives the Storage section's log retention field against a stand-in for the daemon's local API: the field reads the
// saved period, Apply sends the one typed, and a period the daemon rejects stays an unsaved change. What the daemon does
// with the setting — its bounds, its default, and its persistence across a restart — is app › TestLogRetentionSetting's.
// Drives the same section's Clean workspaces control too: one clean-up request, no second click while it runs, and
// what the daemon removed and kept, or its error. Which worktrees the daemon removes is app › CleanWorkspaces' tests'.
// Then shows what AI tools, Security, and Storage read from the harnesses, the tools, and the diagnostics in the store:
// each harness's readiness and path, the command-line tools, the token store, the disk used, and the directories with
// their Show in Finder buttons, which ask the desktop's bridge to open the row's path. And the Daemon section's Stop
// daemon control, whose question names one running execution in the singular and more in the plural. The same section
// starts a stopped daemon through a stub of the desktop's bridge, and shows, beside the Overview, the daemon running
// now and autostart enabled as two states; that a daemon started by hand fires its tasks is
// itest › TestHandStartedDaemonFiresWithAutostartOff's.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { native, type DaemonStatus } from "../api/native";
import type { StreamHandlers } from "../api/transport";
import type { Diagnostics, Harness, Health, Settings, SettingsPatch, Stats, Tool } from "../api/types";
import type { LeaveCheck } from "../components/TaskEditor";
import { Toasts } from "../components/toast";
import i18n from "../i18n";
import { enUS } from "../i18n/en-US";
import { actions, patchStateForTests, startSync } from "../state/store";
import { OverviewScreen } from "./OverviewScreen";
import { SettingsScreen, type SettingsSection } from "./SettingsScreen";

// The daemon as the client sees it: the settings and the health it serves, every request it received with its body,
// the stream it feeds, a settings update applied as the daemon applies it, a period outside 1 to 3650 refused with the
// daemon's own error, and the answer each test gives a workspace clean-up. A stopped daemon answers nothing: every
// request then fails as an unreachable local API does.
const daemon = vi.hoisted(() => ({
  up: true,
  settings: undefined as unknown as Settings,
  health: undefined as unknown as Health,
  requests: [] as { method: string; path: string; body: unknown }[],
  clean: undefined as undefined | (() => Promise<{ status: number; body: unknown }>),
  stream: undefined as StreamHandlers | undefined,
}));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string, body?: unknown) => {
      daemon.requests.push({ method, path, body });
      if (!daemon.up) return { status: 0, body: null };
      if (method === "GET" && path === "/health") return { status: 200, body: daemon.health };
      if (method === "GET" && path === "/settings") return { status: 200, body: daemon.settings };
      if (method === "PATCH" && path === "/settings") {
        const { autostart, ...rest } = body as SettingsPatch;
        const days = rest.logRetentionDays;
        if (days !== undefined && (days < 1 || days > 3650)) {
          return {
            status: 422,
            body: {
              error: {
                code: "validation_failed",
                message: "logRetentionDays must be between 1 and 3650",
                messageKey: "settings_log_retention_invalid",
              },
            },
          };
        }
        daemon.settings = { ...daemon.settings, ...(rest as Partial<Settings>) };
        if (autostart !== undefined) daemon.settings = { ...daemon.settings, autostart: { ...daemon.settings.autostart, enabled: autostart } };
        return { status: 200, body: daemon.settings };
      }
      if (method === "POST" && path === "/workspaces/clean" && daemon.clean) return daemon.clean();
      return { status: 404, body: null };
    },
    requestText: async () => (daemon.up ? { status: 404, text: "" } : { status: 0, text: "" }),
    stream: (handlers: StreamHandlers) => {
      daemon.stream = handlers;
      return () => {};
    },
  },
}));

const health: Health = { status: "ok", version: "0.1.0", apiVersion: "v1", pid: 4242, startedAt: "2026-10-31T09:00:00Z", acceptingFirings: true };

const saved: Settings = {
  language: "en-US",
  trayEnabled: true,
  keepTrayOnQuit: false,
  showRestrictionWarnings: true,
  notificationLevel: "all",
  logRetentionDays: 30,
  theme: "harmflow-dark",
  autostart: { enabled: false, mechanism: "launchd" },
  pathVariables: {},
  allowedFolders: [],
  onboardingCompleted: true,
  quota: {},
};

let root: Root | undefined;
let host: HTMLElement | undefined;
/** The question the screen gives its parent to ask before leaving, while a draft is held. */
let leaveCheck: LeaveCheck | null = null;

// The store's sync starts once per module, as the app starts it once, so that the stream's handlers reach the tests.
// Its periodic reads run on a fake clock that never moves: only each test's own reads reach the stand-in.
beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
  daemon.settings = saved;
  daemon.health = health;
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  startSync();
  await settle();
  vi.useRealTimers();
});

// A toast stays 3.5 seconds in a list the module keeps; a fake clock for its timeout ends each test's toasts with the
// test, so that none shows in the next.
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  daemon.up = true;
  daemon.settings = saved;
  daemon.health = health;
  daemon.requests.length = 0;
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
  leaveCheck = null;
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
  vi.restoreAllMocks();
  native.available = false;
  daemon.requests.length = 0;
  daemon.clean = undefined;
  patchStateForTests({
    settings: undefined,
    diagnostics: undefined,
    harnesses: [],
    tools: [],
    health: undefined,
    connection: "connecting",
    daemonRunning: null,
    redetecting: null,
  });
});

/** Lets the stand-in daemon's answers arrive and React render them. */
const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

/**
 * A section as the app opens it, Storage by default: the screen reads the settings from the daemon. The stand-in serves
 * no harnesses, tools, or diagnostics, so the screen keeps the ones a test put in the store.
 */
async function open(section: SettingsSection = "storage") {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <>
        <SettingsScreen section={section} onLeaveCheck={(check) => (leaveCheck = check)} />
        <Toasts />
      </>,
    ),
  );
  await settle();
}

const field = () => host!.querySelector<HTMLInputElement>("#settings-retention")!;
const apply = () => field().closest(".inline-form")!.querySelector<HTMLButtonElement>("button")!;
const updates = () => daemon.requests.filter((r) => r.method === "PATCH" && r.path === "/settings");
const toasts = () =>
  [...host!.querySelectorAll(".toast")].map((t) => ({
    tone: [...t.classList].find((c) => c.startsWith("toast--"))?.slice("toast--".length),
    text: t.textContent,
  }));

/** Types into the number field as React sees it. */
async function type(value: string) {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(field(), value);
    field().dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function click(target: HTMLElement) {
  await act(async () => target.click());
  await settle();
}

describe("the Storage section's log retention field", () => {
  it("reads the saved period, with Apply disabled", async () => {
    await open();
    expect(field().value).toBe("30");
    expect(apply().disabled).toBe(true);
    expect(leaveCheck).toBeNull();
  });

  it("keeps Apply disabled for the saved period typed again and for an empty field", async () => {
    await open();
    await type("30");
    expect(apply().disabled).toBe(true);
    await type("");
    expect(apply().disabled).toBe(true);
    expect(updates()).toEqual([]);
  });

  it("sends the period typed alone, reads the settings again, and shows it saved", async () => {
    await open();
    await type("14");
    expect(apply().disabled).toBe(false);

    daemon.requests.length = 0;
    await click(apply());
    expect(updates()).toHaveLength(1);
    expect(updates()[0].body).toStrictEqual({ logRetentionDays: 14 });
    // The settings read again after the update, as the daemon now serves them.
    const after = daemon.requests.slice(daemon.requests.indexOf(updates()[0]) + 1);
    expect(after).toContainEqual({ method: "GET", path: "/settings", body: undefined });
    expect(daemon.settings.logRetentionDays).toBe(14);

    expect(toasts()).toEqual([{ tone: "success", text: "Settings saved." }]);
    expect(field().value).toBe("14");
    expect(apply().disabled).toBe(true);
    expect(leaveCheck).toBeNull();
  });

  it("shows the daemon's refusal of a period out of bounds, and keeps the period as an unsaved change", async () => {
    const confirm = vi.spyOn(native, "confirm").mockResolvedValue(false);
    await open();
    await type("3651");
    expect(apply().disabled).toBe(false);

    await click(apply());
    expect(updates().map((r) => r.body)).toStrictEqual([{ logRetentionDays: 3651 }]);
    expect(daemon.settings.logRetentionDays).toBe(30);
    expect(toasts()).toEqual([{ tone: "danger", text: "logRetentionDays must be between 1 and 3650" }]);
    expect(field().value).toBe("3651");
    expect(apply().disabled).toBe(false);

    // Leaving the section asks first, as for any draft of this field.
    expect(leaveCheck).not.toBeNull();
    await expect(leaveCheck!()).resolves.toBe(false);
    expect(confirm.mock.calls).toEqual([
      ["Leaving this section discards the changes that were not saved or applied.", "Discard unsaved changes?", "Discard", "Cancel"],
    ]);
  });
});

describe("the Storage section's Clean workspaces control", () => {
  const workspace = "/Users/me/Library/Application Support/HarmFlow/workspace/fix-issues";
  /** The worktree of an item whose pull request was merged, which the daemon removes. */
  const merged = `${workspace}/harmflow-app-12`;
  /** The worktree of a draft still in progress, which the daemon keeps. */
  const draft = `${workspace}/harmflow-app-15`;

  const cleanButton = () =>
    [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Clean workspaces")!;
  /** Every request that could change something: anything but a read. */
  const changes = () => daemon.requests.filter((r) => r.method !== "GET");
  const result = () => host!.querySelector(".banner--success");

  it("sends one clean-up request, and takes no second click until the daemon answers", async () => {
    let answer!: (res: { status: number; body: unknown }) => void;
    daemon.clean = () => new Promise((resolve) => (answer = resolve));
    await open();
    daemon.requests.length = 0;
    expect(cleanButton().disabled).toBe(false);

    await click(cleanButton());
    expect(changes()).toStrictEqual([{ method: "POST", path: "/workspaces/clean", body: undefined }]);
    expect(cleanButton().disabled).toBe(true);
    await click(cleanButton());
    expect(changes()).toHaveLength(1);

    await act(async () => answer({ status: 200, body: { removed: [], kept: [] } }));
    await settle();
    expect(cleanButton().disabled).toBe(false);
    expect(changes()).toHaveLength(1);
  });

  it("shows what the daemon removed and what it kept, with the reason", async () => {
    daemon.clean = async () => ({
      status: 200,
      body: { removed: [merged], kept: [{ path: draft, reason: "its draft pull request is still in progress" }] },
    });
    await open();
    daemon.requests.length = 0;

    await click(cleanButton());
    expect(changes()).toStrictEqual([{ method: "POST", path: "/workspaces/clean", body: undefined }]);
    expect(result()!.textContent).toContain("1 removed, 1 kept.");
    expect([...result()!.querySelectorAll(".clean-list li")].map((li) => li.textContent)).toStrictEqual([
      `− ${merged}`,
      `= ${draft} — Kept: its draft pull request is still in progress`,
    ]);
    expect(toasts()).toEqual([]);
    expect(cleanButton().disabled).toBe(false);
  });

  it("shows the daemon's error in a danger toast, and no result", async () => {
    // The error the daemon answers on this route: its token check runs before the clean-up does.
    daemon.clean = async () => ({
      status: 401,
      body: { error: { code: "unauthorized", message: "a valid token is required", messageKey: "token_required" } },
    });
    await open();
    daemon.requests.length = 0;

    await click(cleanButton());
    expect(changes()).toStrictEqual([{ method: "POST", path: "/workspaces/clean", body: undefined }]);
    expect(toasts()).toEqual([{ tone: "danger", text: "a valid token is required" }]);
    expect(result()).toBeNull();
    expect(host!.querySelector(".clean-list")).toBeNull();
    expect(cleanButton().disabled).toBe(false);
  });
});

// One harness detected and ready, the other not detected, and both command-line tools found.
const claudeCode: Harness = {
  id: "claude-code",
  name: "Claude Code",
  detected: true,
  path: "/usr/local/bin/claude",
  installations: ["/usr/local/bin/claude"],
  installation: "/usr/local/bin/claude",
  version: "2.1.3",
  authentication: "valid",
  readiness: "ready",
  models: [],
  efforts: [],
  lastCheckedAt: "2026-10-31T12:00:00Z",
};
const codex: Harness = {
  id: "codex",
  name: "Codex",
  detected: false,
  installations: [],
  authentication: "unknown",
  readiness: "not_installed",
  models: [],
  efforts: [],
  lastCheckedAt: "2026-10-31T12:00:00Z",
};
const tools: Tool[] = [
  { id: "git", detected: true, path: "/usr/bin/git", version: "2.54.0" },
  { id: "gh", detected: true, path: "/opt/homebrew/bin/gh", version: "2.99.0", authentication: "valid" },
];

// Diagnostics as GET /diagnostics answers them, with every directory.
const diagnostics: Diagnostics = {
  version: "0.1.0",
  address: "127.0.0.1:7777",
  homeDir: "/Users/test/Library/Application Support/HarmFlow",
  tasksDir: "/Users/test/Library/Application Support/HarmFlow/tasks",
  stateDir: "/Users/test/Library/Application Support/HarmFlow/state",
  logsDir: "/Users/test/Library/Application Support/HarmFlow/logs",
  workspaceDir: "/Users/test/Library/Application Support/HarmFlow/workspace",
  runsDir: "/Users/test/Library/Application Support/HarmFlow/runs",
  runtimeFile: "/Users/test/Library/Application Support/HarmFlow/runtime.json",
  tokenStore: "keychain",
  builtinsDir: "/Users/test/Library/Application Support/HarmFlow/builtins",
  diskUsageBytes: 1.5 * 1024 ** 3,
};

/** The directory rows in the order Storage lists them, each with its en-US label and its path. */
const directories = [
  ["HarmFlow folder", diagnostics.homeDir],
  ["Tasks", diagnostics.tasksDir],
  ["State", diagnostics.stateDir],
  ["Logs", diagnostics.logsDir],
  ["Workspaces", diagnostics.workspaceDir],
  ["Execution directories", diagnostics.runsDir],
  ["Runtime file", diagnostics.runtimeFile],
  ["Built-in agents and skills", diagnostics.builtinsDir],
];

const text = (el: Element | null | undefined) => el?.textContent?.trim();
const panel = (title: string) =>
  [...host!.querySelectorAll(".panel")].find((p) => text(p.querySelector(".panel__title")) === title) ?? null;
const settingRow = (label: string) =>
  [...host!.querySelectorAll(".setting-row")].find((r) => text(r.querySelector(".setting-row__label")) === label) ?? null;
/** A key-value list's rows, each as its key and its value. */
const pairs = (within: Element) => [...within.querySelectorAll(".kv__row")].map((r) => [text(r.querySelector("dt")), text(r.querySelector("dd"))]);
const inventory = (name: string) =>
  [...host!.querySelectorAll(".inventory")].find((c) => text(c.querySelector(".harness-row__text strong")) === name)!;

describe("the AI tools section", () => {
  // harmflow-app#669: the detection Detect again asks for outlives the request, so the button shows it running.
  it("shows Detect again busy while the detection it asked for runs", async () => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools, redetecting: ["codex"] });
    await open("agents");

    const button = [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => text(b) === "Detecting…");
    expect(button?.disabled).toBe(true);
    expect(button?.getAttribute("aria-busy")).toBe("true");
  });

  it("says so when the request to detect again fails, and offers it again", async () => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    const button = () => [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => text(b) === "Detect again");

    await click(button()!);

    expect(daemon.requests).toContainEqual({ method: "POST", path: "/harnesses/refresh", body: undefined });
    expect(toasts()).toEqual([{ tone: "danger", text: expect.stringMatching(/^Detect again failed: /) }]);
    expect(button()?.disabled).toBe(false);
  });

  it("shows each harness's readiness, the detected one's version and path, and the tools with theirs", async () => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");

    const ready = inventory("Claude Code");
    expect(text(ready.querySelector(".harness-row__text .mono"))).toBe("2.1.3 · /usr/local/bin/claude");
    expect(text(ready.querySelector(".harness-row .chip"))).toBe("Ready");
    expect(pairs(ready)).toEqual(
      expect.arrayContaining([
        ["Detected", "Yes"],
        ["Path", "/usr/local/bin/claude"],
        ["Version", "2.1.3"],
        ["Authentication", "Authenticated"],
      ]),
    );

    const absent = inventory("Codex");
    expect(text(absent.querySelector(".harness-row__text .mono"))).toBe("Not detected");
    expect(text(absent.querySelector(".harness-row .chip"))).toBe("Not installed");
    expect(pairs(absent)).toEqual(
      expect.arrayContaining([
        ["Detected", "No"],
        ["Path", "—"],
        ["Version", "—"],
      ]),
    );

    const rows = [...panel("Command-line tools")!.querySelectorAll(".harness-row")].map((r) => [
      text(r.querySelector("strong")),
      text(r.querySelector(".mono")),
      text(r.querySelector(".chip")),
    ]);
    expect(rows).toEqual([
      ["Git", "2.54.0 · /usr/bin/git", "Detected"],
      ["GitHub CLI (gh)", "2.99.0 · /opt/homebrew/bin/gh", "Authenticated"],
    ]);
  });
});

describe("the AI tools section's quota shares", () => {
  const slider = (id: string) => host!.querySelector<HTMLInputElement>(`#settings-quota-${id}`)!;
  const shown = (id: string) => text(slider(id).closest(".quota-share")!.querySelector("output"));

  /** Drags the slider to a share, as React sees it, and lets it go. */
  async function drag(id: string, share: number) {
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(slider(id), String(share));
      slider(id).dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(updates()).toEqual([]);
    expect(shown(id)).toBe(`${share}%`);
    await act(async () => slider(id).dispatchEvent(new Event("pointerup", { bubbles: true })));
    await settle();
  }

  it("shows each AI tool's share, the whole quota when none is set, from 1% to 100%", async () => {
    daemon.settings = { ...daemon.settings, quota: { codex: { maxPercent: 80 } } };
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");

    expect([slider("claude-code").min, slider("claude-code").max, slider("claude-code").value]).toEqual(["1", "100", "100"]);
    expect(shown("claude-code")).toBe("100%");
    expect(slider("codex").value).toBe("80");
    expect(shown("codex")).toBe("80%");
  });

  it("saves the share once it is let go, keeping the other tools' shares", async () => {
    daemon.settings = { ...daemon.settings, quota: { codex: { maxPercent: 80 } } };
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    daemon.requests.length = 0;

    await drag("claude-code", 95);

    expect(updates().map((r) => r.body)).toStrictEqual([{ quota: { codex: { maxPercent: 80 }, "claude-code": { maxPercent: 95 } } }]);
    expect(daemon.settings.quota).toStrictEqual({ codex: { maxPercent: 80 }, "claude-code": { maxPercent: 95 } });
    expect(toasts()).toEqual([{ tone: "success", text: "Settings saved." }]);
    expect(slider("claude-code").value).toBe("95");

    // Leaving the slider after letting it go sends nothing more.
    await act(async () => slider("claude-code").dispatchEvent(new FocusEvent("focusout", { bubbles: true })));
    await settle();
    expect(updates()).toHaveLength(1);
  });
});

describe("the Security section", () => {
  it.each([
    ["keychain", "macOS Keychain"],
    ["file", "File readable only by you"],
  ] as const)("shows the %s token store in the interface's words, beside the token's replacement", async (tokenStore, words) => {
    patchStateForTests({ diagnostics: { ...diagnostics, tokenStore } });
    await open("security");

    const row = settingRow("Token store")!;
    expect(text(row.querySelector(".setting-row__control"))).toBe(words);
    // The row that follows replaces the API token, in the same panel.
    const next = row.nextElementSibling!;
    expect(text(next.querySelector(".setting-row__label"))).toBe("API token");
    expect(text(next.querySelector(".setting-row__control button"))).toBe("Replace API token");
    expect(row.closest(".panel")).toBe(next.closest(".panel"));
  });
});

describe("the Daemon section's Start daemon", () => {
  it("shows the bridge's message when the start fails (harmflow-app#725)", async () => {
    native.available = true;
    patchStateForTests({ connection: "daemon_stopped", health: undefined });
    // The shell answers a failed command with its message alone, not an Error.
    const start = vi
      .spyOn(native, "startDaemon")
      .mockRejectedValue("the daemon did not answer within 10 seconds; see logs/daemon.log in the HarmFlow folder");
    try {
      await open("daemon");
      const button = [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Start daemon")!;
      await click(button);

      expect(start).toHaveBeenCalledTimes(1);
      expect(toasts()).toEqual([
        { tone: "danger", text: "the daemon did not answer within 10 seconds; see logs/daemon.log in the HarmFlow folder" },
      ]);
      expect(button.disabled).toBe(false);
    } finally {
      native.available = false;
      patchStateForTests({ connection: "connecting" });
    }
  });
});

describe("the Daemon section's Stop daemon control", () => {
  /** Stats as GET /stats answers them, with `running` executions. */
  const stats = (running: number): Stats => ({
    running,
    queued: 0,
    completedToday: 0,
    failedToday: 0,
    activity: { running: [], queued: [], completed: [], failed: [] },
  });
  const stopButton = () => [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Stop daemon")!;

  afterEach(() => patchStateForTests({ stats: undefined, connection: "connecting" }));

  it.each([
    [1, "1 execution is running. Stopping the daemon now interrupts it; it will be recorded as interrupted."],
    [2, "2 executions are running. Stopping the daemon now interrupts them; they will be recorded as interrupted."],
  ])("asks before stopping with %i running, in the number's form", async (running, message) => {
    const confirm = vi.spyOn(native, "confirm").mockResolvedValue(false);
    patchStateForTests({ connection: "connected", stats: stats(running) });
    await open("daemon");
    await click(stopButton());
    expect(confirm.mock.calls).toEqual([[message, "Stop the daemon?", "Stop daemon", "Cancel"]]);
    expect(daemon.requests.filter((r) => r.method === "POST")).toEqual([]);
  });
});

describe("the Storage section's disk use and directories", () => {
  it("shows the disk HarmFlow uses as diagnostics report it", async () => {
    patchStateForTests({ diagnostics });
    await open();
    expect(text(panel("Workspaces")!.querySelector(".setting-row__label"))).toBe("Disk used by HarmFlow: 1.5 GB");
  });

  it("lists each directory with its path, and no Show in Finder button without the desktop's bridge", async () => {
    patchStateForTests({ diagnostics });
    await open();
    const list = panel("Directories")!;
    expect(pairs(list)).toEqual(directories);
    expect(list.querySelectorAll("button")).toHaveLength(0);
  });

  it("gives each directory a Show in Finder button that asks the bridge to open its path", async () => {
    native.available = true;
    const openPath = vi.spyOn(native, "openPath").mockResolvedValue(undefined);
    try {
      patchStateForTests({ diagnostics });
      await open();
      const rows = [...panel("Directories")!.querySelectorAll(".kv__row")];
      expect(rows.map((r) => [text(r.querySelector("dt")), text(r.querySelector("dd .mono"))])).toEqual(directories);

      for (const [i, row] of rows.entries()) {
        const button = row.querySelector<HTMLButtonElement>("button")!;
        expect(button.getAttribute("aria-label")).toBe("Show in Finder");
        await click(button);
        expect(openPath).toHaveBeenCalledTimes(i + 1);
        expect(openPath).toHaveBeenLastCalledWith(directories[i][1]);
      }
    } finally {
      native.available = false;
    }
  });
});

describe("starting the daemon by hand, with its running and autostart states shown apart", () => {
  /** The Daemon section of Settings and the Overview, rendered together against the same stand-ins. */
  async function openBoth() {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () =>
      root!.render(
        <>
          <SettingsScreen section="daemon" />
          <OverviewScreen onOpenExecution={() => {}} onNewTask={() => {}} onOpenTask={() => {}} />
          <Toasts />
        </>,
      ),
    );
    await settle();
  }

  /** The stream as the desktop's shell feeds it: it reports each state of its connection to the daemon. */
  async function streamSays(state: "connected" | "daemon_stopped") {
    await act(async () => daemon.stream!.onConnection(state));
    await settle();
  }

  /**
   * Settings read with autostart off while the daemon ran; then the daemon stops. The local API answers nothing, and
   * the bridge's status says no daemon runs, which the store's periodic health check reads.
   */
  async function stoppedWithAutostartOff() {
    native.available = true;
    const status = vi.spyOn(native, "daemonStatus").mockResolvedValue({ running: false });
    await openBoth();
    await streamSays("connected");
    daemon.up = false;
    await act(async () => actions.refreshHealth());
    await settle();
    expect(status).toHaveBeenCalled();
  }

  const settingsRow = (label: string) =>
    [...host!.querySelectorAll(".settings .setting-row")].find((r) => r.querySelector(".setting-row__label")?.textContent === label)!;
  const settingsRunning = () => settingsRow(enUS.settings.runningNow).querySelector(".chip")!.textContent;
  const settingsHint = () => settingsRow(enUS.settings.runningNow).querySelector(".field__hint")?.textContent ?? "";
  const button = (text: string) => [...settingsRow(enUS.settings.runningNow).querySelectorAll("button")].find((b) => b.textContent === text);
  const autostartToggle = () => host!.querySelector<HTMLInputElement>("#settings-autostart")!;
  const overviewChip = (label: string) =>
    [...host!.querySelectorAll(".state-pair__item")].find((i) => i.querySelector(".state-pair__label")?.textContent === label)!.querySelector(".chip")!
      .textContent;
  const overview = () => ({ running: overviewChip(enUS.overview.runningNow), autostart: overviewChip(enUS.overview.autostart) });
  const dangerToasts = () => [...host!.querySelectorAll(".toast--danger")];

  it("shows a stopped daemon with autostart off as not running, with a start button and the toggle off", async () => {
    await stoppedWithAutostartOff();
    expect(settingsRunning()).toBe(enUS.overview.notRunning);
    expect(button(enUS.settings.startDaemon)?.disabled).toBe(false);
    expect(button(enUS.settings.stopDaemon)).toBeUndefined();
    expect(autostartToggle().checked).toBe(false);
    expect(overview()).toEqual({ running: enUS.overview.notRunning, autostart: enUS.overview.autostartOff });
  });

  it("starts the daemon once through the bridge, then shows it running with autostart still off", async () => {
    await stoppedWithAutostartOff();
    let started!: (status: DaemonStatus) => void;
    const start = vi.spyOn(native, "startDaemon").mockReturnValue(new Promise((resolve) => (started = resolve)));

    await act(async () => button(enUS.settings.startDaemon)!.click());
    // No daemon to replace: the start names no pid of a daemon to wait for.
    expect(start.mock.calls).toEqual([[]]);
    const pending = button(enUS.settings.starting)!;
    expect(pending.disabled).toBe(true);
    await act(async () => pending.click());
    expect(start).toHaveBeenCalledTimes(1);

    // The bridge answers once the started daemon does, and the dashboard reads its state again.
    daemon.up = true;
    daemon.health = { ...health, pid: 4343, startedAt: new Date().toISOString() };
    daemon.requests.length = 0;
    await act(async () => started({ running: true, pid: 4343 }));
    await settle();
    const reads = daemon.requests.filter((r) => r.method === "GET").map((r) => r.path);
    expect(reads).toContain("/health");
    expect(reads).toContain("/settings");
    // Settings reads "running now" from the stream, which the shell connects again once the daemon answers.
    await streamSays("connected");

    expect(settingsRunning()).toBe(enUS.common.yes);
    expect(settingsHint()).toContain(enUS.overview.pid.replace("{{pid}}", "4343"));
    expect(button(enUS.settings.stopDaemon)).toBeDefined();
    expect(button(enUS.settings.startDaemon)).toBeUndefined();
    expect(autostartToggle().checked).toBe(false);
    expect(overview()).toEqual({ running: enUS.common.yes, autostart: enUS.overview.autostartOff });
    expect(start).toHaveBeenCalledTimes(1);
  });

  it("toasts the message of a start the bridge rejects, and offers the start again", async () => {
    await stoppedWithAutostartOff();
    // The shell rejects a failed command with its message alone, not an Error.
    const message = "the daemon did not answer within 10 seconds; see logs/daemon.log in the HarmFlow folder";
    const start = vi.spyOn(native, "startDaemon").mockRejectedValue(message);

    await act(async () => button(enUS.settings.startDaemon)!.click());
    await settle();
    expect(start).toHaveBeenCalledTimes(1);
    expect(dangerToasts().map((t) => t.textContent)).toEqual([message]);
    expect(button(enUS.settings.startDaemon)?.disabled).toBe(false);
    expect(settingsRunning()).toBe(enUS.overview.notRunning);
  });

  it("saves autostart alone, which leaves the running state as it is, and the daemon's stop leaves autostart as read", async () => {
    native.available = true;
    const loginItem = vi.spyOn(native, "setLoginItem").mockResolvedValue(undefined);
    await openBoth();
    await streamSays("connected");
    expect(overview()).toEqual({ running: enUS.common.yes, autostart: enUS.overview.autostartOff });

    daemon.requests.length = 0;
    await act(async () => autostartToggle().click());
    await settle();
    expect(daemon.requests.filter((r) => r.method !== "GET")).toStrictEqual([{ method: "PATCH", path: "/settings", body: { autostart: true } }]);
    // With the menu bar shown, the login item starts the tray with the daemon.
    expect(loginItem.mock.calls).toEqual([[true]]);
    expect(autostartToggle().checked).toBe(true);
    expect(overview()).toEqual({ running: enUS.common.yes, autostart: enUS.overview.autostartOn });

    daemon.up = false;
    await streamSays("daemon_stopped");
    expect(overview()).toEqual({ running: enUS.overview.notRunning, autostart: enUS.overview.autostartOn });
    expect(settingsRunning()).toBe(enUS.overview.notRunning);
  });
});
