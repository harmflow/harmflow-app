// Typed handles on the CSS custom properties of tokens.css, for the few places that style from TypeScript
// (inline SVG charts, gauges). Values stay in tokens.css so a theme replaces them in one place (FS-012 FR-007).

export const color = {
  cyan: "var(--hf-color-cyan)",
  teal: "var(--hf-color-teal)",
  violet: "var(--hf-color-violet)",
  magenta: "var(--hf-color-magenta)",
  blue: "var(--hf-color-blue)",
  success: "var(--hf-color-success)",
  warning: "var(--hf-color-warning)",
  danger: "var(--hf-color-danger)",
  neutral: "var(--hf-color-neutral)",
  text: "var(--hf-color-text)",
  textMuted: "var(--hf-color-text-muted)",
  textFaint: "var(--hf-color-text-faint)",
  border: "var(--hf-color-border)",
  claude: "var(--hf-color-claude)",
  codex: "var(--hf-color-codex)",
} as const;

export type Tone = "cyan" | "violet" | "magenta" | "blue" | "success" | "warning" | "danger" | "neutral";

export const toneColor: Record<Tone, string> = {
  cyan: color.cyan,
  violet: color.violet,
  magenta: color.magenta,
  blue: color.blue,
  success: color.success,
  warning: color.warning,
  danger: color.danger,
  neutral: color.neutral,
};

export const font = {
  sans: "var(--hf-font-sans)",
  mono: "var(--hf-font-mono)",
} as const;
