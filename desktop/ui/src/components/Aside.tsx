// The right column of the executions screen: the queue, the recent history, and a quiet signature card.
import { useTranslation } from "react-i18next";
import type { Stats, WorkItem } from "../api/types";
import type { Tone } from "../design/theme";
import { formatRelative } from "../lib/format";
import { useNow } from "../lib/hooks";
import { useStore } from "../state/store";
import { Icon, type IconName } from "./Icon";
import { Button, Chip, EmptyState, ListRow, ListRows, Panel, Sparkline, StatusMark, cx } from "./ui";

export function QueuePanel({ onOpenExecution }: { onOpenExecution: (id: string) => void }) {
  const { t, i18n } = useTranslation();
  const queue = useStore((s) => s.queue);
  const now = useNow(30000);
  const waiting = queue?.waiting ?? [];
  // A work item waits for a later cycle only to be resumed or retried; the other statuses are done or need a human.
  const workItems = (queue?.workItems ?? []).filter((w) => w.status === "in_progress" || w.status === "failed");
  const upcoming = [...(queue?.upcoming ?? [])].sort((a, b) => Date.parse(a.nextFireAt) - Date.parse(b.nextFireAt)).slice(0, 6);
  const count = waiting.length + workItems.length + upcoming.length;

  return (
    <Panel title={t("queue.title", { count })} icon="layers" className="aside-panel">
      {count === 0 ? <EmptyState icon="clock" title={t("queue.empty")} /> : null}
      <ListRows loose>
        {waiting.map((e) => (
          <li key={e.id}>
            <ListRow
              card
              alignTop
              icon="file"
              title={e.taskName}
              secondary={<span className="mono">{t("queue.taskLabel", { task: e.taskId })}</span>}
              meta={
                <>
                  <Icon name="clock" size={13} /> {t(`trigger.${e.trigger}`)} · {formatRelative(e.createdAt, i18n.language, now)}
                </>
              }
              aside={<Chip tone="violet">{t("queue.waiting")}</Chip>}
              onClick={() => onOpenExecution(e.id)}
            />
          </li>
        ))}
        {workItems.map((w) => (
          <li key={`${w.taskId}/${w.key}`}>
            <WorkItemRow item={w} onOpenExecution={onOpenExecution} now={now} />
          </li>
        ))}
        {upcoming.map((u) => {
          const due = Date.parse(u.nextFireAt) <= now;
          return (
            <li key={u.taskId}>
              <ListRow
                card
                alignTop
                icon="clock"
                title={u.taskName}
                secondary={<span className="mono">{t("queue.taskLabel", { task: u.taskId })}</span>}
                meta={
                  <>
                    <Icon name="clock" size={13} /> {t("queue.nextRun")}{" "}
                    <strong>{due ? t("queue.overdue") : formatRelative(u.nextFireAt, i18n.language, now)}</strong>
                  </>
                }
                aside={<Chip tone="blue">{t("queue.upcoming")}</Chip>}
              />
            </li>
          );
        })}
      </ListRows>
    </Panel>
  );
}

function WorkItemRow({ item, onOpenExecution, now }: { item: WorkItem; onOpenExecution: (id: string) => void; now: number }) {
  const { t, i18n } = useTranslation();
  const lastExecution = item.lastExecutionId;
  return (
    <ListRow
      card
      alignTop
      icon={item.pullRequestUrl ? "pr" : "issue"}
      title={item.title || item.key}
      secondary={<span className="mono">{item.repository || t("queue.taskLabel", { task: item.taskId })}</span>}
      meta={
        <>
          <Icon name="clock" size={13} /> {formatRelative(item.updatedAt, i18n.language, now)}
        </>
      }
      aside={
        item.status === "in_progress" ? (
          <Chip tone="cyan">{t("queue.toResume")}</Chip>
        ) : (
          <Chip tone="warning">{t("queue.toRetry")}</Chip>
        )
      }
      onClick={lastExecution ? () => onOpenExecution(lastExecution) : undefined}
    />
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
        <Button variant="link" onClick={onSeeAll}>
          {t("common.seeAll")} <Icon name="arrowRight" size={13} />
        </Button>
      }
    >
      {recent.length === 0 ? <EmptyState icon="history" title={t("history.empty")} /> : null}
      <ListRows loose>
        {recent.slice(0, 7).map((e) => (
          <li key={e.id}>
            <ListRow
              card
              leading={<StatusMark status={e.status} />}
              title={e.workItem ? `${e.taskName} · ${e.workItem.split("/").pop()}` : e.taskName}
              secondary={<span className="mono">{t("queue.taskLabel", { task: e.taskId })}</span>}
              trailing={formatRelative(e.endedAt ?? e.createdAt, i18n.language, now)}
              onClick={() => onOpenExecution(e.id)}
            />
          </li>
        ))}
      </ListRows>
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

export const COUNTERS: CounterDef[] = [
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
