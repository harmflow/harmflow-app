// The right column of the executions screen: the queue, the recent history, and a quiet signature card.
import { useTranslation } from "react-i18next";
import type { Stats } from "../api/types";
import type { Tone } from "../design/tokens";
import { formatRelative } from "../lib/format";
import { useNow } from "../lib/hooks";
import { useStore } from "../state/store";
import { Icon, type IconName } from "./Icon";
import { Chip, EmptyState, Panel, Sparkline, StatusMark, cx } from "./ui";

export function QueuePanel({ onOpenExecution }: { onOpenExecution: (id: string) => void }) {
  const { t, i18n } = useTranslation();
  const queue = useStore((s) => s.queue);
  const now = useNow(30000);
  const waiting = queue?.waiting ?? [];
  const upcoming = [...(queue?.upcoming ?? [])].sort((a, b) => Date.parse(a.nextFireAt) - Date.parse(b.nextFireAt)).slice(0, 6);
  const count = waiting.length + upcoming.length;

  return (
    <Panel title={t("queue.title", { count })} icon="layers" className="aside-panel">
      {count === 0 ? <EmptyState icon="clock" title={t("queue.empty")} /> : null}
      <ul className="queue-list">
        {waiting.map((e) => (
          <li key={e.id}>
            <button type="button" className="queue-item" onClick={() => onOpenExecution(e.id)}>
              <span className="queue-item__icon">
                <Icon name="file" size={18} />
              </span>
              <span className="queue-item__text">
                <span className="queue-item__title">{e.taskName}</span>
                <span className="queue-item__sub mono">{t("queue.taskLabel", { task: e.taskId })}</span>
                <span className="queue-item__when">
                  <Icon name="clock" size={13} /> {t(`trigger.${e.trigger}`)} · {formatRelative(e.createdAt, i18n.language, now)}
                </span>
              </span>
              <Chip tone="violet">{t("queue.waiting")}</Chip>
            </button>
          </li>
        ))}
        {upcoming.map((u) => {
          const due = Date.parse(u.nextFireAt) <= now;
          return (
            <li key={u.taskId}>
              <div className="queue-item">
                <span className="queue-item__icon">
                  <Icon name="clock" size={18} />
                </span>
                <span className="queue-item__text">
                  <span className="queue-item__title">{u.taskName}</span>
                  <span className="queue-item__sub mono">{t("queue.taskLabel", { task: u.taskId })}</span>
                  <span className="queue-item__when">
                    <Icon name="clock" size={13} /> {t("queue.nextRun")}{" "}
                    <strong>{due ? t("queue.overdue") : formatRelative(u.nextFireAt, i18n.language, now)}</strong>
                  </span>
                </span>
                <Chip tone="blue">{t("queue.upcoming")}</Chip>
              </div>
            </li>
          );
        })}
      </ul>
    </Panel>
  );
}

export function RecentHistory({ onOpenExecution, onSeeAll }: { onOpenExecution: (id: string) => void; onSeeAll: () => void }) {
  const { t, i18n } = useTranslation();
  const recent = useStore((s) => s.recent);
  const now = useNow(30000);

  return (
    <Panel
      title={t("history.recentTitle")}
      icon="history"
      className="aside-panel"
      actions={
        <button type="button" className="link" onClick={onSeeAll}>
          {t("common.seeAll")} <Icon name="arrowRight" size={13} />
        </button>
      }
    >
      {recent.length === 0 ? <EmptyState icon="history" title={t("history.empty")} /> : null}
      <ul className="history-list">
        {recent.slice(0, 7).map((e) => (
          <li key={e.id}>
            <button type="button" className="history-item" onClick={() => onOpenExecution(e.id)}>
              <StatusMark status={e.status} />
              <span className="history-item__text">
                <span className="history-item__title">
                  {e.workItem ? `${e.taskName} · ${e.workItem.split("/").pop()}` : e.taskName}
                </span>
                <span className="history-item__sub mono">{t("queue.taskLabel", { task: e.taskId })}</span>
              </span>
              <span className="history-item__time">{formatRelative(e.endedAt ?? e.createdAt, i18n.language, now)}</span>
            </button>
          </li>
        ))}
      </ul>
    </Panel>
  );
}

export function QuoteCard() {
  const { t } = useTranslation();
  return (
    <figure className="quote-card">
      <blockquote>“{t("app.quote")}”</blockquote>
      <figcaption className="mono">{t("app.quoteSignature")}</figcaption>
    </figure>
  );
}

interface CounterDef {
  key: "running" | "queued" | "completedToday" | "failedToday";
  activity: keyof Stats["activity"];
  icon: IconName;
  tone: Tone;
  sparkTone?: Tone;
}

const COUNTERS: CounterDef[] = [
  { key: "running", activity: "running", icon: "playFilled", tone: "cyan" },
  { key: "queued", activity: "queued", icon: "clock", tone: "warning" },
  { key: "completedToday", activity: "completed", icon: "check", tone: "success" },
  { key: "failedToday", activity: "failed", icon: "x", tone: "danger", sparkTone: "magenta" },
];

export function Counters() {
  const { t, i18n } = useTranslation();
  const stats = useStore((s) => s.stats);
  return (
    <div className="counters">
      {COUNTERS.map((c) => (
        <div key={c.key} className={cx("counter", `counter--${c.tone}`)}>
          <span className="counter__icon">
            <Icon name={c.icon} size={22} />
          </span>
          <span className="counter__text">
            <span className="counter__value">
              {stats ? new Intl.NumberFormat(i18n.language).format(stats[c.key]) : "—"}
            </span>
            <span className="counter__label">{t(`counters.${c.key}`)}</span>
          </span>
          <Sparkline values={stats?.activity[c.activity] ?? []} tone={c.sparkTone ?? c.tone} />
        </div>
      ))}
    </div>
  );
}
