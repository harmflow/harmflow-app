// Follows the daemon's event stream as the dashboard does, against a stand-in for the daemon's local API: when a
// harness's check ends, the inventory is read again, once for a burst of checks, and never detected again, which
// would announce another check. And the execution lists are shown as their own answers arrive, whatever /stats takes.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StreamHandlers } from "../api/transport";
import type { ExecutionSummary, HarmflowEvent, Harness, Queue } from "../api/types";
import { actions, getState, startSync } from "./store";

// The daemon as the client sees it: an answer per route, or per path with its query; the routes that never answer;
// every request it received; and the stream it feeds.
const daemon = vi.hoisted(() => ({
  answers: new Map<string, unknown>(),
  silent: new Set<string>(),
  requests: [] as string[],
  stream: undefined as StreamHandlers | undefined,
}));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string) => {
      daemon.requests.push(`${method} ${path}`);
      const route = path.split("?")[0];
      if (daemon.silent.has(route)) return new Promise(() => {});
      const key = daemon.answers.has(path) ? path : route;
      return daemon.answers.has(key) ? { status: 200, body: daemon.answers.get(key) } : { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: (handlers: StreamHandlers) => {
      daemon.stream = handlers;
      return () => {};
    },
  },
}));

const codex: Harness = {
  id: "codex",
  name: "Codex",
  detected: true,
  path: "/opt/homebrew/bin/codex",
  installations: ["/opt/homebrew/bin/codex"],
  version: "0.163.0",
  authentication: "valid",
  readiness: "ready",
  models: [],
  efforts: [],
  lastCheckedAt: "2026-10-21T09:00:06Z",
};

const checked = (id: number): HarmflowEvent => ({
  id,
  schemaVersion: "1",
  timestamp: "2026-10-21T09:00:06Z",
  type: "harness.checked",
  severity: "info",
  harness: "codex",
  payload: { readiness: "ready" },
});

describe("event stream", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("reads the inventory again when a harness's check ends, without detecting again", async () => {
    startSync();
    await vi.advanceTimersByTimeAsync(0);
    daemon.requests.length = 0;
    daemon.answers.set("/harnesses", [codex]);

    daemon.stream!.onEvent(checked(1));
    daemon.stream!.onEvent(checked(2));
    await vi.advanceTimersByTimeAsync(300);

    expect(daemon.requests.filter((r) => r.includes("/harnesses"))).toEqual(["GET /harnesses"]);
    expect(getState().harnesses).toEqual([codex]);
  });
});

describe("execution lists", () => {
  afterEach(() => {
    daemon.answers.clear();
    daemon.silent.clear();
  });

  it("updates the active executions, the recent ones, and the queue while /stats never answers", async () => {
    const running = { id: "exec-running", status: "running" } as unknown as ExecutionSummary;
    const done = { id: "exec-done", status: "completed" } as unknown as ExecutionSummary;
    const queue = { running: 1, pending: [] } as unknown as Queue;
    daemon.silent.add("/stats");
    daemon.answers.set("/executions?active=true&limit=50", [running]);
    daemon.answers.set("/executions", [done]);
    daemon.answers.set("/queue", queue);

    void actions.refreshExecutions();
    await vi.waitFor(() => expect(getState().queue).toEqual(queue));

    expect(getState().active).toEqual([running]);
    expect(getState().recent).toEqual([done]);
    expect(getState().stats).toBeUndefined();
  });
});
