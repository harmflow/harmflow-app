// PROTOTYPE — throwaway. The floating bar that switches a prototype's variants (← →) and stub scenarios (⇧← ⇧→),
// kept in the URL so a variant is shareable and survives a reload. Rendered in dev builds only.
import { useCallback, useEffect, useState } from "react";
import "./prototype.css";

export interface SwitcherOption {
  key: string;
  name: string;
}

export function useSearchParam(name: string, fallback: string): [string, (value: string) => void] {
  const read = () => new URLSearchParams(window.location.search).get(name) ?? fallback;
  const [value, setValue] = useState(read);
  useEffect(() => {
    const onPop = () => setValue(read());
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  });
  const update = useCallback(
    (next: string) => {
      const params = new URLSearchParams(window.location.search);
      params.set(name, next);
      window.history.replaceState(null, "", `${window.location.pathname}?${params.toString()}${window.location.hash}`);
      setValue(next);
    },
    [name],
  );
  return [value, update];
}

function step(options: SwitcherOption[], current: string, by: number): string {
  const i = Math.max(0, options.findIndex((o) => o.key === current));
  return options[(i + by + options.length) % options.length].key;
}

export function PrototypeSwitcher({
  variants,
  variant,
  onVariant,
  scenarios,
  scenario,
  onScenario,
  state,
}: {
  variants: SwitcherOption[];
  variant: string;
  onVariant: (key: string) => void;
  scenarios: SwitcherOption[];
  scenario: string;
  onScenario: (key: string) => void;
  state: unknown;
}) {
  const [showState, setShowState] = useState(false);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (target && (target.closest("input, textarea, select, [contenteditable]") || target.isContentEditable)) return;
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      const by = e.key === "ArrowLeft" ? -1 : 1;
      e.preventDefault();
      if (e.shiftKey) onScenario(step(scenarios, scenario, by));
      else onVariant(step(variants, variant, by));
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [variants, variant, onVariant, scenarios, scenario, onScenario]);

  if (!import.meta.env.DEV) return null;
  const current = variants.find((v) => v.key === variant) ?? variants[0];

  return (
    <div className="proto-switcher" role="toolbar" aria-label="Prototype variants">
      {showState ? <pre className="proto-switcher__state">{JSON.stringify(state, null, 2)}</pre> : null}
      <div className="proto-switcher__bar">
        <span className="proto-switcher__tag">PROTOTYPE</span>
        <button type="button" onClick={() => onVariant(step(variants, variant, -1))} aria-label="Previous variant">
          ←
        </button>
        <span className="proto-switcher__label">
          <strong>{current.key}</strong> {current.name}
        </span>
        <button type="button" onClick={() => onVariant(step(variants, variant, 1))} aria-label="Next variant">
          →
        </button>
        <span className="proto-switcher__sep" />
        <label className="proto-switcher__scenario">
          scenario
          <select value={scenario} onChange={(e) => onScenario(e.target.value)}>
            {scenarios.map((s) => (
              <option key={s.key} value={s.key}>
                {s.name}
              </option>
            ))}
          </select>
        </label>
        <button type="button" className={showState ? "is-on" : undefined} onClick={() => setShowState((v) => !v)}>
          {"{ }"} data
        </button>
      </div>
    </div>
  );
}
