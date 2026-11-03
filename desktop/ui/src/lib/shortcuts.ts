// The dashboard's keyboard shortcuts, each defined once: its keys, the group it is listed under, and its label. The
// handlers match keys only through `matches`, the tooltips show `shortcutText`, and Settings → Shortcuts lists the
// table, so the list cannot drift from what the keys do. `shortcuts.test.ts` fails on a key matched anywhere else.
//
// Settings… (⌘,) and Quit (⌘Q) are native menu items: their keys live in the shell's `menu.rs`, which the native menu
// needs, and the table repeats them for the list; a test fails when the two differ.

/** One way to press a shortcut. Control and Option are never part of one; Command is when `meta` says so. */
export interface Chord {
  /** The key as `KeyboardEvent.key` reads it, a letter in lower case. */
  key: string;
  meta?: boolean;
  /** Whether Shift is pressed with it, or "any" when Shift changes nothing (⌘+ is Shift with = on some layouts). */
  shift?: boolean | "any";
}

/** Where a shortcut works, in the order Settings → Shortcuts lists the groups. */
export const SHORTCUT_GROUPS = ["dashboard", "zoom", "editor", "tasks", "executions", "timeline", "dialogs"] as const;

export type ShortcutGroup = (typeof SHORTCUT_GROUPS)[number];

export interface Shortcut {
  group: ShortcutGroup;
  /** Every way to press it; the first one is the one tooltips show. */
  chords: readonly Chord[];
  /** Whether a held key repeats it. */
  repeats?: boolean;
  /** The native menu's accelerator, for the two shortcuts the native menu handles rather than the dashboard. */
  accelerator?: string;
}

export const SHORTCUTS = {
  newTask: { group: "dashboard", chords: [{ key: "n", meta: true }] },
  toggleSidebar: { group: "dashboard", chords: [{ key: "b", meta: true }] },
  // The sidebar's screens, in its order: `NAV` in Shell.tsx gives each item its shortcut, and a test fails when the
  // digits do not follow that order.
  openOverview: { group: "dashboard", chords: [{ key: "1", meta: true }] },
  openExecutions: { group: "dashboard", chords: [{ key: "2", meta: true }] },
  openTasks: { group: "dashboard", chords: [{ key: "3", meta: true }] },
  openHistory: { group: "dashboard", chords: [{ key: "4", meta: true }] },
  openSettings: { group: "dashboard", chords: [{ key: "5", meta: true }] },
  settings: { group: "dashboard", chords: [{ key: ",", meta: true }], accelerator: "CmdOrCtrl+," },
  quit: { group: "dashboard", chords: [{ key: "q", meta: true }], accelerator: "CmdOrCtrl+Q" },
  zoomIn: {
    group: "zoom",
    chords: [
      { key: "+", meta: true, shift: "any" },
      { key: "=", meta: true, shift: "any" },
    ],
    repeats: true,
  },
  zoomOut: { group: "zoom", chords: [{ key: "-", meta: true }], repeats: true },
  zoomReset: { group: "zoom", chords: [{ key: "0", meta: true }], repeats: true },
  save: { group: "editor", chords: [{ key: "s", meta: true }] },
  cancelMove: { group: "tasks", chords: [{ key: "Escape" }], repeats: true },
  toggleExecutionsAside: { group: "executions", chords: [{ key: "e", meta: true }] },
  previousEvent: { group: "timeline", chords: [{ key: "ArrowUp", shift: "any" }], repeats: true },
  nextEvent: { group: "timeline", chords: [{ key: "ArrowDown", shift: "any" }], repeats: true },
  firstEvent: { group: "timeline", chords: [{ key: "Home", shift: "any" }], repeats: true },
  lastEvent: { group: "timeline", chords: [{ key: "End", shift: "any" }], repeats: true },
  close: { group: "dialogs", chords: [{ key: "Escape", shift: "any" }], repeats: true },
} as const satisfies Record<string, Shortcut>;

export type ShortcutId = keyof typeof SHORTCUTS;

function pressed(e: Pick<KeyboardEvent, "key" | "metaKey" | "ctrlKey" | "altKey" | "shiftKey">, chord: Chord): boolean {
  if (e.ctrlKey || e.altKey || e.metaKey !== (chord.meta ?? false)) return false;
  const shift = chord.shift ?? false;
  if (shift !== "any" && e.shiftKey !== shift) return false;
  return e.key.length === 1 ? e.key.toLowerCase() === chord.key : e.key === chord.key;
}

/**
 * Whether the event presses the shortcut: one of its chords, with no Control or Option, and not as a held key unless
 * the shortcut repeats. Linux and Windows would check ctrlKey for Command.
 */
export function matches(e: Pick<KeyboardEvent, "key" | "metaKey" | "ctrlKey" | "altKey" | "shiftKey" | "repeat">, id: ShortcutId): boolean {
  const shortcut: Shortcut = SHORTCUTS[id];
  if (e.repeat && !shortcut.repeats) return false;
  return shortcut.chords.some((chord) => pressed(e, chord));
}

/** The first of the shortcuts the event presses, or null. */
export function matching<Id extends ShortcutId>(e: Parameters<typeof matches>[0], ids: readonly Id[]): Id | null {
  return ids.find((id) => matches(e, id)) ?? null;
}

const KEY_SYMBOLS: Readonly<Record<string, string>> = {
  ArrowUp: "↑",
  ArrowDown: "↓",
  Escape: "Esc",
  "-": "−",
};

/** A chord's keycaps, with the macOS symbols the tooltips use: ⌘ and ⇧ before the key, a letter in upper case. */
export function keycaps(chord: Chord): string[] {
  const caps: string[] = [];
  if (chord.shift === true) caps.push("⇧");
  if (chord.meta) caps.push("⌘");
  caps.push(KEY_SYMBOLS[chord.key] ?? (chord.key.length === 1 ? chord.key.toUpperCase() : chord.key));
  return caps;
}

/** The shortcut's keys as a tooltip shows them, its first chord's keycaps run together: "⌘N". */
export function shortcutText(id: ShortcutId): string {
  return keycaps(SHORTCUTS[id].chords[0]).join("");
}

/** The shortcut's first chord in the form of `aria-keyshortcuts`: "Meta+N". */
export function ariaKeys(id: ShortcutId): string {
  const chord: Chord = SHORTCUTS[id].chords[0];
  return [chord.meta ? "Meta" : null, chord.shift === true ? "Shift" : null, chord.key.length === 1 ? chord.key.toUpperCase() : chord.key]
    .filter(Boolean)
    .join("+");
}

/** The shortcuts of each group, in the table's order, for Settings → Shortcuts. */
export function shortcutsByGroup(): ReadonlyArray<{ group: ShortcutGroup; ids: ShortcutId[] }> {
  const ids = Object.keys(SHORTCUTS) as ShortcutId[];
  return SHORTCUT_GROUPS.map((group) => ({ group, ids: ids.filter((id) => SHORTCUTS[id].group === group) }));
}

/**
 * The keys a control answers as every control of its kind does, which are no shortcut of HarmFlow's and so are not
 * listed, like macOS's own Edit keys: Enter or Space presses a focused element that acts as a button, Enter keeps what
 * a field holds, and Escape gives a field back the value it had. They are matched here too, so that every key the
 * interface matches is defined in this file.
 */
const CONTROL_KEYS = {
  activate: ["Enter", " "],
  commit: ["Enter"],
  revert: ["Escape"],
} as const;

export function controlKey(e: Pick<KeyboardEvent, "key">, kind: keyof typeof CONTROL_KEYS): boolean {
  return (CONTROL_KEYS[kind] as readonly string[]).includes(e.key);
}
