import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { IconButton } from "./IconButton";
import { Overlay } from "./Overlay";
import { cx } from "./styleHelpers";

export function Modal({
  title,
  onClose,
  children,
  footer,
  wide,
}: {
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <Overlay className="modal" onClose={onClose}>
      <div className={cx("modal__card", wide && "modal__card--wide")}>
        <header className="modal__header">
          <h2>{title}</h2>
          <IconButton icon="x" label={t("common.close")} onClick={onClose} />
        </header>
        <div className="modal__body">{children}</div>
        {footer ? <footer className="modal__footer">{footer}</footer> : null}
      </div>
    </Overlay>
  );
}
