// The dashboard's view of the daemon. The daemon decides and the dashboard asks: everything here is fetched from the
// local API or derived from its events, and recovered after any reconnection.
import { useSyncExternalStore } from "react";
import { ApiError, api } from "../api/client";
import { native } from "../api/native";
import { transport, type ConnectionState } from "../api/transport";
import type {
  Catalog,
  Diagnostics,
  Execution,
  ExecutionSummary,
  Harness,
  HarmflowEvent,
  Health,
  Notification,
  Queue,
  Settings,
  Stats,
  TaskSummary,
  Tool,
} from "../api/types";

export interface State {
  connection: ConnectionState;
  /** null while unknown; false once the shell confirmed no daemon answers. */
  daemonRunning: boolean | null;
  health?: Health;
  lastUpdate?: number;
  stats?: Stats;
  active: ExecutionSummary[];
  recent: ExecutionSummary[];
  queue?: Queue;
  tasks: TaskSummary[];
  tasksLoaded: boolean;
  settings?: Settings;
  harnesses: Harness[];
  tools: Tool[];
  catalog?: Catalog;
  diagnostics?: Diagnostics;
  unread: Notification[];
  events: Record<string, HarmflowEvent[]>;
  logExpired: Record<string, boolean>;
  details: Record<string, Execution>;
  lastEventId: number;
  /** Per task, the id of the latest event saying its file changed outside the dashboard. */
  externalChanges: Record<string, number>;
  appVersion: string;
  /** The commit the interface was built from; empty without git information. */
  appCommit: string;
  /** When the interface was built, in ISO 8601; empty without a build time. */
  appBuiltAt: string;
  /** Where the latest request to restart the daemon on this window's build stands; idle without one. */
  daemonRestart: DaemonRestart;
  /** The latest event that asked the interface to show something: a navigation target, a Settings section included. */
  navigation?: NavigationTarget & { nonce: number };
}

/** What the interface is asked to show: an execution, or a screen, with a section of Settings when it names one. */
export type NavigationTarget = { executionId?: string; screen?: string; section?: string };

/** "refused": the daemon had executions running, so it was left running. */
export type DaemonRestart = "idle" | "restarting" | "refused";

const MAX_EVENTS_PER_EXECUTION = 5000;

let state: State = {
  connection: "connecting",
  daemonRunning: null,
  active: [],
  recent: [],
  tasks: [],
  tasksLoaded: false,
  harnesses: [],
  tools: [],
  unread: [],
  events: {},
  logExpired: {},
  details: {},
  lastEventId: 0,
  externalChanges: {},
  appVersion: __APP_VERSION__,
  appCommit: __APP_COMMIT__,
  appBuiltAt: __APP_BUILT_AT__,
  daemonRestart: "idle",
};

const listeners = new Set<() => void>();

function setState(patch: Partial<State> | ((s: State) => Partial<State>)) {
  const next = typeof patch === "function" ? patch(state) : patch;
  state = { ...state, ...next };
  listeners.forEach((l) => l());
}

export function getState(): State {
  return state;
}

export function useStore<T>(selector: (s: State) => T): T {
  const snapshot = () => selector(state);
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    snapshot,
    snapshot,
  );
}

/** Replaces part of the state directly; for tests that render screens from a known state. */
export function patchStateForTests(patch: Partial<State>) {
  setState(patch);
}

function touch() {
  setState({ lastUpdate: Date.now() });
}

async function settle<T>(p: Promise<T>): Promise<T | undefined> {
  try {
    return await p;
  } catch {
    return undefined;
  }
}

export const actions = {
  async refreshExecutions() {
    const [active, recent, queue, stats] = await Promise.all([
      settle(api.executions({ active: true, limit: 50 })),
      settle(api.executions({ limit: 12, status: "completed,no_work,failed,cancelled,interrupted,needs_attention" })),
      settle(api.queue()),
      settle(api.stats()),
    ]);
    setState((s) => ({
      active: active ?? s.active,
      recent: recent ?? s.recent,
      queue: queue ?? s.queue,
      stats: stats ?? s.stats,
    }));
    touch();
  },

  async refreshStats() {
    const stats = await settle(api.stats());
    if (stats) setState({ stats });
  },

  async refreshHealth() {
    try {
      const health = await api.health();
      setState({ health, daemonRunning: true });
    } catch {
      if (native.available) {
        const status = await settle(native.daemonStatus());
        if (status && !status.running) setState({ daemonRunning: false, connection: "daemon_stopped" });
      }
    }
  },

  async refreshTasks() {
    const tasks = await settle(api.tasks());
    if (tasks) setState({ tasks, tasksLoaded: true });
  },

  async refreshSettings() {
    const [settings, diagnostics] = await Promise.all([settle(api.settings()), settle(api.diagnostics())]);
    setState((s) => ({ settings: settings ?? s.settings, diagnostics: diagnostics ?? s.diagnostics }));
    if (settings) {
      void native.setTrayEnabled(settings.trayEnabled);
      void native.setKeepTrayOnQuit(settings.keepTrayOnQuit);
    }
    return settings;
  },

  async refreshHarnesses(redetect = false) {
    const [harnesses, tools] = await Promise.all([
      settle(redetect ? api.refreshHarnesses() : api.harnesses()),
      settle(api.tools()),
    ]);
    setState((s) => ({ harnesses: harnesses ?? s.harnesses, tools: tools ?? s.tools }));
  },

  async refreshCatalog() {
    const catalog = await settle(api.catalog());
    if (catalog) setState({ catalog });
  },

  async refreshNotifications() {
    const unread = await settle(api.notifications(true));
    if (unread) setState({ unread });
  },

  async markRead(ids?: string[]) {
    await settle(api.markNotificationsRead(ids));
    setState((s) => ({ unread: ids ? s.unread.filter((n) => !ids.includes(n.id)) : [] }));
  },

  async refreshAll() {
    await Promise.all([
      actions.refreshHealth(),
      actions.refreshExecutions(),
      actions.refreshTasks(),
      actions.refreshSettings(),
      actions.refreshHarnesses(),
      actions.refreshCatalog(),
      actions.refreshNotifications(),
    ]);
  },

  /**
   * Replaces the daemon with this bundle's: asks it to stop without force, then starts this bundle's harmflowd once its
   * process exits. A daemon with executions running refuses and keeps running; nothing asks it again with force.
   * Only a click calls this: another window or `make update-app` may own the daemon. Other failures are thrown.
   */
  async restartDaemon() {
    if (state.daemonRestart === "restarting") return;
    const pid = state.health?.pid;
    setState({ daemonRestart: "restarting" });
    try {
      await api.stopDaemon(false);
    } catch (err) {
      const refused = err instanceof ApiError && err.status === 409;
      setState({ daemonRestart: refused ? "refused" : "idle" });
      if (refused) return;
      throw err;
    }
    try {
      await native.startDaemon(pid);
      await actions.refreshAll();
    } finally {
      setState({ daemonRestart: "idle" });
    }
  },

  async loadExecution(id: string) {
    const detail = await settle(api.execution(id));
    if (detail) setState((s) => ({ details: { ...s.details, [id]: detail } }));
    return detail;
  },

  /** Loads the stored events of an execution after the last one already held (replay). */
  async loadEvents(id: string) {
    const held = state.events[id] ?? [];
    const after = held.length ? held[held.length - 1].id : 0;
    try {
      const res = await api.executionEvents(id, after);
      setState((s) => ({
        events: { ...s.events, [id]: mergeEvents(s.events[id] ?? [], res.events) },
        logExpired: { ...s.logExpired, [id]: res.logExpired },
      }));
    } catch {
      // The execution may be gone or the daemon unreachable; the next reconnection retries.
    }
  },

  navigate(target: NavigationTarget) {
    setState({ navigation: { ...target, nonce: Date.now() } });
  },

  /** Drops the navigation target once the interface took or ignored it, so it never fires later. */
  clearNavigation() {
    setState({ navigation: undefined });
  },
};

function mergeEvents(held: HarmflowEvent[], incoming: HarmflowEvent[]): HarmflowEvent[] {
  if (incoming.length === 0) return held;
  const ids = new Set(held.map((e) => e.id));
  const merged = held.concat(incoming.filter((e) => !ids.has(e.id)));
  merged.sort((a, b) => a.id - b.id);
  return merged.length > MAX_EVENTS_PER_EXECUTION ? merged.slice(-MAX_EVENTS_PER_EXECUTION) : merged;
}

// Coalesces bursts of events into one refresh per area.
const timers = new Map<string, ReturnType<typeof setTimeout>>();
function debounce(key: string, fn: () => void, ms = 250) {
  const t = timers.get(key);
  if (t) clearTimeout(t);
  timers.set(
    key,
    setTimeout(() => {
      timers.delete(key);
      fn();
    }, ms),
  );
}

/** Refetches whatever a gap in the stream may have hidden: lists, and the events of each execution held. */
function recover() {
  void actions.refreshAll();
  for (const id of Object.keys(state.events)) void actions.loadEvents(id);
  for (const id of Object.keys(state.details)) void actions.loadExecution(id);
}

type NotificationHandler = (n: Notification) => void;
let onNotification: NotificationHandler = () => {};

export function setNotificationHandler(handler: NotificationHandler) {
  onNotification = handler;
}

function handleEvent(e: HarmflowEvent) {
  setState((s) => {
    const patch: Partial<State> = { lastEventId: Math.max(s.lastEventId, e.id), lastUpdate: Date.now() };
    if (e.executionId) {
      patch.events = { ...s.events, [e.executionId]: mergeEvents(s.events[e.executionId] ?? [], [e]) };
    }
    if (e.type === "task.changed_externally" && e.taskId) {
      patch.externalChanges = { ...s.externalChanges, [e.taskId]: e.id };
    }
    return patch;
  });

  const family = e.type.split(".")[0];
  if (family === "execution" || family === "firing" || e.type === "harness.switched") {
    debounce("executions", () => void actions.refreshExecutions());
  }
  if (family === "task" || (family === "execution" && e.type !== "execution.status_changed")) {
    debounce("tasks", () => void actions.refreshTasks(), 400);
  }
  // A check ended: read the inventory again, without detecting again, which would announce another check.
  if (e.type === "harness.checked") {
    debounce("harnesses", () => void actions.refreshHarnesses());
  }
  // Output lines change nothing in the record, and a program can print many of them.
  if (e.executionId && state.details[e.executionId] && e.type !== "harness.output" && e.type !== "script.output") {
    const id = e.executionId;
    debounce(`detail:${id}`, () => void actions.loadExecution(id), 500);
  }
  if (e.type === "notification.created") {
    const n = e.payload as unknown as Notification | undefined;
    void actions.refreshNotifications();
    if (n && n.id) onNotification(n);
  }
}

let started = false;

/** Opens the event stream and the periodic refreshes. Called once, at startup. */
export function startSync() {
  if (started) return;
  started = true;
  let previous: ConnectionState = "connecting";
  transport.stream({
    onEvent: handleEvent,
    onConnection: (connection) => {
      setState({ connection });
      if (connection === "connected") {
        setState({ daemonRunning: true });
        if (previous !== "connected") recover();
      }
      if (connection === "daemon_stopped") setState({ daemonRunning: false });
      previous = connection;
    },
    onReplayGap: recover,
  });
  void native.appVersion().then((appVersion) => setState({ appVersion }));
  void actions.refreshAll();
  setInterval(() => void actions.refreshStats(), 5000);
  setInterval(() => void actions.refreshHealth(), 10000);
  setInterval(() => void actions.refreshExecutions(), 30000);
  void native.onNavigate((target) => actions.navigate(target));
}
