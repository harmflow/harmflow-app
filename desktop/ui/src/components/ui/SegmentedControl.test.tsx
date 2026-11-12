// @vitest-environment happy-dom
// SegmentedControl: a single choice reads as a radio group, and an option can be disabled on its own.
import { act, useState, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { SegmentedControl } from "./SegmentedControl";
import type { SegmentOption } from "./SegmentedItem";

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

const items = () => [...host.querySelectorAll<HTMLButtonElement>(".segmented__item")];
const item = (label: string) => items().find((b) => b.textContent === label)!;
const click = (label: string) => act(() => item(label).click());

const views: SegmentOption<"cost" | "tokens">[] = [
  { value: "cost", label: "Cost" },
  { value: "tokens", label: "Tokens" },
];

function Single({ options = views, onChange }: { options?: SegmentOption<"cost" | "tokens">[]; onChange?: (v: string) => void }) {
  const [value, setValue] = useState<"cost" | "tokens">("cost");
  return (
    <SegmentedControl
      aria-label="View"
      value={value}
      options={options}
      onChange={(v) => {
        setValue(v);
        onChange?.(v);
      }}
    />
  );
}

describe("SegmentedControl", () => {
  it("renders a named radio group whose options are radios, the chosen one checked and drawn active", () => {
    render(<Single />);
    const group = host.querySelector(".segmented")!;
    expect(group.getAttribute("role")).toBe("radiogroup");
    expect(group.getAttribute("aria-label")).toBe("View");
    expect(items().map((b) => b.getAttribute("role"))).toEqual(["radio", "radio"]);
    expect(items().map((b) => b.getAttribute("aria-checked"))).toEqual(["true", "false"]);
    expect(items().map((b) => b.hasAttribute("aria-pressed"))).toEqual([false, false]);
    expect(items().map((b) => b.classList.contains("segmented__item--active"))).toEqual([true, false]);
  });

  it("reports the option clicked, and checks it once its value comes back", () => {
    const onChange = vi.fn();
    render(<Single onChange={onChange} />);
    click("Tokens");
    expect(onChange).toHaveBeenCalledWith("tokens");
    expect(items().map((b) => b.getAttribute("aria-checked"))).toEqual(["false", "true"]);
    expect(item("Tokens").classList.contains("segmented__item--active")).toBe(true);
  });

  it("disables an option on its own, which a click then does not choose", () => {
    const onChange = vi.fn();
    render(<Single options={[{ ...views[0] }, { ...views[1], disabled: true }]} onChange={onChange} />);
    expect(items().map((b) => b.disabled)).toEqual([false, true]);
    click("Tokens");
    expect(onChange).not.toHaveBeenCalled();
    expect(item("Cost").getAttribute("aria-checked")).toBe("true");
  });
});
