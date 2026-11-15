// @vitest-environment happy-dom
// The New task catalog with a template file that fails validation (harmflow-app#1061): the catalog lists the file below
// the offered templates, named by its file, with each of the daemon's errors and its line and column when the daemon
// gives them; it cannot be picked, and the valid templates stay offered. The search and filters leave it listed.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { LocalizedText, Template } from "../api/types";
import i18n from "../i18n";
import { patchStateForTests } from "../state/store";
import { TemplateButtons } from "./Templates";

vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async () => ({ status: 404, body: null }),
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

const named = (text: string): LocalizedText => ({ "pt-BR": text, "en-US": text, "es-ES": text });

const valid: Template = {
  id: "weekly-notes",
  source: "user",
  path: "/data/templates/weekly-notes.yaml",
  type: "general",
  name: named("Weekly notes"),
  description: named("Sums up the week."),
  validation: { valid: true, errors: [], warnings: [] },
};

// A file the user broke: the daemon lists it with what its file gives, an identifier and a path, and its errors.
const broken: Template = {
  id: "standup",
  source: "user",
  path: "/data/templates/standup.yaml",
  validation: {
    valid: false,
    errors: [
      { message: "yaml: line 4: mapping values are not allowed in this context", line: 4, column: 9 },
      { message: "spec.prompt is required", path: "/spec/prompt" },
    ],
    warnings: [],
  },
};

let root: Root | undefined;
let host: HTMLElement;
const onPick = vi.fn<(template: string) => void>();

function render(templates: Template[]) {
  patchStateForTests({ templates });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<TemplateButtons onPick={onPick} />));
}

const offered = () => [...host.querySelectorAll<HTMLButtonElement>("button.template")].map((b) => b.querySelector("strong")!.textContent);
const invalidEntries = () => [...host.querySelectorAll<HTMLElement>(".template-invalid")];

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  host?.remove();
  onPick.mockReset();
  patchStateForTests({ templates: undefined });
});

describe("the New task catalog's invalid template files", () => {
  it("lists the invalid file with its file, line, and column, not choosable, and keeps the valid one offered", () => {
    render([broken, valid]);
    expect(offered()).toEqual(["Weekly notes"]);

    const [entry, ...rest] = invalidEntries();
    expect(rest).toEqual([]);
    expect(entry.closest("button")).toBeNull();
    expect(entry.querySelector("button")).toBeNull();
    expect(entry.getAttribute("aria-disabled")).toBe("true");
    expect(entry.querySelector("strong")!.textContent).toBe("standup.yaml");
    expect([...entry.querySelectorAll(".template-invalid__errors li")].map((n) => n.textContent)).toEqual([
      "yaml: line 4: mapping values are not allowed in this context — line 4, column 9",
      "spec.prompt is required",
    ]);
    expect(host.textContent).toContain("These template files have errors and cannot start a task:");

    act(() => entry.click());
    expect(onPick).not.toHaveBeenCalled();
    act(() => host.querySelector<HTMLButtonElement>("button.template")!.click());
    expect(onPick).toHaveBeenCalledWith("weekly-notes");
  });

  it("lists the invalid file below the offered templates, and keeps it listed whatever the search", () => {
    render([broken, valid]);
    const catalog = host.querySelector(".template-catalog")!;
    const order = [...catalog.querySelectorAll(".template, .template-invalid")].map((n) => n.className.split(" ")[0]);
    expect(order).toEqual(["template", "template-invalid"]);

    const search = host.querySelector<HTMLInputElement>('input[type="search"]')!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(search, "nothing matches this");
      search.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(offered()).toEqual([]);
    expect(invalidEntries().map((e) => e.querySelector("strong")!.textContent)).toEqual(["standup.yaml"]);
  });

  it("lists the invalid file when no template is valid, saying none is offered", () => {
    render([broken]);
    expect(offered()).toEqual([]);
    expect(host.textContent).toContain("HarmFlow found no templates to start a task from.");
    expect(invalidEntries().map((e) => e.querySelector("strong")!.textContent)).toEqual(["standup.yaml"]);
  });

  it("lists nothing apart when every template is valid", () => {
    render([valid]);
    expect(host.querySelector(".templates-invalid")).toBeNull();
  });
});
