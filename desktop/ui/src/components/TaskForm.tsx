// The visual form of a task: the fields common to every task, then the parameters of its type. Each field writes into
// the task document; a cleared field is removed so its default applies.
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import type { Catalog, CatalogEntry, Harness, SchedulePreview } from "../api/types";
import { daemonText } from "../lib/daemonText";
import { formatDateTime } from "../lib/format";
import { useDebounced } from "../lib/hooks";
import { HARNESS_PROVIDERS, NOTIFIABLE_EVENTS, schemaDefault, schemaEnum, schemaRange } from "../lib/schema";
import {
  envToLines,
  getIn,
  linesToArgs,
  linesToEnv,
  linesToList,
  listToLines,
  programKind,
  scheduleKind,
  setIn,
  setProgramKind,
  setScheduleKind,
  slugify,
  type Doc,
  type Path,
} from "../lib/taskForm";
import { Icon, type IconName } from "./Icon";
import { Button, Checkbox, Field, IconButton, NumberInput, Select, TextArea, TextInput, Toggle, cx } from "./ui";

export interface TaskFormProps {
  doc: Doc;
  onChange: (doc: Doc) => void;
  mode: "create" | "edit";
  catalog?: Catalog;
  harnesses: Harness[];
  errorPaths: Set<string>;
}

type Item = Record<string, unknown>;

function systemZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    return "UTC";
  }
}

function timeZones(): string[] {
  try {
    return (Intl as unknown as { supportedValuesOf(k: string): string[] }).supportedValuesOf("timeZone");
  } catch {
    return [];
  }
}

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

/** Removes undefined and empty-string keys from a list row, keeping the row itself. */
function cleanRow(row: Item): Item {
  const out: Item = {};
  for (const [k, v] of Object.entries(row)) if (v !== undefined && v !== "") out[k] = v;
  return out;
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
  const isGithub = type === "github-issue-implementation";
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
              className="mono"
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
            entries={catalog?.skills ?? []}
            value={get<string[]>(["spec", "skills"]) ?? []}
            onChange={(v) => set(["spec", "skills"], v)}
          />
        </div>
        <Field label={t("tasks.fields.workspacePath")} hint={t("tasks.fields.workspaceHint")}>
          <TextInput className="mono" value={get<string>(["spec", "workspace", "path"]) ?? ""} onChange={(e) => set(["spec", "workspace", "path"], e.target.value)} />
        </Field>
      </Section>
      )}

      {isGithub ? (
        <Section id="type" title={`${t("tasks.sections.type")} — ${t("tasks.types.github-issue-implementation")}`} icon="code">
          <h4 className="subhead">{t("tasks.fields.repositories")}</h4>
          <RepositoryList
            rows={get<Item[]>(["spec", "githubIssueImplementation", "repositories"]) ?? []}
            onChange={(rows) => set(["spec", "githubIssueImplementation", "repositories"], rows)}
            errorPaths={errorPaths}
          />
          <p className="hint-line">
            <Icon name="sparkle" size={13} /> {t("tasks.fields.issueSelectionHint")}
          </p>
          <div className="grid-3">
            <EnumField path={["spec", "githubIssueImplementation", "queue", "ordering"]} label={t("tasks.fields.ordering")} optionKey="ordering" doc={doc} set={set} defLabel={defLabel} />
            <IntField path={["spec", "githubIssueImplementation", "queue", "maxDiscoveredPerCycle"]} label={t("tasks.fields.maxDiscoveredPerCycle")} doc={doc} set={set} />
            <IntField path={["spec", "githubIssueImplementation", "queue", "maxItemsPerCycle"]} label={t("tasks.fields.maxItemsPerCycle")} doc={doc} set={set} />
            <EnumField path={["spec", "githubIssueImplementation", "queue", "resumeStrategy"]} label={t("tasks.fields.resumeStrategy")} optionKey="resumeStrategy" doc={doc} set={set} defLabel={defLabel} />
            <IntField path={["spec", "githubIssueImplementation", "queue", "maxResumeAttempts"]} label={t("tasks.fields.maxResumeAttempts")} doc={doc} set={set} />
            <IntField path={["spec", "githubIssueImplementation", "queue", "maxFailedRetries"]} label={t("tasks.fields.maxFailedRetries")} doc={doc} set={set} />
            <IntField path={["spec", "githubIssueImplementation", "delivery", "testRepairAttempts"]} label={t("tasks.fields.testRepairAttempts")} doc={doc} set={set} />
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
          <Field label={t("tasks.fields.fallbackOn")}>
            <div className="check-row">
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
            </div>
          </Field>
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
          <EnumField path={["spec", "permissions", "profile"]} label={t("tasks.fields.profile")} optionKey="profile" doc={doc} set={set} defLabel={defLabel} />
          <EnumField path={["spec", "permissions", "network"]} label={t("tasks.fields.network")} optionKey="network" doc={doc} set={set} defLabel={defLabel} />
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
            {(["allowedTools", "deniedTools", "writablePaths"] as const).map((key) => (
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
        <Field label={t("tasks.fields.notificationEvents")}>
          <div className="check-grid">
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
          </div>
        </Field>
      </Section>
    </div>
  );
}

type SetFn = (path: Path, value: unknown) => void;

/** A script task's program, in either form, its working directory, environment, and systems. */
function ScriptFields({ doc, onChange, set, err }: { doc: Doc; onChange: (doc: Doc) => void; set: SetFn; err: (path: Path) => string | undefined }) {
  const { t } = useTranslation();
  const kind = programKind(doc);
  const systems = getIn<string[]>(doc, ["spec", "script", "systems"]) ?? [];
  return (
    <>
      <div className="segmented" role="radiogroup" aria-label={t("tasks.fields.programKind")}>
        {(["command", "inline"] as const).map((k) => (
          <button
            key={k}
            type="button"
            role="radio"
            aria-checked={kind === k}
            className={cx("segmented__item", kind === k && "segmented__item--active")}
            onClick={() => onChange(setProgramKind(doc, k))}
          >
            {k === "command" ? t("tasks.fields.programCommand") : t("tasks.fields.programInline")}
          </button>
        ))}
      </div>
      {kind === "command" ? (
        <Field label={t("tasks.fields.command")} hint={t("tasks.fields.commandHint")} error={err(["spec", "script", "command"]) ?? err(["spec", "script", "command", 0])}>
          <ArgsArea rows={4} value={getIn(doc, ["spec", "script", "command"])} onValue={(v) => set(["spec", "script", "command"], v)} placeholder={"/usr/bin/python3\n/Users/me/jobs/audit.py\n--weekly"} />
        </Field>
      ) : (
        <>
          <Field label={t("tasks.fields.interpreter")} hint={t("tasks.fields.interpreterHint")} error={err(["spec", "script", "inline", "interpreter"])}>
            <ArgsArea rows={2} value={getIn(doc, ["spec", "script", "inline", "interpreter"])} onValue={(v) => set(["spec", "script", "inline", "interpreter"], v)} placeholder={"/bin/zsh"} />
          </Field>
          <Field label={t("tasks.fields.source")} error={err(["spec", "script", "inline", "source"])}>
            <TextArea
              className="mono input--prompt"
              rows={10}
              value={getIn<string>(doc, ["spec", "script", "inline", "source"]) ?? ""}
              onChange={(e) => set(["spec", "script", "inline", "source"], e.target.value)}
            />
          </Field>
        </>
      )}
      <div className="grid-2">
        <Field label={t("tasks.fields.workingDirectory")} hint={t("tasks.fields.workingDirectoryHint")} error={err(["spec", "script", "workingDirectory"])}>
          <TextInput
            className="mono"
            value={getIn<string>(doc, ["spec", "script", "workingDirectory"]) ?? ""}
            placeholder="~"
            onChange={(e) => set(["spec", "script", "workingDirectory"], e.target.value)}
          />
        </Field>
        <Field label={t("tasks.fields.systems")} hint={t("tasks.fields.systemsHint")}>
          <div className="check-row">
            {schemaEnum(["spec", "script", "systems"]).map((system) => (
              <Checkbox
                key={system}
                checked={systems.includes(system)}
                label={t(`tasks.options.systems.${system}` as never, { defaultValue: system }) as string}
                onChange={(on) => set(["spec", "script", "systems"], on ? [...systems, system] : systems.filter((s) => s !== system))}
              />
            ))}
          </div>
        </Field>
      </div>
      <Field label={t("tasks.fields.environment")} hint={t("tasks.fields.environmentHint")} error={err(["spec", "script", "environment"])}>
        <EnvArea value={getIn(doc, ["spec", "script", "environment"])} onValue={(v) => set(["spec", "script", "environment"], v)} />
      </Field>
    </>
  );
}

/** An argument list, one argument per line, kept as typed: spaces inside an argument are its own. */
function ArgsArea({ value, onValue, rows, placeholder }: { value: unknown; onValue: (v: string[] | undefined) => void; rows: number; placeholder?: string }) {
  const [text, setText] = useState(listToLines(value));
  useEffect(() => {
    if (JSON.stringify(linesToArgs(text)) !== JSON.stringify(value ?? [])) setText(listToLines(value));
  }, [JSON.stringify(value)]); // keep in sync with YAML edits
  return (
    <TextArea
      rows={rows}
      className="mono"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const args = linesToArgs(e.target.value);
        onValue(args.length ? args : undefined);
      }}
    />
  );
}

function EnvArea({ value, onValue }: { value: unknown; onValue: (v: Record<string, string> | undefined) => void }) {
  const [text, setText] = useState(envToLines(value));
  useEffect(() => {
    if (JSON.stringify(linesToEnv(text)) !== JSON.stringify(value ?? {})) setText(envToLines(value));
  }, [JSON.stringify(value)]); // keep in sync with YAML edits
  return (
    <TextArea
      rows={3}
      className="mono"
      value={text}
      placeholder="TZ=America/Sao_Paulo"
      onChange={(e) => {
        setText(e.target.value);
        const env = linesToEnv(e.target.value);
        onValue(Object.keys(env).length ? env : undefined);
      }}
    />
  );
}

function IntField({ path, label, doc, set }: { path: Path; label: string; doc: Doc; set: SetFn }) {
  const { t } = useTranslation();
  const d = schemaDefault<number>(path);
  const range = schemaRange(path);
  return (
    <Field label={label}>
      <NumberInput
        value={getIn<number>(doc, path)}
        onValue={(v) => set(path, v)}
        min={range.min}
        max={range.max}
        placeholder={d !== undefined ? t("common.defaultValue", { value: d }) : undefined}
      />
    </Field>
  );
}

function EnumField({
  path,
  label,
  optionKey,
  doc,
  set,
  defLabel,
}: {
  path: Path;
  label: string;
  optionKey: "ordering" | "resumeStrategy" | "missedRun" | "overlap" | "profile" | "network";
  doc: Doc;
  set: SetFn;
  defLabel: (path: Path, label?: (v: string) => string) => string;
}) {
  const { t } = useTranslation();
  const optionLabel = (v: string) => t(`tasks.options.${optionKey}.${v}` as never, { defaultValue: v }) as string;
  return (
    <Field label={label}>
      <Select value={getIn<string>(doc, path) ?? ""} onChange={(e) => set(path, e.target.value || undefined)}>
        <option value="">{defLabel(path, optionLabel)}</option>
        {schemaEnum(path).map((v) => (
          <option key={v} value={v}>
            {optionLabel(v)}
          </option>
        ))}
      </Select>
    </Field>
  );
}

function ListArea({ value, onValue }: { value: unknown; onValue: (v: string[] | undefined) => void }) {
  const [text, setText] = useState(listToLines(value));
  useEffect(() => setText(listToLines(value)), [JSON.stringify(value)]); // keep in sync with YAML edits
  return (
    <TextArea
      rows={3}
      className="mono"
      value={text}
      onChange={(e) => {
        setText(e.target.value);
        const list = linesToList(e.target.value);
        onValue(list.length ? list : undefined);
      }}
    />
  );
}

function NamePicker({
  label,
  entries,
  value,
  onChange,
}: {
  label: string;
  entries: CatalogEntry[];
  value: string[];
  onChange: (v: string[] | undefined) => void;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState("");
  const listId = useMemo(() => `names-${Math.random().toString(36).slice(2)}`, []);
  const add = (name: string) => {
    const n = name.trim();
    if (!n || value.includes(n)) return;
    onChange([...value, n]);
    setDraft("");
  };
  return (
    <Field label={label}>
      <div className="name-picker">
        <div className="name-picker__chips">
          {value.map((name) => {
            const entry = entries.find((e) => e.name === name);
            return (
              <span key={name} className={cx("name-chip", !entry && "name-chip--missing")} title={entry?.description}>
                <span className="mono">{name}</span>
                <span className="name-chip__source">
                  {entry ? (entry.source === "builtin" ? t("tasks.fields.builtin") : t("tasks.fields.user")) : t("tasks.fields.notFound")}
                </span>
                <button type="button" aria-label={t("common.remove")} onClick={() => onChange(value.filter((v) => v !== name))}>
                  <Icon name="x" size={12} />
                </button>
              </span>
            );
          })}
        </div>
        <input
          className="input mono"
          list={listId}
          value={draft}
          placeholder={t("tasks.fields.addName")}
          onChange={(e) => {
            const v = e.target.value;
            if (entries.some((x) => x.name === v)) add(v);
            else setDraft(v);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add(draft);
            }
          }}
          onBlur={() => add(draft)}
        />
        <datalist id={listId}>
          {entries
            .filter((e) => !value.includes(e.name))
            .map((e) => (
              <option key={e.name} value={e.name}>
                {e.source === "builtin" ? t("tasks.fields.builtin") : t("tasks.fields.user")} · {e.harnesses.join(", ")}
              </option>
            ))}
        </datalist>
      </div>
    </Field>
  );
}

function RepositoryList({ rows, onChange, errorPaths }: { rows: Item[]; onChange: (rows: Item[] | undefined) => void; errorPaths: Set<string> }) {
  const { t } = useTranslation();
  const base = "/spec/githubIssueImplementation/repositories";
  const update = (i: number, key: string, v: string) => onChange(rows.map((r, j) => (j === i ? cleanRow({ ...r, [key]: v }) : r)));
  return (
    <div className="row-list">
      <div className="row-list__head grid-repo">
        <span>{t("tasks.fields.repository")}</span>
        <span>{t("tasks.fields.localPath")}</span>
        <span>{t("tasks.fields.baseBranch")}</span>
        <span />
      </div>
      {rows.map((row, i) => (
        <div className="grid-repo" key={i}>
          <TextInput
            className={cx("mono", errorPaths.has(`${base}/${i}/name`) && "input--error")}
            placeholder="owner/repo"
            value={(row.name as string) ?? ""}
            onChange={(e) => update(i, "name", e.target.value)}
          />
          <TextInput
            className={cx("mono", errorPaths.has(`${base}/${i}/localPath`) && "input--error")}
            placeholder="${PROJECTS_ROOT}/repo"
            value={(row.localPath as string) ?? ""}
            onChange={(e) => update(i, "localPath", e.target.value)}
          />
          <TextInput
            className="mono"
            placeholder={String(schemaDefault(["spec", "githubIssueImplementation", "repositories", 0, "baseBranch"]) ?? "main")}
            value={(row.baseBranch as string) ?? ""}
            onChange={(e) => update(i, "baseBranch", e.target.value)}
          />
          <IconButton icon="trash" label={t("common.remove")} onClick={() => onChange(rows.filter((_, j) => j !== i))} />
        </div>
      ))}
      <Button size="sm" icon="plus" onClick={() => onChange([...rows, { name: "", localPath: "", baseBranch: "main" }])}>
        {t("tasks.fields.addRepository")}
      </Button>
    </div>
  );
}

function HarnessProfile({
  value,
  onChange,
  harnesses,
  required,
  onRemove,
}: {
  value: Item;
  onChange: (v: Item | undefined) => void;
  harnesses: Harness[];
  required?: boolean;
  onRemove?: () => void;
}) {
  const { t } = useTranslation();
  const provider = (value.provider as string) ?? (required ? "claude-code" : "");
  const harness = harnesses.find((h) => h.id === provider);
  const model = (value.model as string) ?? "";
  const modelInfo = harness?.models.find((m) => m.id === model);
  const efforts = modelInfo?.efforts?.length ? modelInfo.efforts : harness?.efforts ?? [];
  const update = (key: string, v: string) => onChange(cleanRow({ ...value, provider, [key]: v }));
  const modelListId = `models-${provider}`;

  return (
    <div className="grid-profile">
      <Field label={t("tasks.fields.provider")}>
        <Select value={provider} onChange={(e) => onChange(cleanRow({ provider: e.target.value }))}>
          {HARNESS_PROVIDERS.map((p) => {
            const h = harnesses.find((x) => x.id === p);
            return (
              <option key={p} value={p}>
                {t(`harness.names.${p}` as never, { defaultValue: p }) as string}
                {h ? ` — ${t(`harness.readiness.${h.readiness}`)}` : ""}
              </option>
            );
          })}
        </Select>
      </Field>
      <Field label={t("tasks.fields.model")}>
        <input
          className="input mono"
          list={modelListId}
          value={model}
          placeholder={t("tasks.fields.harnessDefault")}
          onChange={(e) => update("model", e.target.value)}
        />
        <datalist id={modelListId}>
          {(harness?.models ?? []).map((m) => (
            <option key={m.id} value={m.id} />
          ))}
        </datalist>
      </Field>
      <Field label={t("tasks.fields.effort")}>
        <Select value={(value.effort as string) ?? ""} onChange={(e) => update("effort", e.target.value)}>
          <option value="">{t("tasks.fields.harnessDefault")}</option>
          {efforts.map((ef) => (
            <option key={ef} value={ef}>
              {ef}
            </option>
          ))}
          {value.effort && !efforts.includes(value.effort as string) ? <option value={value.effort as string}>{value.effort as string}</option> : null}
        </Select>
      </Field>
      {onRemove ? <IconButton icon="trash" label={t("common.remove")} onClick={onRemove} className="grid-profile__remove" /> : <span />}
    </div>
  );
}

function FallbackList({ rows, onChange, harnesses }: { rows: Item[]; onChange: (rows: Item[] | undefined) => void; harnesses: Harness[] }) {
  const { t } = useTranslation();
  return (
    <div className="row-list">
      {rows.map((row, i) => (
        <HarnessProfile
          key={i}
          value={row}
          harnesses={harnesses}
          required
          onChange={(v) => onChange(rows.map((r, j) => (j === i ? v ?? { provider: "codex" } : r)))}
          onRemove={() => onChange(rows.filter((_, j) => j !== i))}
        />
      ))}
      <Button size="sm" icon="plus" onClick={() => onChange([...rows, { provider: "codex" }])}>
        {t("tasks.fields.addFallback")}
      </Button>
    </div>
  );
}

function ScheduleFields({
  doc,
  onChange,
  set,
  defLabel,
  locale,
  errorPaths,
}: {
  doc: Doc;
  onChange: (doc: Doc) => void;
  set: SetFn;
  defLabel: (path: Path, label?: (v: string) => string) => string;
  locale: string;
  errorPaths: Set<string>;
}) {
  const { t } = useTranslation();
  const kind = scheduleKind(doc);
  const cron = getIn<string>(doc, ["spec", "schedule", "cron"]);
  const interval = getIn<string>(doc, ["spec", "schedule", "interval"]);
  const timezone = getIn<string>(doc, ["spec", "schedule", "timezone"]);
  const zones = useMemo(timeZones, []);
  const zone = systemZone();
  const request = useDebounced(JSON.stringify({ cron, interval, timezone }), 400);
  const [preview, setPreview] = useState<SchedulePreview | null>(null);

  useEffect(() => {
    const s = JSON.parse(request) as { cron?: string; interval?: string; timezone?: string };
    if (!s.cron && !s.interval) {
      setPreview(null);
      return;
    }
    let cancelled = false;
    api
      .previewSchedule(s, 5)
      .then((p) => !cancelled && setPreview(p))
      .catch((e: Error) => !cancelled && setPreview({ occurrences: [], error: e.message }));
    return () => {
      cancelled = true;
    };
  }, [request]);

  return (
    <>
      <div className="segmented" role="radiogroup" aria-label={t("tasks.fields.scheduleKind")}>
        {(["none", "cron", "interval"] as const).map((k) => (
          <button
            key={k}
            type="button"
            role="radio"
            aria-checked={kind === k}
            className={cx("segmented__item", kind === k && "segmented__item--active")}
            onClick={() => onChange(setScheduleKind(doc, k))}
          >
            {k === "none" ? t("tasks.fields.scheduleNone") : k === "cron" ? t("tasks.fields.scheduleCron") : t("tasks.fields.scheduleInterval")}
          </button>
        ))}
      </div>
      <div className="grid-2">
        {kind === "cron" ? (
          <Field label={t("tasks.fields.cron")} error={errorPaths.has("/spec/schedule/cron") ? t("common.error") : undefined}>
            <TextInput className="mono" value={cron ?? ""} onChange={(e) => set(["spec", "schedule", "cron"], e.target.value)} placeholder="0 9 * * 1-5" />
          </Field>
        ) : null}
        {kind === "interval" ? (
          <Field label={t("tasks.fields.interval")} error={errorPaths.has("/spec/schedule/interval") ? t("common.error") : undefined}>
            <TextInput className="mono" value={interval ?? ""} onChange={(e) => set(["spec", "schedule", "interval"], e.target.value)} placeholder="30m" />
          </Field>
        ) : null}
        {kind !== "none" ? (
          <Field label={t("tasks.fields.timezone")} hint={t("tasks.fields.timezoneHint", { zone })}>
            <input
              className="input mono"
              list="hf-timezones"
              value={timezone ?? ""}
              placeholder={zone}
              onChange={(e) => set(["spec", "schedule", "timezone"], e.target.value)}
            />
            <datalist id="hf-timezones">
              {zones.map((z) => (
                <option key={z} value={z} />
              ))}
            </datalist>
          </Field>
        ) : null}
      </div>
      <div className="grid-2">
        <EnumField path={["spec", "schedule", "missedRun", "policy"]} label={t("tasks.fields.missedRun")} optionKey="missedRun" doc={doc} set={set} defLabel={defLabel} />
        <EnumField path={["spec", "concurrency", "overlappingRuns"]} label={t("tasks.fields.overlap")} optionKey="overlap" doc={doc} set={set} defLabel={defLabel} />
      </div>
      <div className="preview">
        <span className="preview__label">{t("tasks.fields.preview")}</span>
        {kind === "none" ? <span className="muted">{t("tasks.fields.previewEmpty")}</span> : null}
        {preview?.error ? (
          <span className="tone-danger mono">
            {daemonText({ message: preview.error, messageKey: preview.errorKey, params: preview.errorParams })}
          </span>
        ) : null}
        <ul className="preview__list">
          {(preview?.occurrences ?? []).map((o) => (
            <li key={o} className="mono">
              {formatDateTime(o, locale, preview?.timezone ?? (timezone || undefined))}
            </li>
          ))}
        </ul>
      </div>
    </>
  );
}
