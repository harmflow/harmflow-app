// The interface's typeface, the choice of Settings → General: HarmFlow, the fonts the app bundles, or System, the
// operating system's own. One attribute on the root element switches the three font stacks of theme.css, so a change
// shows at once in the open window, with no restart.

export const TYPEFACES = ["harmflow", "system"] as const;
export type Typeface = (typeof TYPEFACES)[number];

const STORAGE_KEY = "harmflow.typeface";

/** The typeface a setting names: a missing or unknown value reads as the bundled fonts. */
export function resolveTypeface(setting: string | null | undefined): Typeface {
  return setting === "system" ? "system" : "harmflow";
}

/** Applies a typeface at once, with no restart. */
export function applyTypeface(setting: string | null | undefined): Typeface {
  const typeface = resolveTypeface(setting);
  if (typeface === "system") document.documentElement.dataset.typeface = "system";
  else delete document.documentElement.dataset.typeface;
  try {
    localStorage.setItem(STORAGE_KEY, typeface);
  } catch {
    // Storage is a convenience for the first paint only; the daemon's settings are the source.
  }
  return typeface;
}

/** Applies the typeface last chosen, before the daemon's settings arrive, so the first paint does not switch fonts. */
export function applyStoredTypeface(): Typeface {
  try {
    return applyTypeface(localStorage.getItem(STORAGE_KEY));
  } catch {
    return applyTypeface(undefined);
  }
}
