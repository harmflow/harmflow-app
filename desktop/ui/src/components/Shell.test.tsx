// @vitest-environment happy-dom
// The topbar's warning for a daemon of another version, its banner for a window and a daemon built from different
// commits, and that banner's restart of the daemon on the window's build, driven in a DOM against a stand-in for the
// daemon's local API. And the More actions menu's Stop daemon…, whose question names one running execution in the
// singular and more in the plural. And the sidebar's gauges, read from /system only while the sidebar is expanded. And
// the topbar's state of a daemon that runs but does not answer, apart from a stopped one.
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { native, type DaemonStatus } from "../api/native";
import type { Health, Stats, SystemGauges } from "../api/types";
import i18n from "../i18n";
import { formatTime } from "../lib/format";
import { getState, patchStateForTests } from "../state/store";
import { Header, Sidebar, commitsDiffer } from "./Shell";
import { Toasts } from "./toast";

// The daemon as the client sees it: an answer per method and route, and every request it received.
const daemon = vi.hoisted(() => ({
  answers: new Map<string, { status: number; body: unknown }>(),
  requests: [] as Array<{ method: string; path: string; body?: unknown }>,
}));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string, body?: unknown) => {
      daemon.requests.push({ method, path, body });
      return daemon.answers.get(`${method} ${path.split("?")[0]}`) ?? { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

const health: Health = { status: "ok", version: "0.1.0", apiVersion: "v1", pid: 4242, startedAt: "2026-10-23T09:00:00Z", acceptingFirings: true };
const banner = "This dashboard was built from commit";

let root: Root | undefined;
let host: HTMLElement | undefined;

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

beforeEach(() => {
  patchStateForTests({ appVersion: "0.1.0", appCommit: "13bd5f9", health: { ...health, commit: "f4dbab4" }, connection: "connected" });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
  daemon.answers.clear();
  daemon.requests.length = 0;
  native.available = false;
  vi.restoreAllMocks();
  patchStateForTests({ appCommit: "", health: undefined, daemonRestart: "idle" });
});

async function show() {
  host ??= document.body.appendChild(document.createElement("div"));
  root ??= createRoot(host);
  const header = createElement(Header, { screen: "executions", onNewTask: () => {}, onNavigate: () => {}, onOpenExecution: () => {} });
  await act(async () => root!.render(createElement("div", null, header, createElement(Toasts))));
}

const text = () => host!.textContent ?? "";
const restartButton = () => host!.querySelector<HTMLButtonElement>(".banner button");

describe("commitsDiffer", () => {
  it("tells different commits apart, ignores a -dirty suffix on equal hashes, and compares nothing to an empty commit", () => {
    expect(commitsDiffer("13bd5f9", "f4dbab4")).toBe(true);
    expect(commitsDiffer("13bd5f9-dirty", "f4dbab4")).toBe(true);
    expect(commitsDiffer("13bd5f9", "13bd5f9")).toBe(false);
    expect(commitsDiffer("13bd5f9", "13bd5f9-dirty")).toBe(false);
    expect(commitsDiffer("13bd5f9-dirty", "13bd5f9")).toBe(false);
    expect(commitsDiffer("", "f4dbab4")).toBe(false);
    expect(commitsDiffer("13bd5f9", "")).toBe(false);
    expect(commitsDiffer("13bd5f9", undefined)).toBe(false);
  });
});

// harmflow-app#908: a daemon whose health check timed out while its process lives is not answering, not stopped.
describe("the topbar's state of a daemon that does not answer", () => {
  it("names it in the status pill and says, in a banner, that the daemon runs and the desktop keeps trying", async () => {
    patchStateForTests({ connection: "not_answering", appCommit: "" });
    await show();

    const pill = host!.querySelector('[role="status"]');
    expect(pill?.className).toContain("conn-pill--not_answering");
    expect(pill?.textContent).toBe("Not answering");
    expect(text()).toContain("The daemon is running but not answering yet. The desktop keeps trying.");
    expect(text()).not.toContain("Daemon not running");
  });

  it("shows neither for a stopped daemon", async () => {
    patchStateForTests({ connection: "daemon_stopped", appCommit: "" });
    await show();

    expect(host!.querySelector('[role="status"]')?.textContent).toBe("Daemon not running");
    expect(text()).not.toContain("not answering yet");
  });
});

describe("the banner for a daemon built from another commit", () => {
  it("names the window's commit and the daemon's when they differ", async () => {
    await show();
    expect(text()).toContain(
      "This dashboard was built from commit 13bd5f9 and the daemon from commit f4dbab4. Restart the daemon to run this dashboard's build.",
    );
    expect(host!.querySelector(".banner")?.getAttribute("role")).toBe("alert");
  });

  it("shows nothing when the commits are equal, a -dirty suffix aside", async () => {
    patchStateForTests({ appCommit: "13bd5f9", health: { ...health, commit: "13bd5f9-dirty" } });
    await show();
    expect(text()).not.toContain(banner);
    expect(host!.querySelector(".banner")).toBeNull();
  });

  it("shows nothing when either build has no commit", async () => {
    patchStateForTests({ appCommit: "" });
    await show();
    expect(text()).not.toContain(banner);
    patchStateForTests({ appCommit: "13bd5f9", health: { ...health, commit: undefined } });
    await show();
    expect(text()).not.toContain(banner);
    patchStateForTests({ health: { ...health, commit: "" } });
    await show();
    expect(text()).not.toContain(banner);
  });

  it("offers the restart only inside the desktop shell, which can start a daemon", async () => {
    await show();
    expect(text()).toContain(banner);
    expect(restartButton()).toBeNull();
    native.available = true;
    await show();
    expect(restartButton()?.textContent).toBe("Restart daemon");
  });
});

describe("the warning for a daemon of another version", () => {
  const versionWarning = "The daemon runs version 0.1.0 and this dashboard version 0.2.0. Reinstall HarmFlow so both match.";
  const banners = () => Array.from(host!.querySelectorAll(".banner"), (b) => b.textContent ?? "");

  it("names both versions when the commits are equal", async () => {
    patchStateForTests({ appVersion: "0.2.0", appCommit: "13bd5f9", health: { ...health, commit: "13bd5f9" } });
    await show();
    expect(banners()).toEqual([versionWarning]);
  });

  it("shows above the commit banner when the commits differ too", async () => {
    patchStateForTests({ appVersion: "0.2.0", appCommit: "13bd5f9", health: { ...health, commit: "f4dbab4" } });
    native.available = true;
    await show();
    const shown = banners();
    expect(shown).toHaveLength(2);
    expect(shown[0]).toBe(versionWarning);
    expect(shown[1]).toContain(
      "This dashboard was built from commit 13bd5f9 and the daemon from commit f4dbab4. Restart the daemon to run this dashboard's build.",
    );
    expect(restartButton()?.textContent).toBe("Restart daemon");
  });

  it("names both versions when either build has no commit", async () => {
    patchStateForTests({ appVersion: "0.2.0", appCommit: "", health: { ...health, commit: "f4dbab4" } });
    await show();
    expect(banners()).toEqual([versionWarning]);
    patchStateForTests({ appCommit: "13bd5f9", health: { ...health, commit: undefined } });
    await show();
    expect(banners()).toEqual([versionWarning]);
    patchStateForTests({ appCommit: "", health: { ...health, commit: "" } });
    await show();
    expect(banners()).toEqual([versionWarning]);
  });

  it("shows nothing when the versions are equal, whatever the commits say", async () => {
    await show();
    expect(text()).not.toContain("The daemon runs version");
    expect(banners()).toHaveLength(1);
    patchStateForTests({ health: { ...health, commit: "13bd5f9" } });
    await show();
    expect(text()).not.toContain("The daemon runs version");
    expect(host!.querySelector(".banner")).toBeNull();
  });
});

describe("restarting the daemon from the banner", () => {
  it("keeps the daemon when executions are running, without force, and says so", async () => {
    native.available = true;
    daemon.answers.set("POST /daemon/stop", {
      status: 409,
      body: { error: { code: "executions_running", message: "1 executions are running; confirm to stop the daemon and cancel them" } },
    });
    const start = vi.spyOn(native, "startDaemon");
    await show();
    await act(async () => restartButton()!.click());

    expect(daemon.requests.filter((r) => r.method === "POST")).toEqual([{ method: "POST", path: "/daemon/stop", body: { force: false } }]);
    expect(start).not.toHaveBeenCalled();
    expect(getState().daemonRestart).toBe("refused");
    expect(text()).toContain("Executions are running, so the daemon kept running. Restart it once they end.");
    expect(text()).toContain(banner);
    expect(restartButton()?.disabled).toBe(false);
  });

  it("stops the daemon, then starts this bundle's once the old process exits", async () => {
    native.available = true;
    daemon.answers.set("POST /daemon/stop", { status: 202, body: { ok: true, message: "the daemon is stopping" } });
    let started!: (status: DaemonStatus) => void;
    const start = vi.spyOn(native, "startDaemon").mockReturnValue(new Promise((resolve) => (started = resolve)));
    await show();
    await act(async () => restartButton()!.click());

    expect(daemon.requests[0]).toEqual({ method: "POST", path: "/daemon/stop", body: { force: false } });
    expect(start).toHaveBeenCalledWith(4242);
    expect(getState().daemonRestart).toBe("restarting");
    expect(restartButton()?.textContent).toBe("Restarting…");
    expect(restartButton()?.disabled).toBe(true);

    await act(async () => started({ running: true, pid: 4343, commit: "13bd5f9" }));
    expect(getState().daemonRestart).toBe("idle");
    expect(daemon.requests.some((r) => r.method === "GET" && r.path === "/health")).toBe(true);
  });

  it("reports a daemon that does not come back", async () => {
    native.available = true;
    daemon.answers.set("POST /daemon/stop", { status: 202, body: { ok: true, message: "the daemon is stopping" } });
    // The shell answers a failed command with its message alone, not an Error.
    vi.spyOn(native, "startDaemon").mockRejectedValue("daemon pid 4242 did not exit within 30 seconds");
    await show();
    await act(async () => restartButton()!.click());

    expect(getState().daemonRestart).toBe("idle");
    expect(host!.querySelector(".toast--danger")?.textContent).toContain(
      "The daemon could not be restarted: daemon pid 4242 did not exit within 30 seconds",
    );
  });
});

describe("stopping the daemon from the More actions menu", () => {
  /** Stats as GET /stats answers them, with `running` executions. */
  const stats = (running: number): Stats => ({
    running,
    queued: 0,
    completedToday: 0,
    failedToday: 0,
    activity: { running: [], queued: [], completed: [], failed: [] },
  });

  afterEach(() => patchStateForTests({ stats: undefined }));

  async function stopFromMenu() {
    await show();
    await act(async () => host!.querySelector<HTMLButtonElement>('button[aria-label="More actions"]')!.click());
    await act(async () => [...host!.querySelectorAll<HTMLButtonElement>(".popover button")].find((b) => b.textContent === "Stop daemon…")!.click());
  }

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
    patchStateForTests({ stats: stats(running) });
    await stopFromMenu();
    expect(choose.mock.calls).toEqual([[message, "Stop the daemon?", wait, cancel, "Cancel"]]);
    expect(posts()).toEqual([]);
  });

  it("asks for a draining stop when the user waits for the running executions, and shows the drain at once", async () => {
    vi.spyOn(native, "choose").mockResolvedValue("first");
    daemon.answers.set("POST /daemon/stop", { status: 202, body: { ok: true, message: "the daemon stops once its running executions end" } });
    patchStateForTests({ stats: stats(2) });
    await stopFromMenu();
    expect(posts()).toEqual([{ method: "POST", path: "/daemon/stop", body: { drain: true } }]);
    expect(daemon.requests.some((r) => r.method === "GET" && r.path === "/health")).toBe(true);
  });

  it("stops at once, cancelling the running executions, when the user cancels them", async () => {
    vi.spyOn(native, "choose").mockResolvedValue("second");
    daemon.answers.set("POST /daemon/stop", { status: 202, body: { ok: true, message: "the daemon is stopping" } });
    patchStateForTests({ stats: stats(1) });
    await stopFromMenu();
    expect(posts()).toEqual([{ method: "POST", path: "/daemon/stop", body: { force: true } }]);
  });

  it("asks a plain question, and stops without force, when nothing runs", async () => {
    const choose = vi.spyOn(native, "choose");
    const confirm = vi.spyOn(native, "confirm").mockResolvedValue(true);
    daemon.answers.set("POST /daemon/stop", { status: 202, body: { ok: true, message: "the daemon is stopping" } });
    patchStateForTests({ stats: stats(0) });
    await stopFromMenu();
    expect(choose).not.toHaveBeenCalled();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(posts()).toEqual([{ method: "POST", path: "/daemon/stop", body: { force: false } }]);
  });

  it("offers to wait for or cancel the executions the daemon names when its executions_running refusal finds the count behind", async () => {
    vi.spyOn(native, "confirm").mockResolvedValue(true);
    const choose = vi.spyOn(native, "choose").mockResolvedValue("first");
    daemon.answers.set("POST /daemon/stop", {
      status: 409,
      body: {
        error: {
          code: "executions_running",
          message: "2 executions are running; confirm to stop the daemon and cancel them",
          messageKey: "executions_running",
          params: { count: "2" },
        },
      },
    });
    patchStateForTests({ stats: stats(0) });
    await stopFromMenu();
    expect(choose.mock.calls.map((c) => c.slice(2, 4))).toEqual([["Wait for them", "Cancel them"]]);
    expect(posts().map((r) => r.body)).toEqual([{ force: false }, { drain: true }]);
  });
});

describe("the draining state", () => {
  it.each([
    [1, "Stopping: the daemon waits for 1 running execution, and cancels it at"],
    [3, "Stopping: the daemon waits for 3 running executions, and cancels those left at"],
  ])("shows a drain waiting for %i executions, and when its deadline passes", async (executions, words) => {
    const deadlineAt = "2026-10-23T09:30:00Z";
    patchStateForTests({ health: { ...health, acceptingFirings: false, draining: { executions, deadlineAt } } });
    await show();
    const shown = [...host!.querySelectorAll(".banner")].map((b) => b.textContent ?? "").find((s) => s.includes("Stopping:"));
    expect(shown).toContain(words);
    expect(shown).toContain(formatTime(deadlineAt, "en-US", false));
  });

  it("shows no drain while none is under way", async () => {
    await show();
    expect(text()).not.toContain("Stopping:");
  });
});

describe("the sidebar's gauges", () => {
  const gauges: SystemGauges = { cpuPercent: 12, memoryUsedBytes: 2.1e9, memoryTotalBytes: 16e9, diskUsedBytes: 48e9, diskTotalBytes: 500e9 };
  const reads = () => daemon.requests.filter((r) => r.method === "GET" && r.path === "/system").length;
  const texts = () => Array.from(host!.querySelectorAll(".gauge__text"), (g) => g.textContent);

  /** Moves the clock by ms, letting each read it starts answer. */
  async function elapse(ms: number) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  }

  async function showSidebar(collapsed: boolean) {
    host ??= document.body.appendChild(document.createElement("div"));
    root ??= createRoot(host);
    const sidebar = createElement(Sidebar, { screen: "overview", onNavigate: () => {}, collapsed, onToggle: () => {} });
    await act(async () => root!.render(sidebar));
    // Lets the read the render started answer, without moving the clock.
    await elapse(0);
  }

  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    patchStateForTests({ system: undefined, stats: undefined });
  });

  it("reads /system at once and every 5 seconds while expanded, and shows its values", async () => {
    daemon.answers.set("GET /system", { status: 200, body: gauges });
    await showSidebar(false);
    expect(reads()).toBe(1);
    expect(getState().system).toEqual(gauges);
    expect(texts()[0]).toBe("12%");

    await elapse(4999);
    expect(reads()).toBe(1);
    await elapse(1);
    expect(reads()).toBe(2);
    await elapse(5000);
    expect(reads()).toBe(3);
  });

  it("reads nothing while collapsed, and stops reading on collapsing", async () => {
    daemon.answers.set("GET /system", { status: 200, body: gauges });
    await showSidebar(true);
    await elapse(15000);
    expect(reads()).toBe(0);

    await showSidebar(false);
    expect(reads()).toBe(1);
    await showSidebar(true);
    await elapse(15000);
    expect(reads()).toBe(1);
  });

  it("shows — in every meter against an older daemon, whose /system answers 404", async () => {
    patchStateForTests({ system: gauges });
    await showSidebar(false);
    expect(reads()).toBe(1);
    expect(getState().system).toBeUndefined();
    expect(texts()).toEqual(["—", "—", "—"]);
  });

  it("keeps the gauges it has when /system fails otherwise", async () => {
    patchStateForTests({ system: gauges });
    daemon.answers.set("GET /system", { status: 0, body: null });
    await showSidebar(false);
    expect(reads()).toBe(1);
    expect(getState().system).toEqual(gauges);
    expect(texts()[0]).toBe("12%");
  });

  it("shows the counters' running badge from /stats, which brings no gauges", async () => {
    daemon.answers.set("GET /system", { status: 200, body: {} });
    patchStateForTests({ stats: { running: 3, queued: 0, completedToday: 0, failedToday: 0, activity: { running: [], queued: [], completed: [], failed: [] } } });
    await showSidebar(false);
    expect(host!.querySelector(".nav__badge")?.textContent).toBe("3");
    expect(texts()).toEqual(["—", "—", "—"]);
  });
});
