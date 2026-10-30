import { describe, expect, it } from "vitest";
import { schemaDefault, schemaEnum } from "./schema";
import {
  addedFolders,
  envToLines,
  getIn,
  linesToArgs,
  linesToEnv,
  programKind,
  scheduleKind,
  setIn,
  setProgramKind,
  setScheduleKind,
  slugify,
  type Doc,
} from "./taskForm";

describe("added folders", () => {
  it("are the repositories' local paths and a custom profile's writable paths, which the catalog is asked for", () => {
    const spec = (permissions: Doc) => ({
      spec: {
        github: { repositories: [{ name: "o/a", localPath: "~/a" }, { name: "o/b" }, { name: "o/c", localPath: "~/c" }] },
        permissions,
      },
    });
    expect(addedFolders(spec({ profile: "custom", custom: { writablePaths: ["~/out", 3] } }))).toEqual(["~/a", "~/c", "~/out"]);
    expect(addedFolders(spec({ profile: "workspace_write", custom: { writablePaths: ["~/out"] } }))).toEqual(["~/a", "~/c"]);
    expect(addedFolders({ spec: { type: "general" } })).toEqual([]);
    expect(addedFolders({})).toEqual([]);
  });
});

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

  it("makes kebab-case identifiers", () => {
    expect(slugify("  Revisão de PRs — Core!  ")).toBe("revisao-de-prs-core");
  });
});

describe("schema", () => {
  it("reads the defaults from the contract", () => {
    expect(schemaDefault(["spec", "execution", "timeoutMinutes"])).toBe(60);
    expect(schemaDefault(["spec", "github", "queue", "maxItemsPerCycle"])).toBe(1);
    expect(schemaDefault(["spec", "github", "queue", "ordering"])).toBe("oldest_first");
    expect(schemaDefault(["spec", "harness", "fallback", "maxSwitches"])).toBe(1);
    expect(schemaDefault(["spec", "execution", "cancelGraceSeconds"])).toBe(30);
  });

  it("reads closed sets from the contract", () => {
    expect(schemaEnum(["spec", "type"])).toEqual(["github", "general", "script"]);
    expect(schemaEnum(["spec", "schedule", "missedRun", "policy"])).toEqual(["run_on_resume", "skip"]);
    expect(schemaEnum(["spec", "notifications", "native", "events"])).toContain("pull_request.created");
    expect(schemaEnum(["spec", "script", "systems"])).toEqual(["macos", "linux", "windows"]);
  });
});

describe("script tasks", () => {
  it("switches between an argument list and an inline script, keeping the rest", () => {
    let doc: Doc = setIn({ spec: { type: "script", script: {} } }, ["spec", "script", "workingDirectory"], "~/jobs");
    expect(programKind(doc)).toBe("command");
    doc = setProgramKind(doc, "inline");
    expect(getIn(doc, ["spec", "script"])).toEqual({ workingDirectory: "~/jobs", inline: { interpreter: ["/bin/sh"] } });
    expect(programKind(doc)).toBe("inline");
    doc = setProgramKind(doc, "command");
    expect(getIn(doc, ["spec", "script"])).toEqual({ workingDirectory: "~/jobs", command: [""] });
  });

  it("keeps each argument as typed, spaces included", () => {
    expect(linesToArgs("/bin/echo\n  two words \n\n$HOME; rm x")).toEqual(["/bin/echo", "  two words ", "$HOME; rm x"]);
  });

  it("reads and writes environment variables as NAME=value lines", () => {
    expect(linesToEnv("TZ=America/Sao_Paulo\nQUERY=a=b\n\n=nameless\nEMPTY")).toEqual({
      TZ: "America/Sao_Paulo",
      QUERY: "a=b",
      EMPTY: "",
    });
    expect(envToLines({ A: "1", B: "x=y" })).toBe("A=1\nB=x=y");
  });
});
