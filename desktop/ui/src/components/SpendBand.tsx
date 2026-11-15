// The Overview's Spend this month band (harmflow-app#583): one column per day that has passed, in the estimated cost or
// the tokens, the month's total in the corner, and a mark on each day whose figure undercounts in the view shown. Its
// Harness toggle (harmflow-app#970) stacks each column by harness, with a legend; the tooltip and the marks stay the
// day's, naming no harness.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { Spend, SpendDay } from "../api/types";
import { color } from "../design/theme";
import { formatCompact, formatUsd } from "../lib/costs";
import { formatNumber } from "../lib/format";
import {
  SPEND_VIEWS,
  axisTop,
  dayMarks,
  daySegments,
  dayValue,
  isEmptyMonth,
  keepSpendSplit,
  keepSpendView,
  keptSpendSplit,
  keptSpendView,
  monthHarnesses,
  totalValue,
  type SpendView,
} from "../lib/spend";
import { useStore } from "../state/store";
import { Panel, SegmentedControl, cx } from "./ui";

/** The fractions of the axis's top that carry a grid line and a label. */
const GRID = [1, 0.5, 0];

/** Each harness's color in the split, the one the interface already gives it; a harness it does not know is muted. */
const HARNESS_COLORS: Record<string, string> = { "claude-code": color.claude, codex: color.codex };

function harnessColor(harness: string): string {
  return HARNESS_COLORS[harness] ?? color.textMuted;
}

function formatValue(value: number, view: SpendView, locale: string): string {
  return view === "cost" ? formatUsd(value, locale) : formatCompact(value, locale);
}

function formatAxis(value: number, view: SpendView, locale: string): string {
  if (view === "tokens") return formatCompact(value, locale);
  return new Intl.NumberFormat(locale, { style: "currency", currency: "USD", notation: "compact", maximumFractionDigits: 2 }).format(value);
}

function formatDate(date: string, locale: string): string {
  const d = new Date(`${date}T00:00:00`);
  return Number.isNaN(d.getTime()) ? date : new Intl.DateTimeFormat(locale, { weekday: "short", month: "short", day: "numeric" }).format(d);
}

/**
 * The month and year of the spend's `month` (`YYYY-MM`), read as a UTC date so the machine's time zone never moves it
 * to another month; undefined for a value that is no month.
 */
function spendMonth(month: string, locale: string): { month: string; year: string } | undefined {
  const match = /^(\d{4})-(\d{2})$/.exec(month);
  if (!match) return undefined;
  const first = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, 1));
  return { month: new Intl.DateTimeFormat(locale, { month: "short", timeZone: "UTC" }).format(first), year: match[1] };
}

export function SpendBand() {
  const { t, i18n } = useTranslation();
  const spend = useStore((s) => s.spend);
  const [view, setView] = useState<SpendView>(keptSpendView);
  const [split, setSplit] = useState<boolean>(keptSpendSplit);
  const choose = (v: SpendView) => {
    setView(v);
    keepSpendView(v);
  };
  const toggleSplit = () => {
    setSplit(!split);
    keepSpendSplit(!split);
  };
  const month = spend ? spendMonth(spend.month, i18n.language) : undefined;
  return (
    <Panel
      title={month ? t("overview.spend.titleMonth", month) : t("overview.spend.title")}
      icon="coin"
      className="spend"
      actions={
        <>
          <SegmentedControl
            aria-label={t("overview.spend.view")}
            value={view}
            options={SPEND_VIEWS.map((v) => ({ value: v, label: t(`overview.spend.${v}`) }))}
            onChange={choose}
          />
          <div className="segmented">
            <button type="button" aria-pressed={split} className={cx("segmented__item", split && "segmented__item--active")} onClick={toggleSplit}>
              {t("overview.spend.harness")}
            </button>
          </div>
        </>
      }
    >
      {spend ? <SpendChart spend={spend} view={view} split={split} /> : <div className="spend__plot" aria-busy="true" />}
    </Panel>
  );
}

function SpendChart({ spend, view, split }: { spend: Spend; view: SpendView; split: boolean }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const [hovered, setHovered] = useState<number | null>(null);
  const top = axisTop(spend, view);
  const empty = isEmptyMonth(spend);
  const slots = Array.from({ length: spend.daysInMonth }, (_, i) => spend.days[i]);
  const marked = spend.days.some((d) => dayMarks(d, view).length > 0);
  const day = hovered === null ? undefined : spend.days[hovered];
  const harnesses = split ? monthHarnesses(spend) : [];

  return (
    <div className="spend__chart">
      <div className="spend__total">
        <strong className="mono">{formatValue(totalValue(spend, view), view, locale)}</strong>
        <span className="muted">{t(view === "cost" ? "overview.spend.totalCost" : "overview.spend.totalTokens")}</span>
      </div>
      <div className="spend__frame">
        <div className="spend__axis" aria-hidden="true">
          {GRID.map((f) => (
            <span key={f}>{formatAxis(top * f, view, locale)}</span>
          ))}
        </div>
        <div className="spend__plot">
          {GRID.map((f) => (
            <span key={f} className="spend__grid" style={{ bottom: `${f * 100}%` }} aria-hidden="true" />
          ))}
          <div className="spend__columns" style={{ gridTemplateColumns: `repeat(${spend.daysInMonth}, minmax(0, 1fr))` }}>
            {slots.map((d, i) =>
              d ? (
                <SpendColumn key={d.date} day={d} view={view} split={split} top={top} onHover={(on) => setHovered(on ? i : null)} />
              ) : (
                <span key={i} className="spend__slot" />
              ),
            )}
          </div>
          {empty ? <p className="spend__empty">{t("overview.spend.noExecution")}</p> : null}
          {day && hovered !== null ? (
            <SpendTip day={day} view={view} index={hovered} slots={spend.daysInMonth} />
          ) : null}
        </div>
      </div>
      <div className="spend__days mono muted" aria-hidden="true" style={{ gridTemplateColumns: `repeat(${spend.daysInMonth}, minmax(0, 1fr))` }}>
        {slots.map((_, i) => (
          <span key={i}>{i === 0 || (i + 1) % 5 === 0 ? i + 1 : ""}</span>
        ))}
      </div>
      {harnesses.length ? (
        <ul className="spend__legend">
          {harnesses.map((h) => (
            <li key={h}>
              <span className="spend__swatch" style={{ background: harnessColor(h) }} aria-hidden="true" />
              {t(`harness.names.${h}` as never, { defaultValue: h }) as string}
            </li>
          ))}
        </ul>
      ) : null}
      {marked ? (
        <p className="spend__note">
          <span className="spend__mark" aria-hidden="true">
            ▲
          </span>{" "}
          {t(view === "cost" ? "overview.spend.markNoteCost" : "overview.spend.markNoteTokens")}
        </p>
      ) : null}
    </div>
  );
}

function SpendColumn({ day, view, split, top, onHover }: { day: SpendDay; view: SpendView; split: boolean; top: number; onHover: (on: boolean) => void }) {
  const { t, i18n } = useTranslation();
  const value = dayValue(day, view);
  const marks = dayMarks(day, view);
  const label = `${formatDate(day.date, i18n.language)}: ${formatValue(value, view, i18n.language)}${marks.length ? ` — ${t("overview.spend.undercounts")}` : ""}`;
  return (
    <span
      className="spend__slot spend__slot--day"
      tabIndex={0}
      role="img"
      aria-label={label}
      onMouseEnter={() => onHover(true)}
      onMouseLeave={() => onHover(false)}
      onFocus={() => onHover(true)}
      onBlur={() => onHover(false)}
    >
      {marks.length ? (
        <span className="spend__mark spend__mark--day" aria-hidden="true">
          ▲
        </span>
      ) : null}
      {split ? (
        // The day's column, its harnesses stacked inside it from the foot, each its share of the day's figure.
        <span className="spend__column spend__column--split" style={{ height: `${top > 0 ? (value / top) * 100 : 0}%` }}>
          {daySegments(day, view).map((s) => (
            <span
              key={s.harness}
              className="spend__segment"
              data-harness={s.harness}
              style={{ height: `${(s.value / value) * 100}%`, background: harnessColor(s.harness) }}
              aria-hidden="true"
            />
          ))}
        </span>
      ) : (
        <span className="spend__column" style={{ height: `${top > 0 ? (value / top) * 100 : 0}%` }} />
      )}
    </span>
  );
}

/** Beside the hovered column, at the top of the plot: right of it in the month's first half, left of it in the second. */
function SpendTip({ day, view, index, slots }: { day: SpendDay; view: SpendView; index: number; slots: number }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const marks = dayMarks(day, view);
  const side = index < slots / 2 ? { left: `${((index + 1) / slots) * 100}%` } : { right: `${((slots - index) / slots) * 100}%` };
  return (
    <div className="spend__tip" role="tooltip" style={side}>
      <strong>{formatDate(day.date, locale)}</strong>
      <span className="mono">{formatValue(dayValue(day, view), view, locale)}</span>
      <span className="muted">{t("overview.spend.attemptsEnded", { count: day.attempts, formatted: formatNumber(day.attempts, locale) })}</span>
      {marks.map((m) => (
        <span key={m.reason} className="spend__tip-mark">
          <span className="spend__mark" aria-hidden="true">
            ▲
          </span>{" "}
          {t(`overview.spend.reasons.${m.reason}`, { count: m.count })}
        </span>
      ))}
    </div>
  );
}
