// @vitest-environment happy-dom
// The topbar's warning for a daemon of another version, its banner for a window and a daemon built from different
// commits, and that banner's restart of the daemon on the window's build, driven in a DOM against a stand-in for the
// daemon's local API. And the More actions menu's Stop daemon…, whose question names one running execution in the
// singular and more in the plural. And the sidebar's gauges, read from /system, and GitHub's from /github/rate-limit,
// only while the sidebar is expanded and each widget's setting is on. And the topbar's state of a daemon that runs but
// does not answer, apart from a stopped one. And, from the rules app.css writes, that a hidden widget leaves the
// sidebar's foot no space or border. And the sidebar's brand, whose name beside the mark no typeface choice changes.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { native, type DaemonStatus } from "../api/native";
import type { GitHubRateLimit, Health, Settings, Stats, SystemGauges } from "../api/types";
import { applyTypeface } from "../design/typeface";
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

// harmflow-app#1091: a daemon whose health reads starting is still loading its state and recovering interrupted
// executions: the topbar says so, and that the desktop waits for it, rather than show the connection as lost.
describe("the topbar's state of a daemon still starting", () => {
  it("names it in the status pill and says, in a banner, that the daemon is starting and the desktop waits", async () => {
    patchStateForTests({ connection: "starting", appCommit: "" });
    await show();

    const pill = host!.querySelector('[role="status"]');
    expect(pill?.className).toContain("conn-pill--starting");
    expect(pill?.textContent).toBe("Daemon starting");
    expect(text()).toContain(
      "The daemon is starting: it is loading its state and recovering interrupted executions. The desktop waits for it.",
    );
    expect(text()).not.toContain("The connection to the daemon was lost");
    expect(text()).not.toContain("not answering yet");
  });

  it("shows no starting banner once the daemon has started", async () => {
    patchStateForTests({ connection: "connected", appCommit: "" });
    await show();

    expect(text()).not.toContain("The daemon is starting");
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
    noWorkToday: 0,
    activity: { running: [], queued: [], completed: [], failed: [], noWork: [] },
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
  const texts = () => Array.from(host!.querySelectorAll("[aria-labelledby='gauges-title'] .gauge__text"), (g) => g.textContent);

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

  const settings: Settings = {
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

  beforeEach(() => {
    vi.useFakeTimers();
    patchStateForTests({ settings });
  });
  afterEach(() => {
    vi.useRealTimers();
    patchStateForTests({ system: undefined, github: undefined, stats: undefined, settings: undefined });
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

  it("titles the gauges System while expanded, and hides the title collapsed", async () => {
    daemon.answers.set("GET /system", { status: 200, body: gauges });
    await showSidebar(false);
    const panel = host!.querySelector("#gauges-title")?.closest("section.gauges-panel");
    expect(panel?.getAttribute("aria-labelledby")).toBe("gauges-title");
    expect(host!.querySelector("#gauges-title")?.textContent).toBe("System");

    await showSidebar(true);
    expect(host!.querySelector("#gauges-title")).toBeNull();
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
    patchStateForTests({ stats: { running: 3, queued: 0, completedToday: 0, failedToday: 0, noWorkToday: 0, activity: { running: [], queued: [], completed: [], failed: [], noWork: [] } } });
    await showSidebar(false);
    expect(host!.querySelector(".nav__badge")?.textContent).toBe("3");
    expect(texts()).toEqual(["—", "—", "—"]);
  });

  describe("GitHub's", () => {
    const limits: GitHubRateLimit = {
      rest: { limit: 2000, used: 500, resetAt: "2026-11-14T10:00:00Z" },
      graphql: { limit: 5000, used: 1000, resetAt: "2026-11-14T10:30:00Z" },
    };
    const githubReads = () => daemon.requests.filter((r) => r.method === "GET" && r.path === "/github/rate-limit").length;
    const panel = () => host!.querySelector("#github-gauges-title")?.closest("section.gauges-panel");
    const githubGauges = () =>
      Array.from(panel()?.querySelectorAll(".gauge") ?? [], (g) => ({
        label: g.querySelector(".gauge__label")?.textContent,
        percent: g.querySelector("[role=meter]")?.getAttribute("aria-valuenow"),
        text: g.querySelector(".gauge__text")?.textContent,
        note: g.querySelector(".gauge__note")?.textContent,
      }));

    it("sit in a widget titled GitHub directly above the System widget", async () => {
      daemon.answers.set("GET /system", { status: 200, body: gauges });
      daemon.answers.set("GET /github/rate-limit", { status: 200, body: limits });
      await showSidebar(false);
      const github = panel();
      const system = host!.querySelector("#gauges-title")?.closest("section.gauges-panel");
      expect(github?.getAttribute("aria-labelledby")).toBe("github-gauges-title");
      expect(host!.querySelector("#github-gauges-title")?.textContent).toBe("GitHub");
      expect(github?.nextElementSibling).toBe(system);
      expect(githubGauges().map((g) => g.label)).toEqual(["REST", "GraphQL"]);
    });

    it("measure each bucket against the limit GitHub reports for it, and tell when it resets", async () => {
      daemon.answers.set("GET /github/rate-limit", { status: 200, body: limits });
      await showSidebar(false);
      expect(getState().github).toEqual(limits);
      expect(githubGauges()).toEqual([
        { label: "REST", percent: "25", text: "500 / 2,000", note: `Resets at ${formatTime(limits.rest!.resetAt, "en-US", false)}` },
        {
          label: "GraphQL",
          percent: "20",
          text: "1,000 / 5,000",
          note: `Resets at ${formatTime(limits.graphql!.resetAt, "en-US", false)}`,
        },
      ]);
    });

    it("fill a bar no further than full when a bucket reports more used than its limit", async () => {
      daemon.answers.set("GET /github/rate-limit", {
        status: 200,
        body: { ...limits, rest: { limit: 2000, used: 2050, resetAt: "2026-11-14T10:00:00Z" } },
      });
      await showSidebar(false);
      expect(githubGauges()[0]).toMatchObject({ percent: "100", text: "2,050 / 2,000" });
    });

    it.each([
      ["gh answers no bucket", { status: 200, body: {} }],
      ["the reading fails", { status: 0, body: null }],
      ["an older daemon answers 404", { status: 404, body: null }],
    ])("show — in both when %s, and the System gauges keep their readings", async (_, answer) => {
      patchStateForTests({ github: limits });
      daemon.answers.set("GET /system", { status: 200, body: gauges });
      daemon.answers.set("GET /github/rate-limit", answer);
      await showSidebar(false);
      expect(getState().github).toEqual(answer.status === 200 ? {} : undefined);
      expect(githubGauges().map((g) => [g.text, g.note, g.percent])).toEqual([
        ["—", undefined, "0"],
        ["—", undefined, "0"],
      ]);
      expect(texts()[0]).toBe("12%");
    });

    it("are read at once and on the System gauges' clock while expanded, and never while collapsed", async () => {
      daemon.answers.set("GET /github/rate-limit", { status: 200, body: limits });
      await showSidebar(true);
      await elapse(15000);
      expect(githubReads()).toBe(0);
      expect(panel()).toBeFalsy();

      await showSidebar(false);
      expect(githubReads()).toBe(1);
      await elapse(5000);
      expect(githubReads()).toBe(2);
      expect(reads()).toBe(2);

      await showSidebar(true);
      await elapse(15000);
      expect(githubReads()).toBe(2);
    });
  });

  describe("widgets hidden by their settings", () => {
    const limits: GitHubRateLimit = { rest: { limit: 2000, used: 500, resetAt: "2026-11-14T10:00:00Z" } };
    const githubReads = () => daemon.requests.filter((r) => r.method === "GET" && r.path === "/github/rate-limit").length;
    const systemPanel = () => host!.querySelector("#gauges-title")?.closest("section.gauges-panel") ?? null;
    const githubPanel = () => host!.querySelector("#github-gauges-title")?.closest("section.gauges-panel") ?? null;

    async function set(patch: Partial<Settings>) {
      await act(async () => patchStateForTests({ settings: { ...settings, ...patch } }));
      await elapse(0);
    }

    beforeEach(() => {
      daemon.answers.set("GET /system", { status: 200, body: gauges });
      daemon.answers.set("GET /github/rate-limit", { status: 200, body: limits });
    });

    it("show no System widget and read no /system while its setting is off, the GitHub widget shown and read", async () => {
      patchStateForTests({ settings: { ...settings, showSystemWidget: false } });
      await showSidebar(false);
      await elapse(15000);
      expect(systemPanel()).toBeNull();
      expect(reads()).toBe(0);
      expect(githubPanel()).not.toBeNull();
      expect(githubReads()).toBe(4);
    });

    it("show no GitHub widget and read no rate limit while its setting is off, the System widget shown and read", async () => {
      patchStateForTests({ settings: { ...settings, showGitHubWidget: false } });
      await showSidebar(false);
      await elapse(15000);
      expect(githubPanel()).toBeNull();
      expect(githubReads()).toBe(0);
      expect(systemPanel()).not.toBeNull();
      expect(reads()).toBe(4);
      expect(texts()[0]).toBe("12%");
    });

    it("leave only the version and the toggle at the sidebar's foot with both off, the navigation in place", async () => {
      await showSidebar(false);
      const nav = host!.querySelector(".nav");
      await set({ showSystemWidget: false, showGitHubWidget: false });
      const foot = host!.querySelector(".sidebar__foot")!;
      expect([...foot.children].map((c) => c.className)).toEqual(["sidebar__bar"]);
      expect(foot.querySelector(".sidebar__version")?.textContent).toBe("v0.1.0");
      expect(host!.querySelector(".nav")).toBe(nav);
      expect(host!.querySelector(".sidebar")?.children[1]).toBe(nav);
      const before = [reads(), githubReads()];
      await elapse(15000);
      expect([reads(), githubReads()]).toEqual(before);
    });

    it("finds a border, padding, or height on the foot and a margin on a widget's panel, naming the selector and the property", () => {
      const sample = [
        ".sidebar__foot { display: flex; gap: 8px; border-top: 1px solid var(--x); }",
        '.app[data-sidebar="collapsed"] .sidebar__foot { padding-block: 4px; }',
        "/* .sidebar__foot { min-height: 9px; } */",
        ".sidebar__foot > .gauges-panel, .other { margin-top: 2px; color: var(--y); }",
        ".gauges-panel__title { margin: 0; padding: 2px; }",
        ".sidebar__foot .gauges { padding: 4px; border: 1px solid; }",
        ".sidebar__foot { border-radius: 4px; }",
        "@media (max-height: 600px) { .sidebar__foot { min-height: 40px; } }",
      ].join("\n");
      expect(footSpace(sample)).toEqual([
        ".sidebar__foot: border-top",
        '.app[data-sidebar="collapsed"] .sidebar__foot: padding-block',
        ".sidebar__foot > .gauges-panel: margin-top",
        "@media (max-height: 600px) .sidebar__foot: min-height",
      ]);
    });

    it("leave no space or border by the stylesheet's rules: the foot spaces its children by its gap alone, and each widget's border and padding go with it", async () => {
      // happy-dom applies no stylesheet and lays nothing out, so the rules are read as app.css writes them.
      const css = readFileSync(join(__dirname, "..", "design", "app.css"), "utf8");
      expect(footSpace(css)).toEqual([]);
      const foot = declared(css, ".sidebar__foot");
      expect(foot.get("display")).toBe("flex");
      expect(foot.get("flex-direction")).toBe("column");
      expect(foot.get("gap")).toBe("var(--hf-space-2)");
      const box = declared(css, ".gauges");
      expect(box.get("border")).toBe("1px solid var(--hf-color-border)");
      expect(box.get("padding")).toBe("var(--hf-space-3)");

      // The bordered box of each widget is inside the widget's own panel, which the foot holds and a hidden widget takes away.
      await showSidebar(false);
      const shown = host!.querySelector(".sidebar__foot")!;
      const boxes = [...shown.querySelectorAll(".gauges")];
      expect(boxes).toHaveLength(2);
      for (const b of boxes) {
        expect(b.parentElement?.matches("section.gauges-panel")).toBe(true);
        expect(b.parentElement?.parentElement).toBe(shown);
      }
    });

    it.each([
      ["System", { showSystemWidget: false }, { showSystemWidget: true }],
      ["GitHub", { showGitHubWidget: false }, { showGitHubWidget: true }],
    ] as const)("show the %s widget again and read it at once when turned back on, with no restart", async (name, off, on) => {
      const [own, other, panel] =
        name === "System" ? [reads, githubReads, systemPanel] : [githubReads, reads, githubPanel];
      patchStateForTests({ settings: { ...settings, ...off } });
      await showSidebar(false);
      await elapse(6000);
      expect(own()).toBe(0);
      expect(panel()).toBeNull();
      expect(other()).toBe(2);

      await set(on);
      expect(panel()).not.toBeNull();
      expect(own()).toBe(1);
      await elapse(5000);
      expect(own()).toBe(2);
      // The other widget's reads kept their own clock: no read of their own came with the change.
      expect(other()).toBe(3);
    });

    it("keep each other's place and reads while one is turned off and on and the other off", async () => {
      await showSidebar(false);
      expect([reads(), githubReads()]).toEqual([1, 1]);

      await set({ showSystemWidget: false });
      expect(systemPanel()).toBeNull();
      expect(githubPanel()?.nextElementSibling?.className).toBe("sidebar__bar");
      await elapse(5000);
      expect([reads(), githubReads()]).toEqual([1, 2]);

      await set({ showGitHubWidget: false });
      expect(githubPanel()).toBeNull();
      expect(systemPanel()?.nextElementSibling?.className).toBe("sidebar__bar");
      expect([reads(), githubReads()]).toEqual([2, 2]);
      await elapse(5000);
      expect([reads(), githubReads()]).toEqual([3, 2]);
    });

    it("read nothing before the settings are known, and start once they say a widget shows", async () => {
      patchStateForTests({ settings: undefined });
      await showSidebar(false);
      await elapse(15000);
      expect([reads(), githubReads()]).toEqual([0, 0]);

      await set({ showGitHubWidget: false });
      expect([reads(), githubReads()]).toEqual([1, 0]);
      expect(githubPanel()).toBeNull();
    });
  });
});

// harmflow-app#1156: the name beside the mark is the wordmark, an image the typeface choice cannot change, while the
// tagline under it stays live text that follows the choice.
describe("the sidebar's brand", () => {
  const appCss = readFileSync(join(__dirname, "..", "design", "app.css"), "utf8");
  const themeCss = readFileSync(join(__dirname, "..", "design", "theme.css"), "utf8");

  afterEach(() => {
    applyTypeface("harmflow");
  });

  /** Renders the sidebar in a root of its own, so that each typeface gets a whole first render, and returns its brand. */
  async function showBrand(collapsed: boolean) {
    act(() => root?.unmount());
    host?.remove();
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(createElement(Sidebar, { screen: "overview", onNavigate: () => {}, collapsed, onToggle: () => {} })));
    return host.querySelector(".brand")!;
  }

  /**
   * What in the brand would set the name in a font, each as its text or its tag: live text outside the tagline, an
   * element that sets text in an SVG, or a font on an element.
   */
  function setInFont(brand: Element): string[] {
    const texts: string[] = [];
    const walker = document.createTreeWalker(brand, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const text = node.textContent?.trim() ?? "";
      if (text && !node.parentElement?.closest(".brand__tagline")) texts.push(text);
    }
    const fonts = [...brand.querySelectorAll("*")]
      .filter((e) => ["text", "tspan", "foreignobject"].includes(e.localName.toLowerCase()) || e.hasAttribute("font-family") || /font/.test(e.getAttribute("style") ?? ""))
      .map((e) => `<${e.localName}>`);
    return [...texts, ...fonts];
  }

  /** A theme.css token's value, as a number, from a declaration that reads it. */
  function tokenValue(value: string | undefined): number {
    const token = /^var\((--hf-[\w-]+)\)$/.exec(value ?? "")?.[1];
    expect(token, value).toBeDefined();
    return parseFloat(new RegExp(`${token}:\\s*([^;]+);`).exec(themeCss)![1]);
  }

  it("finds a name set in a font, and lets the outlined wordmark and the tagline through", () => {
    const brand = document.createElement("div");
    brand.innerHTML = '<svg class="logo" aria-hidden="true"></svg><div class="brand__text"><span class="brand__name">HarmFlow</span><span class="brand__tagline">Agents at work</span></div>';
    expect(setInFont(brand)).toEqual(["HarmFlow"]);
    brand.innerHTML = '<svg role="img" aria-label="HarmFlow"><text font-family="Inter">HarmFlow</text></svg><span class="brand__tagline">Agents at work</span>';
    expect(setInFont(brand)).toEqual(["HarmFlow", "<text>"]);
    brand.innerHTML = '<svg role="img" aria-label="HarmFlow"><path d="M0 0h4v4H0z"></path></svg><span class="brand__tagline">Agents at work</span>';
    expect(setInFont(brand)).toEqual([]);
  });

  it("draws the name beside the mark as the same outlined image under System and HarmFlow, read as the name", async () => {
    const shown: string[] = [];
    for (const typeface of ["system", "harmflow"] as const) {
      applyTypeface(typeface);
      expect(document.documentElement.dataset.typeface).toBe(typeface === "system" ? "system" : undefined);
      const brand = await showBrand(false);
      expect(setInFont(brand), typeface).toEqual([]);

      const named = [...brand.querySelectorAll("[aria-label]")];
      expect(named.map((e) => [e.localName, e.getAttribute("role"), e.getAttribute("aria-label")]), typeface).toEqual([["svg", "img", "HarmFlow"]]);
      const name = named[0];
      expect([...new Set([...name.querySelectorAll("*")].map((e) => e.localName.toLowerCase()))].sort()).toEqual(["defs", "lineargradient", "path", "stop"]);
      expect(brand.querySelector(".brand__tagline")?.textContent).toBe(i18n.t("app.tagline"));

      // The gradient's id is made per copy; the rest of the image is compared as drawn.
      const id = name.querySelector("[id]")!.id;
      shown.push(name.outerHTML.split(id).join("ID"));
    }
    expect(shown[0]).toBe(shown[1]);
  });

  it("keeps the brand row no taller than the mark, and shows the mark alone collapsed", async () => {
    const brand = await showBrand(false);
    const mark = Number(brand.querySelector("svg.logo")?.getAttribute("height"));
    const wordmark = Number(brand.querySelector("svg.wordmark")?.getAttribute("height"));
    expect(mark).toBe(38);
    // The tagline's line box: its font size times the line height it inherits from the body.
    const tagline = declared(appCss, ".brand__tagline");
    expect(tagline.has("line-height")).toBe(false);
    const taglineLine = tokenValue(tagline.get("font-size")) * tokenValue(declared(appCss, "body").get("line-height"));
    expect(wordmark + tokenValue(declared(appCss, ".brand__text").get("gap")) + taglineLine).toBeLessThanOrEqual(mark);

    const collapsed = await showBrand(true);
    expect([...collapsed.children].map((c) => c.getAttribute("class"))).toEqual(["logo"]);
  });

  it("leaves the tagline in the body's font stack, which the typeface choice switches", () => {
    const fonts = stylesheetRules(appCss).flatMap(({ selectors, declarations }) =>
      selectors.filter((s) => selects(s, "brand__tagline")).flatMap((s) => declarations.filter(([p]) => /^font(?:-family|-variation-settings)?$/.test(p)).map(([p]) => `${s}: ${p}`)),
    );
    expect(fonts).toEqual([]);
    expect(declared(appCss, "body").get("font-family")).toBe("var(--hf-font-sans)");
    expect(themeCss).toMatch(/:root\[data-typeface="system"\]\s*{[^}]*--hf-font-sans:/);
  });
});

type CssRule = { selectors: string[]; within: string | null; declarations: [string, string][] };

/** The rules of a stylesheet, its comments stripped: each one's selectors, its declarations, and the at-rule it sits in. */
function stylesheetRules(css: string): CssRule[] {
  const rules: CssRule[] = [];
  const open: string[] = [];
  for (const [, text, brace] of css.replace(/\/\*[\s\S]*?\*\//g, "").matchAll(/([^{}]*)([{}])/g)) {
    if (brace === "{") {
      open.push(text.trim());
      continue;
    }
    const head = open.pop();
    if (head === undefined || head.startsWith("@")) continue;
    const declarations = text.split(";").flatMap((d): [string, string][] => {
      const colon = d.indexOf(":");
      return colon > 0 ? [[d.slice(0, colon).trim(), d.slice(colon + 1).trim()]] : [];
    });
    rules.push({ selectors: head.split(",").map((s) => s.trim()), within: open.at(-1) ?? null, declarations });
  }
  return rules;
}

/** The declarations of the top-level rules written for exactly this selector, the later rule winning. */
function declared(css: string, selector: string): Map<string, string> {
  const rules = stylesheetRules(css).filter((r) => r.within === null && r.selectors.includes(selector));
  return new Map(rules.flatMap((r) => r.declarations));
}

/** Whether a selector styles an element of this class: the class is in its last compound, the one it selects. */
function selects(selector: string, className: string): boolean {
  const subject = selector.split(/\s*[>+~]\s*|\s+/).at(-1) ?? "";
  return new RegExp(`\\.${className}(?![\\w-])`).test(subject);
}

/** A property that gives the foot a border, padding, or height of its own. */
const footOwn = (property: string) =>
  (/^(?:border|padding)(?:-|$)/.test(property) && !/radius|collapse|spacing/.test(property)) ||
  /^(?:min-)?(?:height|block-size)$/.test(property);

/**
 * What in a stylesheet would leave a bordered or empty space at the sidebar's foot with its widgets hidden, each as
 * "<selector>: <property>": a border, padding, or height on the foot, or a margin on a widget's panel.
 */
function footSpace(css: string): string[] {
  return stylesheetRules(css).flatMap(({ selectors, within, declarations }) =>
    selectors.flatMap((selector) => {
      const offends = selects(selector, "sidebar__foot")
        ? footOwn
        : selects(selector, "gauges-panel")
          ? (property: string) => /^margin(?:-|$)/.test(property)
          : null;
      if (!offends) return [];
      const where = within ? `${within} ${selector}` : selector;
      return declarations.filter(([property]) => offends(property)).map(([property]) => `${where}: ${property}`);
    }),
  );
}
