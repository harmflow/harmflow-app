import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { Counters } from "../components/Aside";
import { HarnessRow, ToolRow } from "../components/Harnesses";
import { HarnessLabel, noticeText } from "../components/Shell";
import { Button, Chip, EmptyState, Panel, StatusMark } from "../components/ui";
import { fieldText } from "../lib/daemonText";
import { formatDuration, formatRelative } from "../lib/format";
import { useNow } from "../lib/hooks";
import { actions, useStore } from "../state/store";

export function OverviewScreen({
  onOpenExecution,
  onNewTask,
  onOpenTask,
}: {
  onOpenExecution: (id: string) => void;
  onNewTask: () => void;
  onOpenTask: (id: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const health = useStore((s) => s.health);
  const settings = useStore((s) => s.settings);
  const daemonRunning = useStore((s) => s.daemonRunning);
  const connection = useStore((s) => s.connection);
  const harnesses = useStore((s) => s.harnesses);
  const tools = useStore((s) => s.tools);
  const tasks = useStore((s) => s.tasks);
  const recent = useStore((s) => s.recent);
  const unread = useStore((s) => s.unread);
  const now = useNow(30000);

  const upcoming = useMemo(
    () =>
      tasks
        .filter((task) => task.enabled && task.nextFireAt)
        .sort((a, b) => Date.parse(a.nextFireAt!) - Date.parse(b.nextFireAt!))
        .slice(0, 6),
    [tasks],
  );
  const attention = useMemo(
    () => recent.filter((e) => e.status === "failed" || e.status === "needs_attention" || e.status === "interrupted").slice(0, 5),
    [recent],
  );
  const enabled = tasks.filter((x) => x.enabled).length;
  const running = connection === "connected" || daemonRunning === true;

  return (
    <div className="screen">
      <Counters />
      <div className="overview-grid">
        <Panel title={t("overview.daemon")} icon="power">
          {/* Running now and autostart enabled are two different states. */}
          <div className="state-pair">
            <div className="state-pair__item">
              <span className="state-pair__label">{t("overview.runningNow")}</span>
              <Chip tone={running ? "success" : "danger"} icon={running ? "check" : "x"}>
                {running ? t("common.yes") : t("overview.notRunning")}
              </Chip>
              {health ? (
                <span className="mono muted">
                  {t("overview.pid", { pid: health.pid })} ·{" "}
                  {t("overview.uptime", { time: formatDuration(now - Date.parse(health.startedAt)) })}
                </span>
              ) : null}
            </div>
            <div className="state-pair__item">
              <span className="state-pair__label">{t("overview.autostart")}</span>
              <Chip tone={settings?.autostart.enabled ? "success" : "neutral"} icon={settings?.autostart.enabled ? "check" : "circle"}>
                {settings?.autostart.enabled ? t("overview.autostartOn") : t("overview.autostartOff")}
              </Chip>
            </div>
          </div>
        </Panel>

        <Panel title={t("overview.harnesses")} icon="agent">
          {harnesses.map((h) => (
            <HarnessRow key={h.id} harness={h} />
          ))}
          <h3 className="subhead">{t("overview.tools")}</h3>
          {tools.map((tool) => (
            <ToolRow key={tool.id} tool={tool} />
          ))}
        </Panel>

        <Panel
          title={t("overview.upcoming")}
          icon="clock"
          actions={<span className="muted">{t("overview.tasksCount", { enabled, total: tasks.length })}</span>}
        >
          {tasks.length === 0 ? (
            <EmptyState
              icon="list"
              title={t("overview.noTasks")}
              action={
                <Button variant="primary" icon="plus" onClick={onNewTask}>
                  {t("overview.createFirst")}
                </Button>
              }
            />
          ) : (
            <ul className="simple-list">
              {upcoming.map((task) => (
                <li key={task.id}>
                  <button type="button" className="simple-row" onClick={() => onOpenTask(task.id)}>
                    <span className="simple-row__main">
                      <strong>{task.name}</strong>
                      <span className="mono muted">{task.schedule}</span>
                    </span>
                    <HarnessLabel harness={task.harness} taskType={task.type} />
                    <span className="simple-row__aside">{formatRelative(task.nextFireAt, locale, now)}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Panel>

        <Panel title={t("overview.attention")} icon="alert">
          {attention.length === 0 ? <EmptyState icon="check" title={t("overview.noAttention")} /> : null}
          <ul className="simple-list">
            {attention.map((e) => (
              <li key={e.id}>
                <button type="button" className="simple-row" onClick={() => onOpenExecution(e.id)}>
                  <StatusMark status={e.status} />
                  <span className="simple-row__main">
                    <strong>{e.taskName}</strong>
                    <span className="mono muted">{fieldText(e.errorMessage, e.errorKey, e.errorParams) ?? e.summary ?? e.workItem ?? e.id}</span>
                  </span>
                  <span className="simple-row__aside">{formatRelative(e.endedAt ?? e.createdAt, locale, now)}</span>
                </button>
              </li>
            ))}
          </ul>
        </Panel>

        <Panel
          title={t("overview.unread")}
          icon="bell"
          className="overview-grid__wide"
          actions={
            unread.length ? (
              <button type="button" className="link" onClick={() => void actions.markRead()}>
                {t("header.markAllRead")}
              </button>
            ) : null
          }
        >
          {unread.length === 0 ? <EmptyState icon="bell" title={t("overview.noUnread")} /> : null}
          <ul className="simple-list">
            {unread.slice(0, 8).map((n) => {
              const text = noticeText(t, n);
              return (
                <li key={n.id}>
                  <button
                    type="button"
                    className="simple-row"
                    onClick={() => {
                      void actions.markRead([n.id]);
                      if (n.executionId) onOpenExecution(n.executionId);
                    }}
                  >
                    <span className="simple-row__main">
                      <strong>{text.title}</strong>
                      <span className="muted">{text.body}</span>
                    </span>
                    <span className="simple-row__aside">{formatRelative(n.createdAt, locale, now)}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        </Panel>
      </div>
    </div>
  );
}
