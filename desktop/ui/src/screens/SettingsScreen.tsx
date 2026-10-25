// Settings: language, the daemon and its autostart, notifications, storage, the installed agents, path variables,
// local API security, and directories.
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import { native } from "../api/native";
import type { CleanResult, Settings, SettingsPatch } from "../api/types";
import { HarnessInventory, ToolRow } from "../components/Harnesses";
import { toast } from "../components/toast";
import { Banner, Button, Chip, Field, IconButton, KeyValue, NumberInput, Panel, Select, TextInput, Toggle } from "../components/ui";
import { applyLanguage } from "../i18n";
import { formatBytes, formatDateTime, formatDuration } from "../lib/format";
import { useNow } from "../lib/hooks";
import { actions, useStore } from "../state/store";

export function SettingsScreen() {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const settings = useStore((s) => s.settings);
  const diagnostics = useStore((s) => s.diagnostics);
  const harnesses = useStore((s) => s.harnesses);
  const tools = useStore((s) => s.tools);
  const health = useStore((s) => s.health);
  const appVersion = useStore((s) => s.appVersion);
  const appCommit = useStore((s) => s.appCommit);
  const appBuiltAt = useStore((s) => s.appBuiltAt);
  const running = useStore((s) => s.stats?.running ?? 0);
  const connection = useStore((s) => s.connection);
  const now = useNow(1000);
  const [clean, setClean] = useState<CleanResult | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  useEffect(() => {
    void actions.refreshSettings();
    void actions.refreshHarnesses();
  }, []);

  const patch = async (p: SettingsPatch, quiet = false) => {
    try {
      const next = await api.updateSettings(p);
      await actions.refreshSettings();
      if (!quiet) toast(t("settings.saved"));
      return next;
    } catch (err) {
      toast((err as Error).message, "danger");
      return undefined;
    }
  };

  const setLanguage = async (language: Settings["language"]) => {
    applyLanguage(language);
    await patch({ language }, true);
  };

  const setAutostart = async (enabled: boolean) => {
    const next = await patch({ autostart: enabled });
    // The tray process starts at login with the daemon, so notifications work with no window open.
    if (next) await native.setLoginItem(enabled && next.trayEnabled).catch(() => undefined);
  };

  const setTray = async (enabled: boolean) => {
    const next = await patch({ trayEnabled: enabled });
    if (next) {
      await native.setTrayEnabled(enabled);
      if (next.autostart.enabled) await native.setLoginItem(enabled).catch(() => undefined);
    }
  };

  const stopDaemon = async () => {
    const message = running > 0 ? t("settings.stopConfirmRunning", { count: running }) : t("settings.stopConfirm");
    if (!(await native.confirm(message, t("settings.stopConfirmTitle"), t("settings.stopDaemon"), t("common.cancel")))) return;
    try {
      await api.stopDaemon(running > 0);
      toast(t("settings.stopped"), "warning");
    } catch (err) {
      toast((err as Error).message, "danger");
    }
  };

  const startDaemon = async () => {
    setBusy("start");
    try {
      await native.startDaemon();
      await actions.refreshAll();
    } catch (err) {
      toast((err as Error).message, "danger");
    } finally {
      setBusy(null);
    }
  };

  const rotate = async () => {
    if (!(await native.confirm(t("settings.rotateConfirm"), t("settings.rotateConfirmTitle"), t("common.confirm"), t("common.cancel")))) return;
    try {
      await api.rotateToken();
      toast(t("settings.rotated"));
    } catch (err) {
      toast((err as Error).message, "danger");
    }
  };

  const cleanWorkspaces = async () => {
    setBusy("clean");
    try {
      setClean(await api.cleanWorkspaces());
    } catch (err) {
      toast((err as Error).message, "danger");
    } finally {
      setBusy(null);
    }
  };

  const daemonUp = connection === "connected";

  return (
    <div className="screen settings">
      <div className="settings-grid">
        <Panel title={t("settings.sections.general")} icon="globe">
          <Field label={t("settings.language")} hint={t("settings.languageHint")}>
            <Select value={settings?.language ?? "system"} onChange={(e) => void setLanguage(e.target.value as Settings["language"])}>
              {(["system", "pt-BR", "en-US", "es-ES"] as const).map((l) => (
                <option key={l} value={l}>
                  {t(`settings.languages.${l}`)}
                </option>
              ))}
            </Select>
          </Field>
          <Toggle checked={settings?.trayEnabled ?? true} onChange={(v) => void setTray(v)} label={t("settings.tray")} />
          <p className="field__hint">{t("settings.trayHint")}</p>
        </Panel>

        <Panel title={t("settings.sections.daemon")} icon="power">
          <div className="state-pair">
            <div className="state-pair__item">
              <span className="state-pair__label">{t("settings.runningNow")}</span>
              <Chip tone={daemonUp ? "success" : "danger"} icon={daemonUp ? "check" : "x"}>
                {daemonUp ? t("common.yes") : t("overview.notRunning")}
              </Chip>
              {health ? (
                <span className="mono muted">
                  {t("overview.pid", { pid: health.pid })} · {t("overview.uptime", { time: formatDuration(now - Date.parse(health.startedAt)) })}
                </span>
              ) : null}
            </div>
          </div>
          <Toggle checked={settings?.autostart.enabled ?? false} onChange={(v) => void setAutostart(v)} label={t("settings.autostart")} disabled={!settings} />
          <p className="field__hint">{t("settings.autostartHint")}</p>
          <div className="inline-actions">
            {daemonUp ? (
              <Button variant="danger-outline" icon="power" onClick={() => void stopDaemon()}>
                {t("settings.stopDaemon")}
              </Button>
            ) : native.available ? (
              <Button variant="primary" icon="power" disabled={busy === "start"} onClick={() => void startDaemon()}>
                {busy === "start" ? t("settings.starting") : t("settings.startDaemon")}
              </Button>
            ) : null}
          </div>
        </Panel>

        <Panel title={t("settings.sections.notifications")} icon="bell">
          <Field label={t("settings.notificationLevel")} hint={t("settings.notificationLevelHint")}>
            <Select value={settings?.notificationLevel ?? "all"} onChange={(e) => void patch({ notificationLevel: e.target.value as Settings["notificationLevel"] })}>
              {(["all", "failures_and_attention", "none"] as const).map((l) => (
                <option key={l} value={l}>
                  {t(`settings.notificationLevels.${l}`)}
                </option>
              ))}
            </Select>
          </Field>
          {native.available ? (
            <Button size="sm" icon="bell" onClick={() => void native.ensureNotificationPermission()}>
              {t("settings.notificationPermission")}
            </Button>
          ) : null}
        </Panel>

        <Panel title={t("settings.sections.storage")} icon="folder">
          <RetentionField value={settings?.logRetentionDays} onSave={(days) => void patch({ logRetentionDays: days })} />
          <p className="field__hint">{t("settings.logRetentionHint")}</p>
          <div className="inline-actions">
            <Button icon="trash" disabled={busy === "clean"} onClick={() => void cleanWorkspaces()}>
              {t("settings.cleanWorkspaces")}
            </Button>
            {diagnostics?.diskUsageBytes !== undefined ? (
              <span className="muted">
                {t("settings.diskUsage")}: <strong>{formatBytes(diagnostics.diskUsageBytes, locale)}</strong>
              </span>
            ) : null}
          </div>
          <p className="field__hint">{t("settings.cleanWorkspacesHint")}</p>
          {clean ? (
            <Banner tone="success" icon="check">
              {t("settings.cleanResult", { removed: clean.removed.length, kept: clean.kept.length })}
              <ul className="clean-list mono">
                {clean.removed.map((p) => (
                  <li key={p}>− {p}</li>
                ))}
                {clean.kept.map((k) => (
                  <li key={k.path} className="muted">
                    = {k.path} — {t("settings.keptReason", { reason: k.reason })}
                  </li>
                ))}
              </ul>
            </Banner>
          ) : null}
        </Panel>

        <Panel
          title={t("settings.sections.harnesses")}
          icon="agent"
          className="settings-grid__wide"
          actions={
            <Button size="sm" icon="refresh" onClick={() => void actions.refreshHarnesses(true)}>
              {t("settings.refreshHarnesses")}
            </Button>
          }
        >
          <p className="field__hint">{t("harness.noCredentials")}</p>
          <div className="inventory-grid">
            {harnesses.map((h) => (
              <HarnessInventory key={h.id} harness={h} locale={locale} />
            ))}
          </div>
          <h3 className="subhead">{t("settings.sections.tools")}</h3>
          {tools.map((tool) => (
            <ToolRow key={tool.id} tool={tool} />
          ))}
        </Panel>

        <Panel title={t("settings.sections.variables")} icon="key">
          <PathVariables value={settings?.pathVariables ?? {}} onSave={(v) => void patch({ pathVariables: v })} />
          <h3 className="subhead">{t("settings.allowedFolders")}</h3>
          <AllowedFolders value={settings?.allowedFolders ?? []} onSave={(v) => void patch({ allowedFolders: v })} />
        </Panel>

        <Panel title={t("settings.sections.security")} icon="shield">
          <KeyValue
            items={[
              [t("settings.address"), diagnostics ? <span className="mono">{diagnostics.address}</span> : null],
              [t("settings.tokenStore"), diagnostics ? t(`settings.tokenStores.${diagnostics.tokenStore}`) : null],
            ]}
          />
          <Button icon="key" onClick={() => void rotate()}>
            {t("settings.rotateToken")}
          </Button>
        </Panel>

        <Panel title={t("settings.sections.directories")} icon="folder" className="settings-grid__wide">
          {diagnostics ? (
            <KeyValue
              items={(["homeDir", "tasksDir", "stateDir", "logsDir", "workspaceDir", "runsDir", "runtimeFile", "builtinsDir"] as const)
                .filter((k) => diagnostics[k])
                .map((k) => [
                  t(`settings.dirs.${k}`),
                  <span className="dir-row">
                    <span className="mono">{diagnostics[k]}</span>
                    {native.available ? <IconButton icon="external" label={t("common.openInFinder")} onClick={() => void native.openPath(diagnostics[k]!)} /> : null}
                  </span>,
                ])}
            />
          ) : null}
        </Panel>

        <Panel title={t("settings.sections.about")} icon="sparkle">
          <KeyValue
            items={[
              [t("settings.appVersion"), <span className="mono">{build(locale, appVersion, appCommit, appBuiltAt)}</span>],
              [
                t("settings.daemonVersion"),
                health ? <span className="mono">{build(locale, health.version, health.commit, health.builtAt)}</span> : null,
              ],
            ]}
          />
        </Panel>
      </div>
    </div>
  );
}

/**
 * A version with the commit it was built from and when, such as `0.1.0 (45a57bb) · 22 Oct 2026, 17:30`; the commit
 * and the time are each left out when unknown.
 */
function build(locale: string, version: string, commit?: string, builtAt?: string): string {
  const parts = [commit ? `${version} (${commit})` : version];
  if (builtAt && !Number.isNaN(Date.parse(builtAt))) parts.push(formatDateTime(builtAt, locale));
  return parts.join(" · ");
}

function RetentionField({ value, onSave }: { value?: number; onSave: (days: number) => void }) {
  const { t } = useTranslation();
  const [days, setDays] = useState<number | undefined>(value);
  useEffect(() => setDays(value), [value]);
  return (
    <Field label={t("settings.logRetentionDays")}>
      <div className="inline-form">
        <NumberInput value={days} onValue={setDays} min={1} max={3650} />
        <Button size="sm" disabled={!days || days === value} onClick={() => days && onSave(days)}>
          {t("common.apply")}
        </Button>
      </div>
    </Field>
  );
}

function PathVariables({ value, onSave }: { value: Record<string, string>; onSave: (v: Record<string, string>) => void }) {
  const { t } = useTranslation();
  const [rows, setRows] = useState<Array<[string, string]>>(Object.entries(value));
  useEffect(() => setRows(Object.entries(value)), [JSON.stringify(value)]);
  const changed = JSON.stringify(Object.fromEntries(rows.filter(([k]) => k.trim()))) !== JSON.stringify(value);
  return (
    <div className="row-list">
      <p className="field__hint">{t("settings.variablesHint")}</p>
      {rows.map(([k, v], i) => (
        <div className="grid-var" key={i}>
          <TextInput
            className="mono"
            placeholder={t("settings.variableName")}
            value={k}
            onChange={(e) => setRows(rows.map((r, j) => (j === i ? [e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_"), r[1]] : r)))}
          />
          <TextInput className="mono" placeholder={t("settings.variableValue")} value={v} onChange={(e) => setRows(rows.map((r, j) => (j === i ? [r[0], e.target.value] : r)))} />
          <IconButton icon="trash" label={t("common.remove")} onClick={() => setRows(rows.filter((_, j) => j !== i))} />
        </div>
      ))}
      <div className="inline-actions">
        <Button size="sm" icon="plus" onClick={() => setRows([...rows, ["", ""]])}>
          {t("settings.addVariable")}
        </Button>
        <Button size="sm" variant="primary" disabled={!changed} onClick={() => onSave(Object.fromEntries(rows.filter(([k]) => k.trim())))}>
          {t("common.save")}
        </Button>
      </div>
    </div>
  );
}

// The folders, beside the home folder and the mounted volumes, where a task's paths may lead.
// The daemon refuses one where no harness may write, and a change that would leave a saved task outside them.
function AllowedFolders({ value, onSave }: { value: string[]; onSave: (v: string[]) => void }) {
  const { t } = useTranslation();
  const [rows, setRows] = useState<string[]>(value);
  useEffect(() => setRows(value), [JSON.stringify(value)]);
  const kept = rows.map((r) => r.trim()).filter(Boolean);
  const changed = JSON.stringify(kept) !== JSON.stringify(value);
  return (
    <div className="row-list">
      <p className="field__hint">{t("settings.allowedFoldersHint")}</p>
      {rows.map((f, i) => (
        <div className="grid-folder" key={i}>
          <TextInput
            className="mono"
            aria-label={t("settings.allowedFolder")}
            placeholder={t("settings.allowedFolderPlaceholder")}
            value={f}
            onChange={(e) => setRows(rows.map((r, j) => (j === i ? e.target.value : r)))}
          />
          <IconButton icon="trash" label={t("common.remove")} onClick={() => setRows(rows.filter((_, j) => j !== i))} />
        </div>
      ))}
      <div className="inline-actions">
        <Button size="sm" icon="plus" onClick={() => setRows([...rows, ""])}>
          {t("settings.addAllowedFolder")}
        </Button>
        <Button size="sm" variant="primary" disabled={!changed} onClick={() => onSave(kept)}>
          {t("common.save")}
        </Button>
      </div>
    </div>
  );
}
