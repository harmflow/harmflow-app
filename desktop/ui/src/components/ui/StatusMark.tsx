import { useTranslation } from "react-i18next";
import type { ExecutionStatus } from "../../api/types";
import { Icon } from "../Icon";
import { cx, statusIcon, statusTone } from "./styleHelpers";

/** A large round status mark for history rows. */
export function StatusMark({ status }: { status: ExecutionStatus }) {
  const { t } = useTranslation();
  const tone = statusTone(status);
  return (
    <span className={cx("status-mark", `status-mark--${tone}`)} title={t(`status.${status}`)}>
      <Icon name={statusIcon(status)} size={16} />
      <span className="sr-only">{t(`status.${status}`)}</span>
    </span>
  );
}
