import { useEffect, useState } from "react";

/**
 * The current time, refreshed every interval — for elapsed times and "in 18 min" labels. A null interval stops the
 * clock. Each tick renders the calling component again, so a fast clock belongs in the smallest one that shows it.
 */
export function useNow(intervalMs: number | null = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (intervalMs === null) return;
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

/** Runs fn when the value stays unchanged for delay ms. */
export function useDebounced<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(id);
  }, [value, delayMs]);
  return debounced;
}

/** Receives, under a key of the setting's own, whether a setting keeps a draft: a value changed and not yet confirmed. */
export type DraftReport = (key: string, held: boolean) => void;

/** Reports whether a setting holds a draft, and that it holds none once the setting is gone. `report` must be stable. */
export function useDraft(report: DraftReport | undefined, key: string, held: boolean) {
  useEffect(() => {
    if (!report) return;
    report(key, held);
    return () => report(key, false);
  }, [report, key, held]);
}
