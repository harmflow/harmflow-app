// @vitest-environment happy-dom
// Drawer: a modal dialog with the label it is given, which closes on its close button, on a click outside its card,
// and on the close shortcut. A click inside the card, or another key, does not close it.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { Drawer } from "./Drawer";

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
