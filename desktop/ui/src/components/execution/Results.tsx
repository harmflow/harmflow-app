import { useTranslation } from "react-i18next";
import { native } from "../../api/native";
import type { Execution, ExecutionSummary } from "../../api/types";
import { daemonText, fieldText } from "../../lib/daemonText";
import { useStore } from "../../state/store";
import { Icon } from "../Icon";
import { Banner, Button, Chip, EmptyState, KeyValue } from "../ui";

export function Results({ detail, summary }: { detail?: Execution; summary: ExecutionSummary }) {
  const { t } = useTranslation();
  const items = detail?.workItems ?? [];
  const showRestrictions = useStore((s) => s.settings?.showRestrictionWarnings ?? true);
  const warnings = (detail?.warnings ?? []).filter((w) => showRestrictions || w.code !== "policy_unenforced");
  if (!items.length && !summary.summary && !summary.errorMessage && !summary.pullRequestUrl && !warnings.length && !detail?.lastOperation) {
    return <EmptyState icon="pr" title={t("executions.detail.noResults")} />;
  }
  return (
    <div className="results">
      {summary.summary ? (
        <div className="results__block">
          <h4>{t("executions.detail.summary")}</h4>
          <p className="results__summary">{summary.summary}</p>
        </div>
      ) : null}
      {summary.errorMessage ? (
        <Banner tone="danger" icon="x">
          <span className="mono">{fieldText(summary.errorMessage, summary.errorKey, summary.errorParams)}</span>
        </Banner>
      ) : null}
      {items.map((w) => (
        <div className="results__block results__item" key={w.key}>
          <h4 className="mono">{w.title ? `${w.key} — ${w.title}` : w.key}</h4>
          <KeyValue
            items={[
              [t("executions.detail.branch"), w.branch ? <span className="mono">{w.branch}</span> : null],
              [
                t("executions.detail.tests"),
                w.tests?.status ? (
                  <Chip tone={w.tests.status === "passed" ? "success" : w.tests.status === "failed" ? "danger" : "neutral"} icon="test">
                    {t(`testsStatus.${w.tests.status}`)}
                  </Chip>
                ) : null,
              ],
              [
                t("executions.detail.pullRequest"),
                w.pullRequest?.url ? (
                  <Button variant="link" className="mono" onClick={() => void native.openUrl(w.pullRequest!.url!)}>
                    #{w.pullRequest.number} <Icon name="external" size={12} />
                  </Button>
                ) : null,
              ],
              [t("executions.detail.commits"), w.commits?.length ? <span className="mono">{w.commits.map((c) => c.slice(0, 7)).join(", ")}</span> : null],
              [t("executions.detail.workspace"), w.worktreePath ? <span className="mono">{w.worktreePath}</span> : null],
            ]}
          />
          {w.tests?.summary ? <pre className="mono results__tests">{w.tests.summary}</pre> : null}
        </div>
      ))}
      {!items.length && summary.pullRequestUrl ? (
        <Button icon="external" onClick={() => void native.openUrl(summary.pullRequestUrl!)}>
          {t("executions.detail.openPullRequest")}
        </Button>
      ) : null}
      {detail?.lastOperation ? (
        <div className="results__block">
          <h4>{t("executions.detail.lastOperation")}</h4>
          <p className="mono">{detail.lastOperation}</p>
        </div>
      ) : null}
      {warnings.length ? (
        <div className="results__block">
          <h4>{t("executions.detail.warnings")}</h4>
          {warnings.map((w, i) => (
            <Banner key={i} tone="warning" icon="shield">
              <strong>{t(`warnings.${w.code}`)}</strong> — {daemonText(w)}
            </Banner>
          ))}
        </div>
      ) : null}
    </div>
  );
}
