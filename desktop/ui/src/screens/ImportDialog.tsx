// Importing a task from its YAML, pasted or read from a file, from the task list or from onboarding — carried out by
// the daemon.
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, api } from "../api/client";
import type { Task } from "../api/types";
import { ValidationPanel } from "../components/TaskEditor";
import { toast } from "../components/toast";
import { Banner, Button, Checkbox, KeyValue, Modal, TextArea } from "../components/ui";
import { daemonText } from "../lib/daemonText";
import { getIn } from "../lib/taskForm";
import { actions } from "../state/store";

export function ImportDialog({ onClose, onImported }: { onClose: () => void; onImported: (t: Task) => void }) {
  const { t } = useTranslation();
  const [yaml, setYaml] = useState("");
  const [overwrite, setOverwrite] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  // A script task runs a program with the user's rights: its program is shown before anything is saved, and the
  // daemon saves it disabled.
  const [script, setScript] = useState<{ yaml: string; doc: Record<string, unknown> } | null>(null);
  const fileRef = useRef<HTMLInputElement | null>(null);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      if (script?.yaml !== yaml) {
        const check = await api.validateTask({ yaml });
        const doc = check.document as Record<string, unknown> | undefined;
        if (check.valid && getIn(doc, ["spec", "type"]) === "script") {
          setScript({ yaml, doc: doc! });
          return;
        }
      }
      const task = await api.importTask(yaml, overwrite);
      toast(t("tasks.imported"));
      for (const w of task.warnings) toast(`${t(`warnings.${w.code}`)}: ${daemonText(w)}`, "warning", 6000);
      void actions.refreshTasks();
      onImported(task);
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, (err as Error).message));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={t("tasks.importTitle")}
      onClose={onClose}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" icon="upload" disabled={!yaml.trim() || busy} onClick={() => void submit()}>
            {script?.yaml === yaml ? t("tasks.importScriptConfirm") : t("common.import")}
          </Button>
        </>
      }
    >
      <p className="muted">{t("tasks.importHint")}</p>
      <div className="inline-actions">
        <Button icon="folder" onClick={() => fileRef.current?.click()}>
          {t("tasks.importFile")}
        </Button>
        <input
          ref={fileRef}
          type="file"
          accept=".yaml,.yml,application/yaml,text/yaml"
          hidden
          onChange={async (e) => {
            const file = e.target.files?.[0];
            if (file) setYaml(await file.text());
          }}
        />
        <Checkbox checked={overwrite} onChange={setOverwrite} label={t("tasks.importOverwrite")} />
      </div>
      <TextArea className="mono" rows={14} value={yaml} placeholder={t("tasks.importPaste")} onChange={(e) => setYaml(e.target.value)} />
      {script?.yaml === yaml ? <ScriptImportPreview doc={script.doc} /> : null}
      {error ? (
        error.validation ? (
          <ValidationPanel validation={error.validation} />
        ) : (
          <Banner tone="danger" icon="x">
            {error.message}
          </Banner>
        )
      ) : null}
    </Modal>
  );
}

/** What an imported script task would run — its whole argument list or script, where, with what, and when. */
function ScriptImportPreview({ doc }: { doc: Record<string, unknown> }) {
  const { t } = useTranslation();
  const command = getIn<string[]>(doc, ["spec", "script", "command"]);
  const interpreter = getIn<string[]>(doc, ["spec", "script", "inline", "interpreter"]);
  const source = getIn<string>(doc, ["spec", "script", "inline", "source"]);
  const env = Object.entries(getIn<Record<string, string>>(doc, ["spec", "script", "environment"]) ?? {});
  const schedule = getIn<string>(doc, ["spec", "schedule", "cron"]) ?? getIn<string>(doc, ["spec", "schedule", "interval"]);
  return (
    <div className="import-preview" data-testid="script-import-preview">
      <Banner tone="warning" icon="shield">
        <strong>{t("tasks.importScriptTitle")}</strong> — {t("tasks.importScriptBody")}
      </Banner>
      <KeyValue
        items={[
          [t("executions.detail.script.command"), command ? <span className="mono">{command.join("\n")}</span> : null],
          [t("executions.detail.script.interpreter"), interpreter ? <span className="mono">{interpreter.join("\n")}</span> : null],
          [t("executions.detail.script.workingDirectory"), <span className="mono">{getIn<string>(doc, ["spec", "script", "workingDirectory"]) ?? "~"}</span>],
          [t("executions.detail.script.environment"), env.length ? <span className="mono">{env.map(([k, v]) => `${k}=${v}`).join("\n")}</span> : null],
          [t("tasks.columns.schedule"), <span className="mono">{schedule ?? t("tasks.manualOnly")}</span>],
        ]}
      />
      {source ? (
        <div className="codebox">
          <pre className="mono">{source}</pre>
        </div>
      ) : null}
    </div>
  );
}
