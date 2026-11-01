// @vitest-environment happy-dom
// Drives two sections of Settings against a stand-in for the daemon's local API. The Storage section's log retention
// field reads the saved period, Apply sends the one typed, and a period the daemon rejects stays an unsaved change; what
// the daemon does with the setting — its bounds, its default, and its persistence across a restart — is
// app › TestLogRetentionSetting's. The Daemon section starts a stopped daemon through a stub of the desktop's bridge,
// and shows, beside the Overview, the daemon running now and autostart enabled as two states; that a daemon started by
// hand fires its tasks is itest › TestHandStartedDaemonFiresWithAutostartOff's.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { native, type DaemonStatus } from "../api/native";
import type { StreamHandlers } from "../api/transport";
import type { Health, Settings, SettingsPatch } from "../api/types";
import type { LeaveCheck } from "../components/TaskEditor";
import { Toasts } from "../components/toast";
import i18n from "../i18n";
import { enUS } from "../i18n/en-US";
import { actions, patchStateForTests, startSync } from "../state/store";
import { OverviewScreen } from "./OverviewScreen";
import { SettingsScreen } from "./SettingsScreen";

// The daemon as the client sees it: the settings and the health it serves, every request it received with its body,
// the stream it feeds, and a settings update applied as the daemon applies it, a period outside 1 to 3650 refused with
// the daemon's own error. A stopped daemon answers nothing: every request then fails as an unreachable local API does.
const daemon = vi.hoisted(() => ({
  up: true,
  settings: undefined as unknown as Settings,
  health: undefined as unknown as Health,
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
  patchStateForTests({ settings: undefined, diagnostics: undefined, health: undefined, connection: "connecting", daemonRunning: null });
});

/** Lets the stand-in daemon's answers arrive and React render them. */
const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

/** The Storage section as the app opens it: the screen reads the settings from the daemon. */
async function open() {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <>
        <SettingsScreen section="storage" onLeaveCheck={(check) => (leaveCheck = check)} />
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

  it("toasts a start the bridge rejects, and offers the start again", async () => {
    await stoppedWithAutostartOff();
    // The shell rejects a failed command with its message alone, not an Error.
    const start = vi
      .spyOn(native, "startDaemon")
      .mockRejectedValue("the daemon did not answer within 10 seconds; see logs/daemon.log in the HarmFlow folder");

    await act(async () => button(enUS.settings.startDaemon)!.click());
    await settle();
    expect(start).toHaveBeenCalledTimes(1);
    expect(dangerToasts()).toHaveLength(1);
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
