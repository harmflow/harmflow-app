// Settings, divided into sections: a list of them on the left, and the open one beside it. Interface: the language, the
// menu bar, and notifications. Agents and tasks: the installed agents and tools, and the paths a task may use. This
// computer: the daemon, storage, and local API security. About stands alone at the foot of the list.
import { useCallback, useEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import { native } from "../api/native";
import type { CleanResult, Settings, SettingsPatch } from "../api/types";
import { HarnessInventory, ToolRow } from "../components/Harnesses";
import { Icon, type IconName } from "../components/Icon";
import { tipBeside, useTooltip } from "../components/Shell";
import type { LeaveCheck } from "../components/TaskEditor";
import { toast } from "../components/toast";
import { Banner, Button, Chip, IconButton, KeyValue, NumberInput, Panel, Select, TextInput, Toggle, cx } from "../components/ui";
import { applyLanguage } from "../i18n";
import { formatBytes, formatDateTime, formatDuration } from "../lib/format";
import { useDraft, useNow, type DraftReport } from "../lib/hooks";
import { actions, useStore } from "../state/store";

export type SettingsSection = "general" | "notifications" | "agents" | "paths" | "daemon" | "storage" | "security" | "about";

/** The section list's groups, in order; About follows them alone, under a divider. */
export const SECTION_GROUPS: ReadonlyArray<{ key: "interface" | "agents" | "computer"; sections: SettingsSection[] }> = [
  { key: "interface", sections: ["general", "notifications"] },
  { key: "agents", sections: ["agents", "paths"] },
  { key: "computer", sections: ["daemon", "storage", "security"] },
];

const SECTION_ICONS: Record<SettingsSection, IconName> = {
  general: "globe",
  notifications: "bell",
  agents: "agent",
  paths: "folder",
  daemon: "power",
  storage: "layers",
  security: "shield",
  about: "sparkle",
};

export function isSettingsSection(value: unknown): value is SettingsSection {
  return typeof value === "string" && Object.hasOwn(SECTION_ICONS, value);
}

/** Below this width of the screen's content box, inside its padding, the section list keeps only its icons. */
export const ICONS_ONLY_BELOW = 640;

/** Whether the element's content box is narrower than `width`, as it resizes. */
function useNarrowerThan(ref: RefObject<HTMLElement | null>, width: number): boolean {
  const [narrower, setNarrower] = useState(false);
  useEffect(() => {
    const element = ref.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => setNarrower(entry.contentRect.width < width));
    observer.observe(element);
    return () => observer.disconnect();
  }, [ref, width]);
  return narrower;
}

export function SettingsScreen({
  section = "general",
  onSection,
  onLeaveCheck,
}: {
  section?: SettingsSection;
  /** Opens another section; the shell asks first while a draft is held. */
  onSection?: (s: SettingsSection) => void;
  /** Receives the question to ask before leaving while a section holds a draft, and null once it holds none. */
  onLeaveCheck?: (check: LeaveCheck | null) => void;
}) {
  const { t } = useTranslation();
  const screen = useRef<HTMLDivElement>(null);
  const iconsOnly = useNarrowerThan(screen, ICONS_ONLY_BELOW);
  const { tipped, tooltip, hide } = useTooltip(tipBeside);

  // The list moves under a shown tooltip; the next hover or focus places it again.
  useEffect(hide, [iconsOnly, section, hide]);

  useEffect(() => {
    void actions.refreshSettings();
    void actions.refreshHarnesses();
  }, []);

  // Three settings keep a draft until confirmed: the path variables and the allowed folders (Save), the log retention
  // (Apply), and a harness's path set by hand (its Save). While one is held, every way out of the section asks first,
  // for another section or another screen, as leaving a task's editor with unsaved changes does.
  const [drafts, setDrafts] = useState<ReadonlySet<string>>(() => new Set());
  const report = useCallback<DraftReport>(
    (key, held) =>
      setDrafts((current) => {
        if (current.has(key) === held) return current;
        const next = new Set(current);
        if (held) next.add(key);
        else next.delete(key);
        return next;
      }),
    [],
  );
  const holding = drafts.size > 0;
  useEffect(() => {
    if (!holding || !onLeaveCheck) return;
    onLeaveCheck(() => native.confirm(t("settings.leaveConfirm"), t("settings.leaveTitle"), t("settings.discard"), t("common.cancel")));
    return () => onLeaveCheck(null);
  }, [holding, onLeaveCheck, t]);

  const item = (s: SettingsSection) => {
    const name = t(`settings.sections.${s}`);
    const open = s === section;
    return (
      <button
        key={s}
        type="button"
        className={cx("nav__item", "settings-nav__item", open && "nav__item--active")}
        aria-current={open ? "page" : undefined}
        onClick={() => onSection?.(s)}
        {...(iconsOnly ? tipped(name) : {})}
      >
        <Icon name={SECTION_ICONS[s]} size={17} />
        <span className={cx("nav__label", iconsOnly && "sr-only")}>{name}</span>
      </button>
    );
  };

  return (
    <div ref={screen} className={cx("screen", "settings", iconsOnly && "settings--icons")}>
      <nav className="settings-nav" aria-label={t("settings.sectionList")}>
        {SECTION_GROUPS.map((group) => (
          <div key={group.key} className="settings-nav__group" role="group" aria-labelledby={`settings-group-${group.key}`}>
            <span id={`settings-group-${group.key}`} className={cx("settings-nav__heading", iconsOnly && "sr-only")}>
              {t(`settings.groups.${group.key}`)}
            </span>
            {group.sections.map(item)}
          </div>
        ))}
        <hr className="settings-nav__divider" />
        {item("about")}
      </nav>
      <section className="settings__section" aria-labelledby="settings-section-title">
        <h2 id="settings-section-title" className="settings__title">
          {t(`settings.sections.${section}`)}
        </h2>
        {section === "general" ? <GeneralSection /> : null}
        {section === "notifications" ? <NotificationsSection /> : null}
        {section === "agents" ? <AgentsSection report={report} /> : null}
        {section === "paths" ? <PathsSection report={report} /> : null}
        {section === "daemon" ? <DaemonSection /> : null}
        {section === "storage" ? <StorageSection report={report} /> : null}
        {section === "security" ? <SecuritySection /> : null}
        {section === "about" ? <AboutSection /> : null}
      </section>
      {tooltip}
    </div>
  );
}

/**
 * One setting in a panel: its label and hint on the left, its control on the right, or under the label where the row is
 * too narrow for both. With `id`, the label names the control of that id.
 */
function SettingRow({ id, label, hint, children }: { id?: string; label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className="setting-row">
      <div className="setting-row__text">
        {id ? (
          <label className="setting-row__label" htmlFor={id}>
            {label}
          </label>
        ) : (
          <span className="setting-row__label">{label}</span>
        )}
        {hint ? <span className="field__hint">{hint}</span> : null}
      </div>
      <div className="setting-row__control">{children}</div>
    </div>
  );
}

/** Saves a change to the settings and reads them again; a toast says so, unless quiet. Undefined when it failed. */
function usePatch() {
  const { t } = useTranslation();
  return async (p: SettingsPatch, quiet = false) => {
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
}

function GeneralSection() {
  const { t } = useTranslation();
  const settings = useStore((s) => s.settings);
  const patch = usePatch();

  const setLanguage = async (language: Settings["language"]) => {
    applyLanguage(language);
    await patch({ language }, true);
  };

  const setTray = async (enabled: boolean) => {
    const next = await patch({ trayEnabled: enabled });
    if (next) {
      await native.setTrayEnabled(enabled);
      if (next.autostart.enabled) await native.setLoginItem(enabled).catch(() => undefined);
    }
  };

  const setKeepTrayOnQuit = async (keep: boolean) => {
    if (await patch({ keepTrayOnQuit: keep })) await native.setKeepTrayOnQuit(keep);
  };

  return (
    <>
      <Panel title={t("settings.subheads.language")}>
        <SettingRow id="settings-language" label={t("settings.language")} hint={t("settings.languageHint")}>
          <Select
            id="settings-language"
            value={settings?.language ?? "system"}
            onChange={(e) => void setLanguage(e.target.value as Settings["language"])}
          >
            {(["system", "pt-BR", "en-US", "es-ES"] as const).map((l) => (
              <option key={l} value={l}>
                {t(`settings.languages.${l}`)}
              </option>
            ))}
          </Select>
        </SettingRow>
      </Panel>
      <Panel title={t("settings.subheads.menuBar")}>
        <SettingRow id="settings-tray" label={t("settings.tray")} hint={t("settings.trayHint")}>
          <Toggle id="settings-tray" checked={settings?.trayEnabled ?? true} onChange={(v) => void setTray(v)} />
        </SettingRow>
        {/* It depends on the tray: while the tray is off, ⌘Q quits. */}
        <SettingRow id="settings-keep-tray" label={t("settings.keepTrayOnQuit")} hint={t("settings.keepTrayOnQuitHint")}>
          <Toggle
            id="settings-keep-tray"
            checked={settings?.keepTrayOnQuit ?? false}
            onChange={(v) => void setKeepTrayOnQuit(v)}
            disabled={!settings?.trayEnabled}
          />
        </SettingRow>
      </Panel>
    </>
  );
}

function NotificationsSection() {
  const { t } = useTranslation();
  const settings = useStore((s) => s.settings);
  const patch = usePatch();
  return (
    <Panel title={t("settings.subheads.notices")}>
      <SettingRow id="settings-notification-level" label={t("settings.notificationLevel")} hint={t("settings.notificationLevelHint")}>
        <Select
          id="settings-notification-level"
          value={settings?.notificationLevel ?? "all"}
          onChange={(e) => void patch({ notificationLevel: e.target.value as Settings["notificationLevel"] })}
        >
          {(["all", "failures_and_attention", "none"] as const).map((l) => (
            <option key={l} value={l}>
              {t(`settings.notificationLevels.${l}`)}
            </option>
          ))}
        </Select>
      </SettingRow>
      {native.available ? (
        <SettingRow label={t("settings.notificationPermissionLabel")} hint={t("settings.notificationPermissionHint")}>
          <Button size="sm" icon="bell" onClick={() => void native.ensureNotificationPermission()}>
            {t("settings.notificationPermission")}
          </Button>
        </SettingRow>
      ) : null}
    </Panel>
  );
}

function AgentsSection({ report }: { report: DraftReport }) {
  const { t, i18n } = useTranslation();
  const harnesses = useStore((s) => s.harnesses);
  const tools = useStore((s) => s.tools);
  return (
    <>
      <Panel
        title={t("settings.subheads.agents")}
        actions={
          <Button size="sm" icon="refresh" onClick={() => void actions.refreshHarnesses(true)}>
            {t("settings.refreshHarnesses")}
          </Button>
        }
      >
        <p className="field__hint">{t("harness.noCredentials")}</p>
        <div className="inventory-grid">
          {harnesses.map((h) => (
            <HarnessInventory key={h.id} harness={h} locale={i18n.language} onDraft={report} />
          ))}
        </div>
      </Panel>
      <Panel title={t("settings.subheads.tools")}>
        {tools.map((tool) => (
          <ToolRow key={tool.id} tool={tool} />
        ))}
      </Panel>
    </>
  );
}

function PathsSection({ report }: { report: DraftReport }) {
  const { t } = useTranslation();
  const settings = useStore((s) => s.settings);
  const patch = usePatch();
  return (
    <>
      <Panel title={t("settings.subheads.variables")}>
        <PathVariables value={settings?.pathVariables ?? {}} onSave={(v) => void patch({ pathVariables: v })} onDraft={report} />
      </Panel>
      <Panel title={t("settings.subheads.allowedFolders")}>
        <AllowedFolders value={settings?.allowedFolders ?? []} onSave={(v) => void patch({ allowedFolders: v })} onDraft={report} />
      </Panel>
    </>
  );
}

function DaemonSection() {
  const { t } = useTranslation();
  const settings = useStore((s) => s.settings);
  const health = useStore((s) => s.health);
  const running = useStore((s) => s.stats?.running ?? 0);
  const connection = useStore((s) => s.connection);
  const now = useNow(1000);
  const patch = usePatch();
  const [starting, setStarting] = useState(false);
  const daemonUp = connection === "connected";

  const setAutostart = async (enabled: boolean) => {
    const next = await patch({ autostart: enabled });
    // The tray process starts at login with the daemon, so notifications work with no window open.
    if (next) await native.setLoginItem(enabled && next.trayEnabled).catch(() => undefined);
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
    setStarting(true);
    try {
      await native.startDaemon();
      await actions.refreshAll();
    } catch (err) {
      toast((err as Error).message, "danger");
    } finally {
      setStarting(false);
    }
  };

  return (
    <>
      <Panel title={t("settings.subheads.status")}>
        <SettingRow
          label={t("settings.runningNow")}
          hint={
            health ? (
              <span className="mono">
                {t("overview.pid", { pid: health.pid })} · {t("overview.uptime", { time: formatDuration(now - Date.parse(health.startedAt)) })}
              </span>
            ) : undefined
          }
        >
          <Chip tone={daemonUp ? "success" : "danger"} icon={daemonUp ? "check" : "x"}>
            {daemonUp ? t("common.yes") : t("overview.notRunning")}
          </Chip>
          {daemonUp ? (
            <Button variant="danger-outline" icon="power" onClick={() => void stopDaemon()}>
              {t("settings.stopDaemon")}
            </Button>
          ) : native.available ? (
            <Button variant="primary" icon="power" disabled={starting} onClick={() => void startDaemon()}>
              {starting ? t("settings.starting") : t("settings.startDaemon")}
            </Button>
          ) : null}
        </SettingRow>
      </Panel>
      <Panel title={t("settings.subheads.startup")}>
        <SettingRow id="settings-autostart" label={t("settings.autostart")} hint={t("settings.autostartHint")}>
          <Toggle
            id="settings-autostart"
            checked={settings?.autostart.enabled ?? false}
            onChange={(v) => void setAutostart(v)}
            disabled={!settings}
          />
        </SettingRow>
      </Panel>
    </>
  );
}

function StorageSection({ report }: { report: DraftReport }) {
  const { t, i18n } = useTranslation();
  const settings = useStore((s) => s.settings);
  const diagnostics = useStore((s) => s.diagnostics);
  const patch = usePatch();
  const [clean, setClean] = useState<CleanResult | null>(null);
  const [cleaning, setCleaning] = useState(false);

  const cleanWorkspaces = async () => {
    setCleaning(true);
    try {
      setClean(await api.cleanWorkspaces());
    } catch (err) {
      toast((err as Error).message, "danger");
    } finally {
      setCleaning(false);
    }
  };

  return (
    <>
      <Panel title={t("settings.subheads.logs")}>
        <SettingRow id="settings-retention" label={t("settings.logRetentionDays")} hint={t("settings.logRetentionHint")}>
          <RetentionField
            id="settings-retention"
            value={settings?.logRetentionDays}
            onSave={(days) => void patch({ logRetentionDays: days })}
            onDraft={report}
          />
        </SettingRow>
      </Panel>
      <Panel title={t("settings.subheads.workspaces")}>
        <SettingRow
          label={
            diagnostics?.diskUsageBytes !== undefined ? (
              <>
                {t("settings.diskUsage")}: <strong>{formatBytes(diagnostics.diskUsageBytes, i18n.language)}</strong>
              </>
            ) : (
              t("settings.diskUsage")
            )
          }
          hint={t("settings.cleanWorkspacesHint")}
        >
          <Button icon="trash" disabled={cleaning} onClick={() => void cleanWorkspaces()}>
            {t("settings.cleanWorkspaces")}
          </Button>
        </SettingRow>
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
      <Panel title={t("settings.subheads.directories")}>
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
    </>
  );
}

function SecuritySection() {
  const { t } = useTranslation();
  const diagnostics = useStore((s) => s.diagnostics);

  const rotate = async () => {
    if (!(await native.confirm(t("settings.rotateConfirm"), t("settings.rotateConfirmTitle"), t("common.confirm"), t("common.cancel")))) return;
    try {
      await api.rotateToken();
      toast(t("settings.rotated"));
    } catch (err) {
      toast((err as Error).message, "danger");
    }
  };

  return (
    <Panel title={t("settings.subheads.localApi")}>
      <SettingRow label={t("settings.address")}>{diagnostics ? <span className="mono">{diagnostics.address}</span> : "—"}</SettingRow>
      <SettingRow label={t("settings.tokenStore")}>{diagnostics ? t(`settings.tokenStores.${diagnostics.tokenStore}`) : "—"}</SettingRow>
      <SettingRow label={t("settings.apiToken")} hint={t("settings.apiTokenHint")}>
        <Button icon="key" onClick={() => void rotate()}>
          {t("settings.rotateToken")}
        </Button>
      </SettingRow>
    </Panel>
  );
}

function AboutSection() {
  const { t, i18n } = useTranslation();
  const health = useStore((s) => s.health);
  const appVersion = useStore((s) => s.appVersion);
  const appCommit = useStore((s) => s.appCommit);
  const appBuiltAt = useStore((s) => s.appBuiltAt);
  const locale = i18n.language;
  return (
    <Panel title={t("settings.subheads.versions")}>
      <SettingRow label={t("settings.appVersion")}>
        <span className="mono">{build(locale, appVersion, appCommit, appBuiltAt)}</span>
      </SettingRow>
      <SettingRow label={t("settings.daemonVersion")}>
        {health ? <span className="mono">{build(locale, health.version, health.commit, health.builtAt)}</span> : "—"}
      </SettingRow>
    </Panel>
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

function RetentionField({ id, value, onSave, onDraft }: { id: string; value?: number; onSave: (days: number) => void; onDraft: DraftReport }) {
  const { t } = useTranslation();
  const [days, setDays] = useState<number | undefined>(value);
  useEffect(() => setDays(value), [value]);
  useDraft(onDraft, "retention", days !== value);
  return (
    <div className="inline-form">
      <NumberInput id={id} value={days} onValue={setDays} min={1} max={3650} />
      <Button size="sm" disabled={!days || days === value} onClick={() => days && onSave(days)}>
        {t("common.apply")}
      </Button>
    </div>
  );
}

function PathVariables({ value, onSave, onDraft }: { value: Record<string, string>; onSave: (v: Record<string, string>) => void; onDraft: DraftReport }) {
  const { t } = useTranslation();
  const [rows, setRows] = useState<Array<[string, string]>>(Object.entries(value));
  useEffect(() => setRows(Object.entries(value)), [JSON.stringify(value)]);
  const changed = JSON.stringify(Object.fromEntries(rows.filter(([k]) => k.trim()))) !== JSON.stringify(value);
  useDraft(onDraft, "pathVariables", changed);
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
function AllowedFolders({ value, onSave, onDraft }: { value: string[]; onSave: (v: string[]) => void; onDraft: DraftReport }) {
  const { t } = useTranslation();
  const [rows, setRows] = useState<string[]>(value);
  useEffect(() => setRows(value), [JSON.stringify(value)]);
  const kept = rows.map((r) => r.trim()).filter(Boolean);
  const changed = JSON.stringify(kept) !== JSON.stringify(value);
  useDraft(onDraft, "allowedFolders", changed);
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
