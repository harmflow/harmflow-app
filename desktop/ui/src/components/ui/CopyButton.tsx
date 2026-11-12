import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { IconButton } from "./IconButton";

export function CopyButton({ text }: { text: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(id);
  }, [copied]);
  return (
    <IconButton
      icon={copied ? "check" : "copy"}
      label={copied ? t("common.copied") : t("common.copy")}
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(() => setCopied(true));
      }}
    />
  );
}
