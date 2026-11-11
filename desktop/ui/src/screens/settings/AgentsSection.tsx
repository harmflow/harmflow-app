import { Fragment, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../../api/client";
import { HarnessInventory, ToolRow } from "../../components/Harnesses";
import { toast } from "../../components/toast";
import { Button, Field, NumberInput, Panel, TextArea } from "../../components/ui";
import { errorMessage } from "../../lib/errorMessage";
import { useDraft, type DraftReport } from "../../lib/hooks";
import { actions, useStore } from "../../state/store";
import { SettingRow } from "./SettingRow";
import { usePatch } from "./usePatch";

export function AgentsSection({ report }: { report: DraftReport }) {
  const { t, i18n } = useTranslation();
  const harnesses = useStore((s) => s.harnesses);
  const settings = useStore((s) => s.settings);
  const patch = usePatch();
  const tools = useStore((s) => s.tools);
  const redetecting = useStore((s) => s.redetecting !== null);
  const redetect = async () => {
    try {
      await actions.redetectHarnesses();
    } catch (err) {
      toast(t("settings.refreshHarnessesFailed", { error: errorMessage(err) }), "danger");
    }
  };
  return (
    <>
      <Panel
        title={t("settings.subheads.agents")}
        actions={
          <Button size="sm" icon="refresh" disabled={redetecting} aria-busy={redetecting} onClick={() => void redetect()}>
            {redetecting ? t("settings.refreshingHarnesses") : t("settings.refreshHarnesses")}
          </Button>
        }
      >
        <div className="inventory-grid">
          {harnesses.map((h) => (
            <HarnessInventory key={h.id} harness={h} locale={i18n.language} onDraft={report} />
          ))}
        </div>
      </Panel>
      <Panel title={t("settings.subheads.quota")}>
        {harnesses.map((h) => {
          const entry = settings?.quota?.[h.id];
          // A change to one of a tool's two settings keeps the other, and the other tools' entries.
          const save = (change: { maxPercent?: number; checkIntervalMinutes?: number }) =>
            void patch({ quota: { ...(settings?.quota ?? {}), [h.id]: { ...entry, maxPercent: entry?.maxPercent ?? FULL_SHARE, ...change } } });
          return (
            <Fragment key={h.id}>
              <SettingRow id={`settings-quota-${h.id}`} label={t("settings.quotaShare", { harness: h.name })} hint={t("settings.quotaShareHint")}>
                <QuotaShareSlider id={`settings-quota-${h.id}`} value={entry?.maxPercent ?? FULL_SHARE} onSave={(share) => save({ maxPercent: share })} />
              </SettingRow>
              <SettingRow
                id={`settings-quota-interval-${h.id}`}
                label={t("settings.quotaInterval", { harness: h.name })}
                hint={t("settings.quotaIntervalHint")}
              >
                <QuotaIntervalField
                  id={`settings-quota-interval-${h.id}`}
                  draft={`quotaInterval-${h.id}`}
                  value={entry?.checkIntervalMinutes ?? DEFAULT_QUOTA_CHECK_MINUTES}
                  onSave={(minutes) => save({ checkIntervalMinutes: minutes })}
                  onDraft={report}
                />
              </SettingRow>
            </Fragment>
          );
        })}
      </Panel>
      <Panel title={t("settings.subheads.systemPrompt")}>
        <SystemPromptField value={settings?.systemPrompt ?? ""} onDraft={report} />
      </Panel>
      <Panel title={t("settings.subheads.tools")}>
        {tools.map((tool) => (
          <ToolRow key={tool.id} tool={tool} />
        ))}
      </Panel>
    </>
  );
}

/**
 * The general system prompt, which every harness session of a GitHub or general task receives. It stays a draft until
 * Save, which sends the text whole, an emptied one included; a text the daemon refuses shows the daemon's error on the
 * field and stays a draft.
 */
function SystemPromptField({ value, onDraft }: { value: string; onDraft: DraftReport }) {
  const { t } = useTranslation();
  const [text, setText] = useState(value);
  const [error, setError] = useState<string>();
  const [saving, setSaving] = useState(false);
  useEffect(() => setText(value), [value]);
  const changed = text !== value;
  useDraft(onDraft, "systemPrompt", changed);
  const save = async () => {
    setSaving(true);
    try {
      await api.updateSettings({ systemPrompt: text });
      await actions.refreshSettings();
      setError(undefined);
      toast(t("settings.saved"));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="row-list">
      <Field label={t("settings.systemPrompt")} hint={t("settings.systemPromptHint")} error={error}>
        <TextArea
          id="settings-system-prompt"
          rows={6}
          value={text}
          aria-invalid={error ? true : undefined}
          onChange={(e) => {
            setText(e.target.value);
            setError(undefined);
          }}
        />
      </Field>
      <div className="inline-actions">
        <Button size="sm" variant="primary" disabled={!changed || saving} onClick={() => void save()}>
          {t("common.save")}
        </Button>
      </div>
    </div>
  );
}

/** The share of a quota window HarmFlow may spend when the user sets none: the whole of it. */
const FULL_SHARE = 100;
// How long a reading of an AI tool's quota stands when no interval is set, and the longest interval, in minutes.
const DEFAULT_QUOTA_CHECK_MINUTES = 5;
const MAX_QUOTA_CHECK_MINUTES = 60;

/**
 * A slider of the share of an AI tool's quota HarmFlow may spend, from 1% to 100%. It shows the share while it is
 * dragged and saves it once it is let go, or on each arrow key, so a drag sends one change.
 */
function QuotaShareSlider({ id, value, onSave }: { id: string; value: number; onSave: (share: number) => void }) {
  const [share, setShare] = useState(value);
  // The last share saved, so letting go and leaving the slider send one change between them.
  const saved = useRef(value);
  useEffect(() => {
    setShare(value);
    saved.current = value;
  }, [value]);
  const save = (next: number) => {
    if (next === saved.current) return;
    saved.current = next;
    onSave(next);
  };
  return (
    <span className="quota-share">
      <input
        id={id}
        type="range"
        className="quota-share__range"
        min={1}
        max={FULL_SHARE}
        step={1}
        value={share}
        aria-valuetext={`${share}%`}
        onChange={(e) => setShare(Number(e.currentTarget.value))}
        onPointerUp={(e) => save(Number(e.currentTarget.value))}
        onKeyUp={(e) => save(Number(e.currentTarget.value))}
        onBlur={(e) => save(Number(e.currentTarget.value))}
      />
      <output className="quota-share__value mono" htmlFor={id}>
        {share}%
      </output>
    </span>
  );
}

/** How long a reading of an AI tool's quota stands before an attempt reads it again, from 1 to 60 minutes. */
function QuotaIntervalField({
  id,
  draft,
  value,
  onSave,
  onDraft,
}: {
  id: string;
  draft: string;
  value: number;
  onSave: (minutes: number) => void;
  onDraft: DraftReport;
}) {
  const { t } = useTranslation();
  const [minutes, setMinutes] = useState<number | undefined>(value);
  useEffect(() => setMinutes(value), [value]);
  useDraft(onDraft, draft, minutes !== value);
  const valid = minutes !== undefined && Number.isInteger(minutes) && minutes >= 1 && minutes <= MAX_QUOTA_CHECK_MINUTES;
  return (
    <div className="inline-form">
      <NumberInput id={id} value={minutes} onValue={setMinutes} min={1} max={MAX_QUOTA_CHECK_MINUTES} />
      <Button size="sm" disabled={!valid || minutes === value} onClick={() => valid && onSave(minutes)}>
        {t("common.apply")}
      </Button>
    </div>
  );
}
