// The first and priority screen: every active execution at once, the selected one in detail, the queue and the recent
// history beside them.
import { useEffect, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { isActive } from "../api/types";
import { Counters, QueuePanel, QuoteCard, RecentHistory } from "../components/Aside";
import { ExecutionCard, useCancel } from "../components/ExecutionCard";
import { ExecutionDetail } from "../components/ExecutionDetail";
import { Button, EmptyState, SectionTitle } from "../components/ui";
import { actions, useStore } from "../state/store";

export function ExecutionsScreen({
  selectedId,
  onSelect,
  onGoToTasks,
  onSeeHistory,
}: {
  selectedId: string | null;
  onSelect: (id: string | null) => void;
  onGoToTasks: () => void;
  onSeeHistory: () => void;
}) {
  const { t } = useTranslation();
  const all = useStore((s) => s.active);
  const recent = useStore((s) => s.recent);
  const { cancel, pending } = useCancel();
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
    <div className="screen screen--with-aside">
      <div className="screen__main">
        <Counters />
        <SectionTitle>{t("executions.activeTitle", { count: running.length })}</SectionTitle>
        {running.length === 0 ? (
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
        ) : (
          <div className="exec-cards">
            {running.map((e) => (
              <ExecutionCard
                key={e.id}
                execution={e}
                selected={e.id === effective}
                onSelect={() => onSelect(e.id)}
                onCancel={() => void cancel(e.id)}
                cancelling={!!pending[e.id]}
              />
            ))}
          </div>
        )}
        {effective ? <ExecutionDetail executionId={effective} summary={summary} /> : null}
      </div>
      <div className="screen__aside">
        <QueuePanel onOpenExecution={(id) => onSelect(id)} />
        <RecentHistory onOpenExecution={(id) => onSelect(id)} onSeeAll={onSeeHistory} />
        <QuoteCard />
      </div>
    </div>
  );
}
