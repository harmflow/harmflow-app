// Reads defaults and closed sets straight from contracts/schemas/task.schema.json, so the form never keeps a copy of
// them. Validation itself is the daemon's: the form, the YAML editor, and import share it.
import taskSchema from "../../../../contracts/schemas/task.schema.json";

type SchemaNode = {
  $ref?: string;
  type?: string | string[];
  properties?: Record<string, SchemaNode>;
  items?: SchemaNode;
  enum?: readonly unknown[];
  const?: unknown;
  default?: unknown;
  description?: string;
  minimum?: number;
  maximum?: number;
  pattern?: string;
};

const root = taskSchema as unknown as SchemaNode & { $defs: Record<string, SchemaNode> };

function deref(node: SchemaNode | undefined): SchemaNode | undefined {
  let current = node;
  for (let guard = 0; current?.$ref && guard < 10; guard++) {
    const name = current.$ref.replace("#/$defs/", "");
    current = root.$defs[name];
  }
  return current;
}

/** The schema node at a document path, such as ["spec", "execution", "timeoutMinutes"]; numbers step into arrays. */
export function schemaAt(path: readonly (string | number)[]): SchemaNode | undefined {
  let node = deref(root);
  for (const segment of path) {
    if (!node) return undefined;
    if (typeof segment === "number") node = deref(node.items);
    else node = deref(node.properties?.[segment]);
  }
  return node;
}

export function schemaDefault<T = unknown>(path: readonly (string | number)[]): T | undefined {
  return schemaAt(path)?.default as T | undefined;
}

export function schemaEnum(path: readonly (string | number)[]): string[] {
  const node = schemaAt(path);
  const values = node?.enum ?? deref(node?.items)?.enum ?? [];
  return values.filter((v): v is string => typeof v === "string");
}

export function schemaRange(path: readonly (string | number)[]): { min?: number; max?: number } {
  const node = schemaAt(path);
  return { min: node?.minimum, max: node?.maximum };
}

export const HARNESS_PROVIDERS = schemaEnum(["spec", "harness", "preferred", "provider"]);
export const NOTIFIABLE_EVENTS = schemaEnum(["spec", "notifications", "native", "events"]);
export const ID_PATTERN = new RegExp(schemaAt(["metadata", "id"])?.pattern ?? "^[a-z0-9-]+$");
