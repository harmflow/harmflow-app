import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import { native } from "../api/native";
import type { ExecutionSummary } from "../api/types";
import { isActive } from "../api/types";
import { fieldText } from "../lib/daemonText";
import { formatTime } from "../lib/format";
import { toneClass, typeGlyph } from "../lib/taskType";
import { buildTimeline } from "../lib/timeline";
import { useStore } from "../state/store";
import { HarnessLabel } from "./Shell";
import { Icon } from "./Icon";
import { toast } from "./toast";
import { Button, Elapsed, ScanBar, StatusChip, cx } from "./ui";

export function useCancel() {
  const { t } = useTranslation();
  const [pending, setPending] = useState<Record<string, boolean>>({});
  const cancel = async (id: string) => {
    const ok = await native.confirm(
      t("executions.cancelConfirm"),
      t("executions.cancelConfirmTitle"),
      t("common.confirm"),
      t("common.back"),
    );
    if (!ok) return;
    setPending((p) => ({ ...p, [id]: true }));
    try {
      await api.cancelExecution(id);
    } catch (err) {
      toast(t("executions.cancelFailed", { message: (err as Error).message }), "danger");
      setPending((p) => ({ ...p, [id]: false }));
    }
  };
  return { cancel, pending };
}

/** The latest step of an execution, as the card shows it: the step and the elapsed time, no percentage. */
export function useCurrentStep(execution: ExecutionSummary): string | undefined {
  const { t } = useTranslation();
  const events = useStore((s) => s.events[execution.id]);
  if (events?.length) {
    const items = buildTimeline(events);
    const last = items[items.length - 1];
    if (last) {
      const label = last.title ?? (t(last.labelKey as never, { defaultValue: last.event.type }) as string);
      return last.detail ? `${label} · ${last.detail}` : label;
    }
  }
  return fieldText(execution.currentStep, execution.currentStepKey, execution.currentStepParams);
}

export function ExecutionCard({
  execution,
  selected,
  onSelect,
  onCancel,
  cancelling,
}: {
  execution: ExecutionSummary;
  selected: boolean;
  onSelect: () => void;
  onCancel: () => void;
  cancelling: boolean;
}) {
  const { t, i18n } = useTranslation();
  const step = useCurrentStep(execution);
  const active = isActive(execution.status);
  const glyph = typeGlyph(execution.taskType);

  return (
    <article
      className={cx("exec-card", selected && "exec-card--selected")}
      onClick={onSelect}
      onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && onSelect()}
      tabIndex={0}
      aria-pressed={selected}
    >
      <header className="exec-card__header">
        <span className={cx("exec-card__glyph", toneClass(glyph.tone))} aria-hidden="true">
          <Icon name={glyph.icon} size={20} />
        </span>
        <h3 className="exec-card__title" title={execution.taskName}>
          {execution.workItem ? `${execution.taskName} · ${execution.workItem.split("/").pop()}` : execution.taskName}
        </h3>
        <StatusChip status={execution.status} compact />
      </header>

      <div className="exec-card__rail" aria-hidden="true" />
      <dl className="exec-card__fields">
        <div>
          <dt>{t("executions.card.task")}</dt>
          <dd className="mono">{execution.taskId}</dd>
        </div>
        <div>
          <dt>{t("executions.card.agent")}</dt>
          <dd>
            <HarnessLabel harness={execution.harness} model={execution.model} taskType={execution.taskType} />
          </dd>
        </div>
        <div>
          <dt>{t("executions.card.repository")}</dt>
          <dd className="mono">
            {execution.repository ? (
              <>
                <Icon name="repo" size={13} /> {execution.repository}
              </>
            ) : (
              "—"
            )}
          </dd>
        </div>
        <div>
          <dt>{t("executions.card.start")}</dt>
          <dd className="mono">
            {formatTime(execution.startedAt ?? execution.createdAt, i18n.language)}
            <span className="exec-card__elapsed">
              <Icon name="clock" size={13} /> <Elapsed start={execution.startedAt ?? execution.createdAt} end={execution.endedAt} />
            </span>
          </dd>
        </div>
      </dl>

      <div className="exec-card__step" title={step} data-testid="execution-step">
        <span className="exec-card__step-label">{t("executions.card.step")}</span>
        <span className="mono exec-card__step-text">{step ?? t("executions.card.waitingStep")}</span>
      </div>
      <ScanBar active={active} tone={execution.status === "switching_harness" ? "magenta" : "cyan"} />

      <Button
        variant="danger-outline"
        icon="stop"
        className="exec-card__cancel"
        disabled={cancelling || !(active || execution.status === "queued")}
        onClick={(e) => {
          e.stopPropagation();
          onCancel();
        }}
      >
        {cancelling ? t("executions.card.cancelling") : t("common.cancel")}
      </Button>
    </article>
  );
}
