import { useTranslation } from "react-i18next";
import { Panel } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { useStore } from "../../state/store";
import { SettingRow } from "./SettingRow";

export function AboutSection() {
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
