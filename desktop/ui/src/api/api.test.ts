// The Desktop's transport, with the Tauri API mocked: the connection state the shell's event stream held before the
// listener existed, each state it emits after, and each event reach the store's handlers, until the store disposes of
// the stream. transport.test.ts runs the web build's transport against a real daemon, apart from the unit run.
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ConnectionState, StreamHandlers, Transport } from "./transport";
import type { HarmflowEvent } from "./types";

type Handler = (event: { payload: unknown }) => void;

const tauri = vi.hoisted(() => {
  // The listeners on each Tauri event, as the shell's emits reach them; an unlisten removes its own.
  const listeners = new Map<string, Set<Handler>>();
  return {
    listeners,
    listen: vi.fn(async (name: string, handler: Handler) => {
      const set = listeners.get(name) ?? new Set<Handler>();
      listeners.set(name, set);
      set.add(handler);
      return () => {
        set.delete(handler);
      };
    }),
    invoke: vi.fn(async (_command: string): Promise<unknown> => "daemon_stopped"),
    emit(name: string, payload: unknown) {
      for (const handler of [...(listeners.get(name) ?? [])]) handler({ payload });
    },
  };
});
vi.mock("@tauri-apps/api/event", () => ({ listen: tauri.listen }));
vi.mock("@tauri-apps/api/core", () => ({ invoke: tauri.invoke }));

const STREAM_EVENTS = ["harmflow://event", "harmflow://connection", "harmflow://replay-gap"];

/** The transport the Desktop's window selects, loaded afresh with the window seen as the Desktop's. */
async function desktopTransport(): Promise<Transport> {
  vi.stubGlobal("window", { __TAURI_INTERNALS__: {} });
  vi.resetModules();
  const { transport } = await import("./transport");
  return transport;
}

function recorder() {
  const states: ConnectionState[] = [];
  const events: HarmflowEvent[] = [];
  const handlers: StreamHandlers = {
    onEvent: (event) => events.push(event),
    onConnection: (state) => states.push(state),
    onReplayGap: () => {},
  };
  return { states, events, handlers };
}

const event = (id: number) => ({ id, type: "harness.message", executionId: "exec-1" }) as unknown as HarmflowEvent;

describe("TauriTransport.stream", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    tauri.listeners.clear();
    tauri.invoke.mockClear();
  });

  it("passes the shell's states and events to the store until it is disposed of", async () => {
    const transport = await desktopTransport();
    expect(transport.mode).toBe("tauri");
    const { states, events, handlers } = recorder();

    const dispose = transport.stream(handlers);
    // First the state the shell held before the listeners existed, as connection_state returns it.
    await vi.waitFor(() => expect(states).toEqual(["daemon_stopped"]));
    expect(tauri.invoke).toHaveBeenCalledWith("connection_state");
    for (const name of STREAM_EVENTS) expect(tauri.listeners.get(name)?.size, name).toBe(1);

    tauri.emit("harmflow://connection", { state: "connected" });
    tauri.emit("harmflow://event", event(7));
    tauri.emit("harmflow://connection", { state: "reconnecting" });
    tauri.emit("harmflow://connection", { state: "connected" });
    tauri.emit("harmflow://event", event(8));
    expect(states).toEqual(["daemon_stopped", "connected", "reconnecting", "connected"]);
    expect(events).toEqual([event(7), event(8)]);

    // Disposed of, the stream removes its listeners, and nothing the shell emits after reaches the handlers.
    dispose();
    for (const name of STREAM_EVENTS) expect(tauri.listeners.get(name)?.size, name).toBe(0);
    tauri.emit("harmflow://connection", { state: "reconnecting" });
    tauri.emit("harmflow://event", event(9));
    expect(states).toEqual(["daemon_stopped", "connected", "reconnecting", "connected"]);
    expect(events).toEqual([event(7), event(8)]);
  });
});
