// @vitest-environment happy-dom
// The Overview's Spend this month band (harmflow-app#583): cost by default, the view kept across launches, one column
// per day that has passed, a mark on a day that undercounts in the view shown and none on the month's total, a tooltip
// beside the hovered column, and the grid kept for a month with no execution.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { Spend, SpendDay } from "../api/types";
import i18n from "../i18n";
import { SPEND_VIEW_KEY, axisTop, dayMarks, isEmptyMonth } from "../lib/spend";
import { patchStateForTests } from "../state/store";
import { SpendBand } from "./SpendBand";

vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async () => ({ status: 404, body: null }),
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

const none = { unpriced: 0, notReported: 0, partial: 0, running: 0 };
const day = (date: string, costUsd: number, tokens: number, attempts: number, undercount = none): SpendDay => ({
  date,
  costUsd,
  tokens,
  attempts,
  undercount,
});

const october: Spend = {
  month: "2026-10",
  daysInMonth: 31,
  days: [
    day("2026-10-01", 2.5, 1_200_000, 3),
    day("2026-10-02", 0.75, 3_000_000, 2, { ...none, unpriced: 1 }),
    day("2026-10-03", 1, 400_000, 1, { ...none, running: 1 }),
  ],
  total: { costUsd: 4.25, tokens: 4_600_000 },
};

const emptyMonth: Spend = { month: "2026-11", daysInMonth: 30, days: [day("2026-11-01", 0, 0, 0)], total: { costUsd: 0, tokens: 0 } };

let root: Root | undefined;
let host: HTMLElement;

function render(spend: Spend) {
  patchStateForTests({ spend });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<SpendBand />));
}

beforeAll(async () => {
  await i18n.changeLanguage("en-US");
});

beforeEach(() => localStorage.clear());

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  host?.remove();
});

describe("the spend's marks and axis", () => {
  it("marks an unpriced model in the Cost view only, and an attempt still running in both", () => {
    expect(dayMarks(october.days[1], "cost")).toEqual([{ reason: "unpriced", count: 1 }]);
    expect(dayMarks(october.days[1], "tokens")).toEqual([]);
    expect(dayMarks(october.days[2], "cost")).toEqual([{ reason: "running", count: 1 }]);
    expect(dayMarks(october.days[2], "tokens")).toEqual([{ reason: "running", count: 1 }]);
  });

  it("tops the axis at $10 or 2M tokens until the dearest day passes it", () => {
    expect(axisTop(october, "cost")).toBe(10);
    expect(axisTop(october, "tokens")).toBe(3_000_000);
    expect(axisTop({ ...october, days: [day("2026-10-01", 14, 0, 1)] }, "cost")).toBe(14);
  });

  it("reads a month with no attempt as empty", () => {
    expect(isEmptyMonth(emptyMonth)).toBe(true);
    expect(isEmptyMonth(october)).toBe(false);
  });
});

describe("SpendBand", () => {
  it("shows the cost by default, a column per day passed, and the total with no mark", () => {
    render(october);
    const cost = host.querySelector<HTMLButtonElement>('[role="radio"][aria-checked="true"]');
    expect(cost?.textContent).toBe("Cost");
    expect(host.querySelectorAll(".spend__slot")).toHaveLength(31);
    expect(host.querySelectorAll(".spend__slot--day")).toHaveLength(3);
    expect(host.querySelectorAll(".spend__mark--day")).toHaveLength(2);
    const total = host.querySelector(".spend__total");
    expect(total?.textContent).toContain("$4.25");
    expect(total?.textContent).toContain("an estimate at API list prices");
    expect(total?.querySelector(".spend__mark")).toBeNull();
    expect(host.querySelector(".spend__note")?.textContent).toContain("model has no price");
  });

  it("switches to tokens, keeps the choice, and drops the unpriced day's mark", () => {
    render(october);
    const tokens = [...host.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find((b) => b.textContent === "Tokens")!;
    act(() => tokens.click());
    expect(tokens.getAttribute("aria-checked")).toBe("true");
    expect(localStorage.getItem(SPEND_VIEW_KEY)).toBe("tokens");
    expect(host.querySelectorAll(".spend__mark--day")).toHaveLength(1);
    expect(host.querySelector(".spend__total")?.textContent).toContain("4.6M");
  });

  it("opens in the view the last launch kept", () => {
    localStorage.setItem(SPEND_VIEW_KEY, "tokens");
    render(october);
    expect(host.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe("Tokens");
  });

  it("shows a hovered day's figure, its attempts, and why it is marked, beside its column", () => {
    render(october);
    const second = host.querySelectorAll<HTMLElement>(".spend__slot--day")[1];
    act(() => second.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    const tip = host.querySelector<HTMLElement>(".spend__tip");
    expect(tip?.textContent).toContain("$0.7500");
    expect(tip?.textContent).toContain("2 attempts ended");
    expect(tip?.textContent).toContain("1 attempt's model has no price");
    expect(tip?.style.left).toBe(`${(2 / 31) * 100}%`);
  });

  it("keeps the grid and the axis for a month with no execution, with a total of zero", () => {
    render(emptyMonth);
    expect(host.querySelector(".spend__empty")?.textContent).toBe("No execution this month");
    expect(host.querySelectorAll(".spend__grid")).toHaveLength(3);
    expect(host.querySelectorAll(".spend__slot--day")).toHaveLength(1);
    expect(host.querySelector(".spend__total")?.textContent).toContain("$0.0000");
  });
});
