// Which view the task screen shows: the list, a new task's editor, or an existing task's editor.

/** A new task names the template it starts from; its `pick` counts the template picks, so that each one opens an editor of
 * its own. */
export type TaskView = { kind: "list" } | { kind: "new"; template: string; pick?: number } | { kind: "edit"; id: string };
