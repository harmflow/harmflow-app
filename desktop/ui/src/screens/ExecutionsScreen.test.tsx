// A dashboard opened late, against a real harmflowd (realDaemon.ts): each execution starts, through the daemon's API,
// before the dashboard's store exists, and the store started afterwards lists it on the Executions screen and recovers
// its whole history, whether it is still running or has ended.
//
// `make test-desktop-daemon` runs it; it stays out of `pnpm test` and its coverage (vite.config.ts, DAEMON_TESTS).
import { renderToString } from "react-dom/server";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { isActive, type HarmflowEvent } from "../api/types";
import i18n from "../i18n";
import { buildTimeline } from "../lib/timeline";
import {
  call,
  CEILING_MS,
  eventsReadsOf,
  idsAndTypes,
  serveDashboard,
  shutDown,
  SILENCE_MS,
  startDaemon,
  storedEvents,
  until,
  untilCompleted,
  type Dashboard,
  type RealDaemon,
} from "../state/realDaemon";
import { actions, getState, startSync } from "../state/store";
import { ExecutionsScreen } from "./ExecutionsScreen";

const INTERRUPTIONS = ["execution.interrupted", "execution.cancelled", "daemon.stopping"];

let daemon: RealDaemon;
let dashboard: Dashboard;
/** The execution that completed before the store started. */
let ended: string;

/** A task of the stand-in Claude Code CLI with no notice, so that its stored log ends at execution.completed. */
async function createTask(id: string, name: string, scenario: string) {
  await call(daemon, "POST", "/tasks", {
    yaml: [
      "apiVersion: harmflow/v1alpha1",
      "kind: Task",
      "metadata:",
      `  id: ${id}`,
      `  name: ${name}`,
      "spec:",
      "  type: general",
      `  prompt: "SCENARIO=${scenario}"`,
      "  harness:",
      "    preferred:",
      "      provider: claude-code",
      "  notifications:",
      "    native:",
      "      events: []",
      "",
    ].join("\n"),
  });
}

async function run(taskId: string): Promise<string> {
  const { executionId } = (await call(daemon, "POST", `/tasks/${taskId}/run`)) as { executionId: string };
  return executionId;
}

/** Waits until the daemon's own log of the execution holds an event that match accepts, and returns the log. */
async function untilStored(id: string, what: string, match: (e: HarmflowEvent) => boolean): Promise<HarmflowEvent[]> {
  const deadline = Date.now() + CEILING_MS;
  for (;;) {
    const events = await storedEvents(daemon, id);
    if (events.some(match)) return events;
    if (Date.now() > deadline) throw new Error(`${what}: not within ${CEILING_MS} ms: ${idsAndTypes(events).join(", ")}`);
    await new Promise((r) => setTimeout(r, 50));
  }
}

/** Starts the dashboard's store, as the app does at startup, and waits for its event stream. */
async function openDashboard() {
  startSync();
  await until("the store's event stream connects", () => (getState().connection === "connected" ? true : undefined), 20_000);
}

function expectNothingInterrupted(events: readonly HarmflowEvent[]) {
  expect(events.filter((e) => INTERRUPTIONS.includes(e.type)).map((e) => e.type)).toEqual([]);
}

/** The store's events of the execution equal the daemon's stored log, by id, type, and order, each once. */
async function expectTheWholeHistory(id: string): Promise<HarmflowEvent[]> {
  const stored = await storedEvents(daemon, id);
  expect(stored[0]?.type).toBe("execution.created");
  expect(stored[stored.length - 1]?.type).toBe("execution.completed");
  const held = getState().events[id] ?? [];
  expect(idsAndTypes(held)).toEqual(idsAndTypes(stored));
  expect(new Set(held.map((e) => e.id)).size).toBe(held.length);
  // The timeline drops the raw and hidden types and folds a call's end into its start: it holds each event it shows
  // once, and is the timeline of the daemon's log.
  const timeline = buildTimeline(held);
  expect(new Set(timeline.map((item) => item.id)).size).toBe(timeline.length);
  expect(timeline).toEqual(buildTimeline(stored));
  expectNothingInterrupted(stored);
  return stored;
}

beforeAll(async () => {
  daemon = await startDaemon();
  dashboard = await serveDashboard(daemon);
  await i18n.changeLanguage("en-US");
  await createTask("ended-before", "Ended before the dashboard", "ok");
  await createTask("running-at-open", "Running when the dashboard opens", "ticks");
  // No store exists yet: the first execution runs to its end with nothing of the dashboard started.
  ended = await run("ended-before");
  await untilStored(ended, "the first execution's end in the daemon's log", (e) => e.type === "execution.completed");
}, CEILING_MS + 120_000);

afterAll(async () => {
  await shutDown(dashboard, daemon);
});

describe("a dashboard opened late against the real daemon", () => {
  it("lists an execution still running and recovers its history, then follows it live to its end", async () => {
    expect(dashboard.streams, "a dashboard stream before the execution started").toHaveLength(0);
    const id = await run("running-at-open");
    // The store opens once the execution has emitted a message, so that some of its history precedes the dashboard.
    const before = await untilStored(id, "a message of the execution in the daemon's log", (e) => e.type === "harness.message");
    expect(before[0]?.type).toBe("execution.created");
    expect(before.some((e) => e.type === "execution.completed"), "the execution ended before the dashboard opened").toBe(false);

    await openDashboard();
    const listed = await until(
      "the execution among the store's active executions",
      () => {
        if (getState().recent.some((e) => e.id === id)) throw new Error("the execution ended before the store listed it as active");
        return getState().active.find((e) => e.id === id);
      },
      SILENCE_MS,
    );
    expect(listed.taskId).toBe("running-at-open");
    expect(listed.taskName).toBe("Running when the dashboard opens");
    expect(isActive(listed.status), `status ${listed.status}`).toBe(true);

    // The Executions screen rendered from the store shows its card.
    const html = renderToString(<ExecutionsScreen selectedId={null} onSelect={() => {}} onGoToTasks={() => {}} onSeeHistory={() => {}} />);
    expect(html).toContain(i18n.t("executions.activeTitle", { count: 1 }));
    expect(html).toMatch(/class="exec-card__title" title="Running when the dashboard opens"/);

    // Opening its detail gives the store every event emitted before the dashboard opened, from execution.created on.
    await Promise.all([actions.loadExecution(id), actions.loadEvents(id)]);
    expect(eventsReadsOf(dashboard, id)).toHaveLength(1);
    const atOpen = getState().events[id] ?? [];
    expect(atOpen[0]?.type).toBe("execution.created");
    expect(idsAndTypes(atOpen).slice(0, before.length)).toEqual(idsAndTypes(before));
    const readUpTo = Math.max(0, ...atOpen.map((e) => e.id));

    // The later events arrive from the stream, with no further read of the stored events, until execution.completed.
    await until(
      "an event of the execution after the detail's read",
      () => {
        const held = getState().events[id] ?? [];
        return held.some((e) => e.id > readUpTo) ? true : undefined;
      },
      SILENCE_MS,
    );
    await untilCompleted("the execution's end in the store", () => getState().events[id] ?? []);
    expect(eventsReadsOf(dashboard, id), "reads of the stored events once the execution ended").toHaveLength(1);
    await expectTheWholeHistory(id);
  });

  it("lists an execution that ended before it opened as completed, and recovers its whole history", async () => {
    // The store started after this execution completed, in the test above or here.
    await openDashboard();
    const listed = await until("the execution in the store's recent history", () => getState().recent.find((e) => e.id === ended), SILENCE_MS);
    expect(listed.taskId).toBe("ended-before");
    expect(listed.status).toBe("completed");
    expect(getState().events[ended], "events of the ended execution before its detail opened").toBeUndefined();

    await Promise.all([actions.loadExecution(ended), actions.loadEvents(ended)]);
    expect(getState().logExpired[ended]).toBe(false);
    await expectTheWholeHistory(ended);
  });
});
