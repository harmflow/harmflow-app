import { useCallback, useEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { native } from "../api/native";
import type { Notification } from "../api/types";
import { askAndStopDaemon } from "../lib/daemonStop";
import { translateNested } from "../lib/daemonText";
import { errorMessage } from "../lib/errorMessage";
import { formatBytes, formatNumber, formatRelative, formatTime } from "../lib/format";
import { useNow } from "../lib/hooks";
import { ariaKeys, shortcutText, type ShortcutId } from "../lib/shortcuts";
import { dynamicText, useRegistered } from "../registrations";
import { actions, every, useStore } from "../state/store";
import { HarnessMark, Icon, Logo, type IconName } from "./Icon";
import { toast } from "./toast";
import { Banner, Button, Gauge, IconButton, cx } from "./ui";

export type Screen = "overview" | "executions" | "tasks" | "history" | "settings";

/** A built-in screen, or the route of a screen a registration added. */
export type ScreenRoute = Screen | (string & {});

/** The sidebar's built-in items, in the order it shows them, each with the shortcut that opens it: ⌘1 the first, and so on. */
export const NAV: Array<{ key: Screen; icon: IconName; shortcut: ShortcutId }> = [
  { key: "overview", icon: "grid", shortcut: "openOverview" },
  { key: "executions", icon: "playFilled", shortcut: "openExecutions" },
  { key: "tasks", icon: "list", shortcut: "openTasks" },
  { key: "history", icon: "history", shortcut: "openHistory" },
  { key: "settings", icon: "settings", shortcut: "openSettings" },
];

/** How often the expanded sidebar reads its gauges again. */
const GAUGES_EVERY_MS = 5000;

interface Tip {
  text: string;
  keys?: string;
  top: number;
  left: number;
  below?: boolean;
}

type Place = (target: HTMLElement, text: string, keys?: string) => Tip;

/** A tooltip just past the edge of the sidebar or of Settings' section list, level with its target. */
export const tipBeside: Place = (target, text, keys) => {
  const box = target.getBoundingClientRect();
  const edge = target.closest(".sidebar, .settings-nav")?.getBoundingClientRect().right ?? box.right;
  return { text, keys, top: box.top + box.height / 2, left: edge + 8 };
};

/** A tooltip just below its target, centred on it: the topbar's, and the task editor's Save. */
export const tipBelow: Place = (target, text, keys) => {
  const box = target.getBoundingClientRect();
  return { text, keys, top: box.bottom + 8, left: box.left + box.width / 2, below: true };
};

/**
 * A tooltip placed by `place`, shown at once on hover and on keyboard focus: `title` appears late and never on focus.
 * `tipped` gives a target its handlers; `tooltip` is the element to render; `hide` clears it.
 */
export function useTooltip(place: Place) {
  const [tip, setTip] = useState<Tip | null>(null);
  const hide = useCallback(() => setTip(null), []);
  const tipped = (text: string, keys?: string) => ({
    onMouseEnter: (e: { currentTarget: HTMLElement }) => setTip(place(e.currentTarget, text, keys)),
    onFocus: (e: { currentTarget: HTMLElement }) => setTip(place(e.currentTarget, text, keys)),
    onMouseLeave: hide,
    onBlur: hide,
  });
  const tooltip = tip
    ? createPortal(
        <span className={cx("tooltip", tip.below && "tooltip--below")} aria-hidden="true" style={{ top: tip.top, left: tip.left }}>
          {tip.text}
          {tip.keys ? <kbd className="tooltip__keys">{tip.keys}</kbd> : null}
        </span>,
        document.body,
      )
    : null;
  return { tipped, tooltip, hide };
}

/**
 * The sidebar, expanded or collapsed to its icons (⌘B). The daemon's connection state is the topbar's alone; the
 * gauges and the version show only while expanded, and the gauges are read from /system only then.
 */
export function Sidebar({
  screen,
  onNavigate,
  collapsed,
  onToggle,
}: {
  screen: ScreenRoute;
  onNavigate: (s: ScreenRoute) => void;
  collapsed: boolean;
  onToggle: () => void;
}) {
  const { t, i18n } = useTranslation();
  const registered = useRegistered();
  const system = useStore((s) => s.system);
  const version = useStore((s) => s.appVersion);
  const running = useStore((s) => s.stats?.running ?? 0);
  const locale = i18n.language;
  const { tipped, tooltip, hide } = useTooltip(tipBeside);

  // The layout moves under a shown tooltip; the next hover or focus places it again.
  useEffect(hide, [collapsed, hide]);

  // The gauges are read at once on expanding and then on their own clock; collapsed, nothing shows them.
  useEffect(() => {
    if (collapsed) return;
    void actions.refreshSystem();
    const timer = every("system", GAUGES_EVERY_MS, actions.refreshSystem);
    return () => clearInterval(timer);
  }, [collapsed]);

  const toggleLabel = collapsed ? t("sidebar.expand") : t("sidebar.collapse");
  // The registered screens follow the built-in ones, with no shortcut of their own.
  const items: Array<{ key: ScreenRoute; icon: IconName; shortcut?: ShortcutId }> = [
    ...NAV,
    ...registered.screens.map((s) => ({ key: s.route, icon: s.icon })),
  ];

  return (
    <aside className="sidebar">
      <div className="brand">
        <Logo size={38} />
        {collapsed ? null : (
          <div className="brand__text">
            <span className="brand__name">{t("app.name")}</span>
            <span className="brand__tagline">{t("app.tagline")}</span>
          </div>
        )}
      </div>

      <nav className="nav" aria-label="HarmFlow">
        {items.map((item) => {
          const label = dynamicText(t, `nav.${item.key}`);
          return (
            <button
              key={item.key}
              type="button"
              className={cx("nav__item", screen === item.key && "nav__item--active")}
              aria-current={screen === item.key ? "page" : undefined}
              aria-keyshortcuts={item.shortcut ? ariaKeys(item.shortcut) : undefined}
              onClick={() => onNavigate(item.key)}
              {...(collapsed ? tipped(label, item.shortcut ? shortcutText(item.shortcut) : undefined) : {})}
            >
              <Icon name={item.icon} size={19} />
              <span className={cx("nav__label", collapsed && "sr-only")}>{label}</span>
              {item.key === "executions" && running > 0 ? <span className="nav__badge">{running}</span> : null}
            </button>
          );
        })}
      </nav>

      <div className="sidebar__spacer" />

      <div className="sidebar__foot">
        {collapsed ? null : (
          <section className="gauges-panel" aria-labelledby="gauges-title">
            <h2 id="gauges-title" className="gauges-panel__title">
              {t("gauges.title")}
            </h2>
            <div className="gauges">
              <Gauge
                label={t("gauges.cpu")}
                value={system?.cpuPercent}
                max={100}
                text={system?.cpuPercent !== undefined ? `${formatNumber(system.cpuPercent, locale)}%` : "—"}
              />
              <Gauge
                label={t("gauges.ram")}
                value={system?.memoryUsedBytes}
                max={system?.memoryTotalBytes}
                text={
                  system?.memoryTotalBytes
                    ? `${formatBytes(system.memoryUsedBytes, locale)} / ${formatBytes(system.memoryTotalBytes, locale)}`
                    : "—"
                }
              />
              <Gauge
                label={t("gauges.disk")}
                value={system?.diskUsedBytes}
                max={system?.diskTotalBytes}
                text={
                  system?.diskTotalBytes
                    ? `${formatBytes(system.diskUsedBytes, locale)} / ${formatBytes(system.diskTotalBytes, locale)}`
                    : "—"
                }
              />
            </div>
          </section>
        )}
        <div className="sidebar__bar">
          {collapsed ? null : <span className="sidebar__version">{t("common.version", { version })}</span>}
          <button
            type="button"
            className="icon-btn sidebar__toggle"
            aria-label={toggleLabel}
            aria-keyshortcuts={ariaKeys("toggleSidebar")}
            onClick={onToggle}
            {...tipped(toggleLabel, shortcutText("toggleSidebar"))}
          >
            <Icon name={collapsed ? "sidebarExpand" : "sidebarCollapse"} size={18} />
          </button>
        </div>
      </div>

      {tooltip}
    </aside>
  );
}

/**
 * Whether the window and the daemon come from different commits. A "-dirty" suffix is no difference when the hashes
 * are otherwise equal, and a build without git information, whose commit is empty, differs from none.
 */
export function commitsDiffer(appCommit?: string, daemonCommit?: string): boolean {
  if (!appCommit || !daemonCommit) return false;
  const clean = (commit: string) => commit.replace(/-dirty$/, "");
  return clean(appCommit) !== clean(daemonCommit);
}

/**
 * The topbar. When the daemon's version differs from the window's, a warning names both, whatever the commits say.
 * When the window and the daemon come from different commits, a banner below it names both and offers to restart the
 * daemon on the window's build; only that click restarts it.
 */
export function Header({
  screen,
  crumb,
  onNewTask,
  onNavigate,
  onOpenExecution,
}: {
  screen: ScreenRoute;
  /** What the breadcrumb adds after the screen's own: Settings' open section. */
  crumb?: string;
  onNewTask: () => void;
  onNavigate: (s: ScreenRoute) => void;
  onOpenExecution: (id: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const connection = useStore((s) => s.connection);
  const lastUpdate = useStore((s) => s.lastUpdate);
  const health = useStore((s) => s.health);
  const appVersion = useStore((s) => s.appVersion);
  const appCommit = useStore((s) => s.appCommit);
  const restart = useStore((s) => s.daemonRestart);
  const daemonCommit = health?.commit ?? "";
  const commitMismatch = commitsDiffer(appCommit, daemonCommit);
  const mismatch = health && health.version !== appVersion;
  const { tipped, tooltip, hide } = useTooltip(tipBelow);

  const restartDaemon = async () => {
    try {
      await actions.restartDaemon();
    } catch (err) {
      toast(t("connection.restartFailed", { message: errorMessage(err) }), "danger");
    }
  };

  return (
    <header className="topbar" data-tauri-drag-region>
      <div className="topbar__titles">
        <span className="breadcrumb">
          <span className="breadcrumb__prompt">&gt;_</span> {dynamicText(t, `screens.${screen}.breadcrumb`)}
          {crumb ? ` / ${crumb}` : null}
        </span>
        <h1 className="topbar__title">{dynamicText(t, `screens.${screen}.title`)}</h1>
        <p className="topbar__subtitle">{dynamicText(t, `screens.${screen}.subtitle`)}</p>
      </div>
      <div className="topbar__status">
        <div className={cx("conn-pill", `conn-pill--${connection}`)} role="status">
          <span className="dot" aria-hidden="true" />
          <span>{t(`connection.${connection}`)}</span>
          <Wave active={connection === "connected"} />
        </div>
        <span className="topbar__updated">
          {t("connection.lastUpdate", { time: lastUpdate ? formatTime(new Date(lastUpdate).toISOString(), i18n.language) : "—" })}
        </span>
      </div>
      <div className="topbar__actions">
        <Button
          variant="primary"
          size="lg"
          icon="plus"
          aria-keyshortcuts={ariaKeys("newTask")}
          onClick={() => {
            hide(); // the picker covers the button, which may get no mouseleave until the pointer moves
            onNewTask();
          }}
          {...tipped(t("header.newTask"), shortcutText("newTask"))}
        >
          {t("header.newTask")}
        </Button>
        <NotificationBell onOpenExecution={onOpenExecution} />
        <MoreMenu onNavigate={onNavigate} />
      </div>
      {mismatch ? (
        <div className="topbar__banner">
          <Banner tone="warning" icon="alert">
            {t("connection.versionMismatch", { daemon: health.version, desktop: appVersion })}
          </Banner>
        </div>
      ) : null}
      {commitMismatch ? (
        <div className="topbar__banner">
          <Banner
            tone="warning"
            icon="alert"
            actions={
              native.available ? (
                <Button variant="secondary" size="sm" icon="refresh" disabled={restart === "restarting"} onClick={() => void restartDaemon()}>
                  {restart === "restarting" ? t("connection.restartingDaemon") : t("connection.restartDaemon")}
                </Button>
              ) : null
            }
          >
            <div>{t("connection.commitMismatch", { daemon: daemonCommit, desktop: appCommit })}</div>
            {restart === "refused" ? <div>{t("connection.restartRefused")}</div> : null}
          </Banner>
        </div>
      ) : null}
      {health?.draining ? (
        <div className="topbar__banner">
          <Banner tone="warning" icon="power">
            {t("connection.draining", {
              count: health.draining.executions,
              time: formatTime(health.draining.deadlineAt, i18n.language, false),
            })}
          </Banner>
        </div>
      ) : null}
      {connection === "disconnected" || connection === "reconnecting" ? (
        <div className="topbar__banner">
          <Banner tone="blue" icon="refresh">
            {t("connection.lostHint")}
          </Banner>
        </div>
      ) : null}
      {connection === "not_answering" ? (
        <div className="topbar__banner">
          <Banner tone="warning" icon="refresh">
            {t("connection.notAnsweringHint")}
          </Banner>
        </div>
      ) : null}
      {tooltip}
    </header>
  );
}

function Wave({ active }: { active: boolean }) {
  return (
    <span className={cx("wave", active && "wave--active")} aria-hidden="true">
      {Array.from({ length: 7 }, (_, i) => (
        <span key={i} style={{ animationDelay: `${i * 90}ms` }} />
      ))}
    </span>
  );
}

function usePopover(): [boolean, (v: boolean) => void, RefObject<HTMLDivElement | null>] {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    window.addEventListener("mousedown", onDown);
    return () => window.removeEventListener("mousedown", onDown);
  }, [open]);
  return [open, setOpen, ref];
}

function NotificationBell({ onOpenExecution }: { onOpenExecution: (id: string) => void }) {
  const { t, i18n } = useTranslation();
  const unread = useStore((s) => s.unread);
  const [open, setOpen, ref] = usePopover();
  const now = useNow(30000);

  const openNotice = (n: Notification) => {
    void actions.markRead([n.id]);
    if (n.executionId) onOpenExecution(n.executionId);
    setOpen(false);
  };

  return (
    <div className="popover-anchor" ref={ref}>
      <button
        type="button"
        className="icon-btn icon-btn--framed"
        aria-label={t("header.notifications")}
        title={t("header.notifications")}
        onClick={() => setOpen(!open)}
      >
        <Icon name="bell" size={18} />
        {unread.length ? <span className="badge">{unread.length}</span> : null}
      </button>
      {open ? (
        <div className="popover popover--wide">
          <div className="popover__header">
            <strong>{t("header.notifications")}</strong>
            {unread.length ? (
              <Button variant="link" onClick={() => void actions.markRead()}>
                {t("header.markAllRead")}
              </Button>
            ) : null}
          </div>
          {unread.length === 0 ? <p className="popover__empty">{t("header.noNotifications")}</p> : null}
          <ul className="notice-list">
            {unread.map((n) => (
              <li key={n.id}>
                <button type="button" className={cx("notice", `notice--${n.severity}`)} onClick={() => openNotice(n)}>
                  <span className="notice__title">{noticeText(t, n).title}</span>
                  <span className="notice__body">{noticeText(t, n).body}</span>
                  <span className="notice__time">{formatRelative(n.createdAt, i18n.language, now)}</span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}

type TFn = ReturnType<typeof useTranslation>["t"];

/**
 * The translated text of a notice, from its event type and parameters; the daemon's text is the fallback. The reason
 * it quotes is translated too when the daemon wrote it.
 */
export function noticeText(t: TFn, n: Notification): { title: string; body: string } {
  const values = translateNested(n.params);
  const params: Record<string, string> = { taskName: "", harness: "", harnessName: "", summary: "", error: "", reason: "", url: "", ...values };
  const key = `notify.${n.eventType}`;
  // The key is built at run time, so the typed overloads, which walk every key of the resources, are left out: past
  // a size, checking them exceeds TypeScript's instantiation depth.
  const translate = t as unknown as (key: string, options: Record<string, string>) => string;
  const title = translate(`${key}.title`, { ...params, defaultValue: n.title });
  const body = translate(`${key}.body`, { ...params, defaultValue: n.body });
  return { title, body };
}

function MoreMenu({ onNavigate }: { onNavigate: (s: ScreenRoute) => void }) {
  const { t } = useTranslation();
  const [open, setOpen, ref] = usePopover();
  const running = useStore((s) => s.stats?.running ?? 0);

  const stop = async () => {
    setOpen(false);
    const choice = await askAndStopDaemon(t, running).catch(() => null);
    if (choice === "drain") void actions.refreshHealth(); // the topbar shows the drain at once
  };

  return (
    <div className="popover-anchor" ref={ref}>
      <IconButton icon="more" label={t("header.more")} className="icon-btn--framed" onClick={() => setOpen(!open)} />
      {open ? (
        <div className="popover">
          <MenuItem
            icon="refresh"
            onClick={() => {
              setOpen(false);
              void actions.refreshAll();
            }}
          >
            {t("header.refreshAll")}
          </MenuItem>
          <MenuItem
            icon="settings"
            onClick={() => {
              setOpen(false);
              onNavigate("settings");
            }}
          >
            {t("header.openSettings")}
          </MenuItem>
          <MenuItem icon="power" tone="danger" onClick={() => void stop()}>
            {t("header.stopDaemon")}
          </MenuItem>
        </div>
      ) : null}
    </div>
  );
}

function MenuItem({ icon, children, onClick, tone }: { icon: IconName; children: ReactNode; onClick: () => void; tone?: "danger" }) {
  return (
    <button type="button" className={cx("menu-item", tone && `menu-item--${tone}`)} onClick={onClick}>
      <Icon name={icon} size={16} />
      <span>{children}</span>
    </button>
  );
}

/**
 * The harness a task or an execution runs on; a script task runs a program instead, and says so. The effort and the
 * configured model and effort are shown only where they are passed: a configured value stands where the harness
 * reported none, marked as configured, never as the one used.
 */
export function HarnessLabel({
  harness,
  model,
  taskType,
  effort,
  configuredModel,
  configuredEffort,
}: {
  harness?: string;
  model?: string;
  taskType?: string;
  effort?: string;
  configuredModel?: string;
  configuredEffort?: string;
}) {
  const { t } = useTranslation();
  if (!harness && taskType === "script") {
    return (
      <span className="harness harness--script">
        <Icon name="terminal" size={15} />
        <span>{t("harness.scriptLabel")}</span>
      </span>
    );
  }
  if (!harness) return <span className="muted">—</span>;
  const name = t(`harness.names.${harness}` as never, { defaultValue: harness }) as string;
  return (
    <span className={cx("harness", `harness--${harness}`)}>
      <HarnessMark harness={harness} size={15} />
      <span>{name}</span>
      <UsedBadge kind="model" used={model} configured={configuredModel} />
      <UsedBadge kind="effort" used={effort} configured={configuredEffort} />
    </span>
  );
}

/**
 * A model or an effort beside the harness: the one the harness reported, or else the configured one, marked as
 * configured in its style and tooltip. `default` shows nothing.
 */
function UsedBadge({ kind, used, configured }: { kind: "model" | "effort"; used?: string; configured?: string }) {
  const { t } = useTranslation();
  const value = used || configured;
  if (!value || value === "default") return null;
  if (used) {
    return (
      <span
        className={cx("harness__model", kind === "effort" && "harness__effort")}
        title={kind === "effort" ? t("harness.effort", { value }) : undefined}
      >
        {value}
      </span>
    );
  }
  return (
    <span
      className={cx("harness__model", "harness__model--configured", kind === "effort" && "harness__effort")}
      title={kind === "effort" ? t("harness.configuredEffort", { value }) : t("harness.configuredModel", { value })}
      data-configured="true"
    >
      {value}
    </span>
  );
}
