// Small building blocks shared by every screen.
import {
  useEffect,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import { useTranslation } from "react-i18next";
import type { ExecutionStatus } from "../api/types";
import { toneColor, type Tone } from "../design/tokens";
import { Icon, type IconName } from "./Icon";

export function cx(...classes: Array<string | false | null | undefined>): string {
  return classes.filter(Boolean).join(" ");
}

type ButtonVariant = "primary" | "secondary" | "ghost" | "danger" | "danger-outline";

export function Button({
  variant = "secondary",
  icon,
  size = "md",
  className,
  children,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; icon?: IconName; size?: "sm" | "md" | "lg" }) {
  return (
    <button type="button" className={cx("btn", `btn--${variant}`, `btn--${size}`, className)} {...rest}>
      {icon ? <Icon name={icon} size={size === "sm" ? 14 : size === "lg" ? 20 : 16} /> : null}
      {children ? <span>{children}</span> : null}
    </button>
  );
}

export function IconButton({
  icon,
  label,
  className,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { icon: IconName; label: string }) {
  return (
    <button type="button" className={cx("icon-btn", className)} aria-label={label} title={label} {...rest}>
      <Icon name={icon} size={18} />
    </button>
  );
}

export function statusTone(status: ExecutionStatus | "scheduled"): Tone {
  switch (status) {
    case "preparing":
    case "running":
    case "testing":
    case "publishing":
      return "success";
    case "switching_harness":
      return "magenta";
    case "queued":
    case "scheduled":
      return "blue";
    case "completed":
      return "success";
    case "failed":
      return "danger";
    case "needs_attention":
    case "interrupted":
      return "warning";
    case "cancelled":
    case "no_work":
    default:
      return "neutral";
  }
}

function statusIcon(status: ExecutionStatus | "scheduled"): IconName {
  switch (status) {
    case "completed":
      return "check";
    case "failed":
      return "x";
    case "cancelled":
      return "stop";
    case "needs_attention":
    case "interrupted":
      return "alert";
    case "queued":
    case "scheduled":
      return "clock";
    case "no_work":
      return "circle";
    case "switching_harness":
      return "switch";
    default:
      return "dot";
  }
}

/** A status as text and icon, never color alone. */
export function StatusChip({ status, compact }: { status: ExecutionStatus | "scheduled"; compact?: boolean }) {
  const { t } = useTranslation();
  const tone = statusTone(status);
  const active = ["preparing", "running", "testing", "publishing", "switching_harness"].includes(status);
  return (
    <span className={cx("chip", `chip--${tone}`, active && "chip--live", compact && "chip--compact")}>
      {active ? <span className="chip__pulse" aria-hidden="true" /> : <Icon name={statusIcon(status)} size={12} />}
      {t(`status.${status}`)}
    </span>
  );
}

/** A large round status mark for history rows. */
export function StatusMark({ status }: { status: ExecutionStatus }) {
  const { t } = useTranslation();
  const tone = statusTone(status);
  return (
    <span className={cx("status-mark", `status-mark--${tone}`)} title={t(`status.${status}`)}>
      <Icon name={statusIcon(status)} size={16} />
      <span className="sr-only">{t(`status.${status}`)}</span>
    </span>
  );
}

export function Chip({ tone = "neutral", icon, children, title }: { tone?: Tone; icon?: IconName; children: ReactNode; title?: string }) {
  return (
    <span className={cx("chip", `chip--${tone}`)} title={title}>
      {icon ? <Icon name={icon} size={12} /> : null}
      {children}
    </span>
  );
}

export function Panel({
  title,
  icon,
  actions,
  className,
  children,
  bodyClassName,
}: {
  title?: ReactNode;
  icon?: IconName;
  actions?: ReactNode;
  className?: string;
  bodyClassName?: string;
  children: ReactNode;
}) {
  return (
    <section className={cx("panel", className)}>
      {title ? (
        <header className="panel__header">
          <h2 className="panel__title">
            {icon ? <Icon name={icon} size={18} className="panel__icon" /> : null}
            {title}
          </h2>
          {actions ? <div className="panel__actions">{actions}</div> : null}
        </header>
      ) : null}
      <div className={cx("panel__body", bodyClassName)}>{children}</div>
    </section>
  );
}

export function SectionTitle({ children, count, actions }: { children: ReactNode; count?: number; actions?: ReactNode }) {
  return (
    <div className="section-title">
      <h2>
        <span className="section-title__prompt">&gt;_</span>
        {children}
        {count !== undefined ? <span className="section-title__count">({count})</span> : null}
      </h2>
      {actions}
    </div>
  );
}

export function EmptyState({ icon = "sparkle", title, hint, action }: { icon?: IconName; title: string; hint?: string; action?: ReactNode }) {
  return (
    <div className="empty">
      <Icon name={icon} size={26} className="empty__icon" />
      <p className="empty__title">{title}</p>
      {hint ? <p className="empty__hint">{hint}</p> : null}
      {action}
    </div>
  );
}

/** Bars of recent activity for a counter card. */
export function Sparkline({ values, tone }: { values: number[]; tone: Tone }) {
  const max = Math.max(1, ...values);
  const w = 54;
  const h = 26;
  const bar = values.length ? w / values.length : w;
  return (
    <svg className="sparkline" width={w} height={h} viewBox={`0 0 ${w} ${h}`} aria-hidden="true">
      {values.map((v, i) => {
        const bh = Math.max(2, (v / max) * (h - 2));
        return (
          <rect
            key={i}
            x={i * bar + 1}
            y={h - bh}
            width={Math.max(1.5, bar - 2)}
            height={bh}
            rx={1}
            fill={toneColor[tone]}
            opacity={0.35 + (0.65 * (i + 1)) / Math.max(1, values.length)}
          />
        );
      })}
    </svg>
  );
}

export function Gauge({ label, value, max, text }: { label: string; value?: number; max?: number; text: string }) {
  const ratio = value !== undefined && max ? Math.min(1, Math.max(0, value / max)) : 0;
  return (
    <div className="gauge">
      <span className="gauge__label">{label}</span>
      <span className="gauge__bar" role="meter" aria-label={label} aria-valuenow={Math.round(ratio * 100)} aria-valuemin={0} aria-valuemax={100}>
        <span className="gauge__fill" style={{ width: `${ratio * 100}%` }} />
      </span>
      <span className="gauge__text">{text}</span>
    </div>
  );
}

/** An indeterminate activity line: work is happening, with no invented percentage. */
export function ScanBar({ active, tone = "cyan" }: { active: boolean; tone?: Tone }) {
  return (
    <div className={cx("scanbar", active && "scanbar--active", `scanbar--${tone}`)} aria-hidden="true">
      <span />
    </div>
  );
}

export interface TabDef<K extends string> {
  key: K;
  label: string;
  icon?: IconName;
  count?: number;
}

export function Tabs<K extends string>({ tabs, value, onChange }: { tabs: TabDef<K>[]; value: K; onChange: (k: K) => void }) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((tab) => (
        <button
          key={tab.key}
          type="button"
          role="tab"
          aria-selected={tab.key === value}
          className={cx("tabs__tab", tab.key === value && "tabs__tab--active")}
          onClick={() => onChange(tab.key)}
        >
          {tab.icon ? <Icon name={tab.icon} size={15} /> : null}
          {tab.label}
          {tab.count !== undefined ? <span className="tabs__count">({tab.count})</span> : null}
        </button>
      ))}
    </div>
  );
}

export function CopyButton({ text }: { text: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(id);
  }, [copied]);
  return (
    <IconButton
      icon={copied ? "check" : "copy"}
      label={copied ? t("common.copied") : t("common.copy")}
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(() => setCopied(true));
      }}
    />
  );
}

export function Field({
  label,
  hint,
  error,
  children,
  inline,
}: {
  label: ReactNode;
  hint?: ReactNode;
  error?: string;
  children: ReactNode;
  inline?: boolean;
}) {
  return (
    <label className={cx("field", inline && "field--inline", error && "field--error")}>
      <span className="field__label">{label}</span>
      {children}
      {error ? <span className="field__error">{error}</span> : hint ? <span className="field__hint">{hint}</span> : null}
    </label>
  );
}

export function TextInput(props: InputHTMLAttributes<HTMLInputElement>) {
  return <input type="text" {...props} className={cx("input", props.className)} />;
}

export function NumberInput({
  value,
  onValue,
  ...rest
}: Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange"> & { value: number | undefined; onValue: (v: number | undefined) => void }) {
  return (
    <input
      type="number"
      {...rest}
      className={cx("input", "input--number", rest.className)}
      value={value ?? ""}
      onChange={(e) => {
        const raw = e.target.value;
        onValue(raw === "" ? undefined : Number(raw));
      }}
    />
  );
}

export function TextArea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={cx("input", "input--area", props.className)} />;
}

export function Select({ children, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <span className="select">
      <select {...props} className={cx("input", "select__control", props.className)}>
        {children}
      </select>
      <Icon name="chevronDown" size={14} className="select__chevron" />
    </span>
  );
}

/** A switch. Without a label of its own, a `<label htmlFor={id}>` elsewhere names it, as a settings row's does. */
export function Toggle({
  id,
  checked,
  onChange,
  label,
  disabled,
}: {
  id?: string;
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: ReactNode;
  disabled?: boolean;
}) {
  return (
    <label className={cx("toggle", disabled && "toggle--disabled")}>
      <input id={id} type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="toggle__track" aria-hidden="true">
        <span className="toggle__thumb" />
      </span>
      {label ? <span className="toggle__label">{label}</span> : null}
    </label>
  );
}

export function Checkbox({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: ReactNode; disabled?: boolean }) {
  return (
    <label className={cx("checkbox", disabled && "checkbox--disabled")}>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="checkbox__box" aria-hidden="true">
        <Icon name="check" size={12} />
      </span>
      <span>{label}</span>
    </label>
  );
}

export function Modal({
  title,
  onClose,
  children,
  footer,
  wide,
}: {
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  const { t } = useTranslation();
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="modal" role="dialog" aria-modal="true" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={cx("modal__card", wide && "modal__card--wide")}>
        <header className="modal__header">
          <h2>{title}</h2>
          <IconButton icon="x" label={t("common.close")} onClick={onClose} />
        </header>
        <div className="modal__body">{children}</div>
        {footer ? <footer className="modal__footer">{footer}</footer> : null}
      </div>
    </div>
  );
}

export function Banner({ tone, icon, children, actions }: { tone: Tone; icon?: IconName; children: ReactNode; actions?: ReactNode }) {
  return (
    <div className={cx("banner", `banner--${tone}`)} role={tone === "danger" || tone === "warning" ? "alert" : "status"}>
      {icon ? <Icon name={icon} size={18} /> : null}
      <div className="banner__text">{children}</div>
      {actions ? <div className="banner__actions">{actions}</div> : null}
    </div>
  );
}

export function KeyValue({ items }: { items: Array<[ReactNode, ReactNode]> }) {
  return (
    <dl className="kv">
      {items.map(([k, v], i) => (
        <div className="kv__row" key={i}>
          <dt>{k}</dt>
          <dd>{v ?? "—"}</dd>
        </div>
      ))}
    </dl>
  );
}
