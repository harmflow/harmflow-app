import { describe, expect, it } from "vitest";
import type { Catalog } from "../api/types";
import { schemaDefault, schemaEnum } from "./schema";
import { getIn, newTaskDocument, scheduleKind, setIn, setScheduleKind, slugify } from "./taskForm";

const catalog: Catalog = {
  agents: [],
  skills: [],
  taskTypes: [
    {
      id: "github-issue-implementation",
      parametersKey: "githubIssueImplementation",
      defaultAgents: ["harmflow-github-issue-implementer"],
      defaultSkills: ["harmflow-github-draft-pr"],
      defaultPrompt: "Implement the oldest issue labeled agent-ready.",
    },
    { id: "general", parametersKey: null, defaultAgents: [], defaultSkills: [] },
  ],
};

describe("form ↔ document", () => {
  it("writes a field at its path without touching the rest", () => {
    const doc = { metadata: { id: "a", name: "A" }, spec: { type: "general" } };
    const next = setIn(doc, ["spec", "execution", "timeoutMinutes"], 20);
    expect(next).toEqual({ metadata: { id: "a", name: "A" }, spec: { type: "general", execution: { timeoutMinutes: 20 } } });
    expect(doc.spec).toEqual({ type: "general" });
  });

  it("removes a cleared field and the containers it leaves empty, so the default applies", () => {
    const doc = { spec: { type: "general", execution: { timeoutMinutes: 20 } } };
    expect(setIn(doc, ["spec", "execution", "timeoutMinutes"], undefined)).toEqual({ spec: { type: "general" } });
    expect(setIn(doc, ["spec", "execution", "timeoutMinutes"], "")).toEqual({ spec: { type: "general" } });
  });

  it("reads nested values and missing ones", () => {
    const doc = { spec: { harness: { preferred: { provider: "codex" } } } };
    expect(getIn(doc, ["spec", "harness", "preferred", "provider"])).toBe("codex");
    expect(getIn(doc, ["spec", "schedule", "cron"])).toBeUndefined();
  });

  it("switches the schedule kind and keeps the time zone", () => {
    let doc: Record<string, unknown> = { spec: { schedule: { timezone: "America/Sao_Paulo" } } };
    expect(scheduleKind(doc)).toBe("none");
    doc = setScheduleKind(doc, "cron");
    expect(scheduleKind(doc)).toBe("cron");
    doc = setScheduleKind(doc, "interval");
    expect(getIn(doc, ["spec", "schedule"])).toEqual({ timezone: "America/Sao_Paulo", interval: "1h" });
    doc = setScheduleKind(doc, "none");
    expect(getIn(doc, ["spec", "schedule"])).toEqual({ timezone: "America/Sao_Paulo" });
  });

  it("builds a GitHub task from the catalog, with repositories only for that type", () => {
    const github = newTaskDocument("github-issue-implementation", catalog, "Implement GitHub issues");
    expect(github).toMatchObject({
      apiVersion: "harmflow/v1alpha1",
      kind: "Task",
      metadata: { id: "implement-github-issues", name: "Implement GitHub issues" },
      spec: {
        type: "github-issue-implementation",
        agents: ["harmflow-github-issue-implementer"],
        skills: ["harmflow-github-draft-pr"],
        githubIssueImplementation: { repositories: [{ baseBranch: "main" }] },
      },
    });
    const general = newTaskDocument("general", catalog, "Triagem de e-mail");
    expect(getIn(general, ["spec", "githubIssueImplementation"])).toBeUndefined();
    expect(getIn(general, ["metadata", "id"])).toBe("triagem-de-e-mail");
  });

  it("makes kebab-case identifiers", () => {
    expect(slugify("  Revisão de PRs — Core!  ")).toBe("revisao-de-prs-core");
  });
});

describe("schema", () => {
  it("reads the defaults of D-082 from the contract", () => {
    expect(schemaDefault(["spec", "execution", "timeoutMinutes"])).toBe(60);
    expect(schemaDefault(["spec", "githubIssueImplementation", "queue", "maxItemsPerCycle"])).toBe(1);
    expect(schemaDefault(["spec", "githubIssueImplementation", "queue", "ordering"])).toBe("oldest_first");
    expect(schemaDefault(["spec", "harness", "fallback", "maxSwitches"])).toBe(1);
    expect(schemaDefault(["spec", "execution", "cancelGraceSeconds"])).toBe(30);
  });

  it("reads closed sets from the contract", () => {
    expect(schemaEnum(["spec", "type"])).toEqual(["github-issue-implementation", "general"]);
    expect(schemaEnum(["spec", "schedule", "missedRun", "policy"])).toEqual(["run_on_resume", "skip"]);
    expect(schemaEnum(["spec", "notifications", "native", "events"])).toContain("pull_request.created");
  });
});
