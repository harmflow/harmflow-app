// The Tasks screen's arrangement: the user's named groups, their order, and the order of the tasks in each. It is
// visual only — the desktop keeps it, as it keeps the zoom, and it belongs to this computer: no task document, no
// request to the daemon, and no export carries it. The screen derives its order from it and the daemon's list each
// time it renders, since the list is read again after every task event.

/** A group, in the user's order; `tasks` holds task IDs in the user's order. */
export type TaskGroup = { id: string; name: string; collapsed: boolean; tasks: string[] };

/** The arrangement as kept: the groups, then the tasks in no group, in their order. */
export type TaskLayout = { groups: TaskGroup[]; ungrouped: string[] };

/** Where a task goes: a group's ID, or null for Ungrouped, and its index there. */
export type Place = { group: string | null; index: number };

/** A group's tasks, or Ungrouped's (group null), as the screen shows them. */
export type Section<T> = { group: TaskGroup | null; tasks: T[] };

const LAYOUT_STORAGE_KEY = "harmflow.taskLayout";

const isStrings = (v: unknown): v is string[] => Array.isArray(v) && v.every((s) => typeof s === "string");

function isGroup(v: unknown): v is TaskGroup {
  if (typeof v !== "object" || v === null) return false;
  const g = v as Record<string, unknown>;
  return typeof g.id === "string" && typeof g.name === "string" && typeof g.collapsed === "boolean" && isStrings(g.tasks);
}

/** Reads a kept arrangement; null when none was kept or what was kept can't be read, so the screen shows today's list. */
export function parseLayout(text: string | null): TaskLayout | null {
  if (text === null) return null;
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return null;
  }
  if (typeof v !== "object" || v === null) return null;
  const { groups, ungrouped } = v as Record<string, unknown>;
  if (!Array.isArray(groups) || !groups.every(isGroup) || !isStrings(ungrouped)) return null;
  return { groups: groups.map((g) => ({ id: g.id, name: g.name, collapsed: g.collapsed, tasks: [...g.tasks] })), ungrouped: [...ungrouped] };
}

export function storedLayout(): TaskLayout | null {
  try {
    return parseLayout(localStorage.getItem(LAYOUT_STORAGE_KEY));
  } catch {
    return null;
  }
}

export function storeLayout(layout: TaskLayout): void {
  try {
    localStorage.setItem(LAYOUT_STORAGE_KEY, JSON.stringify(layout));
  } catch {
    // Without storage the arrangement lasts until the screen is left.
  }
}

/**
 * The arrangement held to the daemon's list: a task the list no longer has leaves it, a task named twice keeps its
 * first place, and a task it doesn't know joins the end of Ungrouped, in the list's order. With nothing kept, null:
 * the list keeps the daemon's order, by ID.
 */
export function reconcile(layout: TaskLayout | null, ids: string[]): TaskLayout | null {
  if (!layout) return null;
  const present = new Set(ids);
  const placed = new Set<string>();
  const keep = (list: string[]) =>
    list.filter((id) => {
      if (!present.has(id) || placed.has(id)) return false;
      placed.add(id);
      return true;
    });
  const groups = layout.groups.map((g) => ({ ...g, tasks: keep(g.tasks) }));
  const ungrouped = keep(layout.ungrouped);
  for (const id of ids) if (!placed.has(id)) ungrouped.push(id);
  return { groups, ungrouped };
}

/** The arrangement to change: the kept one, or the daemon's list as it shows today, all of it Ungrouped. */
export function materialize(layout: TaskLayout | null, ids: string[]): TaskLayout {
  return reconcile(layout ?? { groups: [], ungrouped: [] }, ids)!;
}

export function sameLayout(a: TaskLayout | null, b: TaskLayout | null): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

/** The tasks in the arrangement's order: each group's, then Ungrouped's. */
export function arrange<T extends { id: string }>(layout: TaskLayout | null, tasks: T[]): Section<T>[] {
  const byId = new Map(tasks.map((t) => [t.id, t]));
  const held = reconcile(layout, tasks.map((t) => t.id));
  if (!held) return [{ group: null, tasks }];
  const pick = (ids: string[]) => ids.map((id) => byId.get(id)!);
  return [...held.groups.map((g) => ({ group: g, tasks: pick(g.tasks) })), { group: null, tasks: pick(held.ungrouped) }];
}

/** The place of a task: its group (null for Ungrouped) and its index there; undefined for a task it doesn't hold. */
export function placeOf(layout: TaskLayout, id: string): Place | undefined {
  for (const g of layout.groups) {
    const index = g.tasks.indexOf(id);
    if (index >= 0) return { group: g.id, index };
  }
  const index = layout.ungrouped.indexOf(id);
  return index >= 0 ? { group: null, index } : undefined;
}

/** The task IDs of a group, or of Ungrouped for null. */
export function tasksIn(layout: TaskLayout, group: string | null): string[] {
  return group === null ? layout.ungrouped : (layout.groups.find((g) => g.id === group)?.tasks ?? []);
}

function withList(layout: TaskLayout, group: string | null, change: (list: string[]) => string[]): TaskLayout {
  if (group === null) return { ...layout, ungrouped: change(layout.ungrouped) };
  return { ...layout, groups: layout.groups.map((g) => (g.id === group ? { ...g, tasks: change(g.tasks) } : g)) };
}

/** Moves a task to a place; the index counts the target's tasks without the one moved, and is held to its ends. */
export function moveTask(layout: TaskLayout, id: string, to: Place): TaskLayout {
  if (to.group !== null && !layout.groups.some((g) => g.id === to.group)) return layout;
  const from = placeOf(layout, id);
  const without = from ? withList(layout, from.group, (list) => list.filter((t) => t !== id)) : layout;
  return withList(without, to.group, (list) => {
    const index = Math.min(Math.max(to.index, 0), list.length);
    return [...list.slice(0, index), id, ...list.slice(index)];
  });
}

/** Moves a task one place up (-1) or down (+1) in its group; at either end it stays. */
export function moveTaskBy(layout: TaskLayout, id: string, delta: -1 | 1): TaskLayout {
  const from = placeOf(layout, id);
  if (!from) return layout;
  const index = from.index + delta;
  if (index < 0 || index >= tasksIn(layout, from.group).length) return layout;
  return moveTask(layout, id, { group: from.group, index });
}

/** Moves a group to an index among the groups, held to their ends. */
export function moveGroup(layout: TaskLayout, id: string, index: number): TaskLayout {
  const group = layout.groups.find((g) => g.id === id);
  if (!group) return layout;
  const rest = layout.groups.filter((g) => g.id !== id);
  const at = Math.min(Math.max(index, 0), rest.length);
  return { ...layout, groups: [...rest.slice(0, at), group, ...rest.slice(at)] };
}

/** Whether another group already has the name, ignoring case and the spaces around it. */
export function nameTaken(layout: TaskLayout, name: string, except?: string): boolean {
  const key = name.trim().toLocaleLowerCase();
  return layout.groups.some((g) => g.id !== except && g.name.trim().toLocaleLowerCase() === key);
}

/** The first name of `base`, `base 2`, `base 3`… that no group has. */
export function freeName(layout: TaskLayout, base: string): string {
  for (let n = 1; ; n++) {
    const name = n === 1 ? base : `${base} ${n}`;
    if (!nameTaken(layout, name)) return name;
  }
}

let groupCount = 0;

/** Adds an empty group at the end of the groups, under a name no other group has. */
export function addGroup(layout: TaskLayout, name: string): { layout: TaskLayout; id: string } {
  const id = `group-${Date.now().toString(36)}-${(groupCount++).toString(36)}`;
  return { layout: { ...layout, groups: [...layout.groups, { id, name: freeName(layout, name.trim()), collapsed: false, tasks: [] }] }, id };
}

/** Renames a group; null when the name is empty or another group has it. */
export function renameGroup(layout: TaskLayout, id: string, name: string): TaskLayout | null {
  const trimmed = name.trim();
  if (!trimmed || nameTaken(layout, trimmed, id)) return null;
  return { ...layout, groups: layout.groups.map((g) => (g.id === id ? { ...g, name: trimmed } : g)) };
}

export function setCollapsed(layout: TaskLayout, id: string, collapsed: boolean): TaskLayout {
  return { ...layout, groups: layout.groups.map((g) => (g.id === id ? { ...g, collapsed } : g)) };
}

/** Deletes a group; its tasks join the end of Ungrouped, in their order. No task is deleted. */
export function deleteGroup(layout: TaskLayout, id: string): TaskLayout {
  const group = layout.groups.find((g) => g.id === id);
  if (!group) return layout;
  return { groups: layout.groups.filter((g) => g.id !== id), ungrouped: [...layout.ungrouped, ...group.tasks] };
}

/** Places a new task right after another, in the same group: where a duplicate goes after its original. */
export function placeAfter(layout: TaskLayout, original: string, id: string): TaskLayout {
  const at = placeOf(layout, original);
  if (!at) return layout;
  const without = moveTask(layout, id, { group: null, index: layout.ungrouped.length });
  return moveTask(without, id, { group: at.group, index: placeOf(without, original)!.index + 1 });
}
