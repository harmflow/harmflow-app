// @vitest-environment happy-dom
// ListRows: the list that holds list rows, each in its `<li>`.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import i18n from "../../i18n";
import { ListRow } from "./ListRow";
import { ListRows } from "./ListRows";

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

describe("ListRows", () => {
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
