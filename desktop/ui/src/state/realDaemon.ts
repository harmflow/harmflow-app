// The way the dashboard's tests run against a real harmflowd, with the stand-in Claude Code CLI of the integration
// suite and nothing standing in for the local API or its event stream. The store reaches the daemon through its own
// HttpTransport and the dev server's daemon proxy of vite.config.ts, both unchanged: serveDashboard serves the dev
// server on a free port and gives the transport what a browser on that origin has, relative URLs, a fetch, and an
// EventSource.
//
// `make test-desktop-daemon` builds harmflowd and names it in HARMFLOW_TEST_DAEMON, and runs the files DAEMON_TESTS
// lists (vite.config.ts). Only those tests import this module, so it stays out of the unit run's coverage.
import { spawn, type ChildProcess } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { request as httpRequest, type ClientRequest } from "node:http";
import { createServer as listen } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer, type ViteDevServer } from "vite";
import { expect, vi } from "vitest";
import type { HarmflowEvent } from "../api/types";

// The module's folder from __dirname, not from import.meta.url: a DOM environment's URL resolves "../.." against it
// into no file URL.
const ui = join(__dirname, "..", "..");
const repoRoot = join(ui, "..", "..");
const fakes = join(repoRoot, "tests", "integration", "testdata");

// As the integration suite waits for an execution: a wait fails once silence passes with no new event, and once the
// ceiling passes in all, so a loaded host only stretches it.
export const SILENCE_MS = 20_000;
export const CEILING_MS = 180_000;
const RUNTIME_WAIT_MS = 20_000;

export interface RealDaemon {
  home: string;
  /** The daemon's own API, for the test's set-up and its independent reads; the store never uses it. */
  base: string;
  token: string;
  process: ChildProcess;
  output: string[];
}

/** An event of the stream, as the transport reads it. */
interface StreamMessage {
  type: string;
  data: string;
  lastEventId: string;
}

/**
 * The dashboard's EventSource, read with Node's HTTP client. Node's own EventSource cannot serve a DOM environment: it
 * builds its open and error events with the global Event, which the environment replaces, and Node's EventTarget then
 * refuses to dispatch them, so the transport would never hear the stream open. This one gives the transport what it
 * uses — onopen and onerror, listeners by event type, readyState, and close — with plain objects for events. It never
 * reconnects on its own: a stream that fails or ends is closed, and the transport opens the next one, as it does when
 * the daemon answers with an error.
 */
class StreamSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = StreamSource.CONNECTING;
  onopen: ((event: { type: string }) => void) | null = null;
  onerror: ((event: { type: string }) => void) | null = null;
  onmessage: ((event: StreamMessage) => void) | null = null;
  private readonly listeners = new Map<string, Set<(event: StreamMessage) => void>>();
  private readonly request: ClientRequest;

  constructor(url: string) {
    this.request = httpRequest(url, { headers: { Accept: "text/event-stream" } }, (res) => {
      if (res.statusCode !== 200) {
        res.resume();
        this.fail();
        return;
      }
      this.readyState = StreamSource.OPEN;
      this.onopen?.({ type: "open" });
      res.setEncoding("utf8");
      let pending = "";
      let event = "";
      let data: string[] = [];
      let lastEventId = "";
      res.on("data", (chunk: string) => {
        const lines = (pending + chunk).split("\n");
        pending = lines.pop() ?? "";
        for (const raw of lines) {
          const line = raw.endsWith("\r") ? raw.slice(0, -1) : raw;
          if (line === "") {
            // A blank line ends an event; one with no data line is dropped, as an EventSource drops it.
            if (data.length > 0) this.emit({ type: event || "message", data: data.join("\n"), lastEventId });
            [event, data] = ["", []];
            continue;
          }
          if (line.startsWith(":")) continue;
          const colon = line.indexOf(":");
          const field = colon < 0 ? line : line.slice(0, colon);
          const value = colon < 0 ? "" : line.slice(colon + 1).replace(/^ /, "");
          if (field === "event") event = value;
          else if (field === "data") data.push(value);
          else if (field === "id") lastEventId = value;
        }
      });
      res.on("end", () => this.fail());
      res.on("error", () => this.fail());
    });
    this.request.on("error", () => this.fail());
    this.request.end();
  }

  addEventListener(type: string, listener: (event: StreamMessage) => void) {
    const listeners = this.listeners.get(type) ?? new Set();
    listeners.add(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type: string, listener: (event: StreamMessage) => void) {
    this.listeners.get(type)?.delete(listener);
  }

  close() {
    this.readyState = StreamSource.CLOSED;
    this.request.destroy();
  }

  private emit(message: StreamMessage) {
    if (this.readyState === StreamSource.CLOSED) return;
    if (message.type === "message") this.onmessage?.(message);
    for (const listener of this.listeners.get(message.type) ?? []) listener(message);
  }

  private fail() {
    if (this.readyState === StreamSource.CLOSED) return;
    this.readyState = StreamSource.CLOSED;
    this.onerror?.({ type: "error" });
  }
}

/** The dev server the dashboard reaches the daemon through, and what the dashboard opened and sent through it. */
export interface Dashboard {
  server: ViteDevServer;
  streams: StreamSource[];
  /**
   * The requests the dashboard sent through its transport: their method and path, the JSON body they sent, the status
   * of their answer once it came, and the body of that answer once read.
   */
  requests: Array<{ method: string; path: string; sent: unknown; status?: number; body: Promise<unknown> }>;
}

/** An answer of the dev server, read whole. */
interface Answer {
  status: number;
  headers: Array<[string, string]>;
  text: string;
}

/**
 * Sends one of the transport's requests with Node's HTTP client, and reads its answer whole. The dashboard's fetch is
 * built on it, not on the environment's own: a DOM environment's fetch is its own implementation, never a browser's,
 * and against the dev server it filled none of the store's reads, whose failures the store drops without a word.
 */
function send(url: string, init?: RequestInit): Promise<Answer> {
  const headers: Record<string, string> = {};
  new Headers(init?.headers).forEach((value, name) => (headers[name] = value));
  return new Promise((resolve, reject) => {
    const req = httpRequest(url, { method: init?.method ?? "GET", headers }, (res) => {
      const chunks: Buffer[] = [];
      res.on("data", (c: Buffer) => chunks.push(c));
      res.on("end", () => {
        const received: Array<[string, string]> = [];
        for (const [name, value] of Object.entries(res.headers)) {
          if (value !== undefined) received.push([name, Array.isArray(value) ? value.join(", ") : value]);
        }
        resolve({ status: res.statusCode ?? 0, headers: received, text: Buffer.concat(chunks).toString("utf8") });
      });
      res.on("error", reject);
    });
    req.on("error", reject);
    req.end(typeof init?.body === "string" ? init.body : undefined);
  });
}

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

export async function until<T>(what: string, read: () => T | undefined, timeoutMs: number): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const value = read();
    if (value !== undefined) return value;
    if (Date.now() > deadline) throw new Error(`${what}: not within ${timeoutMs} ms`);
    await new Promise((r) => setTimeout(r, 20));
  }
}

/** Waits until held() holds execution.completed, failing once SILENCE_MS passes with no new event. */
export async function untilCompleted(what: string, held: () => readonly HarmflowEvent[]): Promise<void> {
  let lastCount = 0;
  let lastChange = Date.now();
  await until(
    what,
    () => {
      const events = held();
      if (events.length !== lastCount) [lastCount, lastChange] = [events.length, Date.now()];
      if (Date.now() - lastChange > SILENCE_MS) throw new Error(`no event in ${SILENCE_MS} ms: ${idsAndTypes(events).join(", ")}`);
      return events.some((e) => e.type === "execution.completed") ? true : undefined;
    },
    CEILING_MS,
  );
}

/**
 * Starts harmflowd as the integration suite does: a temporary home, the file token store, the stand-in harnesses.
 * prepare, when given, writes what the daemon reads at its start into the home, and returns the environment variables
 * it adds to the daemon's.
 */
export async function startDaemon(prepare?: (home: string) => Record<string, string>): Promise<RealDaemon> {
  const binary = process.env.HARMFLOW_TEST_DAEMON;
  if (!binary) throw new Error("HARMFLOW_TEST_DAEMON names no harmflowd: run `make test-desktop-daemon`");
  const home = mkdtempSync(join(tmpdir(), "harmflow-ui-"));
  // Nothing of a HarmFlow session this test may run in reaches the daemon, which would record into it.
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith("HARMFLOW_")));
  Object.assign(env, { HARMFLOW_TOKEN_STORE: "file", CODEX_HOME: join(home, "codex") }, prepare?.(home));
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

/**
 * A request to the daemon's own API, through Node's HTTP client: a DOM environment's fetch would send its page's
 * Origin, which the daemon refuses.
 */
export async function call(d: RealDaemon, method: string, path: string, body?: unknown): Promise<unknown> {
  const payload = body === undefined ? undefined : JSON.stringify(body);
  const { status, text } = await new Promise<{ status: number; text: string }>((resolve, reject) => {
    const req = httpRequest(
      `${d.base}${path}`,
      { method, headers: { Authorization: `Bearer ${d.token}`, "Content-Type": "application/json" } },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (c: Buffer) => chunks.push(c));
        res.on("end", () => resolve({ status: res.statusCode ?? 0, text: Buffer.concat(chunks).toString("utf8") }));
        res.on("error", reject);
      },
    );
    req.on("error", reject);
    req.end(payload);
  });
  if (status >= 400) throw new Error(`${method} ${path}: ${status} ${text}`);
  return text ? JSON.parse(text) : null;
}

/** What the daemon's stored log holds for the execution, read past the dashboard. */
export async function storedEvents(d: RealDaemon, id: string): Promise<HarmflowEvent[]> {
  const res = (await call(d, "GET", `/executions/${id}/events?limit=5000`)) as { events: HarmflowEvent[] };
  return res.events;
}

export const idsAndTypes = (events: readonly HarmflowEvent[] | undefined) => (events ?? []).map((e) => `${e.id} ${e.type}`);

/** The reads of the execution's stored events the dashboard sent. */
export function eventsReadsOf(dashboard: Dashboard, id: string) {
  return dashboard.requests.filter((r) => r.path.startsWith(`/api/v1/executions/${encodeURIComponent(id)}/events`));
}

/** Serves the dev server for the daemon and points the transport's fetch and EventSource at it, as a browser would. */
export async function serveDashboard(d: RealDaemon): Promise<Dashboard> {
  // The proxy reads the daemon's runtime file and token from HARMFLOW_HOME, on each request: never the user's.
  process.env.HARMFLOW_HOME = d.home;
  const port = await freePort();
  const server = await createServer({
    configFile: join(ui, "vite.config.ts"),
    root: ui,
    logLevel: "warn",
    server: { port, strictPort: true, host: "127.0.0.1", hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true },
  });
  await server.listen();
  const origin = `http://127.0.0.1:${port}`;
  const dashboard: Dashboard = { server, streams: [], requests: [] };
  // In a DOM environment, the page is on the dev server's origin, so that its fetch sends the transport's requests as
  // a page of that origin sends them.
  (globalThis as { happyDOM?: { setURL(url: string): void } }).happyDOM?.setURL(origin);

  // A browser on the dev server's origin resolves the transport's relative URLs against it.
  vi.stubGlobal(
    "EventSource",
    class extends StreamSource {
      constructor(url: string | URL) {
        super(new URL(url, origin).href);
        dashboard.streams.push(this);
      }
    },
  );
  vi.stubGlobal("fetch", async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(typeof input === "string" || input instanceof URL ? input : input.url, origin);
    const answer = send(url.href, init);
    const request: Dashboard["requests"][number] = {
      method: (init?.method ?? "GET").toUpperCase(),
      path: url.pathname + url.search,
      sent: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      body: answer.then((a) => JSON.parse(a.text) as unknown).catch(() => null),
    };
    dashboard.requests.push(request);
    const { status, headers, text } = await answer;
    request.status = status;
    // A status that allows no body takes none, or the Response refuses it.
    return new Response([204, 205, 304].includes(status) || text === "" ? null : text, { status, headers });
  });
  return dashboard;
}

/** Closes what serveDashboard opened, then stops the daemon and removes its home. */
export async function shutDown(dashboard: Dashboard | undefined, d: RealDaemon | undefined) {
  for (const s of dashboard?.streams ?? []) s.close();
  await dashboard?.server.close();
  if (d) {
    await stopDaemon(d);
    rmSync(d.home, { recursive: true, force: true });
  }
  vi.unstubAllGlobals();
}
