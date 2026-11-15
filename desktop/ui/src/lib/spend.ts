// The Overview's band of the month's spend per day (harmflow-app#583): what each view shows of a day, what marks it, the
// top of the axis, and how the Harness toggle splits a day (harmflow-app#970). The daemon sums the days and each
// harness's share of them; this only reads them.
import type { Spend, SpendDay } from "../api/types";

/** The units the band shows, in the order of its switch: the estimated cost in USD, or the tokens, every class summed. */
export const SPEND_VIEWS = ["cost", "tokens"] as const;
export type SpendView = (typeof SPEND_VIEWS)[number];

/** Where the band's view is kept across launches, as the sidebar's state is. */
export const SPEND_VIEW_KEY = "harmflow.spendView";

/** The view kept by the last launch; cost, the default, when none was kept. */
export function keptSpendView(): SpendView {
  try {
    return localStorage.getItem(SPEND_VIEW_KEY) === "tokens" ? "tokens" : "cost";
  } catch {
    return "cost";
  }
}

export function keepSpendView(view: SpendView) {
  try {
    localStorage.setItem(SPEND_VIEW_KEY, view);
  } catch {
    // A storage the webview refuses keeps nothing; the band still switches.
  }
}

/** Where the band's Harness toggle is kept across launches, as its view is (harmflow-app#970). */
export const SPEND_SPLIT_KEY = "harmflow.spendSplit";

/** Whether the last launch kept the split by harness on; off, the default, when none was kept or the value is unknown. */
export function keptSpendSplit(): boolean {
  try {
    return localStorage.getItem(SPEND_SPLIT_KEY) === "on";
  } catch {
    return false;
  }
}

export function keepSpendSplit(on: boolean) {
  try {
    localStorage.setItem(SPEND_SPLIT_KEY, on ? "on" : "off");
  } catch {
    // A storage the webview refuses keeps nothing; the band still splits.
  }
}

/** The harnesses the split stacks first, from a column's foot; any other comes after them, by its identifier. */
export const SPLIT_ORDER = ["claude-code", "codex"];

function compareHarnesses(a: string, b: string): number {
  const rank = (h: string) => (SPLIT_ORDER.includes(h) ? SPLIT_ORDER.indexOf(h) : SPLIT_ORDER.length);
  return rank(a) - rank(b) || a.localeCompare(b);
}

/** The harnesses that ran an attempt this month, in the split's order: the legend's, and the only ones a column stacks. */
export function monthHarnesses(spend: Spend): string[] {
  return [...new Set(spend.days.flatMap((d) => d.harnesses.map((h) => h.harness)))].sort(compareHarnesses);
}

/** A day's share of each harness in the view's unit, in the split's order, leaving out a harness with none in it. */
export function daySegments(day: SpendDay, view: SpendView): { harness: string; value: number }[] {
  return day.harnesses
    .map((h) => ({ harness: h.harness, value: view === "cost" ? h.costUsd : h.tokens }))
    .filter((s) => s.value > 0)
    .sort((a, b) => compareHarnesses(a.harness, b.harness));
}

/** The axis tops out at no less than this until the month's dearest day passes it, so one day never fills the chart. */
export const AXIS_FLOOR: Record<SpendView, number> = { cost: 10, tokens: 2_000_000 };

export function dayValue(day: SpendDay, view: SpendView): number {
  return view === "cost" ? day.costUsd : day.tokens;
}

/** The month's total in the view's unit. */
export function totalValue(spend: Spend, view: SpendView): number {
  return view === "cost" ? spend.total.costUsd : spend.total.tokens;
}

export function axisTop(spend: Spend, view: SpendView): number {
  return Math.max(AXIS_FLOOR[view], ...spend.days.map((d) => dayValue(d, view)));
}

/** The reasons a day's figure undercounts, in the order the band lists them. */
export const SPEND_REASONS = ["unpriced", "notReported", "partial", "running"] as const;
export type SpendReason = (typeof SPEND_REASONS)[number];

/**
 * The reasons a day's figure undercounts in the view, with the attempts behind each. A model with no price leaves only
 * the cost short, since its tokens are known; no reported usage, a partial count, or an attempt still running leave
 * both short.
 */
export function dayMarks(day: SpendDay, view: SpendView): { reason: SpendReason; count: number }[] {
  const reasons = view === "cost" ? SPEND_REASONS : SPEND_REASONS.filter((reason) => reason !== "unpriced");
  return reasons.map((reason) => ({ reason, count: day.undercount[reason] })).filter((m) => m.count > 0);
}

/** A month with no execution: no attempt ended in it, and none runs. */
export function isEmptyMonth(spend: Spend): boolean {
  return spend.days.every((d) => d.attempts === 0 && d.undercount.running === 0);
}
