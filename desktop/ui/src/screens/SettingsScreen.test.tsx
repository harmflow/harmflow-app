// @vitest-environment happy-dom
// Drives the Storage section's log retention field against a stand-in for the daemon's local API: the field reads the
// saved period, Apply sends the one typed, and a period the daemon rejects stays an unsaved change. What the daemon does
// with the setting — its bounds, its default, and its persistence across a restart — is app › TestLogRetentionSetting's.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { native } from "../api/native";
import type { Settings } from "../api/types";
import type { LeaveCheck } from "../components/TaskEditor";
import { Toasts } from "../components/toast";
import i18n from "../i18n";
import { patchStateForTests } from "../state/store";
import { SettingsScreen } from "./SettingsScreen";

// The daemon as the client sees it: the settings it serves, every request it received with its body, and a settings
// update applied as the daemon applies it, a period outside 1 to 3650 refused with the daemon's own error.
const daemon = vi.hoisted(() => ({
  settings: undefined as unknown as Settings,
  requests: [] as { method: string; path: string; body: unknown }[],
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
  patchStateForTests({ settings: undefined, diagnostics: undefined });
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
