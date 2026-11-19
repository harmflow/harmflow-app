// @vitest-environment happy-dom
// Drives the Storage section's log retention field against a stand-in for the daemon's local API: the field reads the
// saved period, Apply sends the one typed, and a period the daemon rejects stays an unsaved change. What the daemon does
// with the setting — its bounds, its default, and its persistence across a restart — is app › TestLogRetentionSetting's.
// Drives the same section's Clean workspaces control too: one clean-up request, no second click while it runs, and
// what the daemon removed and kept, or its error. Which worktrees the daemon removes is app › CleanWorkspaces' tests'.
// Drives the AI tools section's general system prompt too: a draft until Save, the text sent alone, an emptied field
// that clears it, its character counter in code points, a text over 20,000 characters that marks the field and cannot
// be saved, and a text the daemon refuses shown on the field. And the General section's Sidebar group, whose two
// settings each save as soon as they flip; what the sidebar does with them is Shell.test's.
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
import { api } from "../api/client";
import { native, type DaemonStatus } from "../api/native";
import type { StreamHandlers } from "../api/transport";
import type { Diagnostics, Harness, Health, Settings, SettingsPatch, Stats, Tool } from "../api/types";
import { COUNTER_KEYS } from "../components/Aside";
import type { LeaveCheck } from "../components/TaskEditor";
import { Toasts } from "../components/toast";
import i18n from "../i18n";
import { enUS } from "../i18n/en-US";
import { actions, patchStateForTests, startSync } from "../state/store";
import { OverviewScreen } from "./OverviewScreen";
import { ExecutionsScreen } from "./ExecutionsScreen";
import { SECTION_GROUPS, SettingsScreen, type SettingsSection } from "./SettingsScreen";

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
        if (rest.systemPrompt !== undefined && [...rest.systemPrompt].length > 20000) {
          return {
            status: 422,
            body: {
              error: {
                code: "validation_failed",
                message: "systemPrompt must be at most 20000 characters",
                messageKey: "settings_system_prompt_too_long",
              },
            },
          };
        }
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
      if (method === "POST" && path === "/daemon/stop") return { status: 202, body: { ok: true, message: "accepted" } };
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
  showSystemWidget: true,
  showGitHubWidget: true,
  notificationLevel: "all",
  logRetentionDays: 30,
  theme: "harmflow-dark",
  autostart: { enabled: false, mechanism: "launchd" },
  pathVariables: {},
  allowedFolders: [],
  onboardingCompleted: true,
  quota: {},
  systemPrompt: "",
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

  it("shows each AI tool's quota check interval, five minutes when none is set, and saves it beside the share", async () => {
    daemon.settings = { ...daemon.settings, quota: { codex: { maxPercent: 80, checkIntervalMinutes: 15 } } };
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    const interval = (id: string) => host!.querySelector<HTMLInputElement>(`#settings-quota-interval-${id}`)!;
    const applyInterval = (id: string) => interval(id).closest(".inline-form")!.querySelector<HTMLButtonElement>("button")!;
    expect([interval("claude-code").value, interval("claude-code").min, interval("claude-code").max]).toEqual(["5", "1", "60"]);
    expect(interval("codex").value).toBe("15");
    expect(applyInterval("claude-code").disabled).toBe(true);
    daemon.requests.length = 0;

    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    const typeInterval = async (value: string) =>
      act(async () => {
        setValue.call(interval("claude-code"), value);
        interval("claude-code").dispatchEvent(new Event("input", { bubbles: true }));
      });
    await typeInterval("61");
    expect(applyInterval("claude-code").disabled).toBe(true);
    await typeInterval("30");
    await click(applyInterval("claude-code"));

    expect(updates().map((r) => r.body)).toStrictEqual([
      { quota: { codex: { maxPercent: 80, checkIntervalMinutes: 15 }, "claude-code": { maxPercent: 100, checkIntervalMinutes: 30 } } },
    ]);
    expect(interval("claude-code").value).toBe("30");

    // Moving the share afterwards keeps the interval.
    daemon.requests.length = 0;
    await drag("claude-code", 90);
    expect(updates().map((r) => r.body)).toStrictEqual([
      { quota: { codex: { maxPercent: 80, checkIntervalMinutes: 15 }, "claude-code": { maxPercent: 90, checkIntervalMinutes: 30 } } },
    ]);
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

// harmflow-app#849: the general system prompt's field, in the AI tools section. What the daemon does with the text —
// its bounds, its default, and its delivery to each session — is app › TestSystemPrompt* and
// app › TestEveryAttemptReceivesTheGeneralSystemPrompt's.
describe("the AI tools section's general system prompt", () => {
  const area = () => host!.querySelector<HTMLTextAreaElement>("#settings-system-prompt")!;
  const save = () => area().closest(".row-list")!.querySelector<HTMLButtonElement>(".inline-actions button")!;
  const fieldError = () => text(area().closest(".field")!.querySelector(".field__error"));
  const counter = () => area().closest(".field")!.querySelector(".field__count");
  /** Whether the field is marked in red, as over the limit or refused. */
  const marked = () => area().closest(".field")!.classList.contains("field--error");

  /** Types into the text area as React sees it. */
  async function write(value: string) {
    const setValue = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(area(), value);
      area().dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  it("reads empty on settings without the text, with its label, its help, and Save disabled", async () => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    expect(text(panel("General system prompt")?.querySelector(".field__label"))).toBe("Instructions for every session");
    expect(text(area().closest(".field")!.querySelector(".field__hint"))).toBe(enUS.settings.systemPromptHint);
    expect(area().value).toBe("");
    expect(save().disabled).toBe(true);
    expect(leaveCheck).toBeNull();
  });

  it("holds a draft until Save, then sends the text alone, reads the settings again, and shows it saved", async () => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    const written = "Answer in pt-BR.\nNever touch the CI credentials.";
    await write(written);
    expect(save().disabled).toBe(false);
    expect(leaveCheck).not.toBeNull();

    daemon.requests.length = 0;
    await click(save());
    expect(updates().map((r) => r.body)).toStrictEqual([{ systemPrompt: written }]);
    const after = daemon.requests.slice(daemon.requests.indexOf(updates()[0]) + 1);
    expect(after).toContainEqual({ method: "GET", path: "/settings", body: undefined });
    expect(daemon.settings.systemPrompt).toBe(written);
    expect(toasts()).toEqual([{ tone: "success", text: "Settings saved." }]);
    expect(area().value).toBe(written);
    expect(save().disabled).toBe(true);
    expect(leaveCheck).toBeNull();
  });

  it("clears the text when the emptied field is saved", async () => {
    daemon.settings = { ...daemon.settings, systemPrompt: "Answer in pt-BR." };
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    expect(area().value).toBe("Answer in pt-BR.");

    await write("");
    daemon.requests.length = 0;
    await click(save());
    expect(updates().map((r) => r.body)).toStrictEqual([{ systemPrompt: "" }]);
    expect(daemon.settings.systemPrompt).toBe("");
    expect(area().value).toBe("");
    expect(save().disabled).toBe(true);
  });

  it("counts the text's characters in code points on every change, a paste included, an emoji outside the BMP as one", async () => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    expect(text(counter())).toBe("0 / 20,000");

    await write("abc");
    expect(text(counter())).toBe("3 / 20,000");
    // A paste of 10 characters reaches the field as one change holding the whole text.
    await write("abc" + "paste 😀 ok");
    expect(text(counter())).toBe("13 / 20,000");
    expect(counter()!.classList.contains("field__count--error")).toBe(false);
    // No maxLength, which counts UTF-16 code units and would cut a paste or stop typing at the limit.
    expect(area().hasAttribute("maxlength")).toBe(false);
  });

  it("keeps the counter's normal style below the limit, and at exactly 20,000 characters styles the counter alone and saves", async () => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    // Each emoji is one code point and two UTF-16 code units: 20,000 of them are 40,000 units, which the field accepts.
    await write("😀".repeat(19999));
    expect(text(counter())).toBe("19,999 / 20,000");
    expect(counter()!.classList.contains("field__count--error")).toBe(false);
    expect(marked()).toBe(false);

    const most = "😀".repeat(20000);
    await write(most);
    expect(text(counter())).toBe("20,000 / 20,000 · limit reached");
    expect(counter()!.classList.contains("field__count--error")).toBe(true);
    expect(marked()).toBe(false);
    expect(fieldError()).toBeUndefined();
    expect(area().hasAttribute("aria-invalid")).toBe(false);
    expect(save().disabled).toBe(false);

    daemon.requests.length = 0;
    await click(save());
    expect(updates().map((r) => r.body)).toStrictEqual([{ systemPrompt: most }]);
    expect(daemon.settings.systemPrompt).toBe(most);
    expect(toasts()).toEqual([{ tone: "success", text: "Settings saved." }]);
  });

  it("gives the counter its normal style alone below the limit, with no warning style however close the text comes", async () => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    // The whole class list, not the absence of named classes: a style of any name added below the limit fails it.
    await write("abc" + "paste 😀 ok");
    expect(text(counter())).toBe("13 / 20,000");
    expect([...counter()!.classList]).toEqual(["field__count"]);

    await write("😀".repeat(19999));
    expect(text(counter())).toBe("19,999 / 20,000");
    expect([...counter()!.classList]).toEqual(["field__count"]);
  });

  it("lets typing take the text past the limit, marking the field and allowing no Save until the user deletes it", async () => {
    const most = "é".repeat(20000);
    daemon.settings = { ...daemon.settings, systemPrompt: most };
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    expect(marked()).toBe(false);

    await write(most + "é");
    expect(area().value).toBe(most + "é");
    expect(text(counter())).toBe("20,001 / 20,000 · over the limit");
    expect(counter()!.classList.contains("field__count--error")).toBe(true);
    expect(marked()).toBe(true);
    expect(fieldError()).toBe("The text is over the 20,000-character limit. Shorten it to save.");
    expect(area().getAttribute("aria-invalid")).toBe("true");
    expect(save().disabled).toBe(true);
    expect(leaveCheck).not.toBeNull();
    daemon.requests.length = 0;
    await click(save());
    expect(updates()).toEqual([]);

    await write(most);
    expect(marked()).toBe(false);
    expect(area().hasAttribute("aria-invalid")).toBe(false);
  });

  it("takes a paste past the limit whole, marking the field and allowing no Save until the user trims it", async () => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    const start = "a".repeat(19990);
    await write(start);
    await write(start + "b".repeat(20));
    expect(area().value).toBe(start + "b".repeat(20));
    expect(text(counter())).toBe("20,010 / 20,000 · over the limit");
    expect(marked()).toBe(true);
    expect(save().disabled).toBe(true);

    await write(start + "b".repeat(10));
    expect(marked()).toBe(false);
    expect(fieldError()).toBeUndefined();
    expect(save().disabled).toBe(false);
  });

  it("marks an over-limit text saved before and allows no Save until the user trims it", async () => {
    daemon.settings = { ...daemon.settings, systemPrompt: "x".repeat(20001) };
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    expect(text(counter())).toBe("20,001 / 20,000 · over the limit");
    expect(marked()).toBe(true);
    expect(save().disabled).toBe(true);

    await write("x".repeat(20000));
    expect(marked()).toBe(false);
    expect(save().disabled).toBe(false);
  });

  it("shows a text the daemon refuses on the field, keeping it a draft", async () => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools });
    await open("agents");
    vi.spyOn(api, "updateSettings").mockRejectedValueOnce(new Error("systemPrompt is invalid"));
    await write("Answer in pt-BR.");
    await click(save());
    expect(fieldError()).toBe("systemPrompt is invalid");
    expect(area().getAttribute("aria-invalid")).toBe("true");
    expect(area().value).toBe("Answer in pt-BR.");
    expect(save().disabled).toBe(false);
    expect(leaveCheck).not.toBeNull();
  });
});

describe("the General section's Sidebar group", () => {
  const widgets = [
    ["Show System widget", "showSystemWidget", "CPU, RAM, and disk gauges"],
    ["Show GitHub widget", "showGitHubWidget", "GitHub rate-limit gauges"],
  ] as const;
  const toggle = (label: string) => settingRow(label)!.querySelector<HTMLInputElement>("input[type=checkbox]")!;

  it("holds the two widgets' settings, each on and with its hint, after the existing groups", async () => {
    await open("general");
    const group = panel("Sidebar")!;
    expect(group).not.toBeNull();
    expect(group.nextElementSibling).toBeNull();
    expect([...group.querySelectorAll(".setting-row__label")].map((l) => text(l))).toEqual(widgets.map(([label]) => label));
    for (const [label, , gauges] of widgets) {
      expect(toggle(label).checked).toBe(true);
      expect(text(settingRow(label)!.querySelector(".field__hint"))).toBe(
        `Shows or hides the ${gauges} in the expanded sidebar. While hidden, HarmFlow does not read them.`,
      );
    }
  });

  it.each(widgets)("saves %s as soon as it is flipped, alone, and leaves the other setting on", async (label, key) => {
    await open("general");
    daemon.requests.length = 0;
    await click(toggle(label));
    expect(updates().map((r) => r.body)).toStrictEqual([{ [key]: false }]);
    expect(daemon.settings[key]).toBe(false);
    expect(toggle(label).checked).toBe(false);
    const [other] = widgets.filter(([l]) => l !== label);
    expect(toggle(other[0]).checked).toBe(true);
    expect(toasts()).toEqual([{ tone: "success", text: "Settings saved." }]);

    // Flipped back, it is saved on again.
    await click(toggle(label));
    expect(updates().map((r) => r.body)).toStrictEqual([{ [key]: false }, { [key]: true }]);
    expect(toggle(label).checked).toBe(true);
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

  const posts = () => daemon.requests.filter((r) => r.method === "POST");

  it.each([
    [
      1,
      "1 execution is running. Wait for it to finish, and the daemon stops then, within 30 minutes at most; or cancel it now, and it will be recorded as interrupted.",
      "Wait for it",
      "Cancel it",
    ],
    [
      2,
      "2 executions are running. Wait for them to finish, and the daemon stops then, within 30 minutes at most; or cancel them now, and they will be recorded as interrupted.",
      "Wait for them",
      "Cancel them",
    ],
  ])("offers to wait for or cancel %i running, in the number's form", async (running, message, wait, cancel) => {
    const choose = vi.spyOn(native, "choose").mockResolvedValue("cancel");
    patchStateForTests({ connection: "connected", stats: stats(running) });
    await open("daemon");
    await click(stopButton());
    expect(choose.mock.calls).toEqual([[message, "Stop the daemon?", wait, cancel, "Cancel"]]);
    expect(posts()).toEqual([]);
  });

  it("asks for a draining stop when the user waits for the running executions, and says so", async () => {
    vi.spyOn(native, "choose").mockResolvedValue("first");
    patchStateForTests({ connection: "connected", stats: stats(2) });
    await open("daemon");
    await click(stopButton());
    expect(posts()).toEqual([{ method: "POST", path: "/daemon/stop", body: { drain: true } }]);
    expect(toasts()).toEqual([{ tone: "warning", text: "The daemon stops once the running executions finish." }]);
  });

  it("stops at once, cancelling the running executions, when the user cancels them", async () => {
    vi.spyOn(native, "choose").mockResolvedValue("second");
    patchStateForTests({ connection: "connected", stats: stats(1) });
    await open("daemon");
    await click(stopButton());
    expect(posts()).toEqual([{ method: "POST", path: "/daemon/stop", body: { force: true } }]);
    expect(toasts()).toEqual([{ tone: "warning", text: "The daemon is stopping." }]);
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

// The Overview's spend band keeps its view, cost or tokens, across launches in the user's preferences, with no row in
// Settings: no section holds a control, a label, or a row for it. The texts are the band's own, through i18n, so the
// test follows the locale it renders in. The AI tools section's quota sliders, which speak of what HarmFlow may spend,
// are not the band's.
describe("the spend band's view", () => {
  const sections: SettingsSection[] = [...SECTION_GROUPS.flatMap((g) => g.sections), "about"];

  it.each(sections)("has no control, label, or row in the %s section", async (section) => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools, diagnostics });
    await open(section);
    expect(text(host!.querySelector(".settings__title"))).toBe(i18n.t(`settings.sections.${section}`));

    const selector = i18n.t("overview.spend.view");
    const choices = [i18n.t("overview.spend.cost"), i18n.t("overview.spend.tokens")];
    // Nothing labeled with the selector's text, in its text, its accessible name, or its tooltip.
    expect(host!.textContent).not.toContain(selector);
    const labeled = [...host!.querySelectorAll("[aria-label], [title]")].filter(
      (el) => el.getAttribute("aria-label")?.includes(selector) || el.getAttribute("title")?.includes(selector),
    );
    expect(labeled.map((el) => el.outerHTML)).toEqual([]);
    // No element with the text, the accessible name, or the tooltip of either choice.
    const named = [...host!.querySelectorAll("*")].filter((el) =>
      choices.some((c) => text(el) === c || el.getAttribute("aria-label") === c || el.getAttribute("title") === c),
    );
    expect(named.map((el) => el.outerHTML)).toEqual([]);
  });

  // The band's Harness toggle is kept the same way, beside the band's view, with no row in Settings either.
  it.each(sections)("has no control, label, or row for the Harness toggle in the %s section", async (section) => {
    patchStateForTests({ harnesses: [claudeCode, codex], tools, diagnostics });
    await open(section);
    expect(text(host!.querySelector(".settings__title"))).toBe(i18n.t(`settings.sections.${section}`));

    const toggle = i18n.t("overview.spend.harness");
    // No element with the toggle's text, accessible name, or tooltip.
    const named = [...host!.querySelectorAll("*")].filter(
      (el) => text(el) === toggle || el.getAttribute("aria-label") === toggle || el.getAttribute("title") === toggle,
    );
    expect(named.map((el) => el.outerHTML)).toEqual([]);
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

describe("the Appearance section's metric cards", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => localStorage.clear());

  const label = (key: string) => i18n.t(`counters.${key}` as "counters.running");
  const list = (screen: "overview" | "executions") =>
    host!.querySelector(`[aria-labelledby="metric-cards-${screen}"]`)!;
  const row = (screen: "overview" | "executions", key: string) =>
    list(screen).querySelector<HTMLElement>(`[data-card="${key}"]`)!;
  const order = (screen: "overview" | "executions") =>
    [...list(screen).querySelectorAll<HTMLElement>("[data-card]")].map((r) => [r.dataset.card, r.querySelector("input")!.checked]);
  const click = async (el: Element) => {
    await act(async () => (el as HTMLElement).click());
  };
  const restoreButton = (screen: "overview" | "executions") =>
    [...list(screen).querySelectorAll("button")].find((b) => text(b) === i18n.t("settings.metricCards.restore"))!;

  /** The two screens as the app opens them after Settings, each reading the kept choice when it mounts. */
  async function openScreens() {
    act(() => root?.unmount());
    host?.remove();
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () =>
      root!.render(
        <>
          <div className="as-overview">
            <OverviewScreen onOpenExecution={() => {}} onNewTask={() => {}} onOpenTask={() => {}} />
          </div>
          <div className="as-executions">
            <ExecutionsScreen selectedId={null} onSelect={() => {}} onGoToTasks={() => {}} onSeeHistory={() => {}} />
          </div>
        </>,
      ),
    );
    await settle();
  }
  const shown = (screen: "overview" | "executions") =>
    [...host!.querySelectorAll(`.as-${screen} .counters .counter__label`)].map((l) => text(l));
  // The expected lists come from the app's cards, never a copy of today's: a card a later version adds joins them.
  const others = (...keys: string[]) => COUNTER_KEYS.filter((key) => !keys.includes(key));

  it("lists each screen's cards on its own, every one shown in the default order, with nothing kept", async () => {
    await open("appearance");
    expect(text(host!.querySelector("#metric-cards-overview"))).toBe(i18n.t("nav.overview"));
    expect(text(host!.querySelector("#metric-cards-executions"))).toBe(i18n.t("nav.executions"));
    const all = COUNTER_KEYS.map((key) => [key, true]);
    expect(order("overview")).toEqual(all);
    expect(order("executions")).toEqual(all);
    expect(restoreButton("overview").disabled).toBe(true);

    await openScreens();
    const labels = COUNTER_KEYS.map(label);
    expect(shown("overview")).toEqual(labels);
    expect(shown("executions")).toEqual(labels);
  });

  it("shows on each screen, after a restart, the cards chosen and their order, and no row where every card is hidden", async () => {
    await open("appearance");
    await click(row("overview", "queued").querySelector("input")!);
    for (let at = COUNTER_KEYS.indexOf("failedToday"); at > 0; at--) {
      await click(row("overview", "failedToday").querySelector(".metric-cards__up")!);
    }
    for (const key of COUNTER_KEYS) {
      await click(row("executions", key).querySelector("input")!);
    }
    const rest = others("failedToday");
    expect(order("overview")).toEqual([["failedToday", true], ...rest.map((key) => [key, key !== "queued"])]);
    expect(row("overview", "failedToday").querySelector<HTMLButtonElement>(".metric-cards__up")!.disabled).toBe(true);
    expect(row("overview", rest[rest.length - 1]).querySelector<HTMLButtonElement>(".metric-cards__down")!.disabled).toBe(true);

    // A restart reads the choice back from the desktop's storage.
    await openScreens();
    expect(shown("overview")).toEqual(["failedToday", ...others("failedToday", "queued")].map(label));
    expect(host!.querySelector(".as-executions .counters")).toBeNull();
    expect(host!.querySelector(".as-executions .screen__main")).not.toBeNull();
  });

  it("restores a screen's default cards and order, leaving the other screen's choice", async () => {
    await open("appearance");
    await click(row("overview", "running").querySelector("input")!);
    await click(row("overview", "failedToday").querySelector(".metric-cards__up")!);
    await click(row("executions", "queued").querySelector("input")!);
    expect(restoreButton("overview").disabled).toBe(false);
    await click(restoreButton("overview"));
    expect(order("overview")).toEqual(COUNTER_KEYS.map((key) => [key, true]));
    expect(restoreButton("overview").disabled).toBe(true);

    await openScreens();
    expect(shown("overview")).toEqual(COUNTER_KEYS.map(label));
    expect(shown("executions")).toEqual(others("queued").map(label));
  });

  it("shows a card a later version adds after the kept ones, without losing the kept choice", async () => {
    localStorage.setItem(
      "harmflow.metricCards",
      JSON.stringify({ overview: [{ key: "failedToday", shown: true }, { key: "running", shown: false }, { key: "retired", shown: true }] }),
    );
    await open("appearance");
    const joined = others("failedToday", "running");
    expect(order("overview")).toEqual([["failedToday", true], ["running", false], ...joined.map((key) => [key, true])]);
    await openScreens();
    expect(shown("overview")).toEqual(["failedToday", ...joined].map(label));
  });
});
