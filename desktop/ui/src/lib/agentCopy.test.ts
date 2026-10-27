import { describe, expect, it } from "vitest";
import type { Catalog, CatalogEntry } from "../api/types";
import {
  applyAgentCopy,
  changedOrigin,
  clearOrigin,
  copyChanges,
  copyTypeAgent,
  leftOut,
  lineDiff,
  originOf,
  promptEdited,
  type AgentCopy,
} from "./agentCopy";
import type { Doc } from "./taskForm";

const HASH = "sha256:" + "a".repeat(64);
const NEWER = "sha256:" + "b".repeat(64);

const reviewer: AgentCopy = {
  prompt: "You review the change.",
  description: "Reviews a change",
  model: "sonnet",
  modelHarnesses: ["claude-code"],
  skills: ["review-notes"],
  omittedFields: ["tools", "color"],
  hash: HASH,
};

const entry = (copy: AgentCopy, name = "reviewer"): CatalogEntry => ({ name, kind: "agent", source: "user", harnesses: ["claude-code"], copy });

const task = (spec: Doc = {}): Doc => ({
  apiVersion: "harmflow/v1alpha1",
  kind: "Task",
  metadata: { id: "review", name: "Review" },
  spec: { type: "general", prompt: "", harness: { preferred: { provider: "claude-code" } }, skills: ["style"], ...spec },
});

describe("copying an agent into a task", () => {
  it("copies the instructions, description, model, and skills, and records the origin", () => {
    const doc = applyAgentCopy(task(), "reviewer", reviewer);
    expect(doc.metadata).toMatchObject({ description: "Reviews a change" });
    expect(doc.spec).toMatchObject({
      prompt: "You review the change.",
      harness: { preferred: { provider: "claude-code", model: "sonnet" } },
      skills: ["style", "review-notes"],
      origin: { agent: "reviewer", hash: HASH },
    });
    expect(originOf(doc)).toEqual({ agent: "reviewer", hash: HASH });
  });

  it("leaves out a model the task's harness does not accept, and names it with the fields a task has no place for", () => {
    const doc = applyAgentCopy(task({ harness: { preferred: { provider: "codex" } } }), "reviewer", reviewer);
    expect(doc.spec).toMatchObject({ harness: { preferred: { provider: "codex" } } });
    expect((doc.spec as Doc).harness).toEqual({ preferred: { provider: "codex" } });
    expect(leftOut(doc, reviewer)).toEqual(["tools", "color", "model"]);
    expect(leftOut(applyAgentCopy(task(), "reviewer", reviewer), reviewer)).toEqual(["tools", "color"]);
  });

  it("keeps what was copied when the origin is cleared", () => {
    const doc = clearOrigin(applyAgentCopy(task(), "reviewer", reviewer));
    expect(originOf(doc)).toBeUndefined();
    expect((doc.spec as Doc).prompt).toBe("You review the change.");
  });
});

describe("asking before a copy replaces the prompt", () => {
  const agents = [entry(reviewer)];

  it("asks for a prompt the user wrote", () => {
    expect(promptEdited(task({ prompt: "My own words." }), agents)).toBe(true);
    const copied = applyAgentCopy(task(), "reviewer", reviewer);
    const edited = { ...copied, spec: { ...(copied.spec as Doc), prompt: "You review the change. Twice." } };
    expect(promptEdited(edited, agents)).toBe(true);
  });

  it("does not ask for an empty prompt or the agent's copy as it was", () => {
    expect(promptEdited(task(), agents)).toBe(false);
    expect(promptEdited(applyAgentCopy(task(), "reviewer", reviewer), agents)).toBe(false);
  });
});

describe("the agent a type selects", () => {
  const catalog = (selectedAgent?: string, agents = [entry(reviewer)]): Catalog => ({
    agents,
    skills: [],
    problems: [],
    taskTypes: [{ id: "general", parametersKey: null, defaultAgents: [], defaultSkills: [], selectedAgent }],
  });

  it("is copied into a new task of the type, as choosing it does", () => {
    expect(copyTypeAgent(task(), catalog("reviewer"))).toEqual(applyAgentCopy(task(), "reviewer", reviewer));
  });

  it("leaves the task as it is when the type selects none, or the catalog has no copy of the agent", () => {
    expect(copyTypeAgent(task(), catalog())).toEqual(task());
    expect(copyTypeAgent(task(), catalog("reviewer", []))).toEqual(task());
    expect(copyTypeAgent(task(), catalog("reviewer", [{ ...entry(reviewer), copy: undefined }]))).toEqual(task());
    expect(copyTypeAgent(task(), undefined)).toEqual(task());
  });
});

describe("the notice of a changed origin", () => {
  const copied = applyAgentCopy(task(), "reviewer", reviewer);

  it("shows when the agent's content no longer matches the hash", () => {
    const newer = { ...reviewer, prompt: "You review the change, then the tests.", hash: NEWER };
    expect(changedOrigin(copied, [entry(newer)])).toEqual(newer);
  });

  it("does not show while the hash matches, nor once the agent no longer exists", () => {
    expect(changedOrigin(copied, [entry(reviewer)])).toBeUndefined();
    expect(changedOrigin(copied, [entry(reviewer, "other")])).toBeUndefined();
    expect(changedOrigin(copied, [])).toBeUndefined();
    expect(changedOrigin(task(), [entry(reviewer)])).toBeUndefined();
  });
});

describe("the fields copying again changes", () => {
  const copied = applyAgentCopy(task(), "reviewer", reviewer);

  it("lists the description, the model, and the skills the copy replaces, from the task's value to the agent's", () => {
    const newer = { ...reviewer, description: "Reviews a change and its tests", model: "opus", skills: ["review-notes", "tests"], hash: NEWER };
    expect(copyChanges(copied, "reviewer", newer)).toEqual([
      { field: "description", from: "Reviews a change", to: "Reviews a change and its tests" },
      { field: "model", from: "sonnet", to: "opus" },
      { field: "skills", from: "style, review-notes", to: "style, review-notes, tests" },
    ]);
    expect(copyChanges(task(), "reviewer", reviewer)).toEqual([
      { field: "description", from: "", to: "Reviews a change" },
      { field: "model", from: "", to: "sonnet" },
      { field: "skills", from: "style", to: "style, review-notes" },
    ]);
  });

  it("lists nothing the copy keeps: a skill the agent dropped, an empty description, or a model the harness does not accept", () => {
    expect(copyChanges(copied, "reviewer", { ...reviewer, skills: [], hash: NEWER })).toEqual([]);
    expect(copyChanges(copied, "reviewer", { ...reviewer, description: "", hash: NEWER })).toEqual([]);
    expect(copyChanges(copied, "reviewer", { ...reviewer, model: "gpt-5", modelHarnesses: ["codex"], hash: NEWER })).toEqual([]);
    expect(copyChanges(copied, "reviewer", { ...reviewer, prompt: "You review.", hash: NEWER })).toEqual([]);
  });
});

describe("lineDiff", () => {
  it("marks the lines removed from the first text and added by the second", () => {
    expect(lineDiff("a\nb\nc", "a\nc\nd")).toEqual([
      { kind: "same", text: "a" },
      { kind: "removed", text: "b" },
      { kind: "same", text: "c" },
      { kind: "added", text: "d" },
    ]);
  });

  it("keeps equal texts as they are", () => {
    expect(lineDiff("one\ntwo", "one\ntwo").every((l) => l.kind === "same")).toBe(true);
  });
});
