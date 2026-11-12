import { useTranslation } from "react-i18next";
import type { ExecutionStatus } from "../../api/types";
import { Icon } from "../Icon";
import { cx, statusIcon, statusTone } from "./styleHelpers";

/** A status as text and icon, never color alone. */
export function StatusChip({ status, compact }: { status: ExecutionStatus | "scheduled"; compact?: boolean }) {
  const { t } = useTranslation();
  const tone = statusTone(status);
  const active = ["preparing", "running", "testing", "publishing", "switching_harness"].includes(status);
  return (
    <span className={cx("chip", `chip--${tone}`, active && "chip--live", compact && "chip--compact")}>
      {active ? <span className="chip__pulse" aria-hidden="true" /> : <Icon name={statusIcon(status)} size={12} />}
      {t(`status.${status}`)}
    </span>
  );
}
