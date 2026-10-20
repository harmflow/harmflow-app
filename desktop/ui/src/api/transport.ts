// How the interface reaches the daemon. Inside the Tauri shell every request goes through the Rust core, which holds
// the token, so it never reaches the webview (FS-009 FR-005); events arrive as Tauri events from the Rust SSE client.
// In a plain browser on the Vite dev server, requests go to /api, which the dev server proxies to the daemon with the
// token added server-side.
import eventSchema from "../../../../contracts/schemas/event.schema.json";
import type { HarmflowEvent } from "./types";

export type ConnectionState = "connecting" | "connected" | "reconnecting" | "disconnected" | "daemon_stopped";

export interface RawResponse {
  status: number;
  body: unknown;
}

export interface StreamHandlers {
  onEvent(event: HarmflowEvent): void;
  onConnection(state: ConnectionState): void;
  /** The daemon no longer buffers the events after the last one received; refetch them per execution. */
  onReplayGap(): void;
}

export interface Transport {
  readonly mode: "tauri" | "http";
  request(method: string, path: string, body?: unknown): Promise<RawResponse>;
  requestText(path: string): Promise<{ status: number; text: string }>;
  stream(handlers: StreamHandlers): () => void;
}

export const EVENT_TYPES: readonly string[] = eventSchema.$defs.eventType.enum;

export function isTauri(): boolean {
  return typeof window !== "undefined" && "__TAURI_INTERNALS__" in window;
}

class HttpTransport implements Transport {
  readonly mode = "http" as const;

  async request(method: string, path: string, body?: unknown): Promise<RawResponse> {
    const res = await fetch(`/api/v1${path}`, {
      method,
      headers: body === undefined ? undefined : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    return { status: res.status, body: parseBody(text) };
  }

  async requestText(path: string): Promise<{ status: number; text: string }> {
    const res = await fetch(`/api/v1${path}`);
    return { status: res.status, text: await res.text() };
  }

  stream(handlers: StreamHandlers): () => void {
    let source: EventSource | null = null;
    let closed = false;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let lastId = 0;

    const open = () => {
      if (closed) return;
      handlers.onConnection(lastId === 0 ? "connecting" : "reconnecting");
      const url = lastId > 0 ? `/api/v1/events/stream?after=${lastId}` : "/api/v1/events/stream";
      source = new EventSource(url);
      source.onopen = () => handlers.onConnection("connected");
      const onMessage = (msg: MessageEvent<string>) => {
        const event = parseBody(msg.data) as HarmflowEvent | null;
        if (!event || typeof event.id !== "number") return;
        lastId = Math.max(lastId, event.id);
        handlers.onEvent(event);
      };
      for (const type of EVENT_TYPES) source.addEventListener(type, onMessage as EventListener);
      source.addEventListener("message", onMessage as EventListener);
      source.addEventListener("replay_gap", () => handlers.onReplayGap());
      source.onerror = () => {
        // EventSource retries on its own unless the server answered with an error; reopen by hand in that case so the
        // stream resumes after the last id once the daemon is back.
        if (source && source.readyState === EventSource.CLOSED) {
          source.close();
          handlers.onConnection("disconnected");
          retry = setTimeout(open, 3000);
        } else {
          handlers.onConnection("reconnecting");
        }
      };
    };

    open();
    return () => {
      closed = true;
      if (retry) clearTimeout(retry);
      source?.close();
    };
  }
}

class TauriTransport implements Transport {
  readonly mode = "tauri" as const;

  async request(method: string, path: string, body?: unknown): Promise<RawResponse> {
    const { invoke } = await import("@tauri-apps/api/core");
    return invoke<RawResponse>("api_request", { method, path, body: body ?? null });
  }

  async requestText(path: string): Promise<{ status: number; text: string }> {
    const { invoke } = await import("@tauri-apps/api/core");
    return invoke<{ status: number; text: string }>("api_request_text", { path });
  }

  stream(handlers: StreamHandlers): () => void {
    const unlisteners: Array<() => void> = [];
    let disposed = false;
    void (async () => {
      const { listen } = await import("@tauri-apps/api/event");
      const { invoke } = await import("@tauri-apps/api/core");
      const subs = await Promise.all([
        listen<HarmflowEvent>("harmflow://event", (e) => handlers.onEvent(e.payload)),
        listen<{ state: ConnectionState }>("harmflow://connection", (e) => handlers.onConnection(e.payload.state)),
        listen("harmflow://replay-gap", () => handlers.onReplayGap()),
      ]);
      if (disposed) subs.forEach((u) => u());
      else unlisteners.push(...subs);
      // The Rust client may have connected before this listener existed; ask for its current state.
      const state = await invoke<ConnectionState>("connection_state");
      handlers.onConnection(state);
    })();
    return () => {
      disposed = true;
      unlisteners.forEach((u) => u());
    };
  }
}

function parseBody(text: string): unknown {
  if (text === "") return null;
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

export const transport: Transport = isTauri() ? new TauriTransport() : new HttpTransport();
