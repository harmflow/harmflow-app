// The Costs tab's arithmetic (harmflow-app#39): each attempt's tokens and estimated cost as the daemon recorded them,
// their sum across a failover, and the receipt's lines — tokens × price = amount. The daemon sends numbers and enums
// only (D-099); every word is the tab's.
import type { Attempt } from "../api/types";

/** The token classes of the summary; a receipt splits cache writes by lifetime, which are priced apart. */
export const SUMMARY_CLASSES = ["input", "cacheWrite", "cacheRead", "output"] as const;
export const RECEIPT_CLASSES = ["input", "cacheWriteShort", "cacheWriteLong", "cacheRead", "output"] as const;
export type SummaryClass = (typeof SUMMARY_CLASSES)[number];
export type ReceiptClass = (typeof RECEIPT_CLASSES)[number];
export type Tokens = Record<SummaryClass, number>;

type Usage = NonNullable<Attempt["usage"]>;
type ModelUsage = Usage["models"][number];

/**
 * Where an attempt's figure comes from. reported, estimated, unpriced: the daemon's cost source of a final count;
 * running: the count so far, priced when the attempt ends; partial: the attempt ended before its harness reported
 * the output; none: no usage reported.
 */
export type FigureSource = "reported" | "estimated" | "unpriced" | "running" | "partial" | "none";

export interface ReceiptLine {
  model?: string;
  tokenClass: ReceiptClass;
  tokens: number;
  /** USD per million tokens. */
  price?: number;
  amount?: number;
}

export interface AttemptCost {
  attempt: Attempt;
  source: FigureSource;
  /** The model that priced the attempt: the harness's full id, or the attempt's own when nothing named it. */
  model: string;
  modelSource?: Usage["modelSource"];
  /** Undefined when the harness reported no usage, which is not zero. */
  tokens?: Tokens;
  /** The output is known only from the harness's final count. */
  outputKnown: boolean;
  cost?: number;
  /** The cost by class at the price table, when it prices every model of the attempt. */
  costs?: Tokens;
  lines: ReceiptLine[];
  /** The price table's split differs from the cost the harness reported. */
  splitDiffers: boolean;
  pricesAsOf?: string;
}

const ZERO: Tokens = { input: 0, cacheWrite: 0, cacheRead: 0, output: 0 };

export function sum(list: Tokens[]): Tokens {
  return list.reduce(
    (a, b) => ({ input: a.input + b.input, cacheWrite: a.cacheWrite + b.cacheWrite, cacheRead: a.cacheRead + b.cacheRead, output: a.output + b.output }),
    ZERO,
  );
}

export const totalOf = (t: Tokens): number => t.input + t.cacheWrite + t.cacheRead + t.output;

function summaryTokens(m: ModelUsage["tokens"]): Tokens {
  return { input: m.input, cacheWrite: m.cacheWriteShort + m.cacheWriteLong, cacheRead: m.cacheRead, output: m.output };
}

function figureSource(a: Attempt): FigureSource {
  const u = a.usage;
  if (a.outcome === "running") return "running";
  if (!u || u.status === "not_reported") return "none";
  if (u.status === "partial") return "partial";
  return u.costSource ?? "unpriced";
}

export function attemptCost(a: Attempt): AttemptCost {
  const u = a.usage;
  const source = figureSource(a);
  const models = u && u.status !== "not_reported" ? u.models : [];
  const reported = u?.status === "reported";
  const lines: ReceiptLine[] = [];
  const byClass: Tokens[] = [];
  let everyModelPriced = models.length > 0;
  for (const m of models) {
    for (const c of RECEIPT_CLASSES) {
      const tokens = m.tokens[c];
      if (tokens <= 0 || (c === "output" && !reported)) continue;
      const price = m.price?.[c];
      const amount = reported && price !== undefined ? (tokens * price) / 1e6 : undefined;
      lines.push({ model: m.model, tokenClass: c, tokens, price: m.price ? price : undefined, amount });
    }
    if (reported && m.price) {
      const p = m.price;
      const t = m.tokens;
      byClass.push({
        input: (t.input * p.input) / 1e6,
        cacheWrite: (t.cacheWriteShort * p.cacheWriteShort + t.cacheWriteLong * p.cacheWriteLong) / 1e6,
        cacheRead: (t.cacheRead * p.cacheRead) / 1e6,
        output: (t.output * p.output) / 1e6,
      });
    } else {
      everyModelPriced = false;
    }
  }
  const tokens = models.length ? sum(models.map((m) => summaryTokens(m.tokens))) : undefined;
  const cost = reported && (u.costSource === "reported" || u.costSource === "estimated") ? u.costUsd : undefined;
  const costs = everyModelPriced ? sum(byClass) : undefined;
  return {
    attempt: a,
    source,
    model: models.find((m) => m.model)?.model ?? a.model,
    modelSource: u?.modelSource,
    tokens,
    outputKnown: reported,
    cost,
    costs,
    lines,
    splitDiffers: u?.costSource === "reported" && cost !== undefined && costs !== undefined && Math.abs(totalOf(costs) - cost) >= 0.00005,
    pricesAsOf: u?.pricesAsOf,
  };
}

export interface ExecutionCost {
  attempts: AttemptCost[];
  /** Undefined when no attempt reported usage. */
  tokens?: Tokens;
  cost?: number;
  /** The cost by class, only when every attempt with tokens is priced: a partial split would misstate the mix. */
  costs?: Tokens;
  /** An attempt with tokens, or still running, adds no cost to the total: it is a lower bound. */
  incomplete: boolean;
  /** Milliseconds the attempts ran, the base of the cost per minute. */
  runMs: number;
  pricesAsOf?: string;
}

export function executionCost(attempts: Attempt[], now: number = Date.now()): ExecutionCost {
  const list = attempts.map(attemptCost);
  const counted = list.filter((a) => a.tokens);
  const priced = list.filter((a) => a.cost !== undefined);
  const runMs = attempts.reduce((ms, a) => {
    const from = Date.parse(a.startedAt);
    const to = a.endedAt ? Date.parse(a.endedAt) : now;
    return Number.isNaN(from) || Number.isNaN(to) ? ms : ms + Math.max(0, to - from);
  }, 0);
  return {
    attempts: list,
    tokens: counted.length ? sum(counted.map((a) => a.tokens!)) : undefined,
    cost: priced.length ? priced.reduce((n, a) => n + a.cost!, 0) : undefined,
    costs: counted.length && counted.every((a) => a.costs) ? sum(counted.map((a) => a.costs!)) : undefined,
    incomplete: list.some((a) => a.cost === undefined && (a.tokens !== undefined || a.source === "running")),
    runMs,
    pricesAsOf: list.map((a) => a.pricesAsOf).find(Boolean),
  };
}

/** USD in the locale's format: four decimals under a dollar, and never a rounded-away nonzero amount. */
export function formatUsd(value: number | undefined, locale: string): string {
  if (value === undefined) return "—";
  const digits = Math.abs(value) < 1 ? 4 : 2;
  const f = (v: number) => new Intl.NumberFormat(locale, { style: "currency", currency: "USD", minimumFractionDigits: digits, maximumFractionDigits: digits }).format(v);
  return value > 0 && value < 0.0001 ? `< ${f(0.0001)}` : f(value);
}

/** A price per million tokens, such as $1.25. */
export function formatRate(value: number | undefined, locale: string): string {
  if (value === undefined) return "—";
  return new Intl.NumberFormat(locale, { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 3 }).format(value);
}

export function formatCompact(value: number, locale: string): string {
  return new Intl.NumberFormat(locale, { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

export function formatPercent(value: number, locale: string): string {
  return new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 0 }).format(value);
}
