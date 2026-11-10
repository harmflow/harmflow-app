import { useTranslation } from "react-i18next";
import type { Execution } from "../../api/types";
import { fieldText } from "../../lib/daemonText";
import { formatDateTime } from "../../lib/format";
import { HarnessLabel } from "../Shell";
import { Icon } from "../Icon";
import { Chip, CopyButton, Elapsed, EmptyState, KeyValue } from "../ui";

export function Attempts({ detail, locale }: { detail?: Execution; locale: string }) {
  const { t } = useTranslation();
  const attempts = detail?.attempts ?? [];
  if (!attempts.length) return <EmptyState icon="layers" title={t("executions.detail.noAttempts")} />;
  return (
    <ol className="attempts">
      {attempts.map((a) => (
        <li key={a.number} className="attempt">
          <header className="attempt__header">
            <strong>{t("executions.detail.attempt", { number: a.number })}</strong>
            <HarnessLabel harness={a.harness} model={a.model} />
            {a.outcome ? (
              <Chip
                tone={a.outcome === "succeeded" ? "success" : a.outcome === "running" ? "cyan" : a.outcome === "quota_exhausted" || a.outcome === "harness_unavailable" ? "magenta" : a.outcome === "cancelled" ? "neutral" : "danger"}
              >
                {t(`attemptOutcome.${a.outcome}`)}
              </Chip>
            ) : null}
          </header>
          <KeyValue
            items={[
              [t("executions.detail.model"), <UsedValue used={a.model} configured={a.configuredModel} />],
              [t("executions.detail.effort"), <UsedValue used={a.effort} configured={a.configuredEffort} />],
              [t("executions.detail.harnessVersion"), a.harnessVersion ? <span className="mono">{a.harnessVersion}</span> : null],
              // The installation the attempt ran on and its home: its path, never anything read from it.
              [
                t("executions.detail.installation"),
                a.installation ? (
                  <span className="mono">
                    {a.installation}
                    {a.installationHome ? <span className="muted"> · {a.installationHome}</span> : null}
                  </span>
                ) : null,
              ],
              [t("executions.detail.reason"), fieldText(a.reason, a.reasonKey, a.reasonParams) ?? null],
              [t("executions.detail.started"), formatDateTime(a.startedAt, locale)],
              [t("executions.detail.duration"), <Elapsed start={a.startedAt} end={a.endedAt} locale={locale} />],
              [t("executions.detail.exitCode"), a.exitCode !== undefined ? <span className="mono">{a.exitCode}</span> : null],
              [
                t("executions.detail.error"),
                a.errorMessage ? <span className="mono">{fieldText(a.errorMessage, a.errorKey, a.errorParams)}</span> : null,
              ],
            ]}
          />
          <div className="attempt__input">
            <AttemptInput label={t("executions.detail.prompt")} text={a.prompt} />
            <AttemptInput label={t("executions.detail.sessionContext")} text={a.sessionContext} />
          </div>
        </li>
      ))}
    </ol>
  );
}

/** What the harness reported using, or that it reported nothing — never the configured value in its place. */
function UsedValue({ used, configured }: { used?: string; configured: string }) {
  const { t } = useTranslation();
  return (
    <>
      {used ? <span className="mono">{used}</span> : <span className="muted">{t("executions.detail.notReported")}</span>}
      {configured && configured !== used ? (
        <span className="muted"> · {t("executions.detail.configured", { value: configured })}</span>
      ) : null}
    </>
  );
}

/** What the attempt sent the harness, as the daemon recorded and redacted it; older records have none. */
function AttemptInput({ label, text }: { label: string; text?: string }) {
  const { t } = useTranslation();
  if (!text) {
    return (
      <div className="attempt-input attempt-input--empty">
        <span>{label}</span>
        <span className="muted">{t("executions.detail.notRecorded")}</span>
      </div>
    );
  }
  return (
    <details className="attempt-input">
      <summary>
        <Icon name="chevronRight" size={14} />
        {label}
      </summary>
      <div className="codebox">
        <div className="codebox__tools">
          <CopyButton text={text} />
        </div>
        <pre className="mono">{text}</pre>
      </div>
    </details>
  );
}
