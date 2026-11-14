export function Gauge({ label, value, max, text, note }: { label: string; value?: number; max?: number; text: string; note?: string }) {
  const ratio = value !== undefined && max ? Math.min(1, Math.max(0, value / max)) : 0;
  return (
    <div className="gauge">
      <span className="gauge__label">{label}</span>
      <span className="gauge__bar" role="meter" aria-label={label} aria-valuenow={Math.round(ratio * 100)} aria-valuemin={0} aria-valuemax={100}>
        <span className="gauge__fill" style={{ width: `${ratio * 100}%` }} />
      </span>
      <span className="gauge__text">{text}</span>
      {note ? <span className="gauge__note">{note}</span> : null}
    </div>
  );
}
