// The installed-agents inventory (FS-004 FR-007): what was detected, where, at which version, and whether it is ready.
// Credentials are never shown.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import type { Harness, Tool } from "../api/types";
import type { Tone } from "../design/tokens";
import { fieldText } from "../lib/daemonText";
import { formatRelative } from "../lib/format";
import { actions } from "../state/store";
import { HarnessMark, Icon } from "./Icon";
import { toast } from "./toast";
import { Button, Chip, KeyValue, TextInput } from "./ui";

// A ready harness with something detection could not establish — its sign-in, or a version it can vouch for — is a
// warning, with the diagnostic saying why (FS-004 FR-006, FR-008).
export function readinessTone(h: Harness): Tone {
  switch (h.readiness) {
    case "ready":
      return h.versionSupport === "untested_newer" || h.versionSupport === "unknown" || h.authentication === "unknown" || h.diagnostic
        ? "warning"
        : "success";
    case "not_installed":
      return "neutral";
    case "not_ready":
      return "danger";
    default:
      return "warning";
  }
}

export function HarnessRow({ harness }: { harness: Harness }) {
  const { t } = useTranslation();
  return (
    <div className="harness-row">
      <span className={`harness-row__mark harness--${harness.id}`}>
        <HarnessMark harness={harness.id} size={20} />
      </span>
      <span className="harness-row__text">
        <strong>{harness.name || t(`harness.names.${harness.id}`)}</strong>
        <span className="mono muted">{harness.detected ? `${harness.version ?? "?"} · ${harness.path ?? ""}` : t("harness.notDetected")}</span>
      </span>
      <Chip
        tone={readinessTone(harness)}
        icon={harness.readiness === "ready" ? "check" : harness.readiness === "not_installed" ? "circle" : "alert"}
        title={fieldText(harness.diagnostic, harness.diagnosticKey, harness.diagnosticParams)}
      >
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

export function HarnessInventory({ harness, locale }: { harness: Harness; locale: string }) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);
  const [path, setPath] = useState(harness.path ?? "");
  const [busy, setBusy] = useState(false);

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

  return (
    <div className="inventory">
      <HarnessRow harness={harness} />
      <KeyValue
        items={[
          [t("harness.detected"), harness.detected ? t("common.yes") : t("common.no")],
          [
            t("harness.path"),
            harness.path ? (
              <span className="mono">
                {harness.path}
                {harness.pathSource ? <span className="muted"> · {t(`harness.pathSource.${harness.pathSource}`)}</span> : null}
              </span>
            ) : null,
          ],
          [t("harness.installations"), harness.installations.length ? <span className="mono">{harness.installations.join("\n")}</span> : null],
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
            <span>
              {formatRelative(harness.lastCheckedAt, locale)}
              {harness.capabilitySource ? <span className="muted"> · {t(`harness.capabilitySource.${harness.capabilitySource}`)}</span> : null}
            </span>,
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
          <Button size="sm" icon="pencil" onClick={() => setEditing(true)}>
            {t("harness.setPath")}
          </Button>
          {harness.pathSource === "manual" ? (
            <Button size="sm" variant="ghost" icon="refresh" disabled={busy} onClick={() => void save(null)}>
              {t("harness.autoDetect")}
            </Button>
          ) : null}
        </div>
      )}
    </div>
  );
}
