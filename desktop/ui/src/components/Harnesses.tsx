// The AI tools list: each harness detected, where, at which version, and whether it is ready.
// Credentials are never shown.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import type { Harness, Tool } from "../api/types";
import type { Tone } from "../design/tokens";
import { fieldText } from "../lib/daemonText";
import { formatRelative } from "../lib/format";
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
            <Chip
              tone={
                harness.authentication === "valid"
                  ? "success"
                  : harness.authentication === "invalid"
                    ? "danger"
                    : harness.detected
                      ? "warning"
                      : "neutral"
              }
            >
              {t(`harness.authentication.${harness.authentication}`)}
            </Chip>,
          ],
          [t("harness.models"), harness.models.length ? <span className="mono">{harness.models.map((m) => m.id).join(", ")}</span> : null],
          [t("harness.efforts"), harness.efforts.length ? <span className="mono">{harness.efforts.join(", ")}</span> : null],
          [t("harness.controls"), harness.enforceableControls?.length ? <span className="mono">{harness.enforceableControls.join(", ")}</span> : null],
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
              {harness.signIn === "running" ? t("harness.signingIn") : t("harness.signIn")}
            </Button>
          ) : null}
        </div>
      )}
    </div>
  );
}
