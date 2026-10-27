// The schedule builder of the task form: the frequencies whose controls write a cron expression, the interval's number
// and unit, and the plain-language description of any expression the daemon accepts. The description follows the
// daemon's cron dialect (daemon/internal/domain/schedule.go), not another cron's.
import i18n from "../i18n";

/** The frequencies of the Repeats select, in its order. */
export const REPEATS = ["minutes", "hourly", "daily", "weekdays", "weekly", "monthly", "custom"] as const;
export type RepeatKind = (typeof REPEATS)[number];

/** Every N minutes offers only divisors of 60: `*\/N` restarts each hour, so any other N would not space evenly. */
export const EVERY_MINUTES = [5, 10, 15, 20, 30] as const;

export type Repeat =
  | { kind: "minutes"; every: number }
  | { kind: "hourly"; minute: number }
  | { kind: "daily" | "weekdays"; hour: number; minute: number }
  | { kind: "weekly"; hour: number; minute: number; days: number[] }
  | { kind: "monthly"; hour: number; minute: number; day: number }
  | { kind: "custom" };

export type GuidedRepeat = Exclude<Repeat, { kind: "custom" }>;

/** The expression a frequency's controls write. Weekly writes its days in ascending order, Sunday as 0. */
export function cronOf(r: GuidedRepeat): string {
  switch (r.kind) {
    case "minutes":
      return `*/${r.every} * * * *`;
    case "hourly":
      return `${r.minute} * * * *`;
    case "daily":
      return `${r.minute} ${r.hour} * * *`;
    case "weekdays":
      return `${r.minute} ${r.hour} * * 1-5`;
    case "weekly":
      return `${r.minute} ${r.hour} * * ${[...new Set(r.days)].sort((a, b) => a - b).join(",")}`;
    case "monthly":
      return `${r.minute} ${r.hour} ${r.day} * *`;
  }
}

/**
 * The frequency whose controls write exactly this text, or Custom. A reading is kept only when writing it back gives
 * the same text, so `09 9 * * *`, `0 9 * * 5,1`, `0 9 * * 7`, or a doubled space opens in Custom, unchanged.
 */
export function repeatOf(cron: string | undefined): Repeat {
  const guess = guessRepeat(cron ?? "");
  return guess && cronOf(guess) === cron ? guess : { kind: "custom" };
}

function guessRepeat(cron: string): GuidedRepeat | undefined {
  const f = cron.split(" ");
  if (f.length !== 5 || f[3] !== "*") return undefined;
  const [mi, h, dom, , dow] = f;
  const step = /^\*\/(\d+)$/.exec(mi);
  if (step && h === "*" && dom === "*" && dow === "*") {
    const every = Number(step[1]);
    return (EVERY_MINUTES as readonly number[]).includes(every) ? { kind: "minutes", every } : undefined;
  }
  const minute = whole(mi, 0, 59);
  if (minute === undefined) return undefined;
  if (h === "*") return dom === "*" && dow === "*" ? { kind: "hourly", minute } : undefined;
  const hour = whole(h, 0, 23);
  if (hour === undefined) return undefined;
  if (dom !== "*") {
    const day = whole(dom, 1, 31);
    return day !== undefined && dow === "*" ? { kind: "monthly", hour, minute, day } : undefined;
  }
  if (dow === "*") return { kind: "daily", hour, minute };
  if (dow === "1-5") return { kind: "weekdays", hour, minute };
  const days = dow.split(",").map((d) => whole(d, 0, 6));
  return days.every((d) => d !== undefined) ? { kind: "weekly", hour, minute, days: days as number[] } : undefined;
}

function whole(text: string, min: number, max: number): number | undefined {
  if (!/^\d+$/.test(text)) return undefined;
  const n = Number(text);
  return n >= min && n <= max ? n : undefined;
}

/**
 * The frequency a choice in the Repeats select starts at. It keeps the time and the minute of the expression it
 * replaces when that expression has a single one, and starts at 09:00 otherwise.
 */
export function startRepeat(kind: Exclude<RepeatKind, "custom">, cron: string | undefined): GuidedRepeat {
  const from = repeatOf(cron);
  const f = (cron ?? "").trim().split(/\s+/);
  const minute = "minute" in from ? from.minute : (whole(f[0] ?? "", 0, 59) ?? 0);
  const hour = "hour" in from ? from.hour : (whole(f[1] ?? "", 0, 23) ?? 9);
  switch (kind) {
    case "minutes":
      return { kind, every: from.kind === "minutes" ? from.every : 15 };
    case "hourly":
      return { kind, minute };
    case "daily":
    case "weekdays":
      return { kind, hour, minute };
    case "weekly":
      return { kind, hour, minute, days: from.kind === "weekdays" ? [1, 2, 3, 4, 5] : [1] };
    case "monthly":
      return { kind, hour, minute, day: 1 };
  }
}

export type IntervalUnit = "m" | "h" | "d";
export const INTERVAL_UNITS: readonly IntervalUnit[] = ["m", "h", "d"];

/** The number and unit of an interval the controls can show: a whole number above 0 and m, h, or d, as written. */
export function intervalOf(text: string | undefined): { every: number; unit: IntervalUnit } | undefined {
  const m = /^([1-9]\d*)([mhd])$/.exec(text ?? "");
  if (!m || !Number.isSafeInteger(Number(m[1]))) return undefined;
  return { every: Number(m[1]), unit: m[2] as IntervalUnit };
}

export function intervalText(every: number, unit: IntervalUnit): string {
  return `${every}${unit}`;
}

// The macros the daemon accepts in place of 5 fields, in lowercase only.
const MACROS: Record<string, string> = {
  "@yearly": "0 0 1 1 *",
  "@annually": "0 0 1 1 *",
  "@monthly": "0 0 1 * *",
  "@weekly": "0 0 * * 0",
  "@daily": "0 0 * * *",
  "@midnight": "0 0 * * *",
  "@hourly": "0 * * * *",
};

const MONTH_NAMES: Record<string, number> = {
  JAN: 1, FEB: 2, MAR: 3, APR: 4, MAY: 5, JUN: 6, JUL: 7, AUG: 8, SEP: 9, OCT: 10, NOV: 11, DEC: 12,
};
const DAY_NAMES: Record<string, number> = { SUN: 0, MON: 1, TUE: 2, WED: 3, THU: 4, FRI: 5, SAT: 6 };

interface CronSets {
  minutes: number[];
  hours: number[];
  days: number[];
  months: number[];
  weekdays: number[];
  // A day field that is `*` or starts with `*/` counts as unrestricted when the two day fields are combined.
  daysStar: boolean;
  weekdaysStar: boolean;
}

/** The values each field of an expression fires on, read as the daemon reads them, or undefined when it refuses it. */
export function parseCron(expr: string): CronSets | undefined {
  let text = expr.trim();
  text = MACROS[text] ?? text;
  const f = text.split(/\s+/);
  if (f.length !== 5) return undefined;
  const minutes = parseField(f[0], 0, 59);
  const hours = parseField(f[1], 0, 23);
  const days = parseField(f[2], 1, 31);
  const months = parseField(f[3], 1, 12, MONTH_NAMES);
  const weekdays = parseField(f[4], 0, 7, DAY_NAMES);
  if (!minutes || !hours || !days || !months || !weekdays) return undefined;
  const star = (field: string) => field === "*" || field.startsWith("*/");
  return {
    minutes,
    hours,
    days,
    months,
    // 7 is Sunday, as 0 is.
    weekdays: [...new Set(weekdays.map((d) => d % 7))].sort((a, b) => a - b),
    daysStar: star(f[2]),
    weekdaysStar: star(f[4]),
  };
}

function parseField(field: string, min: number, max: number, names?: Record<string, number>): number[] | undefined {
  const values = new Set<number>();
  for (const part of field.split(",")) {
    let step = 1;
    let range = part;
    const slash = part.indexOf("/");
    if (slash >= 0) {
      const s = atoi(part.slice(slash + 1));
      if (s === undefined || s <= 0) return undefined;
      step = s;
      range = part.slice(0, slash);
    }
    let lo = min;
    let hi = max;
    if (range !== "*") {
      const dash = range.indexOf("-");
      if (dash >= 0) {
        const a = value(range.slice(0, dash), names);
        const b = value(range.slice(dash + 1), names);
        if (a === undefined || b === undefined) return undefined;
        [lo, hi] = [a, b];
      } else {
        const v = value(range, names);
        if (v === undefined) return undefined;
        lo = v;
        hi = slash >= 0 ? max : v;
      }
    }
    if (lo < min || hi > max || lo > hi) return undefined;
    for (let v = lo; v <= hi; v += step) values.add(v);
  }
  return [...values].sort((a, b) => a - b);
}

function value(text: string, names?: Record<string, number>): number | undefined {
  const named = names?.[text.toUpperCase()];
  return named ?? atoi(text);
}

// strconv.Atoi's syntax: an optional sign and decimal digits.
function atoi(text: string): number | undefined {
  return /^[+-]?\d+$/.test(text) ? Number(text) : undefined;
}

// At most this many firings in a day are listed as times, such as "At 09:00, 12:00, and 18:00".
const MAX_LISTED_TIMES = 6;

type Translate = (key: string, options?: Record<string, unknown>) => string;

/**
 * The expression in plain language, in the given language: "At 09:00, Monday through Friday". Undefined for an
 * expression the daemon refuses; the form shows it only once the daemon accepted the same text.
 */
export function describeCron(expr: string, lang: string): string | undefined {
  const sets = parseCron(expr);
  if (!sets) return undefined;
  const fixed = i18n.getFixedT(lang);
  const t: Translate = (key, options) => String(fixed(key as never, options as never));
  const and = (items: string[]) => new Intl.ListFormat(lang, { type: "conjunction" }).format(items);
  const or = (items: string[]) => new Intl.ListFormat(lang, { type: "disjunction" }).format(items);
  return [...timeParts(sets, t, and), ...dayParts(sets, t, and, or)].join(", ");
}

function timeParts(s: CronSets, t: Translate, and: (items: string[]) => string): string[] {
  const allMinutes = s.minutes.length === 60;
  const allHours = s.hours.length === 24;
  if (!allMinutes && !allHours && s.minutes.length * s.hours.length <= MAX_LISTED_TIMES) {
    return [t("cronText.at", { times: and(s.hours.flatMap((h) => s.minutes.map((m) => clock(h, m)))) })];
  }
  const minuteStep = evenStep(s.minutes, 60);
  const hourStep = evenStep(s.hours, 24);
  const minutes = numbers(s.minutes, t, and);
  let first: string;
  if (allMinutes) first = t("cronText.everyMinute");
  else if (minuteStep) first = t("cronText.everyMinutes", { n: minuteStep });
  else if (allHours) return [t("cronText.atMinuteOfEveryHour", { minutes, count: s.minutes.length })];
  else first = t("cronText.atMinute", { minutes, count: s.minutes.length });
  if (allHours) return [first];
  const run = s.hours.length === s.hours[s.hours.length - 1] - s.hours[0] + 1;
  if (run) return [first, t("cronText.betweenHours", { from: clock(s.hours[0], 0), to: clock(s.hours[s.hours.length - 1], 59) })];
  // "Every 15 minutes, every 2 hours" would read as one firing every 2 hours, so a step of hours follows a minute only.
  if (hourStep && !allMinutes && !minuteStep) return [first, t("cronText.everyHours", { n: hourStep })];
  return [first, t("cronText.inHours", { hours: numbers(s.hours, t, and), count: s.hours.length })];
}

function dayParts(s: CronSets, t: Translate, and: (items: string[]) => string, or: (items: string[]) => string): string[] {
  const daysRestricted = s.days.length < 31;
  const weekdaysRestricted = s.weekdays.length < 7;
  const days = () => t("cronText.onDays", { days: numbers(s.days, t, and), count: s.days.length });
  const parts: string[] = [];
  if (s.daysStar || s.weekdaysStar) {
    // One day field is unrestricted by its text, so a day fires when both fields match it.
    if (daysRestricted && weekdaysRestricted) parts.push(days(), t("cronText.ifWeekdays", { days: weekdayList(s.weekdays, t, or) }));
    else if (daysRestricted) parts.push(days());
    else if (weekdaysRestricted) parts.push(onWeekdays(s.weekdays, t, and));
  } else if (daysRestricted && weekdaysRestricted) {
    // Both day fields restricted: either one matching fires the task. When one of them still holds every value, such
    // as `1-31`, it matches every day, and so does the task.
    parts.push(t("cronText.either", { days: days(), weekdays: onWeekdays(s.weekdays, t, and) }));
  }
  if (s.months.length < 12) parts.push(months(s.months, t, and));
  return parts;
}

function onWeekdays(weekdays: number[], t: Translate, and: (items: string[]) => string): string {
  const segments = runs(weekdays);
  if (segments.length === 1 && segments[0].length >= 3) return weekdayRange(segments[0], t);
  return t("cronText.onWeekdays", {
    days: and(segments.flatMap((r) => (r.length >= 3 ? [weekdayRange(r, t)] : r.map((d) => t(`cronText.weekdaysOn.${d}`))))),
  });
}

function weekdayList(weekdays: number[], t: Translate, or: (items: string[]) => string): string {
  return or(runs(weekdays).flatMap((r) => (r.length >= 3 ? [weekdayRange(r, t)] : r.map((d) => t(`cronText.weekdays.${d}`)))));
}

function weekdayRange(run: number[], t: Translate): string {
  return t("cronText.range", { from: t(`cronText.weekdays.${run[0]}`), to: t(`cronText.weekdays.${run[run.length - 1]}`) });
}

function months(values: number[], t: Translate, and: (items: string[]) => string): string {
  const segments = runs(values);
  const name = (m: number) => t(`cronText.months.${m}`);
  if (segments.length === 1 && segments[0].length >= 3) {
    return t("cronText.range", { from: name(segments[0][0]), to: name(segments[0][segments[0].length - 1]) });
  }
  return t("cronText.inMonths", {
    months: and(segments.flatMap((r) => (r.length >= 3 ? [t("cronText.run", { from: name(r[0]), to: name(r[r.length - 1]) })] : r.map(name)))),
  });
}

/** Numbers as a list, three or more in a row as a run: "1 through 7 and 15". */
function numbers(values: number[], t: Translate, and: (items: string[]) => string): string {
  return and(runs(values).flatMap((r) => (r.length >= 3 ? [t("cronText.run", { from: r[0], to: r[r.length - 1] })] : r.map(String))));
}

function runs(values: number[]): number[][] {
  const out: number[][] = [];
  for (const v of values) {
    const last = out[out.length - 1];
    if (last && v === last[last.length - 1] + 1) last.push(v);
    else out.push([v]);
  }
  return out;
}

/** N when the values are 0, N, 2N, … through the whole field and N, above 1, divides its size; undefined otherwise. */
function evenStep(values: number[], size: number): number | undefined {
  if (values.length < 2 || values[0] !== 0) return undefined;
  const n = values[1];
  if (n < 2 || size % n !== 0 || values.length !== size / n) return undefined;
  return values.every((v, i) => v === i * n) ? n : undefined;
}

function clock(hour: number, minute: number): string {
  return `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
}
