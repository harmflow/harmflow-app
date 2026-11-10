import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import type { HarmflowEvent, MessageKey, MessageParams } from "../../api/types";
import { daemonText, eventText } from "../../lib/daemonText";
import { formatTime } from "../../lib/format";
import { matching } from "../../lib/shortcuts";
import { toolKey, type TimelineItem } from "../../lib/timeline";
import { Icon } from "../Icon";
import { Checkbox, CopyButton, EmptyState, cx } from "../ui";

export function Timeline({
  items,
  active,
  selectedId,
  onSelect,
  status,
}: {
  items: TimelineItem[];
  active: boolean;
  selectedId?: number;
  onSelect: (id: number) => void;
  status: string;
}) {
  const { t, i18n } = useTranslation();
  const listRef = useRef<HTMLOListElement | null>(null);
  const [follow, setFollow] = useState(true);
  const idPrefix = useId();
  const optionId = (id: number) => `${idPrefix}-event-${id}`;

  useEffect(() => {
    if (follow && listRef.current) listRef.current.scrollTop = listRef.current.scrollHeight;
  }, [items.length, follow]);

  // A step the user chose, by click or key, stops the timeline following the latest one.
  const select = (id: number) => {
    setFollow(false);
    onSelect(id);
  };

  // The list is one tab stop whose arrow keys step through the events, without wrapping (harmflow-app#190).
  const onKeyDown = (e: KeyboardEvent<HTMLOListElement>) => {
    if (e.target !== e.currentTarget) return;
    const key = matching(e, ["previousEvent", "nextEvent", "firstEvent", "lastEvent"]);
    if (!key) return;
    const at = items.findIndex((i) => i.id === selectedId);
    const last = items.length - 1;
    const moves = { previousEvent: Math.max(at - 1, 0), nextEvent: Math.min(at + 1, last), firstEvent: 0, lastEvent: last };
    const to = moves[key];
    e.preventDefault(); // the list does not scroll by itself, even at an edge
    if (to === at) return;
    select(items[to].id);
    listRef.current?.children[to]?.scrollIntoView({ block: "nearest" });
  };

  if (items.length === 0) {
    return (
      <div className="timeline timeline--empty">
        <EmptyState icon="list" title={t("executions.detail.noEvents")} />
      </div>
    );
  }

  return (
    <div className="timeline">
      <div className="timeline__tools">
        <Checkbox compact checked={follow} onChange={setFollow} label={t("executions.detail.follow")} />
      </div>
      <ol
        className="timeline__list"
        ref={listRef}
        role="listbox"
        tabIndex={0}
        aria-label={t("executions.detail.timeline")}
        aria-activedescendant={selectedId !== undefined ? optionId(selectedId) : undefined}
        onKeyDown={onKeyDown}
      >
        {items.map((item) => {
          const label = item.title ?? (t(item.labelKey as never, { defaultValue: item.event.type }) as string);
          const running = item.event.type === "tool.started" && !item.completed && active;
          return (
            <li
              key={item.id}
              id={optionId(item.id)}
              role="option"
              aria-selected={item.id === selectedId}
              className={cx("tl-item", `tl-item--${item.tone}`, item.id === selectedId && "tl-item--selected")}
              onClick={() => {
                select(item.id);
                // WebKit focuses nothing on a click; the arrow keys work from here on.
                listRef.current?.focus({ preventScroll: true });
              }}
            >
              <span className="tl-item__time mono">{formatTime(item.event.timestamp, i18n.language)}</span>
              <span className="tl-item__rail" aria-hidden="true">
                <span className="tl-item__dot" />
              </span>
              <span className="tl-item__icon">
                <Icon name={item.icon} size={17} />
              </span>
              <span className="tl-item__text">
                <span className="tl-item__title">
                  {label}
                  {item.attempt && item.attempt > 1 ? <span className="tl-item__attempt">#{item.attempt}</span> : null}
                </span>
                {item.detail ? <span className="tl-item__detail mono">{item.detail}</span> : null}
              </span>
              <span className="tl-item__state">
                {running ? <span className="spinner" aria-label={t("executions.detail.running")} /> : null}
                {item.completed?.declined ? (
                  <Icon name="alert" size={14} className="tone-warning" />
                ) : item.completed && !item.completed.ok ? (
                  <Icon name="x" size={14} className="tone-danger" />
                ) : null}
              </span>
            </li>
          );
        })}
        {/* Not an event to select; the header's status says the same. */}
        {active ? (
          <li className="tl-item tl-item--pending" aria-hidden="true">
            <span className="tl-item__time mono">{formatTime(new Date().toISOString(), i18n.language)}</span>
            <span className="tl-item__rail" aria-hidden="true">
              <span className="tl-item__dot tl-item__dot--hollow" />
            </span>
            <span className="tl-item__icon">
              <Icon name="circle" size={17} />
            </span>
            <span className="tl-item__text">
              <span className="tl-item__title muted">{t(`status.${status}` as never)}…</span>
            </span>
          </li>
        ) : null}
      </ol>
    </div>
  );
}

function outputFor(item: TimelineItem, events: HarmflowEvent[]): string | undefined {
  const p = (item.event.payload ?? {}) as Record<string, unknown>;
  const pick = (o: Record<string, unknown>) =>
    [o.output, o.result, o.stdout, o.text, payloadError(o)].find((v): v is string => typeof v === "string" && v !== "");
  if (item.event.type === "tool.started") {
    // A harness may end a call twice, correcting it later with no output: the first end that has one shows.
    const key = toolKey(item.event);
    const ends = key
      ? events
          .filter((e) => e.type === "tool.completed" && toolKey(e) === key && e.attempt === item.event.attempt)
          .sort((a, b) => a.id - b.id)
      : [];
    if (ends.length) {
      for (const end of ends) {
        const output = pick((end.payload ?? {}) as Record<string, unknown>);
        if (output) return output;
      }
      return eventText(ends[0]);
    }
  }
  return pick(p) ?? (item.event.type === "harness.message" ? item.event.message : undefined);
}

/** A payload's error: the daemon's text in the interface's language, a harness's own error as it is. */
function payloadError(p: Record<string, unknown>): string | undefined {
  if (typeof p.error !== "string") return undefined;
  return daemonText({ message: p.error, messageKey: p.errorKey as MessageKey | undefined, params: p.errorParams as MessageParams | undefined });
}

export function EventDetails({ item, events, locale }: { item?: TimelineItem; events: HarmflowEvent[]; locale: string }) {
  const { t } = useTranslation();
  const [moreOpen, setMoreOpen] = useState(false);
  if (!item) {
    return (
      <aside className="event-details">
        <EmptyState icon="terminal" title={t("executions.detail.noEventSelected")} />
      </aside>
    );
  }
  const e = item.event;
  const output = outputFor(item, events);
  const label = item.title ?? (t(item.labelKey as never, { defaultValue: e.type }) as string);
  const info = {
    id: e.id,
    type: e.type,
    severity: e.severity,
    attempt: e.attempt,
    harness: e.harness,
    workItemId: e.workItemId,
    payload: e.payload,
  };
  return (
    <aside className="event-details">
      <header className="event-details__header">
        <span>{t("executions.detail.eventDetails")}</span>
        <span className="mono muted">{formatTime(e.timestamp, locale)}</span>
      </header>
      <div className="event-details__card">
        <span className={cx("event-details__icon", `tone-bg-${item.tone}`)}>
          <Icon name={item.icon} size={20} />
        </span>
        <div className="event-details__text">
          <strong>{label}</strong>
          {item.detail ? <span className="mono muted">{item.detail}</span> : null}
        </div>
        {item.detail ? <CopyButton text={item.detail} /> : null}
      </div>
      {output ? (
        <>
          <h4 className="event-details__subtitle">{t("executions.detail.output")}</h4>
          <div className="codebox">
            <div className="codebox__tools">
              <CopyButton text={output} />
            </div>
            <pre className="mono">{output}</pre>
          </div>
        </>
      ) : null}
      <button type="button" className="disclosure" aria-expanded={moreOpen} onClick={() => setMoreOpen(!moreOpen)}>
        <Icon name={moreOpen ? "chevronDown" : "chevronRight"} size={14} />
        {t("executions.detail.additionalInfo")}
      </button>
      {moreOpen ? (
        <div className="codebox">
          <pre className="mono">{JSON.stringify(info, null, 2)}</pre>
        </div>
      ) : null}
    </aside>
  );
}
