// Follows the daemon's event stream as the dashboard does, against a stand-in for the daemon's local API: when a
// harness's check ends, the inventory is read again, once for a burst of checks, and never detected again, which
// would announce another check. And the execution lists are shown as their own answers arrive, whatever /stats takes,
// and their refresh never reads the sidebar's gauges.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StreamHandlers } from "../api/transport";
import type { ExecutionSummary, HarmflowEvent, Harness, Queue } from "../api/types";
import { actions, getState, patchStateForTests, startSync } from "./store";

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

// harmflow-app#669: the daemon answers Detect again at once and announces each harness's result, so the detection reads
// as running until each harness has announced its check, and a request that fails says so instead of being swallowed.
describe("Detect again", () => {
  const claude: Harness = { ...codex, id: "claude-code", name: "Claude Code" };
  const checkedOf = (id: number, harness: string): HarmflowEvent => ({ ...checked(id), harness });

  beforeEach(() => {
    startSync();
    patchStateForTests({ harnesses: [claude, codex], redetecting: null });
  });
  afterEach(() => {
    daemon.answers.clear();
    daemon.silent.clear();
    patchStateForTests({ redetecting: null });
  });

  it("runs from the request until every harness has announced its check", async () => {
    daemon.answers.set("/harnesses/refresh", [claude, codex]);
    daemon.requests.length = 0;

    await actions.redetectHarnesses();

    expect(daemon.requests).toContain("POST /harnesses/refresh");
    expect(getState().redetecting).toEqual(["claude-code", "codex"]);
    daemon.stream!.onEvent(checkedOf(10, "claude-code"));
    expect(getState().redetecting).toEqual(["codex"]);
    daemon.stream!.onEvent(checkedOf(11, "codex"));
    expect(getState().redetecting).toBeNull();
  });

  it("asks once while a detection it asked for still runs", async () => {
    daemon.answers.set("/harnesses/refresh", [claude, codex]);
    await actions.redetectHarnesses();
    daemon.requests.length = 0;

    await actions.redetectHarnesses();

    expect(daemon.requests).not.toContain("POST /harnesses/refresh");
  });

  it("ends when the connection to the daemon is lost", async () => {
    daemon.answers.set("/harnesses/refresh", [claude, codex]);
    await actions.redetectHarnesses();

    daemon.stream!.onConnection("disconnected");

    expect(getState().redetecting).toBeNull();
  });

  it("ends and throws when the request fails", async () => {
    await expect(actions.redetectHarnesses()).rejects.toThrow();

    expect(getState().redetecting).toBeNull();
  });
});

// harmflow-app#929: the stream brings every execution's live events, never the history before the dashboard opened, so
// a detail opened after one of them reached the store reads the execution's stored history from its start, once.
describe("a detail opened after a live event of its execution", () => {
  const id = "exec-late";
  const route = `/executions/${id}/events`;
  const event = (n: number, type: string): HarmflowEvent => ({
    id: n,
    schemaVersion: "1",
    timestamp: "2026-11-07T22:00:00Z",
    type,
    severity: "info",
    executionId: id,
    payload: {},
  });
  const history = [
    event(1, "execution.created"),
    event(2, "execution.started"),
    event(3, "harness.started"),
    event(4, "harness.message"),
    event(5, "harness.message"),
  ];
  const idsAndTypes = (events: readonly HarmflowEvent[] | undefined) => (events ?? []).map((e) => `${e.id} ${e.type}`);
  const eventsReads = () => daemon.requests.filter((r) => r.startsWith(`GET ${route}`));

  afterEach(() => daemon.answers.clear());

  it("holds the execution's whole history, then the live event once and in order", async () => {
    startSync();
    daemon.requests.length = 0;
    // Only a read from the start gets the history; a read after the live event gets what follows it, nothing yet.
    daemon.answers.set(`${route}?limit=5000`, { events: history, logExpired: false });
    daemon.answers.set(route, { events: [], logExpired: false });

    daemon.stream!.onEvent(event(5, "harness.message"));
    await actions.loadEvents(id);

    expect(eventsReads()).toEqual([`GET ${route}?limit=5000`]);
    expect(idsAndTypes(getState().events[id])).toEqual(idsAndTypes(history));
  });

  it("reads after the last event held once the history was read", async () => {
    startSync();
    daemon.stream!.onEvent(event(5, "harness.message"));
    daemon.answers.set(route, { events: history, logExpired: false });
    await actions.loadEvents(id);
    daemon.requests.length = 0;
    daemon.answers.set(route, { events: [], logExpired: false });

    daemon.stream!.onEvent(event(6, "harness.message"));
    await actions.loadEvents(id);

    expect(eventsReads()).toEqual([`GET ${route}?after=6&limit=5000`]);
    expect(idsAndTypes(getState().events[id])).toEqual(idsAndTypes([...history, event(6, "harness.message")]));
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

  it("reads the counters from /stats and never the gauges, which /system alone answers", async () => {
    const stats = { running: 1, queued: 0, completedToday: 0, failedToday: 0, activity: { running: [], queued: [], completed: [], failed: [] } };
    daemon.answers.set("/stats", stats);
    daemon.requests.length = 0;

    await actions.refreshExecutions();
    await actions.refreshStats();

    expect(getState().stats).toEqual(stats);
    expect(daemon.requests).toContain("GET /stats");
    expect(daemon.requests.filter((r) => r.startsWith("GET /system"))).toEqual([]);
  });
});
