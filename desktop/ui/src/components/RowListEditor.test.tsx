// @vitest-environment happy-dom
// RowListEditor: the shared editor of a list of rows of text fields, with a remove button on each row and an add button
// below, which Settings' path variables and allowed folders and the task form's repositories use.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { Button } from "./ui";
import { RowListEditor, type RowListColumn } from "./RowListEditor";

type Pair = [string, string];

const columns: RowListColumn<Pair>[] = [
  { label: "Key", placeholder: "KEY", width: "minmax(0, 1fr)", mono: true, value: ([k]) => k, set: ([, v], k) => [k.toUpperCase(), v] },
  { label: "Value", width: "130px", value: ([, v]) => v, set: ([k], v) => [k, v], error: ([, v]) => v === "bad" },
];

let root: Root | undefined;
let host: HTMLElement;
let latest: Pair[] = [];

function Harness({ initial, header, actions }: { initial: Pair[]; header?: boolean; actions?: boolean }) {
  const [rows, setRows] = useState(initial);
  latest = rows;
  return (
    <RowListEditor<Pair>
      rows={rows}
      columns={columns}
      empty={() => ["", ""]}
      onChange={setRows}
      addLabel="Add pair"
      removeLabel="Remove"
      header={header}
      hint="Pairs of a key and a value."
      actions={actions ? <Button>Save</Button> : undefined}
    />
  );
}

function render(initial: Pair[], props: { header?: boolean; actions?: boolean } = {}) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<Harness initial={initial} {...props} />));
}

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  host?.remove();
});

const fields = (name: string) => [...host.querySelectorAll<HTMLInputElement>(`input[aria-label="${name}"]`)];
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label || b.getAttribute("aria-label") === label);
const removes = () => [...host.querySelectorAll<HTMLButtonElement>('button[aria-label="Remove"]')];

async function enter(el: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function click(el: HTMLElement) {
  await act(async () => el.click());
}

describe("RowListEditor", () => {
  it("draws a row of fields per item, each named by its column, with a remove button", () => {
    render([
      ["A", "1"],
      ["B", "2"],
    ]);
    expect(fields("Key").map((f) => f.value)).toEqual(["A", "B"]);
    expect(fields("Value").map((f) => f.value)).toEqual(["1", "2"]);
    expect(fields("Key")[0]!.placeholder).toBe("KEY");
    expect(fields("Key")[0]!.classList.contains("mono")).toBe(true);
    expect(fields("Value")[0]!.classList.contains("mono")).toBe(false);
    expect(removes()).toHaveLength(2);
    expect(host.querySelector(".field__hint")?.textContent).toBe("Pairs of a key and a value.");
  });

  it("lays each row out in its columns' tracks, then the remove button's", () => {
    render([["A", "1"]]);
    const row = host.querySelector<HTMLElement>(".row-list__row")!;
    expect(row.style.gridTemplateColumns).toBe("minmax(0, 1fr) 130px 32px");
  });

  it("adds an empty row, edits it through its column, and removes a row", async () => {
    render([["A", "1"]]);
    await click(button("Add pair")!);
    expect(latest).toEqual([
      ["A", "1"],
      ["", ""],
    ]);

    await enter(fields("Key")[1]!, "b");
    await enter(fields("Value")[1]!, "2");
    expect(latest).toEqual([
      ["A", "1"],
      ["B", "2"],
    ]);

    await click(removes()[0]!);
    expect(latest).toEqual([["B", "2"]]);
    expect(fields("Key").map((f) => f.value)).toEqual(["B"]);
  });

  it("marks the field its column's error names", () => {
    render([
      ["A", "bad"],
      ["B", "good"],
    ]);
    expect(fields("Value").map((f) => f.classList.contains("input--error"))).toEqual([true, false]);
    expect(fields("Key").some((f) => f.classList.contains("input--error"))).toBe(false);
  });

  it("draws a header of the columns' labels only when asked", () => {
    render([["A", "1"]]);
    expect(host.querySelector(".row-list__head")).toBeNull();
    act(() => root!.unmount());
    host.remove();

    render([["A", "1"]], { header: true });
    const head = host.querySelector(".row-list__head")!;
    expect([...head.children].map((c) => c.textContent)).toEqual(["Key", "Value", ""]);
  });

  it("puts its actions beside the add button", () => {
    render([], { actions: true });
    const actions = host.querySelector(".inline-actions")!;
    expect([...actions.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["Add pair", "Save"]);
  });
});
