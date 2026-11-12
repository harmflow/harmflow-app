import { formatElapsed } from "../../lib/format";
import { useNow } from "../../lib/hooks";

/** The time since start, ticking every second until end is set: only this label renders again, not its screen. */
export function Elapsed({ start, end, locale }: { start?: string | null; end?: string | null; locale: string }) {
  const now = useNow(end ? null : 1000);
  return <>{formatElapsed(start, end, locale, now)}</>;
}
