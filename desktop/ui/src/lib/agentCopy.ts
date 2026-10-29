// Selecting an agent copies it into the task once: its instructions become the prompt, its description the task's,
// its model the task's when the task's harness accepts it, and its skills join the task's. The task keeps the agent's
// name and the hash of what was copied as its origin only; the copy never follows the agent, and an agent that no
// longer exists changes nothing.
import type { CatalogEntry } from "../api/types";
import { getIn, setIn, type Doc, type Path } from "./taskForm";

export interface Origin {
  agent: string;
  hash: string;
}

export type AgentCopy = NonNullable<CatalogEntry["copy"]>;

/** The agent the task's prompt was copied from, when it records one. */
export function originOf(doc: Doc): Origin | undefined {
  const o = getIn<Partial<Origin>>(doc, ["spec", "origin"]);
  return o?.agent && o.hash ? { agent: o.agent, hash: o.hash } : undefined;
}

/** The harness the task prefers, Claude Code when the document leaves it out, as the form shows it. */
function preferredHarness(doc: Doc): string {
  return getIn<string>(doc, ["spec", "harness", "preferred", "provider"]) ?? "claude-code";
}

/** The task with the agent copied into it, and its origin recorded. */
export function applyAgentCopy(doc: Doc, name: string, copy: AgentCopy): Doc {
  let next = setIn(doc, ["spec", "prompt"], copy.prompt);
  if (copy.description) next = setIn(next, ["metadata", "description"], copy.description);
  if (copy.model && copy.modelHarnesses.includes(preferredHarness(doc))) {
    next = setIn(next, ["spec", "harness", "preferred", "model"], copy.model);
  }
  const skills = getIn<string[]>(next, ["spec", "skills"]) ?? [];
  const added = copy.skills.filter((s) => !skills.includes(s));
  if (added.length) next = setIn(next, ["spec", "skills"], [...skills, ...added]);
  return setIn(next, ["spec", "origin"], { agent: name, hash: copy.hash });
}

/** The task without an origin; what was copied stays, as the task's own. */
export function clearOrigin(doc: Doc): Doc {
  return setIn(doc, ["spec", "origin"], undefined);
}

/**
 * The fields of the agent the copy leaves out: the front-matter fields a task has no place for, and the model when the
 * task's harness does not accept it.
 */
export function leftOut(doc: Doc, copy: AgentCopy): string[] {
  const out = [...copy.omittedFields];
  if (copy.model && !copy.modelHarnesses.includes(preferredHarness(doc))) out.push("model");
  return out;
}

/**
 * Whether replacing the prompt could lose the user's words: a prompt that is neither empty nor what the origin agent's
 * copy holds now.
 */
export function promptEdited(doc: Doc, agents: CatalogEntry[]): boolean {
  const prompt = getIn<string>(doc, ["spec", "prompt"]) ?? "";
  if (!prompt.trim()) return false;
  const origin = originOf(doc);
  const copied = origin ? agents.find((a) => a.name === origin.agent)?.copy : undefined;
  return copied?.prompt !== prompt;
}

/** The origin agent's current copy, when that agent still exists and its content no longer matches the hash. */
export function changedOrigin(doc: Doc, agents: CatalogEntry[]): AgentCopy | undefined {
  const origin = originOf(doc);
  if (!origin) return undefined;
  const copy = agents.find((a) => a.name === origin.agent)?.copy;
  return copy && copy.hash !== origin.hash ? copy : undefined;
}

export interface FieldChange {
  field: "description" | "model" | "skills";
  from: string;
  to: string;
}

const COPIED_FIELDS: { field: FieldChange["field"]; path: Path }[] = [
  { field: "description", path: ["metadata", "description"] },
  { field: "model", path: ["spec", "harness", "preferred", "model"] },
  { field: "skills", path: ["spec", "skills"] },
];

/**
 * The fields besides the prompt that copying the agent into the task changes, each with its value before and after.
 * They are read from what the copy does, so a field it keeps, such as a skill the agent dropped, is not listed.
 */
export function copyChanges(doc: Doc, name: string, copy: AgentCopy): FieldChange[] {
  const next = applyAgentCopy(doc, name, copy);
  const text = (v: unknown) => (Array.isArray(v) ? v.join(", ") : typeof v === "string" ? v : "");
  return COPIED_FIELDS.flatMap(({ field, path }) => {
    const from = text(getIn(doc, path));
    const to = text(getIn(next, path));
    return from === to ? [] : [{ field, from, to }];
  });
}

export interface DiffLine {
  kind: "same" | "removed" | "added";
  text: string;
}

/** The lines that change from one text to the other, by their longest common subsequence. */
export function lineDiff(from: string, to: string): DiffLine[] {
  const a = from.split("\n");
  const b = to.split("\n");
  // lcs[i][j] is the length of the longest common subsequence of a[i:] and b[j:].
  const lcs = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ kind: "same", text: a[i] });
      i++;
      j++;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      out.push({ kind: "removed", text: a[i++] });
    } else {
      out.push({ kind: "added", text: b[j++] });
    }
  }
  while (i < a.length) out.push({ kind: "removed", text: a[i++] });
  while (j < b.length) out.push({ kind: "added", text: b[j++] });
  return out;
}
