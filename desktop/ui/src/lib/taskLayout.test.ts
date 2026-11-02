// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from "vitest";
import {
  addGroup,
  arrange,
  deleteGroup,
  materialize,
  moveGroup,
  moveTask,
  moveTaskBy,
  parseLayout,
  placeAfter,
  reconcile,
  renameGroup,
  setCollapsed,
  storeLayout,
  storedLayout,
  type TaskLayout,
} from "./taskLayout";

const group = (id: string, name: string, tasks: string[], collapsed = false) => ({ id, name, collapsed, tasks });
const ids = (layout: TaskLayout) => ({ groups: layout.groups.map((g) => [g.name, ...g.tasks]), ungrouped: layout.ungrouped });

afterEach(() => localStorage.clear());

describe("the kept arrangement", () => {
  it("is read back as it was kept, under harmflow.taskLayout", () => {
    const layout: TaskLayout = { groups: [group("g1", "Reports", ["b", "a"], true)], ungrouped: ["c"] };
    storeLayout(layout);
    expect(JSON.parse(localStorage.getItem("harmflow.taskLayout")!)).toEqual(layout);
    expect(storedLayout()).toEqual(layout);
  });

  it("is none when nothing was kept", () => {
    expect(storedLayout()).toBeNull();
  });

  it.each([
    ["text that is not JSON", "{groups"],
    ["a value of another shape", '"Reports"'],
    ["no list of groups", '{"ungrouped":[]}'],
    ["a group without a name", '{"groups":[{"id":"g1","collapsed":false,"tasks":[]}],"ungrouped":[]}'],
    ["a task ID that is not text", '{"groups":[],"ungrouped":[42]}'],
  ])("is ignored when it can't be read: %s", (_what, text) => {
    expect(parseLayout(text)).toBeNull();
  });
});

describe("the arrangement held to the daemon's list", () => {
  it("keeps the daemon's order, by ID, when nothing was kept", () => {
    expect(reconcile(null, ["a", "b"])).toBeNull();
    expect(arrange(null, [{ id: "a" }, { id: "b" }])).toEqual([{ group: null, tasks: [{ id: "a" }, { id: "b" }] }]);
  });

  it("drops a deleted task, and puts a task it doesn't know at the end of Ungrouped", () => {
    const layout: TaskLayout = { groups: [group("g1", "Reports", ["gone", "b"])], ungrouped: ["c", "a"] };
    expect(ids(reconcile(layout, ["a", "b", "c", "new"])!)).toEqual({ groups: [["Reports", "b"]], ungrouped: ["c", "a", "new"] });
  });

  it("keeps a task named twice in its first place", () => {
    const layout: TaskLayout = { groups: [group("g1", "Reports", ["a"])], ungrouped: ["a", "b"] };
    expect(ids(reconcile(layout, ["a", "b"])!)).toEqual({ groups: [["Reports", "a"]], ungrouped: ["b"] });
  });

  it("orders the tasks by group, then Ungrouped", () => {
    const layout: TaskLayout = { groups: [group("g2", "Later", ["c"]), group("g1", "Soon", ["b", "a"])], ungrouped: [] };
    const sections = arrange(layout, [{ id: "a" }, { id: "b" }, { id: "c" }, { id: "d" }]);
    expect(sections.map((s) => [s.group?.name ?? null, ...s.tasks.map((t) => t.id)])).toEqual([
      ["Later", "c"],
      ["Soon", "b", "a"],
      [null, "d"],
    ]);
  });
});

describe("the arrangement's changes", () => {
  const start = (): TaskLayout => ({ groups: [group("g1", "Reports", ["a", "b"]), group("g2", "Chores", [])], ungrouped: ["c", "d"] });

  it("starts from today's list, all of it Ungrouped", () => {
    expect(materialize(null, ["a", "b"])).toEqual({ groups: [], ungrouped: ["a", "b"] });
  });

  it("moves a task inside its group, into another group, and to Ungrouped", () => {
    expect(ids(moveTask(start(), "a", { group: "g1", index: 1 }))).toEqual({ groups: [["Reports", "b", "a"], ["Chores"]], ungrouped: ["c", "d"] });
    expect(ids(moveTask(start(), "c", { group: "g2", index: 0 }))).toEqual({ groups: [["Reports", "a", "b"], ["Chores", "c"]], ungrouped: ["d"] });
    expect(ids(moveTask(start(), "b", { group: null, index: 1 }))).toEqual({ groups: [["Reports", "a"], ["Chores"]], ungrouped: ["c", "b", "d"] });
  });

  it("moves a task one place up or down, and keeps it at either end", () => {
    expect(ids(moveTaskBy(start(), "b", -1)).groups[0]).toEqual(["Reports", "b", "a"]);
    expect(ids(moveTaskBy(start(), "a", -1)).groups[0]).toEqual(["Reports", "a", "b"]);
    expect(ids(moveTaskBy(start(), "d", 1)).ungrouped).toEqual(["c", "d"]);
  });

  it("moves a group among the groups", () => {
    expect(moveGroup(start(), "g2", 0).groups.map((g) => g.name)).toEqual(["Chores", "Reports"]);
    expect(moveGroup(start(), "g1", 9).groups.map((g) => g.name)).toEqual(["Chores", "Reports"]);
  });

  it("adds a group at the end under a name no other group has, ignoring case", () => {
    const { layout, id } = addGroup(start(), "reports");
    expect(layout.groups.map((g) => g.name)).toEqual(["Reports", "Chores", "reports 2"]);
    expect(layout.groups.at(-1)).toEqual({ id, name: "reports 2", collapsed: false, tasks: [] });
  });

  it("renames a group, and refuses an empty name or one another group has, ignoring case", () => {
    expect(renameGroup(start(), "g2", "  Home  ")!.groups[1].name).toBe("Home");
    expect(renameGroup(start(), "g2", "REPORTS")).toBeNull();
    expect(renameGroup(start(), "g2", "  ")).toBeNull();
    expect(renameGroup(start(), "g1", "reports")!.groups[0].name).toBe("reports");
  });

  it("collapses and expands a group", () => {
    expect(setCollapsed(start(), "g1", true).groups[0].collapsed).toBe(true);
    expect(setCollapsed(setCollapsed(start(), "g1", true), "g1", false).groups[0].collapsed).toBe(false);
  });

  it("deletes a group and moves its tasks to the end of Ungrouped", () => {
    expect(ids(deleteGroup(start(), "g1"))).toEqual({ groups: [["Chores"]], ungrouped: ["c", "d", "a", "b"] });
  });

  it("puts a duplicate right after its original, in the same group", () => {
    const arrived = reconcile(start(), ["a", "b", "c", "d", "a-copy"])!;
    expect(ids(placeAfter(arrived, "a", "a-copy")).groups[0]).toEqual(["Reports", "a", "a-copy", "b"]);
    expect(ids(placeAfter(arrived, "c", "a-copy")).ungrouped).toEqual(["c", "a-copy", "d"]);
  });
});
