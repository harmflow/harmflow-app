// Each task type's glyph, the icon and the tone the New task dialog shows it with, and the only place that decides them:
// the dialog, onboarding's first task, the Tasks list, the execution card, and the execution detail all draw a type's
// glyph from here.
import type { IconName } from "../components/Icon";

/** The tones a type's glyph takes, each a CSS modifier of its own (`type-glyph--<tone>` in `app.css`). */
export type TypeTone = "blue" | "violet";

export interface TypeGlyph {
  icon: IconName;
  tone: TypeTone;
}

const GLYPHS: Record<string, TypeGlyph> = {
  github: { icon: "code", tone: "blue" },
  general: { icon: "sparkle", tone: "violet" },
  script: { icon: "terminal", tone: "blue" },
};

/** The glyph of a type the mapping does not know: the dot `Icon` draws for a name it does not know. */
export const UNKNOWN_TYPE_GLYPH: TypeGlyph = { icon: "dot", tone: "blue" };

/** The icon and tone of a task type, the same in every view. */
export function typeGlyph(type: string | undefined): TypeGlyph {
  return type !== undefined && Object.hasOwn(GLYPHS, type) ? GLYPHS[type] : UNKNOWN_TYPE_GLYPH;
}

/** The class that gives a glyph its type's tone. */
export function toneClass(tone: TypeTone): string {
  return `type-glyph--${tone}`;
}
