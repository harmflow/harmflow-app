// The visual form of a task: the fields common to every task, then the parameters of its type. Each field writes into
// the task document; a cleared field is removed so its default applies.
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Catalog, CatalogEntry, Harness } from "../api/types";
import { NOTIFIABLE_EVENTS, schemaDefault, schemaEnum } from "../lib/schema";
import { getIn, setIn, slugify, type Doc, type Path } from "../lib/taskForm";
import { Icon, type IconName } from "./Icon";
import { AgentOrigin } from "./taskform/AgentOrigin";
import { EnumField } from "./taskform/EnumField";
import type { Item } from "./taskform/formModel";
import { FallbackList, HarnessProfile } from "./taskform/HarnessProfile";
import { IntField } from "./taskform/IntField";
import { ListArea } from "./taskform/ListArea";
import { NamePicker } from "./taskform/NamePicker";
import { RepositoryList } from "./taskform/RepositoryList";
import { ScheduleFields } from "./taskform/ScheduleFields";
import { ScriptFields } from "./taskform/ScriptFields";
import { Checkbox, Field, FieldGroup, TextArea, TextInput, Toggle } from "./ui";

export { OPTION_FIELDS } from "./taskform/formModel";

export interface TaskFormProps {
  doc: Doc;
  onChange: (doc: Doc) => void;
  mode: "create" | "edit";
  catalog?: Catalog;
  harnesses: Harness[];
  errorPaths: Set<string>;
}

/** The lists of a custom permission profile, in the form's order. */
export const CUSTOM_LISTS = ["allowedTools", "deniedTools", "writablePaths", "readablePaths", "allowedPrograms", "deniedPrograms"] as const;

function Section({ id, title, icon, children }: { id: string; title: string; icon: IconName; children: ReactNode }) {
  return (
    <section className="form-section" id={`section-${id}`}>
      <h3 className="form-section__title">
        <Icon name={icon} size={17} />
        {title}
      </h3>
      <div className="form-section__body">{children}</div>
    </section>
  );
}

/**
 * The skills the form offers. When the preferred harness has installations by home, they follow the installation the
 * profile names, or the one in use while it names none, as its Model and Effort lists do: the built-ins, and the skills
 * that installation's own list holds, which validation and a run resolve the task's against. Otherwise, the catalog's.
 */
function installationSkills(entries: CatalogEntry[], harnesses: Harness[], profile: Item): CatalogEntry[] {
  const provider = (profile.provider as string) ?? "claude-code";
  const harness = harnesses.find((h) => h.id === provider);
  if (!harness?.homes?.length) return entries;
  const id = (profile.installation as string) || harness.installationId;
  const names = harness.homes.find((h) => h.installationId === id)?.skills ?? [];
  const offered = entries.filter((e) => e.source === "builtin" || names.includes(e.name));
  for (const name of names) {
    if (!offered.some((e) => e.name === name)) offered.push({ name, kind: "skill", source: "user", harnesses: [provider] });
  }
  return offered.sort((a, b) => a.name.localeCompare(b.name));
}

export function TaskForm({ doc, onChange, mode, catalog, harnesses, errorPaths }: TaskFormProps) {
  const { t, i18n } = useTranslation();
  const [idTouched, setIdTouched] = useState(mode === "edit");

  const get = <T,>(path: Path) => getIn<T>(doc, path);
  const set = (path: Path, value: unknown) => onChange(setIn(doc, path, value));
  const err = (path: Path) => (errorPaths.has("/" + path.join("/")) ? t("common.error") : undefined);
  const def = (path: Path) => schemaDefault(path);
  const defLabel = (path: Path, label?: (v: string) => string) => {
    const d = def(path);
    if (d === undefined) return t("common.default");
    const text = Array.isArray(d) ? d.join(", ") : String(d);
    return t("common.defaultValue", { value: label ? label(text) : text });
  };

  const type = get<string>(["spec", "type"]) ?? "general";
  const isGithub = type === "github";
  // A script task runs a program instead of a harness session: no prompt, agents, skills, or harness.
  const isScript = type === "script";

  const setName = (name: string) => {
    let next = setIn(doc, ["metadata", "name"], name);
    if (!idTouched && mode === "create") next = setIn(next, ["metadata", "id"], slugify(name));
    onChange(next);
  };

  return (
    <div className="task-form">
      <Section id="general" title={t("tasks.sections.general")} icon="file">
        <div className="grid-2">
          <Field label={t("tasks.fields.name")} error={err(["metadata", "name"])}>
            <TextInput value={get<string>(["metadata", "name"]) ?? ""} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label={t("tasks.fields.id")} hint={t("tasks.fields.idHint")} error={err(["metadata", "id"])}>
            <TextInput
              mono
              value={get<string>(["metadata", "id"]) ?? ""}
              disabled={mode === "edit"}
              onChange={(e) => {
                setIdTouched(true);
                set(["metadata", "id"], e.target.value);
              }}
            />
          </Field>
        </div>
        <Field label={t("tasks.fields.description")}>
          <TextArea rows={2} value={get<string>(["metadata", "description"]) ?? ""} onChange={(e) => set(["metadata", "description"], e.target.value)} />
        </Field>
        <div className="grid-2">
          {/* The type is chosen in the template picker and fixed from then on. */}
          <Field label={t("tasks.fields.type")} error={err(["spec", "type"])}>
            <TextInput value={t(`tasks.types.${type}` as never, { defaultValue: type }) as string} disabled />
          </Field>
          <div className="field field--toggle">
            <Toggle
              checked={get<boolean>(["spec", "enabled"]) ?? (def(["spec", "enabled"]) as boolean) ?? true}
              onChange={(v) => set(["spec", "enabled"], v)}
              label={t("tasks.fields.enabled")}
            />
          </div>
        </div>
      </Section>

      {isScript ? (
        <Section id="script" title={t("tasks.sections.script")} icon="terminal">
          <ScriptFields doc={doc} onChange={onChange} set={set} err={err} />
        </Section>
      ) : null}

      {isScript ? null : (
      <Section id="prompt" title={t("tasks.sections.prompt")} icon="message">
        <AgentOrigin doc={doc} onChange={onChange} catalog={catalog} />
        <Field label={t("tasks.fields.prompt")} hint={t("tasks.fields.promptHint")} error={err(["spec", "prompt"])}>
          <TextArea className="mono input--prompt" rows={8} value={get<string>(["spec", "prompt"]) ?? ""} onChange={(e) => set(["spec", "prompt"], e.target.value)} />
        </Field>
        <div className="grid-2">
          <NamePicker
            label={t("tasks.fields.agents")}
            entries={catalog?.agents ?? []}
            value={get<string[]>(["spec", "agents"]) ?? []}
            onChange={(v) => set(["spec", "agents"], v)}
          />
          <NamePicker
            label={t("tasks.fields.skills")}
            entries={installationSkills(catalog?.skills ?? [], harnesses, get<Item>(["spec", "harness", "preferred"]) ?? {})}
            value={get<string[]>(["spec", "skills"]) ?? []}
            onChange={(v) => set(["spec", "skills"], v)}
          />
        </div>
        <Field label={t("tasks.fields.workspacePath")} hint={t("tasks.fields.workspaceHint")}>
          <TextInput mono value={get<string>(["spec", "workspace", "path"]) ?? ""} onChange={(e) => set(["spec", "workspace", "path"], e.target.value)} />
        </Field>
      </Section>
      )}

      {isGithub ? (
        <Section id="type" title={`${t("tasks.sections.type")} — ${t("tasks.types.github")}`} icon="code">
          <h4 className="subhead">{t("tasks.fields.repositories")}</h4>
          <RepositoryList
            rows={get<Item[]>(["spec", "github", "repositories"]) ?? []}
            onChange={(rows) => set(["spec", "github", "repositories"], rows)}
            errorPaths={errorPaths}
          />
          <p className="hint-line">
            <Icon name="sparkle" size={13} /> {t("tasks.fields.workSelectionHint")}
          </p>
          <div className="grid-3">
            <EnumField label={t("tasks.fields.ordering")} optionKey="ordering" doc={doc} set={set} defLabel={defLabel} />
            <IntField path={["spec", "github", "queue", "maxDiscoveredPerCycle"]} label={t("tasks.fields.maxDiscoveredPerCycle")} doc={doc} set={set} />
            <IntField path={["spec", "github", "queue", "maxItemsPerCycle"]} label={t("tasks.fields.maxItemsPerCycle")} doc={doc} set={set} />
            <EnumField label={t("tasks.fields.resumeStrategy")} optionKey="resumeStrategy" doc={doc} set={set} defLabel={defLabel} />
            <IntField path={["spec", "github", "queue", "maxResumeAttempts"]} label={t("tasks.fields.maxResumeAttempts")} doc={doc} set={set} />
            <IntField path={["spec", "github", "queue", "maxFailedRetries"]} label={t("tasks.fields.maxFailedRetries")} doc={doc} set={set} />
            <IntField path={["spec", "github", "delivery", "testRepairAttempts"]} label={t("tasks.fields.testRepairAttempts")} doc={doc} set={set} />
          </div>
        </Section>
      ) : null}

      {isScript ? null : (
      <Section id="harness" title={t("tasks.sections.harness")} icon="agent">
        <h4 className="subhead">{t("tasks.fields.preferred")}</h4>
        <HarnessProfile
          value={get<Item>(["spec", "harness", "preferred"]) ?? {}}
          onChange={(v) => set(["spec", "harness", "preferred"], v)}
          harnesses={harnesses}
          required
        />
        <h4 className="subhead">{t("tasks.fields.fallbackCandidates")}</h4>
        <FallbackList
          rows={get<Item[]>(["spec", "harness", "fallback", "candidates"]) ?? []}
          onChange={(rows) => set(["spec", "harness", "fallback", "candidates"], rows)}
          harnesses={harnesses}
        />
        <div className="grid-2">
          <FieldGroup label={t("tasks.fields.fallbackOn")} className="check-row">
            {schemaEnum(["spec", "harness", "fallback", "on"]).map((trigger) => {
              const current = get<string[]>(["spec", "harness", "fallback", "on"]) ?? (def(["spec", "harness", "fallback", "on"]) as string[]) ?? [];
              return (
                <Checkbox
                  key={trigger}
                  checked={current.includes(trigger)}
                  label={t(`tasks.options.fallbackOn.${trigger}` as never, { defaultValue: trigger }) as string}
                  onChange={(on) => {
                    const next = on ? [...current, trigger] : current.filter((x) => x !== trigger);
                    set(["spec", "harness", "fallback", "on"], next.length ? next : undefined);
                  }}
                />
              );
            })}
          </FieldGroup>
          <IntField path={["spec", "harness", "fallback", "maxSwitches"]} label={t("tasks.fields.maxSwitches")} doc={doc} set={set} />
        </div>
      </Section>
      )}

      <Section id="schedule" title={t("tasks.sections.schedule")} icon="clock">
        <ScheduleFields doc={doc} onChange={onChange} set={set} defLabel={defLabel} locale={i18n.language} errorPaths={errorPaths} />
      </Section>

      <Section id="permissions" title={t("tasks.sections.permissions")} icon="shield">
        {isScript ? (
          <p className="hint-line">
            <Icon name="shield" size={13} /> {t("tasks.fields.scriptPermissionsHint")}
          </p>
        ) : null}
        <div className="grid-3">
          <EnumField label={t("tasks.fields.profile")} optionKey="profile" doc={doc} set={set} defLabel={defLabel} />
          <EnumField label={t("tasks.fields.network")} optionKey="network" doc={doc} set={set} defLabel={defLabel} />
          <div className="field field--toggle">
            <Toggle
              checked={get<boolean>(["spec", "permissions", "restrictToWorkspace"]) ?? (def(["spec", "permissions", "restrictToWorkspace"]) as boolean) ?? true}
              onChange={(v) => set(["spec", "permissions", "restrictToWorkspace"], v)}
              label={t("tasks.fields.restrictToWorkspace")}
            />
          </div>
        </div>
        {get<string>(["spec", "permissions", "profile"]) === "custom" ? (
          <div className="grid-3">
            {CUSTOM_LISTS.map((key) => (
              <Field key={key} label={t(`tasks.fields.${key}`)} hint={t("tasks.fields.listHint")}>
                <ListArea value={get(["spec", "permissions", "custom", key])} onValue={(v) => set(["spec", "permissions", "custom", key], v)} />
              </Field>
            ))}
          </div>
        ) : null}
      </Section>

      <Section id="execution" title={t("tasks.sections.execution")} icon="clock">
        <div className="grid-3">
          <IntField path={["spec", "execution", "timeoutMinutes"]} label={t("tasks.fields.timeoutMinutes")} doc={doc} set={set} />
          <IntField path={["spec", "execution", "cancelGraceSeconds"]} label={t("tasks.fields.cancelGraceSeconds")} doc={doc} set={set} />
          {isScript ? null : (
            <>
              <IntField path={["spec", "execution", "rateLimit", "maxRetries"]} label={t("tasks.fields.rateLimitMaxRetries")} doc={doc} set={set} />
              <IntField path={["spec", "execution", "rateLimit", "initialWaitSeconds"]} label={t("tasks.fields.rateLimitInitialWait")} doc={doc} set={set} />
              <IntField path={["spec", "execution", "rateLimit", "maxWaitSeconds"]} label={t("tasks.fields.rateLimitMaxWait")} doc={doc} set={set} />
              <div className="field field--toggle">
                {/* Off removes the key, so the default, which allows none, applies. */}
                <Toggle
                  checked={get<boolean>(["spec", "execution", "allowExtraUsage"]) ?? (def(["spec", "execution", "allowExtraUsage"]) as boolean) ?? false}
                  onChange={(v) => set(["spec", "execution", "allowExtraUsage"], v || undefined)}
                  label={t("tasks.fields.allowExtraUsage")}
                />
                <span className="field__hint">{t("tasks.fields.allowExtraUsageHint")}</span>
              </div>
            </>
          )}
        </div>
      </Section>

      <Section id="notifications" title={t("tasks.sections.notifications")} icon="bell">
        <Toggle
          checked={get<boolean>(["spec", "notifications", "native", "enabled"]) ?? (def(["spec", "notifications", "native", "enabled"]) as boolean) ?? true}
          onChange={(v) => set(["spec", "notifications", "native", "enabled"], v)}
          label={t("tasks.fields.notificationsEnabled")}
        />
        <FieldGroup label={t("tasks.fields.notificationEvents")} className="check-grid">
          {NOTIFIABLE_EVENTS.map((ev) => {
            const current =
              get<string[]>(["spec", "notifications", "native", "events"]) ?? (def(["spec", "notifications", "native", "events"]) as string[]) ?? [];
            return (
              <Checkbox
                key={ev}
                checked={current.includes(ev)}
                label={t(`tasks.options.notifiable.${ev}` as never, { defaultValue: ev }) as string}
                onChange={(on) => {
                  const next = on ? [...current, ev] : current.filter((x) => x !== ev);
                  set(["spec", "notifications", "native", "events"], next);
                }}
              />
            );
          })}
        </FieldGroup>
      </Section>
    </div>
  );
}
