// The first and priority screen: every active execution at once, the selected one in detail, the queue and the recent
// history beside them. With the aside hidden (⌘E), the main column takes the whole width.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type MouseEvent } from "react";
import { useTranslation } from "react-i18next";
import { isActive, type ExecutionSummary } from "../api/types";
import { Counters, QueuePanel, QuoteCard, RecentHistory } from "../components/Aside";
import { ExecutionCard, useCancel } from "../components/ExecutionCard";
import { ExecutionDetail } from "../components/ExecutionDetail";
import { Button, EmptyState, IconButton, SectionTitle, cx } from "../components/ui";
import { actions, useStore } from "../state/store";

export function ExecutionsScreen({
  selectedId,
  onSelect,
  asideHidden = false,
  onGoToTasks,
  onSeeHistory,
}: {
  selectedId: string | null;
  onSelect: (id: string | null) => void;
  asideHidden?: boolean;
  onGoToTasks: () => void;
  onSeeHistory: () => void;
}) {
  const { t } = useTranslation();
  const all = useStore((s) => s.active);
  const recent = useStore((s) => s.recent);
  const running = useMemo(() => all.filter((e) => isActive(e.status)), [all]);

  // Cards show the latest step from the events; load them for every running execution.
  const runningKey = running.map((e) => e.id).join(",");
  useEffect(() => {
    for (const id of runningKey.split(",").filter(Boolean)) void actions.loadEvents(id);
  }, [runningKey]);

  // Keep a selection: the chosen execution, else the first running one, else the latest finished one.
  const effective = selectedId ?? running[0]?.id ?? recent[0]?.id ?? null;
  const summary = [...all, ...recent].find((e) => e.id === effective);

  return (
    <div className={asideHidden ? "screen" : "screen screen--with-aside"}>
      <div className="screen__main">
        <Counters screen="executions" />
        {running.length === 0 ? (
          <>
            <SectionTitle>{t("executions.activeTitle", { count: 0 })}</SectionTitle>
            <div className="panel panel--empty">
              <EmptyState
                icon="playFilled"
                title={t("executions.empty")}
                hint={t("executions.emptyHint")}
                action={
                  <Button icon="list" onClick={onGoToTasks}>
                    {t("executions.goToTasks")}
                  </Button>
                }
              />
            </div>
          </>
        ) : (
          <RunningCards running={running} selectedId={effective} onSelect={onSelect} />
        )}
        {effective ? <ExecutionDetail executionId={effective} summary={summary} onOpenExecution={onSelect} /> : null}
      </div>
      {asideHidden ? null : (
        <div className="screen__aside">
          <QueuePanel onOpenExecution={(id) => onSelect(id)} />
          <RecentHistory onOpenExecution={(id) => onSelect(id)} onSeeAll={onSeeHistory} />
          <QuoteCard />
        </div>
      )}
    </div>
  );
}

/** The width of the slice of a hidden card shown at the row's edge, as app.css leaves room for it beside the cards. */
const SLICE_PX = 48;
/** Below this window width the row shows two cards instead of three, as app.css's media query does. */
const NARROW = "(max-width: 1240px)";
const REDUCED_MOTION = "(prefers-reduced-motion: reduce)";

const matches = (query: string) => typeof matchMedia === "function" && matchMedia(query).matches;

/** What the row shows: whether its cards overflow it, which of them are in view (from 0, last excluded), and its ends. */
interface RowView {
  overflows: boolean;
  first: number;
  last: number;
  atStart: boolean;
  atEnd: boolean;
}

const sameView = (a: RowView, b: RowView) =>
  a.overflows === b.overflows && a.first === b.first && a.last === b.last && a.atStart === b.atStart && a.atEnd === b.atEnd;

/** The row's geometry, read from the layout: the cards visible at once, the distance from one card to the next. */
function geometry(row: HTMLElement) {
  const style = getComputedStyle(row);
  const content = row.clientWidth - (parseFloat(style.paddingLeft) || 0) - (parseFloat(style.paddingRight) || 0);
  const visible = matches(NARROW) ? 2 : 3;
  const cards = row.children as HTMLCollectionOf<HTMLElement>;
  const measured = cards.length > 1 ? cards[1].offsetLeft - cards[0].offsetLeft : 0;
  // The cards are as wide as `visible` of them, their gaps, and the slice fill the row, so one card and its gap take
  // that share of what the slice leaves.
  const step = measured > 0 ? measured : Math.max(1, (content - SLICE_PX) / visible);
  return { visible, step, count: cards.length, max: row.scrollWidth - row.clientWidth };
}

function viewOf(row: HTMLElement): RowView {
  const { visible, step, count, max } = geometry(row);
  const overflows = max > 1;
  const atStart = !overflows || row.scrollLeft <= 1;
  const atEnd = !overflows || row.scrollLeft >= max - 1;
  // At the end the last card sits flush right, a slice of the one before the visible ones at the left.
  const start = atEnd ? count - visible : Math.round(row.scrollLeft / step);
  const first = Math.min(Math.max(start, 0), Math.max(count - visible, 0));
  return { overflows, first, last: Math.min(first + visible, count), atStart, atEnd };
}

const behavior = (): ScrollBehavior => (matches(REDUCED_MOTION) ? "auto" : "smooth");

/**
 * The running executions' cards, as a carousel once they overflow the row: Previous and Next beside the heading page
 * it by the cards visible, a dimmed slice of the next hidden card shows at the edge it hides behind, and the selected
 * card is scrolled into view. The row never moves on its own, and measures itself on scroll and resize only.
 */
function RunningCards({
  running,
  selectedId,
  onSelect,
}: {
  running: ExecutionSummary[];
  selectedId: string | null;
  onSelect: (id: string | null) => void;
}) {
  const { t } = useTranslation();
  const { cancel, pending } = useCancel();
  const row = useRef<HTMLDivElement>(null);
  const [view, setView] = useState<RowView>({ overflows: false, first: 0, last: running.length, atStart: true, atEnd: true });
  const key = running.map((e) => e.id).join(",");
  const ids = useMemo(() => (key ? key.split(",") : []), [key]);
  // The cards' order as the row lays them out, and the ones in view when it last measured with its scroll position
  // then: a card starting or ending keeps those in view.
  const order = useRef<string[]>([]);
  const shown = useRef<{ ids: string[]; order: string[]; left: number }>({ ids: [], order: [], left: 0 });

  const measure = useCallback(() => {
    const element = row.current;
    if (!element) return;
    const next = viewOf(element);
    shown.current = { ids: order.current.slice(next.first, next.last), order: order.current, left: element.scrollLeft };
    setView((prev) => (sameView(prev, next) ? prev : next));
  }, []);

  useEffect(() => {
    const element = row.current;
    if (!element) return;
    element.addEventListener("scroll", measure, { passive: true });
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(() => measure());
    observer?.observe(element);
    if (!observer) window.addEventListener("resize", measure);
    return () => {
      element.removeEventListener("scroll", measure);
      observer?.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [measure]);

  // A card starting or ending moves the cards after it: scroll by as many cards from where the row last measured, so
  // the ones in view stay where they were. Not from scrollLeft now: a row that narrowed at its end has its scrollLeft
  // clamped by the browser already, and scrolling back from there would move the cards a second time. Then measure,
  // since the row's width did not change and no resize says so.
  useLayoutEffect(() => {
    const element = row.current;
    if (!element) return;
    order.current = ids;
    const before = shown.current;
    const kept = before.ids.find((id) => ids.includes(id));
    if (kept !== undefined) {
      const moved = ids.indexOf(kept) - before.order.indexOf(kept);
      if (moved !== 0) element.scrollTo({ left: before.left + moved * geometry(element).step });
    }
    measure();
  }, [ids, measure]);

  // A selection made outside the row — the queue, the history, the default — shows its card whole. A click on a whole
  // card leaves the row where it is. With nothing selected the default is the first running execution, so one that
  // starts, listed first, becomes the default and its card shows: a card starting or ending moves the row only so.
  useEffect(() => {
    const element = row.current;
    const index = selectedId === null ? -1 : ids.indexOf(selectedId);
    const card = element?.children[index];
    if (!element || !card) return;
    const box = element.getBoundingClientRect();
    const rect = card.getBoundingClientRect();
    if (rect.left < box.left - 1 || rect.right > box.right + 1) card.scrollIntoView({ block: "nearest", inline: "nearest", behavior: behavior() });
  }, [selectedId]); // only a change of selection moves the row, the default's included

  const page = (direction: 1 | -1) => {
    const element = row.current;
    if (!element) return;
    const { visible, step } = geometry(element);
    const target = Math.max(0, viewOf(element).first + direction * visible);
    element.scrollTo({ left: target * step, behavior: behavior() });
  };

  // Each card snaps by its start, so a whole card sits flush left everywhere but at the row's end: a slice shows at the
  // right until the end, and at the left only there, where the last card sits flush right.
  const sliceStart = view.overflows && view.atEnd && !view.atStart;
  const sliceEnd = view.overflows && !view.atEnd;

  // A slice's overlay is the wrapper's own pseudo-element: a click on it targets the wrapper, never the card under it,
  // so it only pages toward its side.
  const onSlice = (e: MouseEvent<HTMLDivElement>) => {
    if (e.target !== e.currentTarget) return;
    const box = e.currentTarget.getBoundingClientRect();
    const forward = e.clientX > box.left + box.width / 2;
    if (forward ? sliceEnd : sliceStart) page(forward ? 1 : -1);
  };

  return (
    <>
      <SectionTitle
        actions={
          view.overflows ? (
            <div className="exec-carousel__controls">
              <span className="exec-carousel__position">
                {t("executions.carousel.position", { first: view.first + 1, last: view.last, total: running.length })}
              </span>
              <IconButton icon="chevronLeft" label={t("executions.carousel.previous")} disabled={view.atStart} onClick={() => page(-1)} />
              <IconButton icon="chevronRight" label={t("executions.carousel.next")} disabled={view.atEnd} onClick={() => page(1)} />
            </div>
          ) : null
        }
      >
        {t("executions.activeTitle", { count: running.length })}
      </SectionTitle>
      <div
        className={cx(
          "exec-carousel",
          sliceStart && "exec-carousel--more-start",
          sliceEnd && "exec-carousel--more-end",
        )}
        onClick={onSlice}
      >
        <div ref={row} className="exec-cards" role="region" aria-roledescription="carousel" aria-label={t("executions.carousel.label")}>
          {running.map((e) => (
            <ExecutionCard
              key={e.id}
              execution={e}
              selected={e.id === selectedId}
              onSelect={() => onSelect(e.id)}
              onCancel={() => void cancel(e.id)}
              cancelling={!!pending[e.id]}
            />
          ))}
        </div>
      </div>
    </>
  );
}
