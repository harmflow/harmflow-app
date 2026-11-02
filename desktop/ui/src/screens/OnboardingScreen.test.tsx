// @vitest-environment happy-dom
// Drives the onboarding screen against a stand-in for the daemon's local API, as a fresh data folder answers it: the
// autostart option is offered unchecked, and finishing onboarding turns autostart on only when the user checked it.
// What the daemon does with the setting — a fresh daemon reads it off, and writes its launchd agent only on request —
// is the daemon's tests'; that the system then starts it at the next login needs a real login session.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi, type Mock, type MockInstance } from "vitest";
import { native } from "../api/native";
import type { Settings, SettingsPatch } from "../api/types";
import { Toasts } from "../components/toast";
import i18n from "../i18n";
import { patchStateForTests } from "../state/store";
import { DaemonDownScreen, OnboardingScreen } from "./OnboardingScreen";

// The daemon as the client sees it: the settings it serves, every request it received with its body, and a settings
// update applied as the daemon applies it, `autostart` turning the service on.
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
        const { autostart, ...rest } = body as SettingsPatch;
        daemon.settings = {
          ...daemon.settings,
          ...rest,
          ...(autostart !== undefined ? { autostart: { ...daemon.settings.autostart, enabled: autostart } } : {}),
        } as Settings;
        return { status: 200, body: daemon.settings };
      }
      return { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

// The settings a daemon serves from a fresh data folder: onboarding not done, autostart off.
const fresh: Settings = {
  language: "system",
  trayEnabled: true,
  keepTrayOnQuit: false,
  notificationLevel: "all",
  logRetentionDays: 30,
  theme: "harmflow-dark",
  autostart: { enabled: false, mechanism: "launchd" },
  pathVariables: {},
  allowedFolders: [],
  onboardingCompleted: false,
};

let root: Root | undefined;
let host: HTMLElement | undefined;
let finished: Mock<() => void>;
let loginItem: MockInstance<typeof native.setLoginItem>;

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

beforeEach(() => {
  daemon.settings = fresh;
  finished = vi.fn<() => void>();
  // The criterion is whether the screen asks the shell for the login item, not what the operating system does.
  loginItem = vi.spyOn(native, "setLoginItem").mockResolvedValue();
  // Finishing sets the connection first: with the daemon down, finishing sends nothing, which is not this behavior.
  patchStateForTests({ connection: "connected", settings: fresh });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
  vi.restoreAllMocks();
  daemon.requests.length = 0;
  patchStateForTests({ connection: "connecting", settings: undefined, diagnostics: undefined });
});

/** Lets the stand-in daemon's answers arrive and React render them. */
const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

async function open() {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () => root!.render(<OnboardingScreen onFinish={finished} onCreateTask={() => {}} onImport={() => {}} />));
  await settle();
}

const heading = () => host!.querySelector("h2")!.textContent;
const button = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label)!;
const checkbox = () => host!.querySelector<HTMLInputElement>('.onboarding__step input[type="checkbox"]');
const updates = () => daemon.requests.filter((r) => r.method === "PATCH" && r.path === "/settings");

async function click(target: HTMLElement) {
  await act(async () => target.click());
  await settle();
}

/** Goes from the first step to the one that offers autostart. */
async function toAutostartStep() {
  await click(button("Next"));
  await click(button("Next"));
  expect(heading()).toBe(i18n.t("onboarding.daemon.title"));
}

/** Goes on from the autostart step, leaves the first task for later, and finishes onboarding. */
async function finish() {
  await click(button("Next"));
  expect(heading()).toBe(i18n.t("onboarding.firstTask.title"));
  await click(button("Later"));
  expect(heading()).toBe(i18n.t("onboarding.done.title"));
  daemon.requests.length = 0;
  await click(button("Go to live executions"));
  expect(finished).toHaveBeenCalledTimes(1);
}

describe("the onboarding screen's autostart option", () => {
  it("is offered unchecked on first use, with its hint", async () => {
    await open();
    expect(checkbox()).toBeNull();
    await toAutostartStep();

    const box = checkbox();
    expect(box).not.toBeNull();
    expect(box!.checked).toBe(false);
    expect(box!.closest("label")!.textContent).toBe("Start the daemon automatically with the system");
    expect(host!.querySelector(".onboarding__step .field__hint")!.textContent).toBe(
      "Unchecked by default; you can change it later in Settings.",
    );
  });

  it("left unchecked, finishing completes onboarding without autostart and sets no login item", async () => {
    await open();
    await toAutostartStep();
    await finish();

    expect(updates()).toHaveLength(1);
    expect(updates()[0].body).toStrictEqual({ onboardingCompleted: true });
    expect(loginItem).not.toHaveBeenCalled();
    expect(daemon.settings.autostart.enabled).toBe(false);
  });

  it("checked, finishing turns autostart on and sets the login item once", async () => {
    await open();
    await toAutostartStep();
    await click(checkbox()!);
    expect(checkbox()!.checked).toBe(true);
    await finish();

    expect(updates()).toHaveLength(1);
    expect(updates()[0].body).toStrictEqual({ onboardingCompleted: true, autostart: true });
    expect(loginItem).toHaveBeenCalledTimes(1);
    expect(loginItem).toHaveBeenCalledWith(fresh.trayEnabled);
    expect(daemon.settings.autostart.enabled).toBe(true);
  });
});

// The shell answers a failed command with its message alone, not an Error (harmflow-app#725).
describe("a start of the daemon the bridge rejects", () => {
  const failure = "the daemon did not answer within 10 seconds; see logs/daemon.log in the HarmFlow folder";

  beforeEach(() => {
    // A toast stays 3.5 seconds in a list the module keeps; a fake clock for its timeout ends it with the test.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    native.available = true;
    patchStateForTests({ connection: "daemon_stopped" });
    vi.spyOn(native, "startDaemon").mockRejectedValue(failure);
  });

  afterEach(() => {
    native.available = false;
    vi.runOnlyPendingTimers();
    vi.useRealTimers();
  });

  async function render(screen: ReactNode) {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () =>
      root!.render(
        <>
          {screen}
          <Toasts />
        </>,
      ),
    );
    await settle();
  }

  it("shows the bridge's message in the onboarding's toast", async () => {
    await render(<OnboardingScreen onFinish={finished} onCreateTask={() => {}} onImport={() => {}} />);
    await toAutostartStep();
    await click(button("Start the daemon now"));

    expect(native.startDaemon).toHaveBeenCalledTimes(1);
    expect([...host!.querySelectorAll(".toast--danger")].map((t) => t.textContent)).toEqual([`The daemon did not start: ${failure}`]);
    expect(button("Start the daemon now").disabled).toBe(false);
  });

  it("shows the bridge's message on the daemon-down screen", async () => {
    await render(<DaemonDownScreen />);
    await click(button("Start daemon"));

    expect(native.startDaemon).toHaveBeenCalledTimes(1);
    expect(host!.querySelector(".banner")!.textContent).toBe(`The daemon did not start: ${failure}`);
  });
});
