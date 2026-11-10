import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../../api/client";
import type { ExecutionOutput } from "../../api/types";
import { formatBytes } from "../../lib/format";
import { CopyButton, EmptyState } from "../ui";

const RAW_POLL_MS = 2000;

/** Reads the capture from the daemon, and again every few seconds while the execution runs. */
export function RawOutput({ executionId, active }: { executionId: string; active: boolean }) {
  const [output, setOutput] = useState<ExecutionOutput>();
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = async () => {
      // An unreachable daemon keeps the last capture shown; the next read retries.
      const next = await api.executionOutput(executionId).catch(() => undefined);
      if (stopped) return;
      if (next) setOutput(next);
      if (active) timer = setTimeout(() => void load(), RAW_POLL_MS);
    };
    void load();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [executionId, active]);
  return <RawCaptures output={output} />;
}

/** What the harness printed, attempt by attempt, as the daemon captured and redacted it. */
export function RawCaptures({ output }: { output?: ExecutionOutput }) {
  const { t, i18n } = useTranslation();
  const ref = useRef<HTMLPreElement | null>(null);
  // Follows the end of the output until the user scrolls up to read.
  const following = useRef(true);
  const captures = (output?.attempts ?? []).filter((c) => c.text !== "");
  const text = captures.map((c) => c.text).join("\n");
  useEffect(() => {
    if (ref.current && following.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [text]);
  if (!output) return <EmptyState icon="terminal" title={t("common.loading")} />;
  if (output.logExpired) return null; // the banner above says why
  if (captures.length === 0) return <EmptyState icon="terminal" title={t("executions.detail.noRaw")} />;
  const several = output.attempts.length > 1;
  return (
    <div className="codebox codebox--tall">
      <div className="codebox__tools">
        <CopyButton text={text} />
      </div>
      <pre
        className="mono"
        ref={ref}
        onScroll={(e) => {
          const el = e.currentTarget;
          following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
      >
        {captures.map((c) => (
          <span key={c.attempt} className="raw-attempt">
            {several ? (
              <span className="raw-note">
                {t("executions.detail.rawAttempt", {
                  number: c.attempt,
                  harness: t(`harness.names.${c.harness}` as never, { defaultValue: c.harness }) as string,
                })}
              </span>
            ) : null}
            {c.offset > 0 ? (
              <span className="raw-note">
                {t("executions.detail.rawTruncated", {
                  shown: formatBytes(c.size - c.offset, i18n.language),
                  size: formatBytes(c.size, i18n.language),
                })}
              </span>
            ) : null}
            {c.text}
          </span>
        ))}
      </pre>
    </div>
  );
}
