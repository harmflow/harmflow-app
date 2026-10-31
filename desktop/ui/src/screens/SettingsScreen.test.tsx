// @vitest-environment happy-dom
// Drives the Storage section's log retention field against a stand-in for the daemon's local API: the field reads the
// saved period, Apply sends the one typed, and a period the daemon rejects stays an unsaved change. What the daemon does
// with the setting — its bounds, its default, and its persistence across a restart — is app › TestLogRetentionSetting's.
// Drives the same section's Clean workspaces control too: one clean-up request, no second click while it runs, and
// what the daemon removed and kept, or its error. Which worktrees the daemon removes is app › CleanWorkspaces' tests'.
// Then shows what AI tools, Security, and Storage read from the harnesses, the tools, and the diagnostics in the store:
// each harness's readiness and path, the command-line tools, the token store, the disk used, and the directories with
// their Show in Finder buttons, which ask the desktop's bridge to open the row's path.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { native } from "../api/native";
import type { Diagnostics, Harness, Settings, Tool } from "../api/types";
import type { LeaveCheck } from "../components/TaskEditor";
import { Toasts } from "../components/toast";
import i18n from "../i18n";
import { patchStateForTests } from "../state/store";
import { SettingsScreen, type SettingsSection } from "./SettingsScreen";

// The daemon as the client sees it: the settings it serves, every request it received with its body, a settings
// update applied as the daemon applies it, a period outside 1 to 3650 refused with the daemon's own error, and the
// answer each test gives a workspace clean-up.
const daemon = vi.hoisted(() => ({
  settings: undefined as unknown as Settings,
  requests: [] as { method: string; path: string; body: unknown }[],
  clean: undefined as undefined | (() => Promise<{ status: number; body: unknown }>),
}));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string, body?: unknown) => {
      daemon.requests.push({ method, path, body });
      if (method === "GET" && path === "/settings") return { status: 200, body: daemon.settings };
      if (method === "PATCH" && path === "/settings") {
        const days = (body as { logRetentionDays?: number }).logRetentionDays;
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
        daemon.settings = { ...daemon.settings, ...(body as Partial<Settings>) };
        return { status: 200, body: daemon.settings };
      }
      if (method === "POST" && path === "/workspaces/clean" && daemon.clean) return daemon.clean();
      return { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

const saved: Settings = {
  language: "en-US",
  trayEnabled: true,
  keepTrayOnQuit: false,
  notificationLevel: "all",
  logRetentionDays: 30,
  theme: "harmflow-dark",
  autostart: { enabled: false, mechanism: "launchd" },
  pathVariables: {},
  allowedFolders: [],
  onboardingCompleted: true,
};

let root: Root | undefined;
let host: HTMLElement | undefined;
/** The question the screen gives its parent to ask before leaving, while a draft is held. */
let leaveCheck: LeaveCheck | null = null;

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

// A toast stays 3.5 seconds in a list the module keeps; a fake clock for its timeout ends each test's toasts with the
// test, so that none shows in the next.
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  daemon.settings = saved;
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
  leaveCheck = null;
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
  vi.restoreAllMocks();
  daemon.requests.length = 0;
  daemon.clean = undefined;
  patchStateForTests({ settings: undefined, diagnostics: undefined, harnesses: [], tools: [] });
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
