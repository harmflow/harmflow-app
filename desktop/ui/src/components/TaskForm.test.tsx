// @vitest-environment happy-dom
// Drives the task form in a DOM, against a stand-in for the daemon's local API. Its schedule preview asks for the
// occurrences in the task's time zone and lists them in that zone, not the host's. The
// daemon's own computation of the occurrences is itest › TestTaskLifecycle's. A new GitHub task with every section
// filled through its controls saves the document tests/integration/testdata/task-form-document.json holds, which
// itest › TestFormDocumentIsTheFile writes as a file (harmflow-app#299).
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { Catalog, Harness, SchedulePreview, Task } from "../api/types";
import i18n from "../i18n";
import type { Doc } from "../lib/taskForm";
import { TasksScreen, type TaskView } from "../screens/TasksScreen";
import { patchStateForTests } from "../state/store";
import { TaskForm } from "./TaskForm";

// The daemon as the client sees it: an answer per method and route, or per route alone, and every request it received
// with its body as JSON carries it, where a field left undefined is not sent.
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
      daemon.requests.push({ method, path, body: body === undefined ? undefined : JSON.parse(JSON.stringify(body)) });
      const key = [`${method} ${path}`, path].find((k) => daemon.answers.has(k));
      return key ? { status: 200, body: daemon.answers.get(key) } : { status: 404, body: null };
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

// A new GitHub task, every section filled through its controls, saves the document task-form-document.json holds, which
// itest › TestFormDocumentIsTheFile writes as an equal file: the two tests cover the way from the form to the file. That
// document opens back in its controls, and clearing a control removes its key from what is sent, so the default applies.
describe("task form document", () => {
  const FORM_DOCUMENT: Doc = JSON.parse(
    readFileSync(join(__dirname, "..", "..", "..", "..", "tests", "integration", "testdata", "task-form-document.json"), "utf8"),
  );
  const entry = (name: string, kind: "agent" | "skill", source: "builtin" | "user") => ({ name, kind, source, harnesses: ["claude-code", "codex"] });
  // The template's draft selects no agent, so it brings no agent, skill, or origin of its own and every list is filled
  // through its control. The catalog's entries let a whole name typed in a picker add the chip.
  const catalog: Catalog = {
    agents: [entry("issue-reviewer", "agent", "user")],
    skills: ["hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"].map((name) => entry(name, "skill", "builtin")),
    problems: [],
    taskTypes: [],
  };
  // The Effort selects list the efforts of the model typed.
  const harness = (id: Harness["id"], name: string, model: string): Harness => ({
    id,
    name,
    detected: true,
    installations: [],
    authentication: "valid",
    readiness: "ready",
    models: [{ id: model, efforts: ["low", "high"] }],
    efforts: ["low", "high"],
    lastCheckedAt: "2026-10-27T12:00:00Z",
  });
  const stored = (document: Doc): Task => ({
    id: "complete",
    name: "Every section",
    type: "github",
    enabled: false,
    revision: "r1",
    harness: "codex",
    warnings: [],
    document,
    effective: document,
    yaml: JSON.stringify(document, null, 2) + "\n",
    path: "complete.yaml",
    validation: { valid: true, errors: [], warnings: [] },
  });

  beforeAll(() => patchStateForTests({ catalog, harnesses: [harness("claude-code", "Claude Code", "sonnet"), harness("codex", "Codex", "gpt-5.6-terra")] }));
  afterAll(() => patchStateForTests({ catalog: undefined, harnesses: [], tasks: [], tasksLoaded: false }));

  async function openScreen(view: TaskView) {
    daemon.answers.set("POST /tasks", stored(FORM_DOCUMENT));
    daemon.answers.set("GET /tasks", []);
    daemon.answers.set("GET /tasks/complete", stored(FORM_DOCUMENT));
    daemon.answers.set("PUT /tasks/complete", stored(FORM_DOCUMENT));
    daemon.answers.set("/schedules/preview", { occurrences: [] } satisfies SchedulePreview);
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<TasksScreen view={view} onView={() => {}} onOpenExecution={() => {}} />));
    await settle();
  }

  // A group of checkboxes is a `div.field`; any other field is a `label.field`.
  const field = (label: string) =>
    [...host!.querySelectorAll<HTMLElement>(".field")].find((f) => f.querySelector(".field__label")?.textContent === label)!;
  const control = <E extends Element = HTMLInputElement>(label: string) => field(label).querySelector<E>("input, select, textarea, output")!;
  const toggle = (label: string) =>
    [...host!.querySelectorAll("label.toggle")].find((t) => t.querySelector(".toggle__label")?.textContent === label)!.querySelector("input")!;
  const boxes = (fieldLabel: string) => [...field(fieldLabel).querySelectorAll("label.checkbox")];
  const checked = (fieldLabel: string) => boxes(fieldLabel).filter((c) => c.querySelector("input")!.checked).map((c) => c.textContent);
  const chips = (label: string) => [...field(label).querySelectorAll(".name-chip > .mono")].map((c) => c.textContent);
  const repository = () => [...host!.querySelector(".grid-repo:not(.row-list__head)")!.querySelectorAll("input")];
  /** The preferred harness, then each fallback: its harness, model, and effort controls. */
  const profiles = () =>
    [...host!.querySelectorAll("#section-harness .grid-profile")].map((p) => {
      const [harness, effort] = p.querySelectorAll("select");
      return { harness: harness!, model: p.querySelector("input")!, effort: effort! };
    });
  const button = (label: string) => [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label)!;
  const sent = (method: string, path: string) => daemon.requests.filter((r) => r.method === method && r.path === path).map((r) => r.body);

  /** Types into a field or chooses an option, as React sees it. */
  async function enter(el: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement, value: string) {
    const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    await act(async () => {
      Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
    });
  }
  async function click(el: HTMLElement) {
    await act(async () => el.click());
  }
  /** Clicks one of a field's checkboxes. */
  async function tick(fieldLabel: string, label: string) {
    await click(boxes(fieldLabel).find((c) => c.textContent === label)!.querySelector("input")!);
  }

  it("sends, once every section is filled through its controls, the document the daemon writes as a file", async () => {
    // The daemon's draft of a GitHub template that selects no agent: one repository row for the editor to ask for.
    daemon.answers.set("POST /templates/hf-github-issues/draft", {
      document: {
        apiVersion: "harmflow/v1alpha1",
        kind: "Task",
        metadata: { id: "implement-github-issues", name: "Implement GitHub issues" },
        spec: {
          enabled: true,
          type: "github",
          harness: { preferred: { provider: "claude-code" } },
          github: { repositories: [{ baseBranch: "main" }] },
        },
      },
    });
    await openScreen({ kind: "new", template: "hf-github-issues" });

    // General. The name writes an identifier of its own until the identifier is typed.
    await enter(control("Name"), "Every section");
    expect(control("Identifier").value).toBe("every-section");
    await enter(control("Identifier"), "complete");
    await enter(control<HTMLTextAreaElement>("Description"), "Every section of the form, set.");
    await click(toggle("Enabled"));

    // Prompt.
    await enter(control<HTMLTextAreaElement>("Prompt"), "Implement the oldest issue labelled ready-for-agent.");
    await enter(control("Other agents (subagents)"), "issue-reviewer");
    for (const skill of ["hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"]) await enter(control("Skills"), skill);
    await enter(control("Workspace"), "~/harmflow-work");

    // The GitHub type: the template's repository row, then the queue and delivery.
    const [name, localPath, baseBranch] = repository();
    await enter(name!, "harmflow/example");
    await enter(localPath!, "${FORM_REPO}");
    await enter(baseBranch!, "develop");
    await enter(control<HTMLSelectElement>("Order"), "newest_first");
    await enter(control("Items discovered per cycle"), "5");
    await enter(control("Items processed per execution"), "2");
    await enter(control<HTMLSelectElement>("Resume priority"), "alternate");
    await enter(control("Resumptions before needs attention"), "1");
    await enter(control("Retries of a failed item"), "0");
    await enter(control("Test-repair attempts"), "0");

    // Harness: Codex preferred, then Claude Code, switched to only when a harness is unavailable.
    await enter(profiles()[0]!.harness, "codex");
    await enter(profiles()[0]!.model, "gpt-5.6-terra");
    await enter(profiles()[0]!.effort, "high");
    await click(button("Add fallback"));
    await enter(profiles()[1]!.harness, "claude-code");
    await enter(profiles()[1]!.model, "sonnet");
    await enter(profiles()[1]!.effort, "high");
    await tick("Fail over on", "Quota exhausted");
    await enter(control("Maximum switches"), "2");

    // Schedule: weekdays at 06:30 in São Paulo, skipping missed and overlapping firings.
    await click([...host!.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find((b) => b.textContent === "Cron")!);
    await enter(control("Time"), "06:30");
    await enter(control("Time zone"), "America/Sao_Paulo");
    await enter(control<HTMLSelectElement>("Missed runs"), "skip");
    await enter(control<HTMLSelectElement>("Overlapping firings"), "skip");

    // Permissions: the custom profile's lists show once it is chosen.
    await enter(control<HTMLSelectElement>("Profile"), "custom");
    await enter(control<HTMLSelectElement>("Network"), "deny");
    await click(toggle("Restrict writes to the workspace"));
    await enter(control<HTMLTextAreaElement>("Allowed tools"), "Read\nGrep");
    await enter(control<HTMLTextAreaElement>("Denied tools"), "WebFetch");
    await enter(control<HTMLTextAreaElement>("Writable paths"), "${FORM_REPO}");

    // Execution.
    await enter(control("Timeout (minutes), shared by all attempts"), "45");
    await enter(control("Grace before a cancellation is forced (seconds)"), "10");
    await enter(control("Rate-limit retries"), "2");
    await enter(control("First wait (seconds)"), "30");
    await enter(control("Longest wait (seconds)"), "600");

    // Notifications: off, and of the default events only operational failures kept, with failed tests added.
    await click(toggle("Native notifications"));
    for (const event of ["Execution completed", "Needs attention", "Draft PR created", "No alternative harness"]) await tick("Notify me about", event);
    await tick("Notify me about", "Tests failed");

    await click(button("Save"));
    await settle();
    // Strictly equal: a number sent as a string, or a key the form left in, fails.
    expect(sent("POST", "/tasks")).toStrictEqual([{ document: FORM_DOCUMENT }]);
  });

  it("shows the document in its controls, and sends no key of a cleared control, so its default applies", async () => {
    await openScreen({ kind: "edit", id: "complete" });
    expect(control("Identifier").disabled).toBe(true);
    const values = (...labels: string[]) => labels.map((label) => control(label).value);
    const [preferred, fallback] = profiles().map((p) => [p.harness.value, p.model.value, p.effort.value]);
    expect({
      general: [...values("Name", "Identifier", "Description"), toggle("Enabled").checked],
      prompt: [control("Prompt").value, chips("Other agents (subagents)"), chips("Skills"), control("Workspace").value],
      repository: repository().map((input) => input.value),
      queue: values(
        "Order",
        "Items discovered per cycle",
        "Items processed per execution",
        "Resume priority",
        "Resumptions before needs attention",
        "Retries of a failed item",
        "Test-repair attempts",
      ),
      harness: [preferred, fallback, checked("Fail over on"), control("Maximum switches").value],
      schedule: [...values("Repeats", "Time"), control("Cron expression (5 fields)").textContent, ...values("Time zone", "Missed runs", "Overlapping firings")],
      permissions: [...values("Profile", "Network"), toggle("Restrict writes to the workspace").checked, ...values("Allowed tools", "Denied tools", "Writable paths")],
      execution: values(
        "Timeout (minutes), shared by all attempts",
        "Grace before a cancellation is forced (seconds)",
        "Rate-limit retries",
        "First wait (seconds)",
        "Longest wait (seconds)",
      ),
      notifications: [toggle("Native notifications").checked, checked("Notify me about")],
    }).toEqual({
      general: ["Every section", "complete", "Every section of the form, set.", false],
      prompt: [
        "Implement the oldest issue labelled ready-for-agent.",
        ["issue-reviewer"],
        ["hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"],
        "~/harmflow-work",
      ],
      repository: ["harmflow/example", "${FORM_REPO}", "develop"],
      queue: ["newest_first", "5", "2", "alternate", "1", "0", "0"],
      harness: [["codex", "gpt-5.6-terra", "high"], ["claude-code", "sonnet", "high"], ["Harness unavailable"], "2"],
      schedule: ["weekdays", "06:30", "30 6 * * 1-5", "America/Sao_Paulo", "skip", "skip"],
      permissions: ["custom", "deny", false, "Read\nGrep", "WebFetch", "${FORM_REPO}"],
      execution: ["45", "10", "2", "30", "600"],
      notifications: [false, ["Operational failure", "Tests failed"]],
    });

    // One control of each section cleared. The two lists emptied show their defaults again, as the daemon applies them.
    await enter(control<HTMLTextAreaElement>("Description"), "");
    await enter(control("Workspace"), "");
    await enter(control("Items discovered per cycle"), "");
    await tick("Fail over on", "Harness unavailable");
    expect(checked("Fail over on")).toEqual(["Quota exhausted", "Harness unavailable"]);
    await enter(control("Time zone"), "");
    await enter(control<HTMLSelectElement>("Network"), "");
    await enter(control("Timeout (minutes), shared by all attempts"), "");
    for (const event of ["Operational failure", "Tests failed"]) await tick("Notify me about", event);
    expect(checked("Notify me about")).toEqual(["Execution completed", "Operational failure", "Needs attention", "Draft PR created", "No alternative harness"]);
    await click(button("Save"));
    await settle();

    const expected = structuredClone(FORM_DOCUMENT);
    for (const path of [
      ["metadata", "description"],
      // The workspace held its path alone, so it goes with it.
      ["spec", "workspace"],
      ["spec", "github", "queue", "maxDiscoveredPerCycle"],
      ["spec", "harness", "fallback", "on"],
      ["spec", "schedule", "timezone"],
      ["spec", "permissions", "network"],
      ["spec", "execution", "timeoutMinutes"],
      ["spec", "notifications", "native", "events"],
    ]) {
      const parent = path.slice(0, -1).reduce((node, key) => node[key] as Doc, expected);
      delete parent[path.at(-1)!];
    }
    expect(sent("PUT", "/tasks/complete")).toStrictEqual([{ document: expected, baseRevision: "r1" }]);
  });
});

// A field that holds a group of checkboxes is a group its caption names, not a label around them: the caption labels
// no checkbox, so clicking it changes none, and each checkbox is named by its own text alone (harmflow-app#438).
describe("task form checkbox groups", () => {
  const onChange = vi.fn();

  function Form({ doc }: { doc: Doc }) {
    const [value, setValue] = useState(doc);
    return (
      <TaskForm
        doc={value}
        onChange={(d) => {
          onChange(d);
          setValue(d);
        }}
        mode="edit"
        harnesses={[]}
        errorPaths={new Set()}
      />
    );
  }

  const script: Doc = {
    apiVersion: "harmflow/v1alpha1",
    kind: "Task",
    metadata: { id: "audit", name: "Audit" },
    spec: { type: "script", script: { command: ["/bin/date"] } },
  };

  /** The group whose name is the caption: the text of the element its aria-labelledby names. */
  const group = (caption: string) =>
    [...host!.querySelectorAll<HTMLElement>('[role="group"]')].find(
      (g) => document.getElementById(g.getAttribute("aria-labelledby") ?? "")?.textContent === caption,
    );
  /**
   * Every `<label>` around a control, nearest first. A browser names a checkbox by each of them whose first control it
   * is; happy-dom's `labels` lists the nearest alone, so the outer ones are found here.
   */
  const labelsAround = (el: Element) => {
    const found: HTMLLabelElement[] = [];
    for (let l = el.closest("label"); l; l = l.parentElement?.closest("label") ?? null) found.push(l);
    return found;
  };

  it.each([
    { caption: "Fail over on", doc: task({}), options: ["Quota exhausted", "Harness unavailable"] },
    {
      caption: "Notify me about",
      doc: task({}),
      options: [
        "Execution started",
        "Execution completed",
        "Operational failure",
        "Cancellation",
        "Needs attention",
        "Draft PR created",
        "Draft PR updated",
        "Tests failed",
        "Harness unavailable",
        "No alternative harness",
      ],
    },
    { caption: "Runs on", doc: script, options: ["macOS", "Linux", "Windows"] },
  ])("names the $caption group by its caption, each checkbox by its own text, and toggles none from the caption", async ({ caption, doc, options }) => {
    onChange.mockClear();
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Form doc={doc} />));
    await settle();

    const found = group(caption)!;
    expect(found).toBeDefined();
    const boxes = [...found.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')];
    // A checkbox's name is the text of its labels: its own label alone, with no name set beside it.
    expect(boxes.map((box) => labelsAround(box).map((l) => l.textContent))).toEqual(options.map((option) => [option]));
    for (const box of boxes) {
      expect(box.hasAttribute("aria-label") || box.hasAttribute("aria-labelledby")).toBe(false);
    }

    const states = boxes.map((box) => box.checked);
    await act(async () => document.getElementById(found.getAttribute("aria-labelledby")!)!.click());
    expect(boxes.map((box) => box.checked)).toEqual(states);
    expect(onChange).not.toHaveBeenCalled();
  });
});

// A task that runs a harness shows in its Execution section, after the rate-limit fields, whether it allows extra
// usage: off unless its document allows it. Off removes the key, so the default, which allows none, applies. A script
// task runs no harness and shows no such control (harmflow-app#300). What the daemon does with the setting is app ›
// TestExhaustedQuotaHoldsTheAttemptBack's, TestExtraUsageEndsTheAttempt's, and TestExtraUsageAllowed's.
describe("task form extra usage", () => {
  let current: Doc = {};

  function Form({ doc }: { doc: Doc }) {
    const [value, setValue] = useState(doc);
    current = value;
    return <TaskForm doc={value} onChange={(d) => setValue((current = d))} mode="edit" harnesses={[]} errorPaths={new Set()} />;
  }

  async function openForm(doc: Doc) {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Form doc={doc} />));
    await settle();
  }

  /** A task of a type that runs a harness, with the execution limits given. */
  const harnessTask = (type: "general" | "github", execution?: Doc): Doc => {
    const doc = task({});
    return { ...doc, spec: { ...(doc.spec as Doc), type, ...(execution ? { execution } : {}) } };
  };
  const script = (execution?: Doc): Doc => ({
    apiVersion: "harmflow/v1alpha1",
    kind: "Task",
    metadata: { id: "audit", name: "Audit" },
    spec: { type: "script", script: { command: ["/bin/date"] }, ...(execution ? { execution } : {}) },
  });

  const spec = () => current.spec as Doc;
  const section = () => host!.querySelector("#section-execution")!;
  const toggle = () => section().querySelector<HTMLInputElement>("label.toggle input");
  const label = () => section().querySelector("label.toggle")?.textContent;
  const hint = () => section().querySelector(".field--toggle .field__hint")?.textContent;
  const input = (text: string) =>
    [...section().querySelectorAll("label.field")].find((f) => f.querySelector(".field__label")?.textContent === text)!.querySelector("input")!;
  async function click(el: HTMLElement) {
    await act(async () => el.click());
  }
  async function type(el: HTMLInputElement, value: string) {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  it.each(["general", "github"] as const)("shows a %s task's extra usage off when its document sets nothing, after the rate-limit fields", async (kind) => {
    await openForm(harnessTask(kind));
    expect(toggle()?.checked).toBe(false);
    // Named by its label alone: the hint sits beside the toggle's label, not in it.
    expect(label()).toBe("Allow extra usage");
    expect(hint()).toBe(
      "Usage billed beyond what the harness's plan includes. Off, an attempt does not start on a harness whose plan is used up, and a Claude Code attempt that turns to extra usage is ended. On, neither check holds an attempt back.",
    );
    expect(input("Longest wait (seconds)").compareDocumentPosition(toggle()!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(spec().execution).toBeUndefined();
  });

  it.each([
    ["no other limit", undefined],
    ["another limit", { timeoutMinutes: 45 }],
  ])("writes true when turned on, and removes the key when turned off, with %s", async (_, execution) => {
    await openForm(harnessTask("general", execution));
    await click(toggle()!);
    expect(toggle()!.checked).toBe(true);
    expect(spec().execution).toStrictEqual({ ...execution, allowExtraUsage: true });

    await click(toggle()!);
    expect(toggle()!.checked).toBe(false);
    // An execution left with no limit goes with its last key.
    expect(spec().execution).toStrictEqual(execution);
  });

  it.each([true, false])("opens a task whose file sets allowExtraUsage: %s with the toggle at that value", async (allowed) => {
    await openForm(harnessTask("github", { allowExtraUsage: allowed }));
    expect(toggle()!.checked).toBe(allowed);
  });

  it("turns a task whose file sets allowExtraUsage: false on, then off without the key", async () => {
    await openForm(harnessTask("general", { allowExtraUsage: false }));
    await click(toggle()!);
    expect(spec().execution).toStrictEqual({ allowExtraUsage: true });
    await click(toggle()!);
    expect(spec().execution).toBeUndefined();
  });

  it("shows a script task no extra-usage control, and its document never carries the key", async () => {
    await openForm(script({ timeoutMinutes: 5 }));
    expect(section().querySelector(".toggle")).toBeNull();
    expect(section().textContent).not.toContain("Allow extra usage");
    await type(input("Timeout (minutes), shared by all attempts"), "10");
    expect(spec().execution).toStrictEqual({ timeoutMinutes: 10 });
  });

  it.each([
    ["pt-BR", "Permitir uso extra", "Uso cobrado além do que o plano do harness inclui."],
    ["es-ES", "Permitir uso extra", "Uso facturado más allá de lo que incluye el plan del harness."],
  ])("labels the toggle and its hint in %s", async (language, text, hintStart) => {
    await i18n.changeLanguage(language);
    try {
      await openForm(harnessTask("general"));
      expect(label()).toBe(text);
      expect(hint()?.startsWith(hintStart)).toBe(true);
    } finally {
      await i18n.changeLanguage("en-US");
    }
  });
});
