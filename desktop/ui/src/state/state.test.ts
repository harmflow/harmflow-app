// The dashboard's store and timeline against a real harmflowd, with the stand-in Claude Code CLI of the integration
// suite and nothing standing in for the local API or its event stream. The store reaches the daemon through its own
// HttpTransport and the dev server's daemon proxy of vite.config.ts, both unchanged: the test serves the dev server on a
// free port and gives the transport what a browser on that origin has, relative URLs and an EventSource. Between the
// transport and the dev server stands a TCP proxy the test controls, which cuts the event stream and the requests alike
// while harmflowd and its executions go on running.
//
// `make test-desktop-daemon` builds harmflowd and names it in HARMFLOW_TEST_DAEMON. The test stays out of `pnpm test`
// and its coverage, whose floors must not need a daemon (vite.config.ts, DAEMON_TESTS).
import { spawn, type ChildProcess } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { connect, createServer as listen, type Server, type Socket } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer, type ViteDevServer } from "vite";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import type { HarmflowEvent } from "../api/types";
import { buildTimeline } from "../lib/timeline";
import { actions, getState, startSync } from "./store";

const ui = fileURLToPath(new URL("../..", import.meta.url));
const repoRoot = join(ui, "..", "..");
const fakes = join(repoRoot, "tests", "integration", "testdata");

// As the integration suite waits for an execution: a wait fails once silence passes with no new event, and once the
// ceiling passes in all, so a loaded host only stretches it.
const SILENCE_MS = 20_000;
const CEILING_MS = 180_000;
const RUNTIME_WAIT_MS = 20_000;
// How long the transport may take to report a loss, and to connect again once the connection returns: EventSource
// retries every few seconds, and the transport reopens a closed stream after 3.
const CONNECTION_WAIT_MS = 30_000;

const FAILURE_EVENTS = ["execution.failed", "execution.interrupted", "execution.cancelled"];

interface RealDaemon {
  home: string;
  /** The daemon's own API, for the test's set-up and its independent reads; the store never uses it. */
  base: string;
  token: string;
  process: ChildProcess;
  output: string[];
}

/** A TCP proxy in front of the dev server, on one port for the whole file, whose connection the test cuts. */
interface CuttableProxy {
  origin: string;
  /** Drops every open connection and refuses each new one, until restore. */
  cut(): void;
  restore(): void;
  close(): Promise<void>;
}

/** Node's own fetch, before the test gives the transport one that resolves relative URLs. */
const fetchNative = globalThis.fetch;

let daemon: RealDaemon;
let server: ViteDevServer;
let proxy: CuttableProxy;
const streams: EventSource[] = [];
/** The paths of the requests the dashboard sent through its transport, with their bodies once read. */
const requests: Array<{ path: string; body: Promise<unknown> }> = [];

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = listen();
    probe.once("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const address = probe.address();
      probe.close(() => (typeof address === "object" && address ? resolve(address.port) : reject(new Error("no port"))));
    });
  });
}

async function until<T>(what: string, read: () => T | undefined, timeoutMs: number): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const value = read();
    if (value !== undefined) return value;
    if (Date.now() > deadline) throw new Error(`${what}: not within ${timeoutMs} ms`);
    await new Promise((r) => setTimeout(r, 20));
  }
}

/** Starts harmflowd as the integration suite does: a temporary home, the file token store, the stand-in harnesses. */
async function startDaemon(): Promise<RealDaemon> {
  const binary = process.env.HARMFLOW_TEST_DAEMON;
  if (!binary) throw new Error("HARMFLOW_TEST_DAEMON names no harmflowd: run `make test-desktop-daemon`");
  const home = mkdtempSync(join(tmpdir(), "harmflow-ui-"));
  // Nothing of a HarmFlow session this test may run in reaches the daemon, which would record into it.
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith("HARMFLOW_")));
  Object.assign(env, { HARMFLOW_TOKEN_STORE: "file", CODEX_HOME: join(home, "codex") });
  const child = spawn(binary, ["--home", home, "--builtins", repoRoot], { env, stdio: ["ignore", "pipe", "pipe"] });
  const output: string[] = [];
  child.stdout?.on("data", (d) => output.push(String(d)));
  child.stderr?.on("data", (d) => output.push(String(d)));
  const runtime = await until(
    "the daemon writes its address",
    () => {
      if (child.exitCode !== null) throw new Error(`the daemon exited ${child.exitCode}:\n${output.join("")}`);
      try {
        const rt = JSON.parse(readFileSync(join(home, "run", "daemon.json"), "utf8")) as { address?: string; tokenStore?: string };
        return rt.address ? rt : undefined;
      } catch {
        return undefined; // not written yet
      }
    },
    RUNTIME_WAIT_MS,
  );
  expect(runtime.tokenStore).toBe("file");
  const token = readFileSync(join(home, "run", "token"), "utf8").trim();
  const d: RealDaemon = { home, base: `http://${runtime.address}/api/v1`, token, process: child, output };
  await call(d, "PUT", "/harnesses/claude-code", { path: join(fakes, "fake-claude") });
  await call(d, "PUT", "/harnesses/codex", { path: join(fakes, "fake-codex") });
  return d;
}

async function stopDaemon(d: RealDaemon) {
  if (d.process.exitCode !== null || d.process.signalCode !== null) return;
  const exited = new Promise<void>((r) => d.process.once("exit", () => r()));
  d.process.kill("SIGTERM");
  const killed = setTimeout(() => d.process.kill("SIGKILL"), 10_000);
  await exited;
  clearTimeout(killed);
}

async function call(d: RealDaemon, method: string, path: string, body?: unknown): Promise<unknown> {
  const res = await fetchNative(`${d.base}${path}`, {
    method,
    headers: { Authorization: `Bearer ${d.token}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (res.status >= 400) throw new Error(`${method} ${path}: ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

/** What the daemon's stored log holds for the execution, read past the dashboard. */
async function storedEvents(id: string): Promise<HarmflowEvent[]> {
  const res = (await call(daemon, "GET", `/executions/${id}/events?limit=5000`)) as { events: HarmflowEvent[] };
  return res.events;
}

async function startProxy(port: number): Promise<CuttableProxy> {
  let down = false;
  const open = new Set<Socket>();
  const tcp: Server = listen((client) => {
    if (down) {
      client.destroy();
      return;
    }
    const upstream = connect(port, "127.0.0.1");
    for (const socket of [client, upstream]) {
      open.add(socket);
      socket.once("close", () => open.delete(socket));
      socket.on("error", () => {
        client.destroy();
        upstream.destroy();
      });
    }
    client.pipe(upstream).pipe(client);
  });
  await new Promise<void>((resolve, reject) => {
    tcp.once("error", reject);
    tcp.listen(0, "127.0.0.1", () => resolve());
  });
  const address = tcp.address();
  if (typeof address !== "object" || !address) throw new Error("the proxy has no port");
  const dropAll = () => {
    for (const socket of open) socket.destroy();
  };
  return {
    origin: `http://127.0.0.1:${address.port}`,
    cut() {
      down = true;
      dropAll();
    },
    restore() {
      down = false;
    },
    close: () =>
      new Promise<void>((resolve) => {
        dropAll();
        tcp.close(() => resolve());
      }),
  };
}

const idsAndTypes = (events: readonly HarmflowEvent[] | undefined) => (events ?? []).map((e) => `${e.id} ${e.type}`);

function eventsReadsOf(id: string) {
  return requests.filter((r) => r.path.startsWith(`/api/v1/executions/${encodeURIComponent(id)}/events`));
}

beforeAll(async () => {
  daemon = await startDaemon();
  // The proxy reads the daemon's runtime file and token from HARMFLOW_HOME, on each request: never the user's.
  process.env.HARMFLOW_HOME = daemon.home;
  const port = await freePort();
  server = await createServer({
    configFile: join(ui, "vite.config.ts"),
    root: ui,
    logLevel: "warn",
    server: { port, strictPort: true, host: "127.0.0.1", hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true },
  });
  await server.listen();
  // The dashboard's origin is the cuttable proxy's: everything it sends, and its event stream, passes through it.
  proxy = await startProxy(port);
  const origin = proxy.origin;

  if (typeof EventSource !== "function") {
    throw new Error("this Node has no EventSource; vitest.daemon.config.ts passes --experimental-eventsource where it needs one");
  }
  const NodeEventSource = EventSource;
  // A browser on the dev server's origin resolves the transport's relative URLs against it.
  vi.stubGlobal(
    "EventSource",
    class extends NodeEventSource {
      constructor(url: string | URL, init?: EventSourceInit) {
        super(new URL(url, origin), init);
        streams.push(this);
      }
    },
  );
  vi.stubGlobal("fetch", (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(typeof input === "string" || input instanceof URL ? input : input.url, origin);
    const res = fetchNative(url, init);
    requests.push({ path: url.pathname + url.search, body: res.then((r) => r.clone().json()).catch(() => null) });
    return res;
  });
}, 120_000);

afterAll(async () => {
  for (const s of streams) s.close();
  await proxy?.close();
  await server?.close();
  if (daemon) {
    await stopDaemon(daemon);
    rmSync(daemon.home, { recursive: true, force: true });
  }
  vi.unstubAllGlobals();
});

describe("a selected execution against the real daemon", () => {
  it("receives its events live, holds them all, and replays the same timeline", async () => {
    // With no notice, the stored log ends at execution.completed, so the store's events can equal it whole.
    await call(daemon, "POST", "/tasks", {
      yaml: [
        "apiVersion: harmflow/v1alpha1",
        "kind: Task",
        "metadata:",
        "  id: ticking",
        "  name: Task ticking",
        "spec:",
        "  type: general",
        '  prompt: "SCENARIO=ticks"',
        "  harness:",
        "    preferred:",
        "      provider: claude-code",
        "  notifications:",
        "    native:",
        "      events: []",
        "",
      ].join("\n"),
    });

    startSync();
    await until("the store's event stream connects", () => (getState().connection === "connected" ? true : undefined), 20_000);

    const { executionId: id } = (await call(daemon, "POST", "/tasks/ticking/run")) as { executionId: string };
    // Selected on the Executions screen, the detail opens as ExecutionDetail's effect does.
    await Promise.all([actions.loadExecution(id), actions.loadEvents(id)]);
    const reads = eventsReadsOf(id);
    expect(reads).toHaveLength(1);
    const read = (await reads[0].body) as { events: HarmflowEvent[] } | null;
    expect(read, "the stored events the detail read").not.toBeNull();
    // The detail reads only what follows the events the store already holds from the stream: both count as read.
    const heldAtRead = getState().events[id] ?? [];
    const readUpTo = Math.max(0, ...read!.events.map((e) => e.id), ...heldAtRead.map((e) => e.id));

    // Live: a message emitted after the stored events were read reaches the store before the execution ends, with no
    // further read of the stored events, and the timeline shows it.
    const live = await until(
      "a message of the execution after the read",
      () => {
        const held = getState().events[id] ?? [];
        if (held.some((e) => e.type === "execution.completed")) {
          throw new Error(`the execution ended before a message arrived after event ${readUpTo}: ${idsAndTypes(held).join(", ")}`);
        }
        return held.find((e) => e.type === "harness.message" && e.id > readUpTo);
      },
      SILENCE_MS,
    );
    expect(eventsReadsOf(id), "reads of the stored events when the live message arrived").toHaveLength(1);
    expect(buildTimeline(getState().events[id] ?? []).map((item) => item.id)).toContain(live.id);

    // Complete: once it ends, the store holds the daemon's events of it, by id, type, and order, each once.
    let lastCount = 0;
    let lastChange = Date.now();
    await until(
      "the execution's end in the store",
      () => {
        const held = getState().events[id] ?? [];
        if (held.length !== lastCount) [lastCount, lastChange] = [held.length, Date.now()];
        if (Date.now() - lastChange > SILENCE_MS) throw new Error(`no event in ${SILENCE_MS} ms: ${idsAndTypes(held).join(", ")}`);
        return held.some((e) => e.type === "execution.completed") ? true : undefined;
      },
      CEILING_MS,
    );
    const stored = await storedEvents(id);
    expect(stored[0]?.type).toBe("execution.created");
    expect(stored[stored.length - 1]?.type).toBe("execution.completed");
    const held = getState().events[id] ?? [];
    expect(idsAndTypes(held)).toEqual(idsAndTypes(stored));
    expect(new Set(held.map((e) => e.id)).size).toBe(held.length);

    // Replay: a fresh store, holding no events, opens the ended execution again and reads the daemon's stored log.
    vi.resetModules();
    const fresh = await import("./store");
    expect(fresh.getState().events).toEqual({});
    await Promise.all([fresh.actions.loadExecution(id), fresh.actions.loadEvents(id)]);
    const replayed = fresh.getState().events[id] ?? [];
    expect(fresh.getState().logExpired[id]).toBe(false);
    expect(idsAndTypes(replayed)).toEqual(idsAndTypes(held));
    expect(buildTimeline(replayed)).toEqual(buildTimeline(held));
  });
});

describe("a dashboard reconnecting to the real daemon", () => {
  it("receives every event it missed, once each, then the live stream", async () => {
    // The stand-in emits until the test releases it, so the drop, the reconnection, and a live event after it all fall
    // while the execution runs, however slow the host. With no notice, the stored log ends at execution.completed.
    await call(daemon, "POST", "/tasks", {
      yaml: [
        "apiVersion: harmflow/v1alpha1",
        "kind: Task",
        "metadata:",
        "  id: reconnecting",
        "  name: Task reconnecting",
        "spec:",
        "  type: general",
        '  prompt: "SCENARIO=gated"',
        "  harness:",
        "    preferred:",
        "      provider: claude-code",
        "  notifications:",
        "    native:",
        "      events: []",
        "",
      ].join("\n"),
    });

    startSync();
    await until("the store's event stream connects", () => (getState().connection === "connected" ? true : undefined), CONNECTION_WAIT_MS);

    const { executionId: id } = (await call(daemon, "POST", "/tasks/reconnecting/run")) as { executionId: string };
    const release = () => writeFileSync(join(daemon.home, "runs", id, "release"), "");

    // Before the drop: the execution's first messages reach the store from the stream; nothing read its stored events.
    await until(
      "two messages of the execution in the store",
      () => ((getState().events[id] ?? []).filter((e) => e.type === "harness.message").length >= 2 ? true : undefined),
      SILENCE_MS,
    );
    expect(eventsReadsOf(id), "reads of the stored events before the drop").toHaveLength(0);

    // The connection drops, not the daemon: the store reads it lost, and the last event it received is noted.
    proxy.cut();
    await until("the store reads the connection lost", () => (getState().connection !== "connected" ? true : undefined), CONNECTION_WAIT_MS);
    const noted = Math.max(...(getState().events[id] ?? []).map((e) => e.id));

    // It stays down until the daemon's own API shows a message the store missed, the execution still running.
    let missed: HarmflowEvent[] = [];
    for (const deadline = Date.now() + SILENCE_MS; !missed.some((e) => e.type === "harness.message"); ) {
      if (Date.now() > deadline) throw new Error(`no message of the execution after event ${noted} within ${SILENCE_MS} ms`);
      await new Promise((r) => setTimeout(r, 100));
      missed = (await storedEvents(id)).filter((e) => e.id > noted);
    }
    expect(missed.map((e) => e.type), "the execution ended while the dashboard was away").not.toContain("execution.completed");
    expect(getState().connection, "the store's connection while it is down").not.toBe("connected");
    expect(idsAndTypes((getState().events[id] ?? []).filter((e) => e.id > noted)), "events the store got while down").toEqual([]);

    // The connection returns while the execution still runs. With no other action of the test, the store connects
    // again and comes to hold every event the daemon stored after the noted one, each once.
    proxy.restore();
    await until("the store connects again", () => (getState().connection === "connected" ? true : undefined), CONNECTION_WAIT_MS);
    const reconnected = await storedEvents(id);
    expect(reconnected.map((e) => e.type), "the execution ended before the reconnection").not.toContain("execution.completed");
    const missedUpToReconnection = reconnected.filter((e) => e.id > noted);
    await until(
      "every missed event in the store",
      () => {
        const held = new Set((getState().events[id] ?? []).map((e) => e.id));
        return missedUpToReconnection.every((e) => held.has(e.id)) ? true : undefined;
      },
      SILENCE_MS,
    );
    const heldAfter = getState().events[id] ?? [];
    expect(new Set(heldAfter.map((e) => e.id)).size, "events the store holds twice").toBe(heldAfter.length);
    const missedIds = new Set(missedUpToReconnection.map((e) => e.id));
    expect(idsAndTypes(heldAfter.filter((e) => missedIds.has(e.id)))).toEqual(idsAndTypes(missedUpToReconnection));

    // Then the live stream: a message emitted after the reconnection, and after whatever the reconnection read of the
    // stored events, reaches the store with no further read.
    const reads = eventsReadsOf(id);
    const bodies = (await Promise.all(reads.map((r) => r.body))) as Array<{ events?: HarmflowEvent[] } | null>;
    const upTo = Math.max(
      ...reconnected.map((e) => e.id),
      ...bodies.flatMap((b) => (b?.events ?? []).map((e) => e.id)),
      ...(getState().events[id] ?? []).map((e) => e.id),
    );
    await until(
      "a message of the execution after the reconnection",
      () => (getState().events[id] ?? []).find((e) => e.type === "harness.message" && e.id > upTo),
      SILENCE_MS,
    );
    expect(eventsReadsOf(id), "reads of the stored events when the live message arrived").toHaveLength(reads.length);

    // Complete: once released and ended, the store holds the daemon's events of it, by id, type, and order, each once.
    release();
    let lastCount = 0;
    let lastChange = Date.now();
    await until(
      "the execution's end in the store",
      () => {
        const held = getState().events[id] ?? [];
        if (held.length !== lastCount) [lastCount, lastChange] = [held.length, Date.now()];
        if (Date.now() - lastChange > SILENCE_MS) throw new Error(`no event in ${SILENCE_MS} ms: ${idsAndTypes(held).join(", ")}`);
        return held.some((e) => e.type === "execution.completed") ? true : undefined;
      },
      CEILING_MS,
    );
    const stored = await storedEvents(id);
    expect(stored[0]?.type).toBe("execution.created");
    expect(stored[stored.length - 1]?.type).toBe("execution.completed");
    const held = getState().events[id] ?? [];
    expect(idsAndTypes(held)).toEqual(idsAndTypes(stored));
    expect(new Set(held.map((e) => e.id)).size).toBe(held.length);
    const timeline = buildTimeline(held).map((item) => item.id);
    expect(new Set(timeline).size).toBe(timeline.length);
    expect(timeline).toEqual(buildTimeline(stored).map((item) => item.id));

    // Nothing failed.
    expect(stored.filter((e) => FAILURE_EVENTS.includes(e.type)).map((e) => e.type)).toEqual([]);
  });
});
