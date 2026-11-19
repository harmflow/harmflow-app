// The Templates view of the Tasks screen: HarmFlow's templates and the user's, in the daemon's order, with their
// commands — every one carried out by the daemon. A built-in template can be viewed and duplicated, and nothing else;
// one of the user's is edited, duplicated, and deleted, and a new one starts empty, of a type the catalog lists.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import { native } from "../api/native";
import type { Template } from "../api/types";
import { Icon } from "../components/Icon";
import { toast } from "../components/toast";
import { Banner, Button, Chip, EmptyState, Field, IconButton, Modal, Select, TextArea, TextInput, cx } from "../components/ui";
import { daemonText } from "../lib/daemonText";
import { errorMessage } from "../lib/errorMessage";
import { slugify } from "../lib/taskForm";
import { toneClass, typeGlyph } from "../lib/taskType";
import { localized, templateFileName } from "../lib/templates";
import { actions, useStore } from "../state/store";
import type { TaskView } from "./taskView";
import { TasksViewSwitch } from "./TasksViewSwitch";

export function TemplateList({ onView }: { onView: (v: TaskView) => void }) {
  const { t, i18n } = useTranslation();
  const templates = useStore((s) => s.templates) ?? [];
  const [creating, setCreating] = useState(false);

  const open = (tpl: Template) => onView({ kind: "template", id: tpl.id });
  const nameOf = (tpl: Template) => (tpl.validation.valid ? localized(tpl.name, i18n.language) : templateFileName(tpl.path));

  const duplicate = async (tpl: Template) => {
    try {
      const copy = await api.duplicateTemplate(tpl.id);
      void actions.refreshTemplates();
      onView({ kind: "template", id: copy.id });
    } catch (err) {
      toast(errorMessage(err), "danger");
    }
  };

  // Deleting a template changes no task created from it: each task holds its own copy of the template's values.
  const remove = async (tpl: Template) => {
    if (!(await native.confirm(t("tasks.templates.deleteConfirm"), t("tasks.templates.deleteTitle", { name: nameOf(tpl) }), t("common.delete"), t("common.cancel")))) return;
    try {
      await api.deleteTemplate(tpl.id);
      toast(t("tasks.templates.deleted"), "success");
      await actions.refreshTemplates();
    } catch (err) {
      toast(errorMessage(err), "danger");
    }
  };

  return (
    <div className="screen">
      <div className="filters">
        <TasksViewSwitch value="templates" onView={onView} />
        <div className="filters__spacer" />
        <Button variant="primary" icon="plus" onClick={() => setCreating(true)}>
          {t("tasks.templates.newTemplate")}
        </Button>
      </div>

      {templates.length === 0 ? (
        <div className="panel panel--empty">
          <EmptyState icon="layers" title={t("tasks.templates.none")} />
        </div>
      ) : (
        <div className="task-list">
          {templates.map((tpl) => {
            const builtin = tpl.source === "builtin";
            const glyph = typeGlyph(tpl.type);
            return (
              <article key={tpl.id} data-template={tpl.id} className="task-row template-row">
                <div className="task-row__main" onClick={() => open(tpl)}>
                  <div className="task-row__title">
                    <span className={cx("task-row__glyph", toneClass(glyph.tone))}>
                      <Icon name={glyph.icon} size={18} />
                    </span>
                    <strong>{nameOf(tpl)}</strong>
                    <span className="mono muted small">{tpl.id}</span>
                    <Chip tone={builtin ? "neutral" : "violet"}>{t(`tasks.templates.sources.${tpl.source}`)}</Chip>
                    {tpl.validation.valid ? null : (
                      <Chip tone="danger" icon="alert" title={tpl.validation.errors.map((e) => daemonText(e)).join("\n")}>
                        {t("tasks.editor.invalid", { count: tpl.validation.errors.length })}
                      </Chip>
                    )}
                  </div>
                  {tpl.validation.valid ? <p className="task-row__desc">{localized(tpl.description, i18n.language)}</p> : null}
                  {tpl.type ? (
                    <div className="task-row__meta">
                      <span className="meta">{t(`tasks.types.${tpl.type}`, tpl.type)}</span>
                    </div>
                  ) : null}
                </div>
                <div className="task-row__actions template-row__actions">
                  {builtin ? (
                    <IconButton icon="file" label={t("tasks.templates.view")} onClick={() => open(tpl)} />
                  ) : (
                    <IconButton icon="pencil" label={t("common.edit")} onClick={() => open(tpl)} />
                  )}
                  <IconButton icon="copy" label={t("common.duplicate")} disabled={!tpl.validation.valid} onClick={() => void duplicate(tpl)} />
                  {builtin ? null : <IconButton icon="trash" label={t("common.delete")} onClick={() => void remove(tpl)} />}
                </div>
              </article>
            );
          })}
        </div>
      )}

      {creating ? (
        <NewTemplateDialog
          onClose={() => setCreating(false)}
          onCreated={(tpl) => {
            setCreating(false);
            onView({ kind: "template", id: tpl.id });
          }}
        />
      ) : null}
    </div>
  );
}

/**
 * The dialog that starts an empty template of the user's: its task type, among the catalog's, its name and what it is
 * for, and an identifier proposed from the name until the user writes one. The daemon writes it.
 */
function NewTemplateDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (tpl: Template) => void }) {
  const { t } = useTranslation();
  const types = useStore((s) => s.catalog?.taskTypes ?? []);
  const [type, setType] = useState(types[0]?.id ?? "");
  const [name, setName] = useState("");
  const [id, setId] = useState("");
  const [idTouched, setIdTouched] = useState(false);
  const [description, setDescription] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const create = async () => {
    setBusy(true);
    setError(null);
    try {
      const tpl = await api.createTemplate({ id, name: name.trim(), description: description.trim(), type });
      void actions.refreshTemplates();
      onCreated(tpl);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={t("tasks.templates.newTemplate")}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" icon="plus" disabled={!type || !name.trim() || !description.trim() || !id || busy} onClick={() => void create()}>
            {t("tasks.templates.create")}
          </Button>
        </>
      }
    >
      <p className="muted">{t("tasks.templates.newHint")}</p>
      <Field label={t("tasks.fields.type")}>
        <Select value={type} onChange={(e) => setType(e.target.value)}>
          {types.map((ty) => (
            <option key={ty.id} value={ty.id}>
              {t(`tasks.types.${ty.id}`, ty.id)}
            </option>
          ))}
        </Select>
      </Field>
      <Field label={t("tasks.templates.name")}>
        <TextInput
          value={name}
          onChange={(e) => {
            setName(e.target.value);
            if (!idTouched) setId(slugify(e.target.value));
          }}
        />
      </Field>
      <Field label={t("tasks.templates.id")} hint={t("tasks.templates.idHint")}>
        <TextInput
          mono
          value={id}
          onChange={(e) => {
            setIdTouched(true);
            setId(e.target.value);
          }}
        />
      </Field>
      <Field label={t("tasks.templates.description")}>
        <TextArea rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      {error ? (
        <Banner tone="danger" icon="x">
          {error}
        </Banner>
      ) : null}
    </Modal>
  );
}
