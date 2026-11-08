// @vitest-environment happy-dom
// Drives the task form's declared parameters in a DOM, against a stand-in for the daemon's local API: each skill and
// agent the task cites that declares parameters gets a group of fields, and each value is written under its own name.
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

describe("task form declared parameters", () => {
  const catalog: Catalog = {
    agents: [
      { name: "reviewer", kind: "agent", source: "user", harnesses: ["claude-code"],
        parameters: [{ name: "strict", type: "boolean", description: "Refuse on any finding", required: false }] },
    ],
    skills: [
      { name: "release-notes", kind: "skill", source: "user", harnesses: ["claude-code"], parameters: [
        { name: "owner", type: "text", description: "Who owns the repository", required: true },
        { name: "limit", type: "number", description: "How many changes", default: 3, required: false },
      ] },
      { name: "digest", kind: "skill", source: "user", harnesses: ["claude-code"],
        parameters: [{ name: "owner", type: "text", description: "Who reads it", required: false }] },
      { name: "queues", kind: "skill", source: "user", harnesses: ["claude-code"], parameters: [
        { name: "repositories", type: "object", description: "The repositories", required: true, schema: { type: "object" } },
      ] },
      { name: "plain", kind: "skill", source: "user", harnesses: ["claude-code"], parameters: [] },
      { name: "pdf:pdf", kind: "skill", source: "user", harnesses: ["claude-code"] },
    ],
    problems: [],
    taskTypes: [],
  };
  let current: Doc = {};

  function Form({ doc }: { doc: Doc }) {
    const [value, setValue] = useState(doc);
    current = value;
    return <TaskForm doc={value} onChange={(d) => setValue((current = d))} mode="edit" catalog={catalog} harnesses={[]} errorPaths={new Set()} />;
  }

  async function openForm(doc: Doc) {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Form doc={doc} />));
    await settle();
  }

  const citing = (spec: Record<string, unknown> = {}): Doc => ({
    ...task({}),
    spec: { ...(task({}).spec as Record<string, unknown>), skills: ["release-notes", "digest", "queues", "plain", "pdf:pdf"], agents: ["reviewer"], ...spec },
  });
  const section = () => host!.querySelector("#section-parameters");
  async function type(el: HTMLInputElement | HTMLTextAreaElement, text: string) {
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    await act(async () => {
      Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, text);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  it("shows one group per skill and agent that declares parameters, below the task's own fields", async () => {
    await openForm(citing());
    const sections = [...host!.querySelectorAll("section.form-section")].map((s) => s.id);
    expect(sections.at(-1)).toBe("section-parameters");
    const groups = [...section()!.querySelectorAll("h4")].map((h) => h.textContent);
    expect(groups).toEqual(["Skill release-notes", "Skill digest", "Skill queues", "Agent reviewer"]);
    const labels = [...section()!.querySelectorAll(".field__label")].map((l) => l.textContent);
    expect(labels).toEqual(["owner *", "limit", "owner", "repositories *", "strict"]);
    const hints = [...section()!.querySelectorAll(".field__hint")].map((h) => h.textContent);
    expect(hints[1]).toBe("How many changes · number · Default: 3");
    expect(section()!.querySelector<HTMLInputElement>('input[type="number"]')!.placeholder).toBe("3");
  });

  it("writes each value under its own skill or agent, an object from its YAML field", async () => {
    await openForm(citing());
    const texts = [...section()!.querySelectorAll<HTMLInputElement>('input[type="text"]')];
    await type(texts[0], "ana");
    await type(texts[1], "bia");
    const area = section()!.querySelector<HTMLTextAreaElement>("textarea")!;
    await type(area, '{"octo/repo": {"public": true}}');
    await act(async () => section()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
    await settle();
    expect((current.spec as Record<string, unknown>).parameters).toEqual({
      skills: { "release-notes": { owner: "ana" }, digest: { owner: "bia" }, queues: { repositories: { "octo/repo": { public: true } } } },
      agents: { reviewer: { strict: true } },
    });

    // Text that does not parse yet keeps the last valid value and says so; clearing a field removes its value.
    await type(area, '{"octo/repo": ');
    expect(section()!.textContent).toContain("Not valid YAML in its JSON form yet");
    await type(texts[1], "");
    expect((current.spec as Record<string, unknown>).parameters).toEqual({
      skills: { "release-notes": { owner: "ana" }, queues: { repositories: { "octo/repo": { public: true } } } },
      agents: { reviewer: { strict: true } },
    });
  });

  it("shows no parameters for a task that cites nothing that declares any, or whose declarations cannot be read", async () => {
    await openForm(citing({ skills: ["plain", "pdf:pdf"], agents: [] }));
    expect(section()).toBeNull();
  });
});
