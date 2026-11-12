// @vitest-environment happy-dom
// Chip, with and without its remove button.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { Chip } from "./Chip";

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
