// The task list and its commands: create, edit, duplicate, enable, disable, run, export, import, and delete — every one
// carried out by the daemon. The list's groups and order are the user's arrangement, which the desktop keeps
// (lib/taskLayout): moving a task or a group sends nothing to the daemon.
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import type { Task, TemplateLanguage } from "../api/types";
import { TaskEditor, type LeaveCheck } from "../components/TaskEditor";
import { toast } from "../components/toast";
import { Banner } from "../components/ui";
import type { Doc } from "../lib/taskForm";
import { TaskList } from "./TaskList";
import type { TaskView } from "./taskView";

export type { TaskView } from "./taskView";

export function TasksScreen({
  view,
  onView,
  onOpenExecution,
  onLeaveCheck,
  onCreated,
}: {
  view: TaskView;
  onView: (v: TaskView) => void;
  onOpenExecution: (id: string) => void;
  onLeaveCheck?: (check: LeaveCheck | null) => void;
  /** A new task was saved; with none, its editor goes on editing it. */
  onCreated?: (task: Task) => void;
}) {
  if (view.kind === "new")
    return (
      <NewTask
        key={`${view.template}-${view.pick ?? 0}`}
        template={view.template}
        onDone={() => onView({ kind: "list" })}
        onSaved={(task) => (onCreated ? onCreated(task) : onView({ kind: "edit", id: task.id }))}
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
