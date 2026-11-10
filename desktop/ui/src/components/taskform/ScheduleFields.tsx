// The schedule builder: the kind of schedule, a cron expression or an interval built from controls, the time zone, and
// the daemon's preview of the next runs.
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../../api/client";
import type { SchedulePreview } from "../../api/types";
import { daemonText } from "../../lib/daemonText";
import { formatDateTime } from "../../lib/format";
import { useDebounced } from "../../lib/hooks";
import {
  EVERY_MINUTES,
  INTERVAL_UNITS,
  REPEATS,
  cronOf,
  describeCron,
  intervalOf,
  intervalText,
  repeatOf,
  startRepeat,
  type GuidedRepeat,
  type IntervalUnit,
  type RepeatKind,
} from "../../lib/schedule";
import { getIn, scheduleKind, setScheduleKind, type Doc, type Path } from "../../lib/taskForm";
import { Field, NumberInput, SegmentedControl, SegmentedMultiSelect, Select, TextInput } from "../ui";
import { EnumField } from "./EnumField";
import type { SetFn } from "./formModel";

function systemZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    return "UTC";
  }
}

function timeZones(): string[] {
  try {
    return (Intl as unknown as { supportedValuesOf(k: string): string[] }).supportedValuesOf("timeZone");
  } catch {
    return [];
  }
}

export function ScheduleFields({
  doc,
  onChange,
  set,
  defLabel,
  locale,
  errorPaths,
}: {
  doc: Doc;
  onChange: (doc: Doc) => void;
  set: SetFn;
  defLabel: (path: Path, label?: (v: string) => string) => string;
  locale: string;
  errorPaths: Set<string>;
}) {
  const { t } = useTranslation();
  const kind = scheduleKind(doc);
  const cron = getIn<string>(doc, ["spec", "schedule", "cron"]);
  const interval = getIn<string>(doc, ["spec", "schedule", "interval"]);
  const timezone = getIn<string>(doc, ["spec", "schedule", "timezone"]);
  const zones = useMemo(timeZones, []);
  const zone = systemZone();
  const current = JSON.stringify({ cron, interval, timezone });
  const request = useDebounced(current, 400);
  // The preview keeps the request it answers: the debounced request trails the fields.
  const [answer, setAnswer] = useState<{ request: string; preview: SchedulePreview } | null>(null);

  useEffect(() => {
    const s = JSON.parse(request) as { cron?: string; interval?: string; timezone?: string };
    if (!s.cron && !s.interval) {
      setAnswer(null);
      return;
    }
    let cancelled = false;
    api
      .previewSchedule(s, 5)
      .then((p) => !cancelled && setAnswer({ request, preview: p }))
      .catch((e: Error) => !cancelled && setAnswer({ request, preview: { occurrences: [], error: e.message } }));
    return () => {
      cancelled = true;
    };
  }, [request]);

  const preview = answer?.preview;
  // The daemon's word on the schedule as it stands; an answer to an older request says nothing about it.
  const fresh = answer?.request === current ? answer.preview : undefined;
  const expressionError = (p?: SchedulePreview) => !!p?.error && /^(cron|interval|schedule)_/.test(p.errorKey ?? "");
  const fieldError = (path: string) =>
    fresh && expressionError(fresh)
      ? daemonText({ message: fresh.error!, messageKey: fresh.errorKey, params: fresh.errorParams })
      : errorPaths.has(path)
        ? t("common.error")
        : undefined;
  // Described only once the daemon accepted the very same text, so the words never explain a schedule that will not fire.
  const description = cron !== undefined && fresh && !fresh.error ? describeCron(cron, locale) : undefined;

  return (
    <>
      <SegmentedControl
        aria-label={t("tasks.fields.scheduleKind")}
        value={kind}
        options={[
          { value: "none", label: t("tasks.fields.scheduleNone") },
          { value: "cron", label: t("tasks.fields.scheduleCron") },
          { value: "interval", label: t("tasks.fields.scheduleInterval") },
        ]}
        onChange={(k) => onChange(setScheduleKind(doc, k))}
      />
      {kind === "cron" ? (
        <CronBuilder
          cron={cron ?? ""}
          write={(text) => set(["spec", "schedule", "cron"], text)}
          error={fieldError("/spec/schedule/cron")}
          description={description}
          locale={locale}
        />
      ) : null}
      {kind === "interval" ? (
        <IntervalFields
          interval={interval ?? ""}
          write={(text) => set(["spec", "schedule", "interval"], text)}
          error={fieldError("/spec/schedule/interval")}
        />
      ) : null}
      {kind !== "none" ? (
        <div className="grid-2">
          <Field label={t("tasks.fields.timezone")} hint={t("tasks.fields.timezoneHint", { zone })}>
            <input
              className="input mono"
              list="hf-timezones"
              value={timezone ?? ""}
              placeholder={zone}
              onChange={(e) => set(["spec", "schedule", "timezone"], e.target.value)}
            />
            <datalist id="hf-timezones">
              {zones.map((z) => (
                <option key={z} value={z} />
              ))}
            </datalist>
          </Field>
        </div>
      ) : null}
      <div className="grid-2">
        <EnumField label={t("tasks.fields.missedRun")} optionKey="missedRun" doc={doc} set={set} defLabel={defLabel} />
        <EnumField label={t("tasks.fields.overlap")} optionKey="overlap" doc={doc} set={set} defLabel={defLabel} />
      </div>
      <div className="preview">
        <span className="preview__label">{t("tasks.fields.preview")}</span>
        {kind === "none" ? <span className="muted">{t("tasks.fields.previewEmpty")}</span> : null}
        {/* The expression's and the interval's errors show at their field; the others, such as the time zone's, here. */}
        {preview?.error && !expressionError(preview) ? (
          <span className="tone-danger mono">
            {daemonText({ message: preview.error, messageKey: preview.errorKey, params: preview.errorParams })}
          </span>
        ) : null}
        <ul className="preview__list">
          {(preview?.occurrences ?? []).map((o) => (
            <li key={o} className="mono">
              {formatDateTime(o, locale, preview?.timezone ?? (timezone || undefined))}
            </li>
          ))}
        </ul>
      </div>
    </>
  );
}

// Sunday 4 January 2026 starts the week the weekday names are read from.
const weekdayName = (day: number, locale: string, weekday: "short" | "long") =>
  new Intl.DateTimeFormat(locale, { weekday, timeZone: "UTC" }).format(new Date(Date.UTC(2026, 0, 4 + day)));

const twoDigits = (n: number) => String(n).padStart(2, "0");

/**
 * The cron expression from a frequency and its controls, or as typed under Custom. The frequency is chosen once when
 * the builder opens, from the expression, and again only when the expression changes outside it, so typing under
 * Custom an expression a frequency writes keeps Custom.
 */
function CronBuilder({
  cron,
  write,
  error,
  description,
  locale,
}: {
  cron: string;
  write: (cron: string) => void;
  error?: string;
  description?: string;
  locale: string;
}) {
  const { t } = useTranslation();
  const [repeat, setRepeat] = useState<RepeatKind>(() => repeatOf(cron).kind);
  const [seen, setSeen] = useState(cron);
  if (cron !== seen) {
    setSeen(cron);
    setRepeat(repeatOf(cron).kind);
  }
  const writeCron = (text: string) => {
    setSeen(text);
    write(text);
  };
  const read = repeatOf(cron);
  const guided = repeat !== "custom" && read.kind === repeat ? (read as GuidedRepeat) : undefined;
  const update = (patch: Partial<Record<"every" | "minute" | "hour" | "day", number>> & { days?: number[] }) =>
    guided && writeCron(cronOf({ ...guided, ...patch } as GuidedRepeat));
  const choose = (kind: RepeatKind) => {
    setRepeat(kind);
    if (kind !== "custom") writeCron(cronOf(startRepeat(kind, cron)));
  };

  return (
    <>
      <div className="schedule-builder">
        <Field label={t("tasks.fields.repeats")}>
          <Select value={guided?.kind ?? "custom"} onChange={(e) => choose(e.target.value as RepeatKind)}>
            {REPEATS.map((k) => (
              <option key={k} value={k}>
                {t(`tasks.options.repeat.${k}`)}
              </option>
            ))}
          </Select>
        </Field>
        {guided?.kind === "minutes" ? (
          <Field label={t("tasks.fields.everyMinutes")}>
            <Select value={guided.every} onChange={(e) => update({ every: Number(e.target.value) })}>
              {EVERY_MINUTES.map((n) => (
                <option key={n} value={n}>
                  {t("tasks.fields.everyMinutesValue", { n })}
                </option>
              ))}
            </Select>
          </Field>
        ) : null}
        {guided?.kind === "hourly" ? (
          <Field label={t("tasks.fields.minute")}>
            <Select className="mono" value={guided.minute} onChange={(e) => update({ minute: Number(e.target.value) })}>
              {Array.from({ length: 60 }, (_, m) => (
                <option key={m} value={m}>
                  {twoDigits(m)}
                </option>
              ))}
            </Select>
          </Field>
        ) : null}
        {guided?.kind === "weekly" ? (
          <div className="field">
            <span className="field__label" id="hf-schedule-days">
              {t("tasks.fields.days")}
            </span>
            <SegmentedMultiSelect
              aria-labelledby="hf-schedule-days"
              values={guided.days}
              options={[0, 1, 2, 3, 4, 5, 6].map((d) => ({
                value: d,
                label: weekdayName(d, locale, "short"),
                ariaLabel: weekdayName(d, locale, "long"),
                // A weekly schedule keeps at least one day.
                disabled: guided.days.length === 1 && guided.days.includes(d),
              }))}
              onChange={(days) => update({ days })}
            />
          </div>
        ) : null}
        {guided?.kind === "monthly" ? (
          <Field label={t("tasks.fields.dayOfMonth")} hint={guided.day >= 29 ? t("tasks.fields.dayOfMonthHint", { day: guided.day }) : undefined}>
            <Select value={guided.day} onChange={(e) => update({ day: Number(e.target.value) })}>
              {Array.from({ length: 31 }, (_, i) => (
                <option key={i + 1} value={i + 1}>
                  {i + 1}
                </option>
              ))}
            </Select>
          </Field>
        ) : null}
        {guided && "hour" in guided ? (
          <Field label={t("tasks.fields.time")}>
            <input
              type="time"
              className="input mono"
              required
              value={`${twoDigits(guided.hour)}:${twoDigits(guided.minute)}`}
              onChange={(e) => {
                const m = /^(\d{2}):(\d{2})$/.exec(e.target.value);
                if (m) update({ hour: Number(m[1]), minute: Number(m[2]) });
              }}
            />
          </Field>
        ) : null}
        <Field label={t("tasks.fields.cron")} error={error}>
          {guided ? (
            <output className="schedule-expression mono">{cron}</output>
          ) : (
            <TextInput className="mono" value={cron} onChange={(e) => writeCron(e.target.value)} placeholder="0 9 * * 1-5" />
          )}
        </Field>
      </div>
      {description ? <p className="schedule-description">{description}</p> : null}
    </>
  );
}

/** The interval from a number and a unit; a value they cannot show stays a text field, with the daemon's message. */
function IntervalFields({ interval, write, error }: { interval: string; write: (interval: string) => void; error?: string }) {
  const { t } = useTranslation();
  const value = intervalOf(interval);
  if (!value) {
    return (
      <div className="grid-2">
        <Field label={t("tasks.fields.interval")} error={error}>
          <TextInput className="mono" value={interval} onChange={(e) => write(e.target.value)} placeholder="30m" />
        </Field>
      </div>
    );
  }
  return (
    <div className="schedule-builder">
      <Field label={t("tasks.fields.intervalEvery")} error={error}>
        <NumberInput
          min={1}
          step={1}
          value={value.every}
          onValue={(n) => {
            if (n !== undefined && Number.isSafeInteger(n) && n > 0) write(intervalText(n, value.unit));
          }}
        />
      </Field>
      <Field label={t("tasks.fields.intervalUnit")}>
        <Select value={value.unit} onChange={(e) => write(intervalText(value.every, e.target.value as IntervalUnit))}>
          {INTERVAL_UNITS.map((u) => (
            <option key={u} value={u}>
              {t(`tasks.options.intervalUnit.${u}`)}
            </option>
          ))}
        </Select>
      </Field>
    </div>
  );
}
