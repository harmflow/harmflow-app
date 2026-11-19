// Importing a template exported from another machine, its YAML pasted or read from a file — carried out by the daemon.
// The first click asks the daemon to check the template as the import would, writing nothing, and shows what it sets:
// its type, its permission profile, and, for a script template, the program it runs. Only a second click, on the same
// text and choice, saves it; closing the dialog saves nothing.
import { type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, api } from "../api/client";
import type { Template } from "../api/types";
import { ValidationPanel } from "../components/TaskEditor";
import { CUSTOM_LISTS } from "../components/TaskForm";
import { toast } from "../components/toast";
import { Banner, Button, Checkbox, KeyValue, Modal, TextArea } from "../components/ui";
import { getIn } from "../lib/taskForm";
import { localized } from "../lib/templates";
import { actions } from "../state/store";
import { ScriptImportPreview } from "./ImportDialog";

export function TemplateImportDialog({ onClose, onImported }: { onClose: () => void; onImported: (tpl: Template) => void }) {
  const { t } = useTranslation();
  const [yaml, setYaml] = useState("");
  const [overwrite, setOverwrite] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  // The template as the daemon would save it, for the text and choice it was checked with.
  const [preview, setPreview] = useState<{ yaml: string; overwrite: boolean; template: Template } | null>(null);
  const fileRef = useRef<HTMLInputElement | null>(null);
  const confirming = preview?.yaml === yaml && preview.overwrite === overwrite;

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      if (!confirming) {
        setPreview({ yaml, overwrite, template: await api.importTemplate({ yaml, overwrite, dryRun: true }) });
        return;
      }
      const tpl = await api.importTemplate({ yaml, overwrite, dryRun: false });
      toast(t("tasks.templates.imported", { name: localized(tpl.name, "en-US") }), "success");
      for (const m of tpl.needs?.missing ?? []) {
        toast(t(m.kind === "agent" ? "tasks.templates.missingAgent" : "tasks.templates.missingSkill", { name: m.name }), "warning", 6000);
      }
      void actions.refreshTemplates();
      onImported(tpl);
    } catch (err) {
      setPreview(null);
      setError(err instanceof ApiError ? err : new ApiError(0, (err as Error).message));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={t("tasks.templates.importTitle")}
      onClose={onClose}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" icon="upload" disabled={!yaml.trim() || busy} onClick={() => void submit()}>
            {confirming ? t("tasks.templates.importConfirm") : t("common.import")}
          </Button>
        </>
      }
    >
      <p className="muted">{t("tasks.templates.importHint")}</p>
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
        <Checkbox checked={overwrite} onChange={setOverwrite} label={t("tasks.templates.importOverwrite")} />
      </div>
      <TextArea className="mono" rows={14} value={yaml} placeholder={t("tasks.importPaste")} onChange={(e) => setYaml(e.target.value)} />
      {confirming ? <TemplateImportPreview template={preview.template} /> : null}
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

/**
 * What an imported template sets before it is saved: its type and its permission profile, with the custom lists it
 * sets; the agents and skills it cites that this machine lacks; and, for a script template, the program it runs, as a
 * script task's import shows it.
 */
function TemplateImportPreview({ template }: { template: Template }) {
  const { t, i18n } = useTranslation();
  const doc = (template.document ?? {}) as Record<string, unknown>;
  const permissions = ["spec", "permissions"];
  const profile = getIn<string>(doc, [...permissions, "profile"]);
  const network = getIn<string>(doc, [...permissions, "network"]);
  const restrict = getIn<boolean>(doc, [...permissions, "restrictToWorkspace"]);
  const lists = CUSTOM_LISTS.flatMap((key) => {
    const values = getIn<string[]>(doc, [...permissions, "custom", key]);
    return values ? [[t(`tasks.fields.${key}`), <span className="mono">{values.join("\n")}</span>] as [string, ReactNode]] : [];
  });
  const missing = template.needs?.missing ?? [];
  return (
    <div className="import-preview" data-testid="template-import-preview">
      <Banner tone="cyan" icon="shield">
        {t("tasks.templates.importPreview")}
      </Banner>
      <KeyValue
        items={[
          [t("tasks.templates.name"), localized(template.name, i18n.language)],
          [t("tasks.templates.id"), <span className="mono">{template.id}</span>],
          [t("tasks.templates.type"), template.type ? t(`tasks.types.${template.type}`, template.type) : null],
          [
            t("tasks.templates.profile"),
            profile
              ? t(`tasks.options.profile.${profile}`, profile)
              : t("tasks.templates.profileDefault", { profile: t("tasks.options.profile.workspace_write") }),
          ],
          ...(network ? [[t("tasks.fields.network"), t(`tasks.options.network.${network}`, network)] as [string, string]] : []),
          ...(restrict === undefined ? [] : [[t("tasks.fields.restrictToWorkspace"), restrict ? t("common.yes") : t("common.no")] as [string, string]]),
          ...lists,
        ]}
      />
      {missing.length ? (
        <Banner tone="warning" icon="alert">
          {t("tasks.templates.importMissing")}
          <ul>
            {missing.map((m) => (
              <li key={`${m.kind}:${m.name}`}>{t(m.kind === "agent" ? "tasks.templates.missingAgent" : "tasks.templates.missingSkill", { name: m.name })}</li>
            ))}
          </ul>
        </Banner>
      ) : null}
      {template.type === "script" ? (
        <ScriptImportPreview doc={doc} title={t("tasks.templates.importScriptTitle")} body={t("tasks.templates.importScriptBody")} />
      ) : null}
    </div>
  );
}
