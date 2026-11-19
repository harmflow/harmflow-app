import { useState } from "react";
import { useTranslation } from "react-i18next";
import { COUNTER_KEYS } from "../../components/Aside";
import { Button, Checkbox, Panel, Select } from "../../components/ui";
import { TYPEFACES, applyTypeface, resolveTypeface, type Typeface } from "../../design/typeface";
import { formatPercent } from "../../lib/costs";
import {
  CARD_SCREENS,
  moveCard,
  resolveCards,
  storeMetricCards,
  storedMetricCards,
  type CardChoice,
  type CardScreen,
  type MetricCards,
} from "../../lib/metricCards";
import { ZOOM_STEPS, type Zoom } from "../../lib/zoom";
import { useStore } from "../../state/store";
import { SettingRow } from "./SettingRow";
import { usePatch } from "./usePatch";

/**
 * How the dashboard looks: its zoom, its typeface, and the metric cards each screen shows. Neither control
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
      <MetricCardsPanel />
    </>
  );
}

/**
 * The metric cards of the Overview and of the Executions screen, each screen's list on its own: a checkbox shows or
 * hides a card, Move up and Move down reorder it, and Restore default drops the screen's choice. Each change is kept at
 * once, in the desktop's storage; a screen reads it when it opens.
 */
function MetricCardsPanel() {
  const { t } = useTranslation();
  const [kept, setKept] = useState<MetricCards>(() => storedMetricCards());

  const keep = (next: MetricCards) => {
    setKept(next);
    storeMetricCards(next);
  };
  const setList = (screen: CardScreen, list: CardChoice[]) => keep({ ...kept, [screen]: list });
  const restore = (screen: CardScreen) => {
    const { [screen]: _dropped, ...rest } = kept;
    keep(rest);
  };

  return (
    <Panel title={t("settings.subheads.metricCards")}>
      <p className="field__hint">{t("settings.metricCards.hint")}</p>
      {CARD_SCREENS.map((screen) => {
        const list = resolveCards(kept[screen], COUNTER_KEYS);
        return (
          <section key={screen} className="metric-cards" aria-labelledby={`metric-cards-${screen}`}>
            <header className="metric-cards__header">
              <h3 id={`metric-cards-${screen}`} className="metric-cards__title">
                {screen === "overview" ? t("nav.overview") : t("nav.executions")}
              </h3>
              <Button size="sm" variant="ghost" disabled={kept[screen] === undefined} onClick={() => restore(screen)}>
                {t("settings.metricCards.restore")}
              </Button>
            </header>
            <ol className="metric-cards__list">
              {list.map((c, i) => {
                const card = t(`counters.${c.key as (typeof COUNTER_KEYS)[number]}`);
                return (
                  <li key={c.key} className="metric-cards__row" data-card={c.key}>
                    <Checkbox
                      checked={c.shown}
                      label={card}
                      onChange={(shown) => setList(screen, list.map((x) => (x.key === c.key ? { ...x, shown } : x)))}
                    />
                    <span className="metric-cards__moves">
                      <Button size="sm" variant="ghost" icon="chevronDown" className="metric-cards__up" disabled={i === 0}
                        aria-label={t("settings.metricCards.moveUp", { card })} title={t("settings.metricCards.moveUp", { card })}
                        onClick={() => setList(screen, moveCard(list, i, -1))} />
                      <Button size="sm" variant="ghost" icon="chevronDown" className="metric-cards__down" disabled={i === list.length - 1}
                        aria-label={t("settings.metricCards.moveDown", { card })} title={t("settings.metricCards.moveDown", { card })}
                        onClick={() => setList(screen, moveCard(list, i, 1))} />
                    </span>
                  </li>
                );
              })}
            </ol>
          </section>
        );
      })}
    </Panel>
  );
}
