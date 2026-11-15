import { useTranslation } from "react-i18next";
import { Panel, Select } from "../../components/ui";
import { TYPEFACES, applyTypeface, resolveTypeface, type Typeface } from "../../design/typeface";
import { formatPercent } from "../../lib/costs";
import { ZOOM_STEPS, type Zoom } from "../../lib/zoom";
import { useStore } from "../../state/store";
import { SettingRow } from "./SettingRow";
import { usePatch } from "./usePatch";

/**
 * How the dashboard looks: its zoom and its typeface, nothing else, since the zoom scales the text too. Neither control
 * has a reset action: picking 100% or HarmFlow is the reset. The zoom is the window's, the one ⌘+, ⌘−, and ⌘0 move, so
 * the shell holds it and this section only shows and sets it.
 */
export function AppearanceSection({ zoom, onZoom }: { zoom: Zoom; onZoom?: (level: Zoom) => void }) {
  const { t, i18n } = useTranslation();
  const settings = useStore((s) => s.settings);
  const patch = usePatch();

  const setTypeface = async (typeface: Typeface) => {
    applyTypeface(typeface);
    await patch({ typeface }, true);
  };

  return (
    <>
      <Panel title={t("settings.subheads.zoom")}>
        <SettingRow id="settings-zoom" label={t("settings.zoom")} hint={t("settings.zoomHint")}>
          <Select id="settings-zoom" value={String(zoom)} onChange={(e) => onZoom?.(Number(e.target.value) as Zoom)}>
            {ZOOM_STEPS.map((step) => (
              <option key={step} value={step}>
                {formatPercent(step / 100, i18n.language)}
              </option>
            ))}
          </Select>
        </SettingRow>
      </Panel>
      <Panel title={t("settings.subheads.typeface")}>
        <SettingRow id="settings-typeface" label={t("settings.typeface")} hint={t("settings.typefaceHint")}>
          <Select
            id="settings-typeface"
            value={resolveTypeface(settings?.typeface)}
            onChange={(e) => void setTypeface(e.target.value as Typeface)}
          >
            {TYPEFACES.map((f) => (
              <option key={f} value={f}>
                {t(`settings.typefaces.${f}`)}
              </option>
            ))}
          </Select>
        </SettingRow>
      </Panel>
    </>
  );
}
