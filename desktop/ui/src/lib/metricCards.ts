// Which metric cards the Overview and the Executions screen show, and in what order: each screen its own list, chosen in
// Settings › Appearance. It is visual only — the desktop keeps it, as it keeps the zoom and the task list's layout, and
// it belongs to this computer: no request to the daemon carries it.

/** A screen with a row of metric cards. */
export type CardScreen = "overview" | "executions";

export const CARD_SCREENS: readonly CardScreen[] = ["overview", "executions"];

/** One card of a screen's list: the card's key, and whether the screen shows it. */
export type CardChoice = { key: string; shown: boolean };

/** What is kept: each screen's list, in its order. A screen with nothing kept shows its default cards. */
export type MetricCards = Partial<Record<CardScreen, CardChoice[]>>;

const STORAGE_KEY = "harmflow.metricCards";

function isChoice(v: unknown): v is CardChoice {
  if (typeof v !== "object" || v === null) return false;
  const c = v as Record<string, unknown>;
  return typeof c.key === "string" && typeof c.shown === "boolean";
}

/** Reads what was kept; an empty choice when nothing was kept or what was kept can't be read. */
export function parseMetricCards(text: string | null): MetricCards {
  if (text === null) return {};
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return {};
  }
  if (typeof v !== "object" || v === null || Array.isArray(v)) return {};
  const out: MetricCards = {};
  for (const screen of CARD_SCREENS) {
    const list = (v as Record<string, unknown>)[screen];
    if (Array.isArray(list) && list.every(isChoice)) out[screen] = list.map((c) => ({ key: c.key, shown: c.shown }));
  }
  return out;
}

export function storedMetricCards(): MetricCards {
  try {
    return parseMetricCards(localStorage.getItem(STORAGE_KEY));
  } catch {
    return {};
  }
}

export function storeMetricCards(cards: MetricCards): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(cards));
  } catch {
    // Without storage the choice lasts until the app quits.
  }
}

/**
 * A screen's list held to the cards the app has, in their default order: a kept card the app no longer has leaves it, a
 * card named twice keeps its first place, and a card the kept list doesn't name joins its end, shown. With nothing
 * kept, every card in the default order, shown.
 */
export function resolveCards(kept: CardChoice[] | undefined, cards: readonly string[]): CardChoice[] {
  const present = new Set(cards);
  const seen = new Set<string>();
  const out: CardChoice[] = [];
  for (const c of kept ?? []) {
    if (!present.has(c.key) || seen.has(c.key)) continue;
    seen.add(c.key);
    out.push({ key: c.key, shown: c.shown });
  }
  for (const key of cards) if (!seen.has(key)) out.push({ key, shown: true });
  return out;
}

/** The list with the card at `index` moved by `by` places, held inside the list. */
export function moveCard(list: CardChoice[], index: number, by: number): CardChoice[] {
  const to = Math.max(0, Math.min(list.length - 1, index + by));
  if (to === index) return list;
  const out = [...list];
  const [card] = out.splice(index, 1);
  out.splice(to, 0, card);
  return out;
}
