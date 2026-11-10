import { useTranslation } from "react-i18next";
import { Panel } from "../../components/ui";
import { SHORTCUTS, keycaps, shortcutsByGroup } from "../../lib/shortcuts";
import { SettingRow } from "./SettingRow";

/** HarmFlow's keyboard shortcuts, grouped by where they work, read from the one table the handlers match. */
export function ShortcutsSection() {
  const { t } = useTranslation();
  return (
    <>
      {shortcutsByGroup().map(({ group, ids }) => (
        <Panel key={group} title={t(`settings.shortcuts.groups.${group}`)}>
          {ids.map((id) => (
            <SettingRow key={id} label={t(`settings.shortcuts.labels.${id}`)}>
              <span className="keycaps" data-shortcut={id}>
                {SHORTCUTS[id].chords.map((chord, i) => (
                  <span key={i} className="keycaps__chord">
                    {i > 0 ? <span className="keycaps__or">/</span> : null}
                    {keycaps(chord).map((cap, j) => (
                      <kbd key={j} className="keycap">
                        {cap}
                      </kbd>
                    ))}
                  </span>
                ))}
              </span>
            </SettingRow>
          ))}
        </Panel>
      ))}
    </>
  );
}
