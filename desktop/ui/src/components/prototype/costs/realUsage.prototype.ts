// PROTOTYPE — throwaway. Reads token usage out of an attempt's raw capture, the way the daemon's adapters would have to,
// so the Costs tab prototype can show a real execution. Rules found in real Claude Code 2.1.239 and Codex 0.163.0 runs:
// - Claude Code repeats a message's `usage` on every content-block line: count each `message.id` once.
// - Those per-message `output_tokens` are provisional (1–4); the true output arrives only on the `result` line.
// - `result.modelUsage` (per model) is the complete count, auxiliary calls included; with it and the 1-hour cache-write
//   price, the price table reproduces `total_cost_usd` exactly.
// - Codex reports usage once, on `turn.completed`; its `input_tokens` include the cached ones; it never names its model.

export interface Tokens {
  input: number;
  cacheWrite: number;
  cacheRead: number;
  output: number;
}

export interface ParsedStep extends Tokens {
  label: string;
  /** Output tokens on this step are a lower bound until the result line. */
  provisional?: boolean;
}

export interface ParsedAttempt {
  /** reported: the harness printed its cost; partial: no result line, output unknown; none: no usage at all. */
  kind: "reported" | "tokens-only" | "partial" | "none";
  steps: ParsedStep[];
  /** Canonical model ids with their full token counts, from `modelUsage`. */
  models: string[];
  reportedCostUsd?: number;
  /** Share of cache writes at the 1-hour tier, from `usage.cache_creation`. */
  cacheWrite1hShare: number;
  apiKeySource?: string;
  harnessVersion?: string;
}

const ZERO: Tokens = { input: 0, cacheWrite: 0, cacheRead: 0, output: 0 };

function* jsonLines(text: string): Generator<Record<string, unknown>> {
  for (const raw of text.split("\n")) {
    const i = raw.indexOf("{");
    if (i < 0) continue;
    try {
      yield JSON.parse(raw.slice(i)) as Record<string, unknown>;
    } catch {
      // a line cut by the capture's start offset, or plain stderr
    }
  }
}

type U = Record<string, number | undefined> & { cache_creation?: Record<string, number> };

export function parseClaude(text: string): ParsedAttempt {
  const byId = new Map<string, { usage: U; label: string }>();
  let result: Record<string, unknown> | undefined;
  let init: Record<string, unknown> | undefined;
  for (const o of jsonLines(text)) {
    if (o.type === "system" && o.subtype === "init") init = o;
    if (o.type === "assistant") {
      const m = o.message as { id?: string; usage?: U; content?: Array<{ type: string; name?: string }> };
      const id = m.id ?? `line-${byId.size}`;
      const tool = m.content?.find((b) => b.type === "tool_use")?.name;
      const text = m.content?.some((b) => b.type === "text") ? "Agent message" : undefined;
      const prev = byId.get(id)?.label;
      byId.set(id, { usage: m.usage ?? {}, label: tool ?? text ?? (prev && prev !== "Thinking" ? prev : "Thinking") });
    }
    if (o.type === "result") result = o;
  }
  const steps: ParsedStep[] = [...byId.values()].map(({ usage: u, label }) => ({
    label,
    input: u.input_tokens ?? 0,
    cacheWrite: u.cache_creation_input_tokens ?? 0,
    cacheRead: u.cache_read_input_tokens ?? 0,
    output: u.output_tokens ?? 0,
    provisional: true,
  }));
  const base = { apiKeySource: init?.apiKeySource as string | undefined, harnessVersion: init?.claude_code_version as string | undefined };
  if (!result) {
    return { kind: steps.length ? "partial" : "none", steps, models: [], cacheWrite1hShare: 1, ...base };
  }
  const usage = (result.usage ?? {}) as U;
  const cc = usage.cache_creation ?? {};
  const w1 = cc.ephemeral_1h_input_tokens ?? 0;
  const w5 = cc.ephemeral_5m_input_tokens ?? 0;
  const modelUsage = (result.modelUsage ?? {}) as Record<string, { inputTokens: number; cacheCreationInputTokens: number; cacheReadInputTokens: number; outputTokens: number }>;
  const full = Object.values(modelUsage).reduce<Tokens>(
    (a, m) => ({ input: a.input + m.inputTokens, cacheWrite: a.cacheWrite + m.cacheCreationInputTokens, cacheRead: a.cacheRead + m.cacheReadInputTokens, output: a.output + m.outputTokens }),
    ZERO,
  );
  const seen = steps.reduce<Tokens>((a, s) => ({ input: a.input + s.input, cacheWrite: a.cacheWrite + s.cacheWrite, cacheRead: a.cacheRead + s.cacheRead, output: a.output + s.output }), ZERO);
  // The result line settles what the per-message lines could not: the real output, and calls they never showed.
  steps.push({
    label: "result line — final output, auxiliary calls",
    input: Math.max(0, full.input - seen.input),
    cacheWrite: Math.max(0, full.cacheWrite - seen.cacheWrite),
    cacheRead: Math.max(0, full.cacheRead - seen.cacheRead),
    output: Math.max(0, full.output - seen.output),
  });
  return {
    kind: "reported",
    steps,
    models: Object.keys(modelUsage),
    reportedCostUsd: typeof result.total_cost_usd === "number" ? result.total_cost_usd : undefined,
    cacheWrite1hShare: w1 + w5 ? w1 / (w1 + w5) : 1,
    ...base,
  };
}

export function parseCodex(text: string): ParsedAttempt {
  const steps: ParsedStep[] = [];
  for (const o of jsonLines(text)) {
    if (o.type !== "turn.completed") continue;
    const u = (o.usage ?? {}) as U;
    const cached = u.cached_input_tokens ?? 0;
    steps.push({
      label: "turn.completed",
      input: Math.max(0, (u.input_tokens ?? 0) - cached),
      cacheWrite: u.cache_write_input_tokens ?? 0,
      cacheRead: cached,
      output: u.output_tokens ?? 0,
    });
  }
  return { kind: steps.length ? "tokens-only" : "none", steps, models: [], cacheWrite1hShare: 0 };
}

/** USD per million tokens — only the models whose price the real runs confirmed. */
export const REAL_PRICES: Record<string, { name: string; input: number; cacheWrite5m: number; cacheWrite1h: number; cacheRead: number; output: number }> = {
  "claude-haiku-4-5-20251001": { name: "Claude Haiku 4.5", input: 1, cacheWrite5m: 1.25, cacheWrite1h: 2, cacheRead: 0.1, output: 5 },
};
