// @vitest-environment happy-dom
// TextInput's mono and invalid props.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import i18n from "../../i18n";
import { TextInput } from "./TextInput";

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

/** The text field `node` draws. */
function textInput(node: ReactNode) {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(node));
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
