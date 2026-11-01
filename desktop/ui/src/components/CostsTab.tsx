// The execution's token use and estimated cost (harmflow-app#39): a summary — tiles, the token mix, and the attempts
// with each figure's source — and an itemized receipt, tokens × price = amount, copyable as text.
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Attempt } from "../api/types";
import {
  SUMMARY_CLASSES,
  executionCost,
  formatCompact,
  formatPercent,
  formatRate,
  formatUsd,
  totalOf,
  type AttemptCost,
  type ExecutionCost,
  type ReceiptLine,
} from "../lib/costs";
import { formatDuration, formatNumber } from "../lib/format";
import { useNow } from "../lib/hooks";
import { HarnessLabel } from "./Shell";
import { Icon } from "./Icon";
import { Button, Chip, CopyButton, EmptyState, cx } from "./ui";

/** How long each harness's provider keeps a cache write, which prices it: the default lifetime, and the extended one. */
const CACHE_LIFETIMES: Record<string, { cacheWriteShort: string; cacheWriteLong: string }> = {
  "claude-code": { cacheWriteShort: "5 min", cacheWriteLong: "1 h" },
  codex: { cacheWriteShort: "30 min", cacheWriteLong: "12 h" },
};

export function CostsTab({ attempts, active }: { attempts: Attempt[]; active: boolean }) {
  const { t } = useTranslation();
  // A running attempt's cost grows with its time; a finished execution's does not.
  const now = useNow(active ? 1000 : null);
  const [view, setView] = useState<"summary" | "receipt">("summary");
  const cost = useMemo(() => executionCost(attempts, now), [attempts, now]);
  if (!attempts.length) return <EmptyState icon="coin" title={t("executions.detail.noAttempts")} />;
  if (cost.attempts.every((a) => a.source === "none")) {
    return <EmptyState icon="coin" title={t("executions.detail.costs.empty")} hint={t("executions.detail.costs.emptyHint")} />;
  }
  return (
    <div className="costs">
      {view === "receipt" ? (
        <Receipt cost={cost} active={active} onBack={() => setView("summary")} />
      ) : (
        <Summary cost={cost} active={active} onReceipt={() => setView("receipt")} />
      )}
    </div>
  );
}

function useHarnessName() {
  const { t } = useTranslation();
  return (harness: string) => t(`harness.names.${harness}` as never, { defaultValue: harness }) as string;
}

function Summary({ cost, active, onReceipt }: { cost: ExecutionCost; active: boolean; onReceipt: () => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const harnessName = useHarnessName();
  // By cost once every attempt is priced, unless the user chose a measure.
  const [chosen, setBy] = useState<"cost" | "tokens">();
  const by = chosen ?? (cost.costs ? "cost" : "tokens");
  const tokens = cost.tokens;
  const inputSide = tokens ? tokens.input + tokens.cacheWrite + tokens.cacheRead : 0;
  const outputKnown = cost.attempts.every((a) => !a.tokens || a.outputKnown);
  const measure = by === "cost" && cost.costs ? cost.costs : tokens;
  const measureTotal = (measure && totalOf(measure)) || 1;
  const minutes = cost.runMs / 60_000;
  const several = cost.attempts.length > 1;
  // A partial count has no output: while the execution runs it comes later; once it ended, the sum is a lower bound.
  const outputText = (n: number) => (outputKnown ? formatNumber(n, locale) : active ? "—" : `${formatNumber(n, locale)} + ?`);

  return (
    <>
      <div className="costs__tiles">
        <div className="costs-tile costs-tile--hero">
          <span className="costs-tile__label">
            {active ? <span className="costs-live" aria-hidden="true" /> : null}
            {t(active ? "executions.detail.costs.costSoFar" : "executions.detail.costs.cost")}
          </span>
          <span className="costs-tile__value">
            {formatUsd(cost.cost, locale)}
            {cost.incomplete && cost.cost !== undefined ? <span className="costs-tile__plus">+ ?</span> : null}
          </span>
          <span className="costs-tile__sub">{cost.attempts.map((a) => harnessName(a.attempt.harness)).join(" → ")}</span>
        </div>
        <div className="costs-tile">
          <span className="costs-tile__label">{t("executions.detail.costs.tokens")}</span>
          <span className="costs-tile__value">{tokens ? formatCompact(totalOf(tokens), locale) : "—"}</span>
          <span className="costs-tile__sub">
            {tokens
              ? t(outputKnown ? "executions.detail.costs.tokensSplit" : active ? "executions.detail.costs.tokensSplitRunning" : "executions.detail.costs.tokensSplitAtLeast", {
                  input: formatCompact(inputSide, locale),
                  output: formatCompact(tokens.output, locale),
                })
              : t("executions.detail.costs.noTokensYet")}
          </span>
        </div>
        <div className="costs-tile">
          <span className="costs-tile__label">{t("executions.detail.costs.cacheShare")}</span>
          <span className="costs-tile__value">{tokens && inputSide ? formatPercent(tokens.cacheRead / inputSide, locale) : "—"}</span>
          <span className="costs-tile__sub">{t("executions.detail.costs.cacheShareHint")}</span>
        </div>
        <div className="costs-tile">
          <span className="costs-tile__label">{t("executions.detail.costs.perMinute")}</span>
          <span className="costs-tile__value">{cost.cost !== undefined && minutes >= 1 / 60 ? formatUsd(cost.cost / minutes, locale) : "—"}</span>
          <span className="costs-tile__sub">{t("executions.detail.costs.perMinuteHint", { duration: formatDuration(cost.runMs, locale) })}</span>
        </div>
      </div>

      {tokens && measure ? (
        <section className="costs-block">
          <header className="costs-block__header">
            <h4>{t("executions.detail.costs.mixTitle")}</h4>
            <div className="costs-seg" role="group" aria-label={t("executions.detail.costs.measure")}>
              <button type="button" className={cx(by === "cost" && "is-on")} aria-pressed={by === "cost"} onClick={() => setBy("cost")} disabled={!cost.costs}>
                {t("executions.detail.costs.byCost")}
              </button>
              <button type="button" className={cx(by === "tokens" && "is-on")} aria-pressed={by === "tokens"} onClick={() => setBy("tokens")}>
                {t("executions.detail.costs.byTokens")}
              </button>
            </div>
          </header>
          <div className="costs-mix" aria-hidden="true">
            {SUMMARY_CLASSES.map((c) =>
              measure[c] > 0 ? <span key={c} className={`costs-mix__seg costs-c--${c}`} style={{ flexGrow: measure[c] / measureTotal }} /> : null,
            )}
          </div>
          <ul className="costs-legend">
            {SUMMARY_CLASSES.map((c) => (
              <li key={c}>
                <span className="costs-legend__top">
                  <span className={`costs-dot costs-c--${c}`} />
                  <span className="costs-legend__name">{t(`executions.detail.costs.classes.${c}`)}</span>
                  <span className="mono costs-legend__share">{formatPercent(measure[c] / measureTotal, locale)}</span>
                </span>
                <span className="mono muted">
                  {c === "output" && !outputKnown && active
                    ? t("executions.detail.costs.outputPending")
                    : t(c === "output" && !outputKnown ? "executions.detail.costs.legendLineAtLeast" : "executions.detail.costs.legendLine", {
                        tokens: formatNumber(tokens[c], locale),
                        cost: cost.costs ? formatUsd(cost.costs[c], locale) : "—",
                      })}
                </span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      <section className="costs-block">
        <header className="costs-block__header">
          <h4>{t("executions.detail.costs.byAttempt")}</h4>
          <Button size="sm" icon="list" onClick={onReceipt}>
            {t("executions.detail.costs.receipt")}
          </Button>
        </header>
        <div className="table__scroll">
          <table className="table costs-table">
            <thead>
              <tr>
                <th>{t("executions.detail.costs.columns.attempt")}</th>
                <th>{t("executions.detail.costs.columns.harness")}</th>
                <th className="num">{t("executions.detail.costs.columns.input")}</th>
                <th className="num">{t("executions.detail.costs.columns.cacheRead")}</th>
                <th className="num">{t("executions.detail.costs.columns.output")}</th>
                <th className="num">{t("executions.detail.costs.columns.cost")}</th>
                <th>{t("executions.detail.costs.columns.source")}</th>
              </tr>
            </thead>
            <tbody>
              {cost.attempts.map((a) => (
                <tr key={a.attempt.number}>
                  <td className="mono">#{a.attempt.number}</td>
                  <td>
                    <HarnessLabel harness={a.attempt.harness} model={a.model} />
                  </td>
                  <td className="num mono">{a.tokens ? formatNumber(a.tokens.input + a.tokens.cacheWrite, locale) : "—"}</td>
                  <td className="num mono">{a.tokens ? formatNumber(a.tokens.cacheRead, locale) : "—"}</td>
                  <td className="num mono">{a.tokens && a.outputKnown ? formatNumber(a.tokens.output, locale) : "—"}</td>
                  <td className="num mono">
                    <strong>{formatUsd(a.cost, locale)}</strong>
                  </td>
                  <td>
                    <SourceChip a={a} />
                    {a.attempt.outcome === "quota_exhausted" ? (
                      <Chip tone="magenta" icon="switch">
                        {t("attemptOutcome.quota_exhausted")}
                      </Chip>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
            {several ? (
              <tfoot>
                <tr className="costs-table__total">
                  <td colSpan={2}>{t("executions.detail.costs.total")}</td>
                  <td className="num mono">{tokens ? formatNumber(tokens.input + tokens.cacheWrite, locale) : "—"}</td>
                  <td className="num mono">{tokens ? formatNumber(tokens.cacheRead, locale) : "—"}</td>
                  <td className="num mono">{tokens ? outputText(tokens.output) : "—"}</td>
                  <td className="num mono">
                    <strong>{formatUsd(cost.cost, locale)}</strong>
                    {cost.incomplete && cost.cost !== undefined ? <span className="costs-flag"> + ?</span> : null}
                  </td>
                  <td />
                </tr>
              </tfoot>
            ) : null}
          </table>
        </div>
      </section>
      <BillingNote cost={cost} active={active} />
    </>
  );
}

function SourceChip({ a }: { a: AttemptCost }) {
  const { t } = useTranslation();
  const harnessName = useHarnessName();
  switch (a.source) {
    case "reported":
      return (
        <Chip tone="success" icon="check">
          {t("executions.detail.costs.source.reported", { harness: harnessName(a.attempt.harness) })}
        </Chip>
      );
    case "estimated":
      return (
        <Chip tone="violet" icon="sparkle">
          {t("executions.detail.costs.source.estimated")}
        </Chip>
      );
    case "unpriced":
      return (
        <Chip tone="neutral" icon="alert">
          {a.modelSource ? t("executions.detail.costs.source.unpriced", { model: a.model ?? "" }) : t("executions.detail.costs.source.unpricedNoModel")}
        </Chip>
      );
    case "running":
      return (
        <Chip tone="cyan" icon="dot">
          {t("executions.detail.costs.source.running")}
        </Chip>
      );
    case "partial":
      return (
        <Chip tone="warning" icon="alert">
          {t("executions.detail.costs.source.partial")}
        </Chip>
      );
    default:
      return <Chip tone="neutral">{t("executions.detail.costs.source.none")}</Chip>;
  }
}

/** Says the figure is an estimate at API prices, which holds on a subscription too, and why any attempt lacks a cost. */
function BillingNote({ cost, active }: { cost: ExecutionCost; active: boolean }) {
  const { t, i18n } = useTranslation();
  const harnessName = useHarnessName();
  const notes = cost.attempts.flatMap((a) => {
    const params = { number: a.attempt.number, harness: harnessName(a.attempt.harness), model: a.model ?? "" };
    if (a.source === "partial") return [t("executions.detail.costs.noteAttemptPartial", params)];
    if (a.source === "unpriced") return [t(a.modelSource ? "executions.detail.costs.noteAttemptUnpriced" : "executions.detail.costs.noteAttemptNoModel", params)];
    if (a.modelSource === "config" && a.tokens) return [t("executions.detail.costs.noteAttemptConfig", params)];
    return [];
  });
  return (
    <p className="costs-note">
      <Icon name="shield" size={13} />
      <span>
        {cost.pricesAsOf ? t("executions.detail.costs.noteAsOf", { date: formatDay(cost.pricesAsOf, i18n.language) }) : t("executions.detail.costs.note")}
        {active ? ` ${t("executions.detail.costs.noteRunning")}` : null}
        {notes.map((n) => ` ${n}`).join("")}
      </span>
    </p>
  );
}

function formatDay(date: string, locale: string): string {
  const d = new Date(`${date}T00:00:00`);
  return Number.isNaN(d.getTime()) ? date : new Intl.DateTimeFormat(locale, { dateStyle: "medium" }).format(d);
}

function Receipt({ cost, active, onBack }: { cost: ExecutionCost; active: boolean; onBack: () => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const harnessName = useHarnessName();
  const className = (l: ReceiptLine, harness: string) => {
    if (l.tokenClass !== "cacheWriteShort" && l.tokenClass !== "cacheWriteLong") return t(`executions.detail.costs.classes.${l.tokenClass}`);
    const lifetime = CACHE_LIFETIMES[harness]?.[l.tokenClass];
    return lifetime ? t(`executions.detail.costs.classes.${l.tokenClass}`, { lifetime }) : t("executions.detail.costs.classes.cacheWrite");
  };
  const rate = (l: ReceiptLine) => (l.price === undefined ? "—" : t("executions.detail.costs.perMillion", { price: formatRate(l.price, locale) }));
  const totalLabel = t(active ? "executions.detail.costs.runningTotal" : "executions.detail.costs.totalEstimate");
  // An attempt that ended with tokens and no cost leaves the total short; a running one is priced when it ends.
  const short = cost.attempts.some((a) => a.cost === undefined && a.tokens && a.source !== "running");
  const unpriced = short ? ` ${t("executions.detail.costs.plusUnpriced")}` : "";

  // The same receipt as plain text, for the copy button.
  const text: string[] = [t("executions.detail.costs.receiptTitle").toUpperCase()];
  if (cost.pricesAsOf) text.push(t("executions.detail.costs.pricesAsOf", { date: formatDay(cost.pricesAsOf, locale) }));
  for (const a of cost.attempts) {
    text.push("", `${t("executions.detail.costs.receiptAttempt", { number: a.attempt.number })} · ${harnessName(a.attempt.harness)}${a.model ? ` · ${a.model}` : ""}`);
    for (const l of a.lines) {
      const qty = formatNumber(l.tokens, locale);
      text.push(`  ${className(l, a.attempt.harness).padEnd(20)} ${qty.padStart(12)} × ${rate(l).padStart(10)} = ${formatUsd(l.amount, locale).padStart(10)}`);
    }
    text.push(`  ${t("executions.detail.costs.subtotal").padEnd(47)} ${formatUsd(a.cost, locale).padStart(10)}`);
  }
  text.push("", `${totalLabel}: ${formatUsd(cost.cost, locale)}${unpriced}`);

  return (
    <div className="costs-receipt-view">
      <div className="costs-receipt-view__back">
        <Button size="sm" variant="ghost" onClick={onBack}>
          {t("executions.detail.costs.backToSummary")}
        </Button>
      </div>
      <div className="costs-receipt">
        <header className="costs-receipt__head">
          <span className="costs-receipt__brand">{t("executions.detail.costs.receiptTitle")}</span>
          {cost.pricesAsOf ? <span className="mono muted">{t("executions.detail.costs.pricesAsOf", { date: formatDay(cost.pricesAsOf, locale) })}</span> : null}
          <CopyButton text={text.join("\n")} />
        </header>
        {cost.attempts.map((a) => {
          const models = [...new Set(a.lines.map((l) => l.model))];
          return (
            <section key={a.attempt.number} className="costs-receipt__section">
              <div className="costs-receipt__attempt">
                <span className="mono">{t("executions.detail.costs.receiptAttempt", { number: a.attempt.number })}</span>
                <HarnessLabel harness={a.attempt.harness} model={a.model} />
                <SourceChip a={a} />
              </div>
              <table className="costs-receipt__lines">
                <tbody>
                  {a.lines.map((l, i) => (
                    <ReceiptRow key={`${l.model}-${l.tokenClass}`} line={l} label={className(l, a.attempt.harness)} rate={rate(l)} model={models.length > 1 && a.lines[i - 1]?.model !== l.model ? l.model : undefined} />
                  ))}
                  <tr className="costs-receipt__subtotal">
                    <td colSpan={5}>
                      {t("executions.detail.costs.subtotal")}
                      {a.source === "reported" && a.cost !== undefined && a.costs ? (
                        <span className="muted">
                          {" · "}
                          {a.splitDiffers
                            ? t("executions.detail.costs.differsReported", { estimate: formatUsd(totalOf(a.costs), locale), cost: formatUsd(a.cost, locale), harness: harnessName(a.attempt.harness) })
                            : t("executions.detail.costs.matchesReported", { cost: formatUsd(a.cost, locale), harness: harnessName(a.attempt.harness) })}
                        </span>
                      ) : null}
                      {a.attempt.outcome === "quota_exhausted" ? <span className="costs-flag"> · {t("attemptOutcome.quota_exhausted")}</span> : null}
                    </td>
                    <td className="num">{formatUsd(a.cost, locale)}</td>
                  </tr>
                </tbody>
              </table>
            </section>
          );
        })}
        <div className="costs-receipt__total">
          <span>{totalLabel}</span>
          <span className="mono">
            {formatUsd(cost.cost, locale)}
            {unpriced ? <span className="costs-flag">{unpriced}</span> : null}
          </span>
        </div>
        <BillingNote cost={cost} active={active} />
      </div>
    </div>
  );
}

function ReceiptRow({ line, label, rate, model }: { line: ReceiptLine; label: string; rate: string; model?: string }) {
  const { i18n } = useTranslation();
  return (
    <>
      {model ? (
        <tr className="costs-receipt__model">
          <td colSpan={6} className="mono muted">
            {model}
          </td>
        </tr>
      ) : null}
      <tr>
        <td>{label}</td>
        <td className="num">{formatNumber(line.tokens, i18n.language)}</td>
        <td className="costs-receipt__op">×</td>
        <td className="num muted">{rate}</td>
        <td className="costs-receipt__op">=</td>
        <td className="num">{formatUsd(line.amount, i18n.language)}</td>
      </tr>
    </>
  );
}

