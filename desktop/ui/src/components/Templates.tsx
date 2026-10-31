// The task templates a new task starts from, the same wherever the dashboard offers them: the topbar's New task and ⌘N,
// the Tasks screen, the Overview's empty state, and onboarding.
import { useTranslation } from "react-i18next";
import type { Template } from "../api/types";
import { toneClass, typeGlyph } from "../lib/taskType";
import { localized, offeredTemplates } from "../lib/templates";
import { useStore } from "../state/store";
import { Icon } from "./Icon";
import { Modal, cx } from "./ui";

/** One button per template the daemon offers, named and described in the interface's language. */
export function TemplateButtons({ onPick, disabled }: { onPick: (template: string) => void; disabled?: boolean }) {
  const { t, i18n } = useTranslation();
  const templates = offeredTemplates(useStore((s) => s.templates));
  if (!templates.length) return <p className="muted">{t("tasks.templates.none")}</p>;
  return (
    <div className="templates">
      {templates.map((tpl: Template) => {
        const glyph = typeGlyph(tpl.type);
        return (
          <button key={tpl.id} type="button" className="template" disabled={disabled} onClick={() => onPick(tpl.id)}>
            <span className={cx("template__icon", toneClass(glyph.tone))}>
              <Icon name={glyph.icon} size={24} />
            </span>
            <strong>{localized(tpl.name, i18n.language)}</strong>
            <span className="muted">{localized(tpl.description, i18n.language)}</span>
          </button>
        );
      })}
    </div>
  );
}

/** The dialog that starts a new task from a template. */
export function TemplatePicker({ onPick, onClose }: { onPick: (template: string) => void; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <Modal title={t("tasks.templates.title")} onClose={onClose}>
      <TemplateButtons onPick={onPick} />
    </Modal>
  );
}
