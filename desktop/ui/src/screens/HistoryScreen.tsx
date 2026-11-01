// Every execution, newest first, kept with no time limit. Opening one replays its stored
// events, or says its log expired.
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import type { ExecutionStatus, ExecutionSummary, Trigger } from "../api/types";
import { ExecutionDetail } from "../components/ExecutionDetail";
import { HarnessLabel } from "../components/Shell";
import { Button, Chip, EmptyState, IconButton, Select, StatusChip, TextInput } from "../components/ui";
import { formatDateTime, formatElapsed } from "../lib/format";
import { useDebounced } from "../lib/hooks";
import { useStore } from "../state/store";

const STATUSES: ExecutionStatus[] = [
  "queued",
  "preparing",
  "running",
  "switching_harness",
  "testing",
  "publishing",
  "completed",
  "no_work",
  "failed",
  "cancelled",
  "interrupted",
  "needs_attention",
];
const TRIGGERS: Trigger[] = ["manual", "schedule", "catch_up"];
const PAGE = 50;

export function HistoryScreen({ openId, onOpen }: { openId: string | null; onOpen: (id: string | null) => void }) {
  const { t, i18n } = useTranslation();
  const tasks = useStore((s) => s.tasks);
  const lastEventId = useStore((s) => s.lastEventId);
  const [status, setStatus] = useState("");
  const [taskId, setTaskId] = useState("");
  const [trigger, setTrigger] = useState("");
  const [search, setSearch] = useState("");
  const [rows, setRows] = useState<ExecutionSummary[]>([]);
  const [loading, setLoading] = useState(false);
  const [exhausted, setExhausted] = useState(false);
  const refreshKey = useDebounced(lastEventId, 1500);

  const load = useCallback(
    async (before?: string) => {
      setLoading(true);
      try {
        const page = await api.executions({ status: status || undefined, taskId: taskId || undefined, limit: PAGE, before });
        setRows((prev) => (before ? [...prev, ...page.filter((p) => !prev.some((r) => r.id === p.id))] : page));
        setExhausted(page.length < PAGE);
      } catch {
        // The connection banner tells the user; keep what is shown.
      } finally {
        setLoading(false);
      }
    },
    [status, taskId],
  );

  useEffect(() => {
    void load();
  }, [load, refreshKey]);

  const visible = useMemo(() => {
    const q = search.trim().toLowerCase();
    return rows.filter((r) => {
      if (trigger && r.trigger !== trigger) return false;
      if (!q) return true;
      return [r.taskName, r.taskId, r.repository, r.workItem, r.id, r.harness].some((v) => v?.toLowerCase().includes(q));
    });
  }, [rows, trigger, search]);

  const opened = rows.find((r) => r.id === openId);

  return (
    <div className="screen">
      <div className="filters">
        <TextInput
          className="filters__search"
          placeholder={t("history.filters.searchPlaceholder")}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          aria-label={t("common.search")}
        />
        <Select value={status} onChange={(e) => setStatus(e.target.value)} aria-label={t("history.filters.status")}>
          <option value="">{t("history.filters.anyStatus")}</option>
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {t(`status.${s}`)}
            </option>
          ))}
        </Select>
        <Select value={taskId} onChange={(e) => setTaskId(e.target.value)} aria-label={t("history.filters.task")}>
          <option value="">{t("history.filters.anyTask")}</option>
          {tasks.map((task) => (
            <option key={task.id} value={task.id}>
              {task.name}
            </option>
          ))}
        </Select>
        <Select value={trigger} onChange={(e) => setTrigger(e.target.value)} aria-label={t("history.filters.trigger")}>
          <option value="">{t("history.filters.anyTrigger")}</option>
          {TRIGGERS.map((tr) => (
            <option key={tr} value={tr}>
              {t(`trigger.${tr}`)}
            </option>
          ))}
        </Select>
        <IconButton icon="refresh" label={t("common.refresh")} className="icon-btn--framed" onClick={() => void load()} />
      </div>

      <div className="panel panel--table">
        {visible.length === 0 && !loading ? (
          <EmptyState icon="history" title={t("history.empty")} />
        ) : (
          <div className="table__scroll">
            <table className="table table--interactive">
              <thead>
                <tr>
                  <th>{t("history.columns.status")}</th>
                  <th>{t("history.columns.task")}</th>
                  <th>{t("history.columns.workItem")}</th>
                  <th>{t("history.columns.harness")}</th>
                  <th>{t("history.columns.trigger")}</th>
                  <th>{t("history.columns.started")}</th>
                  <th>{t("history.columns.duration")}</th>
                  <th>{t("history.columns.result")}</th>
                </tr>
              </thead>
              <tbody>
                {visible.map((r) => (
                  <tr key={r.id} onClick={() => onOpen(r.id)} className={r.id === openId ? "is-selected" : undefined}>
                    <td>
                      <StatusChip status={r.status} compact />
                    </td>
                    <td>
                      <strong>{r.taskName}</strong>
                      <div className="mono muted small">{r.id}</div>
                    </td>
                    <td className="mono">{r.workItem ?? "—"}</td>
                    <td>
                      <HarnessLabel harness={r.harness} model={r.model} taskType={r.taskType} />
                      {r.attemptCount && r.attemptCount > 1 ? <span className="muted small"> ×{r.attemptCount}</span> : null}
                    </td>
                    <td>{t(`trigger.${r.trigger}`)}</td>
                    <td className="mono small">{formatDateTime(r.startedAt ?? r.createdAt, i18n.language)}</td>
                    <td className="mono">{r.startedAt ? formatElapsed(r.startedAt, r.endedAt, i18n.language) : "—"}</td>
                    <td>
                      {r.testsStatus ? (
                        <Chip tone={r.testsStatus === "passed" ? "success" : r.testsStatus === "failed" ? "danger" : "neutral"} icon="test">
                          {t(`testsStatus.${r.testsStatus}`)}
                        </Chip>
                      ) : null}
                      {r.pullRequestUrl ? <Chip tone="success" icon="pr">PR</Chip> : null}
                      {r.warningCount ? <Chip tone="warning" icon="shield">{t("warnings.count", { count: r.warningCount })}</Chip> : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="table__footer">
          {exhausted ? (
            <span className="muted">{rows.length ? t("history.noMore") : null}</span>
          ) : (
            <Button disabled={loading} onClick={() => void load(rows[rows.length - 1]?.createdAt)}>
              {loading ? t("common.loading") : t("history.loadMore")}
            </Button>
          )}
        </div>
      </div>

      {openId ? (
        <div className="drawer" role="dialog" aria-modal="true" onMouseDown={(e) => e.target === e.currentTarget && onOpen(null)}>
          <div className="drawer__card">
            <div className="drawer__close">
              <IconButton icon="x" label={t("common.close")} onClick={() => onOpen(null)} />
            </div>
            <ExecutionDetail executionId={openId} summary={opened} />
          </div>
        </div>
      ) : null}
    </div>
  );
}
