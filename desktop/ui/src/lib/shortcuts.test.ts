// @vitest-environment happy-dom
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";
import { NAV } from "../components/Shell";
import { enUS } from "../i18n/en-US";
import { SHORTCUTS, SHORTCUT_GROUPS, ariaKeys, controlKey, keycaps, matches, matching, shortcutText, shortcutsByGroup, type ShortcutId } from "./shortcuts";

const press = (init: KeyboardEventInit) => new KeyboardEvent("keydown", init);
const ids = Object.keys(SHORTCUTS) as ShortcutId[];

describe("the shortcut table", () => {
  it("holds the shortcuts HarmFlow has built, each in its group", () => {
    expect(Object.fromEntries(shortcutsByGroup().map(({ group, ids }) => [group, ids]))).toEqual({
      dashboard: ["newTask", "toggleSidebar", "openOverview", "openExecutions", "openTasks", "openHistory", "openSettings", "settings", "quit"],
      zoom: ["zoomIn", "zoomOut", "zoomReset"],
      editor: ["save"],
      tasks: ["cancelMove"],
      executions: ["toggleExecutionsAside"],
      timeline: ["previousEvent", "nextEvent", "firstEvent", "lastEvent"],
      dialogs: ["close"],
    });
  });

  it("opens the sidebar's items with ⌘1 onward, in the order the sidebar shows them", () => {
    expect(NAV.map((item) => shortcutText(item.shortcut))).toEqual(NAV.map((_, index) => `⌘${index + 1}`));
  });

  it("gives every group and every shortcut a label", () => {
    for (const group of SHORTCUT_GROUPS) expect(enUS.settings.shortcuts.groups[group]).toBeTruthy();
    for (const id of ids) expect(enUS.settings.shortcuts.labels[id]).toBeTruthy();
  });

  it.each([
    ["newTask", "⌘N", "Meta+N"],
    ["toggleSidebar", "⌘B", "Meta+B"],
    ["openOverview", "⌘1", "Meta+1"],
    ["openSettings", "⌘5", "Meta+5"],
    ["settings", "⌘,", "Meta+,"],
    ["quit", "⌘Q", "Meta+Q"],
    ["zoomIn", "⌘+", "Meta++"],
    ["zoomOut", "⌘−", "Meta+-"],
    ["zoomReset", "⌘0", "Meta+0"],
    ["save", "⌘S", "Meta+S"],
    ["toggleExecutionsAside", "⌘E", "Meta+E"],
    ["previousEvent", "↑", "ArrowUp"],
    ["nextEvent", "↓", "ArrowDown"],
    ["firstEvent", "Home", "Home"],
    ["lastEvent", "End", "End"],
    ["close", "Esc", "Escape"],
  ] as const)("shows %s as %s, and names it %s to assistive technology", (id, text, aria) => {
    expect(shortcutText(id)).toBe(text);
    expect(ariaKeys(id)).toBe(aria);
  });

  it("puts ⇧ and ⌘ before the key, as the macOS menus do", () => {
    expect(keycaps({ key: "z", meta: true, shift: true })).toEqual(["⇧", "⌘", "Z"]);
    expect(keycaps({ key: "=", meta: true, shift: "any" })).toEqual(["⌘", "="]);
  });
});

describe("matches", () => {
  it("takes ⌘ with the letter, either case, and nothing with Control, Option, or Shift added", () => {
    expect(matches(press({ key: "n", metaKey: true }), "newTask")).toBe(true);
    expect(matches(press({ key: "N", metaKey: true }), "newTask")).toBe(true);
    for (const extra of [{ ctrlKey: true }, { altKey: true }, { shiftKey: true }, { metaKey: false }]) {
      expect(matches(press({ key: "n", metaKey: true, ...extra }), "newTask")).toBe(false);
    }
  });

  it("takes a held key only for a shortcut that repeats", () => {
    expect(matches(press({ key: "s", metaKey: true, repeat: true }), "save")).toBe(false);
    expect(matches(press({ key: "=", metaKey: true, repeat: true }), "zoomIn")).toBe(true);
    expect(matches(press({ key: "ArrowDown", repeat: true }), "nextEvent")).toBe(true);
  });

  it("takes the timeline's keys with no modifier but Shift, and Esc with none but Shift", () => {
    expect(matching(press({ key: "Home" }), ["previousEvent", "nextEvent", "firstEvent", "lastEvent"])).toBe("firstEvent");
    expect(matches(press({ key: "ArrowUp", shiftKey: true }), "previousEvent")).toBe(true);
    expect(matches(press({ key: "ArrowUp", metaKey: true }), "previousEvent")).toBe(false);
    expect(matches(press({ key: "Escape" }), "close")).toBe(true);
    expect(matches(press({ key: "Escape", altKey: true }), "close")).toBe(false);
  });

  it("finds none of a list when no shortcut of it is pressed", () => {
    expect(matching(press({ key: "x", metaKey: true }), ["newTask", "toggleSidebar"])).toBeNull();
  });
});

describe("controlKey", () => {
  it("reads the keys every control answers, which are no shortcut of HarmFlow's", () => {
    expect(controlKey(press({ key: "Enter" }), "activate")).toBe(true);
    expect(controlKey(press({ key: " " }), "activate")).toBe(true);
    expect(controlKey(press({ key: "Enter" }), "commit")).toBe(true);
    expect(controlKey(press({ key: " " }), "commit")).toBe(false);
    expect(controlKey(press({ key: "Escape" }), "revert")).toBe(true);
  });
});

// The native menu handles ⌘, and ⌘Q, with keys the shell defines: the list shows the table's, which must be the same.
describe("the native menu's keys", () => {
  const menu = readFileSync(join(__dirname, "../../../src-tauri/src/menu.rs"), "utf8");
  const accelerator = (name: string) => new RegExp(`pub const ${name}: &str = "([^"]+)";`).exec(menu)?.[1];

  it("are the table's", () => {
    expect(accelerator("SETTINGS_ACCELERATOR")).toBe(SHORTCUTS.settings.accelerator);
    expect(accelerator("QUIT_ACCELERATOR")).toBe(SHORTCUTS.quit.accelerator);
  });

  it("are the only keys of the menu, and Keyboard Shortcuts has none", () => {
    const keys = [...menu.matchAll(/MenuItem::with_id\([^;]*?, (Some\(\w+\)|None::<&str>)\)/g)].map((m) => m[1]);
    expect(keys.sort()).toEqual(["None::<&str>", "Some(QUIT_ACCELERATOR)", "Some(SETTINGS_ACCELERATOR)"]);
  });

  it("are the chords the table shows for them", () => {
    const chord = (accelerator: string) => ({ key: accelerator.replace("CmdOrCtrl+", "").toLowerCase(), meta: true });
    expect(SHORTCUTS.settings.chords).toEqual([chord(SHORTCUTS.settings.accelerator)]);
    expect(SHORTCUTS.quit.chords).toEqual([chord(SHORTCUTS.quit.accelerator)]);
  });
});

// A handler matches keys only through the table, so a key it handles is always one the list shows.
describe("key matches outside the table", () => {
  const src = join(__dirname, "..");
  const files = (dir: string): string[] =>
    readdirSync(dir).flatMap((name) => {
      const path = join(dir, name);
      if (statSync(path).isDirectory()) return files(path);
      return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) ? [path] : [];
    });
  // A keyboard event's key or code read, or a key's name written out.
  const keyMatch = /\b(?:e|ev|event|keyEvent)\.(?:key|code)\b|["'](?:Escape|Enter|ArrowUp|ArrowDown|ArrowLeft|ArrowRight|Home|End|Tab)["']/;

  it("are none", () => {
    const found = files(src)
      .filter((path) => !path.endsWith(join("lib", "shortcuts.ts")))
      .flatMap((path) =>
        readFileSync(path, "utf8")
          .split("\n")
          .map((line, i) => (keyMatch.test(line) ? `${relative(src, path)}:${i + 1}: ${line.trim()}` : null))
          .filter((line): line is string => line !== null),
      );
    expect(found).toEqual([]);
  });

  it("would be found", () => {
    for (const line of ['if (e.key === "s") save();', "const to = moves[ev.key];", 'if (event.code === "KeyB") toggle();', 'const keys = ["Escape"];']) {
      expect(keyMatch.test(line), line).toBe(true);
    }
  });
});
