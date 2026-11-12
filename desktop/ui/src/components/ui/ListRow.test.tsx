// @vitest-environment happy-dom
// The ListRow primitive's slots, in the order a row shows them, and what a click on it does: a row with an action is a
// button, one without is not, and both show the same slots.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { ListRow } from "./ListRow";

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
});
