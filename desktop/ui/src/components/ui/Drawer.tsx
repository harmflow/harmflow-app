import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { IconButton } from "./IconButton";
import { Overlay } from "./Overlay";

/** A panel at the window's right edge, over the screen, that closes as `Modal` does. */
export function Drawer({ label, onClose, children }: { label: string; onClose: () => void; children: ReactNode }) {
  const { t } = useTranslation();
  return (
    <Overlay className="drawer" label={label} onClose={onClose}>
      <div className="drawer__card">
        <div className="drawer__close">
          <IconButton icon="x" label={t("common.close")} onClick={onClose} />
        </div>
        {children}
      </div>
    </Overlay>
  );
}
