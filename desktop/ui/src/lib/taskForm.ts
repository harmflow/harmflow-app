// The task form edits the task document itself, field by field. A field the user clears is removed, so the document —
// and the YAML the daemon writes from it — holds only what the user set; defaults stay implicit.
import type { Catalog } from "../api/types";

export type Doc = Record<string, unknown>;
export type Path = readonly (string | number)[];

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** True for a value the document should not keep: undefined, an empty string, an empty array, an empty object. */
export function isEmptyValue(v: unknown): boolean {
  if (v === undefined || v === null) return true;
  if (typeof v === "string") return v.trim() === "";
  if (Array.isArray(v)) return v.length === 0;
  if (isPlainObject(v)) return Object.keys(v).length === 0;
  return false;
}

export function getIn<T = unknown>(doc: unknown, path: Path): T | undefined {
  let current: unknown = doc;
  for (const segment of path) {
    if (current === null || current === undefined) return undefined;
    current = (current as Record<string | number, unknown>)[segment];
  }
  return current as T | undefined;
}

/** Returns a copy of doc with value at path, pruning containers left empty by a cleared value. */
export function setIn<T extends Doc>(doc: T, path: Path, value: unknown): T {
  if (path.length === 0) return doc;
  const [head, ...rest] = path;
  const container: Record<string | number, unknown> | unknown[] = Array.isArray(doc)
    ? [...(doc as unknown[])]
    : { ...(doc as Record<string, unknown>) };
  const target = container as Record<string | number, unknown>;

  if (rest.length === 0) {
    if (isEmptyValue(value)) {
      if (Array.isArray(container)) container.splice(head as number, 1);
      else delete target[head];
    } else {
      target[head] = value;
    }
  } else {
    const child = target[head];
    const base = (isPlainObject(child) || Array.isArray(child) ? child : typeof rest[0] === "number" ? [] : {}) as Doc;
    const next = setIn(base, rest, value);
    if (isEmptyValue(next)) {
      if (Array.isArray(container)) container.splice(head as number, 1);
      else delete target[head];
    } else {
      target[head] = next;
    }
  }
  return container as T;
}

/** A kebab-case identifier from a name: "Implement GitHub issues" → "implement-github-issues". */
export function slugify(name: string): string {
  return name
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64)
    .replace(/-+$/g, "");
}

export function linesToList(text: string): string[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
}

export function listToLines(list: unknown): string {
  return Array.isArray(list) ? list.join("\n") : "";
}

export type TemplateKind = "github-issue-implementation" | "general" | "script";

const BASE_DOCUMENT = { apiVersion: "harmflow/v1alpha1", kind: "Task" } as const;

/** A new task of a type, with the type's built-in agents, skills, and prompt as the catalog declares them. */
export function newTaskDocument(kind: TemplateKind, catalog: Catalog | undefined, name: string): Doc {
  if (kind === "script") {
    return { ...BASE_DOCUMENT, metadata: { id: slugify(name), name }, spec: { enabled: true, type: "script", script: { command: [""] } } };
  }
  const type = catalog?.taskTypes.find((t) => t.id === kind);
  const spec: Doc = {
    enabled: true,
    type: kind,
    prompt: type?.defaultPrompt ?? "",
    harness: { preferred: { provider: "claude-code" } },
  };
  if (type?.defaultAgents.length) spec.agents = [...type.defaultAgents];
  if (type?.defaultSkills.length) spec.skills = [...type.defaultSkills];
  if (kind === "github-issue-implementation") {
    spec.githubIssueImplementation = { repositories: [{ name: "", localPath: "", baseBranch: "main" }] };
  }
  return {
    ...BASE_DOCUMENT,
    metadata: { id: slugify(name), name },
    spec,
  };
}

/** Which form a script task's program takes: an argument list, or an inline script. */
export function programKind(doc: Doc): "command" | "inline" {
  return getIn(doc, ["spec", "script", "inline"]) !== undefined ? "inline" : "command";
}

/** Switches the program's form, keeping the working directory, the environment, and the systems. */
export function setProgramKind(doc: Doc, kind: "command" | "inline"): Doc {
  if (kind === programKind(doc)) return doc;
  if (kind === "inline") {
    const next = setIn(doc, ["spec", "script", "command"], undefined);
    return setIn(next, ["spec", "script", "inline"], { interpreter: ["/bin/sh"] });
  }
  const next = setIn(doc, ["spec", "script", "inline"], undefined);
  return setIn(next, ["spec", "script", "command"], [""]);
}

/** An argument list, one argument per line; an empty line is dropped and nothing else is trimmed. */
export function linesToArgs(text: string): string[] {
  return text.split("\n").filter((l) => l !== "");
}

/** Environment variables as NAME=value lines. */
export function envToLines(env: unknown): string {
  if (!env || typeof env !== "object") return "";
  return Object.entries(env as Record<string, string>)
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

/** Reads NAME=value lines; the value is everything after the first =, and a line with no name is dropped. */
export function linesToEnv(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const i = line.indexOf("=");
    const name = (i < 0 ? line : line.slice(0, i)).trim();
    if (name) out[name] = i < 0 ? "" : line.slice(i + 1);
  }
  return out;
}

/** Which kind of schedule the document holds. */
export function scheduleKind(doc: Doc): "none" | "cron" | "interval" {
  if (getIn(doc, ["spec", "schedule", "cron"]) !== undefined) return "cron";
  if (getIn(doc, ["spec", "schedule", "interval"]) !== undefined) return "interval";
  return "none";
}

/** Switches the schedule kind, keeping the time zone and the missed-run policy. */
export function setScheduleKind(doc: Doc, kind: "none" | "cron" | "interval"): Doc {
  let next = setIn(doc, ["spec", "schedule", "cron"], undefined);
  next = setIn(next, ["spec", "schedule", "interval"], undefined);
  if (kind === "cron") next = setIn(next, ["spec", "schedule", "cron"], "0 9 * * 1-5");
  if (kind === "interval") next = setIn(next, ["spec", "schedule", "interval"], "1h");
  return next;
}
