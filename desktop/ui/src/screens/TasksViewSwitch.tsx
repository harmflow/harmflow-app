// The switch between the Tasks screen's two views: the task list and the templates.
import { useTranslation } from "react-i18next";
import { SegmentedControl } from "../components/ui";
import type { TaskView } from "./taskView";

export function TasksViewSwitch({ value, onView }: { value: "list" | "templates"; onView: (v: TaskView) => void }) {
  const { t } = useTranslation();
  return (
    <SegmentedControl
      aria-label={t("tasks.views.label")}
      value={value}
      options={[
        { value: "list", label: t("tasks.views.list") },
        { value: "templates", label: t("tasks.views.templates") },
      ]}
      onChange={(kind) => onView({ kind })}
    />
  );
}
