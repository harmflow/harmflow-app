// Every execution, newest first, kept with no time limit. Opening one replays its stored
// events, or says its log expired.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type ExecutionQuery } from "../api/client";
import type { ExecutionStatus, ExecutionSummary, Trigger } from "../api/types";
import { ExecutionDetail } from "../components/ExecutionDetail";
import { HarnessLabel } from "../components/Shell";
import { Banner, Button, Chip, EmptyState, IconButton, Select, StatusChip, TextInput } from "../components/ui";
import { formatDateTime, formatElapsed, formatNumber } from "../lib/format";
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

/** 00:00 of a date input's day (`YYYY-MM-DD`), days after it, in the user's time zone. */
function midnight(day: string, after = 0): Date {
  const [y, m, d] = day.split("-").map(Number);
  return new Date(y, m - 1, d + after);
}

/**
 * The period of the start and end dates as the instants the daemon compares: from 00:00 of the start date, inclusive,
 * to 00:00 of the day after the end date, exclusive. Either date may be empty; an end before the start is no period.
 */
function period(from: string, to: string): Pick<ExecutionQuery, "createdFrom" | "createdUntil"> | null {
  if (from && to && to < from) return null;
  return {
    createdFrom: from ? midnight(from).toISOString() : undefined,
    createdUntil: to ? midnight(to, 1).toISOString() : undefined,
  };
}

/** The executions held, then a page's, leaving out the ones already held. */
function append(held: ExecutionSummary[], page: ExecutionSummary[]): ExecutionSummary[] {
  return [...held, ...page.filter((p) => !held.some((r) => r.id === p.id))];
}

export function HistoryScreen({ openId, onOpen }: { openId: string | null; onOpen: (id: string | null) => void }) {
  const { t, i18n } = useTranslation();
  const tasks = useStore((s) => s.tasks);
  const lastEventId = useStore((s) => s.lastEventId);
  const [status, setStatus] = useState("");
  const [taskId, setTaskId] = useState("");
  const [trigger, setTrigger] = useState("");
  const [search, setSearch] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [rows, setRows] = useState<ExecutionSummary[]>([]);
  // How many executions the filters match, as the daemon counts them; none until it says.
  const [total, setTotal] = useState<number | undefined>(undefined);
  const [loading, setLoading] = useState(false);
  const [exhausted, setExhausted] = useState(false);
  const refreshKey = useDebounced(lastEventId, 1500);
  // The daemon filters by the search text, so it is sent once the user stops typing, not on every key.
  const searched = useDebounced(search.trim(), 300);
  const bounds = useMemo(() => period(from, to), [from, to]);

  // How many executions are shown, which the automatic refresh fetches again, and the filters' generation, which a
  // change of a filter moves on so that a refresh begun under the old filters drops its result.
  const loaded = useRef(0);
  useEffect(() => {
    loaded.current = rows.length;
  }, [rows]);
  const generation = useRef(0);

  // Every filter is the daemon's, so that a page, its cursor, and the total all follow them. An invalid period is no
  // query: the screen says so and keeps what is shown.
  const pageQuery = useMemo(() => {
    if (!bounds) return null;
    const chosen = { status: status || undefined, taskId: taskId || undefined, trigger: trigger || undefined, search: searched || undefined };
    return (before?: string): ExecutionQuery => ({ ...chosen, limit: PAGE, before, ...bounds });
  }, [status, taskId, trigger, searched, bounds]);

  const load = useCallback(
    async (before?: string) => {
      if (!pageQuery) return;
      setLoading(true);
      try {
        const page = await api.executionPage(pageQuery(before));
        setRows((prev) => (before ? append(prev, page.rows) : page.rows));
        setTotal(page.total);
        setExhausted(page.rows.length < PAGE);
      } catch {
        // The connection banner tells the user; keep what is shown.
      } finally {
        setLoading(false);
      }
    },
    [pageQuery],
  );

  // The automatic refresh fetches again, page by page, as many executions as are shown, and at least one page, then
  // replaces the rows with them: the rows take their new state, new executions come in at the top, and the pages added
  // with Load more stay. One request for all of them is not made, since the daemon caps a page.
  const refresh = useCallback(async () => {
    if (!pageQuery) return;
    const wanted = loaded.current;
    const begun = generation.current;
    setLoading(true);
    try {
      let held: ExecutionSummary[] = [];
      let page: { rows: ExecutionSummary[]; total?: number };
      do {
        const before = held[held.length - 1]?.createdAt;
        page = await api.executionPage(pageQuery(before));
        held = append(held, page.rows);
      } while (page.rows.length === PAGE && held.length < wanted);
      if (generation.current !== begun) return;
      setRows(held);
      setTotal(page.total);
      setExhausted(page.rows.length < PAGE);
    } catch {
      // The connection banner tells the user; keep what is shown.
    } finally {
      setLoading(false);
    }
  }, [pageQuery]);

  // A change of a filter starts again from the first page, since what was loaded belongs to the old filters; an
  // execution event, once debounced, refreshes what is shown.
  const seen = useRef({ load, refreshKey });
  useEffect(() => {
    const prev = seen.current;
    seen.current = { load, refreshKey };
    if (prev.load === load && prev.refreshKey !== refreshKey) {
      void refresh();
      return;
    }
    generation.current++;
    void load();
  }, [load, refresh, refreshKey]);

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
        <TextInput
          type="date"
          className="filters__date"
          value={from}
          max={to || undefined}
          onChange={(e) => setFrom(e.target.value)}
          aria-label={t("history.filters.from")}
        />
        <TextInput
          type="date"
          className="filters__date"
          value={to}
          min={from || undefined}
          onChange={(e) => setTo(e.target.value)}
          aria-label={t("history.filters.to")}
        />
        <IconButton icon="refresh" label={t("common.refresh")} className="icon-btn--framed" onClick={() => void load()} />
      </div>
      {bounds ? null : <Banner tone="warning">{t("history.filters.invalidPeriod")}</Banner>}

      <div className="panel panel--table">
        {rows.length === 0 && !loading ? (
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
                {rows.map((r) => (
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
          {total !== undefined && rows.length ? (
            <span className="muted">
              {t("history.count", { loaded: formatNumber(rows.length, i18n.language), total: formatNumber(total, i18n.language) })}
            </span>
          ) : null}
          {exhausted ? (
            <span className="muted">{rows.length ? t("history.noMore") : null}</span>
          ) : (
            <Button disabled={loading || !bounds} onClick={() => void load(rows[rows.length - 1]?.createdAt)}>
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
