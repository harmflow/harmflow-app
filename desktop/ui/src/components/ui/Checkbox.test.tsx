// @vitest-environment happy-dom
// Checkbox, at its regular size and compact, and the only checkbox input of the components besides Toggle's.
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { act, useState, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import i18n from "../../i18n";
import { Checkbox } from "./Checkbox";

let root: Root | undefined;
let host: HTMLElement;

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  host?.remove();
});

function render(node: ReactNode): HTMLElement {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(node));
  return host;
}

const src = join(__dirname, "..", "..");

/** A checkbox that keeps its own state, as a screen holding it would. */
function Held({ compact, start = true }: { compact?: boolean; start?: boolean }) {
  const [checked, setChecked] = useState(start);
  return <Checkbox compact={compact} checked={checked} onChange={setChecked} label="Follow" />;
}

const label = () => host.querySelector<HTMLLabelElement>("label")!;
const input = () => host.querySelector<HTMLInputElement>("input[type='checkbox']")!;
const checkSize = () => host.querySelector(".checkbox__box svg")!.getAttribute("width");

describe("Checkbox", () => {
  it("draws a checkbox without compact as before: its class, box, and label, at the regular size", () => {
    render(<Held />);
    expect(label().className).toBe("checkbox");
    expect(host.querySelector(".checkbox__box")).not.toBeNull();
    expect(label().textContent).toBe("Follow");
    expect(checkSize()).toBe("12");
  });

  it("draws a compact checkbox with the compact modifier and a smaller check", () => {
    render(<Held compact />);
    expect(label().classList).toContain("checkbox");
    expect(label().classList).toContain("checkbox--compact");
    expect(host.querySelector(".checkbox__box")).not.toBeNull();
    expect(label().textContent).toBe("Follow");
    expect(checkSize()).toBe("10");
  });

  it("turns a compact checkbox off and on again with a click", () => {
    render(<Held compact />);
    expect(input().checked).toBe(true);
    act(() => input().click());
    expect(input().checked).toBe(false);
    act(() => input().click());
    expect(input().checked).toBe(true);
  });

  it("keeps the disabled modifier beside the compact one", () => {
    render(<Checkbox compact disabled checked={false} onChange={() => {}} label="Follow" />);
    expect([...label().classList]).toEqual(["checkbox", "checkbox--compact", "checkbox--disabled"]);
    expect(input().disabled).toBe(true);
  });

  // The Toggle primitive draws its switch on a checkbox input of its own, as it did beside Checkbox in one module.
  it("is the only checkbox input in the interface's components: no raw one is left outside the Checkbox and Toggle modules", () => {
    const primitives = ["components/ui/Checkbox.tsx", "components/ui/Toggle.tsx"];
    const sources = (readdirSync(src, { recursive: true }) as string[])
      .map((path) => path.split("\\").join("/"))
      .filter((file) => file.endsWith(".tsx") && !file.endsWith(".test.tsx"));
    expect(sources).toEqual(expect.arrayContaining([...primitives, "components/execution/Timeline.tsx"]));
    const raw = sources
      .filter((file) => !primitives.includes(file))
      .filter((file) => /type=["']checkbox["']/.test(readFileSync(join(src, file), "utf8")));
    expect(raw).toEqual([]);
  });
});
