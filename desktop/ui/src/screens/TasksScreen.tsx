// The task list and its commands: create, edit, duplicate, enable, disable, run, export, import, and delete — every one
// carried out by the daemon.
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import { native } from "../api/native";
import type { Task, TaskSummary, TemplateLanguage } from "../api/types";
import { HarnessLabel } from "../components/Shell";
import { Icon } from "../components/Icon";
import { TaskEditor, type LeaveCheck } from "../components/TaskEditor";
import { TemplatePicker } from "../components/Templates";
import { toast } from "../components/toast";
import { Banner, Button, Chip, EmptyState, IconButton, Select, StatusChip, TextInput, Toggle, cx } from "../components/ui";
import { daemonText } from "../lib/daemonText";
import { formatRelative } from "../lib/format";
import { useNow } from "../lib/hooks";
import type { Doc } from "../lib/taskForm";
import { toneClass, typeGlyph } from "../lib/taskType";
import { actions, useStore } from "../state/store";
import { ImportDialog } from "./ImportDialog";

/** A new task names the template it starts from; its `pick` counts the template picks, so that each one opens an editor of
 * its own. */
export type TaskView = { kind: "list" } | { kind: "new"; template: string; pick?: number } | { kind: "edit"; id: string };

type Filter = "all" | "enabled" | "disabled" | "warnings";

export function TasksScreen({
  view,
  onView,
  onOpenExecution,
  onLeaveCheck,
}: {
  view: TaskView;
  onView: (v: TaskView) => void;
  onOpenExecution: (id: string) => void;
  onLeaveCheck?: (check: LeaveCheck | null) => void;
}) {
  if (view.kind === "new")
    return (
      <NewTask
        key={`${view.template}-${view.pick ?? 0}`}
        template={view.template}
        onDone={() => onView({ kind: "list" })}
        onSaved={(task) => onView({ kind: "edit", id: task.id })}
        onLeaveCheck={onLeaveCheck}
      />
    );
  if (view.kind === "edit") return <EditTask id={view.id} onDone={() => onView({ kind: "list" })} onLeaveCheck={onLeaveCheck} />;
  return <TaskList onView={onView} onOpenExecution={onOpenExecution} />;
}

function NewTask({
  template,
  onDone,
  onSaved,
  onLeaveCheck,
}: {
  template: string;
  onDone: () => void;
  onSaved: (t: Task) => void;
  onLeaveCheck?: (check: LeaveCheck | null) => void;
}) {
  const { i18n } = useTranslation();
  const [initial, setInitial] = useState<Doc | null>(null);
  // The daemon builds the document once per editor, named in the interface's language; a pick that replaces this one
  // mounts an editor of its own, and the answer this one still waits for is dropped.
  useEffect(() => {
    let current = true;
    api.draftTask(template, i18n.language as TemplateLanguage).then(
      (draft) => {
        if (current) setInitial(draft.document as Doc);
      },
      (e: Error) => {
        if (!current) return;
        toast(e.message, "danger");
        onDone();
      },
    );
    return () => {
      current = false;
    };
  }, [template]);
  if (!initial) return null;
  return (
    <div className="screen">
      <TaskEditor mode="create" initial={initial} onClose={onDone} onSaved={onSaved} onLeaveCheck={onLeaveCheck} />
    </div>
  );
}

function EditTask({ id, onDone, onLeaveCheck }: { id: string; onDone: () => void; onLeaveCheck?: (check: LeaveCheck | null) => void }) {
  const [task, setTask] = useState<Task | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    api.task(id).then(setTask, (e: Error) => setError(e.message));
  }, [id]);
  if (error) return <Banner tone="danger" icon="x">{error}</Banner>;
  if (!task) return null;
  return (
    <div className="screen">
      <TaskEditor key={task.id} mode="edit" task={task} onClose={onDone} onSaved={(saved) => setTask(saved)} onLeaveCheck={onLeaveCheck} />
    </div>
  );
}

function TaskList({ onView, onOpenExecution }: { onView: (v: TaskView) => void; onOpenExecution: (id: string) => void }) {
  const { t, i18n } = useTranslation();
  const tasks = useStore((s) => s.tasks);
  const loaded = useStore((s) => s.tasksLoaded);
  const now = useNow(30000);
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const [picker, setPicker] = useState(false);
  const [importing, setImporting] = useState(false);

  const visible = useMemo(() => {
    const q = search.trim().toLowerCase();
    return tasks.filter((task) => {
      if (filter === "enabled" && !task.enabled) return false;
      if (filter === "disabled" && task.enabled) return false;
      if (filter === "warnings" && task.warnings.length === 0 && !task.invalid) return false;
      if (!q) return true;
      return [task.name, task.id, task.description, task.harness, ...(task.repositories ?? [])].some((v) => v?.toLowerCase().includes(q));
    });
  }, [tasks, search, filter]);

  const run = async (task: TaskSummary) => {
    try {
      const res = await api.runTask(task.id);
      const msg = { started: t("tasks.runStarted"), queued: t("tasks.runQueued"), coalesced: t("tasks.runCoalesced"), skipped: t("tasks.runSkipped") }[res.outcome];
      toast(msg, res.outcome === "skipped" ? "warning" : "success");
      void actions.refreshExecutions();
      if (res.outcome === "started" && res.executionId) onOpenExecution(res.executionId);
    } catch (err) {
      toast((err as Error).message, "danger");
    }
  };

  const toggle = async (task: TaskSummary, on: boolean) => {
    try {
      await (on ? api.enableTask(task.id) : api.disableTask(task.id));
      void actions.refreshTasks();
    } catch (err) {
      toast((err as Error).message, "danger");
    }
  };

  const duplicate = async (task: TaskSummary) => {
    try {
      const copy = await api.duplicateTask(task.id);
      void actions.refreshTasks();
      onView({ kind: "edit", id: copy.id });
    } catch (err) {
      toast((err as Error).message, "danger");
    }
  };

  const exportTask = async (task: TaskSummary) => {
    try {
      const yaml = await api.exportTask(task.id);
      if (await native.saveTextFile(`${task.id}.yaml`, yaml)) toast(t("tasks.exported"));
    } catch (err) {
      toast((err as Error).message, "danger");
    }
  };

  const remove = async (task: TaskSummary) => {
    const message = task.running ? `${t("tasks.deleteConfirm")}\n\n${t("tasks.deleteWaits")}` : t("tasks.deleteConfirm");
    if (!(await native.confirm(message, t("tasks.deleteTitle", { name: task.name }), t("common.delete"), t("common.cancel")))) return;
    try {
      const outcome = await api.deleteTask(task.id);
      toast(outcome === "pending" ? t("tasks.deleteWaits") : t("tasks.deleted"), outcome === "pending" ? "warning" : "success");
      void actions.refreshTasks();
    } catch (err) {
      toast((err as Error).message, "danger");
    }
  };

  return (
    <div className="screen">
      <div className="filters">
        <TextInput className="filters__search" placeholder={t("tasks.searchPlaceholder")} value={search} onChange={(e) => setSearch(e.target.value)} aria-label={t("common.search")} />
        <Select value={filter} onChange={(e) => setFilter(e.target.value as Filter)}>
          <option value="all">{t("tasks.filterAll")}</option>
          <option value="enabled">{t("tasks.filterEnabled")}</option>
          <option value="disabled">{t("tasks.filterDisabled")}</option>
          <option value="warnings">{t("tasks.filterWarnings")}</option>
        </Select>
        <div className="filters__spacer" />
        <Button icon="upload" onClick={() => setImporting(true)}>
          {t("common.import")}
        </Button>
        <Button variant="primary" icon="plus" onClick={() => setPicker(true)}>
          {t("header.newTask")}
        </Button>
      </div>

      {loaded && tasks.length === 0 ? (
        <div className="panel panel--empty">
          <EmptyState
            icon="list"
            title={t("tasks.listEmpty")}
            hint={t("tasks.listEmptyHint")}
            action={
              <div className="inline-actions">
                <Button variant="primary" icon="plus" onClick={() => setPicker(true)}>
                  {t("header.newTask")}
                </Button>
                <Button icon="upload" onClick={() => setImporting(true)}>
                  {t("common.import")}
                </Button>
              </div>
            }
          />
        </div>
      ) : (
        <div className="task-list">
          {visible.map((task) => (
            <article key={task.id} className={cx("task-row", !task.enabled && "task-row--disabled")}>
              <div className="task-row__main" onClick={() => onView({ kind: "edit", id: task.id })}>
                <div className="task-row__title">
                  <span className={cx("task-row__glyph", toneClass(typeGlyph(task.type).tone))}>
                    <Icon name={typeGlyph(task.type).icon} size={18} />
                  </span>
                  <strong>{task.name}</strong>
                  <span className="mono muted small">{task.id}</span>
                  {task.running ? <StatusChip status="running" compact /> : null}
                  {task.queued ? <Chip tone="violet" icon="clock">{t("tasks.queuedMarker")}</Chip> : null}
                  {task.deletePending ? <Chip tone="warning" icon="trash">{t("tasks.deletePending")}</Chip> : null}
                  {task.invalid ? <Chip tone="danger" icon="alert" title={t("tasks.invalidOnDisk")}>{t("warnings.external_edit_invalid")}</Chip> : null}
                  {task.warnings.length ? (
                    <Chip tone="warning" icon="shield" title={task.warnings.map((w) => `${t(`warnings.${w.code}`)}: ${daemonText(w)}`).join("\n")}>
                      {t("warnings.count", { count: task.warnings.length })}
                    </Chip>
                  ) : null}
                </div>
                {task.description ? <p className="task-row__desc">{task.description}</p> : null}
                <div className="task-row__meta">
                  <span className="meta">{t(`tasks.types.${task.type}` as never, { defaultValue: task.type }) as string}</span>
                  <span className="meta">
                    <HarnessLabel harness={task.harness} taskType={task.type} />
                    {task.fallbackHarnesses?.length ? (
                      <span className="muted">
                        {" "}
                        <Icon name="switch" size={12} /> {task.fallbackHarnesses.map((h) => t(`harness.names.${h}` as never, { defaultValue: h }) as string).join(", ")}
                      </span>
                    ) : null}
                  </span>
                  <span className="meta mono">
                    <Icon name="clock" size={13} /> {task.schedule ?? t("tasks.manualOnly")}
                    {task.timezone ? <span className="muted"> · {task.timezone}</span> : null}
                  </span>
                  {task.nextFireAt && task.enabled ? (
                    <span className="meta">
                      {t("tasks.columns.nextFire")}: <strong>{formatRelative(task.nextFireAt, i18n.language, now)}</strong>
                    </span>
                  ) : null}
                  {task.repositories?.length ? (
                    <span className="meta mono">
                      <Icon name="repo" size={13} /> {task.repositories.join(", ")}
                    </span>
                  ) : null}
                  {task.lastExecution ? (
                    <button
                      type="button"
                      className="meta link-like"
                      onClick={(e) => {
                        e.stopPropagation();
                        onOpenExecution(task.lastExecution!.id);
                      }}
                    >
                      {t("tasks.columns.last")}: <StatusChip status={task.lastExecution.status} compact />{" "}
                      <span className="muted">{formatRelative(task.lastExecution.endedAt ?? task.lastExecution.createdAt, i18n.language, now)}</span>
                    </button>
                  ) : null}
                </div>
              </div>
              <div className="task-row__actions">
                <Toggle checked={task.enabled} onChange={(on) => void toggle(task, on)} label={task.enabled ? t("common.enabled") : t("common.disabled")} />
                <Button size="sm" icon="play" disabled={!task.enabled} onClick={() => void run(task)}>
                  {t("common.runNow")}
                </Button>
                <IconButton icon="pencil" label={t("common.edit")} onClick={() => onView({ kind: "edit", id: task.id })} />
                <IconButton icon="copy" label={t("common.duplicate")} onClick={() => void duplicate(task)} />
                <IconButton icon="download" label={t("common.export")} onClick={() => void exportTask(task)} />
                <IconButton icon="trash" label={t("common.delete")} onClick={() => void remove(task)} />
              </div>
            </article>
          ))}
        </div>
      )}

      {picker ? (
        <TemplatePicker
          onClose={() => setPicker(false)}
          onPick={(k) => {
            setPicker(false);
            onView({ kind: "new", template: k });
          }}
        />
      ) : null}
      {importing ? (
        <ImportDialog
          onClose={() => setImporting(false)}
          onImported={(task) => {
            setImporting(false);
            onView({ kind: "edit", id: task.id });
          }}
        />
      ) : null}
    </div>
  );
}

