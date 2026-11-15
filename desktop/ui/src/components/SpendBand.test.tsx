// @vitest-environment happy-dom
// The Overview's Spend this month band (harmflow-app#583): cost by default, the view kept across launches, one column
// per day that has passed, a mark on a day that undercounts in the view shown and none on the month's total, a tooltip
// beside the hovered column, and the grid kept for a month with no execution. Its Harness toggle (harmflow-app#970):
// off by default and kept across launches, independent of the view, stacking each column by harness in a fixed order
// and color, with a legend of the month's harnesses, and leaving the tooltip and the marks to the day.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { Spend, SpendDay, SpendHarness } from "../api/types";
import i18n from "../i18n";
import { SPEND_SPLIT_KEY, SPEND_VIEW_KEY, axisTop, dayMarks, daySegments, isEmptyMonth, monthHarnesses } from "../lib/spend";
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
/** A day of the spend, every attempt of it Claude Code's unless the harnesses are given. */
const day = (
  date: string,
  costUsd: number,
  tokens: number,
  attempts: number,
  undercount = none,
  harnesses: SpendHarness[] = attempts ? [{ harness: "claude-code", costUsd, tokens }] : [],
): SpendDay => ({
  date,
  costUsd,
  tokens,
  attempts,
  undercount,
  harnesses,
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

function render(spend: Spend | undefined) {
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

describe("SpendBand's title", () => {
  const title = () => host.querySelector(".panel__title")?.textContent;

  beforeEach(() => {
    // The clock sits in another month and year than the spend shown, so the title can only come from the spend's month.
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2027-01-15T12:00:00"));
  });

  afterEach(async () => {
    vi.useRealTimers();
    await i18n.changeLanguage("en-US");
  });

  it("names the month and year of the spend's month", () => {
    render({ ...emptyMonth, month: "2026-11" });
    expect(title()).toBe("Spend this month - Nov, 2026");
  });

  it("names a month of another year", () => {
    render({ ...emptyMonth, month: "2025-12", daysInMonth: 31, days: [day("2025-12-01", 0, 0, 0)] });
    expect(title()).toBe("Spend this month - Dec, 2025");
  });

  it("names the month in the interface's language", async () => {
    await i18n.changeLanguage("pt-BR");
    render({ ...emptyMonth, month: "2026-11" });
    expect(title()).toMatch(/^Gasto deste mês - nov\.?, 2026$/i);
  });

  it("shows no month while the spend loads", () => {
    render(undefined);
    expect(title()).toBe("Spend this month");
  });
});

/** The band's days of October up to the given one, each with an attempt and no reason to undercount. */
function octoberTo(last: number): Spend {
  const days = Array.from({ length: last }, (_, i) => day(`2026-10-${String(i + 1).padStart(2, "0")}`, 0.5, 100_000, 1));
  return { month: "2026-10", daysInMonth: 31, days, total: { costUsd: 0.5 * last, tokens: 100_000 * last } };
}

function hover(index: number): HTMLElement {
  const column = host.querySelectorAll<HTMLElement>(".spend__slot--day")[index];
  act(() => column.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
  return host.querySelector<HTMLElement>(".spend__tip")!;
}

function unhover(index: number) {
  const column = host.querySelectorAll<HTMLElement>(".spend__slot--day")[index];
  act(() => column.dispatchEvent(new MouseEvent("mouseout", { bubbles: true })));
}

function chooseView(label: "Cost" | "Tokens") {
  const button = [...host.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find((b) => b.textContent === label)!;
  act(() => button.click());
}

describe("SpendBand's marks, columns, and tooltip", () => {
  afterEach(() => vi.useRealTimers());

  it("marks a day with a partial count in the cost view and in the token view, with the line that explains it", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-10-03T12:00:00"));
    // An attempt that ended with its input tokens counted, no output tokens, and so no cost, its count partial.
    const partial: Spend = {
      month: "2026-10",
      daysInMonth: 31,
      days: [day("2026-10-01", 1, 300_000, 1), day("2026-10-02", 0, 12_000, 1, { ...none, partial: 1 }), day("2026-10-03", 0.5, 90_000, 1)],
      total: { costUsd: 1.5, tokens: 402_000 },
    };
    render(partial);
    const marked = () => [...host.querySelectorAll(".spend__slot--day")].map((slot) => slot.querySelector(".spend__mark--day") !== null);

    expect(marked()).toEqual([false, true, false]);
    expect(host.querySelector(".spend__note")?.textContent).toContain(i18n.t("overview.spend.markNoteCost"));

    chooseView("Tokens");
    expect(marked()).toEqual([false, true, false]);
    expect(host.querySelector(".spend__note")?.textContent).toContain(i18n.t("overview.spend.markNoteTokens"));
  });

  it("shows on the 5th five columns from the left and none for the days to come, each one block of the day's tokens", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-05T12:00:00"));
    // The daemon sums every token class of a day's attempts — input, output, cache — into its tokens.
    const fifth: Spend = {
      month: "2026-11",
      daysInMonth: 30,
      days: [
        day("2026-11-01", 1, 3_000_000, 2),
        day("2026-11-02", 0.25, 1_500_000, 3),
        day("2026-11-03", 0, 0, 0),
        day("2026-11-04", 0.5, 600_000, 1),
        day("2026-11-05", 0.1, 300_000, 1),
      ],
      total: { costUsd: 1.85, tokens: 5_400_000 },
    };
    render(fifth);

    for (const view of ["Cost", "Tokens"] as const) {
      chooseView(view);
      const columns = host.querySelector<HTMLElement>(".spend__columns")!;
      expect(columns.style.gridTemplateColumns).toBe("repeat(30, minmax(0, 1fr))");
      const slots = [...columns.children];
      expect(slots).toHaveLength(30);
      expect(slots.slice(0, 5).every((slot) => slot.classList.contains("spend__slot--day")), view).toBe(true);
      expect(slots.slice(5).some((slot) => slot.classList.contains("spend__slot--day") || slot.childElementCount > 0), view).toBe(false);
    }

    // In the token view, the second day is one block, half the axis's top of 3M.
    const second = host.querySelectorAll<HTMLElement>(".spend__slot--day")[1];
    const blocks = second.querySelectorAll<HTMLElement>(".spend__column");
    expect(blocks).toHaveLength(1);
    expect(second.childElementCount).toBe(1);
    expect(blocks[0].style.height).toBe("50%");
  });

  it("anchors the tooltip right of a column in the month's first half and left of one in its second, at the plot's top", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-10-28T12:00:00"));
    render(octoberTo(28));

    const first = hover(0);
    expect(first.parentElement?.classList.contains("spend__plot")).toBe(true);
    expect(first.style.left).toBe(`${(1 / 31) * 100}%`);
    expect(first.style.right).toBe("");
    unhover(0);

    const last = hover(27);
    expect(last.parentElement?.classList.contains("spend__plot")).toBe(true);
    expect(last.style.right).toBe(`${(4 / 31) * 100}%`);
    expect(last.style.left).toBe("");

    const tip = rule(appCss, ".spend__tip");
    expect(tip.position).toBe("absolute");
    expect(tip.top).toBe("0");
  });

  it("keeps both tooltips inside the panel at the band's width in a window of the minimum width", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-10-28T12:00:00"));
    render(octoberTo(28));

    // The unit tests' DOM does no layout: the plot's width comes from the stylesheet, in a window of the minimum width
    // with the sidebar open, the screen's scroll bar showing, and the axis at its own minimum width.
    const conf = JSON.parse(readFileSync(join(__dirname, "..", "..", "..", "src-tauri", "tauri.conf.json"), "utf8"));
    const windowWidth: number = conf.app.windows.find((w: { label: string }) => w.label === "main").minWidth;
    const screen = sides(rule(appCss, ".screen").padding);
    const body = sides(rule(appCss, ".panel__body").padding);
    const border = px(rule(appCss, ".panel").border.split(" ")[0]);
    const axis = px(rule(appCss, ".spend__axis")["min-width"]);
    const frameGap = px(rule(appCss, ".spend__frame").gap);
    const plot =
      windowWidth -
      px("var(--hf-sidebar-width)") -
      px(rule(appCss, "::-webkit-scrollbar").width) -
      screen.left -
      screen.right -
      2 * border -
      body.left -
      body.right -
      axis -
      frameGap;
    // The panel's inside, in the plot's coordinates.
    const panelLeft = -(axis + frameGap + body.left);
    const panelRight = plot + body.right;
    const tip = rule(appCss, ".spend__tip");
    const tipWidth = px(tip["min-width"]);
    const margin = sides(tip.margin);
    const percent = (value: string) => (parseFloat(value) / 100) * plot;

    const first = hover(0);
    const firstLeft = percent(first.style.left) + margin.left;
    expect(firstLeft).toBeGreaterThanOrEqual(panelLeft);
    expect(firstLeft + tipWidth).toBeLessThanOrEqual(panelRight);
    unhover(0);

    const last = hover(27);
    const lastRight = plot - percent(last.style.right) - margin.right;
    expect(lastRight).toBeLessThanOrEqual(panelRight);
    expect(lastRight - tipWidth).toBeGreaterThanOrEqual(panelLeft);
  });
});

/**
 * October split between the two harnesses: on the first, $1.50 and 200K tokens of Claude Code and $0.50 and 600K of
 * Codex, which the daemon lists Codex first; on the second, Codex alone, its model unpriced, so it has tokens and no
 * cost; on the third, Claude Code alone.
 */
const split: Spend = {
  month: "2026-10",
  daysInMonth: 31,
  days: [
    day("2026-10-01", 2, 800_000, 4, none, [
      { harness: "codex", costUsd: 0.5, tokens: 600_000 },
      { harness: "claude-code", costUsd: 1.5, tokens: 200_000 },
    ]),
    day("2026-10-02", 0, 500_000, 1, { ...none, unpriced: 1 }, [{ harness: "codex", costUsd: 0, tokens: 500_000 }]),
    day("2026-10-03", 1, 400_000, 1),
  ],
  total: { costUsd: 3, tokens: 1_700_000 },
};

function harnessToggle(): HTMLButtonElement {
  return [...host.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")].find((b) => b.textContent === "Harness")!;
}

/** Each day's segments, from the column's foot: the harness and the share of the column it takes. */
function segments(): { harness: string; height: string; background: string }[][] {
  return [...host.querySelectorAll(".spend__slot--day")].map((slot) =>
    [...slot.querySelectorAll<HTMLElement>(".spend__segment")].map((s) => ({
      harness: s.dataset.harness!,
      height: s.style.height,
      background: s.style.background,
    })),
  );
}

function legend(): string[] {
  return [...host.querySelectorAll(".spend__legend li")].map((li) => li.textContent!);
}

describe("the split's order", () => {
  it("lists the month's harnesses Claude Code first, then Codex, then any other by its identifier", () => {
    const other = { ...split, days: [...split.days, day("2026-10-04", 1, 1, 1, none, [{ harness: "aider", costUsd: 1, tokens: 1 }])] };
    expect(monthHarnesses(other)).toEqual(["claude-code", "codex", "aider"]);
    expect(monthHarnesses({ ...split, days: [split.days[1]] })).toEqual(["codex"]);
  });

  it("leaves out of a day's segments a harness with nothing in the view's unit", () => {
    expect(daySegments(split.days[1], "cost")).toEqual([]);
    expect(daySegments(split.days[1], "tokens")).toEqual([{ harness: "codex", value: 500_000 }]);
  });
});

describe("SpendBand's Harness toggle", () => {
  it("is off by default, a pressed button beside the view, and the columns stay whole", () => {
    render(split);
    expect(harnessToggle().getAttribute("aria-pressed")).toBe("false");
    expect(host.querySelector(".panel__actions")?.contains(harnessToggle())).toBe(true);
    expect(host.querySelectorAll(".spend__segment")).toHaveLength(0);
    expect(host.querySelector(".spend__legend")).toBeNull();
  });

  it("stacks each column by harness in the cost view, Claude Code at the foot, each in its harness's color", () => {
    render(split);
    act(() => harnessToggle().click());
    expect(harnessToggle().getAttribute("aria-pressed")).toBe("true");
    const claude = "var(--hf-color-claude)";
    const codex = "var(--hf-color-codex)";
    expect(segments()).toEqual([
      [
        { harness: "claude-code", height: "75%", background: claude },
        { harness: "codex", height: "25%", background: codex },
      ],
      [],
      [{ harness: "claude-code", height: "100%", background: claude }],
    ]);
    // The column keeps the day's height: $2 of the axis's top of $10.
    expect(host.querySelectorAll<HTMLElement>(".spend__column")[0].style.height).toBe("20%");
    expect(host.querySelectorAll(".spend__slot")).toHaveLength(31);
  });

  it("stacks each column by harness in the token view, in the same order", () => {
    render(split);
    act(() => harnessToggle().click());
    chooseView("Tokens");
    expect(segments().map((d) => d.map((s) => `${s.harness} ${s.height}`))).toEqual([
      ["claude-code 25%", "codex 75%"],
      ["codex 100%"],
      ["claude-code 100%"],
    ]);
    expect(host.querySelectorAll<HTMLElement>(".spend__column")[0].style.height).toBe("40%");
  });

  it("is independent of the view: choosing a unit keeps the split, and toggling keeps the unit", () => {
    render(split);
    chooseView("Tokens");
    act(() => harnessToggle().click());
    expect(host.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe("Tokens");
    chooseView("Cost");
    expect(harnessToggle().getAttribute("aria-pressed")).toBe("true");
    act(() => harnessToggle().click());
    expect(host.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe("Cost");
    expect(localStorage.getItem(SPEND_VIEW_KEY)).toBe("cost");
  });

  it("keeps the toggle across launches, and reads a stored value it does not know as off", () => {
    render(split);
    act(() => harnessToggle().click());
    expect(localStorage.getItem(SPEND_SPLIT_KEY)).toBe("on");
    act(() => root?.unmount());
    host.remove();
    render(split);
    expect(harnessToggle().getAttribute("aria-pressed")).toBe("true");
    act(() => harnessToggle().click());
    expect(localStorage.getItem(SPEND_SPLIT_KEY)).toBe("off");

    act(() => root?.unmount());
    host.remove();
    localStorage.setItem(SPEND_SPLIT_KEY, "yes");
    render(split);
    expect(harnessToggle().getAttribute("aria-pressed")).toBe("false");
  });

  it("names in its legend only the harnesses that ran an attempt this month", () => {
    localStorage.setItem(SPEND_SPLIT_KEY, "on");
    render(split);
    expect(legend()).toEqual(["Claude Code", "Codex"]);
    act(() => root?.unmount());
    host.remove();
    render({ ...split, days: [split.days[1]] });
    expect(legend()).toEqual(["Codex"]);
  });

  it("leaves the tooltip, the column's name, and the marks to the day, naming no harness", () => {
    render(split);
    const off = { tip: hover(1).textContent, marks: host.querySelectorAll(".spend__mark--day").length, note: host.querySelector(".spend__note")?.textContent };
    const label = host.querySelectorAll(".spend__slot--day")[0].getAttribute("aria-label");
    unhover(1);
    act(() => harnessToggle().click());
    const tip = hover(1);
    expect(tip.textContent).toBe(off.tip);
    expect(tip.textContent).not.toMatch(/Claude Code|Codex/);
    expect(host.querySelectorAll(".spend__mark--day")).toHaveLength(off.marks);
    expect(host.querySelector(".spend__note")?.textContent).toBe(off.note);
    expect(host.querySelectorAll(".spend__slot--day")[0].getAttribute("aria-label")).toBe(label);
    expect(host.querySelector(".spend__total .spend__mark")).toBeNull();
  });

  it("shows the same empty state for a month with no execution", () => {
    localStorage.setItem(SPEND_SPLIT_KEY, "on");
    render(emptyMonth);
    expect(host.querySelector(".spend__empty")?.textContent).toBe("No execution this month");
    expect(host.querySelectorAll(".spend__grid")).toHaveLength(3);
    expect(host.querySelector(".spend__legend")).toBeNull();
    expect(host.querySelectorAll(".spend__segment")).toHaveLength(0);
  });
});

const designDir = join(__dirname, "..", "design");
const appCss = readFileSync(join(designDir, "app.css"), "utf8");
const themeCss = readFileSync(join(designDir, "theme.css"), "utf8");

/** The declarations of a selector's first rule in a stylesheet. */
function rule(css: string, selector: string): Record<string, string> {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = new RegExp(`(?:^|\\n)${escaped} \\{([^}]*)\\}`).exec(css);
  if (!match) throw new Error(`no rule for ${selector}`);
  return Object.fromEntries(
    match[1]
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .split(";")
      .map((d) => d.trim())
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(":")).trim(), d.slice(d.indexOf(":") + 1).trim()]),
  );
}

/** A length in pixels: `0`, `<n>px`, or a token of theme.css that holds one. */
function px(value: string): number {
  const token = /^var\((--[\w-]+)\)$/.exec(value);
  if (token) return px(rule(themeCss, ":root")[token[1]]);
  if (value === "0") return 0;
  const length = /^(\d+(?:\.\d+)?)px$/.exec(value);
  if (!length) throw new Error(`not a length in pixels: ${value}`);
  return Number(length[1]);
}

/** The left and right lengths of a padding or margin shorthand. */
function sides(value: string): { left: number; right: number } {
  const parts = value.split(/\s+/).map(px);
  const right = parts.length === 1 ? parts[0] : parts[1];
  return { left: parts.length === 4 ? parts[3] : right, right };
}
