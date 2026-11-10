// @vitest-environment happy-dom
// The shared primitives of ui.tsx:
// - The segmented controls: a single choice reads as a radio group, a multi-select as a group of toggle buttons, and an
//   option can be disabled on its own in either.
// - Button's link variant (harmflow-app#990), drawn with the look of a bare text-styled action and used by every one of
//   them.
// - The overlays: a Drawer is a modal dialog with the label it is given, and it closes on its close button, on a click
//   outside its card, and on the close shortcut; a Modal closes the same ways, through the same overlay. A click inside
//   the card, or another key, closes neither.
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { act, useState, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import { Button, Drawer, Modal, SegmentedControl, SegmentedMultiSelect, type SegmentOption } from "./ui";

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

function render(node: ReactNode) {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(node));
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

const src = join(__dirname, "..");
const appCss = readFileSync(join(src, "design", "app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

/** The declarations of the rule whose selector list is exactly `selectors`. */
function rule(selectors: string): Record<string, string> {
  const escaped = selectors.replace(/[.*+?^${}()|[\]\\]/g, "\\$&").replace(/,\s*/g, ",\\s*");
  const match = new RegExp(`(?:^|\\})\\s*${escaped}\\s*\\{([^}]*)\\}`).exec(appCss);
  if (!match) throw new Error(`no rule for ${selectors}`);
  return Object.fromEntries(
    match[1]
      .split(";")
      .map((d) => d.trim())
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(":")).trim(), d.slice(d.indexOf(":") + 1).trim()]),
  );
}

/** Every component and module file of the interface, outside the tests. */
function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name) ? [path] : [];
  });
}

describe("Button's link variant", () => {
  it("renders a button with the link variant's class, an extra class, and its children", () => {
    const onClick = vi.fn();
    render(
      <Button variant="link" className="mono" onClick={onClick}>
        #9
      </Button>,
    );
    const button = host.querySelector("button")!;
    expect(button.type).toBe("button");
    expect([...button.classList]).toEqual(["btn", "btn--link", "btn--md", "mono"]);
    expect(button.textContent).toBe("#9");
    act(() => button.click());
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("draws no padding, border, or background, the link color, and an underline on hover", () => {
    expect(rule(".btn--link, .link-like")).toMatchObject({
      padding: "0",
      border: "none",
      background: "none",
      color: "var(--hf-color-blue)",
      "font-size": "var(--hf-font-size-sm)",
    });
    expect(rule(".btn--link")).toMatchObject({ "font-weight": "inherit", "white-space": "inherit" });
    expect(rule(".btn--link:hover:not(:disabled)")).toMatchObject({
      background: "none",
      color: "var(--hf-color-cyan)",
      "text-decoration": "underline",
    });
    expect(rule(".link-like")).toEqual({ color: "inherit" });
  });

  it("is the only text-styled action: no .link rule and no button with the link class outside ui.tsx", () => {
    expect(appCss).not.toMatch(/(^|[\s,])\.link(?![\w-])/m);
    const bare = sourceFiles(src)
      .filter((file) => !file.endsWith(join("components", "ui.tsx")))
      .filter((file) => /className=["{`]+[^"}`]*\blink\b(?!-)/.test(readFileSync(file, "utf8")));
    expect(bare).toEqual([]);
  });
});

const dialog = () => host!.querySelector<HTMLElement>('[role="dialog"]')!;
const press = (key: string) => act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true })));
const mouseDown = (target: HTMLElement) => act(() => void target.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
const closeButton = () => dialog().querySelector<HTMLButtonElement>('button[aria-label="Close"]')!;

describe("Drawer", () => {
  it("is a modal dialog with its label, its close button, and what it holds in its card", () => {
    render(
      <Drawer label="Execution" onClose={() => {}}>
        <p className="content">Detail</p>
      </Drawer>,
    );
    expect(dialog().classList.contains("drawer")).toBe(true);
    expect(dialog().getAttribute("aria-modal")).toBe("true");
    expect(dialog().getAttribute("aria-label")).toBe("Execution");
    expect(dialog().querySelector(".drawer__card .drawer__close button")).toBe(closeButton());
    expect(dialog().querySelector(".drawer__card .content")!.textContent).toBe("Detail");
  });

  it("closes on its close button, on a click outside its card, and on the close shortcut", () => {
    const onClose = vi.fn();
    render(
      <Drawer label="Execution" onClose={onClose}>
        <p className="content">Detail</p>
      </Drawer>,
    );

    act(() => closeButton().click());
    expect(onClose).toHaveBeenCalledTimes(1);

    mouseDown(dialog());
    expect(onClose).toHaveBeenCalledTimes(2);

    press("Escape");
    expect(onClose).toHaveBeenCalledTimes(3);
  });

  it("stays open on a click inside its card and on another key", () => {
    const onClose = vi.fn();
    render(
      <Drawer label="Execution" onClose={onClose}>
        <p className="content">Detail</p>
      </Drawer>,
    );
    mouseDown(dialog().querySelector<HTMLElement>(".drawer__card")!);
    mouseDown(dialog().querySelector<HTMLElement>(".content")!);
    press("Enter");
    expect(onClose).not.toHaveBeenCalled();
  });

  it("stops listening for the close shortcut once it is gone", () => {
    const onClose = vi.fn();
    render(
      <Drawer label="Execution" onClose={onClose}>
        <p>Detail</p>
      </Drawer>,
    );
    act(() => root!.render(null));
    press("Escape");
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("Modal", () => {
  it("closes on the close shortcut and on a click outside its card, but not on a click inside it", () => {
    const onClose = vi.fn();
    render(
      <Modal title="Confirm" onClose={onClose}>
        <p className="content">Body</p>
      </Modal>,
    );
    expect(dialog().classList.contains("modal")).toBe(true);
    expect(dialog().getAttribute("aria-modal")).toBe("true");

    mouseDown(dialog().querySelector<HTMLElement>(".modal__card")!);
    expect(onClose).not.toHaveBeenCalled();

    mouseDown(dialog());
    expect(onClose).toHaveBeenCalledTimes(1);

    press("Escape");
    expect(onClose).toHaveBeenCalledTimes(2);

    act(() => closeButton().click());
    expect(onClose).toHaveBeenCalledTimes(3);
  });
});
