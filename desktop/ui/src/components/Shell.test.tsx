// @vitest-environment happy-dom
// The topbar's banner for a window and a daemon built from different commits, and its restart of the daemon on the
// window's build, driven in a DOM against a stand-in for the daemon's local API.
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { native, type DaemonStatus } from "../api/native";
import type { Health } from "../api/types";
import i18n from "../i18n";
import { getState, patchStateForTests } from "../state/store";
import { Header, commitsDiffer } from "./Shell";
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
