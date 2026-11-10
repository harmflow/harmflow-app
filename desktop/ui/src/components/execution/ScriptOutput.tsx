import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { HarmflowEvent } from "../../api/types";
import { CopyButton, EmptyState, cx } from "../ui";

/** The streams a script.output event's line comes from; a line that names none is stdout's. */
export const SCRIPT_STREAMS = ["stdout", "stderr"] as const;

/** A script's output, line by line, each marked with its stream, as the daemon redacted it. */
export function ScriptOutput({ lines }: { lines: HarmflowEvent[] }) {
  const { t } = useTranslation();
  const ref = useRef<HTMLPreElement | null>(null);
  const following = useRef(true);
  useEffect(() => {
    if (ref.current && following.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [lines.length]);
  if (lines.length === 0) return <EmptyState icon="terminal" title={t("executions.detail.script.noOutput")} />;
  const text = lines.map((e) => e.message ?? "").join("\n");
  return (
    <div className="codebox codebox--tall">
      <div className="codebox__tools">
        <CopyButton text={text} />
      </div>
      <pre
        className="mono script-output"
        ref={ref}
        onScroll={(e) => {
          const el = e.currentTarget;
          following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
      >
        {lines.map((e) => {
          const named = (e.payload as { stream?: string } | undefined)?.stream;
          const stream = SCRIPT_STREAMS.find((s) => s === named) ?? "stdout";
          return (
            <span key={e.id} className={cx("script-line", `script-line--${stream}`)} data-stream={stream}>
              <span className="script-line__stream" aria-label={stream}>
                {t(`executions.detail.script.streams.${stream}`)}
              </span>
              {e.message ?? ""}
              {"\n"}
            </span>
          );
        })}
      </pre>
    </div>
  );
}
