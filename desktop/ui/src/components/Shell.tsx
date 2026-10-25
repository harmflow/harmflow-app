import { useCallback, useEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import { native } from "../api/native";
import type { Notification } from "../api/types";
import { translateNested } from "../lib/daemonText";
import { formatBytes, formatNumber, formatRelative, formatTime } from "../lib/format";
import { useNow } from "../lib/hooks";
import { actions, useStore } from "../state/store";
import { HarnessMark, Icon, Logo, type IconName } from "./Icon";
import { toast } from "./toast";
import { Banner, Button, Gauge, IconButton, cx } from "./ui";

export type Screen = "overview" | "executions" | "tasks" | "history" | "settings";

const NAV: Array<{ key: Screen; icon: IconName }> = [
  { key: "overview", icon: "grid" },
  { key: "executions", icon: "playFilled" },
  { key: "tasks", icon: "list" },
  { key: "history", icon: "history" },
  { key: "settings", icon: "settings" },
];

const TOGGLE_SHORTCUT = "⌘B";
const NEW_TASK_SHORTCUT = "⌘N";

interface Tip {
  text: string;
  keys?: string;
  top: number;
  left: number;
  below?: boolean;
}

type Place = (target: HTMLElement, text: string, keys?: string) => Tip;

/** A tooltip just past the sidebar's edge, level with its target. */
const tipBeside: Place = (target, text, keys) => {
  const box = target.getBoundingClientRect();
  const edge = target.closest(".sidebar")?.getBoundingClientRect().right ?? box.right;
  return { text, keys, top: box.top + box.height / 2, left: edge + 8 };
};

/** A tooltip just below its target, centred on it: the topbar's. */
const tipBelow: Place = (target, text, keys) => {
  const box = target.getBoundingClientRect();
  return { text, keys, top: box.bottom + 8, left: box.left + box.width / 2, below: true };
};

/**
 * A tooltip placed by `place`, shown at once on hover and on keyboard focus: `title` appears late and never on focus.
 * `tipped` gives a target its handlers; `tooltip` is the element to render; `hide` clears it.
 */
function useTooltip(place: Place) {
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
 * gauges and the version show only while expanded.
 */
export function Sidebar({
  screen,
  onNavigate,
  collapsed,
  onToggle,
}: {
  screen: Screen;
  onNavigate: (s: Screen) => void;
  collapsed: boolean;
  onToggle: () => void;
}) {
  const { t, i18n } = useTranslation();
  const system = useStore((s) => s.stats?.system);
  const version = useStore((s) => s.appVersion);
  const running = useStore((s) => s.stats?.running ?? 0);
  const locale = i18n.language;
  const { tipped, tooltip, hide } = useTooltip(tipBeside);

  // The layout moves under a shown tooltip; the next hover or focus places it again.
  useEffect(hide, [collapsed, hide]);

  const toggleLabel = collapsed ? t("sidebar.expand") : t("sidebar.collapse");

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
        {NAV.map((item) => (
          <button
            key={item.key}
            type="button"
            className={cx("nav__item", screen === item.key && "nav__item--active")}
            aria-current={screen === item.key ? "page" : undefined}
            onClick={() => onNavigate(item.key)}
            {...(collapsed ? tipped(t(`nav.${item.key}`)) : {})}
          >
            <Icon name={item.icon} size={19} />
            <span className={cx("nav__label", collapsed && "sr-only")}>{t(`nav.${item.key}`)}</span>
            {item.key === "executions" && running > 0 ? <span className="nav__badge">{running}</span> : null}
          </button>
        ))}
      </nav>

      <div className="sidebar__spacer" />

      <div className="sidebar__foot">
        {collapsed ? null : (
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
        )}
        <div className="sidebar__bar">
          {collapsed ? null : <span className="sidebar__version">{t("common.version", { version })}</span>}
          <button
            type="button"
            className="icon-btn sidebar__toggle"
            aria-label={toggleLabel}
            aria-keyshortcuts="Meta+B"
            onClick={onToggle}
            {...tipped(toggleLabel, TOGGLE_SHORTCUT)}
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
 * The topbar. When the window and the daemon come from different commits, a banner names both and offers to restart
 * the daemon on the window's build; only that click restarts it.
 */
export function Header({
  screen,
  onNewTask,
  onNavigate,
  onOpenExecution,
}: {
  screen: Screen;
  onNewTask: () => void;
  onNavigate: (s: Screen) => void;
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
      toast(t("connection.restartFailed", { message: err instanceof Error ? err.message : String(err) }), "danger");
    }
  };

  return (
    <header className="topbar" data-tauri-drag-region>
      <div className="topbar__titles">
        <span className="breadcrumb">
          <span className="breadcrumb__prompt">&gt;_</span> {t(`screens.${screen}.breadcrumb`)}
        </span>
        <h1 className="topbar__title">{t(`screens.${screen}.title`)}</h1>
        <p className="topbar__subtitle">{t(`screens.${screen}.subtitle`)}</p>
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
          aria-keyshortcuts="Meta+N"
          onClick={() => {
            hide(); // the picker covers the button, which may get no mouseleave until the pointer moves
            onNewTask();
          }}
          {...tipped(t("header.newTask"), NEW_TASK_SHORTCUT)}
        >
          {t("header.newTask")}
        </Button>
        <NotificationBell onOpenExecution={onOpenExecution} />
        <MoreMenu onNavigate={onNavigate} />
      </div>
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
      ) : mismatch ? (
        <div className="topbar__banner">
          <Banner tone="warning" icon="alert">
            {t("connection.versionMismatch", { daemon: health.version, desktop: appVersion })}
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
              <button type="button" className="link" onClick={() => void actions.markRead()}>
                {t("header.markAllRead")}
              </button>
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
  const params: Record<string, string> = { taskName: "", harness: "", summary: "", error: "", reason: "", url: "", ...values };
  const key = `notify.${n.eventType}`;
  // The key is built at run time, so the typed overloads, which walk every key of the resources, are left out: past
  // a size, checking them exceeds TypeScript's instantiation depth.
  const translate = t as unknown as (key: string, options: Record<string, string>) => string;
  const title = translate(`${key}.title`, { ...params, defaultValue: n.title });
  const body = translate(`${key}.body`, { ...params, defaultValue: n.body });
  return { title, body };
}

function MoreMenu({ onNavigate }: { onNavigate: (s: Screen) => void }) {
  const { t } = useTranslation();
  const [open, setOpen, ref] = usePopover();
  const running = useStore((s) => s.stats?.running ?? 0);

  const stop = async () => {
    setOpen(false);
    const message = running > 0 ? t("settings.stopConfirmRunning", { count: running }) : t("settings.stopConfirm");
    if (!(await native.confirm(message, t("settings.stopConfirmTitle"), t("settings.stopDaemon"), t("common.cancel")))) return;
    await api.stopDaemon(running > 0).catch(() => undefined);
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

/** The harness a task or an execution runs on; a script task runs a program instead, and says so. */
export function HarnessLabel({ harness, model, taskType }: { harness?: string; model?: string; taskType?: string }) {
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
      {model && model !== "default" ? <span className="harness__model">{model}</span> : null}
    </span>
  );
}
