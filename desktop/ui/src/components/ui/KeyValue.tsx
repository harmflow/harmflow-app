import type { ReactNode } from "react";

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
