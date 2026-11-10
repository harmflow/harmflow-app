// Settings, divided into sections: a list of them on the left, and the open one beside it. Interface: the language, the
// menu bar, notifications, and the keyboard shortcuts. Execution: the AI tools, the general system prompt, and the command-line tools, and the paths a task may use. This
// computer: the daemon, storage, and local API security. About stands alone at the foot of the list.
import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { useTranslation } from "react-i18next";
import { native } from "../api/native";
import { Icon, type IconName } from "../components/Icon";
import { tipBeside, useTooltip } from "../components/Shell";
import type { LeaveCheck } from "../components/TaskEditor";
import { cx } from "../components/ui";
import type { DraftReport } from "../lib/hooks";
import { dynamicText, useRegistered } from "../registrations";
import { actions } from "../state/store";
import { AboutSection } from "./settings/AboutSection";
import { AgentsSection } from "./settings/AgentsSection";
import { DaemonSection } from "./settings/DaemonSection";
import { GeneralSection } from "./settings/GeneralSection";
import { NotificationsSection } from "./settings/NotificationsSection";
import { PathsSection } from "./settings/PathsSection";
import { SecuritySection } from "./settings/SecuritySection";
import { ShortcutsSection } from "./settings/ShortcutsSection";
import { StorageSection } from "./settings/StorageSection";

export { DIRECTORIES } from "./settings/directories";

export type SettingsSection = "general" | "notifications" | "shortcuts" | "agents" | "paths" | "daemon" | "storage" | "security" | "about";

/** A built-in section, or the key of a section a registration added. */
export type SettingsSectionKey = SettingsSection | (string & {});

/** The section list's groups, in order; About follows them alone, under a divider. */
export const SECTION_GROUPS: ReadonlyArray<{ key: "interface" | "agents" | "computer"; sections: SettingsSection[] }> = [
  { key: "interface", sections: ["general", "notifications", "shortcuts"] },
  { key: "agents", sections: ["agents", "paths"] },
  { key: "computer", sections: ["daemon", "storage", "security"] },
];

const SECTION_ICONS: Record<SettingsSection, IconName> = {
  general: "globe",
  notifications: "bell",
  shortcuts: "keyboard",
  agents: "agent",
  paths: "folder",
  daemon: "power",
  storage: "layers",
  security: "shield",
  about: "sparkle",
};

/** Whether the value is a built-in section; a registered section is in the registrations instead. */
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
  section?: SettingsSectionKey;
  /** Opens another section; the shell asks first while a draft is held. */
  onSection?: (s: SettingsSectionKey) => void;
  /** Receives the question to ask before leaving while a section holds a draft, and null once it holds none. */
  onLeaveCheck?: (check: LeaveCheck | null) => void;
}) {
  const { t } = useTranslation();
  const registered = useRegistered();
  const RegisteredSection = registered.settingsSections.find((s) => s.key === section)?.component;
  const screen = useRef<HTMLDivElement>(null);
  const iconsOnly = useNarrowerThan(screen, ICONS_ONLY_BELOW);
  const { tipped, tooltip, hide } = useTooltip(tipBeside);

  // The list moves under a shown tooltip; the next hover or focus places it again.
  useEffect(hide, [iconsOnly, section, hide]);

  useEffect(() => {
    void actions.refreshSettings();
    void actions.refreshHarnesses();
  }, []);

  // Four settings keep a draft until confirmed: the path variables and the allowed folders (Save), the log retention
  // (Apply), a harness's path set by hand (its Save), and the general system prompt (Save). While one is held, every way out of the section asks first,
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

  const item = (s: SettingsSectionKey, icon: IconName = SECTION_ICONS[s as SettingsSection]) => {
    const name = dynamicText(t, `settings.sections.${s}`);
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
        <Icon name={icon} size={17} />
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
            {group.sections.map((s) => item(s))}
            {registered.settingsSections.filter((s) => s.group === group.key).map((s) => item(s.key, s.icon))}
          </div>
        ))}
        <hr className="settings-nav__divider" />
        {item("about")}
      </nav>
      <section className="settings__section" aria-labelledby="settings-section-title">
        <h2 id="settings-section-title" className="settings__title">
          {dynamicText(t, `settings.sections.${section}`)}
        </h2>
        {section === "general" ? <GeneralSection /> : null}
        {section === "notifications" ? <NotificationsSection /> : null}
        {section === "shortcuts" ? <ShortcutsSection /> : null}
        {section === "agents" ? <AgentsSection report={report} /> : null}
        {section === "paths" ? <PathsSection report={report} /> : null}
        {section === "daemon" ? <DaemonSection /> : null}
        {section === "storage" ? <StorageSection report={report} /> : null}
        {section === "security" ? <SecuritySection /> : null}
        {section === "about" ? <AboutSection /> : null}
        {RegisteredSection ? <RegisteredSection /> : null}
      </section>
      {tooltip}
    </div>
  );
}
