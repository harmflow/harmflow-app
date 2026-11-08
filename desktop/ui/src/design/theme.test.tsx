// @vitest-environment happy-dom
// A token's new value reaches every component that uses it, with no edit to the component: the components below are
// rendered with theme.css and app.css in the document, a token is changed on the document's root, as a theme would
// change it, and each component's computed style follows.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { EmptyState, SectionTitle } from "../components/ui";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLElement;
let root: Root;
let sheets: HTMLStyleElement[];

beforeEach(() => {
  sheets = ["theme.css", "app.css"].map((file) => {
    const style = document.createElement("style");
    style.textContent = readFileSync(join(__dirname, file), "utf8");
    document.head.appendChild(style);
    return style;
  });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() =>
    root.render(
      <>
        <SectionTitle>Tasks</SectionTitle>
        <EmptyState title="Nothing yet" />
      </>,
    ),
  );
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  for (const sheet of sheets) sheet.remove();
  document.documentElement.removeAttribute("style");
});

function element(selector: string): HTMLElement {
  const found = host.querySelector<HTMLElement>(selector);
  if (!found) throw new Error(`nothing rendered matches ${selector}`);
  return found;
}

/** A computed property of a rendered element, read again after each change. */
function computed(selector: string, property: string): string {
  return getComputedStyle(element(selector)).getPropertyValue(property).trim();
}

/** What the document computes for a value written on an element of its own, to compare with a component's. */
function resolved(property: string, value: string): string {
  const probe = document.createElement("span");
  probe.style.setProperty(property, value);
  document.body.appendChild(probe);
  const result = getComputedStyle(probe).getPropertyValue(property).trim();
  probe.remove();
  return result;
}

function setToken(token: string, value: string) {
  document.documentElement.style.setProperty(token, value);
}

describe("a token's change", () => {
  it.each([
    ["a color token", ".section-title__prompt", "color", "--hf-color-cyan", "#123456"],
    ["a font token", ".section-title__prompt", "font-family", "--hf-font-mono", '"Test Mono", monospace'],
    ["a spacing token", ".section-title h2", "gap", "--hf-space-2", "13px"],
    ["a spacing token in a shorthand", ".empty", "padding-top", "--hf-space-5", "27px"],
  ])("reaches the component that uses %s", (_, selector, property, token, value) => {
    const before = computed(selector, property);
    expect(before, `${selector} ${property} before ${token} changes`).not.toBe("");
    expect(before).not.toBe(resolved(property, value));

    setToken(token, value);

    expect(computed(selector, property), `${selector} ${property} after ${token}: ${value}`).toBe(resolved(property, value));
  });
});
