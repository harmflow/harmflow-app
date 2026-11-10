// Small building blocks shared by every screen.
import {
  useEffect,
  useId,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import { useTranslation } from "react-i18next";
import type { ExecutionStatus } from "../api/types";
import { toneColor, type Tone } from "../design/theme";
import { formatElapsed } from "../lib/format";
import { useNow } from "../lib/hooks";
import { matches } from "../lib/shortcuts";
import { Icon, type IconName } from "./Icon";

export function cx(...classes: Array<string | false | null | undefined>): string {
  return classes.filter(Boolean).join(" ");
}

/** The time since start, ticking every second until end is set: only this label renders again, not its screen. */
export function Elapsed({ start, end, locale }: { start?: string | null; end?: string | null; locale: string }) {
  const now = useNow(end ? null : 1000);
  return <>{formatElapsed(start, end, locale, now)}</>;
}

/** `link` draws a text-styled action: no padding, border, or background, the link color, and an underline on hover. */
type ButtonVariant = "primary" | "secondary" | "ghost" | "danger" | "danger-outline" | "link";

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

/** A short label in a tone. Given `onRemove`, it ends in a remove button named `removeLabel`. */
export function Chip({
  tone = "neutral",
  icon,
  children,
  title,
  onRemove,
  removeLabel,
}: {
  tone?: Tone;
  icon?: IconName;
  children: ReactNode;
  title?: string;
  onRemove?: () => void;
  removeLabel?: string;
}) {
  return (
    <span className={cx("chip", `chip--${tone}`, onRemove && "chip--removable")} title={title}>
      {icon ? <Icon name={icon} size={12} /> : null}
      {children}
      {onRemove ? (
        <button type="button" className="chip__remove" aria-label={removeLabel} onClick={onRemove}>
          <Icon name="x" size={12} />
        </button>
      ) : null}
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

export interface SegmentOption<V extends string | number> {
  value: V;
  label: ReactNode;
  /** The option's accessible name, when its label is too short to say it (a weekday's "Mon"). */
  ariaLabel?: string;
  disabled?: boolean;
}

/** What names a segmented control's group: a label of its own, or the id of a caption elsewhere. */
type SegmentedName = { "aria-label"?: string; "aria-labelledby"?: string };

function SegmentedItem<V extends string | number>({ option, on, radio, onClick }: { option: SegmentOption<V>; on: boolean; radio: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      role={radio ? "radio" : undefined}
      aria-checked={radio ? on : undefined}
      aria-pressed={radio ? undefined : on}
      aria-label={option.ariaLabel}
      disabled={option.disabled}
      className={cx("segmented__item", on && "segmented__item--active")}
      onClick={onClick}
    >
      {option.label}
    </button>
  );
}

/** A single choice drawn as a row of buttons: a radio group, each option a radio checked while chosen. */
export function SegmentedControl<V extends string | number>({
  value,
  options,
  onChange,
  ...name
}: { value: V; options: SegmentOption<V>[]; onChange: (v: V) => void } & SegmentedName) {
  return (
    <div className="segmented" role="radiogroup" {...name}>
      {options.map((o) => (
        <SegmentedItem key={o.value} option={o} on={o.value === value} radio onClick={() => onChange(o.value)} />
      ))}
    </div>
  );
}

/** Several choices drawn as a row of toggle buttons, each pressed while chosen; a click adds or removes its value. */
export function SegmentedMultiSelect<V extends string | number>({
  values,
  options,
  onChange,
  ...name
}: { values: V[]; options: SegmentOption<V>[]; onChange: (values: V[]) => void } & SegmentedName) {
  return (
    <div className="segmented" role="group" {...name}>
      {options.map((o) => {
        const on = values.includes(o.value);
        return (
          <SegmentedItem
            key={o.value}
            option={o}
            on={on}
            radio={false}
            onClick={() => onChange(on ? values.filter((v) => v !== o.value) : [...values, o.value])}
          />
        );
      })}
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

/**
 * A field whose control is a group, such as a row of checkboxes. Its caption names the group, which sits in a
 * container of `className`, and labels none of its controls: a `<label>` would name, and toggle, the first of them.
 */
export function FieldGroup({ label, hint, className, children }: { label: ReactNode; hint?: ReactNode; className?: string; children: ReactNode }) {
  const id = useId();
  return (
    <div className="field">
      <span className="field__label" id={id}>
        {label}
      </span>
      <div className={className} role="group" aria-labelledby={id}>
        {children}
      </div>
      {hint ? <span className="field__hint">{hint}</span> : null}
    </div>
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

/** A checkbox. `compact` draws it at the size of a toolbar's small monospace text, as the event timeline's tools are. */
export function Checkbox({
  checked,
  onChange,
  label,
  disabled,
  compact,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: ReactNode;
  disabled?: boolean;
  compact?: boolean;
}) {
  return (
    <label className={cx("checkbox", compact && "checkbox--compact", disabled && "checkbox--disabled")}>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="checkbox__box" aria-hidden="true">
        <Icon name="check" size={compact ? 10 : 12} />
      </span>
      <span>{label}</span>
    </label>
  );
}

/** What every overlay shares: a modal dialog that closes on the close shortcut, and on a click on itself, outside its card. */
function Overlay({ className, label, onClose, children }: { className: string; label?: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => matches(e, "close") && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className={className} role="dialog" aria-modal="true" aria-label={label} onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      {children}
    </div>
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
  return (
    <Overlay className="modal" onClose={onClose}>
      <div className={cx("modal__card", wide && "modal__card--wide")}>
        <header className="modal__header">
          <h2>{title}</h2>
          <IconButton icon="x" label={t("common.close")} onClick={onClose} />
        </header>
        <div className="modal__body">{children}</div>
        {footer ? <footer className="modal__footer">{footer}</footer> : null}
      </div>
    </Overlay>
  );
}

/** A panel at the window's right edge, over the screen, that closes as `Modal` does. */
export function Drawer({ label, onClose, children }: { label: string; onClose: () => void; children: ReactNode }) {
  const { t } = useTranslation();
  return (
    <Overlay className="drawer" label={label} onClose={onClose}>
      <div className="drawer__card">
        <div className="drawer__close">
          <IconButton icon="x" label={t("common.close")} onClick={onClose} />
        </div>
        {children}
      </div>
    </Overlay>
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

/** A column of list rows, each in its `<li>`; `loose` spaces cards apart. */
export function ListRows({ loose, className, children }: { loose?: boolean; className?: string; children: ReactNode }) {
  return <ul className={cx("list-rows", loose && "list-rows--loose", className)}>{children}</ul>;
}

/**
 * A row of a list: a leading mark or icon, a title over a secondary line and an optional third one, then what sits
 * on its right — `aside` as it is drawn (a chip, a harness label), and `trailing` in the muted small text of a time.
 * With `onClick` it is a button; without, it shows the same row and does nothing. A `card` row has a border and a
 * sunken ground, as the Executions screen's aside draws them; a `tone` marks its left edge, as a notice's severity.
 */
export function ListRow({
  leading,
  icon,
  title,
  secondary,
  wrap,
  meta,
  aside,
  trailing,
  onClick,
  card,
  tone,
  alignTop,
}: {
  leading?: ReactNode;
  icon?: IconName;
  title: ReactNode;
  secondary?: ReactNode;
  /** The secondary line wraps, breaking a long path anywhere, where it is cut to one line otherwise. */
  wrap?: boolean;
  meta?: ReactNode;
  aside?: ReactNode;
  trailing?: ReactNode;
  onClick?: () => void;
  card?: boolean;
  tone?: "info" | "warning" | "error";
  alignTop?: boolean;
}) {
  const className = cx("list-row", card && "list-row--card", tone && `list-row--${tone}`, alignTop && "list-row--top");
  const content = (
    <>
      {icon ? (
        <span className="list-row__icon">
          <Icon name={icon} size={18} />
        </span>
      ) : (
        leading
      )}
      <span className="list-row__text">
        <span className="list-row__title">{title}</span>
        {secondary !== undefined ? <span className={cx("list-row__sub", wrap && "list-row__sub--wrap")}>{secondary}</span> : null}
        {meta !== undefined ? <span className="list-row__meta">{meta}</span> : null}
      </span>
      {aside}
      {trailing !== undefined ? <span className="list-row__trail">{trailing}</span> : null}
    </>
  );
  return onClick ? (
    <button type="button" className={className} onClick={onClick}>
      {content}
    </button>
  ) : (
    <div className={className}>{content}</div>
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
