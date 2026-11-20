// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from "vitest";
import { moveCard, parseMetricCards, resolveCards, storeMetricCards, storedMetricCards, type MetricCards } from "./metricCards";

const CARDS = ["running", "queued", "completedToday", "failedToday"];

afterEach(() => localStorage.clear());

describe("the kept metric cards", () => {
  it("are read back as they were kept, under harmflow.metricCards", () => {
    const cards: MetricCards = { overview: [{ key: "queued", shown: false }, { key: "running", shown: true }] };
    storeMetricCards(cards);
    expect(JSON.parse(localStorage.getItem("harmflow.metricCards")!)).toEqual(cards);
    expect(storedMetricCards()).toEqual(cards);
  });

  it("read as nothing kept when nothing was kept or what was kept can't be read", () => {
    expect(storedMetricCards()).toEqual({});
    for (const text of ["not json", "[]", "null", '{"overview": [{"key": 1}]}', '{"overview": "running"}']) {
      expect(parseMetricCards(text), text).toEqual({});
    }
  });

  it("keep a screen whose list reads and leave out one whose list doesn't", () => {
    const text = JSON.stringify({ overview: [{ key: "running", shown: true }], executions: [{ key: 2 }], other: [] });
    expect(parseMetricCards(text)).toEqual({ overview: [{ key: "running", shown: true }] });
  });
});

describe("a screen's list", () => {
  it("is every card in the default order, shown, with nothing kept", () => {
    expect(resolveCards(undefined, CARDS)).toEqual(CARDS.map((key) => ({ key, shown: true })));
  });

  it("keeps the kept order and visibility, drops a card the app no longer has, and appends a new card shown", () => {
    const kept = [
      { key: "failedToday", shown: true },
      { key: "gone", shown: true },
      { key: "queued", shown: false },
      { key: "running", shown: true },
      { key: "queued", shown: true },
    ];
    expect(resolveCards(kept, [...CARDS, "noWorkToday"])).toEqual([
      { key: "failedToday", shown: true },
      { key: "queued", shown: false },
      { key: "running", shown: true },
      { key: "completedToday", shown: true },
      { key: "noWorkToday", shown: true },
    ]);
  });

  it("moves a card up or down by one place, and not past either end", () => {
    const list = resolveCards(undefined, CARDS);
    expect(moveCard(list, 2, -1).map((c) => c.key)).toEqual(["running", "completedToday", "queued", "failedToday"]);
    expect(moveCard(list, 2, 1).map((c) => c.key)).toEqual(["running", "queued", "failedToday", "completedToday"]);
    expect(moveCard(list, 0, -1)).toBe(list);
    expect(moveCard(list, 3, 1)).toBe(list);
  });
});
