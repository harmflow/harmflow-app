// @vitest-environment happy-dom
// Drives the task form's harness profiles in a DOM, against a stand-in for the daemon's local API: a Codex profile's
// ephemeral session, and the installation a profile chooses.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { Catalog, Harness } from "../../api/types";
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

// A Codex profile, preferred or fallback, shows an Ephemeral session toggle beside Model and Effort, on unless its
// document sets ephemeral: false. Off writes false and shows the note of what the kept session holds, the text the
// daemon's validation warning gives; on removes the key, so the YAML writes it only when it differs from the default. A
// Claude Code profile, which cannot keep its session, shows no toggle (harmflow-app#473). What the daemon does with the
// setting is codex › TestPrepareEphemeralSession's and app › TestEphemeralFalseNeedsAHarnessThatKeepsSessions's.
describe("task form ephemeral session", () => {
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

  const NOTE =
    "the kept session holds everything the session read and printed, command output included, and stays under the Codex home, outside HarmFlow's log retention, until you delete it";
  /** A task whose preferred profile and fallback candidates are the ones given. */
  const withProfiles = (preferred: Doc, ...candidates: Doc[]): Doc => {
    const doc = task({});
    return { ...doc, spec: { ...(doc.spec as Doc), harness: { preferred, ...(candidates.length ? { fallback: { candidates } } : {}) } } };
  };
  const harness = () => (current.spec as Doc).harness as { preferred: Doc; fallback?: { candidates: Doc[] } };
  const profiles = () => [...host!.querySelectorAll("#section-harness .grid-profile")];
  const toggle = (i: number) => profiles()[i]!.querySelector<HTMLInputElement>("label.toggle input");
  const label = (i: number) => profiles()[i]!.querySelector("label.toggle")?.textContent;
  const note = (i: number) => profiles()[i]!.querySelector(".grid-profile__note")?.textContent;
  async function click(el: HTMLElement) {
    await act(async () => el.click());
  }

  it("shows a Codex profile's toggle on beside Model and Effort, and writes no key", async () => {
    await openForm(withProfiles({ provider: "codex" }));
    expect(label(0)).toBe("Ephemeral session");
    expect(toggle(0)!.checked).toBe(true);
    const effort = [...profiles()[0]!.querySelectorAll(".field")].find((f) => f.querySelector(".field__label")?.textContent === "Effort")!;
    expect(effort.compareDocumentPosition(toggle(0)!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(note(0)).toBeUndefined();
    expect(harness().preferred).toStrictEqual({ provider: "codex" });
  });

  it("writes false and shows the note when turned off, and removes the key when turned on", async () => {
    await openForm(withProfiles({ provider: "codex", model: "gpt-x" }));
    await click(toggle(0)!);
    expect(toggle(0)!.checked).toBe(false);
    expect(harness().preferred).toStrictEqual({ provider: "codex", model: "gpt-x", ephemeral: false });
    expect(note(0)).toBe(NOTE);

    await click(toggle(0)!);
    expect(toggle(0)!.checked).toBe(true);
    expect(harness().preferred).toStrictEqual({ provider: "codex", model: "gpt-x" });
    expect(note(0)).toBeUndefined();
  });

  it("opens a fallback whose file sets ephemeral: false off, with the note, and turns it on without the key", async () => {
    await openForm(withProfiles({ provider: "claude-code" }, { provider: "codex", ephemeral: false }));
    expect(toggle(0)).toBeNull();
    expect(toggle(1)!.checked).toBe(false);
    expect(note(1)).toBe(NOTE);
    await click(toggle(1)!);
    expect(harness().fallback!.candidates).toStrictEqual([{ provider: "codex" }]);
  });

  it("shows a Claude Code profile no toggle", async () => {
    await openForm(withProfiles({ provider: "claude-code" }));
    expect(toggle(0)).toBeNull();
    expect(profiles()[0]!.textContent).not.toContain("Ephemeral session");
  });

  it.each([
    ["pt-BR", "Sessão efêmera", "a sessão mantida guarda tudo"],
    ["es-ES", "Sesión efímera", "la sesión conservada guarda todo"],
  ])("labels the toggle and its note in %s", async (language, text, noteStart) => {
    await i18n.changeLanguage(language);
    try {
      await openForm(withProfiles({ provider: "codex", ephemeral: false }));
      expect(label(0)).toBe(text);
      expect(note(0)?.startsWith(noteStart)).toBe(true);
    } finally {
      await i18n.changeLanguage("en-US");
    }
  });
});

// harmflow-app#475: a profile offers the installations of the harness it chose, one per Codex home, and its Model and
// Effort lists are those of the installation chosen, or of the one in use while none is.
describe("task form installation", () => {
  let current: Doc = {};
  const codexHome = (id: string, model: string, effort: string): Harness => ({
    id: "codex",
    name: "Codex",
    detected: true,
    installations: ["/opt/homebrew/bin/codex"],
    installationId: id,
    home: `/Users/someone/${id.slice(2)}`,
    authentication: "valid",
    readiness: "ready",
    models: [{ id: "default" }, { id: model, efforts: ["default", effort] }],
    efforts: ["default", effort],
    lastCheckedAt: "2026-11-08T09:00:00Z",
  });
  const first = { ...codexHome("~/.codex", "model-a", "low"), skills: ["pdf"] };
  const second = { ...codexHome("~/.codex-b", "model-b", "high"), skills: ["triage"] };
  const harnesses: Harness[] = [
    { ...codexHome("~/.codex-b", "model-b", "high"), homes: [first, second] },
    { id: "claude-code", name: "Claude Code", detected: true, installations: [], authentication: "valid", readiness: "ready", models: [{ id: "sonnet" }], efforts: ["default"], lastCheckedAt: "2026-11-08T09:00:00Z" },
  ];
  // The catalog lists the skills of the installation in use, ~/.codex-b, and Claude Code's.
  const catalog: Catalog = {
    agents: [],
    skills: [
      { name: "hf-github-draft-pr", kind: "skill", source: "builtin", harnesses: ["claude-code", "codex"] },
      { name: "review-notes", kind: "skill", source: "user", harnesses: ["claude-code"] },
      { name: "triage", kind: "skill", source: "user", harnesses: ["codex"] },
    ],
    taskTypes: [],
    problems: [],
  };

  function Form({ doc }: { doc: Doc }) {
    const [value, setValue] = useState(doc);
    current = value;
    return (
      <TaskForm doc={value} onChange={(d) => setValue((current = d))} mode="edit" catalog={catalog} harnesses={harnesses} errorPaths={new Set()} />
    );
  }

  async function openForm(doc: Doc) {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Form doc={doc} />));
    await settle();
  }

  const withProfiles = (preferred: Doc, ...candidates: Doc[]): Doc => {
    const doc = task({});
    return { ...doc, spec: { ...(doc.spec as Doc), harness: { preferred, ...(candidates.length ? { fallback: { candidates } } : {}) } } };
  };
  const preferred = () => ((current.spec as Doc).harness as { preferred: Doc }).preferred;
  const profiles = () => [...host!.querySelectorAll("#section-harness .grid-profile")];
  const fieldOf = (i: number, label: string) =>
    [...profiles()[i]!.querySelectorAll(".field")].find((f) => f.querySelector(".field__label")?.textContent === label);
  const installation = (i: number) => fieldOf(i, "Installation")?.querySelector("select");
  const options = (el: Element | null | undefined) => [...(el?.querySelectorAll("option") ?? [])].map((o) => o.getAttribute("value"));
  const models = (i: number) => options(profiles()[i]!.querySelector("datalist"));
  const efforts = (i: number) => options(fieldOf(i, "Effort")?.querySelector("select"));

  it("lists the chosen harness's installations, and the Model and Effort lists follow the one chosen", async () => {
    await openForm(withProfiles({ provider: "codex" }, { provider: "claude-code" }));
    expect(options(installation(0))).toStrictEqual(["", "~/.codex", "~/.codex-b"]);
    expect(installation(0)!.options[0]!.textContent).toBe("In use (~/.codex-b)");
    expect(installation(1)).toBeUndefined();
    expect(models(0)).toStrictEqual(["default", "model-b"]);
    expect(efforts(0)).toStrictEqual(["", "default", "high"]);

    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(installation(0), "~/.codex");
      installation(0)!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    expect(preferred()).toStrictEqual({ provider: "codex", installation: "~/.codex" });
    expect(models(0)).toStrictEqual(["default", "model-a"]);
    expect(efforts(0)).toStrictEqual(["", "default", "low"]);
  });

  it("offers the skills of the installation chosen, or of the one in use while none is", async () => {
    const skills = () =>
      options(
        [...host!.querySelectorAll(".field")]
          .find((f) => f.querySelector(".field__label")?.textContent === "Skills")
          ?.querySelector("datalist"),
      );
    await openForm(withProfiles({ provider: "codex" }));
    expect(skills()).toStrictEqual(["hf-github-draft-pr", "triage"]);

    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(installation(0), "~/.codex");
      installation(0)!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    expect(skills()).toStrictEqual(["hf-github-draft-pr", "pdf"]);

    const provider = fieldOf(0, "AI tool")!.querySelector("select")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(provider, "claude-code");
      provider.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    expect(skills()).toStrictEqual(["hf-github-draft-pr", "review-notes", "triage"]);
  });

  // harmflow-app#1030: a fallback candidate offers the installations of the harness it chose, as the preferred profile
  // does, and its Model and Effort lists follow its own choice.
  it("offers a Codex fallback candidate its installations, and its Model and Effort lists follow its choice", async () => {
    const candidates = () => ((current.spec as Doc).harness as { fallback: { candidates: Doc[] } }).fallback.candidates;
    await openForm(withProfiles({ provider: "claude-code" }, { provider: "codex" }));
    expect(installation(0)).toBeUndefined();
    expect(options(installation(1))).toStrictEqual(["", "~/.codex", "~/.codex-b"]);
    expect(installation(1)!.options[0]!.textContent).toBe("In use (~/.codex-b)");
    expect(models(1)).toStrictEqual(["default", "model-b"]);
    expect(efforts(1)).toStrictEqual(["", "default", "high"]);

    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(installation(1), "~/.codex");
      installation(1)!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    expect(models(1)).toStrictEqual(["default", "model-a"]);
    expect(efforts(1)).toStrictEqual(["", "default", "low"]);
    expect(candidates()).toStrictEqual([{ provider: "codex", installation: "~/.codex" }]);
    expect(preferred()).toStrictEqual({ provider: "claude-code" });
    expect(installation(0)).toBeUndefined();
  });

  it("keeps an installation this machine lacks, saying so, and drops it when the harness changes", async () => {
    await openForm(withProfiles({ provider: "codex", installation: "~/.gone" }));
    expect(options(installation(0))).toStrictEqual(["", "~/.codex", "~/.codex-b", "~/.gone"]);
    expect(installation(0)!.value).toBe("~/.gone");
    expect(installation(0)!.selectedOptions[0]!.textContent).toBe("~/.gone — not on this machine");

    const provider = fieldOf(0, "AI tool")!.querySelector("select")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(provider, "claude-code");
      provider.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    expect(preferred()).toStrictEqual({ provider: "claude-code" });
    expect(installation(0)).toBeUndefined();
  });
});
