import { describe, expect, it } from "vitest";
import type { HarmflowEvent } from "../api/types";
import i18n from "../i18n";
import { buildTimeline, changedFilesFromEvents, currentStepFrom } from "./timeline";

let nextId = 1;
function ev(type: string, extra: Partial<HarmflowEvent> = {}): HarmflowEvent {
  return {
    id: nextId++,
    schemaVersion: "1",
    timestamp: "2026-10-20T19:32:14-03:00",
    executionId: "exec-1",
    type,
    severity: "info",
    ...extra,
  };
}

describe("buildTimeline", () => {
  it("folds a tool's completion into its start", () => {
    const events = [
      ev("execution.started"),
      ev("tool.started", { payload: { toolUseId: "t1", tool: "Bash", input: { command: "./gradlew test" } } }),
      ev("tool.completed", { payload: { toolUseId: "t1", isError: false, output: "BUILD SUCCESSFUL" } }),
    ];
    const items = buildTimeline(events);
    expect(items).toHaveLength(2);
    expect(items[1]).toMatchObject({ detail: "./gradlew test", icon: "terminal", completed: { ok: true } });
  });

  it("marks a failed tool", () => {
    const items = buildTimeline([
      ev("tool.started", { payload: { toolUseId: "t2", tool: "Bash", command: "npm test" } }),
      ev("tool.completed", { payload: { toolUseId: "t2", exitCode: 1 } }),
    ]);
    expect(items[0].completed?.ok).toBe(false);
  });

  it("shows the daemon's messages in the interface's language, the agent's and tools' as they are", async () => {
    await i18n.changeLanguage("pt-BR");
    try {
      const items = buildTimeline([
        ev("harness.rate_limited", {
          message: "rate limited; retrying on Codex in 30s (retry 1 of 3)",
          messageKey: "harness_rate_limited",
          params: { harness: "Codex", wait: "30s", retry: "1", max: "3" },
        }),
        ev("harness.message", { message: "I opened the draft pull request." }),
        ev("tool.completed", { message: "Bash failed", messageKey: "tool_failed", params: { tool: "Bash" }, payload: { tool: "Bash" } }),
        ev("step.started", { message: "Running the tests" }),
      ]);
      expect(items.map((i) => i.detail ?? i.title)).toEqual([
        "limite de taxa; nova tentativa em Codex daqui a 30s (tentativa 1 de 3)",
        "I opened the draft pull request.",
        "Bash falhou",
        "Running the tests",
      ]);
    } finally {
      await i18n.changeLanguage("en-US");
    }
  });

  it("keeps raw output out of the timeline and orders by id", () => {
    const a = ev("harness.started");
    const raw = ev("harness.output", { message: "> Task :compileJava" });
    const b = ev("step.started", { message: "Creating worktree", payload: { detail: "git worktree add ../123-agent" } });
    const items = buildTimeline([b, raw, a]);
    expect(items.map((i) => i.event.type)).toEqual(["harness.started", "step.started"]);
    expect(items[1]).toMatchObject({ title: "Creating worktree", detail: "git worktree add ../123-agent" });
  });

  it("shows failovers and unenforced restrictions with their own tone", () => {
    const items = buildTimeline([
      ev("harness.switched", { message: "claude-code → codex" }),
      ev("policy.unenforced", { severity: "warning", message: "network: deny" }),
    ]);
    expect(items.map((i) => i.tone)).toEqual(["magenta", "warning"]);
  });

  it("shows a run on an untested harness version as a warning naming the version", () => {
    const [item] = buildTimeline([
      ev("harness.version_untested", {
        severity: "warning",
        harness: "codex",
        message: "Codex 0.999.0 is newer than the versions HarmFlow was tested with",
        messageKey: "harness_version_untested",
        params: { harness: "Codex", version: "0.999.0" },
      }),
    ]);
    expect(item).toMatchObject({ tone: "warning", icon: "alert" });
    expect(item.detail ?? item.title).toContain("0.999.0");
  });

  it("shows a harness's wait for the network as a warning quoting the harness", () => {
    const notice = "Reconnecting... waiting for network (Connection failed: error sending request)";
    const [item] = buildTimeline([ev("harness.network_waiting", { severity: "warning", message: notice })]);
    expect(item).toMatchObject({ tone: "warning", icon: "alert", labelKey: "eventTypes.harness.network_waiting", detail: notice });
  });

  it("drops duplicates delivered twice by replay and live stream", () => {
    const e = ev("execution.started");
    expect(buildTimeline([e, e])).toHaveLength(1);
  });
});

describe("derived views", () => {
  it("lists the changed files the events name", () => {
    const files = changedFilesFromEvents([
      ev("workspace.changed", { payload: { paths: ["src/a.ts", "src/b.ts"] } }),
      ev("tool.started", { payload: { tool: "Edit", input: { file_path: "src/a.ts" } } }),
      ev("tool.started", { payload: { tool: "Write", input: { file_path: "src/c.ts" } } }),
    ]);
    expect(files).toEqual(["src/a.ts", "src/b.ts", "src/c.ts"]);
  });

  it("takes the latest step as the current one", () => {
    const first = ev("execution.started");
    const last = ev("test.started", { message: "./gradlew test" });
    expect(currentStepFrom([last, first])?.id).toBe(last.id);
  });
});
