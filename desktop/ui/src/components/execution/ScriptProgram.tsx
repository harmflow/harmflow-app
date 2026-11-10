import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Execution } from "../../api/types";
import { Chip, CopyButton, EmptyState, KeyValue } from "../ui";

/** The program as it ran, its working directory and variables, and how it ended. */
export function ScriptProgram({ detail, active }: { detail?: Execution; active: boolean }) {
  const { t } = useTranslation();
  const s = detail?.script;
  if (!s) return <EmptyState icon="prompt" title={t("executions.detail.script.notStarted")} />;
  const env = Object.entries(s.environment ?? {});
  const failed = s.signal !== undefined || (s.exitCode !== undefined && s.exitCode !== 0);
  const status =
    s.exitCode === undefined ? (
      <Chip tone={active ? "cyan" : "neutral"}>{active ? t("executions.detail.script.running") : "—"}</Chip>
    ) : (
      <Chip tone={failed ? "danger" : "success"} icon={failed ? "x" : "check"}>
        {s.signal ? t("executions.detail.script.signal", { signal: s.signal }) : t("executions.detail.script.exitCode", { code: s.exitCode })}
      </Chip>
    );
  // A row that does not apply to the program's form is left out rather than shown empty.
  const rows: [string, ReactNode][] = [
    [t("executions.detail.script.exitStatus"), status],
    [t("executions.detail.script.program"), <span className="mono">{s.program}</span>],
  ];
  if (s.command?.length) rows.push([t("executions.detail.script.command"), <ArgList args={s.command} />]);
  if (s.interpreter?.length) rows.push([t("executions.detail.script.interpreter"), <ArgList args={s.interpreter} />]);
  if (s.scriptPath) rows.push([t("executions.detail.script.scriptPath"), <span className="mono">{s.scriptPath}</span>]);
  rows.push([t("executions.detail.script.workingDirectory"), <span className="mono">{s.workingDirectory}</span>]);
  if (env.length) {
    rows.push([t("executions.detail.script.environment"), <span className="mono">{env.map(([k, v]) => `${k}=${v}`).join("\n")}</span>]);
  }
  return (
    <div className="results">
      <KeyValue items={rows} />
      {s.source ? (
        <div className="results__block">
          <h4>{t("executions.detail.script.source")}</h4>
          <div className="codebox">
            <div className="codebox__tools">
              <CopyButton text={s.source} />
            </div>
            <pre className="mono">{s.source}</pre>
          </div>
        </div>
      ) : null}
      <p className="muted small">{t("executions.detail.script.noHarness")}</p>
    </div>
  );
}

/** An argument list, one argument per line, so an argument with spaces reads as one. */
function ArgList({ args }: { args: string[] }) {
  return (
    <ol className="arg-list mono">
      {args.map((a, i) => (
        <li key={i}>{a === "" ? '""' : a}</li>
      ))}
    </ol>
  );
}
