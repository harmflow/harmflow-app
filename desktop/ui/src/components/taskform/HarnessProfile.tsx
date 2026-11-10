// The AI tool a task runs on, its model and effort, and the candidates it falls back to.
import { useTranslation } from "react-i18next";
import type { Harness } from "../../api/types";
import { daemonText } from "../../lib/daemonText";
import { HARNESS_PROVIDERS } from "../../lib/schema";
import { Button, Field, IconButton, Select, Toggle, cx } from "../ui";
import { cleanRow, type Item } from "./formModel";

export function HarnessProfile({
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
  // The installations of the chosen harness, one per home; the Model and Effort lists are those of the one chosen, or
  // of the one in use while none is.
  const homes = harness?.homes ?? [];
  const installation = (value.installation as string) ?? "";
  const installed = homes.find((h) => h.installationId === installation) ?? harness;
  const model = (value.model as string) ?? "";
  const modelInfo = installed?.models.find((m) => m.id === model);
  const efforts = modelInfo?.efforts?.length ? modelInfo.efforts : installed?.efforts ?? [];
  const update = (key: string, v: string) => onChange(cleanRow({ ...value, provider, [key]: v }));
  // An identifier holds only letters, digits, - and _, so a home's path becomes those.
  const modelListId = `models-${provider}-${installation.replace(/[^A-Za-z0-9_-]/g, "_")}`;
  // Only Codex can keep its session. On, the default, removes the key, so the YAML writes it only when it is off.
  const keepsSessions = provider === "codex";
  const ephemeral = value.ephemeral !== false;

  return (
    <div className={cx("grid-profile", homes.length > 0 && "grid-profile--installation", keepsSessions && "grid-profile--session")}>
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
      {homes.length > 0 ? (
        <Field label={t("tasks.fields.installation")}>
          <Select className="mono" value={installation} onChange={(e) => update("installation", e.target.value)}>
            <option value="">{t("tasks.fields.installationInUse", { installation: harness?.installationId ?? "" })}</option>
            {homes.map((h) => (
              <option key={h.installationId} value={h.installationId}>
                {h.installationId} — {t(`harness.readiness.${h.readiness}`)}
              </option>
            ))}
            {installation && !homes.some((h) => h.installationId === installation) ? (
              <option value={installation}>{t("tasks.fields.installationMissing", { installation })}</option>
            ) : null}
          </Select>
        </Field>
      ) : null}
      <Field label={t("tasks.fields.model")}>
        <input
          className="input mono"
          list={modelListId}
          value={model}
          placeholder={t("tasks.fields.harnessDefault")}
          onChange={(e) => update("model", e.target.value)}
        />
        <datalist id={modelListId}>
          {(installed?.models ?? []).map((m) => (
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
      {keepsSessions ? (
        <div className="field field--toggle">
          <Toggle
            checked={ephemeral}
            onChange={(on) => onChange(cleanRow({ ...value, provider, ephemeral: on ? undefined : false }))}
            label={t("tasks.fields.ephemeral")}
          />
        </div>
      ) : null}
      {onRemove ? <IconButton icon="trash" label={t("common.remove")} onClick={onRemove} className="grid-profile__remove" /> : <span />}
      {keepsSessions && !ephemeral ? (
        <span className="field__hint grid-profile__note">
          {daemonText({ message: "", messageKey: "session_kept_note", params: { harness: t("harness.names.codex") } })}
        </span>
      ) : null}
    </div>
  );
}

export function FallbackList({ rows, onChange, harnesses }: { rows: Item[]; onChange: (rows: Item[] | undefined) => void; harnesses: Harness[] }) {
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
