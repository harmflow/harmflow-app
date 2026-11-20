// The Form tab's simple and advanced modes (harmflow-app#553). Simple mode shows the fields a task of its type needs to
// validate, plus its schedule and whether it is enabled; advanced mode shows every field the schema accepts. The fields
// a task needs come from the `required` lists of contracts/schemas/task.schema.json, so a new optional field lands in
// advanced mode with no change here. The mode is a view choice of the user's, kept across launches; it is not part of
// any task.
import taskSchema from "../../../../contracts/schemas/task.schema.json";
import type { Doc } from "./taskForm";

export const EDITOR_MODES = ["simple", "advanced"] as const;
export type EditorMode = (typeof EDITOR_MODES)[number];

/** Where the last mode chosen is kept, as the sidebar's state is. */
export const EDITOR_MODE_KEY = "harmflow.editorMode";

/** The mode the user last chose; advanced, the form as it always was, when none was kept. */
export function keptEditorMode(): EditorMode {
  try {
    return localStorage.getItem(EDITOR_MODE_KEY) === "simple" ? "simple" : "advanced";
  } catch {
    return "advanced";
  }
}

export function keepEditorMode(mode: EditorMode) {
  try {
    localStorage.setItem(EDITOR_MODE_KEY, mode);
  } catch {
    // A storage the webview refuses keeps nothing; the switch still works.
  }
}

type Node = {
  $ref?: string;
  type?: string;
  required?: string[];
  properties?: Record<string, Node>;
  items?: Node;
  anyOf?: Node[];
  oneOf?: Node[];
  allOf?: Branch[];
};
type Branch = { if?: { properties?: { type?: { const?: string } } }; then?: { required?: string[] }; else?: { required?: string[] } };
const defs = (taskSchema as unknown as { $defs: Record<string, Node> }).$defs;

function deref(node: Node | undefined): Node | undefined {
  let current = node;
  for (let guard = 0; current?.$ref && guard < 10; guard++) current = defs[current.$ref.replace("#/$defs/", "")];
  return current;
}

/**
 * What simple mode shows of a key: null for the whole key, or the keys below it it shows, each with what it shows of
 * it. A list is read as one of its items, so a path below one names no index.
 */
export type Shown = { [key: string]: Shown } | null;

/**
 * What simple mode shows below a schema node: nothing to choose for a value with no keys below it, so the whole of it;
 * otherwise the keys it requires, its own and those of each alternative it accepts (a script's command or inline
 * source), each read the same way down to the values. A key no list requires is in advanced mode, at any depth.
 */
function shownBelow(node: Node | undefined, depth = 0): Shown {
  let n = deref(node);
  if (n?.type === "array" && n.items) n = deref(n.items);
  if (!n?.properties || depth > 10) return null;
  const keys = new Set(n.required ?? []);
  for (const alternative of [...(n.anyOf ?? []), ...(n.oneOf ?? [])]) for (const k of deref(alternative)?.required ?? []) keys.add(k);
  const shown: Record<string, Shown> = {};
  for (const k of keys) shown[k] = shownBelow(n.properties[k], depth + 1);
  return shown;
}

/** The keys of `spec` a task of the type must set: the spec's own list and each branch of its type's conditions. */
function specRequired(type: string): string[] {
  const spec = defs.spec;
  const keys = new Set(spec.required ?? []);
  for (const branch of spec.allOf ?? []) {
    const applies = branch.if?.properties?.type?.const === type;
    for (const k of (applies ? branch.then?.required : branch.else?.required) ?? []) keys.add(k);
  }
  return [...keys];
}

/**
 * What simple mode shows of a task of the type, as a map from each key of `metadata` and `spec` it shows to what it
 * shows of that key (`Shown`): the keys the type requires, and below each the keys it requires in turn, at any depth,
 * plus the schedule and whether the task is enabled, whole.
 */
export function simpleFields(type: string): { metadata: Record<string, Shown>; spec: Record<string, Shown> } {
  const properties = deref(defs.spec)?.properties ?? {};
  const spec: Record<string, Shown> = {};
  for (const key of specRequired(type)) spec[key] = shownBelow(properties[key]);
  spec.enabled = null;
  spec.schedule = null;
  const metadata: Record<string, Shown> = {};
  for (const key of defs.metadata.required ?? []) metadata[key] = shownBelow(defs.metadata.properties?.[key]);
  return { metadata, spec };
}

/**
 * Whether simple mode shows the field at a path of `metadata` or `spec`, such as ["spec", "harness", "preferred",
 * "model"]; a path below a list names no index (["spec", "github", "repositories", "baseBranch"]).
 */
export function shownInSimple(type: string, path: readonly string[]): boolean {
  const [top, ...keys] = path;
  const fields = simpleFields(type);
  let shown: Shown | undefined = top === "metadata" ? fields.metadata : top === "spec" ? fields.spec : undefined;
  if (!shown || keys.length === 0) return false;
  for (const key of keys) {
    if (shown === null) return true;
    if (!(key in shown)) return false;
    shown = shown[key];
  }
  return true;
}

/** Whether a value is set: a key with an empty string, list, or object counts as not set. */
function isSet(value: unknown): boolean {
  if (value === undefined || value === null || value === "") return false;
  if (Array.isArray(value)) return value.length > 0;
  if (typeof value === "object") return Object.values(value as object).some(isSet);
  return true;
}

/**
 * How many fields the task sets that simple mode hides: each set key simple mode does not show, at any depth below the
 * keys it shows in part — once in each item of a list, such as each repository's base branch — and each value of a
 * declared parameter it does not show.
 */
export function hiddenFieldCount(doc: Doc, shownParameters: ReadonlySet<string> = new Set()): number {
  const spec = (doc.spec ?? {}) as Record<string, unknown>;
  const type = typeof spec.type === "string" ? spec.type : "general";
  const fields = simpleFields(type);
  let count = 0;
  const countIn = (values: Record<string, unknown>, shown: Record<string, Shown>) => {
    for (const [key, value] of Object.entries(values)) {
      if (key === "parameters" && values === spec) continue;
      if (!isSet(value)) continue;
      const below = shown[key];
      if (!(key in shown)) count++;
      else if (below && typeof value === "object") {
        for (const item of Array.isArray(value) ? value : [value]) {
          if (item && typeof item === "object" && !Array.isArray(item)) countIn(item as Record<string, unknown>, below);
        }
      }
    }
  };
  countIn((doc.metadata ?? {}) as Record<string, unknown>, fields.metadata);
  countIn(spec, fields.spec);
  const parameters = (spec.parameters ?? {}) as Record<string, Record<string, Record<string, unknown>>>;
  for (const [kind, items] of Object.entries(parameters)) {
    for (const [name, values] of Object.entries(items ?? {})) {
      for (const [param, v] of Object.entries(values ?? {})) if (isSet(v) && !shownParameters.has(`${kind}/${name}/${param}`)) count++;
    }
  }
  return count;
}

/** The declared parameters simple mode shows, as `<skills|agents>/<name>/<parameter>`: the required ones with no default. */
export function simpleParameters(groups: { key: string; name: string; declared: { name: string; required?: boolean; default?: unknown }[] }[]): Set<string> {
  const out = new Set<string>();
  for (const g of groups) for (const d of g.declared) if (d.required && d.default === undefined) out.add(`${g.key}/${g.name}/${d.name}`);
  return out;
}
