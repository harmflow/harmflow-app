import type { ReactNode } from "react";
import type { Tone } from "../../design/theme";
import { Icon, type IconName } from "../Icon";
import { cx } from "./styleHelpers";

export function Banner({ tone, icon, children, actions }: { tone: Tone; icon?: IconName; children: ReactNode; actions?: ReactNode }) {
  return (
    <div className={cx("banner", `banner--${tone}`)} role={tone === "danger" || tone === "warning" ? "alert" : "status"}>
      {icon ? <Icon name={icon} size={18} /> : null}
      <div className="banner__text">{children}</div>
      {actions ? <div className="banner__actions">{actions}</div> : null}
    </div>
  );
}
