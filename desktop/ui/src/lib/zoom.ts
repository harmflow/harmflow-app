// The dashboard's zoom: its fixed steps, the keys that move it, and the level kept between launches. The
// desktop keeps the level, not the daemon's settings: it belongs to the window and its display.

/** The steps, in percent. 150 is the cap until the layout is checked at narrower widths. */
export const ZOOM_STEPS = [80, 90, 100, 110, 125, 150] as const;

export type Zoom = (typeof ZOOM_STEPS)[number];

export type ZoomKey = "in" | "out" | "reset";

const ZOOM_STORAGE_KEY = "harmflow.zoom";

/**
 * The zoom key pressed, if any: ⌘+ or ⌘= zooms in, ⌘− out, and ⌘0 returns to 100%, the keypad's + and − included. ⌘+
 * is Shift with = on the US and ABNT2 layouts, so Shift is allowed on that key alone; Control and Option are not. A
 * held key keeps zooming, as in Safari, up to the end of the range. Linux and Windows would check ctrlKey.
 */
export function zoomKey(e: KeyboardEvent): ZoomKey | null {
  if (!e.metaKey || e.ctrlKey || e.altKey) return null;
  if (e.key === "+" || e.key === "=") return "in";
  if (e.shiftKey) return null;
  if (e.key === "-") return "out";
  if (e.key === "0") return "reset";
  return null;
}

/** The level after a key: one step in or out, stopping at either end without wrapping, or back to 100%. */
export function zoomed(level: Zoom, key: ZoomKey): Zoom {
  if (key === "reset") return 100;
  const next = ZOOM_STEPS.indexOf(level) + (key === "in" ? 1 : -1);
  return ZOOM_STEPS[Math.min(Math.max(next, 0), ZOOM_STEPS.length - 1)];
}

/** The level kept from the last launch; 100% when none was kept, or when what was kept is not a step. */
export function storedZoom(): Zoom {
  try {
    const kept = Number(localStorage.getItem(ZOOM_STORAGE_KEY));
    return ZOOM_STEPS.find((step) => step === kept) ?? 100;
  } catch {
    return 100;
  }
}

export function storeZoom(level: Zoom): void {
  try {
    localStorage.setItem(ZOOM_STORAGE_KEY, String(level));
  } catch {
    // Without storage the level lasts until the app quits.
  }
}
