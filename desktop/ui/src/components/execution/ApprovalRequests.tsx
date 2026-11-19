import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../../api/client";
import type { ApprovalDecision } from "../../api/types";
import type { PendingApproval } from "../../lib/approvals";
import { Banner, Button } from "../ui";

/** The decisions the owner can give, in the order they are offered. Codex's own "cancel" is not one. */
export const APPROVAL_DECISIONS: ApprovalDecision[] = ["accept", "accept_for_session", "decline"];

/**
 * The approval requests a running execution waits on, each read in full — the command, its working directory, the
 * harness's reason, the network host — with the three answers the owner can give. HarmFlow sends exactly the one
 * chosen, and answers none by itself.
 */
export function ApprovalRequests({ executionId, requests }: { executionId: string; requests: PendingApproval[] }) {
  const { t } = useTranslation();
  const [sending, setSending] = useState<Record<string, boolean>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  if (!requests.length) return null;

  const answer = async (q: PendingApproval, decision: ApprovalDecision) => {
    setSending((s) => ({ ...s, [q.requestId]: true }));
    setErrors((s) => ({ ...s, [q.requestId]: "" }));
    try {
      await api.answerApproval(executionId, q.requestId, decision);
    } catch (err) {
      setErrors((s) => ({ ...s, [q.requestId]: err instanceof Error ? err.message : String(err) }));
      setSending((s) => ({ ...s, [q.requestId]: false }));
    }
  };

  return (
    <div className="detail__alerts" data-testid="approval-requests">
      {requests.map((q) => {
        const rows: [string, string][] = [];
        if (q.command) rows.push([t("executions.approval.command"), q.command]);
        if (q.cwd) rows.push([t("executions.approval.cwd"), q.cwd]);
        if (q.reason) rows.push([t("executions.approval.reason"), q.reason]);
        if (q.networkHost) rows.push([t("executions.approval.networkHost"), q.networkHost]);
        if (q.paths?.length) rows.push([t("executions.approval.paths"), q.paths.join("\n")]);
        if (q.network) rows.push([t("executions.approval.network"), "✓"]);
        return (
          <Banner key={`${q.attempt ?? 0}:${q.requestId}`} tone="warning" icon="shield">
            <div className="approval">
              <strong>
                {t("executions.approval.title")} ·{" "}
                {t(`executions.approval.kinds.${q.kind}` as never, { defaultValue: q.kind }) as string}
              </strong>
              <span>
                {t("executions.approval.intro", {
                  harness: q.harness ? (t(`harness.names.${q.harness}` as never, { defaultValue: q.harness }) as string) : "",
                })}
              </span>
              <dl className="approval__fields">
                {rows.map(([label, value]) => (
                  <div key={label} style={{ display: "contents" }}>
                    <dt>{label}</dt>
                    <dd className="mono">{value}</dd>
                  </div>
                ))}
              </dl>
              <div className="approval__actions">
                {APPROVAL_DECISIONS.map((d) => (
                  <Button
                    key={d}
                    variant={d === "decline" ? "danger-outline" : d === "accept" ? "primary" : "secondary"}
                    disabled={sending[q.requestId]}
                    onClick={() => void answer(q, d)}
                  >
                    {t(`executions.approval.${d}`)}
                  </Button>
                ))}
              </div>
              <small>{t("executions.approval.sessionHint")}</small>
              {errors[q.requestId] ? <span role="alert">{errors[q.requestId]}</span> : null}
            </div>
          </Banner>
        );
      })}
    </div>
  );
}
