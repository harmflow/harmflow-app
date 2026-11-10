// The agent a task's prompt is copied from, and the review of a newer copy of it.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { native } from "../../api/native";
import type { Catalog } from "../../api/types";
import { applyAgentCopy, changedOrigin, clearOrigin, copyChanges, leftOut, lineDiff, originOf, promptEdited } from "../../lib/agentCopy";
import { getIn, type Doc } from "../../lib/taskForm";
import { Icon } from "../Icon";
import { Banner, Button, Field, Modal, Select } from "../ui";

/**
 * The agent the task's prompt is copied from. Choosing one copies it once, asking first when that would replace a
 * prompt the user wrote; the fields it leaves out are named. When the agent has changed since, a notice offers to copy
 * it again, after showing the difference in the prompt and in every other field the copy replaces. An origin agent
 * that no longer exists shows nothing.
 */
export function AgentOrigin({ doc, onChange, catalog }: { doc: Doc; onChange: (doc: Doc) => void; catalog?: Catalog }) {
  const { t } = useTranslation();
  const [reviewing, setReviewing] = useState(false);
  const all = catalog?.agents ?? [];
  const agents = all.filter((a) => a.copy);
  const origin = originOf(doc);
  const selected = origin ? agents.find((a) => a.name === origin.agent) : undefined;
  const changed = changedOrigin(doc, all);
  const omitted = selected?.copy ? leftOut(doc, selected.copy) : [];
  const prompt = getIn<string>(doc, ["spec", "prompt"]) ?? "";
  const fieldChanges = changed && origin ? copyChanges(doc, origin.agent, changed) : [];

  const choose = async (name: string) => {
    if (!name) {
      onChange(clearOrigin(doc));
      return;
    }
    const copy = agents.find((a) => a.name === name)?.copy;
    if (!copy) return;
    if (
      promptEdited(doc, all) &&
      !(await native.confirm(t("tasks.agent.replaceConfirm", { name }), t("tasks.agent.replaceTitle"), t("tasks.agent.replace"), t("common.cancel")))
    ) {
      return;
    }
    onChange(applyAgentCopy(doc, name, copy));
  };

  return (
    <>
      <Field label={t("tasks.agent.label")} hint={t("tasks.agent.hint")}>
        <Select aria-label={t("tasks.agent.label")} value={origin?.agent ?? ""} onChange={(e) => void choose(e.target.value)}>
          <option value="">{t("tasks.agent.none")}</option>
          {origin && !selected ? <option value={origin.agent}>{origin.agent}</option> : null}
          {agents.map((a) => (
            <option key={a.name} value={a.name}>
              {a.name} — {a.source === "builtin" ? t("tasks.fields.builtin") : t("tasks.fields.user")}
            </option>
          ))}
        </Select>
      </Field>
      {omitted.length ? (
        <p className="hint-line agent-left-out">
          <Icon name="alert" size={13} /> {t("tasks.agent.leftOut", { fields: omitted.join(", ") })}
        </p>
      ) : null}
      {changed && origin ? (
        <Banner
          tone="warning"
          icon="refresh"
          actions={
            <Button size="sm" onClick={() => setReviewing(true)}>
              {t("tasks.agent.copyAgain")}
            </Button>
          }
        >
          {t("tasks.agent.changed", { name: origin.agent })}
        </Banner>
      ) : null}
      {reviewing && changed && origin ? (
        <Modal
          wide
          title={t("tasks.agent.diffTitle", { name: origin.agent })}
          onClose={() => setReviewing(false)}
          footer={
            <>
              <Button onClick={() => setReviewing(false)}>{t("common.cancel")}</Button>
              <Button
                variant="primary"
                onClick={() => {
                  onChange(applyAgentCopy(doc, origin.agent, changed));
                  setReviewing(false);
                }}
              >
                {t("tasks.agent.replaceCopy")}
              </Button>
            </>
          }
        >
          {fieldChanges.length ? (
            <>
              <p className="muted">{t("tasks.agent.fieldsHint")}</p>
              <dl className="copy-changes">
                {fieldChanges.map((c) => (
                  <div key={c.field} className="copy-changes__row">
                    <dt>{t(`tasks.fields.${c.field}`)}</dt>
                    <dd className="mono">
                      <span className="copy-changes__from">{c.from || (c.field === "model" ? t("tasks.fields.harnessDefault") : t("tasks.agent.none"))}</span>
                      {" → "}
                      <span className="copy-changes__to">{c.to}</span>
                    </dd>
                  </div>
                ))}
              </dl>
            </>
          ) : null}
          {prompt !== changed.prompt ? (
            <>
              <p className="muted">{t("tasks.agent.diffHint")}</p>
              <pre className="prompt-diff mono">
                {lineDiff(prompt, changed.prompt).map((l, i) => (
                  <span key={i} className={`prompt-diff__line prompt-diff__line--${l.kind}`}>
                    {l.kind === "added" ? "+ " : l.kind === "removed" ? "- " : "  "}
                    {l.text}
                    {"\n"}
                  </span>
                ))}
              </pre>
            </>
          ) : (
            <p className="muted agent-copy-same">{fieldChanges.length ? t("tasks.agent.promptSame") : t("tasks.agent.nothingChanges")}</p>
          )}
        </Modal>
      ) : null}
    </>
  );
}
