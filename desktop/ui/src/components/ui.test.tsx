// @vitest-environment happy-dom
// The segmented controls: a single choice reads as a radio group, a multi-select as a group of toggle buttons, and an
// option can be disabled on its own in either.
import { act, useState, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SegmentedControl, SegmentedMultiSelect, type SegmentOption } from "./ui";

let root: Root | undefined;
let host: HTMLElement;

function render(node: ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(node));
}

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  host?.remove();
});

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

const days: SegmentOption<number>[] = [
  { value: 1, label: "Mon", ariaLabel: "Monday" },
  { value: 2, label: "Tue", ariaLabel: "Tuesday" },
  { value: 3, label: "Wed", ariaLabel: "Wednesday" },
];

function Multi({ onChange }: { onChange?: (v: number[]) => void }) {
  const [values, setValues] = useState<number[]>([1, 3]);
  return (
    <>
      <span id="days">Days</span>
      <SegmentedMultiSelect
        aria-labelledby="days"
        values={values}
        options={days.map((d) => ({ ...d, disabled: values.length === 1 && values.includes(d.value) }))}
        onChange={(v) => {
          setValues(v);
          onChange?.(v);
        }}
      />
    </>
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

describe("SegmentedMultiSelect", () => {
  it("renders a group named by its caption, whose options are toggle buttons pressed while chosen", () => {
    render(<Multi />);
    const group = host.querySelector(".segmented")!;
    expect(group.getAttribute("role")).toBe("group");
    expect(group.getAttribute("aria-labelledby")).toBe("days");
    expect(items().map((b) => b.hasAttribute("role"))).toEqual([false, false, false]);
    expect(items().map((b) => b.hasAttribute("aria-checked"))).toEqual([false, false, false]);
    expect(items().map((b) => b.getAttribute("aria-pressed"))).toEqual(["true", "false", "true"]);
    expect(items().map((b) => b.getAttribute("aria-label"))).toEqual(["Monday", "Tuesday", "Wednesday"]);
    expect(items().map((b) => b.classList.contains("segmented__item--active"))).toEqual([true, false, true]);
  });

  it("adds an option a click presses and removes one it releases", () => {
    const onChange = vi.fn();
    render(<Multi onChange={onChange} />);
    click("Tue");
    expect(onChange).toHaveBeenLastCalledWith([1, 3, 2]);
    expect(items().map((b) => b.getAttribute("aria-pressed"))).toEqual(["true", "true", "true"]);
    click("Mon");
    expect(onChange).toHaveBeenLastCalledWith([3, 2]);
    expect(items().map((b) => b.getAttribute("aria-pressed"))).toEqual(["false", "true", "true"]);
  });

  it("disables an option on its own, as the weekday picker keeps its last day", () => {
    const onChange = vi.fn();
    render(<Multi onChange={onChange} />);
    click("Mon");
    expect(items().map((b) => b.disabled)).toEqual([false, false, true]);
    click("Wed");
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(item("Wed").getAttribute("aria-pressed")).toBe("true");
  });
});
