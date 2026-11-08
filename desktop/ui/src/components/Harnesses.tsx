// The AI tools list: each harness detected, where, at which version, and whether it is ready.
// Credentials are never shown.
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import type { Harness, Tool } from "../api/types";
import type { Tone } from "../design/theme";
import { fieldText } from "../lib/daemonText";
import { formatDateTime, formatRelative } from "../lib/format";
import { useDraft, type DraftReport } from "../lib/hooks";
import { actions } from "../state/store";
import { HarnessMark, Icon, type IconName } from "./Icon";
import { toast } from "./toast";
import { Button, Chip, KeyValue, TextInput } from "./ui";

// A ready harness with something detection could not establish — its sign-in, or a version it can vouch for — is a
// warning, with the diagnostic saying why.
export function readinessTone(h: Harness): Tone {
  switch (h.readiness) {
    case "ready":
      return h.versionSupport === "untested_newer" || h.versionSupport === "unknown" || h.authentication === "unknown" || h.diagnostic
        ? "warning"
        : "success";
    case "checking":
    case "not_installed":
      return "neutral";
    case "not_ready":
      return "danger";
    default:
      return "warning";
  }
}

function readinessIcon(h: Harness): IconName {
  switch (h.readiness) {
    case "ready":
      return "check";
    case "checking":
      return "clock";
    case "not_installed":
      return "circle";
    default:
      return "alert";
  }
}

// While its first check runs, a harness is neither detected nor missing yet.
export function HarnessRow({ harness }: { harness: Harness }) {
  const { t } = useTranslation();
  return (
    <div className="harness-row">
      <span className={`harness-row__mark harness--${harness.id}`}>
        <HarnessMark harness={harness.id} size={20} />
      </span>
      <span className="harness-row__text">
        <strong>{harness.name || t(`harness.names.${harness.id}`)}</strong>
        <span className="mono muted">
          {harness.readiness === "checking"
            ? t("harness.readiness.checking")
            : harness.detected
              ? `${harness.version ?? "?"} · ${harness.path ?? ""}`
              : t("harness.notDetected")}
        </span>
      </span>
      <Chip tone={readinessTone(harness)} icon={readinessIcon(harness)} title={fieldText(harness.diagnostic, harness.diagnosticKey, harness.diagnosticParams)}>
        {t(`harness.readiness.${harness.readiness}`)}
      </Chip>
    </div>
  );
}

export function ToolRow({ tool }: { tool: Tool }) {
  const { t } = useTranslation();
  const tone: Tone = !tool.detected ? "neutral" : tool.authentication === "invalid" ? "danger" : "success";
  return (
    <div className="harness-row">
      <span className="harness-row__mark">
        <Icon name={tool.id === "gh" ? "pr" : "branch"} size={18} />
      </span>
      <span className="harness-row__text">
        <strong>{t(`tools.${tool.id}`)}</strong>
        <span className="mono muted">{tool.detected ? `${tool.version ?? "?"} · ${tool.path ?? ""}` : t("harness.notDetected")}</span>
      </span>
      <Chip tone={tone} icon={tool.detected ? "check" : "circle"} title={fieldText(tool.diagnostic, tool.diagnosticKey, tool.diagnosticParams)}>
        {tool.detected
          ? tool.authentication && tool.authentication !== "unknown"
            ? t(`harness.authentication.${tool.authentication}`)
            : t("harness.detected")
          : t("harness.notDetected")}
      </Chip>
    </div>
  );
}

/**
 * A harness's long-lived token: a field that saves one, or the one saved — when, until when, and whether the harness
 * rejected it — with a control that removes it. The token goes to the daemon, which keeps it in the credential store,
 * and is never shown again.
 */
export function TokenControl({ harness, locale }: { harness: Harness; locale: string }) {
  const { t } = useTranslation();
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);

  const run = async (change: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await change();
      setValue("");
      await actions.refreshHarnesses();
    } catch (err) {
      toast(t("harness.tokenFailed", { message: (err as Error).message }), "danger");
    } finally {
      setBusy(false);
    }
  };

  const token = harness.token;
  if (token) {
    return (
      <span className="inline-actions">
        <span>{t("harness.tokenSaved", { saved: formatDateTime(token.savedAt, locale), expires: formatDateTime(token.expiresAt, locale) })}</span>
        {token.rejected ? (
          <Chip tone="danger" icon="alert">
            {t("harness.tokenRejected")}
          </Chip>
        ) : null}
        <Button size="sm" variant="ghost" icon="trash" disabled={busy} onClick={() => void run(() => api.removeHarnessToken(harness.id))}>
          {t("harness.tokenRemove")}
        </Button>
      </span>
    );
  }
  return (
    <span className="inline-form">
      <TextInput
        type="password"
        autoComplete="off"
        spellCheck={false}
        aria-label={t("harness.token")}
        value={value}
        placeholder={t("harness.tokenPlaceholder")}
        onChange={(e) => setValue(e.target.value)}
        className="mono"
      />
      <Button size="sm" variant="primary" icon="key" disabled={busy || !value.trim()} onClick={() => void run(() => api.setHarnessToken(harness.id, value.trim()))}>
        {t("harness.tokenSave")}
      </Button>
    </span>
  );
}

function authenticationTone(h: Harness): Tone {
  return h.authentication === "valid" ? "success" : h.authentication === "invalid" ? "danger" : h.detected ? "warning" : "neutral";
}

/**
 * The installations of a harness's binary, one per home: each by the identifier a task names it by, its home, how ready
 * it is, and what its own probes read — its sign-in, plan, managed requirements, and models. The one in use is marked.
 */
function HomeList({ harness }: { harness: Harness }) {
  const { t } = useTranslation();
  const homes = harness.homes ?? [];
  return (
    <div>
      <ul className="installations">
        {homes.map((home) => {
          const requirements = home.managedRequirements;
          const details = [
            `${t("harness.authLabel")}: ${t(`harness.authentication.${home.authentication}`)}`,
            home.account?.plan ? `${t("harness.plan")}: ${home.account.plan === "unknown" ? t("harness.planUnknown") : home.account.plan}` : "",
            home.account ? `${t("harness.requirements")}: ${t(`harness.requirementsState.${!requirements ? "unknown" : requirements.managed ? "managed" : "none"}`)}` : "",
            home.models.length ? `${t("harness.models")}: ${home.models.map((m) => m.id).join(", ")}` : "",
            home.efforts.length ? `${t("harness.efforts")}: ${home.efforts.join(", ")}` : "",
          ].filter(Boolean);
          return (
            <li key={home.installationId} className="installations__item" title={home.home}>
              <span className="mono">{home.installationId}</span>
              <Chip tone={readinessTone(home)} icon={readinessIcon(home)} title={fieldText(home.diagnostic, home.diagnosticKey, home.diagnosticParams)}>
                {t(`harness.readiness.${home.readiness}`)}
              </Chip>
              {home.installationId === harness.installationId ? (
                <Chip tone="success" icon="check">
                  {t("harness.inUse")}
                </Chip>
              ) : null}
              <span className="muted">{details.join(" · ")}</span>
            </li>
          );
        })}
      </ul>
      <span className="muted">{t("harness.homesHint")}</span>
    </div>
  );
}

/** A harness's inventory card. A path being set by hand is a draft until its Save, reported to `onDraft`. */
export function HarnessInventory({ harness, locale, onDraft }: { harness: Harness; locale: string; onDraft?: DraftReport }) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);
  const [path, setPath] = useState(harness.path ?? "");
  const [busy, setBusy] = useState(false);
  useDraft(onDraft, `harness:${harness.id}`, editing && path !== (harness.path ?? ""));

  // The daemon runs the harness's own sign-in, which the user approves in the browser; the row follows its events.
  const signIn = async () => {
    setBusy(true);
    try {
      await api.signInHarness(harness.id);
      await actions.refreshHarnesses();
    } catch (err) {
      toast(t("harness.signInFailed", { message: (err as Error).message }), "danger");
    } finally {
      setBusy(false);
    }
  };

  const save = async (value: string | null) => {
    setBusy(true);
    try {
      await api.setHarnessPath(harness.id, value);
      await actions.refreshHarnesses();
      setEditing(false);
    } catch (err) {
      toast((err as Error).message, "danger");
    } finally {
      setBusy(false);
    }
  };

  // A harness still being checked has no check time yet.
  const checking = harness.readiness === "checking";

  // A list the requirements allow: a null one allows any value, an empty one none.
  const allowed = (list: string[] | null): ReactNode =>
    list === null ? t("harness.allowedAny") : list.length ? <span className="mono">{list.join(", ")}</span> : t("harness.allowedNone");
  // The account's rows, for a harness whose check read it: how it signs in, its plan, unknown with the reason, and
  // whether an administrator manages its requirements, with what they allow. Never a credential or an account ID.
  const { account, managedRequirements: requirements } = harness;
  const accountItems: Array<[ReactNode, ReactNode]> = [];
  if (account) {
    accountItems.push([t("harness.signInMethod"), t(`harness.signIn.${account.signInMethod}`)]);
    if (account.plan) {
      accountItems.push([
        t("harness.plan"),
        account.plan === "unknown" ? (
          <span>
            {t("harness.planUnknown")}
            {account.planReason ? <span className="muted"> · {fieldText(account.planReason, account.planReasonKey, account.planReasonParams)}</span> : null}
          </span>
        ) : (
          <span className="mono">{account.plan}</span>
        ),
      ]);
    }
    accountItems.push([t("harness.requirements"), t(`harness.requirementsState.${!requirements ? "unknown" : requirements.managed ? "managed" : "none"}`)]);
    if (requirements?.managed) {
      accountItems.push(
        [t("harness.allowedApprovalPolicies"), allowed(requirements.approvalPolicies)],
        [t("harness.allowedApprovalsReviewers"), allowed(requirements.approvalsReviewers)],
        [t("harness.allowedPermissionProfiles"), allowed(requirements.permissionProfiles)],
      );
    }
  }
  return (
    <div className="inventory">
      <HarnessRow harness={harness} />
      <KeyValue
        items={[
          [t("harness.detected"), checking ? t("harness.readiness.checking") : harness.detected ? t("common.yes") : t("common.no")],
          [
            t("harness.path"),
            harness.path ? (
              <span className="mono">
                {harness.path}
                {harness.pathSource ? <span className="muted"> · {t(`harness.pathSource.${harness.pathSource}`)}</span> : null}
              </span>
            ) : null,
          ],
          [
            t("harness.installations"),
            // Each installation is a choice, the one in use marked; one chosen is saved as a path set by hand.
            harness.installations.length ? (
              <ul className="installations">
                {harness.installations.map((entry) => (
                  <li key={entry} className="installations__item">
                    <span className="mono">{entry}</span>
                    {entry === harness.installation ? (
                      <Chip tone="success" icon="check">
                        {t("harness.inUse")}
                      </Chip>
                    ) : (
                      <Button size="sm" variant="ghost" disabled={busy} aria-label={t("harness.useInstallation", { path: entry })} onClick={() => void save(entry)}>
                        {t("harness.use")}
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            ) : null,
          ],
          // Only a harness with installations by home has the row.
          ...(harness.homes?.length ? [[t("harness.homes"), <HomeList harness={harness} />] as [string, ReactNode]] : []),
          [
            t("harness.version"),
            harness.version || harness.versionSupport ? (
              <span className="mono">
                {harness.version}
                {harness.versionSupport ? (
                  <span className="muted">
                    {harness.version ? " · " : ""}
                    {t(`harness.versionSupport.${harness.versionSupport}`)}
                  </span>
                ) : null}
              </span>
            ) : null,
          ],
          [
            t("harness.authLabel"),
            <Chip tone={authenticationTone(harness)}>
              {t(`harness.authentication.${harness.authentication}`)}
            </Chip>,
          ],
          // Only a harness the daemon keeps a token for has the row.
          ...(harness.takesToken ? [[t("harness.token"), <TokenControl harness={harness} locale={locale} />] as [string, ReactNode]] : []),
          [t("harness.models"), harness.models.length ? <span className="mono">{harness.models.map((m) => m.id).join(", ")}</span> : null],
          [t("harness.efforts"), harness.efforts.length ? <span className="mono">{harness.efforts.join(", ")}</span> : null],
          [t("harness.controls"), harness.enforceableControls?.length ? <span className="mono">{harness.enforceableControls.join(", ")}</span> : null],
          ...accountItems,
          [
            t("harness.lastCheck"),
            checking ? null : (
              <span>
                {formatRelative(harness.lastCheckedAt, locale)}
                {harness.capabilitySource ? <span className="muted"> · {t(`harness.capabilitySource.${harness.capabilitySource}`)}</span> : null}
              </span>
            ),
          ],
          [
            t("harness.diagnostic"),
            harness.diagnostic ? (
              <span className="mono">{fieldText(harness.diagnostic, harness.diagnosticKey, harness.diagnosticParams)}</span>
            ) : null,
          ],
        ]}
      />
      {editing ? (
        <div className="inline-form">
          <TextInput value={path} placeholder={t("harness.pathPlaceholder")} onChange={(e) => setPath(e.target.value)} className="mono" />
          <Button variant="primary" disabled={busy || !path.trim()} onClick={() => void save(path.trim())}>
            {t("common.save")}
          </Button>
          <Button variant="ghost" onClick={() => setEditing(false)}>
            {t("common.cancel")}
          </Button>
        </div>
      ) : (
        <div className="inline-actions">
          <Button
            size="sm"
            icon="pencil"
            onClick={() => {
              setPath(harness.path ?? ""); // a path typed and cancelled before is no draft
              setEditing(true);
            }}
          >
            {t("harness.setPath")}
          </Button>
          {harness.pathSource === "manual" ? (
            <Button size="sm" variant="ghost" icon="refresh" disabled={busy} onClick={() => void save(null)}>
              {t("harness.autoDetect")}
            </Button>
          ) : null}
          {harness.signIn ? (
            <Button size="sm" variant="ghost" icon="key" disabled={busy || harness.signIn === "running"} onClick={() => void signIn()}>
              {harness.signIn === "running" ? t("harness.signingIn") : t("harness.signInAgain")}
            </Button>
          ) : null}
        </div>
      )}
    </div>
  );
}
