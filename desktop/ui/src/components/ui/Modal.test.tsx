// @vitest-environment happy-dom
// Modal: it closes as a Drawer does, through the same overlay; a click inside its card does not close it.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { Modal } from "./Modal";

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

const dialog = () => host!.querySelector<HTMLElement>('[role="dialog"]')!;
const press = (key: string) => act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true })));
const mouseDown = (target: HTMLElement) => act(() => void target.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
const closeButton = () => dialog().querySelector<HTMLButtonElement>('button[aria-label="Close"]')!;

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
