// The task templates a new task starts from, the same wherever the dashboard offers them: the topbar's New task and ⌘N,
// the Tasks screen, the Overview's empty state, and onboarding. A task saved as a template joins them, after HarmFlow's.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import type { TaskSummary, Template } from "../api/types";
import { errorMessage } from "../lib/errorMessage";
import { slugify } from "../lib/taskForm";
import { toneClass, typeGlyph } from "../lib/taskType";
import { filterTemplates, localized, offeredTemplates, templateSources, templateTypes } from "../lib/templates";
import { actions, useStore } from "../state/store";
import { Icon } from "./Icon";
import { toast } from "./toast";
import { Banner, Button, Chip, Field, Modal, Select, TextArea, TextInput, cx } from "./ui";

/**
 * The catalog of the templates the daemon offers, named and described in the interface's language, with their type
 * and source, and what each needs that this machine lacks: a search over the names and descriptions, and filters by
 * source and by type. A template that needs something can still be picked.
 */
export function TemplateButtons({ onPick, disabled }: { onPick: (template: string) => void; disabled?: boolean }) {
  const { t, i18n } = useTranslation();
  const templates = offeredTemplates(useStore((s) => s.templates));
  const [query, setQuery] = useState("");
  const [source, setSource] = useState("");
  const [type, setType] = useState("");
  if (!templates.length) return <p className="muted">{t("tasks.templates.none")}</p>;
  const shown = filterTemplates(templates, { query, source, type }, i18n.language);
  return (
    <div className="template-catalog">
      <div className="filters">
        <TextInput
          type="search"
          className="filters__search"
          placeholder={t("tasks.templates.searchPlaceholder")}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          aria-label={t("common.search")}
        />
        <Select value={source} onChange={(e) => setSource(e.target.value)} aria-label={t("tasks.templates.source")}>
          <option value="">{t("tasks.templates.anySource")}</option>
          {templateSources.map((s) => (
            <option key={s} value={s}>
              {t(`tasks.templates.sources.${s}`)}
            </option>
          ))}
        </Select>
        <Select value={type} onChange={(e) => setType(e.target.value)} aria-label={t("tasks.templates.type")}>
          <option value="">{t("tasks.templates.anyType")}</option>
          {templateTypes(templates).map((ty) => (
            <option key={ty} value={ty}>
              {t(`tasks.types.${ty}`, ty)}
            </option>
          ))}
        </Select>
      </div>
      {shown.length ? (
        <div className="templates">
          {shown.map((tpl: Template) => {
            const glyph = typeGlyph(tpl.type);
            const needs = tpl.needs;
            return (
              <button key={tpl.id} type="button" className="template" disabled={disabled} onClick={() => onPick(tpl.id)}>
                <span className={cx("template__icon", toneClass(glyph.tone))}>
                  <Icon name={glyph.icon} size={24} />
                </span>
                <strong>{localized(tpl.name, i18n.language)}</strong>
                {tpl.source === "user" ? <Chip tone="violet">{t("tasks.templates.yours")}</Chip> : null}
                <span className="muted">{localized(tpl.description, i18n.language)}</span>
                <span className="template__meta">
                  {t(`tasks.types.${tpl.type}`, tpl.type ?? "")} · {t(`tasks.templates.sources.${tpl.source}`)}
                </span>
                {needs && (!needs.harnessReady || needs.missing.length > 0) && (
                  <ul className="template__needs">
                    {!needs.harnessReady && (
                      <li>{t("tasks.templates.harnessNotReady", { harness: t(`harness.names.${needs.harness}`) })}</li>
                    )}
                    {needs.missing.map((m) => (
                      <li key={`${m.kind}:${m.name}`}>
                        {t(m.kind === "agent" ? "tasks.templates.missingAgent" : "tasks.templates.missingSkill", { name: m.name })}
                      </li>
                    ))}
                  </ul>
                )}
              </button>
            );
          })}
        </div>
      ) : (
        <p className="muted">{t("tasks.templates.noMatch")}</p>
      )}
    </div>
  );
}

/** The dialog that starts a new task from a template. */
export function TemplatePicker({ onPick, onClose }: { onPick: (template: string) => void; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <Modal title={t("tasks.templates.title")} onClose={onClose}>
      <TemplateButtons onPick={onPick} />
    </Modal>
  );
}

/**
 * The dialog that saves a task as a template of the user's: it asks for the template's name and what it is for, and
 * proposes an identifier from the name, as the task editor does, until the user writes one. The daemon writes it.
 */
export function SaveAsTemplateDialog({ task, onClose }: { task: TaskSummary; onClose: () => void }) {
  const { t } = useTranslation();
  const [name, setName] = useState(task.name);
  const [description, setDescription] = useState(task.description ?? "");
  const [id, setId] = useState(slugify(task.name));
  const [idTouched, setIdTouched] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.saveAsTemplate(task.id, { id, name: name.trim(), description: description.trim() });
      toast(t("tasks.templates.saved", { name: name.trim() }));
      void actions.refreshTemplates();
      onClose();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={t("tasks.templates.saveTitle", { name: task.name })}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" icon="layers" disabled={!name.trim() || !description.trim() || !id || busy} onClick={() => void save()}>
            {t("tasks.templates.saveAs")}
          </Button>
        </>
      }
    >
      <p className="muted">{t("tasks.templates.saveHint")}</p>
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
