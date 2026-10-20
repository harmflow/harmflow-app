// Locale-aware formatting of dates, times, durations, and sizes (FS-012 FR-006).

const SECOND = 1000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** A compact duration such as `45s`, `8m 32s`, `1h 04m`, or `2d 3h`. Negative values read as zero. */
export function formatDuration(ms: number): string {
  const total = Math.max(0, Math.floor(ms / SECOND));
  const d = Math.floor(total / 86400);
  const h = Math.floor((total % 86400) / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${String(m).padStart(2, "0")}m`;
  if (m > 0) return `${m}m ${String(s).padStart(2, "0")}s`;
  return `${s}s`;
}

/** Elapsed time from start to end, or to now while the end is unknown. */
export function elapsedMs(start?: string | null, end?: string | null, now: number = Date.now()): number {
  if (!start) return 0;
  const from = Date.parse(start);
  const to = end ? Date.parse(end) : now;
  if (Number.isNaN(from) || Number.isNaN(to)) return 0;
  return Math.max(0, to - from);
}

export function formatElapsed(start?: string | null, end?: string | null, now?: number): string {
  return formatDuration(elapsedMs(start, end, now));
}

export function formatTime(iso: string | undefined | null, locale: string, withSeconds = true): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat(locale, {
    hour: "2-digit",
    minute: "2-digit",
    second: withSeconds ? "2-digit" : undefined,
  }).format(date);
}

export function formatDateTime(iso: string | undefined | null, locale: string, timeZone?: string): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  try {
    return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short", timeZone }).format(date);
  } catch {
    return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(date);
  }
}

const RELATIVE_UNITS: Array<[Intl.RelativeTimeFormatUnit, number]> = [
  ["day", DAY],
  ["hour", HOUR],
  ["minute", MINUTE],
  ["second", SECOND],
];

/** "3 h ago", "in 18 min" — past or future, in the locale's words. */
export function formatRelative(iso: string | undefined | null, locale: string, now: number = Date.now()): string {
  if (!iso) return "—";
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "—";
  const diff = then - now;
  const abs = Math.abs(diff);
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: "auto", style: "short" });
  if (abs < 45 * SECOND) return rtf.format(0, "second");
  for (const [unit, size] of RELATIVE_UNITS) {
    if (abs >= size || unit === "second") {
      return rtf.format(Math.round(diff / size), unit);
    }
  }
  return rtf.format(0, "second");
}

export function formatNumber(value: number, locale: string, maximumFractionDigits = 0): string {
  return new Intl.NumberFormat(locale, { maximumFractionDigits }).format(value);
}

export function formatBytes(bytes: number | undefined | null, locale: string): string {
  if (bytes === undefined || bytes === null || !Number.isFinite(bytes)) return "—";
  const units = ["byte", "kilobyte", "megabyte", "gigabyte", "terabyte"] as const;
  let value = Math.max(0, bytes);
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i++;
  }
  return new Intl.NumberFormat(locale, {
    style: "unit",
    unit: units[i],
    unitDisplay: "short",
    maximumFractionDigits: value < 10 && i > 0 ? 1 : 0,
  }).format(value);
}

export function shortId(id: string | undefined | null): string {
  if (!id) return "—";
  return id.length > 14 ? id.slice(0, 14) : id;
}
