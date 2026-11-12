import { toneColor, type Tone } from "../../design/theme";

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
