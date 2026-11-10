// @vitest-environment happy-dom
// The shared primitives of ui.tsx:
// - The segmented controls: a single choice reads as a radio group, a multi-select as a group of toggle buttons, and an
//   option can be disabled on its own in either.
// - Button's link variant (harmflow-app#990), drawn with the look of a bare text-styled action and used by every one of
//   them.
// - The overlays: a Drawer is a modal dialog with the label it is given, and it closes on its close button, on a click
//   outside its card, and on the close shortcut; a Modal closes the same ways, through the same overlay. A click inside
//   the card, or another key, closes neither.
// - The ListRow primitive's slots, in the order a row shows them, and what a click on it does: a row with an action is
//   a button, one without is not, and both show the same slots.
// - Chip, with and without its remove button.
// - Checkbox, at its regular size and compact.
// - TextInput's mono and invalid props.
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { act, useState, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import { Button, Checkbox, Chip, Drawer, ListRow, ListRows, Modal, SegmentedControl, SegmentedMultiSelect, TextInput, type SegmentOption } from "./ui";

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

/** Each slot of a row by its class, in document order, with its text. */
const slots = (row: Element) =>
  [...row.querySelectorAll(":scope > *, .list-row__text > *")].map((el) => [el.className, el.textContent]);

describe("ListRow", () => {
  it("shows the leading mark, the title over its two lines, the aside, and the trailing time, in that order", () => {
    const row = render(
      <ListRow
        leading={<span className="mark">M</span>}
        title="Nightly sync"
        secondary="Task nightly"
        meta="Scheduled · in 2 min"
        aside={<span className="chip">Waiting</span>}
        trailing="5 min ago"
      />,
    ).firstElementChild!;
    expect(slots(row)).toEqual([
      ["mark", "M"],
      ["list-row__text", "Nightly syncTask nightlyScheduled · in 2 min"],
      ["list-row__title", "Nightly sync"],
      ["list-row__sub", "Task nightly"],
      ["list-row__meta", "Scheduled · in 2 min"],
      ["chip", "Waiting"],
      ["list-row__trail", "5 min ago"],
    ]);
  });

  it("leaves out the slots it is not given, and boxes an icon in the leading slot", () => {
    const row = render(<ListRow icon="clock" title="Only a title" />).firstElementChild!;
    expect(slots(row).map(([className]) => className)).toEqual(["list-row__icon", "list-row__text", "list-row__title"]);
    expect(row.querySelector(".list-row__icon svg")).not.toBeNull();
  });

  it("is a button that calls its action once per click, and with no action is no button", () => {
    const onClick = vi.fn();
    const clickable = render(<ListRow title="Open me" trailing="now" onClick={onClick} />).firstElementChild as HTMLElement;
    expect(clickable.tagName).toBe("BUTTON");
    expect(clickable.getAttribute("type")).toBe("button");
    act(() => clickable.click());
    expect(onClick).toHaveBeenCalledTimes(1);
    const shown = slots(clickable);
    act(() => root?.unmount());
    host?.remove();

    const still = render(<ListRow title="Open me" trailing="now" />).firstElementChild!;
    expect(still.tagName).toBe("DIV");
    expect(still.querySelector("button")).toBeNull();
    expect(slots(still)).toEqual(shown);
  });

  it("marks a card, a tone, a row aligned at its top, and a secondary line that wraps", () => {
    const row = render(<ListRow title="Failed" secondary="/a/long/path" wrap card tone="error" alignTop />).firstElementChild!;
    expect([...row.classList]).toEqual(["list-row", "list-row--card", "list-row--error", "list-row--top"]);
    expect([...row.querySelector(".list-row__sub")!.classList]).toEqual(["list-row__sub", "list-row__sub--wrap"]);
  });

  it("is held in a list that spaces cards apart when loose", () => {
    const list = render(
      <ListRows loose className="extra">
        <li>
          <ListRow title="One" />
        </li>
      </ListRows>,
    ).firstElementChild!;
    expect(list.tagName).toBe("UL");
    expect([...list.classList]).toEqual(["list-rows", "list-rows--loose", "extra"]);
    expect(list.querySelectorAll(":scope > li > .list-row")).toHaveLength(1);
  });
});

const chip = () => host.querySelector<HTMLElement>(".chip")!;

describe("Chip", () => {
  it("draws its tone and title with no button when it is not removable", () => {
    render(
      <Chip tone="success" title="Ready">
        Claude Code
      </Chip>,
    );
    expect([...chip().classList]).toEqual(["chip", "chip--success"]);
    expect(chip().getAttribute("title")).toBe("Ready");
    expect(chip().textContent).toBe("Claude Code");
    expect(chip().querySelector("button")).toBeNull();
  });

  it("ends in a remove button with its label and the x icon, which calls onRemove", () => {
    const onRemove = vi.fn();
    render(
      <Chip tone="violet" onRemove={onRemove} removeLabel="Remove">
        review-notes
      </Chip>,
    );
    expect(chip().classList.contains("chip--removable")).toBe(true);
    const remove = chip().querySelector<HTMLButtonElement>("button")!;
    expect(remove.getAttribute("aria-label")).toBe("Remove");
    expect(remove.type).toBe("button");
    expect(chip().lastElementChild).toBe(remove);
    expect(remove.querySelector("svg")?.getAttribute("data-icon")).toBe("x");

    act(() => remove.click());
    expect(onRemove).toHaveBeenCalledTimes(1);
  });
});

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

  it("is the only checkbox input in the interface's components: no raw one is left outside ui.tsx", () => {
    const sources = (readdirSync(src, { recursive: true }) as string[])
      .map((path) => path.split("\\").join("/"))
      .filter((file) => file.endsWith(".tsx") && !file.endsWith(".test.tsx") && file !== "components/ui.tsx");
    expect(sources).toContain("components/execution/Timeline.tsx");
    const raw = sources.filter((file) => /type=["']checkbox["']/.test(readFileSync(join(src, file), "utf8")));
    expect(raw).toEqual([]);
  });
});

/** The text field `node` draws. */
function textInput(node: ReactNode) {
  render(node);
  return host.querySelector("input")!;
}

// `mono` and `invalid` add their classes and nothing else, so a field without them is drawn as before.
describe("TextInput", () => {
  it("draws a plain text field without the props", () => {
    const el = textInput(<TextInput value="" onChange={() => {}} />);
    expect(el.className).toBe("input");
    expect(el.type).toBe("text");
    expect(el.hasAttribute("aria-invalid")).toBe(false);
  });

  it("adds mono for mono and input--error for invalid, merged with className", () => {
    const el = textInput(<TextInput mono invalid className="filters__search" value="" onChange={() => {}} />);
    expect(el.className).toBe("input mono input--error filters__search");
    expect(el.hasAttribute("mono")).toBe(false);
    expect(el.hasAttribute("invalid")).toBe(false);
    expect(el.hasAttribute("aria-invalid")).toBe(false);
  });

  it("draws no error class when invalid is false", () => {
    const el = textInput(<TextInput mono invalid={false} value="" onChange={() => {}} />);
    expect(el.className).toBe("input mono");
  });

  it("lets a type override text and passes every input attribute", () => {
    const el = textInput(<TextInput type="time" list="zones" placeholder="09:00" value="" onChange={() => {}} />);
    expect(el.type).toBe("time");
    expect(el.getAttribute("list")).toBe("zones");
    expect(el.placeholder).toBe("09:00");
  });
});
