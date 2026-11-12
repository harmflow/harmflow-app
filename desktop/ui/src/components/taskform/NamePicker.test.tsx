// @vitest-environment happy-dom
// Drives the task form's name picker in a DOM, against a stand-in for the daemon's local API.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { Catalog } from "../../api/types";
import i18n from "../../i18n";
import type { Doc } from "../../lib/taskForm";
import { TaskForm } from "../TaskForm";

// The daemon as the client sees it: an answer per method and route, or per route alone, and every request it received
// with its body as JSON carries it, where a field left undefined is not sent.
const daemon = vi.hoisted(() => ({
  answers: new Map<string, unknown>(),
  requests: [] as { method: string; path: string; body: unknown }[],
}));
vi.mock("../../api/transport", () => ({
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

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
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

// A group of checkboxes is a `div.field`; any other field is a `label.field`.
const field = (label: string) =>
  [...host!.querySelectorAll<HTMLElement>(".field")].find((f) => f.querySelector(".field__label")?.textContent === label)!;
/** A name picker's remove buttons, one per chip, each the last child of its chip. */
const removeButtons = (within: HTMLElement) => [...within.querySelectorAll<HTMLButtonElement>('button[aria-label="Remove"]')];
async function click(el: HTMLElement) {
  await act(async () => el.click());
}

// A name picker shows each picked name as a removable chip: its source, a name the catalog lacks in the warning tone,
// and the catalog's description as its title. Its remove button takes the name out of the document.
describe("task form name picker", () => {
  const catalog: Catalog = {
    agents: [],
    skills: [{ name: "review-notes", kind: "skill", source: "user", harnesses: ["claude-code"], description: "Writes the review's notes" }],
    problems: [],
    taskTypes: [],
  };
  let current: Doc = {};

  function Form({ doc }: { doc: Doc }) {
    const [value, setValue] = useState(doc);
    current = value;
    return <TaskForm doc={value} onChange={(d) => setValue((current = d))} mode="edit" catalog={catalog} harnesses={[]} errorPaths={new Set()} />;
  }

  const picked = () =>
    removeButtons(field("Skills")).map((remove) => {
      const chip = remove.parentElement!;
      return {
        name: chip.querySelector(".mono")!.textContent,
        source: chip.querySelector(".name-picker__source")!.textContent,
        title: chip.getAttribute("title"),
        missing: chip.classList.contains("chip--warning"),
      };
    });

  it("removes a name from the task when its chip's remove button is clicked", async () => {
    const doc = task({});
    (doc.spec as Doc).skills = ["review-notes", "gone"];
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Form doc={doc} />));
    await settle();

    expect(picked()).toEqual([
      { name: "review-notes", source: "Yours", title: "Writes the review's notes", missing: false },
      { name: "gone", source: "Not found", title: null, missing: true },
    ]);

    await click(removeButtons(field("Skills"))[0]!);
    expect(current.spec).toMatchObject({ skills: ["gone"] });
    expect(picked().map((p) => p.name)).toEqual(["gone"]);
  });
});
