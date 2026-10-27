// Creating and editing a task through the form or the YAML editor. Both are views of one definition, validated by the
// one schema the daemon applies; the daemon is the only writer.
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, api } from "../api/client";
import { native } from "../api/native";
import type { Catalog, Task, TaskInput, ValidationResult } from "../api/types";
import { daemonText } from "../lib/daemonText";
import { useDebounced } from "../lib/hooks";
import type { Doc } from "../lib/taskForm";
import { actions, getState, useStore } from "../state/store";
import { Icon } from "./Icon";
import { tipBelow, useTooltip } from "./Shell";
import { TaskForm } from "./TaskForm";
import { toast } from "./toast";
import { Banner, Button, Chip, Tabs, cx } from "./ui";
import { YamlEditor } from "./YamlEditor";

type EditorTab = "form" | "yaml";

const SAVE_SHORTCUT = "⌘S";

/** Asks whether the editor may be left, discarding its unsaved changes; resolves true to leave. */
export type LeaveCheck = () => Promise<boolean>;

export interface TaskEditorProps {
  mode: "create" | "edit";
  /** For edit: the task as loaded. */
  task?: Task;
  /** For create: the template document. */
  initial?: Doc;
  onClose: () => void;
  onSaved: (task: Task) => void;
  /** Receives the question to ask before leaving while the editor holds unsaved changes, and null once it holds none. */
  onLeaveCheck?: (check: LeaveCheck | null) => void;
}

/** YAML for a document the daemon could not serialize; JSON is valid YAML, so nothing is lost. */
function fallbackYaml(doc: Doc): string {
  return JSON.stringify(doc, null, 2) + "\n";
}

export function TaskEditor({ mode, task, initial, onClose, onSaved, onLeaveCheck }: TaskEditorProps) {
  const { t } = useTranslation();
  const catalog = useStore((s) => s.catalog);
  const harnesses = useStore((s) => s.harnesses);
  const externalChange = useStore((s) => (task ? s.externalChanges[task.id] : undefined));
  const openedAtEvent = useRef(externalChange ?? 0);

  const [tab, setTab] = useState<EditorTab>("form");
  const [doc, setDoc] = useState<Doc>(() => (task?.document as Doc) ?? initial ?? {});
  const [yaml, setYaml] = useState<string>(() => task?.yaml ?? (initial ? fallbackYaml(initial) : ""));
  const [baseRevision, setBaseRevision] = useState(task?.revision);
  const [validation, setValidation] = useState<ValidationResult | null>(task?.validation ?? null);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [externalSeen, setExternalSeen] = useState(false);
  // Set on a save's entry, before `saving` renders: two ⌘S presses in one moment send one request.
  const inFlight = useRef(false);
  const { tipped, tooltip } = useTooltip(tipBelow);

  const showExternal = !!task && externalChange !== undefined && externalChange > openedAtEvent.current && !externalSeen;

  // A harness that finds the user's skills per workspace, as Codex does, offers the ones it has in the task's workspace:
  // the catalog is asked for it, and the one already loaded stands in until it answers.
  const spec = doc.spec as { workspace?: { path?: unknown } } | undefined;
  const workspace = useDebounced(typeof spec?.workspace?.path === "string" ? spec.workspace.path : "", 500);
  const [workspaceCatalog, setWorkspaceCatalog] = useState<Catalog>();
  useEffect(() => {
    let cancelled = false;
    api.catalog(workspace || undefined).then(
      (c) => !cancelled && setWorkspaceCatalog(c),
      () => {}, // the catalog already loaded stays
    );
    return () => {
      cancelled = true;
    };
  }, [workspace]);

  const input = (): TaskInput => (tab === "form" ? { document: doc, baseRevision } : { yaml, baseRevision });

  // Every way out of an editor with unsaved changes asks first, except Save and the editor's own Discard; one with
  // none closes at once. The shell owns the ways out, so it holds the question while there is one.
  useEffect(() => {
    if (!dirty || !onLeaveCheck) return;
    onLeaveCheck(() => native.confirm(t("tasks.editor.leaveConfirm"), t("tasks.editor.leaveTitle"), t("tasks.editor.discard"), t("common.cancel")));
    return () => onLeaveCheck(null);
  }, [dirty, onLeaveCheck, t]);

  // Live validation of whatever the current tab holds.
  const probe = useDebounced(tab === "form" ? JSON.stringify(doc) : yaml, 500);
  useEffect(() => {
    if (!dirty) return;
    let cancelled = false;
    api
      .validateTask(tab === "form" ? { document: doc } : { yaml })
      .then((res) => !cancelled && setValidation(res))
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
    // `probe` is the debounced content; doc and yaml are read at that moment.
  }, [probe]);

  const switchTab = async (next: EditorTab) => {
    if (next === tab) return;
    try {
      if (next === "yaml") {
        const res = await api.validateTask({ document: doc });
        setYaml(res.yaml ?? fallbackYaml(doc));
        setValidation(res);
      } else {
        const res = await api.validateTask({ yaml });
        setValidation(res);
        if (!res.document) {
          toast(t("tasks.editor.convertFailed"), "warning");
          return;
        }
        setDoc(res.document as Doc);
      }
      setTab(next);
    } catch (err) {
      toast((err as Error).message, "danger");
    }
  };

  const save = async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    setSaving(true);
    setConflict(false);
    try {
      const saved = mode === "create" ? await api.createTask(input()) : await api.updateTask(task!.id, input());
      toast(t("tasks.editor.saved"), "success");
      setDirty(false);
      setBaseRevision(saved.revision);
      setDoc(saved.document as Doc);
      setYaml(saved.yaml);
      setValidation(saved.validation ?? null);
      openedAtEvent.current = getState().externalChanges[saved.id] ?? openedAtEvent.current;
      void actions.refreshTasks();
      onSaved(saved);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 409 && mode === "edit") setConflict(true);
        if (err.validation) setValidation(err.validation);
        if (err.status !== 422 && err.status !== 409) toast(err.message, "danger");
        if (err.status === 409 && mode === "create") toast(err.message, "danger");
      } else {
        toast((err as Error).message, "danger");
      }
    } finally {
      inFlight.current = false;
      setSaving(false);
    }
  };

  // ⌘S saves as the Save button does, from either tab and any focus, the YAML editor included, whose keymap lets the
  // key through. A modal dialog keeps the keyboard. The listener lives as long as the editor, and calls the latest save,
  // with the document and revision of the last render. Linux and Windows would check ctrlKey.
  const saveRef = useRef(save);
  saveRef.current = save;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!e.metaKey || e.ctrlKey || e.altKey || e.shiftKey || e.repeat) return;
      if (e.key.toLowerCase() !== "s") return;
      if (document.querySelector('[aria-modal="true"]')) return;
      e.preventDefault();
      void saveRef.current();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const reloadFromDisk = async () => {
    if (!task) return;
    const fresh = await api.task(task.id);
    setDoc(fresh.document as Doc);
    setYaml(fresh.yaml);
    setBaseRevision(fresh.revision);
    setValidation(fresh.validation ?? null);
    setDirty(false);
    setConflict(false);
    setExternalSeen(true);
    openedAtEvent.current = externalChange ?? openedAtEvent.current;
  };

  const errors = validation?.errors ?? [];
  const warnings = validation?.warnings ?? [];
  const errorPaths = useMemo(() => new Set(errors.map((e) => e.path ?? "").filter(Boolean)), [errors]);
  const errorLines = useMemo(() => errors.map((e) => e.line).filter((l): l is number => typeof l === "number"), [errors]);
  const name = (doc.metadata as { name?: string } | undefined)?.name ?? task?.name ?? "";

  return (
    <div className="editor">
      <header className="editor__header">
        <div>
          <h2 className="editor__title">{mode === "create" ? t("tasks.editor.newTitle") : t("tasks.editor.editTitle", { name })}</h2>
          {dirty ? <span className="editor__dirty">{t("tasks.editor.unsaved")}</span> : null}
        </div>
        <div className="editor__actions">
          {validation ? (
            validation.valid ? (
              <Chip tone="success" icon="check">
                {t("tasks.editor.valid")}
              </Chip>
            ) : (
              <Chip tone="danger" icon="x">
                {t("tasks.editor.invalid", { count: errors.length })}
              </Chip>
            )
          ) : null}
          <Button variant="ghost" onClick={onClose}>
            {dirty ? t("tasks.editor.discard") : t("common.close")}
          </Button>
          <Button
            variant="primary"
            icon="check"
            disabled={saving}
            aria-keyshortcuts="Meta+S"
            onClick={() => void save()}
            {...tipped(t("common.save"), SAVE_SHORTCUT)}
          >
            {saving ? t("common.saving") : t("common.save")}
          </Button>
        </div>
        {tooltip}
      </header>

      {task?.running ? (
        <Banner tone="blue" icon="clock">
          {t("tasks.editor.runningNote")}
        </Banner>
      ) : null}
      {conflict ? (
        <Banner
          tone="danger"
          icon="alert"
          actions={
            <>
              <Button size="sm" onClick={() => void reloadFromDisk()}>
                {t("tasks.editor.reloadFromDisk")}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setConflict(false)}>
                {t("tasks.editor.keepEditing")}
              </Button>
            </>
          }
        >
          <strong>{t("tasks.editor.conflictTitle")}.</strong> {t("tasks.editor.conflict")}
        </Banner>
      ) : null}
      {showExternal && !conflict ? (
        <Banner
          tone="warning"
          icon="alert"
          actions={
            <>
              <Button size="sm" onClick={() => void reloadFromDisk()}>
                {t("tasks.editor.reloadFromDisk")}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setExternalSeen(true)}>
                {t("tasks.editor.keepEditing")}
              </Button>
            </>
          }
        >
          {t("tasks.editor.externalChange")}
        </Banner>
      ) : null}

      <Tabs
        tabs={[
          { key: "form", label: t("tasks.editor.formTab"), icon: "list" },
          { key: "yaml", label: t("tasks.editor.yamlTab"), icon: "code" },
        ]}
        value={tab}
        onChange={(k) => void switchTab(k)}
      />

      <div className="editor__body">
        <div className={cx("editor__main", tab === "yaml" && "editor__main--yaml")}>
          {tab === "form" ? (
            <TaskForm
              doc={doc}
              mode={mode}
              catalog={workspaceCatalog ?? catalog}
              harnesses={harnesses}
              errorPaths={errorPaths}
              onChange={(next) => {
                setDoc(next);
                setDirty(true);
              }}
            />
          ) : (
            <>
              <p className="hint-line">
                <Icon name="shield" size={13} /> {t("tasks.editor.yamlHint")}
              </p>
              <YamlEditor
                value={yaml}
                errorLineNumbers={errorLines}
                onChange={(v) => {
                  setYaml(v);
                  setDirty(true);
                }}
              />
            </>
          )}
        </div>
        <aside className="editor__side">
          <ValidationPanel validation={validation} />
          {warnings.length ? (
            <div className="validation">
              <h3>{t("tasks.editor.warningsTitle")}</h3>
              <ul>
                {warnings.map((w, i) => (
                  <li key={i} className="validation__item validation__item--warning">
                    <Icon name="shield" size={14} />
                    <span>
                      {w.code ? <strong>{t(`warnings.${w.code}` as never, { defaultValue: w.code }) as string}: </strong> : null}
                      {daemonText(w)}
                      {w.path ? <span className="mono muted"> {w.path}</span> : null}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </aside>
      </div>
    </div>
  );
}

export function ValidationPanel({ validation }: { validation: ValidationResult | null }) {
  const { t } = useTranslation();
  const errors = validation?.errors ?? [];
  return (
    <div className="validation">
      <h3>{t("tasks.editor.errorsTitle")}</h3>
      {!validation ? <p className="muted">—</p> : null}
      {validation && errors.length === 0 ? (
        <p className="validation__ok">
          <Icon name="check" size={14} /> {t("tasks.editor.valid")}
        </p>
      ) : null}
      <ul>
        {errors.map((e, i) => (
          <li key={i} className="validation__item">
            <Icon name="x" size={14} />
            <span>
              {daemonText(e)}
              {e.line ? <span className="mono muted"> — {t("tasks.editor.line", { line: e.line, column: e.column ?? 1 })}</span> : null}
              {e.path ? <span className="mono muted"> {e.path}</span> : null}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}
