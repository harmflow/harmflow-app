// @vitest-environment happy-dom
// Drives the task form's schedule preview in a DOM, against a stand-in for the daemon's local API: the form asks for
// the occurrences in the task's time zone and lists them in that zone, not the host's (FS-002 FR-002, AC-003). The
// daemon's own computation of the occurrences is itest › TestTaskLifecycle's.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { SchedulePreview } from "../api/types";
import i18n from "../i18n";
import type { Doc } from "../lib/taskForm";
import { TaskForm } from "./TaskForm";

// The daemon as the client sees it: an answer per route, and every request it received with its body.
const daemon = vi.hoisted(() => ({
  answers: new Map<string, unknown>(),
  requests: [] as { method: string; path: string; body: unknown }[],
}));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string, body?: unknown) => {
      daemon.requests.push({ method, path, body });
      return daemon.answers.has(path) ? { status: 200, body: daemon.answers.get(path) } : { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

let root: Root | undefined;
let host: HTMLElement | undefined;

// A host in São Paulo's zone would list the occurrences at 09:00 whichever zone the form used, so the host is UTC.
beforeAll(async () => {
  vi.stubEnv("TZ", "UTC");
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

afterAll(() => {
  vi.unstubAllEnvs();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
  daemon.answers.clear();
  daemon.requests.length = 0;
});

/** Lets the stand-in daemon's answers arrive and React render them. */
const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

const task = (schedule: Record<string, string>): Doc => ({
  apiVersion: "harmflow/v1alpha1",
  kind: "Task",
  metadata: { id: "standup", name: "Standup notes" },
  spec: { type: "general", prompt: "Write the standup notes.", harness: { preferred: { provider: "claude-code" } }, schedule },
});

async function open(doc: Doc) {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () => root!.render(<TaskForm doc={doc} onChange={() => {}} mode="edit" harnesses={[]} errorPaths={new Set()} />));
  await settle();
}

const listed = () => [...host!.querySelectorAll(".preview__list li")].map((li) => li.textContent ?? "");

describe("task form schedule preview", () => {
  it("lists each occurrence in the task's time zone, not the host's (FS-002 AC-003)", async () => {
    expect(Intl.DateTimeFormat().resolvedOptions().timeZone).toBe("UTC");
    expect(new Date("2026-10-23T12:00:00Z").getHours()).toBe(12);

    // 09:00 in São Paulo, UTC-3 all year, is 12:00 UTC.
    const preview: SchedulePreview = {
      occurrences: ["2026-10-23T12:00:00Z", "2026-10-24T12:00:00Z", "2026-10-25T12:00:00Z"],
      timezone: "America/Sao_Paulo",
    };
    daemon.answers.set("/schedules/preview", preview);
    await open(task({ cron: "0 9 * * *", timezone: "America/Sao_Paulo" }));

    expect(daemon.requests).toEqual([
      { method: "POST", path: "/schedules/preview", body: { schedule: { cron: "0 9 * * *", timezone: "America/Sao_Paulo" }, count: 5 } },
    ]);

    // ICU may put a narrow no-break space before AM; \s matches it.
    const rows = listed();
    expect(rows).toHaveLength(3);
    for (const [i, day] of ["Oct 23, 2026", "Oct 24, 2026", "Oct 25, 2026"].entries()) {
      expect(rows[i]).toContain(day);
      expect(rows[i]).toMatch(/\b9:00\sAM$/);
      expect(rows[i]).not.toContain("12:00");
    }
  });
});
