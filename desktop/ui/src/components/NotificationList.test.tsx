// @vitest-environment happy-dom
// Drives the unread notices in a DOM, against a stand-in for the daemon's local API: the notice that a task reached its
// run-again limit reads in each interface language, with the limit counted, and opens its execution (harmflow-app#1041).
// That the daemon sends it whatever events the task chose is app › TestRunAgainStopsAtItsLimit's.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { Notification } from "../api/types";
import i18n from "../i18n";
import { getState, patchStateForTests } from "../state/store";
import { NotificationList } from "./NotificationList";

// The daemon as the client sees it: every request it received, each answered with nothing.
const daemon = vi.hoisted(() => ({ requests: [] as string[] }));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string) => {
      daemon.requests.push(`${method} ${path}`);
      return { status: 204, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

let root: Root | undefined;
let host: HTMLElement | undefined;
const unread = getState().unread;

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

afterEach(async () => {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
  daemon.requests.length = 0;
  patchStateForTests({ unread });
  await i18n.changeLanguage("en-US");
});

/** The notice as the daemon keeps it: its text in en-US, and the params the interface writes its own from. */
const limitReached = (count: string): Notification => ({
  id: `n-limit-${count}`,
  createdAt: "2026-11-15T10:00:00Z",
  eventType: "execution.run_again_limit_reached",
  taskId: "core-issues",
  executionId: "exec-last",
  title: "Run-again limit reached",
  body: `Core issues ran again ${count} times in a row and now waits for its schedule.`,
  severity: "warning",
  read: false,
  params: { taskName: "Core issues", executionId: "exec-last", count },
});

async function show(notice: Notification, onOpenExecution: (id: string) => void = () => {}) {
  patchStateForTests({ unread: [notice] });
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () => root!.render(<NotificationList onOpenExecution={onOpenExecution} empty={null} toned />));
}

const row = () => host!.querySelector<HTMLElement>("li > *")!;

describe("the run-again limit's notice", () => {
  it.each([
    ["en-US", "10", "Core issues: run-again limit reached", "Core issues ran again 10 times in a row, its run-again limit; it now waits for its schedule."],
    ["en-US", "1", "Core issues: run-again limit reached", "Core issues ran again 1 time in a row, its run-again limit; it now waits for its schedule."],
    [
      "pt-BR",
      "3",
      "Core issues: limite de execuções seguidas atingido",
      "Core issues rodou de novo 3 vezes seguidas, seu limite de execuções seguidas; agora aguarda seu agendamento.",
    ],
    [
      "es-ES",
      "3",
      "Core issues: límite de ejecuciones seguidas alcanzado",
      "Core issues se ejecutó de nuevo 3 veces seguidas, su límite de ejecuciones seguidas; ahora espera su programación.",
    ],
  ])("reads in %s with a limit of %s", async (language, count, title, body) => {
    await i18n.changeLanguage(language);
    await show(limitReached(count));
    expect(row().textContent).toContain(title);
    expect(row().textContent).toContain(body);
  });

  it("opens its execution when clicked, and marks it read", async () => {
    const opened = vi.fn();
    await show(limitReached("10"), opened);
    await act(async () => row().click());
    expect(opened).toHaveBeenCalledWith("exec-last");
    expect(daemon.requests.some((r) => r.startsWith("POST /notifications"))).toBe(true);
  });
});
