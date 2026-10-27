// @vitest-environment happy-dom
// Drives the task form in a DOM, against a stand-in for the daemon's local API. Its schedule preview asks for the
// occurrences in the task's time zone and lists them in that zone, not the host's. The
// daemon's own computation of the occurrences is itest › TestTaskLifecycle's.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { Catalog, SchedulePreview } from "../api/types";
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

async function open(doc: Doc, mode: "create" | "edit" = "edit") {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () => root!.render(<TaskForm doc={doc} onChange={() => {}} mode={mode} harnesses={[]} errorPaths={new Set()} />));
  await settle();
}

const listed = () => [...host!.querySelectorAll(".preview__list li")].map((li) => li.textContent ?? "");

describe("task form schedule preview", () => {
  it("lists each occurrence in the task's time zone, not the host's", async () => {
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

// The type is chosen in the template picker and fixed from then on; the form shows it and follows it. The daemon's
// refusal of a changed type is app › TestUpdateKeepsTheType's.
describe("task form type", () => {
  const typeInput = () =>
    [...host!.querySelectorAll("label.field")]
      .find((field) => field.querySelector(".field__label")?.textContent === "Task type")
      ?.querySelector("input");
  const sections = () => [...host!.querySelectorAll("section.form-section")].map((s) => s.id);

  it.each(["create", "edit"] as const)("shows the type read-only in %s mode", async (mode) => {
    await open(task({}), mode);
    expect(typeInput()?.value).toBe("General");
    expect(typeInput()?.disabled).toBe(true);
    expect(host!.querySelector("#section-general select")).toBeNull();
  });

  it("shows the sections of the document's type", async () => {
    await open(
      { apiVersion: "harmflow/v1alpha1", kind: "Task", metadata: { id: "audit", name: "Audit" }, spec: { type: "script", script: { command: ["/bin/date"] } } },
      "create",
    );
    expect(typeInput()?.value).toBe("Script");
    expect(sections()).toContain("section-script");
    expect(sections()).not.toContain("section-prompt");
    expect(sections()).not.toContain("section-harness");
  });
});

// Selecting an agent copies it into the task once; the copy is the task's, and never follows the agent. Whether the
// session then runs that copy with no main agent is the harness adapters' TestPrepareDeliversBuiltins.
describe("task form agent", () => {
  const HASH = "sha256:" + "a".repeat(64);
  const reviewer = {
    prompt: "You review the change.",
    description: "Reviews a change",
    model: "sonnet",
    modelHarnesses: ["claude-code"],
    skills: ["review-notes"],
    omittedFields: ["tools"],
    hash: HASH,
  };
  const catalog = (copy = reviewer): Catalog => ({
    agents: [
      { name: "reviewer", kind: "agent", source: "user", harnesses: ["claude-code"], copy },
      { name: "tester", kind: "agent", source: "user", harnesses: ["claude-code"], copy: { ...reviewer, prompt: "You test.", hash: "sha256:" + "c".repeat(64) } },
    ],
    skills: [],
    problems: [],
    taskTypes: [],
  });
  let current: Doc = {};

  function Form({ doc, cat }: { doc: Doc; cat: Catalog }) {
    const [value, setValue] = useState(doc);
    current = value;
    return <TaskForm doc={value} onChange={(d) => setValue((current = d))} mode="edit" catalog={cat} harnesses={[]} errorPaths={new Set()} />;
  }

  async function openForm(doc: Doc, cat: Catalog) {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Form doc={doc} cat={cat} />));
    await settle();
  }

  const select = () => host!.querySelector<HTMLSelectElement>('select[aria-label="Agent"]')!;
  const prompt = () => host!.querySelector<HTMLTextAreaElement>("textarea.input--prompt")!;
  async function choose(name: string) {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(select(), name);
      select().dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
  }
  async function edit(text: string) {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(prompt(), text);
      prompt().dispatchEvent(new Event("input", { bubbles: true }));
    });
  }
  const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent === label);
  const fieldRows = () => [...document.querySelectorAll(".copy-changes__row")].map((r) => [r.querySelector("dt")?.textContent, r.querySelector("dd")?.textContent]);

  it("copies the agent, names what it left out, and asks before another agent replaces an edited prompt", async () => {
    // Outside the desktop shell, the form asks through the page's own confirm, which the test DOM lacks.
    const confirm = vi.fn((_message?: string) => false);
    Object.assign(window, { confirm });
    try {
      await openForm(task({}), catalog());
      // The prompt the task already has is the user's: replacing it asks, and declining keeps it.
      await choose("reviewer");
      expect(confirm).toHaveBeenCalledTimes(1);
      expect(prompt().value).toBe("Write the standup notes.");

      confirm.mockReturnValue(true);
      await choose("reviewer");
      expect(prompt().value).toBe("You review the change.");
      expect(current.metadata).toMatchObject({ description: "Reviews a change" });
      expect(current.spec).toMatchObject({
        harness: { preferred: { provider: "claude-code", model: "sonnet" } },
        skills: ["review-notes"],
        origin: { agent: "reviewer", hash: HASH },
      });
      expect(host!.querySelector(".agent-left-out")?.textContent).toContain("Not copied from the agent: tools.");

      await edit("You review the change. Then you say why.");
      confirm.mockReturnValue(false);
      confirm.mockClear();
      await choose("tester");
      expect(confirm).toHaveBeenCalledTimes(1);
      expect(prompt().value).toBe("You review the change. Then you say why.");
      expect(current.spec).toMatchObject({ origin: { agent: "reviewer", hash: HASH } });
    } finally {
      Reflect.deleteProperty(window, "confirm");
    }
  });

  it("offers to copy a changed agent again, and replaces the copy only once confirmed", async () => {
    const newer = { ...reviewer, prompt: "You review the change.\nYou run the tests.", hash: "sha256:" + "b".repeat(64) };
    const doc = task({});
    (doc.spec as Doc).prompt = "You review the change.";
    (doc.spec as Doc).origin = { agent: "reviewer", hash: HASH };
    await openForm(doc, catalog(newer));
    expect(host!.querySelector(".banner")?.textContent).toContain("The agent reviewer has changed");

    await act(async () => button("Copy again from the agent")!.click());
    const diff = [...document.querySelectorAll(".prompt-diff__line")].map((l) => l.textContent);
    expect(diff).toEqual(["  You review the change.\n", "+ You run the tests.\n"]);
    // The task has no description, model, or skills of its own yet, so replacing the copy sets all three.
    expect(fieldRows()).toEqual([
      ["Description", "None → Reviews a change"],
      ["Model", "Harness default → sonnet"],
      ["Skills", "None → review-notes"],
    ]);
    await act(async () => button("Cancel")!.click());
    expect(prompt().value).toBe("You review the change.");

    await act(async () => button("Copy again from the agent")!.click());
    await act(async () => button("Replace the copy")!.click());
    expect(prompt().value).toBe("You review the change.\nYou run the tests.");
    expect(current.spec).toMatchObject({ origin: { agent: "reviewer", hash: newer.hash } });
    expect(host!.querySelector(".banner")).toBeNull();
  });

  it("shows a changed model and skills before copying again an agent whose instructions did not change", async () => {
    const newer = { ...reviewer, model: "opus", skills: ["review-notes", "tests"], hash: "sha256:" + "b".repeat(64) };
    const doc = task({});
    doc.metadata = { ...(doc.metadata as Doc), description: "Reviews a change" };
    doc.spec = {
      ...(doc.spec as Doc),
      prompt: "You review the change.",
      harness: { preferred: { provider: "claude-code", model: "sonnet" } },
      skills: ["review-notes"],
      origin: { agent: "reviewer", hash: HASH },
    };
    await openForm(doc, catalog(newer));
    expect(host!.querySelector(".banner")?.textContent).toContain("The agent reviewer has changed");

    await act(async () => button("Copy again from the agent")!.click());
    expect(fieldRows()).toEqual([
      ["Model", "sonnet → opus"],
      ["Skills", "review-notes → review-notes, tests"],
    ]);
    expect(document.querySelector(".prompt-diff")).toBeNull();
    expect(document.querySelector(".agent-copy-same")?.textContent).toBe("The prompt stays as it is.");

    await act(async () => button("Replace the copy")!.click());
    expect(prompt().value).toBe("You review the change.");
    expect(current.spec).toMatchObject({
      harness: { preferred: { provider: "claude-code", model: "opus" } },
      skills: ["review-notes", "tests"],
      origin: { agent: "reviewer", hash: newer.hash },
    });
  });

  it("says nothing in the task changes when the agent changed only in what the copy keeps", async () => {
    // The agent dropped its skill; the copy adds skills and never removes one, so the task stays as it is.
    const newer = { ...reviewer, skills: [], hash: "sha256:" + "b".repeat(64) };
    const doc = task({});
    doc.metadata = { ...(doc.metadata as Doc), description: "Reviews a change" };
    doc.spec = {
      ...(doc.spec as Doc),
      prompt: "You review the change.",
      harness: { preferred: { provider: "claude-code", model: "sonnet" } },
      skills: ["review-notes"],
      origin: { agent: "reviewer", hash: HASH },
    };
    await openForm(doc, catalog(newer));

    await act(async () => button("Copy again from the agent")!.click());
    expect(fieldRows()).toEqual([]);
    expect(document.querySelector(".prompt-diff")).toBeNull();
    expect(document.querySelector(".agent-copy-same")?.textContent).toBe(
      "Nothing in the task changes: replacing the copy only records the agent's current version.",
    );
  });

  it("shows nothing for an origin agent that no longer exists", async () => {
    const doc = task({});
    (doc.spec as Doc).origin = { agent: "gone", hash: HASH };
    await openForm(doc, catalog());
    expect(host!.querySelector(".banner")).toBeNull();
    expect(host!.querySelector(".agent-left-out")).toBeNull();
    expect(select().value).toBe("gone");
    expect(prompt().value).toBe("Write the standup notes.");
  });
});

// The Schedule section builds the expression from a frequency and its controls, keeps any other expression as typed
// under Custom, and describes it once the daemon accepted it. Each frequency's text, and the description in every
// language, are lib › schedule.test.ts's.
describe("task form schedule builder", () => {
  let current: Doc = {};
  const onChange = vi.fn();

  function Form({ doc }: { doc: Doc }) {
    const [value, setValue] = useState(doc);
    current = value;
    return (
      <TaskForm
        doc={value}
        onChange={(d) => {
          onChange(d);
          setValue((current = d));
        }}
        mode="edit"
        harnesses={[]}
        errorPaths={new Set()}
      />
    );
  }

  async function openForm(doc: Doc) {
    onChange.mockClear();
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Form doc={doc} />));
    await settle();
  }

  const schedule = () => (current.spec as Doc).schedule as Doc | undefined;
  const field = (label: string) =>
    [...host!.querySelectorAll("label.field")].find((f) => f.querySelector(".field__label")?.textContent === label);
  const control = <E extends Element>(label: string) => field(label)?.querySelector<E>("select, input, output") ?? null;
  const expression = () => control<HTMLInputElement | HTMLOutputElement>("Cron expression (5 fields)");
  const description = () => host!.querySelector(".schedule-description")?.textContent;
  const day = (name: string) => host!.querySelector<HTMLButtonElement>(`button[aria-label="${name}"]`)!;

  async function pick(label: string, value: string) {
    const el = control<HTMLSelectElement>(label)!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event("change", { bubbles: true }));
    });
  }
  async function type(el: HTMLInputElement, value: string) {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }
  async function click(el: HTMLElement) {
    await act(async () => el.click());
  }
  /** Waits out the preview's debounce, then lets the daemon's answer render. */
  async function previewed() {
    await act(() => new Promise<void>((resolve) => setTimeout(resolve, 450)));
    await settle();
  }

  it("starts Cron at Weekdays, 09:00, and Interval at 1 hour", async () => {
    await openForm(task({}));
    const kind = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find((b) => b.textContent === label)!;

    await click(kind("Cron"));
    expect(schedule()).toEqual({ cron: "0 9 * * 1-5" });
    expect(control<HTMLSelectElement>("Repeats")?.value).toBe("weekdays");
    expect(control<HTMLInputElement>("Time")?.value).toBe("09:00");
    expect(expression()?.tagName).toBe("OUTPUT");
    expect(expression()?.textContent).toBe("0 9 * * 1-5");

    await click(kind("Interval"));
    expect(schedule()).toEqual({ interval: "1h" });
    expect(control<HTMLInputElement>("Every")?.value).toBe("1");
    expect(control<HTMLSelectElement>("Unit")?.value).toBe("h");
  });

  it.each(["0 9 * * MON-FRI", "0 9,17 * * *", "0 */2 * * *", "0 9 * 1 *", "@daily", "09 9 * * *"])(
    "opens %s in Custom and leaves it as it is",
    async (cron) => {
      await openForm(task({ cron }));
      expect(control<HTMLSelectElement>("Repeats")?.value).toBe("custom");
      expect(expression()?.tagName).toBe("INPUT");
      expect((expression() as HTMLInputElement).value).toBe(cron);
      expect(onChange).not.toHaveBeenCalled();
    },
  );

  it("opens an expression a frequency writes in that frequency, without writing it", async () => {
    await openForm(task({ cron: "30 8 * * 1,3,5" }));
    expect(control<HTMLSelectElement>("Repeats")?.value).toBe("weekly");
    expect(control<HTMLInputElement>("Time")?.value).toBe("08:30");
    expect(["Monday", "Tuesday", "Wednesday", "Friday"].map((d) => day(d).getAttribute("aria-pressed"))).toEqual(["true", "false", "true", "true"]);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("keeps Custom while the user types an expression a frequency writes", async () => {
    await openForm(task({ cron: "0 9 * * 1-5" }));
    await pick("Repeats", "custom");
    expect(onChange).not.toHaveBeenCalled();
    expect((expression() as HTMLInputElement).value).toBe("0 9 * * 1-5");

    await type(expression() as HTMLInputElement, "0 9 * * *");
    expect(schedule()).toEqual({ cron: "0 9 * * *" });
    expect(control<HTMLSelectElement>("Repeats")?.value).toBe("custom");
    expect(expression()?.tagName).toBe("INPUT");
  });

  it("writes each frequency's expression from its controls", async () => {
    await openForm(task({ cron: "0 9 * * 1-5" }));
    await type(control<HTMLInputElement>("Time")!, "18:30");
    expect(schedule()?.cron).toBe("30 18 * * 1-5");

    // The weekly days start from the weekdays, and the last day left cannot be removed.
    await pick("Repeats", "weekly");
    expect(schedule()?.cron).toBe("30 18 * * 1,2,3,4,5");
    await click(day("Wednesday"));
    await click(day("Sunday"));
    expect(schedule()?.cron).toBe("30 18 * * 0,1,2,4,5");
    for (const d of ["Sunday", "Monday", "Tuesday", "Thursday"]) await click(day(d));
    expect(schedule()?.cron).toBe("30 18 * * 5");
    expect(day("Friday").disabled).toBe(true);

    await pick("Repeats", "monthly");
    expect(schedule()?.cron).toBe("30 18 1 * *");
    expect(field("Day of the month")?.querySelector(".field__hint")).toBeNull();
    await pick("Day of the month", "31");
    expect(schedule()?.cron).toBe("30 18 31 * *");
    expect(field("Day of the month")?.querySelector(".field__hint")?.textContent).toBe("Months without day 31 are skipped.");

    await pick("Repeats", "daily");
    expect(schedule()?.cron).toBe("30 18 * * *");

    await pick("Repeats", "hourly");
    expect(schedule()?.cron).toBe("30 * * * *");
    await pick("Minute", "5");
    expect(schedule()?.cron).toBe("5 * * * *");

    await pick("Repeats", "minutes");
    expect([...control<HTMLSelectElement>("Every")!.options].map((o) => o.value)).toEqual(["5", "10", "15", "20", "30"]);
    expect(schedule()?.cron).toBe("*/15 * * * *");
    await pick("Every", "20");
    expect(schedule()?.cron).toBe("*/20 * * * *");
    expect(expression()?.textContent).toBe("*/20 * * * *");
  });

  it("describes the expression once the daemon accepted it, and the preview follows the controls", async () => {
    daemon.answers.set("/schedules/preview", { occurrences: ["2026-11-02T09:00:00Z"] } satisfies SchedulePreview);
    await openForm(task({ cron: "0 9 1 * MON" }));
    expect(description()).toBe("At 09:00, on day 1 of the month or on Mondays");

    await openForm(task({ cron: "0 9 * * 1-5" }));
    expect(description()).toBe("At 09:00, Monday through Friday");
    await pick("Repeats", "daily");
    // The daemon has not seen the new expression yet, so it is not described.
    expect(description()).toBeUndefined();
    await previewed();
    expect(daemon.requests.at(-1)?.body).toEqual({ schedule: { cron: "0 9 * * *" }, count: 5 });
    expect(description()).toBe("At 09:00");
  });

  it("describes the expression in the interface's language", async () => {
    daemon.answers.set("/schedules/preview", { occurrences: ["2026-11-02T09:00:00Z"] } satisfies SchedulePreview);
    await i18n.changeLanguage("pt-BR");
    try {
      await openForm(task({ cron: "0 9 * * 1-5" }));
      expect(description()).toBe("Às 09:00, de segunda-feira a sexta-feira");
    } finally {
      await i18n.changeLanguage("en-US");
    }
  });

  it("shows the daemon's message at the expression's field, and no description", async () => {
    daemon.answers.set("/schedules/preview", {
      occurrences: [],
      error: 'cron expression "0 9 * *" must have 5 fields: minute hour day-of-month month day-of-week',
      errorKey: "cron_field_count",
      errorParams: { cron: "0 9 * *" },
    } satisfies SchedulePreview);
    await openForm(task({ cron: "0 9 * *" }));
    expect(field("Cron expression (5 fields)")?.querySelector(".field__error")?.textContent).toBe(
      'cron expression "0 9 * *" must have 5 fields: minute hour day-of-month month day-of-week',
    );
    expect(description()).toBeUndefined();
    // The message shows once, at the field.
    expect(host!.querySelector(".preview .tone-danger")).toBeNull();
  });

  it("keeps the time zone's error in the preview", async () => {
    daemon.answers.set("/schedules/preview", {
      occurrences: [],
      error: 'unknown time zone "Mars/Olympus"',
      errorKey: "timezone_unknown",
      errorParams: { timezone: "Mars/Olympus" },
    } satisfies SchedulePreview);
    await openForm(task({ cron: "0 9 * * 1-5", timezone: "Mars/Olympus" }));
    expect(host!.querySelector(".preview .tone-danger")?.textContent).toBe('unknown time zone "Mars/Olympus"');
    expect(field("Cron expression (5 fields)")?.querySelector(".field__error")).toBeNull();
    expect(description()).toBeUndefined();
  });

  it("builds the interval from a number and a unit", async () => {
    await openForm(task({ interval: "90m" }));
    expect(control<HTMLInputElement>("Every")?.value).toBe("90");
    expect(control<HTMLSelectElement>("Unit")?.value).toBe("m");
    expect(onChange).not.toHaveBeenCalled();

    await pick("Unit", "d");
    expect(schedule()).toEqual({ interval: "90d" });
    await type(control<HTMLInputElement>("Every")!, "2");
    expect(schedule()).toEqual({ interval: "2d" });
    // A number the interval cannot take is not written.
    await type(control<HTMLInputElement>("Every")!, "0");
    expect(schedule()).toEqual({ interval: "2d" });
  });

  it("shows an interval the controls cannot show as text, with the daemon's message", async () => {
    daemon.answers.set("/schedules/preview", {
      occurrences: [],
      error: 'invalid interval "1.5h": use a whole number of minutes, hours, or days, such as 30m',
      errorKey: "interval_invalid",
      errorParams: { interval: "1.5h" },
    } satisfies SchedulePreview);
    await openForm(task({ interval: "1.5h" }));
    const text = control<HTMLInputElement>("Interval (30m, 2h, 1d)")!;
    expect(text.value).toBe("1.5h");
    expect(field("Interval (30m, 2h, 1d)")?.querySelector(".field__error")?.textContent).toBe(
      'invalid interval "1.5h": use a whole number of minutes, hours, or days, such as 30m',
    );
    expect(onChange).not.toHaveBeenCalled();

    await type(text, "90m");
    expect(schedule()).toEqual({ interval: "90m" });
    expect(control<HTMLInputElement>("Every")?.value).toBe("90");
  });
});
