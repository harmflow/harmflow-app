// What the Form tab's simple mode shows of each task type, read from the task schema's required lists, and how many of
// a task's fields it hides (harmflow-app#553).
import { describe, expect, it } from "vitest";
import { hiddenFieldCount, shownInSimple, simpleFields, simpleParameters } from "./editorMode";

describe("simpleFields", () => {
  it("keeps what a general task needs to validate, its schedule, and whether it is enabled", () => {
    expect(simpleFields("general")).toEqual({
      metadata: { id: null, name: null },
      spec: { type: null, prompt: null, harness: { preferred: { provider: null } }, enabled: null, schedule: null },
    });
  });

  it("adds a GitHub task's repositories and gives a script task its program instead of a prompt and harness", () => {
    expect(simpleFields("github").spec).toMatchObject({
      github: { repositories: { name: null, localPath: null } },
      prompt: null,
      harness: { preferred: { provider: null } },
    });
    expect(Object.keys(simpleFields("script").spec).sort()).toEqual(["enabled", "schedule", "script", "type"]);
    expect(simpleFields("script").spec.script).toEqual({ command: null, inline: { interpreter: null, source: null } });
  });
});

describe("shownInSimple", () => {
  it("shows a required key whole, only the required keys below a key shown in part, and no optional key", () => {
    expect(shownInSimple("general", ["spec", "prompt"])).toBe(true);
    expect(shownInSimple("general", ["spec", "harness", "preferred"])).toBe(true);
    expect(shownInSimple("general", ["spec", "harness", "fallback"])).toBe(false);
    expect(shownInSimple("general", ["spec", "execution"])).toBe(false);
    expect(shownInSimple("general", ["metadata", "description"])).toBe(false);
  });

  it("goes down to the values: a profile's provider but not its model, a repository's path but not its base branch", () => {
    expect(shownInSimple("general", ["spec", "harness", "preferred", "provider"])).toBe(true);
    for (const key of ["installation", "model", "effort", "ephemeral"]) {
      expect(shownInSimple("general", ["spec", "harness", "preferred", key])).toBe(false);
    }
    expect(shownInSimple("github", ["spec", "github", "repositories", "name"])).toBe(true);
    expect(shownInSimple("github", ["spec", "github", "repositories", "localPath"])).toBe(true);
    expect(shownInSimple("github", ["spec", "github", "repositories", "baseBranch"])).toBe(false);
    expect(shownInSimple("github", ["spec", "github", "queue"])).toBe(false);
  });

  it("shows a script's program in either form, and none of its working directory, environment, or systems", () => {
    expect(shownInSimple("script", ["spec", "script", "command"])).toBe(true);
    expect(shownInSimple("script", ["spec", "script", "inline", "interpreter"])).toBe(true);
    expect(shownInSimple("script", ["spec", "script", "inline", "source"])).toBe(true);
    for (const key of ["workingDirectory", "environment", "systems"]) expect(shownInSimple("script", ["spec", "script", key])).toBe(false);
  });

  it("shows a schedule and whether the task is enabled whole", () => {
    expect(shownInSimple("general", ["spec", "schedule", "missedRun"])).toBe(true);
    expect(shownInSimple("general", ["spec", "enabled"])).toBe(true);
    expect(shownInSimple("general", ["spec", "concurrency"])).toBe(false);
    expect(shownInSimple("general", ["spec", "completion"])).toBe(false);
    expect(shownInSimple("general", ["spec", "workspace"])).toBe(false);
  });
});

describe("hiddenFieldCount", () => {
  const base = { metadata: { id: "t", name: "T" }, spec: { type: "general", prompt: "p", harness: { preferred: { provider: "claude-code" } } } };

  it("counts nothing for a task that sets only what simple mode shows", () => {
    expect(hiddenFieldCount(base)).toBe(0);
  });

  it("counts each hidden key, each hidden key below a key shown in part, and each declared value simple mode leaves out", () => {
    const doc = {
      metadata: { ...base.metadata, description: "d" },
      spec: {
        ...base.spec,
        execution: { timeoutMinutes: 5 },
        skills: [],
        harness: { ...base.spec.harness, fallback: { maxSwitches: 1 } },
        parameters: { skills: { notes: { tone: "dry", repo: "x" } } },
      },
    };
    const shown = simpleParameters([{ key: "skills", name: "notes", declared: [{ name: "repo", required: true }, { name: "tone", required: true, default: "warm" }] }]);
    expect(shown).toEqual(new Set(["skills/notes/repo"]));
    expect(hiddenFieldCount(doc, shown)).toBe(4);
  });

  it("counts the hidden keys of the preferred profile, of each repository, and of a script", () => {
    const profile = { metadata: base.metadata, spec: { ...base.spec, harness: { preferred: { provider: "codex", model: "m", effort: "high", ephemeral: false } } } };
    expect(hiddenFieldCount(profile)).toBe(3);

    const github = {
      metadata: base.metadata,
      spec: {
        ...base.spec,
        type: "github",
        github: {
          repositories: [
            { name: "o/a", localPath: "/a", baseBranch: "main" },
            { name: "o/b", localPath: "/b", baseBranch: "dev" },
            { name: "o/c", localPath: "/c" },
          ],
        },
      },
    };
    expect(hiddenFieldCount(github)).toBe(2);

    const script = {
      metadata: base.metadata,
      spec: { type: "script", script: { command: ["/bin/date"], workingDirectory: "/tmp", environment: { TZ: "UTC" }, systems: [] } },
    };
    expect(hiddenFieldCount(script)).toBe(2);
  });
});
