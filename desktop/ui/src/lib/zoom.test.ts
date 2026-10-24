// @vitest-environment happy-dom
import { beforeEach, describe, expect, it } from "vitest";
import { ZOOM_STEPS, storeZoom, storedZoom, zoomKey, zoomed, type Zoom, type ZoomKey } from "./zoom";

describe("zoomKey", () => {
  const key = (init: KeyboardEventInit) => zoomKey(new KeyboardEvent("keydown", { metaKey: true, ...init }));

  it.each([
    [{ key: "=" }, "in"],
    [{ key: "+", shiftKey: true }, "in"], // ⌘+ on the US and ABNT2 layouts
    [{ key: "=", shiftKey: true }, "in"], // the same, where the web view reports the key without Shift
    [{ key: "+", code: "NumpadAdd" }, "in"],
    [{ key: "-" }, "out"],
    [{ key: "-", code: "NumpadSubtract" }, "out"],
    [{ key: "0" }, "reset"],
    [{ key: "=", repeat: true }, "in"],
  ] as const)("reads %o as %s", (init, expected) => {
    expect(key(init)).toBe(expected);
  });

  it.each([
    { key: "=", metaKey: false },
    { key: "=", ctrlKey: true },
    { key: "=", altKey: true },
    { key: "-", shiftKey: true },
    { key: "0", shiftKey: true },
    { key: "-", ctrlKey: true },
    { key: "0", altKey: true },
    { key: "b" },
    { key: "1" },
  ])("leaves %o alone", (init) => {
    expect(key(init)).toBeNull();
  });
});

describe("zoomed", () => {
  /** The level after each of the presses. */
  const presses = (level: Zoom, key: ZoomKey, times: number) => Array.from({ length: times }, () => (level = zoomed(level, key)));

  it("moves one step per press through the fixed steps, and stops at either end without wrapping", () => {
    expect(ZOOM_STEPS).toEqual([80, 90, 100, 110, 125, 150]);
    expect(presses(100, "in", 4)).toEqual([110, 125, 150, 150]);
    expect(presses(150, "out", 6)).toEqual([125, 110, 100, 90, 80, 80]);
  });

  it("returns to 100% from any step", () => {
    for (const level of ZOOM_STEPS) expect(zoomed(level, "reset")).toBe(100);
  });
});

describe("the kept level", () => {
  beforeEach(() => localStorage.clear());

  it("is 100% when none was kept", () => {
    expect(storedZoom()).toBe(100);
  });

  it("is the level stored last", () => {
    storeZoom(125);
    expect(storedZoom()).toBe(125);
    storeZoom(80);
    expect(storedZoom()).toBe(80);
  });

  it.each(["0", "175", "1.25", "110%", "big", ""])("is 100%% when what was kept, %o, is not a step", (kept) => {
    localStorage.setItem("harmflow.zoom", kept);
    expect(storedZoom()).toBe(100);
  });
});
