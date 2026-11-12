import type { ReactNode } from "react";

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
