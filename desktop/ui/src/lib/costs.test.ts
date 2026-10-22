// The Costs tab's arithmetic (harmflow-app#39), on the figures of real runs the daemon recorded.
import { describe, expect, it } from "vitest";
import type { Attempt } from "../api/types";
import { attemptCost, executionCost, formatUsd } from "./costs";

const haiku = { input: 1, cacheWriteShort: 1.25, cacheWriteLong: 2, cacheRead: 0.1, output: 5 };

// Claude Code 2.1.239 on Haiku 4.5: the CLI printed $0.0364015, and the price table reproduces it.
const claudeRun: Attempt = {
  number: 1,
  harness: "claude-code",
  model: "claude-haiku-4-5-20251001",
  configuredModel: "haiku",
  configuredEffort: "default",
  startedAt: "2026-10-21T09:00:00Z",
  endedAt: "2026-10-21T09:00:30Z",
  outcome: "succeeded",
  usage: {
    status: "reported",
    modelSource: "harness",
    costSource: "reported",
    costUsd: 0.0364015,
    pricesAsOf: "2026-10-22",
    models: [
      {
        model: "claude-haiku-4-5-20251001",
        tokens: { input: 25, cacheWriteShort: 0, cacheWriteLong: 12752, cacheRead: 65425, output: 866 },
        price: haiku,
        costUsd: 0.0364015,
      },
    ],
  },
};

// Codex 0.163.0 at the model its configuration sets, estimated from the table.
const codexRun = (price?: typeof haiku, model = "gpt-test"): Attempt => ({
  number: 2,
  harness: "codex",
  configuredModel: "default",
  configuredEffort: "default",
  startedAt: "2026-10-21T09:01:00Z",
  endedAt: "2026-10-21T09:02:30Z",
  outcome: "succeeded",
  usage: {
    status: "reported",
    modelSource: model ? "config" : undefined,
    costSource: price ? "estimated" : "unpriced",
    costUsd: price ? 0.05 : undefined,
    pricesAsOf: "2026-10-22",
    models: [{ model: model || undefined, tokens: { input: 24355, cacheWriteShort: 0, cacheWriteLong: 0, cacheRead: 135680, output: 1194 }, price, costUsd: price ? 0.05 : undefined }],
  },
});

describe("costs", () => {
  it("shows the cost the CLI reported, split by the price table, line by line", () => {
    const a = attemptCost(claudeRun);
    expect(a.source).toBe("reported");
    expect(a.model).toBe("claude-haiku-4-5-20251001");
    expect(a.cost).toBe(0.0364015);
    expect(a.tokens).toEqual({ input: 25, cacheWrite: 12752, cacheRead: 65425, output: 866 });
    expect(a.costs!.input + a.costs!.cacheWrite + a.costs!.cacheRead + a.costs!.output).toBeCloseTo(0.0364015, 9);
    expect(a.splitDiffers).toBe(false);
    expect(a.lines.map((l) => [l.tokenClass, l.tokens, l.price])).toEqual([
      ["input", 25, 1],
      ["cacheWriteLong", 12752, 2],
      ["cacheRead", 65425, 0.1],
      ["output", 866, 5],
    ]);
  });

  it("sums a failover's attempts, and marks the total short when one ended without a cost", () => {
    const quota: Attempt = {
      ...claudeRun,
      outcome: "quota_exhausted",
      usage: { status: "partial", modelSource: "harness", models: [{ model: "claude-haiku-4-5-20251001", tokens: { input: 9, cacheWriteShort: 0, cacheWriteLong: 11511, cacheRead: 14129, output: 0 } }] },
    };
    const x = executionCost([quota, codexRun(haiku)]);
    expect(x.attempts.map((a) => a.source)).toEqual(["partial", "estimated"]);
    expect(x.attempts[0].outputKnown).toBe(false);
    expect(x.attempts[0].lines.some((l) => l.tokenClass === "output")).toBe(false);
    expect(x.tokens).toEqual({ input: 9 + 24355, cacheWrite: 11511, cacheRead: 14129 + 135680, output: 1194 });
    expect(x.cost).toBe(0.05);
    expect(x.costs).toBeUndefined(); // no split by class that leaves out the partial attempt
    expect(x.incomplete).toBe(true);
    expect(x.runMs).toBe(30_000 + 90_000);
  });

  it("never shows a harness that reported nothing as zero", () => {
    const none = executionCost([{ ...claudeRun, usage: undefined }, { ...claudeRun, number: 2, usage: { status: "not_reported", models: [] } }]);
    expect(none.attempts.map((a) => a.source)).toEqual(["none", "none"]);
    expect(none.tokens).toBeUndefined();
    expect(none.cost).toBeUndefined();
    expect(none.incomplete).toBe(false);
  });

  it("prices a running attempt only when it ends", () => {
    const running: Attempt = { ...claudeRun, outcome: "running", endedAt: undefined, usage: { status: "partial", models: claudeRun.usage!.models.map((m) => ({ model: m.model, tokens: { ...m.tokens, output: 0 } })) } };
    const x = executionCost([running], Date.parse("2026-10-21T09:02:00Z"));
    expect(x.attempts[0].source).toBe("running");
    expect(x.cost).toBeUndefined();
    expect(x.incomplete).toBe(true);
    expect(x.runMs).toBe(120_000);
  });

  it("shows the tokens without a cost when no model is named or priced", () => {
    const a = attemptCost(codexRun(undefined, ""));
    expect(a.source).toBe("unpriced");
    expect(a.modelSource).toBeUndefined();
    // Codex reported no model, and the configured default never stands in for one (D-105).
    expect(a.model).toBeUndefined();
    expect(a.cost).toBeUndefined();
    expect(a.tokens?.cacheRead).toBe(135680);
    expect(a.lines.every((l) => l.amount === undefined && l.price === undefined)).toBe(true);
  });

  it("formats USD for the locale, without rounding a small cost away", () => {
    expect(formatUsd(0.0364015, "en-US")).toBe("$0.0364");
    expect(formatUsd(12.345, "en-US")).toBe("$12.35");
    expect(formatUsd(0.00001, "en-US")).toBe("< $0.0001");
    expect(formatUsd(undefined, "en-US")).toBe("—");
    expect(formatUsd(0.0364015, "pt-BR")).toBe("US$ 0,0364");
  });
});
