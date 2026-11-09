// A dropped connection between the dashboard and a real harmflowd, with the stand-in Claude Code CLI of the integration
// suite: the transport reports the loss, the store and the topbar show it as a connection loss and never as a failed
// execution, and once the connection returns they show the execution as the daemon has it. The store reaches the daemon
// through its own HttpTransport and the dev server's daemon proxy of vite.config.ts, both unchanged, as state.test.ts
// does; between them stands a TCP proxy the test controls, which cuts the event stream and the requests alike while
// harmflowd and the execution go on running.
//
// `make test-desktop-daemon` builds harmflowd and names it in HARMFLOW_TEST_DAEMON. The test stays out of `pnpm test`
// and its coverage, whose floors must not need a daemon (vite.config.ts, DAEMON_TESTS).
import { spawn, type ChildProcess } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { connect, createServer as listen, type Server, type Socket } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createElement } from "react";
import { renderToString } from "react-dom/server";
import { createServer, type ViteDevServer } from "vite";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { Header } from "../components/Shell";
import i18n from "../i18n";
import { StreamSource } from "../state/realDaemon";
import { actions, getState, startSync } from "../state/store";
import { ACTIVE_STATUSES, type Execution, type ExecutionStatus, type HarmflowEvent } from "./types";

const ui = fileURLToPath(new URL("../..", import.meta.url));
const repoRoot = join(ui, "..", "..");
const fakes = join(repoRoot, "tests", "integration", "testdata");

const RUNTIME_WAIT_MS = 20_000;
// How long the transport may take to report the loss, and to connect again once the connection returns: EventSource
// retries every few seconds, and the transport reopens a closed stream after 3.
const CONNECTION_WAIT_MS = 30_000;
// As the integration suite waits for an execution, with room for the store's own refreshes after the connection returns.
const CEILING_MS = 180_000;

/** The statuses a lost connection must never make the dashboard show. */
const FAILURES: readonly ExecutionStatus[] = ["failed", "interrupted", "cancelled"];
const FAILURE_EVENTS = ["execution.failed", "execution.interrupted", "execution.cancelled"];

interface RealDaemon {
  home: string;
  /** The daemon's own API, for the test's set-up and its independent reads; the store never uses it. */
  base: string;
  token: string;
  process: ChildProcess;
  output: string[];
}

/** A TCP proxy in front of the dev server, on one port for the whole test, whose connection the test cuts. */
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
const streams: StreamSource[] = [];

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

/** The topbar as the store makes it now. */
function topbar(): string {
  return renderToString(
    createElement(Header, { screen: "executions", onNewTask: () => {}, onNavigate: () => {}, onOpenExecution: () => {} }),
  );
}

const showsLoss = (html: string) => /conn-pill--(reconnecting|disconnected)/.test(html) || html.includes(i18n.t("connection.lostHint"));

/** Where the store reads the execution's status: its lists and its record. */
function statusesHeld(id: string) {
  const s = getState();
  return {
    active: s.active.find((e) => e.id === id)?.status,
    recent: s.recent.find((e) => e.id === id)?.status,
    record: s.details[id]?.status,
  };
}

beforeAll(async () => {
  await i18n.changeLanguage("en-US");
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

  // A browser on the dev server's origin resolves the transport's relative URLs against it. The stream is the daemon
  // tests' own, not Node's EventSource, which on some Nodes stops for good after a refused retry (realDaemon.ts).
  vi.stubGlobal(
    "EventSource",
    class extends StreamSource {
      constructor(url: string | URL) {
        super(new URL(url, origin).href);
        streams.push(this);
      }
    },
  );
  vi.stubGlobal("fetch", (input: string | URL | Request, init?: RequestInit) =>
    fetchNative(new URL(typeof input === "string" || input instanceof URL ? input : input.url, origin), init),
  );
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

describe("a dropped connection to the real daemon", () => {
  it("reads as a connection loss, never a failed execution, then as the execution's current state", async () => {
    await call(daemon, "POST", "/tasks", {
      yaml: [
        "apiVersion: harmflow/v1alpha1",
        "kind: Task",
        "metadata:",
        "  id: dropping",
        "  name: Task dropping",
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
    await until("the store's event stream connects", () => (getState().connection === "connected" ? true : undefined), CONNECTION_WAIT_MS);
    expect(showsLoss(topbar())).toBe(false);

    // Opened on the Executions screen, so the store holds its record, which a reconnection reads again.
    const { executionId: id } = (await call(daemon, "POST", "/tasks/dropping/run")) as { executionId: string };
    await Promise.all([actions.loadExecution(id), actions.loadEvents(id)]);

    // Running: its session's first message reached the store live, and the store's list and record read it active.
    await until(
      "a message of the running execution in the store",
      () => {
        const held = getState().events[id] ?? [];
        if (held.some((e) => e.type === "execution.completed")) throw new Error("the execution ended before a message arrived");
        return held.find((e) => e.type === "harness.message");
      },
      CEILING_MS,
    );
    await Promise.all([actions.loadExecution(id), actions.refreshExecutions()]);
    const before = statusesHeld(id);
    expect(ACTIVE_STATUSES, "the store's record before the cut").toContain(before.record);
    expect(ACTIVE_STATUSES, "the store's active list before the cut").toContain(before.active);
    const heldAtCut = Math.max(0, ...(getState().events[id] ?? []).map((e) => e.id));

    proxy.cut();
    // The daemon, read past the dashboard: the connection dropped while the execution still ran.
    const atCut = (await call(daemon, "GET", `/executions/${id}`)) as Execution;
    expect(ACTIVE_STATUSES, "the execution's status when the connection was cut; a late cut proves nothing").toContain(atCut.status);

    // A connection loss: the transport reports it, and the topbar shows it in its pill and its banner.
    await until(
      "the store reads the connection lost",
      () => (getState().connection === "reconnecting" || getState().connection === "disconnected" ? true : undefined),
      CONNECTION_WAIT_MS,
    );
    const lossShown = getState().connection;
    const lost = topbar();
    expect(lost).toContain(`conn-pill--${lossShown}`);
    expect(lost).toContain(i18n.t(`connection.${lossShown}`));
    expect(lost).toContain(i18n.t("connection.lostHint"));

    // The connection stays down until the daemon's own API reports the execution completed.
    const deadline = Date.now() + CEILING_MS;
    for (;;) {
      const s = getState();
      expect(["reconnecting", "disconnected"], "the store's connection while it is down").toContain(s.connection);
      const held = statusesHeld(id);
      for (const [where, status] of Object.entries(held)) {
        expect(FAILURES, `the store's ${where} while the connection is down`).not.toContain(status);
      }
      expect(ACTIVE_STATUSES, "the store's record while the connection is down: the last status it received").toContain(held.record);
      expect(ACTIVE_STATUSES, "the store's active list while the connection is down").toContain(held.active);
      const done = (await call(daemon, "GET", `/executions/${id}`)) as Execution;
      if (done.status === "completed") break;
      expect(ACTIVE_STATUSES, "the execution's status in the daemon while the connection is down").toContain(done.status);
      if (Date.now() > deadline) throw new Error(`the execution did not complete within ${CEILING_MS} ms: ${done.status}`);
      await new Promise((r) => setTimeout(r, 100));
    }
    // It went on in the daemon: events the store never received while the connection was down.
    const storedAtRestore = (await call(daemon, "GET", `/executions/${id}/events?limit=5000`)) as { events: HarmflowEvent[] };
    expect(storedAtRestore.events.some((e) => e.id > heldAtCut), "events emitted after the cut").toBe(true);
    expect(topbar()).toContain(i18n.t("connection.lostHint"));

    // Then the current state, with no other action of the test than giving the connection back.
    proxy.restore();
    await until("the store connects again", () => (getState().connection === "connected" ? true : undefined), CONNECTION_WAIT_MS);
    const back = topbar();
    expect(back).toContain("conn-pill--connected");
    expect(showsLoss(back)).toBe(false);
    const daemonRecord = (await call(daemon, "GET", `/executions/${id}`)) as Execution;
    expect(daemonRecord.status).toBe("completed");
    // The store's own refreshes bring it there, each as the reconnection or a replayed event asked for it.
    await until(
      "the store reads the execution completed, in its recent history and its record, with the daemon's attempts",
      () => {
        const s = getState();
        const recent = s.recent.find((e) => e.id === id);
        const record = s.details[id];
        if (s.active.some((e) => e.id === id)) return undefined;
        if (recent?.status !== daemonRecord.status || recent.attemptCount !== daemonRecord.attemptCount) return undefined;
        if (record?.status !== daemonRecord.status) return undefined;
        return JSON.stringify(record.attempts) === JSON.stringify(daemonRecord.attempts) ? true : undefined;
      },
      CEILING_MS,
    );
    expect(getState().details[id]?.attempts).toEqual(daemonRecord.attempts);
    expect(showsLoss(topbar())).toBe(false);

    // Nothing failed: neither the daemon's stored log nor the store holds an event of a failed execution.
    const stored = (await call(daemon, "GET", `/executions/${id}/events?limit=5000`)) as { events: HarmflowEvent[] };
    expect(stored.events.map((e) => e.type)).toContain("execution.completed");
    expect(stored.events.filter((e) => FAILURE_EVENTS.includes(e.type)).map((e) => e.type)).toEqual([]);
    expect((getState().events[id] ?? []).filter((e) => FAILURE_EVENTS.includes(e.type)).map((e) => e.type)).toEqual([]);
  });
});
