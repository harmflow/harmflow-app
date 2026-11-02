// The task list and its commands: create, edit, duplicate, enable, disable, run, export, import, and delete — every one
// carried out by the daemon. The list's groups and order are the user's arrangement, which the desktop keeps
// (lib/taskLayout): moving a task or a group sends nothing to the daemon.
import { Fragment, useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";
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
import { errorMessage } from "../lib/errorMessage";
import { formatRelative } from "../lib/format";
import { useNow } from "../lib/hooks";
import {
  addGroup,
  arrange,
  deleteGroup,
  materialize,
  moveGroup,
  moveTask,
  moveTaskBy,
  nameTaken,
  placeAfter,
  placeOf,
  reconcile,
  renameGroup,
  sameLayout,
  setCollapsed,
  storeLayout,
  storedLayout,
  tasksIn,
  type Place,
  type TaskGroup,
  type TaskLayout,
} from "../lib/taskLayout";
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

  // The arrangement, held to the daemon's list once the list is loaded — never against the empty one before it, which
  // would drop every task from it. With no group the list looks as it did before groups, with no heading and no
  // handle. Moving is off while a search or a filter hides tasks: a drop among hidden tasks has no clear place.
  const ids = useMemo(() => tasks.map((task) => task.id), [tasks]);
  const [layout, setLayout] = useState<TaskLayout | null>(storedLayout);
  const keep = (next: TaskLayout) => {
    storeLayout(next);
    setLayout(next);
  };
  useEffect(() => {
    if (!loaded) return;
    const next = reconcile(layout, ids);
    if (next && !sameLayout(next, layout)) keep(next);
  }, [ids, loaded]);
  const current = materialize(layout, ids);
  const currentRef = useRef(current);
  currentRef.current = current;
  const filtering = search.trim() !== "" || filter !== "all";
  const shown = useMemo(() => new Set(visible.map((task) => task.id)), [visible]);
  const sections = arrange(layout, tasks);
  const grouped = sections.some((s) => s.group !== null);

  const [announcement, setAnnouncement] = useState("");
  const [renaming, setRenaming] = useState<string | null>(null);
  const [menu, setMenu] = useState<string | null>(null);
  const [focusHandle, setFocusHandle] = useState<string | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!focusHandle) return;
    [...(listRef.current?.querySelectorAll<HTMLElement>("[data-handle]") ?? [])].find((h) => h.dataset.handle === focusHandle)?.focus();
    setFocusHandle(null);
  }, [focusHandle]);

  const groupName = (l: TaskLayout, group: string | null) => (group === null ? t("tasks.groups.ungrouped") : (l.groups.find((g) => g.id === group)?.name ?? ""));

  const placeTask = (next: TaskLayout, id: string) => {
    keep(next);
    const at = placeOf(next, id)!;
    const name = tasks.find((task) => task.id === id)?.name ?? id;
    setAnnouncement(t("tasks.groups.movedTask", { name, group: groupName(next, at.group), position: at.index + 1, count: tasksIn(next, at.group).length }));
  };
  const placeGroup = (next: TaskLayout, id: string) => {
    keep(next);
    const index = next.groups.findIndex((g) => g.id === id);
    setAnnouncement(t("tasks.groups.movedGroup", { name: next.groups[index].name, position: index + 1, count: next.groups.length }));
  };

  const newGroup = () => {
    const { layout: next, id } = addGroup(current, t("tasks.groups.newGroup"));
    keep(next);
    setRenaming(id);
  };
  const rename = (group: TaskGroup, name: string) => {
    setRenaming(null);
    if (!name.trim() || name.trim() === group.name) return;
    if (nameTaken(current, name, group.id)) {
      toast(t("tasks.groups.nameTaken", { name: name.trim() }), "danger");
      return;
    }
    const next = renameGroup(current, group.id, name);
    if (next) keep(next);
  };

  // A drag starts once the pointer moves a few pixels from where it pressed the handle; a press that doesn't move is a
  // click, which opens the handle's menu. Escape during a drag cancels it, and the click its release fires is dropped.
  type Drop = { kind: "task"; place: Place; task?: string; edge?: "before" | "after"; group?: string } | { kind: "group"; index: number; group: string; edge: "before" | "after" };
  const dragRef = useRef<{ kind: "task" | "group"; id: string; x: number; y: number; moved: boolean; cancelled: boolean } | null>(null);
  const dropRef = useRef<Drop | null>(null);
  const swallowClick = useRef(false);
  const [dragging, setDragging] = useState<string | null>(null);
  const [drop, setDrop] = useState<Drop | null>(null);

  const dropAt = (x: number, y: number): Drop | null => {
    const d = dragRef.current!;
    const l = currentRef.current;
    const el = document.elementFromPoint?.(x, y);
    if (d.kind === "group") {
      const section = el?.closest<HTMLElement>("[data-group-section]");
      const target = section?.dataset.groupSection;
      if (!section || !target || target === d.id) return null;
      const rect = section.getBoundingClientRect();
      const edge = y < rect.top + rect.height / 2 ? "before" : "after";
      const rest = l.groups.filter((g) => g.id !== d.id);
      return { kind: "group", index: rest.findIndex((g) => g.id === target) + (edge === "after" ? 1 : 0), group: target, edge };
    }
    // Over its own row the task stays where it is. Between two rows, or over a group's heading, it lands at the edge of
    // the nearest row: before the first one whose middle is below the pointer, or else after the last. A group that
    // shows no row, empty or collapsed, takes it at its end.
    const over = el?.closest<HTMLElement>("[data-task]");
    if (over?.dataset.task === d.id) return null;
    const middle = (r: HTMLElement) => {
      const rect = r.getBoundingClientRect();
      return rect.top + rect.height / 2;
    };
    let near: { row: HTMLElement; edge: "before" | "after" } | undefined = over ? { row: over, edge: y < middle(over) ? "before" : "after" } : undefined;
    if (!near) {
      const section = el?.closest<HTMLElement>("[data-group]");
      if (!section) return null;
      const rows = [...section.querySelectorAll<HTMLElement>("[data-task]")].filter((r) => r.dataset.task !== d.id);
      const next = rows.find((r) => y < middle(r));
      near = next ? { row: next, edge: "before" } : rows.length ? { row: rows[rows.length - 1], edge: "after" } : undefined;
      if (!near) {
        const group = section.dataset.group || null;
        return { kind: "task", place: { group, index: tasksIn(l, group).filter((id) => id !== d.id).length }, group: section.dataset.group };
      }
    }
    const target = near.row.dataset.task!;
    const at = placeOf(l, target)!;
    const from = placeOf(l, d.id);
    const shift = from && from.group === at.group && from.index < at.index ? 1 : 0;
    return { kind: "task", place: { group: at.group, index: at.index + (near.edge === "after" ? 1 : 0) - shift }, task: target, edge: near.edge };
  };

  const startDrag = (kind: "task" | "group", id: string, e: ReactPointerEvent<HTMLButtonElement>) => {
    if (e.button !== 0 || filtering) return;
    dragRef.current = { kind, id, x: e.clientX, y: e.clientY, moved: false, cancelled: false };
    const end = () => {
      dragRef.current = dropRef.current = null;
      setDragging(null);
      setDrop(null);
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("keydown", onKey);
    };
    const onMove = (ev: PointerEvent) => {
      const d = dragRef.current;
      if (!d || d.cancelled) return;
      if (!d.moved && Math.hypot(ev.clientX - d.x, ev.clientY - d.y) < 4) return;
      d.moved = true;
      setDragging(d.id);
      dropRef.current = dropAt(ev.clientX, ev.clientY);
      setDrop(dropRef.current);
    };
    const onUp = () => {
      const d = dragRef.current;
      const target = dropRef.current;
      if (d && (d.moved || d.cancelled)) {
        swallowClick.current = true;
        setTimeout(() => (swallowClick.current = false), 0);
      }
      if (d && d.moved && !d.cancelled && target) {
        if (target.kind === "task") placeTask(moveTask(currentRef.current, d.id, target.place), d.id);
        else placeGroup(moveGroup(currentRef.current, d.id, target.index), d.id);
      }
      end();
    };
    const onKey = (ev: KeyboardEvent) => {
      const d = dragRef.current;
      if (ev.key !== "Escape" || !d?.moved) return;
      ev.preventDefault();
      d.cancelled = true;
      dropRef.current = null;
      setDragging(null);
      setDrop(null);
      setAnnouncement(t("tasks.groups.moveCancelled"));
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("keydown", onKey);
  };

  /** The handle of a task or a group: dragged, it moves it; clicked, or pressed with Enter or Space, it opens its menu. */
  const handle = (key: string, label: string, items: MenuChoice[], onDown: (e: ReactPointerEvent<HTMLButtonElement>) => void) => (
    <div className="popover-anchor drag-handle__anchor">
      <button
        type="button"
        className="drag-handle"
        data-handle={key}
        aria-label={label}
        title={label}
        aria-haspopup="menu"
        aria-expanded={menu === key}
        onPointerDown={onDown}
        onClick={() => {
          if (swallowClick.current) return;
          setMenu(menu === key ? null : key);
        }}
      >
        <Icon name="grip" size={16} />
      </button>
      {menu === key ? (
        <MoveMenu
          items={items}
          onClose={() => {
            setMenu(null);
            setFocusHandle(key);
          }}
        />
      ) : null}
    </div>
  );

  const taskChoices = (id: string): MenuChoice[] => {
    const at = placeOf(current, id)!;
    const count = tasksIn(current, at.group).length;
    const choices: MenuChoice[] = [
      { label: t("tasks.groups.moveUp"), disabled: at.index === 0, onSelect: () => placeTask(moveTaskBy(current, id, -1), id) },
      { label: t("tasks.groups.moveDown"), disabled: at.index === count - 1, onSelect: () => placeTask(moveTaskBy(current, id, 1), id) },
    ];
    for (const g of current.groups)
      if (g.id !== at.group) choices.push({ label: t("tasks.groups.moveTo", { group: g.name }), onSelect: () => placeTask(moveTask(current, id, { group: g.id, index: g.tasks.length }), id) });
    if (at.group !== null)
      choices.push({
        label: t("tasks.groups.moveTo", { group: t("tasks.groups.ungrouped") }),
        onSelect: () => placeTask(moveTask(current, id, { group: null, index: current.ungrouped.length }), id),
      });
    return choices;
  };
  const groupChoices = (id: string): MenuChoice[] => {
    const index = current.groups.findIndex((g) => g.id === id);
    return [
      { label: t("tasks.groups.moveUp"), disabled: index === 0, onSelect: () => placeGroup(moveGroup(current, id, index - 1), id) },
      { label: t("tasks.groups.moveDown"), disabled: index === current.groups.length - 1, onSelect: () => placeGroup(moveGroup(current, id, index + 1), id) },
    ];
  };

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
      // The copy goes right after its original, in the same group, before the list read again shows it.
      keep(placeAfter(materialize(layout, [...ids, copy.id]), task.id, copy.id));
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
      toast(errorMessage(err), "danger");
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

  /** The groups in the user's order, then Ungrouped; with no group, the rows alone, as the list showed before groups. */
  const renderSections = (row: (task: TaskSummary) => ReactNode) =>
    sections.map((section) => {
      const rows = section.tasks.filter((task) => shown.has(task.id));
      const g = section.group;
      if (!g && !grouped) return <Fragment key="ungrouped">{rows.map(row)}</Fragment>;
      if (filtering && rows.length === 0) return null;
      const body = (
        <div className="task-group__body">
          {rows.map(row)}
          {rows.length === 0 ? <p className="task-group__drop">{t("tasks.groups.dropHere")}</p> : null}
        </div>
      );
      const count = <span className="task-group__count">{t("tasks.groups.count", { count: section.tasks.length })}</span>;
      if (!g)
        return (
          <section key="ungrouped" className={cx("task-group", drop?.kind === "task" && drop.group === "" && "task-group--drop")} data-group="">
            <header className="task-group__head">
              <h3 className="task-group__name">{t("tasks.groups.ungrouped")}</h3>
              {count}
            </header>
            {body}
          </section>
        );
      return (
        <section
          key={g.id}
          className={cx(
            "task-group",
            drop?.kind === "task" && drop.group === g.id && "task-group--drop",
            drop?.kind === "group" && drop.group === g.id && `task-group--drop-${drop.edge}`,
            dragging === g.id && "task-group--dragging",
          )}
          data-group={g.id}
          data-group-section={g.id}
        >
          <header className="task-group__head">
            {filtering ? null : handle(`group:${g.id}`, t("tasks.groups.moveGroup", { name: g.name }), groupChoices(g.id), (e) => startDrag("group", g.id, e))}
            <IconButton
              icon={g.collapsed ? "chevronRight" : "chevronDown"}
              label={g.collapsed ? t("tasks.groups.expand") : t("tasks.groups.collapse")}
              aria-expanded={!g.collapsed}
              onClick={() => keep(setCollapsed(current, g.id, !g.collapsed))}
            />
            {renaming === g.id ? (
              <GroupName name={g.name} label={t("tasks.groups.nameLabel")} onDone={(name) => rename(g, name)} />
            ) : (
              <h3 className="task-group__name">{g.name}</h3>
            )}
            {count}
            <div className="task-group__actions">
              <IconButton icon="pencil" label={t("tasks.groups.rename")} onClick={() => setRenaming(g.id)} />
              <IconButton icon="trash" label={t("tasks.groups.delete")} onClick={() => keep(deleteGroup(current, g.id))} />
            </div>
          </header>
          {g.collapsed ? null : body}
        </section>
      );
    });

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
        <Button icon="layers" onClick={newGroup}>
          {t("tasks.groups.newGroup")}
        </Button>
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
        <div ref={listRef} className={cx("task-list", dragging && "task-list--dragging")}>
          {renderSections((task) => (
            <article
              key={task.id}
              data-task={task.id}
              className={cx(
                "task-row",
                !task.enabled && "task-row--disabled",
                dragging === task.id && "task-row--dragging",
                drop?.kind === "task" && drop.task === task.id && `task-row--drop-${drop.edge}`,
              )}
            >
              {!grouped || filtering ? null : handle(`task:${task.id}`, t("tasks.groups.moveTask", { name: task.name }), taskChoices(task.id), (e) => startDrag("task", task.id, e))}
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
      <div className="sr-only" role="status" aria-live="polite">
        {announcement}
      </div>

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

type MenuChoice = { label: string; disabled?: boolean; onSelect: () => void };

/** A handle's move choices. It takes the focus when it opens; Escape or a press outside closes it. */
function MoveMenu({ items, onClose }: { items: MenuChoice[]; onClose: () => void }) {
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    ref.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.parentElement?.contains(e.target as Node)) onClose();
    };
    window.addEventListener("mousedown", onDown);
    return () => window.removeEventListener("mousedown", onDown);
  }, []);
  return (
    <div
      ref={ref}
      className="popover popover--start"
      role="menu"
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onClose();
        }
      }}
    >
      {items.map((item) => (
        <button
          key={item.label}
          type="button"
          role="menuitem"
          className="menu-item"
          disabled={item.disabled}
          onClick={() => {
            item.onSelect();
            onClose();
          }}
        >
          <span>{item.label}</span>
        </button>
      ))}
    </div>
  );
}

/** A group's name being edited: Enter or leaving the field keeps it, Escape keeps the name it had. */
function GroupName({ name, label, onDone }: { name: string; label: string; onDone: (name: string) => void }) {
  const [value, setValue] = useState(name);
  const done = useRef(false);
  const finish = (v: string) => {
    if (done.current) return;
    done.current = true;
    onDone(v);
  };
  return (
    <TextInput
      className="task-group__rename"
      aria-label={label}
      autoFocus
      value={value}
      onChange={(e) => setValue(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === "Enter") finish(value);
        if (e.key === "Escape") finish(name);
      }}
      onBlur={() => finish(value)}
    />
  );
}

